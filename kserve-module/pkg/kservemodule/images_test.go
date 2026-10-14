package kservemodule

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/onsi/gomega"
)

func TestKserveImageParamMap_AllValuesAreRelatedImage(t *testing.T) {
	g := NewWithT(t)
	for key, val := range kserveImageParamMap {
		g.Expect(val).Should(HavePrefix("RELATED_IMAGE_"), "key %q has value %q without RELATED_IMAGE_ prefix", key, val)
	}
}

func TestModelControllerImageParamMap_AllValuesAreRelatedImage(t *testing.T) {
	g := NewWithT(t)
	for key, val := range modelControllerImageParamMap {
		g.Expect(val).Should(HavePrefix("RELATED_IMAGE_"), "key %q has value %q without RELATED_IMAGE_ prefix", key, val)
	}
}

func TestWVAImageParamMap_AllValuesAreRelatedImage(t *testing.T) {
	g := NewWithT(t)
	for key, val := range wvaImageParamMap {
		g.Expect(val).Should(HavePrefix("RELATED_IMAGE_"), "key %q has value %q without RELATED_IMAGE_ prefix", key, val)
	}
}

func TestModelExpressImageParamMap_RewritesBundleParamsFromBothOverlays(t *testing.T) {
	for _, overlay := range []string{ModelExpressManifestSourcePath, ModelExpressManifestSourcePathXKS} {
		t.Run(overlay, func(t *testing.T) {
			g := NewWithT(t)
			bundle := t.TempDir()
			g.Expect(os.MkdirAll(filepath.Join(bundle, overlay), 0o750)).To(Succeed())
			g.Expect(os.MkdirAll(filepath.Join(bundle, "base"), 0o750)).To(Succeed())
			paramsFile := filepath.Join(bundle, "base", "params.env")
			g.Expect(os.WriteFile(paramsFile, []byte(
				"MODELEXPRESS_OPERATOR_IMAGE=quay.io/opendatahub/odh-modelexpress-operator:odh-stable\n"+
					"MODELEXPRESS_SERVER_IMAGE=quay.io/opendatahub/odh-modelexpress:odh-stable\n"), 0o600)).To(Succeed())

			t.Setenv("RELATED_IMAGE_ODH_MODELEXPRESS_OPERATOR_IMAGE", "registry.example/operator@sha256:1111")
			t.Setenv("RELATED_IMAGE_ODH_MODELEXPRESS_IMAGE", "registry.example/server@sha256:2222")

			g.Expect(applyParams(filepath.Join(bundle, overlay), modelExpressImageParamMap)).To(Succeed())

			params, err := parseParams(paramsFile)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(params).To(Equal(map[string]string{
				"MODELEXPRESS_OPERATOR_IMAGE": "registry.example/operator@sha256:1111",
				"MODELEXPRESS_SERVER_IMAGE":   "registry.example/server@sha256:2222",
			}))
		})
	}
}

func TestImageParamMaps_NoKeyOverlap(t *testing.T) {
	g := NewWithT(t)
	for key := range kserveImageParamMap {
		_, exists := modelControllerImageParamMap[key]
		g.Expect(exists).Should(BeFalse(), "key %q exists in both kserve and modelcontroller image maps", key)
	}
}
