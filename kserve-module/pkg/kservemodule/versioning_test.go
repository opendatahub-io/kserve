package kservemodule

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	. "github.com/onsi/gomega"
)

func buildLLMISVCDeployment(t *testing.T) []unstructured.Unstructured {
	t.Helper()
	g := NewWithT(t)

	deploy := &appsv1.Deployment{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
		ObjectMeta: metav1.ObjectMeta{Name: llmISVCControllerDeployment, Namespace: "opendatahub"},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "manager", Image: "test:latest"},
					},
				},
			},
		},
	}
	u, err := runtime.DefaultUnstructuredConverter.ToUnstructured(deploy)
	g.Expect(err).ShouldNot(HaveOccurred())

	return []unstructured.Unstructured{{Object: u}}
}

// The preset watch filters on this, so a false here means a deleted preset is
// never recreated, and a false positive means user copies drive reconciles.
func TestIsShippedPreset(t *testing.T) {
	preset := func(namespace string, annotations map[string]any) *unstructured.Unstructured {
		metadata := map[string]any{"name": "v1-2-3-kserve-config-llm-decode", "namespace": namespace}
		if annotations != nil {
			metadata["annotations"] = annotations
		}
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "serving.kserve.io/v1alpha2",
			"kind":       "LLMInferenceServiceConfig",
			"metadata":   metadata,
		}}
	}
	wellKnown := map[string]any{wellKnownAnnotationKey: wellKnownAnnotationValue}

	testCases := map[string]struct {
		obj      *unstructured.Unstructured
		expected bool
	}{
		"shipped preset":                            {obj: preset("opendatahub", wellKnown), expected: true},
		"user copy in another namespace":            {obj: preset("user-models", wellKnown), expected: false},
		"user config in the applications namespace": {obj: preset("opendatahub", nil), expected: false},
		"other kind carrying the annotation": {
			obj: &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "v1",
				"kind":       "ConfigMap",
				"metadata": map[string]any{
					"name": "not-a-preset", "namespace": "opendatahub", "annotations": wellKnown,
				},
			}},
			expected: false,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			NewWithT(t).Expect(isShippedPreset(tc.obj, "opendatahub")).Should(Equal(tc.expected))
		})
	}
}

func TestWellKnownPresetNames_SkipsUserConfigsAndOtherKinds(t *testing.T) {
	g := NewWithT(t)

	resources := []unstructured.Unstructured{
		{Object: map[string]any{
			"apiVersion": "serving.kserve.io/v1alpha2",
			"kind":       "LLMInferenceServiceConfig",
			"metadata": map[string]any{
				"name":        "v1-2-3-kserve-config-llm-decode",
				"annotations": map[string]any{wellKnownAnnotationKey: wellKnownAnnotationValue},
			},
		}},
		{Object: map[string]any{
			"apiVersion": "serving.kserve.io/v1alpha2",
			"kind":       "LLMInferenceServiceConfig",
			"metadata":   map[string]any{"name": "my-own-config"},
		}},
		{Object: map[string]any{
			"apiVersion": "v1",
			"kind":       "ConfigMap",
			"metadata": map[string]any{
				"name":        "not-a-preset",
				"annotations": map[string]any{wellKnownAnnotationKey: wellKnownAnnotationValue},
			},
		}},
	}

	g.Expect(wellKnownPresetNames(resources)).Should(ConsistOf("v1-2-3-kserve-config-llm-decode"))
}

func TestVersionLLMInferenceServiceConfigs_RenamesWellKnown(t *testing.T) {
	g := NewWithT(t)

	resources := []unstructured.Unstructured{
		{Object: map[string]any{
			"apiVersion": "serving.kserve.io/v1alpha1",
			"kind":       "LLMInferenceServiceConfig",
			"metadata": map[string]any{
				"name": "kserve-config-llm-template",
				"annotations": map[string]any{
					wellKnownAnnotationKey: wellKnownAnnotationValue,
				},
			},
		}},
	}

	result, err := versionedWellKnownLLMInferenceServiceConfigs(resources, "v3-4-0")
	g.Expect(err).ShouldNot(HaveOccurred())
	g.Expect(result[0].GetName()).Should(Equal("v3-4-0-kserve-config-llm-template"))
}

