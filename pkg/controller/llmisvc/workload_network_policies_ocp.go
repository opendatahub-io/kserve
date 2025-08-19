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

	"github.com/kserve/kserve/pkg/constants"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"knative.dev/pkg/kmeta"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
)

const (
	networkPoliciesNamespaceNameKey = "kubernetes.io/metadata.name"
)

var (
	ocpMonitoringNamespace    = constants.GetEnvOrDefault("OCP_MONITORING_NAMESPACE", "openshift-monitoring")
	ocpUWMonitoringNamespace  = constants.GetEnvOrDefault("OCP_USER_WORKLOAD_MONITORING_NAMESPACE", "openshift-user-workload-monitoring")
	ocpKubeApiServerNamespace = constants.GetEnvOrDefault("OCP_KUBE_API_SERVER_NAMESPACE", "openshift-kube-apiserver")
	ocpDNSNamespace           = constants.GetEnvOrDefault("OCP_DNS_NAMESPACE", "openshift-dns")
)

func (r *LLMInferenceServiceReconciler) reconcileNetworkPolicies(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService) error {
	if err := r.reconcileSchedulerNetworkPolicy(ctx, llmSvc); err != nil {
		return fmt.Errorf("failed to reconcile scheduler network policy: %w", err)
	}
	if err := r.reconcileWorkloadNetworkPolicy(ctx, llmSvc); err != nil {
		return fmt.Errorf("failed to reconcile workload network policy: %w", err)
	}
	return nil
}

func (r *LLMInferenceServiceReconciler) reconcileSchedulerNetworkPolicy(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService) error {
	cfg, err := LoadConfig(ctx, r.Clientset)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	expected := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      kmeta.ChildName(llmSvc.GetName(), "-kserve-router-scheduler"),
			Namespace: llmSvc.GetNamespace(),
			Labels:    r.schedulerLabels(llmSvc),
		},
		Spec: networkingv1.NetworkPolicySpec{
			PolicyTypes: []networkingv1.PolicyType{
				networkingv1.PolicyTypeIngress,
				networkingv1.PolicyTypeEgress,
			},
			PodSelector: metav1.LabelSelector{
				MatchLabels: r.schedulerLabels(llmSvc),
			},
			Ingress: []networkingv1.NetworkPolicyIngressRule{
				{
					From: []networkingv1.NetworkPolicyPeer{
						// Gateway Traffic
						{NamespaceSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{networkPoliciesNamespaceNameKey: cfg.IngressGatewayNamespace},
						}},
						// Monitoring (scraping)
						{NamespaceSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{networkPoliciesNamespaceNameKey: ocpMonitoringNamespace},
						}},
						// Monitoring (scraping)
						{NamespaceSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{networkPoliciesNamespaceNameKey: ocpUWMonitoringNamespace},
						}},
					},
				},
			},
			Egress: []networkingv1.NetworkPolicyEgressRule{
				{
					To: []networkingv1.NetworkPolicyPeer{
						// Scheduler scraping vLLM metrics
						{NamespaceSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{networkPoliciesNamespaceNameKey: llmSvc.GetNamespace()},
						}},
						// Scheduler watch pods.
						{NamespaceSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{networkPoliciesNamespaceNameKey: ocpKubeApiServerNamespace},
						}},
						// DNS
						{NamespaceSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{networkPoliciesNamespaceNameKey: ocpDNSNamespace},
						}},
					},
				},
			},
		},
	}
	if llmSvc.Spec.Router == nil || llmSvc.Spec.Router.Scheduler == nil {
		return Delete(ctx, r, llmSvc, expected)
	}
	return Reconcile(ctx, r, llmSvc, &networkingv1.NetworkPolicy{}, expected, semanticNetworkPolicyIsEqual)
}

func (r *LLMInferenceServiceReconciler) reconcileWorkloadNetworkPolicy(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService) error {
	cfg, err := LoadConfig(ctx, r.Clientset)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	expected := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      kmeta.ChildName(llmSvc.GetName(), "-kserve-workload"),
			Namespace: llmSvc.GetNamespace(),
			Labels: map[string]string{
				"app.kubernetes.io/part-of": "llminferenceservice",
				"app.kubernetes.io/name":    llmSvc.GetName(),
			},
		},
		Spec: networkingv1.NetworkPolicySpec{
			// Only restrict ingress traffic since runtime need to download models from arbitrary locations.
			PolicyTypes: []networkingv1.PolicyType{
				networkingv1.PolicyTypeIngress,
			},
			PodSelector: metav1.LabelSelector{
				MatchLabels: getWorkloadLabelSelector(llmSvc.ObjectMeta, &llmSvc.Spec),
			},
			Ingress: []networkingv1.NetworkPolicyIngressRule{
				{
					From: []networkingv1.NetworkPolicyPeer{
						// Gateway Traffic
						{NamespaceSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{networkPoliciesNamespaceNameKey: cfg.IngressGatewayNamespace},
						}},
						// Monitoring (scraping)
						{NamespaceSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{networkPoliciesNamespaceNameKey: ocpMonitoringNamespace},
						}},
						// Monitoring (scraping)
						{NamespaceSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{networkPoliciesNamespaceNameKey: ocpUWMonitoringNamespace},
						}},
						// Scheduler and Inference traffic (NIXL, routing-sidecar, etc)
						{NamespaceSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{networkPoliciesNamespaceNameKey: llmSvc.GetNamespace()},
						}},
					},
				},
			},
		},
	}
	if llmSvc.Spec.Router == nil || llmSvc.Spec.Router.Scheduler == nil {
		return Delete(ctx, r, llmSvc, expected)
	}
	return Reconcile(ctx, r, llmSvc, &networkingv1.NetworkPolicy{}, expected, semanticNetworkPolicyIsEqual)
}

func semanticNetworkPolicyIsEqual(expected *networkingv1.NetworkPolicy, curr *networkingv1.NetworkPolicy) bool {
	return equality.Semantic.DeepEqual(expected.Spec, curr.Spec) &&
		equality.Semantic.DeepDerivative(expected.Labels, curr.Labels) &&
		equality.Semantic.DeepDerivative(expected.Annotations, curr.Annotations)
}
