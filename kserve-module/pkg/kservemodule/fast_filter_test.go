package kservemodule

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	. "github.com/onsi/gomega"
)

func llmISVCConfig(name, image string) unstructured.Unstructured {
	return unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "serving.kserve.io/v1alpha1",
		"kind":       "LLMInferenceServiceConfig",
		"metadata": map[string]any{
			"name": name,
			"annotations": map[string]any{
				wellKnownAnnotationKey: wellKnownAnnotationValue,
			},
		},
		"spec": map[string]any{
			"template": map[string]any{
				"containers": []any{
					map[string]any{
						"name":  "main",
						"image": image,
					},
				},
			},
		},
	}}
}

func templateResource(name, image string) unstructured.Unstructured {
	return unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "template.openshift.io/v1",
		"kind":       "Template",
		"metadata": map[string]any{
			"name": name,
		},
		"objects": []any{
			map[string]any{
				"apiVersion": "serving.kserve.io/v1beta1",
				"kind":       "ServingRuntime",
				"spec": map[string]any{
					"containers": []any{
						map[string]any{
							"name":  "kserve-container",
							"image": image,
						},
					},
				},
			},
		},
	}}
}

func TestFilterFastResources_AllSameImage_BothFiltered(t *testing.T) {
	g := NewWithT(t)

	resources := []unstructured.Unstructured{
		llmISVCConfig("kserve-config-llm-nvidia-cuda", "registry.io/vllm@sha256:abc123"),
		llmISVCConfig("kserve-config-llm-nvidia-cuda-fast-1", "registry.io/vllm@sha256:abc123"),
		llmISVCConfig("kserve-config-llm-nvidia-cuda-fast-2", "registry.io/vllm@sha256:abc123"),
	}

	result := filterFastResources(resources)

	g.Expect(result).Should(HaveLen(1))
	g.Expect(result[0].GetName()).Should(Equal("kserve-config-llm-nvidia-cuda"))
}

func TestFilterFastResources_FastDiffersFromStable_SameFastImages_OneKept(t *testing.T) {
	g := NewWithT(t)

	resources := []unstructured.Unstructured{
		llmISVCConfig("kserve-config-llm-nvidia-cuda", "registry.io/vllm@sha256:stable"),
		llmISVCConfig("kserve-config-llm-nvidia-cuda-fast-1", "registry.io/vllm@sha256:patch1"),
		llmISVCConfig("kserve-config-llm-nvidia-cuda-fast-2", "registry.io/vllm@sha256:patch1"),
	}

	result := filterFastResources(resources)

	g.Expect(result).Should(HaveLen(2))
	names := []string{result[0].GetName(), result[1].GetName()}
	g.Expect(names).Should(ContainElements(
		"kserve-config-llm-nvidia-cuda",
		"kserve-config-llm-nvidia-cuda-fast-2",
	))
}

func TestFilterFastResources_AllDifferentImages_AllKept(t *testing.T) {
	g := NewWithT(t)

	resources := []unstructured.Unstructured{
		llmISVCConfig("kserve-config-llm-nvidia-cuda", "registry.io/vllm@sha256:stable"),
		llmISVCConfig("kserve-config-llm-nvidia-cuda-fast-1", "registry.io/vllm@sha256:patch1"),
		llmISVCConfig("kserve-config-llm-nvidia-cuda-fast-2", "registry.io/vllm@sha256:patch2"),
	}

	result := filterFastResources(resources)

	g.Expect(result).Should(HaveLen(3))
}

func TestFilterFastResources_OnlyFast1MatchesStable(t *testing.T) {
	g := NewWithT(t)

	resources := []unstructured.Unstructured{
		llmISVCConfig("kserve-config-llm-nvidia-cuda", "registry.io/vllm@sha256:stable"),
		llmISVCConfig("kserve-config-llm-nvidia-cuda-fast-1", "registry.io/vllm@sha256:stable"),
		llmISVCConfig("kserve-config-llm-nvidia-cuda-fast-2", "registry.io/vllm@sha256:patch"),
	}

	result := filterFastResources(resources)

	g.Expect(result).Should(HaveLen(2))
	names := []string{result[0].GetName(), result[1].GetName()}
	g.Expect(names).Should(ContainElements(
		"kserve-config-llm-nvidia-cuda",
		"kserve-config-llm-nvidia-cuda-fast-2",
	))
}

