package kservemodule

import (
	"testing"

	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func tracingPreset(name string, wellKnown bool) unstructured.Unstructured {
	annotations := map[string]any{}
	if wellKnown {
		annotations[wellKnownAnnotationKey] = wellKnownAnnotationValue
	}
	return unstructured.Unstructured{Object: map[string]any{
		"apiVersion": llmISVCConfigGVK.GroupVersion().String(),
		"kind":       llmISVCConfigKind,
		"metadata": map[string]any{
			"name": name, "annotations": annotations,
		},
		"spec": map[string]any{"tracing": map[string]any{
			"exporterEndpoint": "http://otel-collector:4317",
			"samplerArg":       "0.05",
		}},
	}}
}

func TestPatchWellKnownTracingPreset(t *testing.T) {
	g := NewWithT(t)
	cfg := &tracingPlatformConfig{Enabled: true, SampleRatio: "0.1", Endpoint: "http://collector.ns.svc:4317"}
	resources := []unstructured.Unstructured{
		tracingPreset("v1-2-3-kserve-config-llm-tracing", true),
		tracingPreset("v1-2-3-kserve-config-llm-decode", true),
		tracingPreset("v1-2-3-kserve-config-llm-tracing-copy", false),
	}

	patched, err := patchWellKnownTracingPreset(resources, cfg)
	g.Expect(err).NotTo(HaveOccurred())
	exporter, _, _ := unstructured.NestedString(patched[0].Object, "spec", "tracing", "exporter")
	endpoint, _, _ := unstructured.NestedString(patched[0].Object, "spec", "tracing", "exporterEndpoint")
	sampler, _, _ := unstructured.NestedString(patched[0].Object, "spec", "tracing", "sampler")
	ratio, _, _ := unstructured.NestedString(patched[0].Object, "spec", "tracing", "samplerArg")
	g.Expect([]string{exporter, endpoint, sampler, ratio}).To(Equal([]string{tracingExporter, cfg.Endpoint, tracingSampler, cfg.SampleRatio}))
	unchanged, _, _ := unstructured.NestedString(patched[1].Object, "spec", "tracing", "exporterEndpoint")
	g.Expect(unchanged).To(Equal("http://otel-collector:4317"))
	unchanged, _, _ = unstructured.NestedString(patched[2].Object, "spec", "tracing", "exporterEndpoint")
	g.Expect(unchanged).To(Equal("http://otel-collector:4317"))
}

func TestPatchWellKnownTracingPreset_SkipsWhenDisabled(t *testing.T) {
	g := NewWithT(t)
	resources := []unstructured.Unstructured{tracingPreset("v1-2-3-kserve-config-llm-tracing", true)}
	for _, cfg := range []*tracingPlatformConfig{nil, {Enabled: false}} {
		patched, err := patchWellKnownTracingPreset(resources, cfg)
		g.Expect(err).NotTo(HaveOccurred())
		endpoint, _, _ := unstructured.NestedString(patched[0].Object, "spec", "tracing", "exporterEndpoint")
		g.Expect(endpoint).To(Equal("http://otel-collector:4317"))
	}
}
