/*
Copyright 2025 The KServe Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package llmisvc

import (
	"context"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
)

// WorkloadMetricsCollector implements prometheus.Collector to expose
// model-level operational metrics for LLMInferenceServices.
// Metrics are pulled on-demand during Prometheus scrape rather than pushed during reconciliation.
type WorkloadMetricsCollector struct {
	client client.Client
	mu     sync.Mutex

	replicaGauge   *prometheus.GaugeVec
	conditionGauge *prometheus.GaugeVec
}

// NewWorkloadMetricsCollector creates a new metrics collector for LLMInferenceService workloads.
func NewWorkloadMetricsCollector(client client.Client) *WorkloadMetricsCollector {
	return &WorkloadMetricsCollector{
		client: client,
		replicaGauge: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "llmisvc_workload_replicas_ready",
				Help: "Number of ready replicas for LLMInferenceService workload components (primary, prefill, scheduler)",
			},
			[]string{"llmisvc_name", "namespace", "component_type"},
		),
		conditionGauge: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "llmisvc_condition_status",
				Help: "Status of LLMInferenceService conditions (value=1 with status label: true, false, unknown)",
			},
			[]string{"llmisvc_name", "namespace", "condition_type", "status"},
		),
	}
}

// Describe implements prometheus.Collector.
func (c *WorkloadMetricsCollector) Describe(ch chan<- *prometheus.Desc) {
	c.replicaGauge.Describe(ch)
	c.conditionGauge.Describe(ch)
}

// Collect implements prometheus.Collector.
// Called on every Prometheus scrape to pull fresh metrics from Kubernetes API.
// Uses a mutex to ensure thread-safe access to shared gauge state.
// Includes a bounded context to prevent blocking the metrics endpoint on slow API responses.
func (c *WorkloadMetricsCollector) Collect(ch chan<- prometheus.Metric) {
	c.mu.Lock()
	defer c.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	logger := log.FromContext(ctx).WithName("metrics")

	// Reset gauges before collecting fresh metrics
	c.replicaGauge.Reset()
	c.conditionGauge.Reset()

	// List all LLMISVCs across all namespaces
	llmisvcs := &v1alpha2.LLMInferenceServiceList{}
	if err := c.client.List(ctx, llmisvcs); err != nil {
		logger.Error(err, "failed to list LLMInferenceServices for metrics collection")
		return
	}

	for i := range llmisvcs.Items {
		isvc := &llmisvcs.Items[i]
		c.collectReplicaMetrics(isvc)
		c.collectConditionMetrics(isvc)
	}

	// Emit collected metrics
	c.replicaGauge.Collect(ch)
	c.conditionGauge.Collect(ch)
}

// collectReplicaMetrics extracts replica counts from workload status.
func (c *WorkloadMetricsCollector) collectReplicaMetrics(isvc *v1alpha2.LLMInferenceService) {
	if isvc.Status.Workloads == nil {
		return
	}

	// Primary workload replicas
	if isvc.Status.Workloads.Primary != nil {
		primaryLabels := prometheus.Labels{
			"llmisvc_name":   isvc.Name,
			"namespace":      isvc.Namespace,
			"component_type": "primary",
		}
		replicas := int64(0)
		if isvc.Status.Workloads.Primary.ReadyReplicas != nil {
			replicas = int64(*isvc.Status.Workloads.Primary.ReadyReplicas)
		}
		c.replicaGauge.With(primaryLabels).Set(float64(replicas))
	}

	// Prefill workload replicas (disaggregated serving)
	if isvc.Status.Workloads.Prefill != nil {
		prefillLabels := prometheus.Labels{
			"llmisvc_name":   isvc.Name,
			"namespace":      isvc.Namespace,
			"component_type": "prefill",
		}
		replicas := int64(0)
		if isvc.Status.Workloads.Prefill.ReadyReplicas != nil {
			replicas = int64(*isvc.Status.Workloads.Prefill.ReadyReplicas)
		}
		c.replicaGauge.With(prefillLabels).Set(float64(replicas))
	}

	// Scheduler workload replicas (EPP)
	if isvc.Status.Workloads.Scheduler != nil {
		schedulerLabels := prometheus.Labels{
			"llmisvc_name":   isvc.Name,
			"namespace":      isvc.Namespace,
			"component_type": "scheduler",
		}
		replicas := int64(0)
		if isvc.Status.Workloads.Scheduler.ReadyReplicas != nil {
			replicas = int64(*isvc.Status.Workloads.Scheduler.ReadyReplicas)
		}
		c.replicaGauge.With(schedulerLabels).Set(float64(replicas))
	}
}

// collectConditionMetrics extracts condition status values.
// Emits separate metrics for each status value (True, False) following kube-state-metrics pattern.
func (c *WorkloadMetricsCollector) collectConditionMetrics(isvc *v1alpha2.LLMInferenceService) {
	for _, condition := range isvc.Status.Conditions {
		baseLabels := prometheus.Labels{
			"llmisvc_name":   isvc.Name,
			"namespace":      isvc.Namespace,
			"condition_type": string(condition.Type),
		}

		// Emit True/False/Unknown as separate metric series with status label
		// This avoids negative values and makes PromQL aggregations meaningful
		var statusValue string
		switch condition.Status {
		case corev1.ConditionTrue:
			statusValue = "true"
		case corev1.ConditionFalse:
			statusValue = "false"
		case corev1.ConditionUnknown:
			statusValue = "unknown"
		default:
			continue
		}

		labels := prometheus.Labels{
			"llmisvc_name":   baseLabels["llmisvc_name"],
			"namespace":      baseLabels["namespace"],
			"condition_type": baseLabels["condition_type"],
			"status":         statusValue,
		}
		c.conditionGauge.With(labels).Set(1)
	}
}
