package kservemodule

import (
	"context"
	"testing"

	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

var modelExpressServerGVK = modelExpressServerListGVK.GroupVersion().WithKind("ModelExpressServer")

func modelExpressServer(namespace, name string, finalizers ...string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(modelExpressServerGVK)
	u.SetNamespace(namespace)
	u.SetName(name)
	u.SetFinalizers(finalizers)
	return u
}

func modelExpressTestReconciler(objs ...client.Object) *KserveModuleReconciler {
	mapper := meta.NewDefaultRESTMapper(nil)
	mapper.Add(modelExpressServerGVK, meta.RESTScopeNamespace)
	cli := fake.NewClientBuilder().
		WithScheme(runtime.NewScheme()).
		WithRESTMapper(mapper).
		WithObjects(objs...).
		Build()
	return &KserveModuleReconciler{Client: cli}
}

func TestModelExpressRemovalBlockers_NoCRDMeansNoBlockers(t *testing.T) {
	g := NewWithT(t)
	cli := fake.NewClientBuilder().
		WithScheme(runtime.NewScheme()).
		WithRESTMapper(meta.NewDefaultRESTMapper(nil)).
		Build()

	blockers, err := modelExpressRemovalBlockers(context.Background(), &KserveModuleReconciler{Client: cli})
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(blockers).To(BeEmpty())
}

func TestModelExpressRemovalBlockers_OnlyFinalizerHoldersSorted(t *testing.T) {
	g := NewWithT(t)
	r := modelExpressTestReconciler(
		modelExpressServer("team-b", "enforced", modelExpressAuthDelegatorFinalizer),
		modelExpressServer("team-a", "open"),
		modelExpressServer("team-a", "enforced", "example.com/other", modelExpressAuthDelegatorFinalizer),
		modelExpressServer("team-c", "other-finalizer", "example.com/other"),
	)

	blockers, err := modelExpressRemovalBlockers(context.Background(), r)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(blockers).To(Equal([]string{"team-a/enforced", "team-b/enforced"}))
}

func TestComponentsConfig_OnlyModelExpressHasRemovalBlockers(t *testing.T) {
	g := NewWithT(t)
	for _, comp := range components {
		if comp.name == ModelExpressComponentName {
			g.Expect(comp.removalBlockers).NotTo(BeNil())
			continue
		}
		g.Expect(comp.removalBlockers).To(BeNil(), "component %q", comp.name)
	}
}
