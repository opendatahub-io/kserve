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
	"fmt"

	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"knative.dev/pkg/kmeta"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
)

// reconcileMonitoringResources reconciles all monitoring-related resources for an LLMInferenceService,
// including RBAC permissions, Prometheus operator monitors for the llm-d scheduler and the vLLM engine.
func (r *LLMInferenceServiceReconciler) reconcileMonitoringResources(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService) error {
	logger := log.FromContext(ctx).WithName("reconcileMonitoring")
	ctx = log.IntoContext(ctx, logger)

	logger.Info("Reconciling Monitoring Resources for LLMInferenceService")

	if err := r.reconcileMetricsReaderRBAC(ctx, llmSvc); err != nil {
		return fmt.Errorf("failed to reconcile metrics reader RBAC: %w", err)
	}

	if err := r.reconcileVLLMEngineMonitor(ctx, llmSvc); err != nil {
		return fmt.Errorf("failed to reconcile VLLM engine monitor: %w", err)
	}

	if err := r.reconcileSchedulerMonitor(ctx, llmSvc); err != nil {
		return fmt.Errorf("failed to reconcile scheduler monitor: %w", err)
	}

	return nil
}

// reconcileMetricsReaderRBAC creates and manages RBAC resources (ServiceAccount, Secret, ClusterRoleBinding)
// required for metrics collection from LLMInferenceService components - specifically, for the scheduler.
func (r *LLMInferenceServiceReconciler) reconcileMetricsReaderRBAC(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService) error {
	log.FromContext(ctx).Info("Reconciling LLMInferenceService metrics reader RBAC for namespace")

	serviceAccount := r.expectedMetricsReaderServiceAccount(llmSvc)
	if err := Reconcile[*v1alpha1.LLMInferenceService](ctx, r, nil, &corev1.ServiceAccount{}, serviceAccount, semanticServiceAccountIsEqual); err != nil {
		return fmt.Errorf("failed to reconcile metrics reader service account %s/%s: %w", serviceAccount.GetNamespace(), serviceAccount.GetName(), err)
	}

	secret := r.expectedMetricsReaderSecret(llmSvc)
	if err := Reconcile[*v1alpha1.LLMInferenceService](ctx, r, nil, &corev1.Secret{}, secret, semanticSecretSATokenIsEqual); err != nil {
		return fmt.Errorf("failed to reconcile metrics reader secret %s/%s: %w", secret.GetNamespace(), secret.GetName(), err)
	}

	clusterRoleBinding := r.expectedMetricsReaderClusterRoleBinding(llmSvc)
	if err := Reconcile[*v1alpha1.LLMInferenceService](ctx, r, nil, &rbacv1.ClusterRoleBinding{}, clusterRoleBinding, semanticClusterRoleBindingIsEqual); err != nil {
		return fmt.Errorf("failed to reconcile metrics reader cluster role binding %s: %w", clusterRoleBinding.GetName(), err)
	}

	return nil
}

// reconcileVLLMEngineMonitor creates and manages a PodMonitor resource to scrape metrics
// from vLLM engine pods running within the LLMInferenceService.
func (r *LLMInferenceServiceReconciler) reconcileVLLMEngineMonitor(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService) error {
	log.FromContext(ctx).Info("Reconciling LLMInferenceService engine monitor")

	monitor := r.expectedVLLMEngineMonitor(llmSvc)
	if err := Reconcile[*v1alpha1.LLMInferenceService](ctx, r, nil, &monitoringv1.PodMonitor{}, monitor, semanticPodMonitorIsEqual); err != nil {
		return fmt.Errorf("failed to reconcile vLLM engine monitor %s/%s: %w", monitor.GetNamespace(), monitor.GetName(), err)
	}
	return nil
}

