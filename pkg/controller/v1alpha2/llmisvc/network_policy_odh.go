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
	"errors"
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
	"sigs.k8s.io/controller-runtime/pkg/log"
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
	networkPolicyAllowAllSuffix  = "-networkpolicy-allow-all"

	legacyMonitoringNetworkPolicySuffix = "-prometheus-scraping"

	// The two ingress policy modes. Policy names embed the mode
	// (<service>-networkpolicy-<mode>-<component-key>), so the values must stay
	// stable across releases.
	networkPolicyModeDefault = "default"
	networkPolicyModeStrict  = "strict"

	networkPolicyComponentLabel = "llm-network-policy"
	defaultWorkloadPort         = int32(8000)
	defaultSchedulerGRPCPort    = int32(9002)
	defaultSchedulerMetricsPort = int32(9090)
	maxNetworkPolicyPort        = int32(65535)
)

var networkPolicyDisabled, _ = env.GetBool(networkPolicyDisabledEnvVar, false)

var errNetworkPolicyOwnershipConflict = errors.New("NetworkPolicy ownership conflict")

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

func allowAllNetworkPolicyName(llmSvc *v1alpha2.LLMInferenceService) string {
	return networkPolicyName(llmSvc, networkPolicyAllowAllSuffix)
}

func legacyMonitoringNetworkPolicyName(llmSvc *v1alpha2.LLMInferenceService) string {
	return kmeta.ChildName(llmSvc.GetName(), legacyMonitoringNetworkPolicySuffix)
}

// networkPolicyComponentKeys lists every fixed component key of the selector
// inventory. Stale-policy cleanup uses it to bound the policy names this
// controller may own, so it must cover each key networkPolicySelectors can
// produce.
var networkPolicyComponentKeys = []string{
	networkPolicyComponentMain,
	networkPolicyComponentPrefill,
	networkPolicyComponentScheduler,
	networkPolicyComponentTokenizer,
	networkPolicyComponentMainMN,
	networkPolicyComponentPrefillMN,
	networkPolicyComponentPD,
}

// ingressNetworkPolicyName returns the deterministic policy name
// <service>-networkpolicy-<mode>-<component-key>.
func ingressNetworkPolicyName(llmSvc *v1alpha2.LLMInferenceService, mode, component string) string {
	return kmeta.ChildName(llmSvc.GetName(), "-networkpolicy-"+mode+"-"+component)
}

// ingressNetworkPolicyNames returns the bounded set of per-component ingress
// policy names for one mode.
func ingressNetworkPolicyNames(llmSvc *v1alpha2.LLMInferenceService, mode string) []string {
	names := make([]string, 0, len(networkPolicyComponentKeys))
	for _, component := range networkPolicyComponentKeys {
		names = append(names, ingressNetworkPolicyName(llmSvc, mode, component))
	}
	return names
}

