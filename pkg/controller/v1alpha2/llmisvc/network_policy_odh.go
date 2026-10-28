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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/env"
	"k8s.io/utils/ptr"
	"knative.dev/pkg/kmeta"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	igwapi "sigs.k8s.io/gateway-api-inference-extension/api/v1"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	igwapiv1alpha2 "github.com/kserve/kserve/pkg/apis/gie/v1alpha2pool"
	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
	"github.com/kserve/kserve/pkg/constants"
	"github.com/kserve/kserve/pkg/utils"
)

const (
	networkPolicyDisabledEnvVar = "LLMISVC_NETWORK_POLICY_DISABLED"

	networkPolicyWorkloadSuffix  = "-networkpolicy-workload"
	networkPolicySchedulerSuffix = "-networkpolicy-scheduler"
	networkPolicyTokenizerSuffix = "-networkpolicy-tokenizer"
	networkPolicyEgressSuffix    = "-networkpolicy-egress"

	legacyMonitoringNetworkPolicySuffix = "-prometheus-scraping"

	networkPolicyComponentLabel = "llm-network-policy"
	defaultWorkloadPort         = int32(8000)
	defaultSchedulerGRPCPort    = int32(9002)
	defaultSchedulerMetricsPort = int32(9090)
	maxNetworkPolicyPort        = int32(65535)
)

var networkPolicyDisabled, _ = env.GetBool(networkPolicyDisabledEnvVar, false)

func networkPolicyName(llmSvc *v1alpha2.LLMInferenceService, suffix string) string {
	return kmeta.ChildName(llmSvc.GetName(), suffix)
}

func workloadNetworkPolicyName(llmSvc *v1alpha2.LLMInferenceService) string {
	return networkPolicyName(llmSvc, networkPolicyWorkloadSuffix)
}

func schedulerNetworkPolicyName(llmSvc *v1alpha2.LLMInferenceService) string {
	return networkPolicyName(llmSvc, networkPolicySchedulerSuffix)
}

func tokenizerNetworkPolicyName(llmSvc *v1alpha2.LLMInferenceService) string {
	return networkPolicyName(llmSvc, networkPolicyTokenizerSuffix)
}

func egressNetworkPolicyName(llmSvc *v1alpha2.LLMInferenceService) string {
	return networkPolicyName(llmSvc, networkPolicyEgressSuffix)
}

func legacyMonitoringNetworkPolicyName(llmSvc *v1alpha2.LLMInferenceService) string {
	return kmeta.ChildName(llmSvc.GetName(), legacyMonitoringNetworkPolicySuffix)
}

func networkPolicySelector(llmSvc *v1alpha2.LLMInferenceService, role string) metav1.LabelSelector {
	return metav1.LabelSelector{MatchLabels: map[string]string{
		networkPolicyServiceIDLabel: networkPolicyServiceID(llmSvc),
		networkPolicyRoleLabel:      role,
	}}
}

func allServicePodsSelector(llmSvc *v1alpha2.LLMInferenceService) metav1.LabelSelector {
	return metav1.LabelSelector{MatchLabels: map[string]string{
		networkPolicyServiceIDLabel: networkPolicyServiceID(llmSvc),
	}}
}

func networkPolicyPeer(llmSvc *v1alpha2.LLMInferenceService, role string) netv1.NetworkPolicyPeer {
	return netv1.NetworkPolicyPeer{PodSelector: &metav1.LabelSelector{
		MatchLabels: networkPolicySelector(llmSvc, role).MatchLabels,
	}}
}

func networkPolicyPort(protocol corev1.Protocol, port int32) netv1.NetworkPolicyPort {
	return netv1.NetworkPolicyPort{Protocol: ptr.To(protocol), Port: ptr.To(intstr.FromInt32(port))}
}

func networkPolicyPorts(protocol corev1.Protocol, ports ...int32) []netv1.NetworkPolicyPort {
	unique := uniquePorts(ports...)
	result := make([]netv1.NetworkPolicyPort, 0, len(unique))
	for _, port := range unique {
		result = append(result, networkPolicyPort(protocol, port))
	}
	return result
}