// reconcileSchedulerMonitor creates and manages a ServiceMonitor resource to scrape metrics
// from the scheduler service of the LLMInferenceService.
func (r *LLMInferenceServiceReconciler) reconcileSchedulerMonitor(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService) error {
	log.FromContext(ctx).Info("Reconciling LLMInferenceService scheduler monitor")

	monitor := r.expectedSchedulerMonitor(llmSvc)
	if err := Reconcile[*v1alpha1.LLMInferenceService](ctx, r, nil, &monitoringv1.ServiceMonitor{}, monitor, semanticServiceMonitorIsEqual); err != nil {
		return fmt.Errorf("failed to reconcile scheduler monitor %s/%s: %w", monitor.GetNamespace(), monitor.GetName(), err)
	}
	return nil
}

// expectedMetricsReaderServiceAccount returns the expected ServiceAccount configuration
// for metrics collection. This is required to correctly scrape metrics from llm-d scheduler.
func (r *LLMInferenceServiceReconciler) expectedMetricsReaderServiceAccount(llmSvc *v1alpha1.LLMInferenceService) *corev1.ServiceAccount {
	return &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kserve-metrics-reader-sa",
			Namespace: llmSvc.GetNamespace(),
			Labels: map[string]string{
				"app.kubernetes.io/component": "llm-monitoring",
				"app.kubernetes.io/part-of":   "llminferenceservice",
			},
		},
		AutomountServiceAccountToken: &[]bool{false}[0],
	}
}

// expectedMetricsReaderSecret returns the Secret definition to hold a token for the ServiceAccount used by metrics collection
// components. This is required to correctly scrape metrics from llm-d scheduler.
func (r *LLMInferenceServiceReconciler) expectedMetricsReaderSecret(llmSvc *v1alpha1.LLMInferenceService) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kserve-metrics-reader-sa-secret",
			Namespace: llmSvc.GetNamespace(),
			Annotations: map[string]string{
				"kubernetes.io/service-account.name": "kserve-metrics-reader-sa",
			},
			Labels: map[string]string{
				"app.kubernetes.io/component": "llm-monitoring",
				"app.kubernetes.io/part-of":   "llminferenceservice",
			},
		},
		Type: corev1.SecretTypeServiceAccountToken,
	}
}

// expectedMetricsReaderClusterRoleBinding returns the expected ClusterRoleBinding configuration
// that grants the metrics reader ServiceAccount the necessary permissions for metrics collection.
// This is required to correctly scrape metrics from llm-d scheduler.
func (r *LLMInferenceServiceReconciler) expectedMetricsReaderClusterRoleBinding(llmSvc *v1alpha1.LLMInferenceService) *rbacv1.ClusterRoleBinding {
	return &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: kmeta.ChildName("kserve-metrics-reader-role-binding-", llmSvc.GetNamespace()),
			Labels: map[string]string{
				"app.kubernetes.io/component": "llm-monitoring",
				"app.kubernetes.io/part-of":   "llminferenceservice",
			},
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      "kserve-metrics-reader-sa",
				Namespace: llmSvc.GetNamespace(),
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     "kserve-metrics-reader-cluster-role",
		},
	}
}