// staleIngressNetworkPolicyNames returns the bounded set of ingress policy
// names a reconcile of mode no longer desires: every per-component name of
// both modes except the desired components' names of the reconciled mode —
// covering the other mode after a mode switch and the components that left
// the selector inventory after a topology change — plus the pre-selector
// strict-mode names that a per-component reconcile no longer overwrites. The
// set stays bounded to the fixed component keys and the historical suffixes;
// cleanup never sweeps unrelated service-owned resources.
func staleIngressNetworkPolicyNames(llmSvc *v1alpha2.LLMInferenceService, mode string, inventory networkPolicySelectorInventory) []string {
	desired := make(map[string]struct{}, len(inventory))
	for _, entry := range inventory {
		desired[ingressNetworkPolicyName(llmSvc, mode, entry.Component)] = struct{}{}
	}
	names := make([]string, 0, 2*len(networkPolicyComponentKeys)+3)
	for _, component := range networkPolicyComponentKeys {
		for _, candidateMode := range []string{networkPolicyModeDefault, networkPolicyModeStrict} {
			name := ingressNetworkPolicyName(llmSvc, candidateMode, component)
			if _, keep := desired[name]; !keep {
				names = append(names, name)
			}
		}
	}
	return append(names,
		workloadNetworkPolicyName(llmSvc),
		schedulerNetworkPolicyName(llmSvc),
		tokenizerNetworkPolicyName(llmSvc),
	)
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

// networkPolicyIngressObject builds an ingress-only policy that selects the
// Pods of one inventory entry through the entry's complete stored selector.
func networkPolicyIngressObject(llmSvc *v1alpha2.LLMInferenceService, name string, selector metav1.LabelSelector, rules []netv1.NetworkPolicyIngressRule) *netv1.NetworkPolicy {
	return &netv1.NetworkPolicy{
		ObjectMeta: networkPolicyObjectMeta(llmSvc, name, networkPolicyComponentLabel),
		Spec: netv1.NetworkPolicySpec{
			PodSelector: *selector.DeepCopy(),
			PolicyTypes: []netv1.PolicyType{netv1.PolicyTypeIngress},
			Ingress:     rules,
		},
	}
}

// selectorPeers converts inventory entries to same-namespace NetworkPolicy
// source peers. Each entry keeps its complete stored selector as its own peer;
// selectors are never collapsed into a shared application-name-only selector.
func selectorPeers(entries networkPolicySelectorInventory) []netv1.NetworkPolicyPeer {
	peers := make([]netv1.NetworkPolicyPeer, 0, len(entries))
	for i := range entries {
		peers = append(peers, netv1.NetworkPolicyPeer{PodSelector: entries[i].Selector.DeepCopy()})
	}
	return peers
}

type networkPolicySchedulerPeer struct {
	peer  netv1.NetworkPolicyPeer
	ports []int32
}

// expectedDefaultIngressNetworkPolicies returns the default-mode policy set:
// one ingress-only policy per inventory entry that selects the entry's Pods
// through its complete stored selector and allows all ingress to them. No
// egress rules are created; egress remains governed by the opt-in tracing
// policy alone.
func expectedDefaultIngressNetworkPolicies(llmSvc *v1alpha2.LLMInferenceService, inventory networkPolicySelectorInventory) []*netv1.NetworkPolicy {
	policies := make([]*netv1.NetworkPolicy, 0, len(inventory))
	for _, entry := range inventory {
		policies = append(policies, networkPolicyIngressObject(llmSvc,
			ingressNetworkPolicyName(llmSvc, networkPolicyModeDefault, entry.Component),
			entry.Selector,
			[]netv1.NetworkPolicyIngressRule{{}}))
	}
	return policies
}

// expectedStrictIngressNetworkPolicies returns the strict-mode policy set: one
// ingress policy per inventory entry, with the entry's policy-local class
// deciding its traffic rules. Workload entries keep Gateway, scheduler, and
// monitoring access plus all-protocol workload-peer traffic; scheduler entries
// keep Gateway gRPC, monitoring metrics, and configured workload-to-ZMQ access;
// tokenizer entries keep scheduler-to-tokenizer access.
func expectedStrictIngressNetworkPolicies(llmSvc *v1alpha2.LLMInferenceService, inventory networkPolicySelectorInventory, gatewayNamespaces []string, gatewayPorts []int32, schedulerPeers []networkPolicySchedulerPeer, config *Config) []*netv1.NetworkPolicy {
	workloads := inventory.ofClass(networkPolicyRoleWorkload)
	schedulers := inventory.ofClass(networkPolicyRoleScheduler)
	tokenizers := inventory.ofClass(networkPolicyRoleTokenizer)
	workloadPeerSources := selectorPeers(workloads)
	schedulerPeerSources := selectorPeers(schedulers)

	policies := make([]*netv1.NetworkPolicy, 0, len(inventory))
	for _, entry := range workloads {
		policies = append(policies, expectedStrictWorkloadIngressNetworkPolicy(llmSvc, entry, workloadPeerSources, gatewayNamespaces, gatewayPorts, schedulerPeers, config))
	}
	for _, entry := range schedulers {
		policies = append(policies, expectedStrictSchedulerIngressNetworkPolicy(llmSvc, entry, workloadPeerSources, gatewayNamespaces, config))
	}
	for _, entry := range tokenizers {
		policies = append(policies, expectedStrictTokenizerIngressNetworkPolicy(llmSvc, entry, schedulerPeerSources))
	}
	return policies
}

// expectedStrictWorkloadIngressNetworkPolicy builds the strict-mode ingress
// policy for one workload inventory entry. The workload-peer rule covers every
// workload selector of the service — including the target's own, which spans
// multi-node leaders and workers — so compute-only workers are admitted
// without any InferencePool serving marker or llm-d role label.
func expectedStrictWorkloadIngressNetworkPolicy(llmSvc *v1alpha2.LLMInferenceService, entry networkPolicySelectorEntry, workloadPeerSources []netv1.NetworkPolicyPeer, gatewayNamespaces []string, gatewayPorts []int32, schedulerPeers []networkPolicySchedulerPeer, config *Config) *netv1.NetworkPolicy {
	rules := make([]netv1.NetworkPolicyIngressRule, 0, len(schedulerPeers)+3)
	if len(gatewayNamespaces) > 0 {
		ports := uniquePorts(gatewayPorts...)
		if len(ports) == 0 {
			ports = []int32{defaultWorkloadPort}
		}
		rules = append(rules, gatewayIngressRule(gatewayNamespaces, ports...))
	}
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
	// The strict NetworkPolicy feature gate (and before it the legacy monitoring
	// gate) is not consulted here: when strict policies are reconciled, metrics
	// peers must stay allowed while monitoring is enabled.
	if !monitoringDisabled {
		rules = append(rules, monitoringIngressRule(defaultWorkloadPort, config))
	}
	// Separate prefill/decode Pods and multi-node Pods can use runtime-defined
	// peer ports, so the full port range is admitted for the service's own
	// workload selectors only.
	rules = append(rules, netv1.NetworkPolicyIngressRule{
		From:  workloadPeerSources,
		Ports: allPortsNetworkPolicyPorts(),
	})
	return networkPolicyIngressObject(llmSvc, ingressNetworkPolicyName(llmSvc, networkPolicyModeStrict, entry.Component), entry.Selector, rules)
}

// expectedStrictSchedulerIngressNetworkPolicy builds the strict-mode ingress
// policy for one scheduler inventory entry. Configured workload-to-ZMQ access
// is granted from the workload inventory peers; when no workload is stored yet
// the rule is skipped, which denies the port instead of broadening the source.
func expectedStrictSchedulerIngressNetworkPolicy(llmSvc *v1alpha2.LLMInferenceService, entry networkPolicySelectorEntry, workloadPeerSources []netv1.NetworkPolicyPeer, gatewayNamespaces []string, config *Config) *netv1.NetworkPolicy {
	rules := []netv1.NetworkPolicyIngressRule{}
	if grpcPort := schedulerPort(llmSvc, "grpc", defaultSchedulerGRPCPort); grpcPort > 0 && len(gatewayNamespaces) > 0 {
		rules = append(rules, gatewayIngressRule(gatewayNamespaces, grpcPort))
	}
	if !monitoringDisabled {
		if metricsPort := schedulerPort(llmSvc, "metrics", defaultSchedulerMetricsPort); metricsPort > 0 {
			rules = append(rules, monitoringIngressRule(metricsPort, config))
		}
	}
	if zmqPort, ok := schedulerConfiguredPort(llmSvc, "zmq"); ok && len(workloadPeerSources) > 0 {
		rules = append(rules, netv1.NetworkPolicyIngressRule{
			From:  workloadPeerSources,
			Ports: []netv1.NetworkPolicyPort{networkPolicyPort(corev1.ProtocolTCP, zmqPort)},
		})
	}
	return networkPolicyIngressObject(llmSvc, ingressNetworkPolicyName(llmSvc, networkPolicyModeStrict, entry.Component), entry.Selector, rules)
}

// expectedStrictTokenizerIngressNetworkPolicy builds the strict-mode ingress
// policy for one tokenizer inventory entry. Without a stored scheduler there
// is no legitimate source, so the policy keeps no ingress rules — denying all
// ingress — rather than emitting an empty From that would admit everything.
func expectedStrictTokenizerIngressNetworkPolicy(llmSvc *v1alpha2.LLMInferenceService, entry networkPolicySelectorEntry, schedulerPeerSources []netv1.NetworkPolicyPeer) *netv1.NetworkPolicy {
	var rules []netv1.NetworkPolicyIngressRule
	if len(schedulerPeerSources) > 0 {
		rules = append(rules, netv1.NetworkPolicyIngressRule{
			From:  schedulerPeerSources,
			Ports: []netv1.NetworkPolicyPort{networkPolicyPort(corev1.ProtocolTCP, tokenizerServicePort)},
		})
	}
	return networkPolicyIngressObject(llmSvc, ingressNetworkPolicyName(llmSvc, networkPolicyModeStrict, entry.Component), entry.Selector, rules)
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
	// The own managed scheduler is admitted through its stored Deployment
	// selector. A managed scheduler that is not stored yet is an error so the
	// reconcile retries, rather than emitting an empty peer or widening the
	// source beyond the scheduler's Pods.
	schedulerPeers := make([]networkPolicySchedulerPeer, 0, 1)
	ownScheduler, err := r.managedSchedulerNetworkPolicySelector(ctx, llmSvc)
	if err != nil {
		return nil, nil, nil, err
	}
	if ownScheduler != nil {
		schedulerPeers = append(schedulerPeers, networkPolicySchedulerPeer{
			peer:  netv1.NetworkPolicyPeer{PodSelector: ownScheduler},
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
			// Like the own scheduler, a shared-pool sibling's managed scheduler is
			// admitted only through its stored Deployment selector; an unresolved
			// one aborts so the reconcile retries without widening the source. A
			// force-stopped sibling's scheduler Deployment is deliberately absent:
			// it yields no peer, which must be skipped — never appended as an
			// empty PodSelector — and it still counts as a matched scheduler so
			// the unmapped third-party EPP fallback below is not widened to the
			// whole namespace for a scheduler this controller knows.
			peerScheduler, err := r.managedSchedulerNetworkPolicySelector(ctx, peer)
			if err != nil {
				return nil, nil, nil, err
			}
			peerPorts := uniquePorts(pool.targetPorts...)
			if len(peerPorts) == 0 {
				peerPorts = []int32{defaultWorkloadPort}
			}
			matchedSchedulerPeer = true
			if peerScheduler != nil {
				schedulerPeers = append(schedulerPeers, networkPolicySchedulerPeer{
					peer:  netv1.NetworkPolicyPeer{PodSelector: peerScheduler},
					ports: peerPorts,
				})
			}
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
		return fmt.Errorf("%w: NetworkPolicy %s/%s is not controlled by LLMInferenceService %s/%s",
			errNetworkPolicyOwnershipConflict,
			expected.GetNamespace(), expected.GetName(), llmSvc.GetNamespace(), llmSvc.GetName())
	}
	// Ownership was verified above. Pass a typed nil owner so lifecycle_crud.Delete
	// does not defer this cleanup to garbage collection while the LLMISVC is
	// terminating; envtest and clusters without a functioning GC must still lose
	// the policy before the controller removes the finalizer.
	return Delete(ctx, r, (*v1alpha2.LLMInferenceService)(nil), expected)
}

// deleteOwnedNetworkPolicies removes a bounded set of owned policy names.
func deleteOwnedNetworkPolicies(ctx context.Context, r *LLMISVCReconciler, llmSvc *v1alpha2.LLMInferenceService, names []string) error {
	for _, name := range names {
		policy := &netv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: llmSvc.GetNamespace()}}
		if err := deleteOwnedNetworkPolicy(ctx, r, llmSvc, policy); err != nil {
			return fmt.Errorf("failed to remove NetworkPolicy %s/%s: %w", policy.GetNamespace(), policy.GetName(), err)
		}
	}
	return nil
}

func reconcileLLMISVCNetworkPolicies(ctx context.Context, r *LLMISVCReconciler, llmSvc *v1alpha2.LLMInferenceService, config *Config) error {
	if networkPolicyDisabled {
		return nil
	}

	strict := config != nil && config.FeatureGates.StrictNetworkPolicy
	// Ingress coverage and the opt-in tracing policy reconcile independently:
	// an exporter lookup or write failure must not leave the service without
	// ingress policies, and an ingress failure must not skip the tracing
	// lifecycle. Each step's error is collected so one pass reports every
	// problem and the reconcile retries.
	var errs error
	if strict {
		errs = reconcileStrictNetworkPolicies(ctx, r, llmSvc, config)
	} else {
		errs = reconcileDefaultModeNetworkPolicies(ctx, r, llmSvc)
	}

	// The general egress policy earlier builds created is egress-only: its
	// removal cannot open an ingress gap, so it is independent of the ingress
	// reconciliation above. Removal stays ownership-checked, and a failure is
	// reported instead of successful tracing establishment and retried.
	if err := deleteOwnedNetworkPolicy(ctx, r, llmSvc, &netv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: egressNetworkPolicyName(llmSvc), Namespace: llmSvc.GetNamespace()},
	}); err != nil {
		errs = errors.Join(errs, fmt.Errorf("failed to remove broad egress NetworkPolicy %s/%s: %w",
			llmSvc.GetNamespace(), egressNetworkPolicyName(llmSvc), err))
	}

	// The former monitoring policy and the branch-created combined allow-all
	// policy are superseded ingress coverage, so they are removed only after
	// the complete desired ingress set exists: create-before-delete, and no
	// pruning of prior coverage while an inventory or write failure is being
	// retried. A prune failure likewise defers this removal to the retry; the
	// tracing lifecycle below still runs and reports its own errors.
	if errs == nil {
		for _, name := range []string{legacyMonitoringNetworkPolicyName(llmSvc), allowAllNetworkPolicyName(llmSvc)} {
			policy := &netv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: llmSvc.GetNamespace()}}
			if err := deleteOwnedNetworkPolicy(ctx, r, llmSvc, policy); err != nil {
				errs = errors.Join(errs, fmt.Errorf("failed to remove superseded NetworkPolicy %s/%s: %w",
					policy.GetNamespace(), policy.GetName(), err))
			}
		}
	} else {
		log.FromContext(ctx).Info("Deferring superseded NetworkPolicy removal until the desired ingress set is reconciled",
			"namespace", llmSvc.GetNamespace(), "service", llmSvc.GetName())
	}

	// The existing tracing policy is retained when the exporter lookup or the
	// policy write fails; the error is reported and the reconcile retries
	// without a broad or deny-all egress fallback.
	return errors.Join(errs, r.reconcileTracingNetworkPolicy(ctx, llmSvc))
}

