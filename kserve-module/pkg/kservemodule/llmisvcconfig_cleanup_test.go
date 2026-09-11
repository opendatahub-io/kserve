package kservemodule

import (
	"context"
	"errors"
	"testing"
	"time"

	. "github.com/onsi/gomega"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	k8serr "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
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

	config := func(name string, generation, observedGeneration int64, configInUse string, refs ...map[string]any) unstructured.Unstructured {
		cfg := unstructured.Unstructured{Object: map[string]any{}}
		cfg.SetName(name)
		cfg.SetGeneration(generation)
		status := map[string]any{
			"observedGeneration": observedGeneration,
			"conditions": []any{
				map[string]any{"type": "ConfigInUse", "status": configInUse},
			},
		}
		if refs != nil {
			references := make([]any, len(refs))
			for i := range refs {
				references[i] = refs[i]
			}
			status["referencedBy"] = references
		}
		cfg.Object["status"] = status
		return cfg
	}

	configs := []unstructured.Unstructured{
		config("cfg-unused", 3, 3, "False"),
		config("cfg-used", 3, 3, "True", map[string]any{"name": "svc1", "namespace": "ns1"}),
		config("cfg-pending", 3, 0, "False"),
		config("cfg-unobserved", 3, 3, "Unknown"),
	}

	blockers := referencedConfigBlockers(configs)
	g.Expect(blockers).To(ConsistOf(
		"cfg-used (referenced by ns1/svc1)",
		"cfg-pending (waiting for llmisvc controller to observe the current generation)",
		"cfg-unobserved (ConfigInUse=Unknown)",
	))
}

func TestConfigDeletionBlocker(t *testing.T) {
	g := NewWithT(t)
	cfg := &unstructured.Unstructured{Object: map[string]any{
		"status": map[string]any{"observedGeneration": int64(2)},
	}}
	cfg.SetGeneration(2)

	blocker, err := configDeletionBlocker(cfg)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(blocker).To(Equal("waiting for ConfigInUse condition"))
}

func TestNonTerminatingConfigs(t *testing.T) {
	g := NewWithT(t)
	pending := unstructured.Unstructured{Object: map[string]any{}}
	pending.SetName("pending")
	terminating := unstructured.Unstructured{Object: map[string]any{}}
	terminating.SetName("terminating")
	terminating.SetDeletionTimestamp(&metav1.Time{Time: time.Now()})

	configs := nonTerminatingConfigs([]unstructured.Unstructured{pending, terminating})
	g.Expect(configs).To(HaveLen(1))
	g.Expect(configs[0].GetName()).To(Equal("pending"))
}