func allPortsNetworkPolicyPorts() []netv1.NetworkPolicyPort {
	ports := make([]netv1.NetworkPolicyPort, 0, 3)
	for _, protocol := range []corev1.Protocol{corev1.ProtocolTCP, corev1.ProtocolUDP, corev1.ProtocolSCTP} {
		ports = append(ports, netv1.NetworkPolicyPort{
			Protocol: ptr.To(protocol),
			Port:     ptr.To(intstr.FromInt32(1)),
			EndPort:  ptr.To(maxNetworkPolicyPort),
		})
	}
	return ports
}

func networkPolicyObjectMeta(llmSvc *v1alpha2.LLMInferenceService, name, component string) metav1.ObjectMeta {
	return metav1.ObjectMeta{
		Name:      name,
		Namespace: llmSvc.GetNamespace(),
		Labels: map[string]string{
			constants.KubernetesComponentLabelKey: component,
			constants.KubernetesPartOfLabelKey:    constants.LLMInferenceServicePartOfValue,
			constants.KubernetesAppNameLabelKey:   llmSvc.GetName(),
			networkPolicyServiceIDLabel:           networkPolicyServiceID(llmSvc),
		},
		OwnerReferences: []metav1.OwnerReference{
			*metav1.NewControllerRef(llmSvc, v1alpha2.LLMInferenceServiceGVK),
		},
	}
}

func namespacePeers(namespaces []string) []netv1.NetworkPolicyPeer {
	peers := make([]netv1.NetworkPolicyPeer, 0, len(namespaces))
	for _, namespace := range namespaces {
		peers = append(peers, namespaceSelectorPeer(namespace))
	}
	return peers
}

func gatewayIngressRule(gatewayNamespaces []string, ports ...int32) netv1.NetworkPolicyIngressRule {
	return netv1.NetworkPolicyIngressRule{
		// Gateway API references do not carry a portable source Pod selector.
		// Until that contract exists, the referenced namespaces are an
		// explicitly documented temporary exception. Restrict the exception to
		// the actual service ports and never use it for tokenizer access.
		From:  namespacePeers(gatewayNamespaces),
		Ports: networkPolicyPorts(corev1.ProtocolTCP, ports...),
	}
}

func monitoringIngressRule(port int32, config *Config) netv1.NetworkPolicyIngressRule {
	return netv1.NetworkPolicyIngressRule{
		From:  namespacePeers(prometheusPeerNamespaces(config)),
		Ports: []netv1.NetworkPolicyPort{networkPolicyPort(corev1.ProtocolTCP, port)},
	}
}

func sameNamespacePeer(llmSvc *v1alpha2.LLMInferenceService, role string) netv1.NetworkPolicyPeer {
	return networkPolicyPeer(llmSvc, role)
}

func networkPolicyIngressObject(llmSvc *v1alpha2.LLMInferenceService, name, role string, rules []netv1.NetworkPolicyIngressRule) *netv1.NetworkPolicy {
	return &netv1.NetworkPolicy{
		ObjectMeta: networkPolicyObjectMeta(llmSvc, name, networkPolicyComponentLabel),
		Spec: netv1.NetworkPolicySpec{
			PodSelector: networkPolicySelector(llmSvc, role),
			PolicyTypes: []netv1.PolicyType{netv1.PolicyTypeIngress},
			Ingress:     rules,
		},
	}
}

type networkPolicySchedulerPeer struct {
	peer  netv1.NetworkPolicyPeer
	ports []int32
}