// reconcileStrictNetworkPolicies enforces the least-privilege policy set: one
// ingress policy per stored component, scoped to that component's stored Pod
// selector. Egress is not policed unless the opt-in tracing policy governs it.
// It runs when the llmisvc.featureGates.strictNetworkPolicy gate is enabled.
func reconcileStrictNetworkPolicies(ctx context.Context, r *LLMISVCReconciler, llmSvc *v1alpha2.LLMInferenceService, config *Config) error {
	inventory, err := r.networkPolicySelectors(ctx, llmSvc)
	if err != nil {
		return fmt.Errorf("failed to build the NetworkPolicy selector inventory: %w", err)
	}
	gatewayNamespaces, gatewayPorts, schedulerPeers, err := r.gatewayNamespacesForNetworkPolicy(ctx, llmSvc, ingressNamespace(config), config)
	if err != nil {
		if meta.IsNoMatchError(err) {
			// InferencePool CRDs are optional. A missing optional API must not
			// prevent baseline policies for services that do not use a pool.
			gatewayNamespaces = uniqueNamespaces(gatewayPeerNamespaces(llmSvc, ingressNamespace(config))...)
			gatewayPorts = []int32{defaultWorkloadPort}
			schedulerPeers = nil
			// The own scheduler selector is stored with its Deployment, not with
			// the optional pool APIs, so keep that peer during the fallback.
			ownScheduler, serr := r.managedSchedulerNetworkPolicySelector(ctx, llmSvc)
			if serr != nil {
				return fmt.Errorf("failed to resolve the managed scheduler selector during the optional-API fallback: %w", serr)
			}
			if ownScheduler != nil {
				schedulerPeers = append(schedulerPeers, networkPolicySchedulerPeer{
					peer:  netv1.NetworkPolicyPeer{PodSelector: ownScheduler},
					ports: gatewayPorts,
				})
			}
		} else {
			return fmt.Errorf("failed to resolve Gateway NetworkPolicy peers: %w", err)
		}
	}

	policies := expectedStrictIngressNetworkPolicies(llmSvc, inventory, gatewayNamespaces, gatewayPorts, schedulerPeers, config)
	for _, policy := range policies {
		if err := reconcileOwnedNetworkPolicy(ctx, r, llmSvc, policy); err != nil {
			return err
		}
	}

	// The complete desired strict set exists, so remove every other owned
	// ingress policy: the permissive default-mode names, the strict-mode names
	// of components that left the selector inventory, and the pre-selector
	// strict-mode names. Delete last so a failure above leaves the prior
	// coverage in place instead of a partial strict set.
	return deleteOwnedNetworkPolicies(ctx, r, llmSvc, staleIngressNetworkPolicyNames(llmSvc, networkPolicyModeStrict, inventory))
}

