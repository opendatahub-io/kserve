package kservemodule

import (
	"context"
	"strings"

	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func isWellKnownTracingPreset(obj *unstructured.Unstructured) bool {
	return isWellKnownConfig(obj) && strings.HasSuffix(obj.GetName(), tracingPresetSuffix)
}

// includeExistingTracingPresets adds minimal apply objects for historical
// versioned presets so their platform-controlled tracing fields stay current.
func (r *KserveModuleReconciler) includeExistingTracingPresets(ctx context.Context, resources []unstructured.Unstructured) ([]unstructured.Unstructured, error) {
	knownNames := make(map[string]struct{}, len(resources))
	for i := range resources {
		knownNames[resources[i].GetName()] = struct{}{}
	}

	presets := &unstructured.UnstructuredList{}
	presets.SetGroupVersionKind(llmISVCConfigListGVK)
	if err := r.List(ctx, presets, client.InNamespace(r.getApplicationsNamespace())); err != nil {
		if apiMeta.IsNoMatchError(err) {
			return resources, nil
		}
		return nil, err
	}

	for i := range presets.Items {
		preset := &presets.Items[i]
		if !isWellKnownTracingPreset(preset) {
			continue
		}
		if _, exists := knownNames[preset.GetName()]; exists {
			continue
		}

		resource := unstructured.Unstructured{Object: map[string]any{
			"apiVersion": llmISVCConfigGVK.GroupVersion().String(),
			"kind":       llmISVCConfigKind,
			"metadata": map[string]any{
				"name":      preset.GetName(),
				"namespace": r.getApplicationsNamespace(),
				"annotations": map[string]any{
					wellKnownAnnotationKey: wellKnownAnnotationValue,
				},
			},
		}}
		resources = append(resources, resource)
		knownNames[preset.GetName()] = struct{}{}
	}

	return resources, nil
}

func patchWellKnownTracingPreset(resources []unstructured.Unstructured, cfg *tracingPlatformConfig) ([]unstructured.Unstructured, error) {
	if cfg == nil || !cfg.Enabled {
		return resources, nil
	}

	for i := range resources {
		if !isWellKnownTracingPreset(&resources[i]) {
			continue
		}
		fields := map[string]string{
			"exporterEndpoint": cfg.Endpoint,
			"samplerArg":       cfg.SampleRatio,
		}
		for field, value := range fields {
			if err := unstructured.SetNestedField(resources[i].Object, value, "spec", "tracing", field); err != nil {
				return nil, err
			}
		}
	}
	return resources, nil
}
