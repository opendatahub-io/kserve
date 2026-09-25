package kservemodule

import (
	"context"
	"fmt"
	"math"
	"strconv"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	monitoringCRName             = "default-monitoring"
	monitoringAPIGroup           = "services.platform.opendatahub.io"
	monitoringAPIVersion         = "v1alpha1"
	monitoringKind               = "Monitoring"
	defaultTracesSampleRatio     = "0.1"
	platformCollectorServiceName = "data-science-collector-collector"
	platformCollectorPort        = 4317
	upstreamTracingEndpoint      = "http://otel-collector:4317"
	tracingPresetSuffix          = "kserve-config-llm-tracing"
)

var monitoringGVK = schema.GroupVersionKind{
	Group: monitoringAPIGroup, Version: monitoringAPIVersion, Kind: monitoringKind,
}

type tracingPlatformConfig struct {
	Enabled     bool
	SampleRatio string
	Endpoint    string
}

func (r *KserveModuleReconciler) resolveTracingPlatformConfig(ctx context.Context) (*tracingPlatformConfig, error) {
	monitoring := &unstructured.Unstructured{}
	monitoring.SetGroupVersionKind(monitoringGVK)
	if err := r.Get(ctx, client.ObjectKey{Name: monitoringCRName}, monitoring); err != nil {
		if apierrors.IsNotFound(err) || apiMeta.IsNoMatchError(err) {
			return nil, nil
		}
		return nil, err
	}

	tracesValue, found, err := unstructured.NestedFieldNoCopy(monitoring.Object, "spec", "traces")
	if err != nil {
		return nil, err
	}
	if !found || tracesValue == nil {
		return &tracingPlatformConfig{}, nil
	}
	traces, ok := tracesValue.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("spec.traces must be an object")
	}
	if traces == nil {
		return &tracingPlatformConfig{}, nil
	}

	ratio, _, err := unstructured.NestedString(traces, "sampleRatio")
	if err != nil {
		return nil, err
	}
	if ratio == "" {
		ratio = defaultTracesSampleRatio
	} else if !validTracesSampleRatio(ratio) {
		// Treat malformed optional values like omitted values so the preset never receives an invalid sampler argument.
		ctrl.LoggerFrom(ctx).Info("invalid monitoring trace sample ratio, using default", "sampleRatio", ratio, "default", defaultTracesSampleRatio)
		ratio = defaultTracesSampleRatio
	}

	return &tracingPlatformConfig{
		Enabled:     true,
		SampleRatio: ratio,
		// The platform collector exposes the OTLP/gRPC endpoint on its fixed service port.
		Endpoint: fmt.Sprintf("http://%s.%s.svc:%d", platformCollectorServiceName, r.getMonitoringNamespace(), platformCollectorPort),
	}, nil
}

func validTracesSampleRatio(ratio string) bool {
	parsed, err := strconv.ParseFloat(ratio, 64)
	return err == nil && !math.IsNaN(parsed) && !math.IsInf(parsed, 0) && parsed >= 0 && parsed <= 1
}