func TestConfigDeletionWebhookDeleteRule(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)

	webhookKey := types.NamespacedName{Name: llmISVCConfigWebhookName}
	newWebhook := func() *admissionregistrationv1.ValidatingWebhookConfiguration {
		return &admissionregistrationv1.ValidatingWebhookConfiguration{
			ObjectMeta: metav1.ObjectMeta{Name: llmISVCConfigWebhookName},
			Webhooks: []admissionregistrationv1.ValidatingWebhook{
				{
					Name: "llminferenceserviceconfig.kserve-webhook-server.v1alpha2.validator",
					Rules: []admissionregistrationv1.RuleWithOperations{{
						Operations: []admissionregistrationv1.OperationType{
							admissionregistrationv1.Create,
							admissionregistrationv1.Update,
							admissionregistrationv1.Delete,
						},
						Rule: admissionregistrationv1.Rule{
							APIGroups:   []string{"serving.kserve.io"},
							APIVersions: []string{"v1alpha2"},
							Resources:   []string{"llminferenceserviceconfigs"},
						},
					}},
				},
				{
					Name: "llminferenceserviceconfig.kserve-webhook-server.v1alpha1.validator",
					Rules: []admissionregistrationv1.RuleWithOperations{{
						Operations: []admissionregistrationv1.OperationType{admissionregistrationv1.Create, admissionregistrationv1.Update, admissionregistrationv1.Delete},
						Rule: admissionregistrationv1.Rule{
							APIGroups:   []string{"serving.kserve.io"},
							APIVersions: []string{"v1alpha1"},
							Resources:   []string{"llminferenceserviceconfigs"},
						},
					}},
				},
			},
		}
	}

	t.Run("disables and restores only the v1alpha2 DELETE rule", func(t *testing.T) {
		g := NewWithT(t)
		cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(newWebhook()).Build()
		r := &KserveModuleReconciler{Client: cli}

		patch, err := r.disableConfigDeletionWebhookDelete(context.Background())
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(patch.rules).To(HaveLen(1))

		got := &admissionregistrationv1.ValidatingWebhookConfiguration{}
		g.Expect(cli.Get(context.Background(), webhookKey, got)).To(Succeed())
		g.Expect(got.Webhooks[0].Rules[0].Operations).To(Equal([]admissionregistrationv1.OperationType{admissionregistrationv1.Create, admissionregistrationv1.Update}))
		g.Expect(got.Webhooks[1].Rules[0].Operations).To(ContainElement(admissionregistrationv1.Delete))

		g.Expect(r.restoreConfigDeletionWebhookDelete(context.Background(), patch)).To(Succeed())
		g.Expect(cli.Get(context.Background(), webhookKey, got)).To(Succeed())
		g.Expect(got.Webhooks[0].Rules[0].Operations).To(Equal([]admissionregistrationv1.OperationType{
			admissionregistrationv1.Create,
			admissionregistrationv1.Update,
			admissionregistrationv1.Delete,
		}))
	})

	t.Run("restores by rule content after reordering", func(t *testing.T) {
		g := NewWithT(t)
		cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(newWebhook()).Build()
		r := &KserveModuleReconciler{Client: cli}
		patch, err := r.disableConfigDeletionWebhookDelete(context.Background())
		g.Expect(err).NotTo(HaveOccurred())
		got := &admissionregistrationv1.ValidatingWebhookConfiguration{}
		g.Expect(cli.Get(context.Background(), webhookKey, got)).To(Succeed())
		unrelated := *got.Webhooks[0].Rules[0].DeepCopy()
		unrelated.Resources = []string{"unrelated"}
		got.Webhooks[0].Rules = append([]admissionregistrationv1.RuleWithOperations{unrelated}, got.Webhooks[0].Rules...)
		g.Expect(cli.Update(context.Background(), got)).To(Succeed())
		g.Expect(r.restoreConfigDeletionWebhookDelete(context.Background(), patch)).To(Succeed())
		g.Expect(cli.Get(context.Background(), webhookKey, got)).To(Succeed())
		g.Expect(got.Webhooks[0].Rules[0]).To(Equal(unrelated))
		g.Expect(got.Webhooks[0].Rules[1].Operations).To(ContainElement(admissionregistrationv1.Delete))
		g.Expect(got.Annotations).NotTo(HaveKey(configWebhookRestoreAnnotation))
	})

	for _, terminating := range []bool{false, true} {
		name := "recovers before reporting no configs"
		if terminating {
			name = "recovers before waiting for finalizers"
		}
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)
			failRestore := false
			restoreAttempts := 0
			cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(newWebhook()).
				WithInterceptorFuncs(interceptor.Funcs{
					List: func(_ context.Context, _ client.WithWatch, obj client.ObjectList, _ ...client.ListOption) error {
						list := obj.(*unstructured.UnstructuredList)
						if terminating {
							cfg := unstructured.Unstructured{Object: map[string]any{}}
							cfg.SetGroupVersionKind(llmISVCConfigGVK)
							cfg.SetName("terminating")
							cfg.SetAnnotations(map[string]string{wellKnownAnnotationKey: wellKnownAnnotationValue})
							now := metav1.Now()
							cfg.SetDeletionTimestamp(&now)
							list.Items = []unstructured.Unstructured{cfg}
						}
						return nil
					},
					Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
						if obj.GetAnnotations()[configWebhookRestoreAnnotation] == "" {
							restoreAttempts++
							if failRestore {
								return errors.New("temporary restore failure")
							}
						}
						return c.Update(ctx, obj, opts...)
					},
				}).Build()
			r := &KserveModuleReconciler{Client: cli}
			patch, err := r.disableConfigDeletionWebhookDelete(context.Background())
			g.Expect(err).NotTo(HaveOccurred())
			failRestore = true
			g.Expect(r.restoreConfigDeletionWebhookDelete(context.Background(), patch)).NotTo(Succeed())
			got := &admissionregistrationv1.ValidatingWebhookConfiguration{}
			g.Expect(cli.Get(context.Background(), webhookKey, got)).To(Succeed())
			g.Expect(got.Annotations).To(HaveKey(configWebhookRestoreAnnotation))
			failRestore = false
			// A new reconciler has no in-memory recovery state.
			restarted := &KserveModuleReconciler{Client: cli}
			outcome, err := restarted.cleanupLLMISVCConfigsOnDelete(context.Background(), "test")
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(outcome.done).To(Equal(!terminating))
			g.Expect(restoreAttempts).To(Equal(2))
			g.Expect(cli.Get(context.Background(), webhookKey, got)).To(Succeed())
			g.Expect(got.Webhooks[0].Rules[0].Operations).To(ContainElement(admissionregistrationv1.Delete))
			g.Expect(got.Annotations).NotTo(HaveKey(configWebhookRestoreAnnotation))
		})
	}

	t.Run("does nothing when the webhook is absent", func(t *testing.T) {
		g := NewWithT(t)
		cli := fake.NewClientBuilder().WithScheme(scheme).Build()
		r := &KserveModuleReconciler{Client: cli}

		patch, err := r.disableConfigDeletionWebhookDelete(context.Background())
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(patch.rules).To(BeEmpty())
		g.Expect(r.restoreConfigDeletionWebhookDelete(context.Background(), patch)).To(Succeed())
	})

	t.Run("returns an error when the v1alpha2 DELETE rule is absent", func(t *testing.T) {
		g := NewWithT(t)
		webhook := newWebhook()
		webhook.Webhooks[0].Rules[0].Operations = []admissionregistrationv1.OperationType{admissionregistrationv1.Create, admissionregistrationv1.Update}
		cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(webhook).Build()
		r := &KserveModuleReconciler{Client: cli}

		_, err := r.disableConfigDeletionWebhookDelete(context.Background())
		g.Expect(err).To(MatchError(ContainSubstring("no v1alpha2 LLMInferenceServiceConfig DELETE rule found")))
	})
}

func TestDeleteWellKnownConfigsRetriesForbidden(t *testing.T) {
	g := NewWithT(t)
	scheme := runtime.NewScheme()
	config := unstructured.Unstructured{Object: map[string]any{}}
	config.SetGroupVersionKind(llmISVCConfigGVK)
	config.SetNamespace("test")
	config.SetName("default")

	deleteAttempts := 0
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&config).
		WithInterceptorFuncs(interceptor.Funcs{
			Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
				deleteAttempts++
				if deleteAttempts == 1 {
					return k8serr.NewForbidden(schema.GroupResource{Group: llmISVCConfigGVK.Group, Resource: "llminferenceserviceconfigs"}, obj.GetName(), errors.New("webhook cache not updated"))
				}
				return c.Delete(ctx, obj, opts...)
			},
		}).Build()
	r := &KserveModuleReconciler{Client: cli}

	g.Expect(r.deleteWellKnownConfigs(context.Background(), []unstructured.Unstructured{config})).To(Succeed())
	g.Expect(deleteAttempts).To(Equal(2))
}
