package kservemodule

import (
	"encoding/json"
	"os"
	"testing"

	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/yaml"
)

func TestPreserveConfidentialDefaults(t *testing.T) {
	for _, raw := range []string{`{"storageClassName":"customer-csi","storageSize":"500Gi","luksResourceId":"kbs:///customer/disk/key"}`, "invalid-json", ""} {
		t.Run(raw, func(t *testing.T) {
			g := NewWithT(t)
			scheme := runtime.NewScheme()
			g.Expect(corev1.AddToScheme(scheme)).To(Succeed())
			live := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: kserveConfigMapName, Namespace: "applications"}, Data: map[string]string{"confidentialBareMetal": raw}}
			r := &KserveModuleReconciler{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(live).Build()}
			resources := []unstructured.Unstructured{{Object: map[string]any{
				"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": kserveConfigMapName, "namespace": "applications"},
				"data": map[string]any{"confidentialBareMetal": "installation-template", "ingress": "new-product-value"},
			}}}
			result, err := r.preserveConfidentialDefaults(t.Context(), resources)
			g.Expect(err).NotTo(HaveOccurred())
			data, _, err := unstructured.NestedStringMap(result[0].Object, "data")
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(data["confidentialBareMetal"]).To(Equal(raw))
			g.Expect(data["ingress"]).To(Equal("new-product-value"))
			// A subsequent render/upgrade still uses the live administrator value.
			result, err = r.preserveConfidentialDefaults(t.Context(), result)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(result[0].Object["data"]).To(Equal(resources[0].Object["data"]))
		})
	}
}

func TestConfidentialDefaultsTemplateOnInstall(t *testing.T) {
	g := NewWithT(t)
	scheme := runtime.NewScheme()
	g.Expect(corev1.AddToScheme(scheme)).To(Succeed())
	r := &KserveModuleReconciler{Client: fake.NewClientBuilder().WithScheme(scheme).Build()}
	manifest, err := os.ReadFile("../../../config/overlays/odh/patches/inferenceservice-config-patch.yaml")
	g.Expect(err).NotTo(HaveOccurred())
	obj := unstructured.Unstructured{}
	g.Expect(yaml.Unmarshal(manifest, &obj.Object)).To(Succeed())
	resources, err := r.preserveConfidentialDefaults(t.Context(), []unstructured.Unstructured{obj})
	g.Expect(err).NotTo(HaveOccurred())
	value, _, err := unstructured.NestedString(resources[0].Object, "data", "confidentialBareMetal")
	g.Expect(err).NotTo(HaveOccurred())
	defaults := map[string]any{}
	g.Expect(json.Unmarshal([]byte(value), &defaults)).To(Succeed())
	g.Expect(defaults).To(HaveKeyWithValue("storageClassName", ""))
	g.Expect(defaults).To(HaveKeyWithValue("storageSize", "200Gi"))
	g.Expect(defaults).To(HaveKeyWithValue("luksResourceId", "kbs:///default/luks-password/key"))
}