func TestVersionLLMInferenceServiceConfigs_SkipsNonWellKnown(t *testing.T) {
	g := NewWithT(t)

	resources := []unstructured.Unstructured{
		{Object: map[string]any{
			"apiVersion": "serving.kserve.io/v1alpha1",
			"kind":       "LLMInferenceServiceConfig",
			"metadata": map[string]any{
				"name": "custom-config",
			},
		}},
	}

	result, err := versionedWellKnownLLMInferenceServiceConfigs(resources, "v3-4-0")
	g.Expect(err).ShouldNot(HaveOccurred())
	g.Expect(result[0].GetName()).Should(Equal("custom-config"))
}

func TestVersionLLMInferenceServiceConfigs_SetsEnvOnLLMISVCController(t *testing.T) {
	g := NewWithT(t)

	resources := buildLLMISVCDeployment(t)

	result, err := versionedWellKnownLLMInferenceServiceConfigs(resources, "v3-4-0")
	g.Expect(err).ShouldNot(HaveOccurred())

	for _, r := range result {
		if r.GetKind() == "Deployment" && r.GetName() == llmISVCControllerDeployment {
			containers, _, _ := unstructured.NestedSlice(r.Object, "spec", "template", "spec", "containers")
			g.Expect(containers).ShouldNot(BeEmpty())
			c := containers[0].(map[string]any)
			envs := c["env"].([]any)
			found := false
			for _, e := range envs {
				env := e.(map[string]any)
				if env["name"] == llmISVCConfigPrefixEnv {
					g.Expect(env["value"]).Should(Equal("v3-4-0-kserve-"))
					found = true
				}
			}
			g.Expect(found).Should(BeTrue(), "LLM_INFERENCE_SERVICE_CONFIG_PREFIX env not found")
			return
		}
	}
	t.Fatal("llmisvc deployment not found in result")
}

func TestVersionLLMInferenceServiceConfigs_SkipsOtherDeployments(t *testing.T) {
	g := NewWithT(t)

	deploy := &appsv1.Deployment{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
		ObjectMeta: metav1.ObjectMeta{Name: "kserve-controller-manager"},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "manager", Image: "test:latest"},
					},
				},
			},
		},
	}
	u, err := runtime.DefaultUnstructuredConverter.ToUnstructured(deploy)
	g.Expect(err).ShouldNot(HaveOccurred())

	resources := []unstructured.Unstructured{{Object: u}}
	result, err := versionedWellKnownLLMInferenceServiceConfigs(resources, "v3-4-0")
	g.Expect(err).ShouldNot(HaveOccurred())

	containers, _, _ := unstructured.NestedSlice(result[0].Object, "spec", "template", "spec", "containers")
	c := containers[0].(map[string]any)
	_, hasEnv := c["env"]
	g.Expect(hasEnv).Should(BeFalse(), "kserve-controller-manager should not get LLM prefix env")
}

func TestVersionLLMInferenceServiceConfigs_EmptyPrefix(t *testing.T) {
	g := NewWithT(t)

	resources := []unstructured.Unstructured{
		{Object: map[string]any{
			"apiVersion": "serving.kserve.io/v1alpha1",
			"kind":       "LLMInferenceServiceConfig",
			"metadata": map[string]any{
				"name": "kserve-config-llm-template",
				"annotations": map[string]any{
					wellKnownAnnotationKey: wellKnownAnnotationValue,
				},
			},
		}},
	}

	result, err := versionedWellKnownLLMInferenceServiceConfigs(resources, "")
	g.Expect(err).ShouldNot(HaveOccurred())
	g.Expect(result[0].GetName()).Should(Equal("kserve-config-llm-template"))
}
