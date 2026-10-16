package kservemodule

import (
	"context"
	"testing"

	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	k8serr "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestCleanupWVAComponent_IdempotentWhenNothingPresent(t *testing.T) {
	g := NewWithT(t)

	cli := fake.NewClientBuilder().WithScheme(wvaTestScheme()).WithRESTMapper(wvaTestRESTMapper()).Build()
	r := &KserveModuleReconciler{Client: cli, applicationsNamespace: "opendatahub"}

	g.Expect(cleanupWVAComponent(context.Background(), r)).To(Succeed())
}

func TestCleanupWVAComponent_DeletesCRsAndCRDAndStripsConfigKey(t *testing.T) {
	g := NewWithT(t)

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: kserveConfigMapName, Namespace: "opendatahub"},
		Data: map[string]string{
			ingressConfigKeyName:              `{"ingressDomain":"example.com"}`,
			autoscalingWVAControllerConfigKey: `{"prometheus":{"url":"http://thanos"}}`,
		},
	}
	crd := &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: variantAutoscalingCRDName},
	}
	va := leftoverVariantAutoscaling("leftover-va", "opendatahub")

	cli := fake.NewClientBuilder().
		WithScheme(wvaTestScheme()).
		WithRESTMapper(wvaTestRESTMapper()).
		WithObjects(cm, crd, va).
		Build()
	r := &KserveModuleReconciler{Client: cli, applicationsNamespace: "opendatahub"}

	g.Expect(cleanupWVAComponent(context.Background(), r)).To(Succeed())

	err := cli.Get(context.Background(), client.ObjectKey{Name: variantAutoscalingCRDName}, &apiextensionsv1.CustomResourceDefinition{})
	g.Expect(k8serr.IsNotFound(err)).To(BeTrue())

	updated := &corev1.ConfigMap{}
	g.Expect(cli.Get(context.Background(), client.ObjectKey{Name: kserveConfigMapName, Namespace: "opendatahub"}, updated)).To(Succeed())
	g.Expect(updated.Data).NotTo(HaveKey(autoscalingWVAControllerConfigKey))
	g.Expect(updated.Data).To(HaveKey(ingressConfigKeyName))

	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(variantAutoscalingGVK.GroupVersion().WithKind(variantAutoscalingGVK.Kind + "List"))
	g.Expect(cli.List(context.Background(), list)).To(Succeed())
	g.Expect(list.Items).To(BeEmpty())
}

func TestStripWVAAutoscalingConfig_NoopsWhenKeyAbsent(t *testing.T) {
	g := NewWithT(t)

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: kserveConfigMapName, Namespace: "opendatahub"},
		Data:       map[string]string{ingressConfigKeyName: `{}`},
	}
	cli := fake.NewClientBuilder().WithScheme(wvaTestScheme()).WithObjects(cm).Build()
	r := &KserveModuleReconciler{Client: cli, applicationsNamespace: "opendatahub"}

	g.Expect(r.stripWVAAutoscalingConfig(context.Background())).To(Succeed())
	updated := &corev1.ConfigMap{}
	g.Expect(cli.Get(context.Background(), client.ObjectKey{Name: kserveConfigMapName, Namespace: "opendatahub"}, updated)).To(Succeed())
	g.Expect(updated.Data).To(HaveKey(ingressConfigKeyName))
}

func leftoverVariantAutoscaling(name, namespace string) *unstructured.Unstructured {
	va := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "llmd.ai/v1alpha1",
		"kind":       "VariantAutoscaling",
		"metadata": map[string]any{
			"name":      name,
			"namespace": namespace,
		},
	}}
	va.SetGroupVersionKind(variantAutoscalingGVK)
	return va
}

func wvaTestScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	_ = apiextensionsv1.AddToScheme(s)
	return s
}

func wvaTestRESTMapper() apimeta.RESTMapper {
	rm := apimeta.NewDefaultRESTMapper(nil)
	rm.Add(schema.GroupVersionKind{Group: "apiextensions.k8s.io", Version: "v1", Kind: "CustomResourceDefinition"}, testClusterScope)
	rm.Add(schema.GroupVersionKind{Group: "", Version: "v1", Kind: "ConfigMap"}, testNamespaceScope)
	rm.Add(variantAutoscalingGVK, testNamespaceScope)
	return rm
}
