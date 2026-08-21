package kservemodule

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// presetObject builds an LLMInferenceServiceConfig the way the module ships
// them. Built from llmISVCConfigGVK so a version bump cannot leave the tests
// asserting against an apiVersion nothing serves.
func presetObject(namespace string, annotations map[string]any) *unstructured.Unstructured {
	metadata := map[string]any{"name": "v1-2-3-kserve-config-llm-decode", "namespace": namespace}
	if annotations != nil {
		metadata["annotations"] = annotations
	}

	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": llmISVCConfigGVK.GroupVersion().String(),
		"kind":       llmISVCConfigGVK.Kind,
		"metadata":   metadata,
	}}
}