func expectedWorkloadNetworkPolicy(llmSvc *v1alpha2.LLMInferenceService, gatewayNamespaces []string, gatewayPorts []int32, schedulerPeers []networkPolicySchedulerPeer, config *Config) *netv1.NetworkPolicy {
	if len(schedulerPeers) == 0 {
		ports := uniquePorts(gatewayPorts...)
		if len(ports) == 0 {
			ports = []int32{defaultWorkloadPort}
		}
		schedulerPeers = []networkPolicySchedulerPeer{{
			peer:  sameNamespacePeer(llmSvc, networkPolicyRoleScheduler),
			ports: ports,
		}}
	}
	rules := make([]netv1.NetworkPolicyIngressRule, 0, len(schedulerPeers)+3)
	for _, schedulerPeer := range schedulerPeers {
		ports := uniquePorts(schedulerPeer.ports...)
		if len(ports) == 0 {
			ports = []int32{defaultWorkloadPort}
		}
		rules = append(rules, netv1.NetworkPolicyIngressRule{
			From:  []netv1.NetworkPolicyPeer{schedulerPeer.peer},
			Ports: networkPolicyPorts(corev1.ProtocolTCP, ports...),
		})
	}
	if len(gatewayNamespaces) > 0 {
		if len(gatewayPorts) == 0 {
			gatewayPorts = []int32{defaultWorkloadPort}
		}
		rules = append([]netv1.NetworkPolicyIngressRule{gatewayIngressRule(gatewayNamespaces, gatewayPorts...)}, rules...)
	}
	// The monitoring ingress feature gate controlled the legacy optional policy.
	// These baseline policies always isolate the Pods, so metrics peers must stay
	// allowed while monitoring is enabled, including when that gate is off.
	if !monitoringDisabled {
		rules = append(rules, monitoringIngressRule(defaultWorkloadPort, config))
	}
	// Separate prefill/decode Pods and multi-node Pods can use runtime-defined
	// peer ports. Keep this rule scoped to the protected identity of the same
	// LLMISVC; it is harmless for a single workload Pod and avoids topology
	// specific gaps when a service changes between those layouts.
	rules = append(rules, netv1.NetworkPolicyIngressRule{
		From:  []netv1.NetworkPolicyPeer{sameNamespacePeer(llmSvc, networkPolicyRoleWorkload)},
		Ports: allPortsNetworkPolicyPorts(),
	})
	return networkPolicyIngressObject(llmSvc, workloadNetworkPolicyName(llmSvc), networkPolicyRoleWorkload, rules)
}

func expectedSchedulerNetworkPolicy(llmSvc *v1alpha2.LLMInferenceService, gatewayNamespaces []string, config *Config) *netv1.NetworkPolicy {
	rules := []netv1.NetworkPolicyIngressRule{}
	if grpcPort := schedulerPort(llmSvc, "grpc", defaultSchedulerGRPCPort); grpcPort > 0 {
		if len(gatewayNamespaces) > 0 {
			rules = append(rules, gatewayIngressRule(gatewayNamespaces, grpcPort))
		}
	}
	if !monitoringDisabled {
		if metricsPort := schedulerPort(llmSvc, "metrics", defaultSchedulerMetricsPort); metricsPort > 0 {
			rules = append(rules, monitoringIngressRule(metricsPort, config))
		}
	}
	if zmqPort, ok := schedulerConfiguredPort(llmSvc, "zmq"); ok {
		rules = append(rules, netv1.NetworkPolicyIngressRule{
			From:  []netv1.NetworkPolicyPeer{sameNamespacePeer(llmSvc, networkPolicyRoleWorkload)},
			Ports: []netv1.NetworkPolicyPort{networkPolicyPort(corev1.ProtocolTCP, zmqPort)},
		})
	}
	return networkPolicyIngressObject(llmSvc, schedulerNetworkPolicyName(llmSvc), networkPolicyRoleScheduler, rules)
}

func expectedTokenizerNetworkPolicy(llmSvc *v1alpha2.LLMInferenceService) *netv1.NetworkPolicy {
	return networkPolicyIngressObject(llmSvc, tokenizerNetworkPolicyName(llmSvc), networkPolicyRoleTokenizer,
		[]netv1.NetworkPolicyIngressRule{{
			From:  []netv1.NetworkPolicyPeer{sameNamespacePeer(llmSvc, networkPolicyRoleScheduler)},
			Ports: []netv1.NetworkPolicyPort{networkPolicyPort(corev1.ProtocolTCP, tokenizerServicePort)},
		}})
}