func TestFilterFastResources_TemplateResources_BothFiltered(t *testing.T) {
	g := NewWithT(t)

	resources := []unstructured.Unstructured{
		templateResource("nvidia-cuda-runtime", "registry.io/vllm@sha256:abc123"),
		templateResource("nvidia-cuda-runtime-fast-1", "registry.io/vllm@sha256:abc123"),
		templateResource("nvidia-cuda-runtime-fast-2", "registry.io/vllm@sha256:abc123"),
	}

	result := filterFastResources(resources)

	g.Expect(result).Should(HaveLen(1))
	g.Expect(result[0].GetName()).Should(Equal("nvidia-cuda-runtime"))
}

func TestFilterFastResources_TemplateResources_AllDifferent_AllKept(t *testing.T) {
	g := NewWithT(t)

	resources := []unstructured.Unstructured{
		templateResource("nvidia-cuda-runtime", "registry.io/vllm@sha256:stable"),
		templateResource("nvidia-cuda-runtime-fast-1", "registry.io/vllm@sha256:patch1"),
		templateResource("nvidia-cuda-runtime-fast-2", "registry.io/vllm@sha256:patch2"),
	}

	result := filterFastResources(resources)

	g.Expect(result).Should(HaveLen(3))
}

func TestFilterFastResources_NonFastResourcesUnchanged(t *testing.T) {
	g := NewWithT(t)

	resources := []unstructured.Unstructured{
		{Object: map[string]any{
			"apiVersion": "v1",
			"kind":       "ConfigMap",
			"metadata":   map[string]any{"name": "my-config"},
		}},
		{Object: map[string]any{
			"apiVersion": "apps/v1",
			"kind":       "Deployment",
			"metadata":   map[string]any{"name": "my-deploy"},
		}},
	}

	result := filterFastResources(resources)

	g.Expect(result).Should(HaveLen(2))
}

func TestFilterFastResources_NoStableCounterpart_FastKept(t *testing.T) {
	g := NewWithT(t)

	resources := []unstructured.Unstructured{
		llmISVCConfig("kserve-config-llm-nvidia-cuda-fast-1", "registry.io/vllm@sha256:abc123"),
	}

	result := filterFastResources(resources)

	g.Expect(result).Should(HaveLen(1))
	g.Expect(result[0].GetName()).Should(Equal("kserve-config-llm-nvidia-cuda-fast-1"))
}

func TestFilterFastResources_MixedResourceTypes(t *testing.T) {
	g := NewWithT(t)

	resources := []unstructured.Unstructured{
		llmISVCConfig("kserve-config-llm-nvidia-cuda", "registry.io/vllm@sha256:same"),
		llmISVCConfig("kserve-config-llm-nvidia-cuda-fast-1", "registry.io/vllm@sha256:same"),
		templateResource("nvidia-cuda-runtime", "registry.io/vllm@sha256:stable"),
		templateResource("nvidia-cuda-runtime-fast-1", "registry.io/vllm@sha256:patch"),
		{Object: map[string]any{
			"apiVersion": "v1",
			"kind":       "ConfigMap",
			"metadata":   map[string]any{"name": "unrelated"},
		}},
	}

	result := filterFastResources(resources)

	g.Expect(result).Should(HaveLen(4))
	var names []string
	for _, r := range result {
		names = append(names, r.GetName())
	}
	g.Expect(names).Should(ContainElements(
		"kserve-config-llm-nvidia-cuda",
		"nvidia-cuda-runtime",
		"nvidia-cuda-runtime-fast-1",
		"unrelated",
	))
	g.Expect(names).ShouldNot(ContainElement("kserve-config-llm-nvidia-cuda-fast-1"))
}

