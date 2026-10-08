package kservemodule

import (
	"context"
	"testing"

	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8serr "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/opendatahub-io/kserve-module/pkg/apis/v1alpha1"
)

func TestDetachLLMISVCDrainResources(t *testing.T) {
	g := NewWithT(t)
	ctx := context.Background()
	scheme := teardownTestScheme(t)
	kserve := &platformv1alpha1.Kserve{ObjectMeta: metav1.ObjectMeta{
		Name: "default-kserve",
		UID:  types.UID("kserve-uid"),
	}}
	ownerRef := *metav1.NewControllerRef(kserve, platformv1alpha1.GroupVersion.WithKind("Kserve"))
	llmController := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
		Name:            llmISVCControllerDeployment,
		Namespace:       "opendatahub",
		OwnerReferences: []metav1.OwnerReference{ownerRef},
	}}
	unrelated := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Name:            "inferenceservice-config",
		Namespace:       "opendatahub",
		OwnerReferences: []metav1.OwnerReference{ownerRef},
	}}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(kserve, llmController, unrelated).Build()
	reconciler := &KserveModuleReconciler{Client: cli, Scheme: scheme}

	drainResource := unstructured.Unstructured{}
	drainResource.SetGroupVersionKind(appsv1.SchemeGroupVersion.WithKind("Deployment"))
	drainResource.SetNamespace(llmController.Namespace)
	drainResource.SetName(llmController.Name)
	g.Expect(reconciler.detachLLMISVCDrainResources(ctx, kserve, []unstructured.Unstructured{drainResource})).To(Succeed())

	g.Expect(cli.Get(ctx, client.ObjectKeyFromObject(llmController), llmController)).To(Succeed())
	g.Expect(llmController.GetOwnerReferences()).To(BeEmpty(), "the cleanup controller must survive Kserve foreground deletion")
	g.Expect(cli.Get(ctx, client.ObjectKeyFromObject(unrelated), unrelated)).To(Succeed())
	g.Expect(unrelated.GetOwnerReferences()).To(ConsistOf(ownerRef), "ordinary Kserve operands remain on the existing GC path")
}

func TestLLMISVCConfigCleanupDrain(t *testing.T) {
	ctx := context.Background()

	t.Run("reports the services blocking preset cleanup", func(t *testing.T) {
		g := NewWithT(t)
		config := teardownConfig("preset", "True", map[string]any{"namespace": "llm", "name": "granite"})
		reconciler := &KserveModuleReconciler{Client: fake.NewClientBuilder().WithScheme(teardownTestScheme(t)).WithObjects(config).Build()}

		outcome, err := reconciler.cleanupLLMISVCConfigsOnDelete(ctx, "opendatahub")
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(outcome.done).To(BeFalse())
		g.Expect(outcome.blockers).To(ConsistOf("preset (referenced by llm/granite)"))
	})

	t.Run("deletes an unreferenced preset after its service finalizes", func(t *testing.T) {
		g := NewWithT(t)
		scheme := teardownTestScheme(t)
		config := teardownConfig("preset", "True", map[string]any{"namespace": "llm", "name": "granite"})
		cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(config).Build()
		reconciler := &KserveModuleReconciler{Client: cli}

		outcome, err := reconciler.cleanupLLMISVCConfigsOnDelete(ctx, "opendatahub")
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(outcome.done).To(BeFalse())

		current := &unstructured.Unstructured{}
		current.SetGroupVersionKind(llmISVCConfigGVK)
		g.Expect(cli.Get(ctx, client.ObjectKeyFromObject(config), current)).To(Succeed())
		current.Object["status"] = map[string]any{
			"observedGeneration": int64(1),
			"conditions":         []any{map[string]any{"type": "ConfigInUse", "status": "False"}},
		}
		g.Expect(cli.Update(ctx, current)).To(Succeed())

		outcome, err = reconciler.cleanupLLMISVCConfigsOnDelete(ctx, "opendatahub")
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(outcome.done).To(BeFalse(), "the delete request has been accepted and finalizers may still be running")
		outcome, err = reconciler.cleanupLLMISVCConfigsOnDelete(ctx, "opendatahub")
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(outcome.done).To(BeTrue(), "the Kserve finalizer can proceed after preset finalization")
	})

	t.Run("finishes immediately when no presets exist", func(t *testing.T) {
		g := NewWithT(t)
		reconciler := &KserveModuleReconciler{Client: fake.NewClientBuilder().WithScheme(teardownTestScheme(t)).Build()}
		outcome, err := reconciler.cleanupLLMISVCConfigsOnDelete(ctx, "opendatahub")
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(outcome.done).To(BeTrue())
		g.Expect(outcome.blockers).To(BeEmpty())
	})

	t.Run("removes the cleanup controller after preset finalization", func(t *testing.T) {
		g := NewWithT(t)
		scheme := teardownTestScheme(t)
		controller := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: llmISVCControllerDeployment, Namespace: "opendatahub"}}
		service := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "llmisvc-webhook-server-service", Namespace: "opendatahub"}}
		ordinary := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "kserve-controller-manager", Namespace: "opendatahub"}}
		cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(controller, service, ordinary).Build()
		reconciler := &KserveModuleReconciler{Client: cli}

		resources := []unstructured.Unstructured{
			{Object: map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": controller.Name, "namespace": controller.Namespace}}},
			{Object: map[string]any{"apiVersion": "v1", "kind": "Service", "metadata": map[string]any{"name": service.Name, "namespace": service.Namespace}}},
			{Object: map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": ordinary.Name, "namespace": ordinary.Namespace}}},
		}
		g.Expect(reconciler.deleteLLMISVCDrainResources(ctx, resources)).To(Succeed())
		g.Expect(k8serr.IsNotFound(cli.Get(ctx, client.ObjectKeyFromObject(controller), controller))).To(BeTrue())
		g.Expect(k8serr.IsNotFound(cli.Get(ctx, client.ObjectKeyFromObject(service), service))).To(BeTrue())
		g.Expect(cli.Get(ctx, client.ObjectKeyFromObject(ordinary), ordinary)).To(Succeed(), "ordinary InferenceService controller cleanup remains GC-owned")
	})
}

func teardownConfig(name, inUse string, refs ...map[string]any) *unstructured.Unstructured {
	config := &unstructured.Unstructured{Object: map[string]any{}}
	config.SetGroupVersionKind(llmISVCConfigGVK)
	config.SetNamespace("opendatahub")
	config.SetName(name)
	config.SetGeneration(1)
	config.SetAnnotations(map[string]string{wellKnownAnnotationKey: wellKnownAnnotationValue})
	status := map[string]any{
		"observedGeneration": int64(1),
		"conditions":         []any{map[string]any{"type": "ConfigInUse", "status": inUse}},
	}
	if len(refs) > 0 {
		references := make([]any, len(refs))
		for i := range refs {
			references[i] = refs[i]
		}
		status["referencedBy"] = references
	}
	config.Object["status"] = status
	return config
}

func teardownTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}
