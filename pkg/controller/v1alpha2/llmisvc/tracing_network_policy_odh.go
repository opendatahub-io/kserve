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
	"net/url"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"knative.dev/pkg/kmeta"
	"knative.dev/pkg/network"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
	"github.com/kserve/kserve/pkg/constants"
	"github.com/kserve/kserve/pkg/utils"
)

const (
	tracingNetworkPolicySuffix = "-otlp-egress"
	tracingNPComponentLabel    = "llm-tracing"
	// tracingNetworkPolicyOptInAnnotation enables the workload-side egress policy.
	// Tracing alone must not implicitly deny unrelated workload egress.
	tracingNetworkPolicyOptInAnnotation = constants.KServeAPIGroupName + "/enable-tracing-egress-network-policy"
	defaultOTLPPort                     = 4317
)

func tracingNetworkPolicyName(llmSvc *v1alpha2.LLMInferenceService) string {
	return kmeta.ChildName(llmSvc.GetName(), tracingNetworkPolicySuffix)
}

func (r *LLMISVCReconciler) reconcileTracingNetworkPolicy(ctx context.Context, llmSvc *v1alpha2.LLMInferenceService) error {
	if utils.GetForceStopRuntime(llmSvc) || llmSvc.Spec.Tracing == nil || llmSvc.GetAnnotations()[tracingNetworkPolicyOptInAnnotation] != "true" {
		return r.cleanupTracingNetworkPolicy(ctx, llmSvc)
	}

	expected := expectedTracingNetworkPolicy(llmSvc)
	if err := Reconcile(ctx, r, llmSvc, &netv1.NetworkPolicy{}, expected, semanticNetworkPolicyIsEqual); err != nil {
		return fmt.Errorf("failed to reconcile tracing network policy %s/%s: %w", expected.GetNamespace(), expected.GetName(), err)
	}
	return nil
}

func (r *LLMISVCReconciler) cleanupTracingNetworkPolicy(ctx context.Context, llmSvc *v1alpha2.LLMInferenceService) error {
	expected := expectedTracingNetworkPolicy(llmSvc)
	if err := Delete[*v1alpha2.LLMInferenceService](ctx, r, llmSvc, expected); err != nil {
		return fmt.Errorf("failed to delete tracing network policy: %w", err)
	}
	return nil
}

func expectedTracingNetworkPolicy(llmSvc *v1alpha2.LLMInferenceService) *netv1.NetworkPolicy {
	tcp := corev1.ProtocolTCP
	udp := corev1.ProtocolUDP
	port := func(protocol corev1.Protocol, value int32) netv1.NetworkPolicyPort {
		return netv1.NetworkPolicyPort{
			Protocol: &protocol,
			Port:     ptr.To(intstr.FromInt32(value)),
		}
	}

	tracingEndpoint := ""
	if llmSvc.Spec.Tracing != nil {
		tracingEndpoint = ptr.Deref(llmSvc.Spec.Tracing.ExporterEndpoint, "")
	}
	otlpPeer, otlpPort, hasCrossNamespaceOTLP := otlpPeerForEndpoint(tracingEndpoint, llmSvc.GetNamespace())

	policy := &netv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      tracingNetworkPolicyName(llmSvc),
			Namespace: llmSvc.GetNamespace(),
			Labels: map[string]string{
				constants.KubernetesPartOfLabelKey:    constants.LLMInferenceServicePartOfValue,
				constants.KubernetesAppNameLabelKey:   llmSvc.GetName(),
				constants.KubernetesComponentLabelKey: tracingNPComponentLabel,
			},
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(llmSvc, v1alpha2.LLMInferenceServiceGVK),
			},
		},
		Spec: netv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{
				constants.KubernetesPartOfLabelKey:  constants.LLMInferenceServicePartOfValue,
				constants.KubernetesAppNameLabelKey: llmSvc.GetName(),
			}},
			PolicyTypes: []netv1.PolicyType{netv1.PolicyTypeEgress},
			Egress: []netv1.NetworkPolicyEgressRule{
				{Ports: []netv1.NetworkPolicyPort{
					port(udp, 53), port(tcp, 53), port(udp, 5353), port(tcp, 5353),
				}},
				{Ports: []netv1.NetworkPolicyPort{port(tcp, 443), port(tcp, 6443)}},
				{To: []netv1.NetworkPolicyPeer{{PodSelector: &metav1.LabelSelector{}}}},
			},
		},
	}
	if hasCrossNamespaceOTLP {
		policy.Spec.Egress = append(policy.Spec.Egress, netv1.NetworkPolicyEgressRule{
			Ports: []netv1.NetworkPolicyPort{port(tcp, otlpPort)},
			To:    []netv1.NetworkPolicyPeer{otlpPeer},
		})
	}
	return policy
}

// otlpPeerForEndpoint maps a cluster-local Service endpoint to a NetworkPolicy
// peer. Same-namespace endpoints need no extra rule because the policy already
// allows unrestricted traffic within the namespace.
func otlpPeerForEndpoint(endpoint, serviceNamespace string) (netv1.NetworkPolicyPeer, int32, bool) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Hostname() == "" {
		return netv1.NetworkPolicyPeer{}, 0, false
	}

	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	hostParts := strings.Split(host, ".")
	clusterDomain := strings.TrimSuffix(strings.ToLower(network.GetClusterDomainName()), ".")
	var namespace string
	switch {
	case len(hostParts) == 2:
		// Kubernetes search paths resolve service.namespace from a pod.
		namespace = hostParts[1]
	case len(hostParts) == 3 && hostParts[2] == "svc":
		namespace = hostParts[1]
	case len(hostParts) > 3 && hostParts[2] == "svc" && strings.Join(hostParts[3:], ".") == clusterDomain:
		namespace = hostParts[1]
	default:
		return netv1.NetworkPolicyPeer{}, 0, false
	}
	if namespace == "" || namespace == serviceNamespace {
		return netv1.NetworkPolicyPeer{}, 0, false
	}

	port := int32(defaultOTLPPort)
	if portString := parsed.Port(); portString != "" {
		value, err := strconv.ParseInt(portString, 10, 32)
		if err != nil || value < 1 || value > 65535 {
			return netv1.NetworkPolicyPeer{}, 0, false
		}
		port = int32(value)
	}
	return namespaceSelectorPeer(namespace), port, true
}
