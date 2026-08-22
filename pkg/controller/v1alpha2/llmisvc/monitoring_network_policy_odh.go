//go:build distro

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

	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
	"github.com/kserve/kserve/pkg/constants"
	"github.com/kserve/kserve/pkg/utils"
)

const (
	prometheusNamespace = "redhat-ods-monitoring"
	networkPolicyName   = "kserve-llm-isvc-prometheus-scraping"
)

func (r *LLMISVCReconciler) reconcileMonitoringNetworkPolicy(ctx context.Context, llmSvc *v1alpha2.LLMInferenceService) error {
	logger := log.FromContext(ctx).WithName("reconcileMonitoringNetworkPolicy")

	if utils.GetForceStopRuntime(llmSvc) {
		return nil
	}

	logger.Info("Reconciling monitoring NetworkPolicy for Prometheus scraping")

	expected := expectedMonitoringNetworkPolicy(llmSvc)
	if err := Reconcile[*v1alpha2.LLMInferenceService](ctx, r, nil, &netv1.NetworkPolicy{}, expected, semanticNetworkPolicyIsEqual); err != nil {
		return fmt.Errorf("failed to reconcile monitoring network policy %s/%s: %w", expected.GetNamespace(), expected.GetName(), err)
	}

	return nil
}

func (r *LLMISVCReconciler) cleanupMonitoringNetworkPolicy(ctx context.Context, llmSvc *v1alpha2.LLMInferenceService) error {
	expected := expectedMonitoringNetworkPolicy(llmSvc)
	if err := Delete[*v1alpha2.LLMInferenceService](ctx, r, nil, expected); err != nil {
		return fmt.Errorf("failed to delete monitoring network policy: %w", err)
	}
	return nil
}

// expectedMonitoringNetworkPolicy returns the NetworkPolicy that allows
// Prometheus in redhat-ods-monitoring to scrape metrics from
// LLMInferenceService pods. The broad pod selector covers both vLLM
// workload pods (port 8000) and EPP scheduler pods (port 9090); ports
// that have no listener on a given pod are harmlessly ignored by the
// network stack.
func expectedMonitoringNetworkPolicy(llmSvc *v1alpha2.LLMInferenceService) *netv1.NetworkPolicy {
	vllmPort := intstr.FromInt32(8000)
	schedulerPort := intstr.FromInt32(9090)
	tcp := corev1.ProtocolTCP

	return &netv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      networkPolicyName,
			Namespace: llmSvc.GetNamespace(),
			Labels: map[string]string{
				constants.KubernetesComponentLabelKey: "llm-monitoring",
				constants.KubernetesPartOfLabelKey:    constants.LLMInferenceServicePartOfValue,
			},
		},
		Spec: netv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{
					constants.KubernetesPartOfLabelKey: constants.LLMInferenceServicePartOfValue,
				},
			},
			PolicyTypes: []netv1.PolicyType{netv1.PolicyTypeIngress},
			Ingress: []netv1.NetworkPolicyIngressRule{
				{
					From: []netv1.NetworkPolicyPeer{
						{
							NamespaceSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									"kubernetes.io/metadata.name": prometheusNamespace,
								},
							},
						},
					},
					Ports: []netv1.NetworkPolicyPort{
						{
							Protocol: &tcp,
							Port:     &vllmPort,
						},
						{
							Protocol: &tcp,
							Port:     &schedulerPort,
						},
					},
				},
			},
		},
	}
}

func semanticNetworkPolicyIsEqual(expected *netv1.NetworkPolicy, current *netv1.NetworkPolicy) bool {
	return equality.Semantic.DeepDerivative(expected.Spec, current.Spec) &&
		equality.Semantic.DeepDerivative(expected.Labels, current.Labels) &&
		equality.Semantic.DeepDerivative(expected.Annotations, current.Annotations)
}