func expectedDefaultEgressNetworkPolicy(llmSvc *v1alpha2.LLMInferenceService) *netv1.NetworkPolicy {
	return &netv1.NetworkPolicy{
		ObjectMeta: networkPolicyObjectMeta(llmSvc, egressNetworkPolicyName(llmSvc), networkPolicyComponentLabel),
		Spec: netv1.NetworkPolicySpec{
			PodSelector: allServicePodsSelector(llmSvc),
			PolicyTypes: []netv1.PolicyType{netv1.PolicyTypeEgress},
			// Model storage, LoRA storage, API clients, peer runtimes, and
			// telemetry destinations are user-configured or dynamic. This is an
			// explicit allow-all egress decision under the platform contract.
			Egress: []netv1.NetworkPolicyEgressRule{{}},
		},
	}
}

func expectedDenyAllEgressNetworkPolicy(llmSvc *v1alpha2.LLMInferenceService) *netv1.NetworkPolicy {
	policy := expectedDefaultEgressNetworkPolicy(llmSvc)
	// An empty egress rule list with PolicyTypes:Egress explicitly denies all
	// egress. It is used only as a fail-closed transition state while the
	// restrictive tracing policy is being repaired.
	policy.Spec.Egress = []netv1.NetworkPolicyEgressRule{}
	return policy
}

func schedulerConfiguredPort(llmSvc *v1alpha2.LLMInferenceService, name string) (int32, bool) {
	if llmSvc.Spec.Router == nil || llmSvc.Spec.Router.Scheduler == nil || llmSvc.Spec.Router.Scheduler.Template == nil {
		return 0, false
	}
	for _, container := range llmSvc.Spec.Router.Scheduler.Template.Containers {
		for _, port := range container.Ports {
			if port.Name == name && port.ContainerPort > 0 {
				return port.ContainerPort, true
			}
		}
	}
	return 0, false
}

func schedulerPort(llmSvc *v1alpha2.LLMInferenceService, name string, fallback int32) int32 {
	if port, ok := schedulerConfiguredPort(llmSvc, name); ok {
		return port
	}
	if llmSvc.Spec.Router != nil && llmSvc.Spec.Router.Scheduler != nil && llmSvc.Spec.Router.Scheduler.Template != nil {
		// A user supplied scheduler template is authoritative. Do not admit a
		// port that the merged template does not expose.
		return 0
	}
	return fallback
}

// workloadNetworkPolicyLabels returns the labels that identify the workload to
// an InferencePool. The protected NetworkPolicy labels are deliberately not
// included: pool routing labels are user-configurable and may intentionally be
// shared by several LLMInferenceServices.
func workloadNetworkPolicyLabels(llmSvc *v1alpha2.LLMInferenceService) map[string]string {
	labels := GetWorkloadLabelSelector(llmSvc.ObjectMeta, &llmSvc.Spec)
	utils.PropagateMap(llmSvc.Spec.Labels, &labels)
	return labels
}

func poolSelectorMatchesWorkload(selector map[string]string, labels map[string]string) bool {
	if len(selector) == 0 {
		return false
	}
	for key, value := range selector {
		if labels[key] != value {
			return false
		}
	}
	return true
}

type poolRoutingConfig struct {
	selector           map[string]string
	targetPorts        []int32
	selectorPropagated bool
}

func configuredPoolRouting(llmSvc *v1alpha2.LLMInferenceService) poolRoutingConfig {
	if llmSvc.Spec.Router != nil && llmSvc.Spec.Router.Scheduler != nil && llmSvc.Spec.Router.Scheduler.Pool != nil && llmSvc.Spec.Router.Scheduler.Pool.Spec != nil {
		selector := make(map[string]string, len(llmSvc.Spec.Router.Scheduler.Pool.Spec.Selector.MatchLabels))
		for key, value := range llmSvc.Spec.Router.Scheduler.Pool.Spec.Selector.MatchLabels {
			selector[string(key)] = string(value)
		}
		ports := make([]int32, 0, len(llmSvc.Spec.Router.Scheduler.Pool.Spec.TargetPorts))
		for _, port := range llmSvc.Spec.Router.Scheduler.Pool.Spec.TargetPorts {
			if port.Number > 0 && int32(port.Number) <= maxNetworkPolicyPort {
				ports = append(ports, int32(port.Number))
			}
		}
		if len(selector) > 0 || len(ports) > 0 {
			return poolRoutingConfig{selector: selector, targetPorts: ports}
		}
	}
	return poolRoutingConfig{selector: workloadNetworkPolicyLabels(llmSvc), targetPorts: []int32{defaultWorkloadPort}, selectorPropagated: true}
}

