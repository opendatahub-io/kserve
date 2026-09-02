package kservemodule

import (
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func isWellKnownTracingPreset(obj *unstructured.Unstructured) bool {
	return isWellKnownConfig(obj) && strings.HasSuffix(obj.GetName(), tracingPresetSuffix)
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
			"exporter":         tracingExporter,
			"exporterEndpoint": cfg.Endpoint,
			"sampler":          tracingSampler,
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
