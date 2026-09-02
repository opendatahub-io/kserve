package kservemodule

import (
	"context"
	"errors"
	"testing"

	. "github.com/onsi/gomega"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func monitoringResource(traces map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": monitoringGVK.GroupVersion().String(),
		"kind":       monitoringKind,
		"metadata":   map[string]any{"name": monitoringCRName},
		"spec":       map[string]any{"traces": traces},
	}}
}

func TestResolveTracingPlatformConfig(t *testing.T) {
	tests := []struct {
		name       string
		monitoring *unstructured.Unstructured
		expected   *tracingPlatformConfig
	}{
		{name: "monitoring absent"},
		{name: "traces absent", monitoring: &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": monitoringGVK.GroupVersion().String(), "kind": monitoringKind,
			"metadata": map[string]any{"name": monitoringCRName},
		}}},
		{name: "traces null", monitoring: &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": monitoringGVK.GroupVersion().String(), "kind": monitoringKind,
			"metadata": map[string]any{"name": monitoringCRName},
			"spec":     map[string]any{"traces": nil},
		}}},
		{name: "traces empty", monitoring: monitoringResource(map[string]any{}), expected: &tracingPlatformConfig{
			Enabled: true, SampleRatio: defaultTracesSampleRatio,
			Endpoint: "http://data-science-collector-collector.redhat-ods-monitoring.svc:4317",
		}},
		{name: "explicit sample ratio", monitoring: monitoringResource(map[string]any{"sampleRatio": "0.5"}), expected: &tracingPlatformConfig{
			Enabled: true, SampleRatio: "0.5",
			Endpoint: "http://data-science-collector-collector.redhat-ods-monitoring.svc:4317",
		}},
		{name: "invalid sample ratio", monitoring: monitoringResource(map[string]any{"sampleRatio": "not-a-number"}), expected: &tracingPlatformConfig{
			Enabled: true, SampleRatio: invalidTracesSampleRatio,
			Endpoint: "http://data-science-collector-collector.redhat-ods-monitoring.svc:4317",
		}},
		{name: "NaN sample ratio", monitoring: monitoringResource(map[string]any{"sampleRatio": "NaN"}), expected: &tracingPlatformConfig{
			Enabled: true, SampleRatio: invalidTracesSampleRatio,
			Endpoint: "http://data-science-collector-collector.redhat-ods-monitoring.svc:4317",
		}},
		{name: "+Inf sample ratio", monitoring: monitoringResource(map[string]any{"sampleRatio": "+Inf"}), expected: &tracingPlatformConfig{
			Enabled: true, SampleRatio: invalidTracesSampleRatio,
			Endpoint: "http://data-science-collector-collector.redhat-ods-monitoring.svc:4317",
		}},
		{name: "out-of-range sample ratio", monitoring: monitoringResource(map[string]any{"sampleRatio": "2"}), expected: &tracingPlatformConfig{
			Enabled: true, SampleRatio: invalidTracesSampleRatio,
			Endpoint: "http://data-science-collector-collector.redhat-ods-monitoring.svc:4317",
		}},
		{name: "zero sample ratio", monitoring: monitoringResource(map[string]any{"sampleRatio": "0"}), expected: &tracingPlatformConfig{
			Enabled: true, SampleRatio: "0",
			Endpoint: "http://data-science-collector-collector.redhat-ods-monitoring.svc:4317",
		}},
		{name: "one sample ratio", monitoring: monitoringResource(map[string]any{"sampleRatio": "1"}), expected: &tracingPlatformConfig{
			Enabled: true, SampleRatio: "1",
			Endpoint: "http://data-science-collector-collector.redhat-ods-monitoring.svc:4317",
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)
			t.Setenv("MONITORING_NAMESPACE", "redhat-ods-monitoring")
			var objects []client.Object
			if tt.monitoring != nil {
				objects = append(objects, tt.monitoring)
			}
			r := &KserveModuleReconciler{Client: fake.NewClientBuilder().WithObjects(objects...).Build()}

			actual, err := r.resolveTracingPlatformConfig(context.Background())
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(actual).To(Equal(tt.expected))
		})
	}
}

func TestResolveTracingPlatformConfig_DefaultMonitoringNamespace(t *testing.T) {
	g := NewWithT(t)
	t.Setenv("MONITORING_NAMESPACE", "")
	monitoring := monitoringResource(map[string]any{})
	r := &KserveModuleReconciler{Client: fake.NewClientBuilder().WithObjects(monitoring).Build()}

	config, err := r.resolveTracingPlatformConfig(context.Background())
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(config.Endpoint).To(Equal("http://data-science-collector-collector.opendatahub.svc:4317"))
}

func TestResolveTracingPlatformConfig_PropagatesAPIError(t *testing.T) {
	g := NewWithT(t)
	wantErr := errors.New("api unavailable")
	cli := fake.NewClientBuilder().WithInterceptorFuncs(interceptor.Funcs{
		Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
			return wantErr
		},
	}).Build()
	r := &KserveModuleReconciler{Client: cli}

	_, err := r.resolveTracingPlatformConfig(context.Background())
	g.Expect(err).To(MatchError(wantErr.Error()))
}

func TestResolveTracingPlatformConfig_SkipsUnavailableAPI(t *testing.T) {
	g := NewWithT(t)
	cli := fake.NewClientBuilder().WithInterceptorFuncs(interceptor.Funcs{
		Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
			return &apiMeta.NoKindMatchError{GroupKind: monitoringGVK.GroupKind(), SearchedVersions: []string{monitoringAPIVersion}}
		},
	}).Build()
	r := &KserveModuleReconciler{Client: cli}

	config, err := r.resolveTracingPlatformConfig(context.Background())
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(config).To(BeNil())
}

func TestMonitoringWatchFiltersSingleton(t *testing.T) {
	g := NewWithT(t)
	r := &KserveModuleReconciler{}
	for _, watch := range r.buildDynamicWatches() {
		if watch.groupKind != monitoringGVK.GroupKind() {
			continue
		}
		g.Expect(watch.filterFn(&unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"name": monitoringCRName}}})).To(BeTrue())
		g.Expect(watch.filterFn(&unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"name": "other-monitoring"}}})).To(BeFalse())
		return
	}
	t.Fatal("Monitoring watch not found")
}

func TestMonitoringGVK(t *testing.T) {
	g := NewWithT(t)
	g.Expect(schema.GroupVersionKind{Group: monitoringAPIGroup, Version: monitoringAPIVersion, Kind: monitoringKind}).To(Equal(monitoringGVK))
}
