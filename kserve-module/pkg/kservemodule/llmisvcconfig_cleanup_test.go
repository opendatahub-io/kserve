package kservemodule

import (
	"context"
	"errors"
	"testing"

	. "github.com/onsi/gomega"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	k8serr "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestReferencedByNames(t *testing.T) {
	newConfig := func(refs ...map[string]any) *unstructured.Unstructured {
		cfg := &unstructured.Unstructured{Object: map[string]any{}}
		if refs != nil {
			list := make([]any, len(refs))
			for i := range refs {
				list[i] = refs[i]
			}
			_ = unstructured.SetNestedSlice(cfg.Object, list, "status", "referencedBy")
		}
		return cfg
	}

	t.Run("no status", func(t *testing.T) {
		g := NewWithT(t)
		g.Expect(referencedByNames(&unstructured.Unstructured{Object: map[string]any{}})).To(BeEmpty())
	})

	t.Run("namespaced names sorted", func(t *testing.T) {
		g := NewWithT(t)
		cfg := newConfig(
			map[string]any{"name": "svc-b", "namespace": "ns2"},
			map[string]any{"name": "svc-a", "namespace": "ns1"},
		)
		g.Expect(referencedByNames(cfg)).To(Equal([]string{"ns1/svc-a", "ns2/svc-b"}))
	})

	t.Run("skips malformed entries without a name", func(t *testing.T) {
		g := NewWithT(t)
		cfg := newConfig(
			map[string]any{"namespace": "ns1"},             // no name -> skipped
			map[string]any{"name": "", "namespace": "ns2"}, // empty name -> skipped
			map[string]any{"name": "svc-ok", "namespace": "ns3"},
		)
		g.Expect(referencedByNames(cfg)).To(Equal([]string{"ns3/svc-ok"}))
	})
}

func TestReferencedConfigBlockers(t *testing.T) {
	g := NewWithT(t)

	config := func(name string, refs ...map[string]any) unstructured.Unstructured {
		cfg := unstructured.Unstructured{Object: map[string]any{}}
		cfg.SetName(name)
		if refs != nil {
			list := make([]any, len(refs))
			for i := range refs {
				list[i] = refs[i]
			}
			_ = unstructured.SetNestedSlice(cfg.Object, list, "status", "referencedBy")
		}
		return cfg
	}

	configs := []unstructured.Unstructured{
		config("cfg-unused"),
		config("cfg-used", map[string]any{"name": "svc1", "namespace": "ns1"}),
	}

	blockers := referencedConfigBlockers(configs)
	g.Expect(blockers).To(ConsistOf("cfg-used (referenced by ns1/svc1)"))
}

func TestDeleteConfigDeletionWebhook(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)

	webhookKey := types.NamespacedName{Name: llmISVCConfigWebhookName}

	t.Run("deletes the webhook when present", func(t *testing.T) {
		g := NewWithT(t)
		webhook := &admissionregistrationv1.ValidatingWebhookConfiguration{
			ObjectMeta: metav1.ObjectMeta{Name: llmISVCConfigWebhookName},
		}
		cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(webhook).Build()
		r := &KserveModuleReconciler{Client: cli}

		g.Expect(r.deleteConfigDeletionWebhook(context.Background())).To(Succeed())

		err := cli.Get(context.Background(), webhookKey, &admissionregistrationv1.ValidatingWebhookConfiguration{})
		g.Expect(k8serr.IsNotFound(err)).To(BeTrue(), "webhook should be gone")
	})

	t.Run("is idempotent when the webhook is absent", func(t *testing.T) {
		g := NewWithT(t)
		cli := fake.NewClientBuilder().WithScheme(scheme).Build()
		r := &KserveModuleReconciler{Client: cli}

		g.Expect(r.deleteConfigDeletionWebhook(context.Background())).To(Succeed())
	})

	t.Run("wraps a non-NotFound delete error", func(t *testing.T) {
		g := NewWithT(t)
		boom := errors.New("boom")
		cli := fake.NewClientBuilder().WithScheme(scheme).
			WithInterceptorFuncs(interceptor.Funcs{
				Delete: func(context.Context, client.WithWatch, client.Object, ...client.DeleteOption) error {
					return boom
				},
			}).Build()
		r := &KserveModuleReconciler{Client: cli}

		err := r.deleteConfigDeletionWebhook(context.Background())
		g.Expect(err).To(MatchError(ContainSubstring("boom")))
		g.Expect(err.Error()).To(ContainSubstring(llmISVCConfigWebhookName))
	})
}