// expectedVLLMEngineMonitor returns the expected PodMonitor configuration for scraping
// metrics from vLLM engine pods.
func (r *LLMInferenceServiceReconciler) expectedVLLMEngineMonitor(llmSvc *v1alpha1.LLMInferenceService) *monitoringv1.PodMonitor {
	metricsPort := intstr.FromInt32(8000)

	return &monitoringv1.PodMonitor{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kserve-llm-isvc-vllm-engine",
			Namespace: llmSvc.GetNamespace(),
			Labels: map[string]string{
				"app.kubernetes.io/component":      "llm-monitoring",
				"app.kubernetes.io/part-of":        "llminferenceservice",
				"monitoring.opendatahub.io/scrape": "true",
			},
		},
		Spec: monitoringv1.PodMonitorSpec{
			Selector: metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app.kubernetes.io/part-of": "llminferenceservice",
				},
				MatchExpressions: []metav1.LabelSelectorRequirement{
					{
						Key:      "app.kubernetes.io/component",
						Operator: metav1.LabelSelectorOpIn,
						Values: []string{
							"llminferenceservice-workload",
							"llminferenceservice-workload-prefill",
							"llminferenceservice-workload-worker",
							"llminferenceservice-workload-leader",
							"llminferenceservice-workload-leader-prefill",
							"llminferenceservice-workload-worker-prefill",
						},
					},
				},
			},
			PodMetricsEndpoints: []monitoringv1.PodMetricsEndpoint{
				{
					TargetPort: &metricsPort,
					Scheme:     "https",
					TLSConfig: &monitoringv1.SafeTLSConfig{
						InsecureSkipVerify: ptr.To(true),
					},
					MetricRelabelConfigs: []monitoringv1.RelabelConfig{
						{
							SourceLabels: []monitoringv1.LabelName{"__name__"},
							Action:       "replace",
							Replacement:  ptr.To("kserve_$1"),
							TargetLabel:  "__name__",
						},
					},
					RelabelConfigs: []monitoringv1.RelabelConfig{
						{
							SourceLabels: []monitoringv1.LabelName{"__meta_kubernetes_pod_label_app_kubernetes_io_name"},
							Action:       "replace",
							TargetLabel:  "llm_isvc_name",
						},
						{
							SourceLabels: []monitoringv1.LabelName{"__meta_kubernetes_pod_label_llm_d_ai_role"},
							Action:       "replace",
							TargetLabel:  "llm_isvc_role",
						},
						{
							SourceLabels: []monitoringv1.LabelName{"__meta_kubernetes_pod_label_app_kubernetes_io_component"},
							Action:       "replace",
							Regex:        "llminferenceservice-(.*)",
							Replacement:  ptr.To("$1"),
							TargetLabel:  "llm_isvc_component",
						},
					},
				},
			},
		},
	}
}

// expectedSchedulerMonitor returns the expected ServiceMonitor configuration for scraping
// metrics from the llm-d scheduler. The scheduler requires authorization.
func (r *LLMInferenceServiceReconciler) expectedSchedulerMonitor(llmSvc *v1alpha1.LLMInferenceService) *monitoringv1.ServiceMonitor {
	return &monitoringv1.ServiceMonitor{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kserve-llm-isvc-scheduler",
			Namespace: llmSvc.GetNamespace(),
			Labels: map[string]string{
				"app.kubernetes.io/component":      "llm-monitoring",
				"app.kubernetes.io/part-of":        "llminferenceservice",
				"monitoring.opendatahub.io/scrape": "true",
			},
		},
		Spec: monitoringv1.ServiceMonitorSpec{
			Selector: metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app.kubernetes.io/component": "llminferenceservice-router-scheduler",
					"app.kubernetes.io/part-of":   "llminferenceservice",
				},
			},
			Endpoints: []monitoringv1.Endpoint{
				{
					Port: "metrics",
					Authorization: &monitoringv1.SafeAuthorization{
						Credentials: &corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: "kserve-metrics-reader-sa-secret",
							},
							Key: "token",
						},
					},
					MetricRelabelConfigs: []monitoringv1.RelabelConfig{
						{
							SourceLabels: []monitoringv1.LabelName{"__name__"},
							Action:       "replace",
							Replacement:  ptr.To("kserve_$1"),
							TargetLabel:  "__name__",
						},
					},
					RelabelConfigs: []monitoringv1.RelabelConfig{
						{
							SourceLabels: []monitoringv1.LabelName{"__meta_kubernetes_pod_label_app_kubernetes_io_name"},
							Action:       "replace",
							TargetLabel:  "llm_isvc_name",
						},
						{
							SourceLabels: []monitoringv1.LabelName{"__meta_kubernetes_pod_label_app_kubernetes_io_component"},
							Action:       "replace",
							Regex:        "llminferenceservice-(.*)",
							Replacement:  ptr.To("$1"),
							TargetLabel:  "llm_isvc_component",
						},
					},
				},
			},
		},
	}
}