func TestFilterFastResources_EmptyInput(t *testing.T) {
	g := NewWithT(t)

	result := filterFastResources(nil)

	g.Expect(result).Should(BeEmpty())
}

func TestFilterFastResources_Fast2OnlyMatchesStable(t *testing.T) {
	g := NewWithT(t)

	resources := []unstructured.Unstructured{
		llmISVCConfig("kserve-config-llm-nvidia-cuda", "registry.io/vllm@sha256:stable"),
		llmISVCConfig("kserve-config-llm-nvidia-cuda-fast-1", "registry.io/vllm@sha256:patch"),
		llmISVCConfig("kserve-config-llm-nvidia-cuda-fast-2", "registry.io/vllm@sha256:stable"),
	}

	result := filterFastResources(resources)

	g.Expect(result).Should(HaveLen(2))
	names := []string{result[0].GetName(), result[1].GetName()}
	g.Expect(names).Should(ContainElements(
		"kserve-config-llm-nvidia-cuda",
		"kserve-config-llm-nvidia-cuda-fast-1",
	))
}

func TestParseFastSuffix(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantBase string
		wantSfx  string
		wantFast bool
	}{
		{"fast-1 suffix", "config-nvidia-cuda-fast-1", "config-nvidia-cuda", "-fast-1", true},
		{"fast-2 suffix", "config-nvidia-cuda-fast-2", "config-nvidia-cuda", "-fast-2", true},
		{"no fast suffix", "config-nvidia-cuda", "config-nvidia-cuda", "", false},
		{"fast-3 not recognized", "config-fast-3", "config-fast-3", "", false},
		{"fast in middle", "fast-1-config", "fast-1-config", "", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)
			base, sfx, isFast := parseFastSuffix(tc.input)
			g.Expect(base).Should(Equal(tc.wantBase))
			g.Expect(sfx).Should(Equal(tc.wantSfx))
			g.Expect(isFast).Should(Equal(tc.wantFast))
		})
	}
}

func TestExtractImage_LLMInferenceServiceConfig(t *testing.T) {
	g := NewWithT(t)

	r := llmISVCConfig("test", "registry.io/image:v1")
	g.Expect(extractImage(r)).Should(Equal("registry.io/image:v1"))
}

func TestExtractImage_Template(t *testing.T) {
	g := NewWithT(t)

	r := templateResource("test", "registry.io/image:v1")
	g.Expect(extractImage(r)).Should(Equal("registry.io/image:v1"))
}

func TestExtractImage_UnknownKind(t *testing.T) {
	g := NewWithT(t)

	r := unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]any{"name": "test"},
	}}
	g.Expect(extractImage(r)).Should(BeEmpty())
}

func TestFilterFastResources_MultipleBaseResources(t *testing.T) {
	g := NewWithT(t)

	resources := []unstructured.Unstructured{
		llmISVCConfig("kserve-config-nvidia-cuda", "registry.io/cuda@sha256:stable"),
		llmISVCConfig("kserve-config-nvidia-cuda-fast-1", "registry.io/cuda@sha256:stable"),
		llmISVCConfig("kserve-config-amd-rocm", "registry.io/rocm@sha256:stable"),
		llmISVCConfig("kserve-config-amd-rocm-fast-1", "registry.io/rocm@sha256:patch"),
	}

	result := filterFastResources(resources)

	g.Expect(result).Should(HaveLen(3))
	var names []string
	for _, r := range result {
		names = append(names, r.GetName())
	}
	g.Expect(names).Should(ContainElements(
		"kserve-config-nvidia-cuda",
		"kserve-config-amd-rocm",
		"kserve-config-amd-rocm-fast-1",
	))
	g.Expect(names).ShouldNot(ContainElement("kserve-config-nvidia-cuda-fast-1"))
}