func poolRoutingFromV1(pool *igwapi.InferencePool) poolRoutingConfig {
	selector := make(map[string]string, len(pool.Spec.Selector.MatchLabels))
	for key, value := range pool.Spec.Selector.MatchLabels {
		selector[string(key)] = string(value)
	}
	ports := make([]int32, 0, len(pool.Spec.TargetPorts))
	for _, port := range pool.Spec.TargetPorts {
		if port.Number > 0 && int32(port.Number) <= maxNetworkPolicyPort {
			ports = append(ports, int32(port.Number))
		}
	}
	return poolRoutingConfig{selector: selector, targetPorts: ports, selectorPropagated: true}
}

func poolRoutingFromV1Alpha2(pool *igwapiv1alpha2.InferencePool) poolRoutingConfig {
	selector := make(map[string]string, len(pool.Spec.Selector))
	for key, value := range pool.Spec.Selector {
		selector[string(key)] = string(value)
	}
	ports := []int32{}
	if pool.Spec.TargetPortNumber > 0 && pool.Spec.TargetPortNumber <= maxNetworkPolicyPort {
		ports = append(ports, pool.Spec.TargetPortNumber)
	}
	return poolRoutingConfig{selector: selector, targetPorts: ports, selectorPropagated: true}
}

func (r *LLMISVCReconciler) poolRouting(ctx context.Context, llmSvc *v1alpha2.LLMInferenceService) (poolRoutingConfig, error) {
	configured := configuredPoolRouting(llmSvc)
	if llmSvc.Spec.Router == nil || llmSvc.Spec.Router.Scheduler == nil || llmSvc.Spec.Router.Scheduler.Pool == nil || !llmSvc.Spec.Router.Scheduler.Pool.HasRef() {
		return configured, nil
	}

	poolName := llmSvc.Spec.Router.Scheduler.Pool.Ref.Name
	v1Pool := &igwapi.InferencePool{}
	key := types.NamespacedName{Name: poolName, Namespace: llmSvc.GetNamespace()}
	if err := r.Get(ctx, key, v1Pool); err == nil {
		return poolRoutingFromV1(v1Pool), nil
	} else if !apierrors.IsNotFound(err) && !meta.IsNoMatchError(err) {
		return poolRoutingConfig{}, fmt.Errorf("failed to get referenced InferencePool %s/%s: %w", key.Namespace, key.Name, err)
	}

	alphaPool := &igwapiv1alpha2.InferencePool{}
	if err := r.Get(ctx, key, alphaPool); err == nil {
		return poolRoutingFromV1Alpha2(alphaPool), nil
	} else if !apierrors.IsNotFound(err) && !meta.IsNoMatchError(err) {
		return poolRoutingConfig{}, fmt.Errorf("failed to get referenced v1alpha2 InferencePool %s/%s: %w", key.Namespace, key.Name, err)
	}

	// The scheduler reconciliation will report a missing referenced pool. Keep
	// policy reconciliation retryable but retain the service's own labels so a
	// later pool creation can immediately repair the policy.
	return configured, nil
}

func hasExternalPoolReference(llmSvc *v1alpha2.LLMInferenceService) bool {
	return llmSvc != nil && llmSvc.Spec.Router != nil && llmSvc.Spec.Router.Scheduler != nil &&
		llmSvc.Spec.Router.Scheduler.Pool != nil && llmSvc.Spec.Router.Scheduler.Pool.HasRef()
}