// cleanupMonitoringResources removes LLM monitoring resources when the last LLMInferenceService in the namespace is deleted.
func (r *LLMInferenceServiceReconciler) cleanupMonitoringResources(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService) error {
	logger := log.FromContext(ctx).WithName("cleanupMonitoring")
	ctx = log.IntoContext(ctx, logger)

	llmSvcList := &v1alpha1.LLMInferenceServiceList{}
	if err := r.List(ctx, llmSvcList, &client.ListOptions{Namespace: llmSvc.GetNamespace()}); err != nil {
		return fmt.Errorf("failed to list LLMInferenceServices in namespace %s: %w", llmSvc.GetNamespace(), err)
	}

	namespaceHasLlmIsvcs := false
	for _, svc := range llmSvcList.Items {
		if svc.DeletionTimestamp.IsZero() {
			namespaceHasLlmIsvcs = true
			break
		}
	}

	if namespaceHasLlmIsvcs {
		logger.Info("Other LLMInferenceServices exist in namespace, skipping monitoring cleanup",
			"namespace", llmSvc.GetNamespace())
		return nil
	}

	logger.Info("Cleaning up monitoring resources - last LLMInferenceService in namespace",
		"namespace", llmSvc.GetNamespace())

	vllmMonitor := r.expectedVLLMEngineMonitor(llmSvc)
	if err := Delete[*v1alpha1.LLMInferenceService](ctx, r, nil, vllmMonitor); err != nil {
		return fmt.Errorf("failed to delete VLLM engine monitor: %w", err)
	}

	schedulerMonitor := r.expectedSchedulerMonitor(llmSvc)
	if err := Delete[*v1alpha1.LLMInferenceService](ctx, r, nil, schedulerMonitor); err != nil {
		return fmt.Errorf("failed to delete scheduler monitor: %w", err)
	}

	serviceAccount := r.expectedMetricsReaderServiceAccount(llmSvc)
	if err := Delete[*v1alpha1.LLMInferenceService](ctx, r, nil, serviceAccount); err != nil {
		return fmt.Errorf("failed to delete metrics reader service account: %w", err)
	}

	secret := r.expectedMetricsReaderSecret(llmSvc)
	if err := Delete[*v1alpha1.LLMInferenceService](ctx, r, nil, secret); err != nil {
		return fmt.Errorf("failed to delete metrics reader secret: %w", err)
	}

	clusterRoleBinding := r.expectedMetricsReaderClusterRoleBinding(llmSvc)
	if err := Delete[*v1alpha1.LLMInferenceService](ctx, r, nil, clusterRoleBinding); err != nil {
		return fmt.Errorf("failed to delete metrics reader cluster role binding: %w", err)
	}

	return nil
}

func semanticSecretSATokenIsEqual(expected *corev1.Secret, current *corev1.Secret) bool {
	return equality.Semantic.DeepDerivative(expected.Type, current.Type) &&
		equality.Semantic.DeepDerivative(expected.Labels, current.Labels) &&
		equality.Semantic.DeepDerivative(expected.Annotations, current.Annotations)
}

func semanticPodMonitorIsEqual(expected *monitoringv1.PodMonitor, current *monitoringv1.PodMonitor) bool {
	return equality.Semantic.DeepDerivative(expected.Spec, current.Spec) &&
		equality.Semantic.DeepDerivative(expected.Labels, current.Labels) &&
		equality.Semantic.DeepDerivative(expected.Annotations, current.Annotations)
}

func semanticServiceMonitorIsEqual(expected *monitoringv1.ServiceMonitor, current *monitoringv1.ServiceMonitor) bool {
	return equality.Semantic.DeepDerivative(expected.Spec, current.Spec) &&
		equality.Semantic.DeepDerivative(expected.Labels, current.Labels) &&
		equality.Semantic.DeepDerivative(expected.Annotations, current.Annotations)
}
