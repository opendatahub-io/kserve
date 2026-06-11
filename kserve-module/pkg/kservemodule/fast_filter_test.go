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

func TestFilterFastResources(t *testing.T) {
	tests := []struct {
		name          string
		resources     []unstructured.Unstructured
		wantNames     []string
		dontWantNames []string
	}{
		{
			name: "all same image, both fast variants filtered",
			resources: []unstructured.Unstructured{
				llmISVCConfig("kserve-config-llm-nvidia-cuda", "registry.io/vllm@sha256:abc123"),
				llmISVCConfig("kserve-config-llm-nvidia-cuda-fast-1", "registry.io/vllm@sha256:abc123"),
				llmISVCConfig("kserve-config-llm-nvidia-cuda-fast-2", "registry.io/vllm@sha256:abc123"),
			},
			wantNames: []string{"kserve-config-llm-nvidia-cuda"},
		},
		{
			name: "fast differs from stable, same fast images, one kept",
			resources: []unstructured.Unstructured{
				llmISVCConfig("kserve-config-llm-nvidia-cuda", "registry.io/vllm@sha256:stable"),
				llmISVCConfig("kserve-config-llm-nvidia-cuda-fast-1", "registry.io/vllm@sha256:patch1"),
				llmISVCConfig("kserve-config-llm-nvidia-cuda-fast-2", "registry.io/vllm@sha256:patch1"),
			},
			wantNames: []string{
				"kserve-config-llm-nvidia-cuda",
				"kserve-config-llm-nvidia-cuda-fast-2",
			},
		},
		{
			name: "all different images, all kept",
			resources: []unstructured.Unstructured{
				llmISVCConfig("kserve-config-llm-nvidia-cuda", "registry.io/vllm@sha256:stable"),
				llmISVCConfig("kserve-config-llm-nvidia-cuda-fast-1", "registry.io/vllm@sha256:patch1"),
				llmISVCConfig("kserve-config-llm-nvidia-cuda-fast-2", "registry.io/vllm@sha256:patch2"),
			},
			wantNames: []string{
				"kserve-config-llm-nvidia-cuda",
				"kserve-config-llm-nvidia-cuda-fast-1",
				"kserve-config-llm-nvidia-cuda-fast-2",
			},
		},
		{
			name: "only fast-1 matches stable",
			resources: []unstructured.Unstructured{
				llmISVCConfig("kserve-config-llm-nvidia-cuda", "registry.io/vllm@sha256:stable"),
				llmISVCConfig("kserve-config-llm-nvidia-cuda-fast-1", "registry.io/vllm@sha256:stable"),
				llmISVCConfig("kserve-config-llm-nvidia-cuda-fast-2", "registry.io/vllm@sha256:patch"),
			},
			wantNames: []string{
				"kserve-config-llm-nvidia-cuda",
				"kserve-config-llm-nvidia-cuda-fast-2",
			},
		},
		{
			name: "only fast-2 matches stable",
			resources: []unstructured.Unstructured{
				llmISVCConfig("kserve-config-llm-nvidia-cuda", "registry.io/vllm@sha256:stable"),
				llmISVCConfig("kserve-config-llm-nvidia-cuda-fast-1", "registry.io/vllm@sha256:patch"),
				llmISVCConfig("kserve-config-llm-nvidia-cuda-fast-2", "registry.io/vllm@sha256:stable"),
			},
			wantNames: []string{
				"kserve-config-llm-nvidia-cuda",
				"kserve-config-llm-nvidia-cuda-fast-1",
			},
		},
		{
			name: "no stable counterpart, fast kept",
			resources: []unstructured.Unstructured{
				llmISVCConfig("kserve-config-llm-nvidia-cuda-fast-1", "registry.io/vllm@sha256:abc123"),
			},
			wantNames: []string{"kserve-config-llm-nvidia-cuda-fast-1"},
		},
		{
			name:      "empty input",
			resources: nil,
			wantNames: nil,
		},
		{
			name: "template resources, all same image, both filtered",
			resources: []unstructured.Unstructured{
				templateResource("nvidia-cuda-runtime", "registry.io/vllm@sha256:abc123"),
				templateResource("nvidia-cuda-runtime-fast-1", "registry.io/vllm@sha256:abc123"),
				templateResource("nvidia-cuda-runtime-fast-2", "registry.io/vllm@sha256:abc123"),
			},
			wantNames: []string{"nvidia-cuda-runtime"},
		},
		{
			name: "template resources, all different, all kept",
			resources: []unstructured.Unstructured{
				templateResource("nvidia-cuda-runtime", "registry.io/vllm@sha256:stable"),
				templateResource("nvidia-cuda-runtime-fast-1", "registry.io/vllm@sha256:patch1"),
				templateResource("nvidia-cuda-runtime-fast-2", "registry.io/vllm@sha256:patch2"),
			},
			wantNames: []string{
				"nvidia-cuda-runtime",
				"nvidia-cuda-runtime-fast-1",
				"nvidia-cuda-runtime-fast-2",
			},
		},
		{
			name: "non-fast resources unchanged",
			resources: []unstructured.Unstructured{
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
			},
			wantNames: []string{"my-config", "my-deploy"},
		},
		{
			name: "mixed resource types",
			resources: []unstructured.Unstructured{
				llmISVCConfig("kserve-config-llm-nvidia-cuda", "registry.io/vllm@sha256:same"),
				llmISVCConfig("kserve-config-llm-nvidia-cuda-fast-1", "registry.io/vllm@sha256:same"),
				templateResource("nvidia-cuda-runtime", "registry.io/vllm@sha256:stable"),
				templateResource("nvidia-cuda-runtime-fast-1", "registry.io/vllm@sha256:patch"),
				{Object: map[string]any{
					"apiVersion": "v1",
					"kind":       "ConfigMap",
					"metadata":   map[string]any{"name": "unrelated"},
				}},
			},
			wantNames: []string{
				"kserve-config-llm-nvidia-cuda",
				"nvidia-cuda-runtime",
				"nvidia-cuda-runtime-fast-1",
				"unrelated",
			},
			dontWantNames: []string{"kserve-config-llm-nvidia-cuda-fast-1"},
		},
		{
			name: "multiple base resources",
			resources: []unstructured.Unstructured{
				llmISVCConfig("kserve-config-nvidia-cuda", "registry.io/cuda@sha256:stable"),
				llmISVCConfig("kserve-config-nvidia-cuda-fast-1", "registry.io/cuda@sha256:stable"),
				llmISVCConfig("kserve-config-amd-rocm", "registry.io/rocm@sha256:stable"),
				llmISVCConfig("kserve-config-amd-rocm-fast-1", "registry.io/rocm@sha256:patch"),
			},
			wantNames: []string{
				"kserve-config-nvidia-cuda",
				"kserve-config-amd-rocm",
				"kserve-config-amd-rocm-fast-1",
			},
			dontWantNames: []string{"kserve-config-nvidia-cuda-fast-1"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)
			result := filterFastResources(tc.resources)

			if tc.wantNames == nil {
				g.Expect(result).Should(BeEmpty())
				return
			}

			g.Expect(result).Should(HaveLen(len(tc.wantNames)))
			var names []string
			for _, r := range result {
				names = append(names, r.GetName())
			}
			g.Expect(names).Should(ContainElements(tc.wantNames))
			for _, name := range tc.dontWantNames {
				g.Expect(names).ShouldNot(ContainElement(name))
			}
		})
	}
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

func TestExtractImage(t *testing.T) {
	tests := []struct {
		name      string
		resource  unstructured.Unstructured
		wantImage string
	}{
		{
			name:      "LLMInferenceServiceConfig",
			resource:  llmISVCConfig("test", "registry.io/image:v1"),
			wantImage: "registry.io/image:v1",
		},
		{
			name:      "Template",
			resource:  templateResource("test", "registry.io/image:v1"),
			wantImage: "registry.io/image:v1",
		},
		{
			name: "unknown kind",
			resource: unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "v1",
				"kind":       "ConfigMap",
				"metadata":   map[string]any{"name": "test"},
			}},
			wantImage: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)
			g.Expect(extractImage(tc.resource)).Should(Equal(tc.wantImage))
		})
	}
}