// reconcileDefaultModeNetworkPolicies runs when the strict gate is off (the
// default): it maintains one ingress-only allow-all policy per stored
// component and removes the strict-mode policies from an earlier strict-mode
// reconcile. Tracing is independent of the gate and of this reconciliation:
// when tracing is requested the opt-in tracing policy governs egress without
// changing ingress allowance.
func reconcileDefaultModeNetworkPolicies(ctx context.Context, r *LLMISVCReconciler, llmSvc *v1alpha2.LLMInferenceService) error {
	inventory, err := r.networkPolicySelectors(ctx, llmSvc)
	if err != nil {
		return fmt.Errorf("failed to build the NetworkPolicy selector inventory: %w", err)
	}
	for _, policy := range expectedDefaultIngressNetworkPolicies(llmSvc, inventory) {
		if err := reconcileOwnedNetworkPolicy(ctx, r, llmSvc, policy); err != nil {
			return err
		}
	}

	// The complete desired default set exists, so remove every other owned
	// ingress policy: the strict-mode names after a mode switch, the
	// default-mode names of components that left the selector inventory, and
	// the pre-selector strict-mode names.
	return deleteOwnedNetworkPolicies(ctx, r, llmSvc, staleIngressNetworkPolicyNames(llmSvc, networkPolicyModeDefault, inventory))
}

