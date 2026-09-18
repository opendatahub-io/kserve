//go:build distro

/*
Copyright 2026 The KServe Authors.

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
	"k8s.io/utils/env"
	"knative.dev/pkg/kmeta"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
	"github.com/kserve/kserve/pkg/constants"
	"github.com/kserve/kserve/pkg/utils"
)

const (
	// monitoringNamespaceEnvVar, if set on the controller, is an extra Prometheus
	// peer for a non-default monitoring namespace. RHOAI's default is already
	// listed as defaultRHOAIMonitoringNamespace.
	monitoringNamespaceEnvVar = "MONITORING_NAMESPACE"

	// defaultMonitoringNamespace is where Platform Prometheus runs on OpenShift.
	defaultMonitoringNamespace = "openshift-monitoring"

	// defaultUserWorkloadMonitoringNamespace is where User Workload Monitoring Prometheus runs.
	defaultUserWorkloadMonitoringNamespace = "openshift-user-workload-monitoring"

	// defaultRHOAIMonitoringNamespace is the RHOAI DSCI default monitoring namespace.
	defaultRHOAIMonitoringNamespace = "redhat-ods-monitoring"
)

// prometheusPeerNamespaces is the union of OpenShift Platform Monitoring, User
// Workload Monitoring, the RHOAI DSCI default monitoring namespace, and
// MONITORING_NAMESPACE when the operator injects a non-default value.
func prometheusPeerNamespaces() []string {
	return uniqueNamespaces(
		defaultMonitoringNamespace,
		defaultUserWorkloadMonitoringNamespace,
		defaultRHOAIMonitoringNamespace,
		env.GetString(monitoringNamespaceEnvVar, ""),
	)
}

func uniqueNamespaces(nss ...string) []string {
	seen := make(map[string]struct{}, len(nss))
	out := make([]string, 0, len(nss))
	for _, ns := range nss {
		if ns == "" {
			continue
		}
		if _, ok := seen[ns]; ok {
			continue
		}
		seen[ns] = struct{}{}
		out = append(out, ns)
	}
	return out
}

// ingressNamespace is the namespace of the cluster default Gateway.
// Distro builds run on RHOAI (OCP) and RHAII (xKS/OCP), so this must come from
// Config (kserveIngressGateway), not an OpenShift-only default.
// If config is missing, use POD_NAMESPACE like NewConfig, never openshift-ingress.
func ingressNamespace(config *Config) string {
	if config != nil && config.IngressGatewayNamespace != "" {
		return config.IngressGatewayNamespace
	}
	return constants.KServeNamespace
}

func monitoringNetworkPolicyName(llmSvc *v1alpha2.LLMInferenceService) string {
	return kmeta.ChildName(llmSvc.GetName(), "-prometheus-scraping")
}

// gatewayPeerNamespaces is the managed Gateway namespace plus any
// spec.router.gateway.refs namespaces that are not this service's namespace.
// Same-namespace Gateways are already allowed by the intra-namespace rule.
func gatewayPeerNamespaces(llmSvc *v1alpha2.LLMInferenceService, ingressNs string) []string {
	if ingressNs == "" {
		ingressNs = constants.KServeNamespace
	}
	seen := map[string]struct{}{ingressNs: {}}
	out := []string{ingressNs}
	if llmSvc == nil || llmSvc.Spec.Router == nil || !llmSvc.Spec.Router.Gateway.HasRefs() {
		return out
	}
	svcNs := llmSvc.GetNamespace()
	for _, ref := range llmSvc.Spec.Router.Gateway.Refs {
		ns := string(ref.Namespace)
		if ns == "" {
			ns = svcNs
		}
		if ns == "" || ns == svcNs {
			continue
		}
		if _, ok := seen[ns]; ok {
			continue
		}
		seen[ns] = struct{}{}
		out = append(out, ns)
	}
	return out
}

func namespaceSelectorPeer(ns string) netv1.NetworkPolicyPeer {
	return netv1.NetworkPolicyPeer{
		NamespaceSelector: &metav1.LabelSelector{
			MatchLabels: map[string]string{
				"kubernetes.io/metadata.name": ns,
			},
		},
	}
}

func (r *LLMISVCReconciler) reconcileMonitoringNetworkPolicy(ctx context.Context, llmSvc *v1alpha2.LLMInferenceService, config *Config) error {
	logger := log.FromContext(ctx).WithName("reconcileMonitoringNetworkPolicy")

	if utils.GetForceStopRuntime(llmSvc) {
		return r.cleanupMonitoringNetworkPolicy(ctx, llmSvc)
	}

	logger.Info("Reconciling monitoring NetworkPolicy for Prometheus scraping")

	expected := expectedMonitoringNetworkPolicy(llmSvc, ingressNamespace(config))
	if err := Reconcile(ctx, r, llmSvc, &netv1.NetworkPolicy{}, expected, semanticNetworkPolicyIsEqual); err != nil {
		return fmt.Errorf("failed to reconcile monitoring network policy %s/%s: %w", expected.GetNamespace(), expected.GetName(), err)
	}

	return nil
}

func (r *LLMISVCReconciler) cleanupMonitoringNetworkPolicy(ctx context.Context, llmSvc *v1alpha2.LLMInferenceService) error {
	// Owner is nil so Delete actually removes the object during finalize.
	// envtest has no GC, and Delete skips work when the owner is deleting.
	expected := expectedMonitoringNetworkPolicy(llmSvc, "")
	if err := Delete[*v1alpha2.LLMInferenceService](ctx, r, nil, expected); err != nil {
		return fmt.Errorf("failed to delete monitoring network policy: %w", err)
	}
	return nil
}

// expectedMonitoringNetworkPolicy returns a per-service NetworkPolicy that allows:
//  1. Prometheus in Platform Monitoring, User Workload Monitoring, the RHOAI
//     default monitoring namespace, and an optional extra MONITORING_NAMESPACE
//     to scrape metrics on ports 8000 & 9090
//  2. The managed Gateway and any cross-namespace spec.router.gateway.refs to reach vLLM (8000)
//     and EPP ext_proc (9002)
//  3. All pods within the namespace to communicate on any port (intra-namespace service-to-service)
func expectedMonitoringNetworkPolicy(llmSvc *v1alpha2.LLMInferenceService, ingressNs string) *netv1.NetworkPolicy {
	vllmPort := intstr.FromInt32(8000)
	eppExtProcPort := intstr.FromInt32(9002)
	metricsPort := intstr.FromInt32(9090)
	tcp := corev1.ProtocolTCP

	peerNs := gatewayPeerNamespaces(llmSvc, ingressNs)
	gatewayPeers := make([]netv1.NetworkPolicyPeer, 0, len(peerNs))
	for _, ns := range peerNs {
		gatewayPeers = append(gatewayPeers, namespaceSelectorPeer(ns))
	}

	promNs := prometheusPeerNamespaces()
	prometheusPeers := make([]netv1.NetworkPolicyPeer, 0, len(promNs))
	for _, ns := range promNs {
		prometheusPeers = append(prometheusPeers, namespaceSelectorPeer(ns))
	}

	return &netv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      monitoringNetworkPolicyName(llmSvc),
			Namespace: llmSvc.GetNamespace(),
			Labels: map[string]string{
				constants.KubernetesComponentLabelKey: "llm-monitoring",
				constants.KubernetesPartOfLabelKey:    constants.LLMInferenceServicePartOfValue,
				constants.KubernetesAppNameLabelKey:   llmSvc.GetName(),
			},
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(llmSvc, v1alpha2.LLMInferenceServiceGVK),
			},
		},
		Spec: netv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{
					constants.KubernetesPartOfLabelKey:  constants.LLMInferenceServicePartOfValue,
					constants.KubernetesAppNameLabelKey: llmSvc.GetName(),
				},
			},
			PolicyTypes: []netv1.PolicyType{netv1.PolicyTypeIngress},
			Ingress: []netv1.NetworkPolicyIngressRule{
				// Allow Prometheus scraping from Platform, UWM, RHOAI monitoring, and any extra MONITORING_NAMESPACE
				{
					From: prometheusPeers,
					Ports: []netv1.NetworkPolicyPort{
						{
							Protocol: &tcp,
							Port:     &vllmPort,
						},
						{
							Protocol: &tcp,
							Port:     &metricsPort,
						},
					},
				},
				// Allow managed Gateway and cross-namespace custom gateway.refs
				{
					From: gatewayPeers,
					Ports: []netv1.NetworkPolicyPort{
						{
							Protocol: &tcp,
							Port:     &vllmPort,
						},
						{
							Protocol: &tcp,
							Port:     &eppExtProcPort,
						},
					},
				},
				// Allow all pods within the namespace to communicate on any port
				{
					From: []netv1.NetworkPolicyPeer{
						{
							PodSelector: &metav1.LabelSelector{},
						},
					},
				},
			},
		},
	}
}

func semanticNetworkPolicyIsEqual(expected *netv1.NetworkPolicy, current *netv1.NetworkPolicy) bool {
	return equality.Semantic.DeepEqual(expected.Spec, current.Spec) &&
		equality.Semantic.DeepDerivative(expected.Labels, current.Labels) &&
		equality.Semantic.DeepDerivative(expected.Annotations, current.Annotations)
}