// gatewayNamespacesForNetworkPolicy starts with the service's own Gateway
// references and then discovers sibling LLMIServices whose managed pool
// selector matches this service's workload labels. This covers the supported
// shared-pool pattern where a routerless LLMISVC joins another service's pool by
// overriding spec.labels (for example the multi-GPU vendor sample).
func (r *LLMISVCReconciler) gatewayNamespacesForNetworkPolicy(ctx context.Context, llmSvc *v1alpha2.LLMInferenceService, ingressNs string, config *Config) ([]string, []int32, []networkPolicySchedulerPeer, error) {
	namespaces := uniqueNamespaces(gatewayPeerNamespaces(llmSvc, ingressNs)...)
	if llmSvc.Spec.Router != nil && llmSvc.Spec.Router.Route != nil && llmSvc.Spec.Router.Route.HTTP != nil && llmSvc.Spec.Router.Route.HTTP.HasRefs() {
		for _, ref := range llmSvc.Spec.Router.Route.HTTP.Refs {
			route := &gwapiv1.HTTPRoute{}
			key := types.NamespacedName{Namespace: llmSvc.GetNamespace(), Name: ref.Name}
			if err := r.Get(ctx, key, route); err != nil {
				return nil, nil, nil, fmt.Errorf("failed to get referenced HTTPRoute %s/%s for Gateway NetworkPolicy peers: %w", key.Namespace, key.Name, err)
			}
			for _, parent := range route.Spec.ParentRefs {
				if parent.Kind != nil && string(*parent.Kind) != "Gateway" {
					continue
				}
				ns := route.GetNamespace()
				if parent.Namespace != nil && string(*parent.Namespace) != "" {
					ns = string(*parent.Namespace)
				}
				namespaces = uniqueNamespaces(append(namespaces, ns)...)
			}
		}
	}
	targetRouting, err := r.poolRouting(ctx, llmSvc)
	if err != nil {
		return nil, nil, nil, err
	}
	workloadLabels := workloadNetworkPolicyLabels(llmSvc)
	if targetRouting.selectorPropagated {
		for key, value := range targetRouting.selector {
			workloadLabels[key] = value
		}
	}
	ports := uniquePorts(targetRouting.targetPorts...)
	if len(ports) == 0 {
		ports = []int32{defaultWorkloadPort}
	}
	schedulerPeers := make([]networkPolicySchedulerPeer, 0, 1)
	if hasManagedScheduler(llmSvc) {
		schedulerPeers = append(schedulerPeers, networkPolicySchedulerPeer{
			peer:  sameNamespacePeer(llmSvc, networkPolicyRoleScheduler),
			ports: ports,
		})
	}
	matchedSchedulerPeer := false

	services := &v1alpha2.LLMInferenceServiceList{}
	if err := r.List(ctx, services, client.InNamespace(llmSvc.GetNamespace())); err != nil {
		return nil, nil, nil, fmt.Errorf("failed to list LLMInferenceServices for shared InferencePool discovery: %w", err)
	}

	for i := range services.Items {
		peer := services.Items[i].DeepCopy()
		if peer.GetUID() == llmSvc.GetUID() || peer.GetName() == llmSvc.GetName() {
			continue
		}
		if config != nil {
			combined, err := r.combineBaseRefsConfig(ctx, peer, config)
			if err != nil {
				// A sibling with an invalid or unavailable base config cannot be
				// safely treated as a shared-pool Gateway source. Its own reconcile
				// will report the configuration error and a later generation event
				// will retry this dependency scan.
				continue
			}
			peer.Spec = combined.Config.Spec
		}
		if peer.Spec.Router == nil || peer.Spec.Router.Scheduler == nil || peer.Spec.Router.Scheduler.Pool == nil {
			continue
		}
		pool, err := r.poolRouting(ctx, peer)
		if err != nil {
			return nil, nil, nil, err
		}
		if !poolSelectorMatchesWorkload(pool.selector, workloadLabels) {
			continue
		}
		namespaces = uniqueNamespaces(append(namespaces, gatewayPeerNamespaces(peer, ingressNs)...)...)
		ports = uniquePorts(append(ports, pool.targetPorts...)...)
		if hasManagedScheduler(peer) {
			peerPorts := uniquePorts(pool.targetPorts...)
			if len(peerPorts) == 0 {
				peerPorts = []int32{defaultWorkloadPort}
			}
			schedulerPeers = append(schedulerPeers, networkPolicySchedulerPeer{
				peer:  sameNamespacePeer(peer, networkPolicyRoleScheduler),
				ports: peerPorts,
			})
			matchedSchedulerPeer = true
		}
	}

	if hasExternalPoolReference(llmSvc) && !matchedSchedulerPeer {
		// A third-party EPP in this namespace cannot be reverse-mapped to a
		// protected scheduler identity yet. Keep the temporary exception narrow:
		// same-namespace sources may reach only the resolved pool target ports.
		schedulerPeers = append(schedulerPeers, networkPolicySchedulerPeer{
			peer:  namespaceSelectorPeer(llmSvc.GetNamespace()),
			ports: ports,
		})
	}

	return namespaces, ports, schedulerPeers, nil
}