func cleanupLLMISVCNetworkPolicies(ctx context.Context, r *LLMISVCReconciler, llmSvc *v1alpha2.LLMInferenceService) error {
	names := make([]string, 0, 7+2*len(networkPolicyComponentKeys))
	names = append(names,
		legacyMonitoringNetworkPolicyName(llmSvc),
		workloadNetworkPolicyName(llmSvc),
		schedulerNetworkPolicyName(llmSvc),
		tokenizerNetworkPolicyName(llmSvc),
		egressNetworkPolicyName(llmSvc),
		allowAllNetworkPolicyName(llmSvc),
		tracingNetworkPolicyName(llmSvc),
	)
	names = append(names, ingressNetworkPolicyNames(llmSvc, networkPolicyModeDefault)...)
	names = append(names, ingressNetworkPolicyNames(llmSvc, networkPolicyModeStrict)...)
	for _, name := range names {
		policy := &netv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: llmSvc.GetNamespace()}}
		if err := deleteOwnedNetworkPolicy(ctx, r, llmSvc, policy); err != nil {
			if errors.Is(err, errNetworkPolicyOwnershipConflict) {
				log.FromContext(ctx).Info("Skipping NetworkPolicy cleanup because it is not controlled by the LLMInferenceService",
					"namespace", policy.GetNamespace(), "name", policy.GetName(), "service", llmSvc.GetNamespace()+"/"+llmSvc.GetName(), "error", err)
				continue
			}
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
