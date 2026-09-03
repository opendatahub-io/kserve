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
			"exporter":         "otlp",
			"exporterEndpoint": "http://otel-collector:4317",
			"sampler":          "parentbased_traceidratio",
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
	endpoint, _, _ := unstructured.NestedString(patched[0].Object, "spec", "tracing", "exporterEndpoint")
	ratio, _, _ := unstructured.NestedString(patched[0].Object, "spec", "tracing", "samplerArg")
	g.Expect([]string{endpoint, ratio}).To(Equal([]string{cfg.Endpoint, cfg.SampleRatio}))
	exporter, _, _ := unstructured.NestedString(patched[0].Object, "spec", "tracing", "exporter")
	sampler, _, _ := unstructured.NestedString(patched[0].Object, "spec", "tracing", "sampler")
	g.Expect(exporter).To(Equal("otlp"))
	g.Expect(sampler).To(Equal("parentbased_traceidratio"))
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