func uniquePorts(ports ...int32) []int32 {
	seen := make(map[int32]struct{}, len(ports))
	result := make([]int32, 0, len(ports))
	for _, port := range ports {
		if port <= 0 || port > maxNetworkPolicyPort {
			continue
		}
		if _, ok := seen[port]; ok {
			continue
		}
		seen[port] = struct{}{}
		result = append(result, port)
	}
	return result
}

// enqueueSharedNetworkPolicyDependents conservatively requeues every
// LLMInferenceService in the cluster. A sibling's pool
// selector or Gateway reference can change which source namespace and target
// ports a routerless shared-pool service needs; reverse-mapping only by
// protected labels would miss that dependency. Generation-only watches keep
// status updates from creating a reconcile loop.
func (r *LLMISVCReconciler) enqueueSharedNetworkPolicyDependents(ctx context.Context, object client.Object) []reconcile.Request {
	services := &v1alpha2.LLMInferenceServiceList{}
	if err := r.List(ctx, services); err != nil {
		return nil
	}
	requests := make([]reconcile.Request, 0, len(services.Items))
	for i := range services.Items {
		requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{
			Namespace: services.Items[i].GetNamespace(),
			Name:      services.Items[i].GetName(),
		}})
	}
	return requests
}

func deleteOwnedNetworkPolicy(ctx context.Context, r *LLMISVCReconciler, llmSvc *v1alpha2.LLMInferenceService, expected *netv1.NetworkPolicy) error {
	current := &netv1.NetworkPolicy{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: expected.GetNamespace(), Name: expected.GetName()}, current); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	if !metav1.IsControlledBy(current, llmSvc) {
		// Never delete an object with the expected name that belongs to another
		// controller. Surface the conflict instead: leaving the legacy broad
		// policy additive would defeat the migration's least-privilege claim.
		return fmt.Errorf("NetworkPolicy %s/%s is not controlled by LLMInferenceService %s/%s",
			expected.GetNamespace(), expected.GetName(), llmSvc.GetNamespace(), llmSvc.GetName())
	}
	// Ownership was verified above. Pass a typed nil owner so lifecycle_crud.Delete
	// does not defer this cleanup to garbage collection while the LLMISVC is
	// terminating; envtest and clusters without a functioning GC must still lose
	// the policy before the controller removes the finalizer.
	return Delete(ctx, r, (*v1alpha2.LLMInferenceService)(nil), expected)
}

