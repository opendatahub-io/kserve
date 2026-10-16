package kservemodule

import (
	"encoding/json"
	"os"
	"testing"

	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"

	platformv1alpha1 "github.com/opendatahub-io/kserve-module/pkg/apis/v1alpha1"
)

func TestConfidentialContainersConfig(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config *platformv1alpha1.ConfidentialContainersConfig
		key    string
		init   string
	}{
		{name: "omitted", key: "kbs:///default/kserve-luks-password/key"},
		{name: "empty", config: &platformv1alpha1.ConfidentialContainersConfig{}, key: "kbs:///default/kserve-luks-password/key"},
		{
			name:   "disk key only",
			config: &platformv1alpha1.ConfidentialContainersConfig{LUKSResourceID: "kbs:///customer/disk/key"},
			key:    "kbs:///customer/disk/key",
		},
		{
			name:   "init data only",
			config: &platformv1alpha1.ConfidentialContainersConfig{InitData: "customer-initdata"},
			key:    "kbs:///default/kserve-luks-password/key", init: "customer-initdata",
		},
		{
			name: "both settings",
			config: &platformv1alpha1.ConfidentialContainersConfig{
				LUKSResourceID: "kbs:///customer/disk/key", InitData: "customer-initdata",
			},
			key: "kbs:///customer/disk/key", init: "customer-initdata",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)
			manifest, err := os.ReadFile("../../../config/overlays/odh/patches/inferenceservice-config-patch.yaml")
			g.Expect(err).NotTo(HaveOccurred())
			obj := unstructured.Unstructured{}
			g.Expect(yaml.Unmarshal(manifest, &obj.Object)).To(Succeed())
			kserve := &platformv1alpha1.Kserve{Spec: platformv1alpha1.KserveSpec{ConfidentialContainers: tc.config}}
			resources, err := customizeKserveConfigMap([]unstructured.Unstructured{obj}, kserve)
			g.Expect(err).NotTo(HaveOccurred())
			_, cm, err := getIndexedResource[corev1.ConfigMap](resources, configMapGVK, kserveConfigMapName)
			g.Expect(err).NotTo(HaveOccurred())
			defaults := map[string]string{}
			g.Expect(json.Unmarshal([]byte(cm.Data["confidentialBareMetal"]), &defaults)).To(Succeed())
			g.Expect(defaults).To(Equal(map[string]string{"luksResourceId": tc.key, "initData": tc.init}))
		})
	}
}
