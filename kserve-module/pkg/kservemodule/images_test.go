package kservemodule

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/opendatahub-io/odh-platform-utilities/pkg/render/kustomize"
)

func TestConfidentialStorageHelperImage_RenderedFromParams(t *testing.T) {
	for _, override := range []string{"", "registry.example.com/mirrored/osc-storage-helper@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"} {
		name := "default"
		if override != "" {
			name = "mirrored"
		}
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)
			configDir := filepath.Join(t.TempDir(), "config")
			g.Expect(os.CopyFS(configDir, os.DirFS("../../../config"))).To(Succeed())
			overlay := filepath.Join(configDir, "overlays", "odh")
			params, err := parseParams(filepath.Join(overlay, "params.env"))
			g.Expect(err).NotTo(HaveOccurred())
			expectedImage := params["kserve-osc-storage-helper"]
			g.Expect(expectedImage).NotTo(BeEmpty())
			if override != "" {
				expectedImage = override
			}

			t.Setenv("RELATED_IMAGE_OSC_STORAGE_HELPER_IMAGE", override)
			g.Expect(applyParams(overlay, kserveImageParamMap)).To(Succeed())
			resources, err := kustomize.Render(overlay, nil)
			g.Expect(err).NotTo(HaveOccurred())
			for _, resource := range resources {
				if resource.GetKind() != "LLMInferenceServiceConfig" || resource.GetName() != "kserve-config-llm-confidential-bare-metal" {
					continue
				}
				containers, found, err := unstructured.NestedSlice(resource.Object, "spec", "template", "initContainers")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(found).To(BeTrue())
				for _, container := range []string{"format-disk", "check-ready"} {
					g.Expect(containers).To(ContainElement(SatisfyAll(
						HaveKeyWithValue("name", container),
						HaveKeyWithValue("image", expectedImage),
					)), "%s must use the configured helper image", container)
				}
				return
			}
			t.Fatal("confidential bare-metal preset missing from rendered overlay")
		})
	}
}

func TestDefaultAndMultiPortPresetImages_RenderedFromOperatorParams(t *testing.T) {
	const vllmImage = "registry.example.com/vllm@sha256:1111111111111111111111111111111111111111111111111111111111111111"
	const sidecarImage = "registry.example.com/sidecar@sha256:2222222222222222222222222222222222222222222222222222222222222222"
	const defaultDecodePreset = "kserve-config-llm-decode-worker-data-parallel"
	const defaultPrefillPreset = "kserve-config-llm-prefill-worker-data-parallel"
	const decodePreset = "kserve-config-llm-decode-worker-data-parallel-multi-port"
	const prefillPreset = "kserve-config-llm-prefill-worker-data-parallel-multi-port"

	type imageTarget struct {
		preset    string
		path      []string
		container string
		image     string
	}
	targets := []imageTarget{
		{defaultDecodePreset, []string{"spec", "template", "containers"}, "main", vllmImage},
		{defaultDecodePreset, []string{"spec", "worker", "containers"}, "main", vllmImage},
		{defaultPrefillPreset, []string{"spec", "prefill", "template", "containers"}, "main", vllmImage},
		{defaultPrefillPreset, []string{"spec", "prefill", "worker", "containers"}, "main", vllmImage},
		{decodePreset, []string{"spec", "template", "containers"}, "main", vllmImage},
		{decodePreset, []string{"spec", "worker", "containers"}, "main", vllmImage},
		{decodePreset, []string{"spec", "template", "initContainers"}, "llm-d-routing-sidecar", sidecarImage},
		{decodePreset, []string{"spec", "worker", "initContainers"}, "llm-d-routing-sidecar", sidecarImage},
		{prefillPreset, []string{"spec", "prefill", "template", "containers"}, "main", vllmImage},
		{prefillPreset, []string{"spec", "prefill", "worker", "containers"}, "main", vllmImage},
	}

	for _, overlayName := range []string{"odh", "odh-xks"} {
		t.Run(overlayName, func(t *testing.T) {
			g := NewWithT(t)
			configDir := filepath.Join(t.TempDir(), "config")
			g.Expect(os.CopyFS(configDir, os.DirFS("../../../config"))).To(Succeed())
			t.Setenv("RELATED_IMAGE_RHAII_VLLM_CUDA_IMAGE", vllmImage)
			t.Setenv("RELATED_IMAGE_ODH_LLM_D_ROUTER_DISAGG_SIDECAR_IMAGE", sidecarImage)
			g.Expect(applyParams(filepath.Join(configDir, "overlays", "odh"), kserveImageParamMap)).To(Succeed())

			resources, err := kustomize.Render(filepath.Join(configDir, "overlays", overlayName), nil)
			g.Expect(err).NotTo(HaveOccurred())
			found := make([]bool, len(targets))
			for _, resource := range resources {
				if resource.GetKind() != "LLMInferenceServiceConfig" {
					continue
				}
				for i, target := range targets {
					if resource.GetName() != target.preset {
						continue
					}
					containers, exists, err := unstructured.NestedSlice(resource.Object, target.path...)
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(exists).To(BeTrue(), "%s is missing %v", target.preset, target.path)
					g.Expect(containers).To(ContainElement(SatisfyAll(
						HaveKeyWithValue("name", target.container),
						HaveKeyWithValue("image", target.image),
					)), "%s %v must use the configured image", target.preset, target.path)
					found[i] = true
				}
			}
			for i, exists := range found {
				g.Expect(exists).To(BeTrue(), "%s missing from rendered overlay", targets[i].preset)
			}
		})
	}
}

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
