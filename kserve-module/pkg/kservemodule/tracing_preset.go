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

func upstreamTracingEndpointFromResources(resources []unstructured.Unstructured) string {
	for i := range resources {
		if !isWellKnownTracingPreset(&resources[i]) {
			continue
		}
		endpoint, found, err := unstructured.NestedString(resources[i].Object, "spec", "tracing", "exporterEndpoint")
		if err == nil && found && endpoint != "" {
			return endpoint
		}
	}
	return upstreamTracingEndpoint
}

// includeExistingTracingPresets adds existing specs for historical versioned
// presets so endpoint updates do not cause server-side apply to remove fields
// omitted from a minimal desired object.
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
		if spec, found, err := unstructured.NestedFieldCopy(preset.Object, "spec"); err != nil {
			return nil, err
		} else if found {
			resource.Object["spec"] = spec
		}
		resources = append(resources, resource)
		knownNames[preset.GetName()] = struct{}{}
	}

	return resources, nil
}

func patchWellKnownTracingPreset(resources []unstructured.Unstructured, cfg *tracingPlatformConfig) ([]unstructured.Unstructured, error) {
	if cfg == nil || !cfg.Enabled {
		return resources, nil
	}
	return patchWellKnownTracingPresetFields(resources, map[string]string{
		"exporterEndpoint": cfg.Endpoint,
		"samplerArg":       cfg.SampleRatio,
	})
}

func patchWellKnownTracingPresetEndpoint(resources []unstructured.Unstructured, endpoint string) ([]unstructured.Unstructured, error) {
	return patchWellKnownTracingPresetFields(resources, map[string]string{
		"exporterEndpoint": endpoint,
	})
}

func patchWellKnownTracingPresetFields(resources []unstructured.Unstructured, fields map[string]string) ([]unstructured.Unstructured, error) {
	for i := range resources {
		if !isWellKnownTracingPreset(&resources[i]) {
			continue
		}
		for field, value := range fields {
			if err := unstructured.SetNestedField(resources[i].Object, value, "spec", "tracing", field); err != nil {
				return nil, err
			}
		}
	}
	return resources, nil
}