func reconcileLLMISVCNetworkPolicies(ctx context.Context, r *LLMISVCReconciler, llmSvc *v1alpha2.LLMInferenceService, config *Config) error {
	if networkPolicyDisabled {
		return nil
	}

	// Remove the former monitoring policy. It included a blanket same-namespace
	// all-port ingress rule and must not remain additive after migration.
	if err := deleteOwnedNetworkPolicy(ctx, r, llmSvc, &netv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: legacyMonitoringNetworkPolicyName(llmSvc), Namespace: llmSvc.GetNamespace()},
	}); err != nil {
		return fmt.Errorf("failed to remove legacy monitoring NetworkPolicy: %w", err)
	}

	tracingRequested := llmSvc.Spec.Tracing != nil && llmSvc.GetAnnotations()[constants.EnableTracingEgressNetworkPolicyAnnotationKey] == "true"
	if tracingRequested {
		// Remove the broad default before attempting the restrictive policy so a
		// failed tracing reconcile cannot leave both policies selecting the same
		// Pods. A retry restores the restrictive policy when its dependencies are
		// available.
		if err := deleteOwnedNetworkPolicy(ctx, r, llmSvc, expectedDefaultEgressNetworkPolicy(llmSvc)); err != nil {
			return fmt.Errorf("failed to remove default LLMISVC egress NetworkPolicy: %w", err)
		}
	}
	if err := r.reconcileTracingNetworkPolicy(ctx, llmSvc); err != nil {
		if tracingRequested {
			if denyErr := reconcileOwnedNetworkPolicy(ctx, r, llmSvc, expectedDenyAllEgressNetworkPolicy(llmSvc)); denyErr != nil {
				return fmt.Errorf("failed to preserve fail-closed LLMISVC egress while tracing policy reconciliation failed: %w; tracing error: %w", denyErr, err)
			}
		}
		return err
	}

	if !tracingRequested {
		if err := reconcileOwnedNetworkPolicy(ctx, r, llmSvc, expectedDefaultEgressNetworkPolicy(llmSvc)); err != nil {
			return err
		}
	}

	gatewayNamespaces, gatewayPorts, schedulerPeers, err := r.gatewayNamespacesForNetworkPolicy(ctx, llmSvc, ingressNamespace(config), config)
	if err != nil {
		if meta.IsNoMatchError(err) {
			// InferencePool CRDs are optional. A missing optional API must not
			// prevent baseline policies for services that do not use a pool.
			gatewayNamespaces = uniqueNamespaces(gatewayPeerNamespaces(llmSvc, ingressNamespace(config))...)
			gatewayPorts = []int32{defaultWorkloadPort}
			schedulerPeers = nil
		} else {
			return err
		}
	}

	policies := []*netv1.NetworkPolicy{
		expectedWorkloadNetworkPolicy(llmSvc, gatewayNamespaces, gatewayPorts, schedulerPeers, config),
		expectedSchedulerNetworkPolicy(llmSvc, gatewayNamespaces, config),
		expectedTokenizerNetworkPolicy(llmSvc),
	}
	for _, policy := range policies {
		if err := reconcileOwnedNetworkPolicy(ctx, r, llmSvc, policy); err != nil {
			return err
		}
	}
	return nil
}

func cleanupLLMISVCNetworkPolicies(ctx context.Context, r *LLMISVCReconciler, llmSvc *v1alpha2.LLMInferenceService) error {
	policies := []*netv1.NetworkPolicy{
		{ObjectMeta: metav1.ObjectMeta{Name: legacyMonitoringNetworkPolicyName(llmSvc), Namespace: llmSvc.GetNamespace()}},
		{ObjectMeta: metav1.ObjectMeta{Name: workloadNetworkPolicyName(llmSvc), Namespace: llmSvc.GetNamespace()}},
		{ObjectMeta: metav1.ObjectMeta{Name: schedulerNetworkPolicyName(llmSvc), Namespace: llmSvc.GetNamespace()}},
		{ObjectMeta: metav1.ObjectMeta{Name: tokenizerNetworkPolicyName(llmSvc), Namespace: llmSvc.GetNamespace()}},
		{ObjectMeta: metav1.ObjectMeta{Name: egressNetworkPolicyName(llmSvc), Namespace: llmSvc.GetNamespace()}},
		{ObjectMeta: metav1.ObjectMeta{Name: tracingNetworkPolicyName(llmSvc), Namespace: llmSvc.GetNamespace()}},
	}
	for _, policy := range policies {
		if err := deleteOwnedNetworkPolicy(ctx, r, llmSvc, policy); err != nil {
			return fmt.Errorf("failed to delete NetworkPolicy %s/%s: %w", policy.GetNamespace(), policy.GetName(), err)
		}
	}
	return nil
}

func reconcileOwnedNetworkPolicy(ctx context.Context, r *LLMISVCReconciler, llmSvc *v1alpha2.LLMInferenceService, expected *netv1.NetworkPolicy) error {
	if err := Reconcile(ctx, r, llmSvc, &netv1.NetworkPolicy{}, expected, semanticNetworkPolicyIsEqual); err != nil {
		return fmt.Errorf("failed to reconcile NetworkPolicy %s/%s: %w", expected.GetNamespace(), expected.GetName(), err)
	}
	return nil
}
