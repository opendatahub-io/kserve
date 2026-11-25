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
	"reflect"
	"strconv"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	disaggregatedsetv1 "sigs.k8s.io/lws/api/disaggregatedset/v1"
	lwsapi "sigs.k8s.io/lws/api/leaderworkerset/v1"

	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
	"github.com/kserve/kserve/pkg/constants"
)

func TestAllPortsNetworkPolicyPorts(t *testing.T) {
	ports := allPortsNetworkPolicyPorts()
	if len(ports) != 3 {
		t.Fatalf("got %d protocol ranges, want 3", len(ports))
	}
	for _, port := range ports {
		if port.Port == nil || port.Port.IntValue() != 1 || port.EndPort == nil || *port.EndPort != maxNetworkPolicyPort {
			t.Fatalf("got port range %#v, want 1-%d", port, maxNetworkPolicyPort)
		}
	}
}

func TestExpectedStrictWorkloadNetworkPolicyUsesReferencedGatewayNamespacesAndStoredSelectors(t *testing.T) {
	service := &v1alpha2.LLMInferenceService{
		ObjectMeta: metav1.ObjectMeta{Name: "model", Namespace: "team-a"},
		Spec: v1alpha2.LLMInferenceServiceSpec{
			WorkloadSpec: v1alpha2.WorkloadSpec{Worker: &corev1.PodSpec{}},
			Router: &v1alpha2.RouterSpec{Gateway: &v1alpha2.GatewaySpec{Refs: []v1alpha2.GatewayObjectReference{
				{UntypedObjectReference: v1alpha2.UntypedObjectReference{Name: "gateway", Namespace: "team-a"}},
				{UntypedObjectReference: v1alpha2.UntypedObjectReference{Name: "gateway", Namespace: "shared-gateway"}},
			}}},
		},
	}
	mainSelector := metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "model", "app.kubernetes.io/component": "workload"}}
	prefillSelector := metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "model", "llm-d.ai/role": "prefill"}}
	schedulerSelector := metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "model", "app.kubernetes.io/component": "router-scheduler"}}
	inventory := networkPolicySelectorInventory{
		{Component: networkPolicyComponentMain, Class: networkPolicyRoleWorkload, Selector: mainSelector},
		{Component: networkPolicyComponentPrefill, Class: networkPolicyRoleWorkload, Selector: prefillSelector},
	}
	schedulerPeers := []networkPolicySchedulerPeer{{
		peer:  netv1.NetworkPolicyPeer{PodSelector: schedulerSelector.DeepCopy()},
		ports: []int32{defaultWorkloadPort, 8007},
	}}

	policies := expectedStrictIngressNetworkPolicies(service, inventory,
		gatewayPeerNamespaces(service, "managed-gateway"), []int32{defaultWorkloadPort, 8007}, schedulerPeers, nil)
	if len(policies) != 2 {
		t.Fatalf("got %d policies, want one per workload inventory entry", len(policies))
	}
	policy := policies[0]
	if got, want := policy.Name, ingressNetworkPolicyName(service, networkPolicyModeStrict, networkPolicyComponentMain); got != want {
		t.Fatalf("policy name got %q, want %q", got, want)
	}
	if !reflect.DeepEqual(policy.Spec.PodSelector, mainSelector) {
		t.Fatalf("policy selector got %#v, want the stored main Deployment selector", policy.Spec.PodSelector)
	}
	if got := gatewayPeerNamespaces(service, "managed-gateway"); !reflect.DeepEqual(got, []string{"team-a", "shared-gateway"}) {
		t.Fatalf("gateway namespaces got %v", got)
	}
	if len(policy.Spec.Ingress) != 4 {
		t.Fatalf("expected gateway, scheduler, monitoring, and workload-peer ingress rules, got %#v", policy.Spec.Ingress)
	}
	gateway := policy.Spec.Ingress[0]
	if len(gateway.Ports) != 2 || gateway.Ports[0].Port.IntValue() != int(defaultWorkloadPort) || gateway.Ports[1].Port.IntValue() != 8007 {
		t.Fatalf("gateway ports got %#v", gateway.Ports)
	}
	scheduler := policy.Spec.Ingress[1]
	if len(scheduler.From) != 1 || !reflect.DeepEqual(scheduler.From[0].PodSelector, &schedulerSelector) {
		t.Fatalf("scheduler peer source got %#v, want the stored scheduler Deployment selector", scheduler.From)
	}
	if got := scheduler.Ports; len(got) != 2 || got[0].Port.IntValue() != int(defaultWorkloadPort) || got[1].Port.IntValue() != 8007 {
		t.Fatalf("scheduler target ports got %#v, want 8000 and 8007", got)
	}
	peers := policy.Spec.Ingress[len(policy.Spec.Ingress)-1]
	if got := peers.Ports; len(got) != 3 || got[0].EndPort == nil || *got[0].EndPort != maxNetworkPolicyPort {
		t.Fatalf("workload peer ports got %#v, want explicit all-protocol ranges", got)
	}
	if len(peers.From) != 2 || !reflect.DeepEqual(peers.From[0].PodSelector, &mainSelector) || !reflect.DeepEqual(peers.From[1].PodSelector, &prefillSelector) {
		t.Fatalf("workload peer sources got %#v, want the stored workload selectors", peers.From)
	}
	for _, rule := range policy.Spec.Ingress {
		if len(rule.From) == 0 {
			t.Fatalf("ingress rule %#v would admit every source", rule)
		}
	}
}

func TestExpectedStrictWorkloadNetworkPolicyAllowsStoredWorkloadPeersWithoutServingMarker(t *testing.T) {
	service := &v1alpha2.LLMInferenceService{
		ObjectMeta: metav1.ObjectMeta{Name: "prefill-decode", Namespace: "team-a"},
		Spec:       v1alpha2.LLMInferenceServiceSpec{Prefill: &v1alpha2.WorkloadSpec{}},
	}
	// The multi-node entry selects leaders and compute-only workers through the
	// LWS membership label alone; peer admission must not depend on an
	// InferencePool serving marker or an llm-d role label.
	workerSelector := metav1.LabelSelector{MatchLabels: map[string]string{lwsapi.SetNameLabelKey: "prefill-decode-kserve-mn"}}
	mainSelector := metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "prefill-decode"}}
	inventory := networkPolicySelectorInventory{
		{Component: networkPolicyComponentMain, Class: networkPolicyRoleWorkload, Selector: mainSelector},
		{Component: networkPolicyComponentMainMN, Class: networkPolicyRoleWorkload, Selector: workerSelector},
	}

	policies := expectedStrictIngressNetworkPolicies(service, inventory, nil, nil, nil, nil)
	if len(policies) != 2 {
		t.Fatalf("got %d policies, want one per workload inventory entry", len(policies))
	}
	for _, policy := range policies {
		peer := policy.Spec.Ingress[len(policy.Spec.Ingress)-1]
		if len(peer.Ports) != 3 {
			t.Fatalf("same-service peer ports got %#v, want all protocols", peer.Ports)
		}
		if len(peer.From) != 2 {
			t.Fatalf("same-service peer sources got %#v, want both stored workload selectors", peer.From)
		}
		for _, source := range peer.From {
			if source.PodSelector == nil {
				t.Fatalf("peer source has no pod selector: %#v", source)
			}
		}
	}
}

func TestExpectedStrictSchedulerNetworkPolicyUsesTemplatePortsAndWorkloadPeers(t *testing.T) {
	service := &v1alpha2.LLMInferenceService{
		ObjectMeta: metav1.ObjectMeta{Name: "model", Namespace: "team-a"},
		Spec: v1alpha2.LLMInferenceServiceSpec{Router: &v1alpha2.RouterSpec{
			Gateway: &v1alpha2.GatewaySpec{},
			Scheduler: &v1alpha2.SchedulerSpec{Template: &corev1.PodSpec{Containers: []corev1.Container{{Ports: []corev1.ContainerPort{
				{Name: "grpc", ContainerPort: 9102, Protocol: corev1.ProtocolTCP},
				{Name: "metrics", ContainerPort: 9190, Protocol: corev1.ProtocolTCP},
				{Name: "zmq", ContainerPort: 5657, Protocol: corev1.ProtocolTCP},
			}}}}},
		}},
	}
	workloadSelector := metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "model"}}
	schedulerSelector := metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "model", "app.kubernetes.io/component": "router-scheduler"}}
	inventory := networkPolicySelectorInventory{
		{Component: networkPolicyComponentMain, Class: networkPolicyRoleWorkload, Selector: workloadSelector},
		{Component: networkPolicyComponentScheduler, Class: networkPolicyRoleScheduler, Selector: schedulerSelector},
	}

	policies := expectedStrictIngressNetworkPolicies(service, inventory, gatewayPeerNamespaces(service, "managed-gateway"), nil, nil, nil)
	if len(policies) != 2 {
		t.Fatalf("got %d policies, want one per inventory entry", len(policies))
	}
	policy := policies[1]
	if got, want := policy.Name, ingressNetworkPolicyName(service, networkPolicyModeStrict, networkPolicyComponentScheduler); got != want {
		t.Fatalf("policy name got %q, want %q", got, want)
	}
	if !reflect.DeepEqual(policy.Spec.PodSelector, schedulerSelector) {
		t.Fatalf("policy selector got %#v, want the stored scheduler Deployment selector", policy.Spec.PodSelector)
	}
	if len(policy.Spec.Ingress) < 3 {
		t.Fatalf("expected gateway, monitoring, and workload ingress rules, got %d", len(policy.Spec.Ingress))
	}
	if got := policy.Spec.Ingress[0].Ports[0].Port.IntValue(); got != 9102 {
		t.Fatalf("gateway port got %d, want 9102", got)
	}
	if got := policy.Spec.Ingress[1].Ports[0].Port.IntValue(); got != 9190 {
		t.Fatalf("metrics port got %d, want 9190", got)
	}
	zmq := policy.Spec.Ingress[2]
	if got := zmq.Ports[0].Port.IntValue(); got != 5657 {
		t.Fatalf("ZMQ port got %d, want 5657", got)
	}
	if len(zmq.From) != 1 || !reflect.DeepEqual(zmq.From[0].PodSelector, &workloadSelector) {
		t.Fatalf("ZMQ peer source got %#v, want the stored workload selector", zmq.From)
	}
}

func TestExpectedStrictTokenizerNetworkPolicyAllowsOnlyStoredScheduler(t *testing.T) {
	service := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{Name: "model", Namespace: "team-a"}}
	tokenizerSelector := metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "model", "app.kubernetes.io/component": "tokenizer"}}
	schedulerSelector := metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "model", "app.kubernetes.io/component": "router-scheduler"}}

	policies := expectedStrictIngressNetworkPolicies(service, networkPolicySelectorInventory{
		{Component: networkPolicyComponentScheduler, Class: networkPolicyRoleScheduler, Selector: schedulerSelector},
		{Component: networkPolicyComponentTokenizer, Class: networkPolicyRoleTokenizer, Selector: tokenizerSelector},
	}, nil, nil, nil, nil)
	if len(policies) != 2 {
		t.Fatalf("got %d policies, want one per inventory entry", len(policies))
	}
	policy := policies[1]
	if got, want := policy.Name, ingressNetworkPolicyName(service, networkPolicyModeStrict, networkPolicyComponentTokenizer); got != want {
		t.Fatalf("policy name got %q, want %q", got, want)
	}
	if len(policy.Spec.Ingress) != 1 {
		t.Fatalf("expected one scheduler ingress rule, got %#v", policy.Spec.Ingress)
	}
	rule := policy.Spec.Ingress[0]
	if len(rule.From) != 1 || !reflect.DeepEqual(rule.From[0].PodSelector, &schedulerSelector) {
		t.Fatalf("tokenizer source got %#v, want the stored scheduler Deployment selector", rule.From)
	}
	if got := rule.Ports[0].Port.IntValue(); got != int(tokenizerServicePort) {
		t.Fatalf("tokenizer port got %d, want %d", got, tokenizerServicePort)
	}

	// Without a stored scheduler the tokenizer policy denies all ingress
	// instead of admitting an empty source.
	policies = expectedStrictIngressNetworkPolicies(service, networkPolicySelectorInventory{
		{Component: networkPolicyComponentTokenizer, Class: networkPolicyRoleTokenizer, Selector: tokenizerSelector},
	}, nil, nil, nil, nil)
	if len(policies) != 1 || len(policies[0].Spec.Ingress) != 0 {
		t.Fatalf("tokenizer policy without a stored scheduler must deny all ingress: %#v", policies)
	}
}

func TestExpectedDefaultIngressNetworkPoliciesAreIngressOnlyPerComponent(t *testing.T) {
	service := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{Name: "model", Namespace: "team-a"}}
	mainSelector := metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "model"}}
	schedulerSelector := metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "model", "app.kubernetes.io/component": "router-scheduler"}}

	policies := expectedDefaultIngressNetworkPolicies(service, networkPolicySelectorInventory{
		{Component: networkPolicyComponentMain, Class: networkPolicyRoleWorkload, Selector: mainSelector},
		{Component: networkPolicyComponentScheduler, Class: networkPolicyRoleScheduler, Selector: schedulerSelector},
	})
	if len(policies) != 2 {
		t.Fatalf("got %d policies, want one per inventory entry", len(policies))
	}
	for i, selector := range []metav1.LabelSelector{mainSelector, schedulerSelector} {
		wantName := ingressNetworkPolicyName(service, networkPolicyModeDefault, networkPolicyComponentMain)
		if i == 1 {
			wantName = ingressNetworkPolicyName(service, networkPolicyModeDefault, networkPolicyComponentScheduler)
		}
		policy := policies[i]
		if policy.Name != wantName {
			t.Fatalf("policy name got %q, want %q", policy.Name, wantName)
		}
		if !reflect.DeepEqual(policy.Spec.PodSelector, selector) {
			t.Fatalf("policy %s selector got %#v, want the stored selector", policy.Name, policy.Spec.PodSelector)
		}
		if !reflect.DeepEqual(policy.Spec.PolicyTypes, []netv1.PolicyType{netv1.PolicyTypeIngress}) {
			t.Fatalf("policy %s types got %v", policy.Name, policy.Spec.PolicyTypes)
		}
		if len(policy.Spec.Ingress) != 1 || len(policy.Spec.Ingress[0].From) != 0 || len(policy.Spec.Ingress[0].Ports) != 0 {
			t.Fatalf("policy %s ingress is not explicit allow-all: %#v", policy.Name, policy.Spec.Ingress)
		}
		if policy.Spec.Egress != nil {
			t.Fatalf("policy %s must not create egress rules: %#v", policy.Name, policy.Spec.Egress)
		}
	}
}

// legacyAllowAllNetworkPolicy and legacyGeneralEgressNetworkPolicy are
// test-only fixtures for the broad egress NetworkPolicies that earlier builds
// of this controller created: the combined allow-all policy and the general
// egress policy. Production code no longer builds or reconciles them; the
// migration and cleanup tests seed these shapes to verify that reconcile
// removes the historical objects with ownership checks. The selector mimics
// the removed identity-label scheme those builds selected Pods with.
func legacyAllowAllNetworkPolicy(service *v1alpha2.LLMInferenceService) *netv1.NetworkPolicy {
	return &netv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:            allowAllNetworkPolicyName(service),
			Namespace:       service.GetNamespace(),
			OwnerReferences: ownedByController(service),
		},
		Spec: netv1.NetworkPolicySpec{
			PodSelector: legacyNetworkPolicyIdentitySelector(),
			PolicyTypes: []netv1.PolicyType{netv1.PolicyTypeIngress, netv1.PolicyTypeEgress},
			Ingress:     []netv1.NetworkPolicyIngressRule{{}},
			Egress:      []netv1.NetworkPolicyEgressRule{{}},
		},
	}
}

func legacyGeneralEgressNetworkPolicy(service *v1alpha2.LLMInferenceService) *netv1.NetworkPolicy {
	return &netv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:            egressNetworkPolicyName(service),
			Namespace:       service.GetNamespace(),
			OwnerReferences: ownedByController(service),
		},
		Spec: netv1.NetworkPolicySpec{
			PodSelector: legacyNetworkPolicyIdentitySelector(),
			PolicyTypes: []netv1.PolicyType{netv1.PolicyTypeEgress},
			Egress:      []netv1.NetworkPolicyEgressRule{{}},
		},
	}
}

func legacyNetworkPolicyIdentitySelector() metav1.LabelSelector {
	return metav1.LabelSelector{MatchLabels: map[string]string{
		"serving.kserve.io/llmisvc-network-policy-id": "legacy-test-fixture",
	}}
}

func newNetworkPolicyTestClient(t *testing.T, objects ...client.Object) client.WithWatch {
	t.Helper()
	return clientfake.NewClientBuilder().
		WithScheme(networkPolicyTestScheme(t)).
		WithRESTMapper(networkPolicyTestRESTMapper()).
		WithObjects(objects...).
		Build()
}

func newNetworkPolicyTestClientWithInterceptor(t *testing.T, funcs interceptor.Funcs, objects ...client.Object) client.WithWatch {
	t.Helper()
	return clientfake.NewClientBuilder().
		WithScheme(networkPolicyTestScheme(t)).
		WithRESTMapper(networkPolicyTestRESTMapper()).
		WithObjects(objects...).
		WithInterceptorFuncs(funcs).
		Build()
}

func networkPolicyTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := netv1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add NetworkPolicy scheme: %v", err)
	}
	if err := v1alpha2.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add LLMInferenceService scheme: %v", err)
	}
	// The selector inventory reads Deployments and the optional LWS and
	// DisaggregatedSet APIs; without their types registered the fake client
	// fails the reads instead of reporting NotFound.
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add apps scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add core scheme: %v", err)
	}
	if err := lwsapi.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add LeaderWorkerSet scheme: %v", err)
	}
	if err := disaggregatedsetv1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add DisaggregatedSet scheme: %v", err)
	}
	if err := gwapiv1.Install(scheme); err != nil {
		t.Fatalf("failed to add Gateway API scheme: %v", err)
	}
	return scheme
}

func networkPolicyTestRESTMapper() meta.RESTMapper {
	mapper := meta.NewDefaultRESTMapper(nil)
	mapper.Add(netv1.SchemeGroupVersion.WithKind("NetworkPolicy"), meta.RESTScopeNamespace)
	return mapper
}

func TestReconcileLLMISVCNetworkPoliciesDefaultModeCreatesPerComponentIngressAndRemovesStrict(t *testing.T) {
	service := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{
		Name:      "model",
		Namespace: "team-a",
		UID:       "model-uid",
	}}
	mainSelector := metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "model"}}
	ownedPolicy := func(name string) *netv1.NetworkPolicy {
		return &netv1.NetworkPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: service.Namespace, OwnerReferences: ownedByController(service)},
			Spec:       netv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"seed": "strict"}}},
		}
	}
	// Seed the stored main Deployment the inventory reads, plus strict-mode
	// policies from both the current per-component names and the pre-selector
	// names (and the legacy policy) to simulate migrating to default mode.
	seeded := []client.Object{
		ownedDeployment(service, mainDeploymentName(service), mainSelector.DeepCopy(), mainSelector.MatchLabels),
		ownedPolicy(ingressNetworkPolicyName(service, networkPolicyModeStrict, networkPolicyComponentMain)),
		ownedPolicy(ingressNetworkPolicyName(service, networkPolicyModeStrict, networkPolicyComponentScheduler)),
		ownedPolicy(workloadNetworkPolicyName(service)),
		legacyGeneralEgressNetworkPolicy(service),
		legacyAllowAllNetworkPolicy(service),
		&netv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{
			Name:      legacyMonitoringNetworkPolicyName(service),
			Namespace: service.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(service, v1alpha2.LLMInferenceServiceGVK),
			},
		}},
	}
	cl := newNetworkPolicyTestClient(t, seeded...)
	r := &LLMISVCReconciler{Client: cl, EventRecorder: record.NewFakeRecorder(16)}

	if err := reconcileLLMISVCNetworkPolicies(t.Context(), r, service, &Config{}); err != nil {
		t.Fatalf("default-mode reconcile failed: %v", err)
	}

	defaultMain := &netv1.NetworkPolicy{}
	if err := cl.Get(t.Context(), types.NamespacedName{
		Name:      ingressNetworkPolicyName(service, networkPolicyModeDefault, networkPolicyComponentMain),
		Namespace: service.Namespace,
	}, defaultMain); err != nil {
		t.Fatalf("per-component default NetworkPolicy was not created: %v", err)
	}
	if !metav1.IsControlledBy(defaultMain, service) {
		t.Fatalf("per-component default NetworkPolicy is not controlled by the service")
	}
	if !reflect.DeepEqual(defaultMain.Spec.PodSelector, mainSelector) {
		t.Fatalf("per-component default selector got %#v, want the stored Deployment selector", defaultMain.Spec.PodSelector)
	}
	if !reflect.DeepEqual(defaultMain.Spec.PolicyTypes, []netv1.PolicyType{netv1.PolicyTypeIngress}) {
		t.Fatalf("per-component default types got %v", defaultMain.Spec.PolicyTypes)
	}
	if len(defaultMain.Spec.Ingress) != 1 || len(defaultMain.Spec.Ingress[0].From) != 0 || len(defaultMain.Spec.Ingress[0].Ports) != 0 {
		t.Fatalf("per-component default ingress is not explicit allow-all: %#v", defaultMain.Spec.Ingress)
	}
	if defaultMain.Spec.Egress != nil {
		t.Fatalf("per-component default policy must not create egress rules: %#v", defaultMain.Spec.Egress)
	}

	for _, name := range append([]string{
		workloadNetworkPolicyName(service),
		egressNetworkPolicyName(service),
		allowAllNetworkPolicyName(service),
		legacyMonitoringNetworkPolicyName(service),
	}, ingressNetworkPolicyNames(service, networkPolicyModeStrict)...) {
		err := cl.Get(t.Context(), types.NamespacedName{Name: name, Namespace: service.Namespace}, &netv1.NetworkPolicy{})
		if !apierrors.IsNotFound(err) {
			t.Fatalf("policy %s remained after default-mode reconcile: %v", name, err)
		}
	}
}

func TestReconcileLLMISVCNetworkPoliciesStrictModeReplacesDefaultAndRemovesBroadEgress(t *testing.T) {
	service := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{
		Name:      "model",
		Namespace: "team-a",
		UID:       "model-uid",
	}}
	mainSelector := metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "model"}}
	defaultMain := &netv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:            ingressNetworkPolicyName(service, networkPolicyModeDefault, networkPolicyComponentMain),
			Namespace:       service.Namespace,
			OwnerReferences: ownedByController(service),
		},
		Spec: netv1.NetworkPolicySpec{PodSelector: mainSelector},
	}
	cl := newNetworkPolicyTestClient(t,
		ownedDeployment(service, mainDeploymentName(service), mainSelector.DeepCopy(), mainSelector.MatchLabels),
		legacyAllowAllNetworkPolicy(service),
		legacyGeneralEgressNetworkPolicy(service),
		defaultMain)
	r := &LLMISVCReconciler{Client: cl, EventRecorder: record.NewFakeRecorder(16)}

	config := &Config{FeatureGates: FeatureGates{StrictNetworkPolicy: true}}
	if err := reconcileLLMISVCNetworkPolicies(t.Context(), r, service, config); err != nil {
		t.Fatalf("strict-mode reconcile failed: %v", err)
	}

	strictMain := &netv1.NetworkPolicy{}
	if err := cl.Get(t.Context(), types.NamespacedName{
		Name:      ingressNetworkPolicyName(service, networkPolicyModeStrict, networkPolicyComponentMain),
		Namespace: service.Namespace,
	}, strictMain); err != nil {
		t.Fatalf("strict policy was not created: %v", err)
	}
	if !reflect.DeepEqual(strictMain.Spec.PodSelector, mainSelector) {
		t.Fatalf("strict policy selector got %#v, want the stored Deployment selector", strictMain.Spec.PodSelector)
	}
	if !metav1.IsControlledBy(strictMain, service) {
		t.Fatalf("strict policy is not controlled by the service")
	}
	// Egress is mode-independent: strict mode keeps the per-component
	// allow-all egress policy alongside its ingress-only policies.
	egressMain := &netv1.NetworkPolicy{}
	if err := cl.Get(t.Context(), types.NamespacedName{
		Name:      allowAllEgressNetworkPolicyName(service, networkPolicyComponentMain),
		Namespace: service.Namespace,
	}, egressMain); err != nil {
		t.Fatalf("per-component allow-all egress policy was not created in strict mode: %v", err)
	}
	if !reflect.DeepEqual(egressMain.Spec.PodSelector, mainSelector) {
		t.Fatalf("strict-mode egress policy selector got %#v, want the stored Deployment selector", egressMain.Spec.PodSelector)
	}
	if !reflect.DeepEqual(egressMain.Spec.PolicyTypes, []netv1.PolicyType{netv1.PolicyTypeEgress}) {
		t.Fatalf("strict-mode egress policy types got %v", egressMain.Spec.PolicyTypes)
	}
	if len(egressMain.Spec.Egress) != 1 || len(egressMain.Spec.Egress[0].To) != 0 || len(egressMain.Spec.Egress[0].Ports) != 0 {
		t.Fatalf("strict-mode egress policy is not explicit allow-all: %#v", egressMain.Spec.Egress)
	}
	// The retired legacy broad egress names are removed once the replacement
	// coverage exists.
	for _, name := range append([]string{
		egressNetworkPolicyName(service),
		allowAllNetworkPolicyName(service),
	}, ingressNetworkPolicyNames(service, networkPolicyModeDefault)...) {
		err := cl.Get(t.Context(), types.NamespacedName{Name: name, Namespace: service.Namespace}, &netv1.NetworkPolicy{})
		if !apierrors.IsNotFound(err) {
			t.Fatalf("policy %s remained after strict-mode reconcile: %v", name, err)
		}
	}
}

func TestGatewayNamespacesForNetworkPolicyErrorsOnUnresolvedManagedScheduler(t *testing.T) {
	service := &v1alpha2.LLMInferenceService{
		ObjectMeta: metav1.ObjectMeta{Name: "model", Namespace: "team-a", UID: "model-uid"},
		Spec: v1alpha2.LLMInferenceServiceSpec{Router: &v1alpha2.RouterSpec{
			Scheduler: &v1alpha2.SchedulerSpec{Template: &corev1.PodSpec{}},
		}},
	}
	r := &LLMISVCReconciler{Client: newNetworkPolicyTestClient(t)}

	namespaces, _, _, err := r.gatewayNamespacesForNetworkPolicy(t.Context(), service, "managed-gateway", nil)
	if err == nil {
		t.Fatal("an unresolved managed scheduler must fail the reconcile so it retries")
	}
	if len(namespaces) != 0 {
		t.Fatalf("unresolved managed scheduler returned partial Gateway namespaces: %v", namespaces)
	}
	if !strings.Contains(err.Error(), schedulerDeploymentName(service)) {
		t.Fatalf("unresolved managed scheduler error = %v, want it to name %s", err, schedulerDeploymentName(service))
	}
	if errors.Is(err, errNetworkPolicyOwnershipConflict) {
		t.Fatalf("unresolved managed scheduler error = %v, want a retryable error not an ownership conflict", err)
	}
}

// TestGatewayNamespacesForNetworkPolicyResolvesReferencedHTTPRouteParents
// covers the strict-traffic Gateway discovery for services that reference an
// HTTPRoute instead of naming Gateway refs: the parent Gateway namespaces of
// the stored route become the policy's Gateway peers — an omitted parentRef
// namespace meaning the route's own namespace — and a referenced route that
// is missing aborts discovery so the reconcile retries instead of guessing.
func TestGatewayNamespacesForNetworkPolicyResolvesReferencedHTTPRouteParents(t *testing.T) {
	service := &v1alpha2.LLMInferenceService{
		ObjectMeta: metav1.ObjectMeta{Name: "model", Namespace: "team-a", UID: "model-uid"},
		Spec: v1alpha2.LLMInferenceServiceSpec{Router: &v1alpha2.RouterSpec{
			Route: &v1alpha2.GatewayRoutesSpec{HTTP: &v1alpha2.HTTPRouteSpec{
				Refs: []corev1.LocalObjectReference{{Name: "shared-route"}},
			}},
		}},
	}
	route := &gwapiv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "shared-route", Namespace: "team-a"},
		Spec: gwapiv1.HTTPRouteSpec{CommonRouteSpec: gwapiv1.CommonRouteSpec{
			ParentRefs: []gwapiv1.ParentReference{
				// An explicit cross-namespace Gateway parent, plus a parent whose
				// omitted kind defaults to Gateway and whose omitted namespace
				// means the route's own namespace.
				{Kind: ptr.To(gwapiv1.Kind("Gateway")), Name: "edge-gateway", Namespace: ptr.To(gwapiv1.Namespace("gateway-ns"))},
				{Name: "local-gateway"},
			},
		}},
	}
	cl := newNetworkPolicyTestClient(t, route)
	r := &LLMISVCReconciler{Client: cl}

	namespaces, _, _, err := r.gatewayNamespacesForNetworkPolicy(t.Context(), service, "managed-gateway", nil)
	if err != nil {
		t.Fatalf("gateway peer discovery failed: %v", err)
	}
	if !reflect.DeepEqual(namespaces, []string{"gateway-ns", "team-a"}) {
		t.Fatalf("gateway namespaces got %v, want the referenced route's parent namespaces", namespaces)
	}

	// A referenced route that is not stored is a retryable failure, not a
	// silently narrowed peer set.
	missingRouteService := service.DeepCopy()
	missingRouteService.Spec.Router.Route.HTTP.Refs = []corev1.LocalObjectReference{{Name: "missing-route"}}
	namespaces, _, _, err = r.gatewayNamespacesForNetworkPolicy(t.Context(), missingRouteService, "managed-gateway", nil)
	if err == nil || !strings.Contains(err.Error(), "missing-route") {
		t.Fatalf("missing referenced route error = %v, want it to name the route", err)
	}
	if len(namespaces) != 0 {
		t.Fatalf("missing referenced route returned partial Gateway namespaces: %v", namespaces)
	}
}

func TestDefaultModeTracingTransitionsPreserveIngress(t *testing.T) {
	for _, startStrict := range []bool{false, true} {
		t.Run(fmt.Sprintf("initial-strict-%t", startStrict), func(t *testing.T) {
			service := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{
				Name: "model", Namespace: "team-a", UID: "model-uid",
			}}
			// Model a namespace with default-deny ingress. The controller must
			// retain an explicit ingress grant, independently of tracing egress.
			defaultDeny := &netv1.NetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "default-deny", Namespace: service.Namespace},
				Spec: netv1.NetworkPolicySpec{
					PodSelector: metav1.LabelSelector{},
					PolicyTypes: []netv1.PolicyType{netv1.PolicyTypeIngress},
				},
			}
			mainSelector := metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "model"}}
			cl := newNetworkPolicyTestClient(t, defaultDeny,
				ownedDeployment(service, mainDeploymentName(service), mainSelector.DeepCopy(), mainSelector.MatchLabels))
			r := &LLMISVCReconciler{Client: cl, EventRecorder: record.NewFakeRecorder(100)}
			config := &Config{FeatureGates: FeatureGates{StrictNetworkPolicy: startStrict}}
			if err := reconcileLLMISVCNetworkPolicies(t.Context(), r, service, config); err != nil {
				t.Fatal(err)
			}

			// Exercise tracing off→on→off→on, including repeated reconciles.
			// If initially strict, first opt in there, then switch to default
			// mode with tracing still enabled.
			for _, tracing := range []bool{true, false, true} {
				service.Annotations = map[string]string{constants.EnableTracingEgressNetworkPolicyAnnotationKey: strconv.FormatBool(tracing)}
				if tracing {
					service.Spec.Tracing = &v1alpha2.TracingSpec{}
				} else {
					service.Spec.Tracing = nil
				}
				if err := reconcileLLMISVCNetworkPolicies(t.Context(), r, service, config); err != nil {
					t.Fatal(err)
				}
				config.FeatureGates.StrictNetworkPolicy = false
				for range 2 {
					if err := reconcileLLMISVCNetworkPolicies(t.Context(), r, service, config); err != nil {
						t.Fatal(err)
					}
					// Ingress coverage survives every transition through the
					// per-component default policy, never through a broad grant.
					defaultMain := &netv1.NetworkPolicy{}
					if err := cl.Get(t.Context(), types.NamespacedName{
						Name:      ingressNetworkPolicyName(service, networkPolicyModeDefault, networkPolicyComponentMain),
						Namespace: service.Namespace,
					}, defaultMain); err != nil {
						t.Fatalf("tracing=%t: missing ingress grant: %v", tracing, err)
					}
					if !reflect.DeepEqual(defaultMain.Spec.PodSelector, mainSelector) {
						t.Fatalf("tracing=%t: default policy selector got %#v, want the stored Deployment selector", tracing, defaultMain.Spec.PodSelector)
					}
					if !reflect.DeepEqual(defaultMain.Spec.PolicyTypes, []netv1.PolicyType{netv1.PolicyTypeIngress}) {
						t.Fatalf("tracing=%t: default policy types got %v", tracing, defaultMain.Spec.PolicyTypes)
					}

					policies := &netv1.NetworkPolicyList{}
					if err := cl.List(t.Context(), policies, client.InNamespace(service.Namespace)); err != nil {
						t.Fatal(err)
					}
					// The namespace default-deny, the per-component default ingress
					// grant, and exactly one egress policy: the unrestricted
					// per-component allow-all grant while opted out, the restricted
					// tracing policy while opted in.
					wantCount := 3
					egressMainName := allowAllEgressNetworkPolicyName(service, networkPolicyComponentMain)
					if tracing {
						if err := cl.Get(t.Context(), client.ObjectKey{Name: tracingNetworkPolicyName(service), Namespace: service.Namespace}, &netv1.NetworkPolicy{}); err != nil {
							t.Fatal(err)
						}
						if err := cl.Get(t.Context(), client.ObjectKey{Name: egressMainName, Namespace: service.Namespace}, &netv1.NetworkPolicy{}); !apierrors.IsNotFound(err) {
							t.Fatalf("tracing=%t: the unrestricted egress grant must not remain while tracing is opted in: %v", tracing, err)
						}
					} else {
						egressMain := &netv1.NetworkPolicy{}
						if err := cl.Get(t.Context(), client.ObjectKey{Name: egressMainName, Namespace: service.Namespace}, egressMain); err != nil {
							t.Fatalf("tracing=%t: missing unrestricted egress grant: %v", tracing, err)
						}
						if !reflect.DeepEqual(egressMain.Spec.PodSelector, mainSelector) {
							t.Fatalf("tracing=%t: egress policy selector got %#v, want the stored Deployment selector", tracing, egressMain.Spec.PodSelector)
						}
						if !reflect.DeepEqual(egressMain.Spec.PolicyTypes, []netv1.PolicyType{netv1.PolicyTypeEgress}) {
							t.Fatalf("tracing=%t: egress policy types got %v", tracing, egressMain.Spec.PolicyTypes)
						}
						if len(egressMain.Spec.Egress) != 1 || len(egressMain.Spec.Egress[0].To) != 0 || len(egressMain.Spec.Egress[0].Ports) != 0 {
							t.Fatalf("tracing=%t: egress policy is not explicit allow-all: %#v", tracing, egressMain.Spec.Egress)
						}
					}
					if len(policies.Items) != wantCount {
						t.Fatalf("tracing=%t: got %d policies, want %d", tracing, len(policies.Items), wantCount)
					}
					for _, policy := range policies.Items {
						if tracing {
							for _, rule := range policy.Spec.Egress {
								if len(rule.To) == 0 && len(rule.Ports) == 0 {
									t.Fatalf("%s bypasses tracing with unrestricted egress", policy.Name)
								}
							}
						}
					}
				}
			}
		})
	}
}

func TestReconcileLLMISVCNetworkPoliciesKillSwitchSuppressesAllModes(t *testing.T) {
	service := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{
		Name:      "model",
		Namespace: "team-a",
		UID:       "model-uid",
	}}
	strict := &netv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:            workloadNetworkPolicyName(service),
			Namespace:       service.Namespace,
			OwnerReferences: ownedByController(service),
		},
		Spec: netv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"seed": "strict"}}},
	}
	cl := newNetworkPolicyTestClient(t, strict)
	r := &LLMISVCReconciler{Client: cl, EventRecorder: record.NewFakeRecorder(16)}

	networkPolicyDisabled = true
	defer func() { networkPolicyDisabled = false }()

	// Default mode: no allow-all policy is created.
	if err := reconcileLLMISVCNetworkPolicies(t.Context(), r, service, &Config{}); err != nil {
		t.Fatalf("kill-switch reconcile failed: %v", err)
	}
	err := cl.Get(t.Context(), types.NamespacedName{Name: allowAllNetworkPolicyName(service), Namespace: service.Namespace}, &netv1.NetworkPolicy{})
	if !apierrors.IsNotFound(err) {
		t.Fatalf("allow-all NetworkPolicy created while kill switch is set: %v", err)
	}
	// Existing semantics: the kill switch only stops management; policies from
	// before it was set are left in place.
	if err := cl.Get(t.Context(), types.NamespacedName{Name: workloadNetworkPolicyName(service), Namespace: service.Namespace}, &netv1.NetworkPolicy{}); err != nil {
		t.Fatalf("existing policy should be left untouched while kill switch is set: %v", err)
	}

	// Strict mode: nothing is reconciled either.
	if err := reconcileLLMISVCNetworkPolicies(t.Context(), r, service, &Config{FeatureGates: FeatureGates{StrictNetworkPolicy: true}}); err != nil {
		t.Fatalf("kill-switch reconcile failed: %v", err)
	}
	err = cl.Get(t.Context(), types.NamespacedName{Name: tokenizerNetworkPolicyName(service), Namespace: service.Namespace}, &netv1.NetworkPolicy{})
	if !apierrors.IsNotFound(err) {
		t.Fatalf("strict policy created while kill switch is set: %v", err)
	}
}

func TestCleanupNetworkPoliciesDeletesOwnedPoliciesDuringServiceDeletion(t *testing.T) {
	now := metav1.Now()
	service := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{
		Name:              "terminating-service",
		Namespace:         "team-a",
		UID:               "terminating-service-uid",
		DeletionTimestamp: &now,
	}}
	policies := []*netv1.NetworkPolicy{
		legacyGeneralEgressNetworkPolicy(service),
		legacyAllowAllNetworkPolicy(service),
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:            ingressNetworkPolicyName(service, networkPolicyModeStrict, networkPolicyComponentMain),
				Namespace:       service.Namespace,
				OwnerReferences: ownedByController(service),
			},
			Spec: netv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"seed": "strict"}}},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:            ingressNetworkPolicyName(service, networkPolicyModeDefault, networkPolicyComponentMainMN),
				Namespace:       service.Namespace,
				OwnerReferences: ownedByController(service),
			},
			Spec: netv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"seed": "default"}}},
		},
	}

	scheme := runtime.NewScheme()
	if err := netv1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add NetworkPolicy scheme: %v", err)
	}
	objects := make([]client.Object, 0, len(policies))
	for _, policy := range policies {
		objects = append(objects, policy)
	}
	client := clientfake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	r := &LLMISVCReconciler{Client: client}
	if err := cleanupLLMISVCNetworkPolicies(t.Context(), r, service); err != nil {
		t.Fatalf("cleanup failed: %v", err)
	}

	for _, policy := range policies {
		remaining := &netv1.NetworkPolicy{}
		err := client.Get(t.Context(), types.NamespacedName{Name: policy.Name, Namespace: policy.Namespace}, remaining)
		if !apierrors.IsNotFound(err) {
			t.Fatalf("owned policy %s remained after cleanup: %v", policy.Name, err)
		}
	}
}

// TestCleanupNetworkPoliciesDeletesEveryBoundedEgressNameDuringServiceDeletion
// pins the finalizer's bounded egress coverage: service deletion removes
// every fixed per-component allow-all egress policy name — the active name of
// a still-stored component together with the stale names of components that
// already left the topology — plus the policies of the egress states and
// legacy schemes the service has moved on from, independent of its current
// mode or state. Cleanup stays bounded to the fixed name lists, so a policy
// the controller never manages survives untouched, and a repeated pass over
// the already-deleted names is success.
func TestCleanupNetworkPoliciesDeletesEveryBoundedEgressNameDuringServiceDeletion(t *testing.T) {
	now := metav1.Now()
	service := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{
		Name:              "terminating-service",
		Namespace:         "team-a",
		UID:               "terminating-service-uid",
		DeletionTimestamp: &now,
	}}
	// The service's last reconcile stored only the main workload, so the main
	// egress name is active coverage and the other six are stale names of
	// components that left the topology.
	mainSelector := metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "terminating-service"}}
	ownedEgress := func(component string) *netv1.NetworkPolicy {
		return &netv1.NetworkPolicy{
			ObjectMeta: metav1.ObjectMeta{
				Name:            allowAllEgressNetworkPolicyName(service, component),
				Namespace:       service.Namespace,
				OwnerReferences: ownedByController(service),
			},
			Spec: netv1.NetworkPolicySpec{PodSelector: mainSelector},
		}
	}
	unrelated := &netv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "terminating-service-unrelated",
			Namespace:       service.Namespace,
			OwnerReferences: ownedByController(service),
		},
		Spec: netv1.NetworkPolicySpec{PodSelector: mainSelector},
	}
	seeded := make([]client.Object, 0, 5+len(networkPolicyComponentKeys))
	seeded = append(seeded,
		ownedDeployment(service, mainDeploymentName(service), mainSelector.DeepCopy(), mainSelector.MatchLabels),
		// Coverage of egress states and legacy schemes the service has since
		// left: deletion must not depend on the current mode or egress state.
		expectedTracingNetworkPolicy(service, netv1.NetworkPolicyPeer{}, 0, false),
		legacyGeneralEgressNetworkPolicy(service),
		legacyAllowAllNetworkPolicy(service),
	)
	for _, component := range networkPolicyComponentKeys {
		seeded = append(seeded, ownedEgress(component))
	}
	seeded = append(seeded, unrelated)
	cl := newNetworkPolicyTestClient(t, seeded...)
	r := &LLMISVCReconciler{Client: cl}

	if err := cleanupLLMISVCNetworkPolicies(t.Context(), r, service); err != nil {
		t.Fatalf("cleanup failed: %v", err)
	}
	for _, name := range append(allowAllEgressNetworkPolicyNames(service),
		tracingNetworkPolicyName(service),
		egressNetworkPolicyName(service),
		allowAllNetworkPolicyName(service),
	) {
		if err := cl.Get(t.Context(), types.NamespacedName{Name: name, Namespace: service.Namespace}, &netv1.NetworkPolicy{}); !apierrors.IsNotFound(err) {
			t.Fatalf("owned policy %s remained after cleanup: %v", name, err)
		}
	}
	// Cleanup is bounded to the fixed name lists: a service-owned policy the
	// controller never manages must not be swept by finalization.
	if err := cl.Get(t.Context(), types.NamespacedName{Name: unrelated.Name, Namespace: unrelated.Namespace}, &netv1.NetworkPolicy{}); err != nil {
		t.Fatalf("unmanaged policy must survive the bounded cleanup: %v", err)
	}
	// A repeated pass over the already-deleted names is success.
	if err := cleanupLLMISVCNetworkPolicies(t.Context(), r, service); err != nil {
		t.Fatalf("repeated cleanup failed: %v", err)
	}
}

func TestDeleteOwnedNetworkPolicyHandlesMissingAndOwnedPolicies(t *testing.T) {
	service := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{
		Name: "model", Namespace: "team-a", UID: "model-uid",
	}}
	expected := legacyGeneralEgressNetworkPolicy(service)

	if err := deleteOwnedNetworkPolicy(t.Context(), &LLMISVCReconciler{Client: newNetworkPolicyTestClient(t)}, service, expected); err != nil {
		t.Fatalf("missing policy should be treated as success: %v", err)
	}

	cl := newNetworkPolicyTestClient(t, expected)
	if err := deleteOwnedNetworkPolicy(t.Context(), &LLMISVCReconciler{Client: cl}, service, expected); err != nil {
		t.Fatalf("owned policy deletion failed: %v", err)
	}
	if err := cl.Get(t.Context(), client.ObjectKeyFromObject(expected), &netv1.NetworkPolicy{}); !apierrors.IsNotFound(err) {
		t.Fatalf("owned policy remained after deletion: %v", err)
	}
}

func TestDeleteOwnedNetworkPolicyRecognizesOwnershipConflictsWithoutMutation(t *testing.T) {
	service := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{
		Name: "model", Namespace: "team-a", UID: "model-uid",
	}}
	foreignService := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{
		Name: "other-model", Namespace: service.Namespace, UID: "other-model-uid",
	}}

	ownerless := legacyAllowAllNetworkPolicy(service).DeepCopy()
	ownerless.OwnerReferences = nil
	foreign := legacyAllowAllNetworkPolicy(service).DeepCopy()
	foreign.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(foreignService, v1alpha2.LLMInferenceServiceGVK)}

	for _, tt := range []struct {
		name   string
		policy *netv1.NetworkPolicy
	}{
		{name: "ownerless", policy: ownerless},
		{name: "foreign controller", policy: foreign},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cl := newNetworkPolicyTestClient(t, tt.policy)
			before := &netv1.NetworkPolicy{}
			if err := cl.Get(t.Context(), client.ObjectKeyFromObject(tt.policy), before); err != nil {
				t.Fatalf("failed to read policy before deletion attempt: %v", err)
			}

			err := deleteOwnedNetworkPolicy(t.Context(), &LLMISVCReconciler{Client: cl}, service, tt.policy)
			if !errors.Is(err, errNetworkPolicyOwnershipConflict) {
				t.Fatalf("delete error = %v, want ownership conflict", err)
			}
			for _, detail := range []string{
				"NetworkPolicy " + service.Namespace + "/" + tt.policy.Name,
				"LLMInferenceService " + service.Namespace + "/" + service.Name,
			} {
				if !strings.Contains(err.Error(), detail) {
					t.Fatalf("ownership conflict %q does not include %q", err, detail)
				}
			}

			after := &netv1.NetworkPolicy{}
			if err := cl.Get(t.Context(), client.ObjectKeyFromObject(tt.policy), after); err != nil {
				t.Fatalf("conflicting policy was deleted: %v", err)
			}
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("conflicting policy was mutated: before=%#v after=%#v", before, after)
			}
		})
	}
}

func TestCleanupNetworkPoliciesContinuesPastFirstMiddleAndLastOwnershipConflicts(t *testing.T) {
	service := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{
		Name: "model", Namespace: "team-a", UID: "model-uid",
	}}
	foreignService := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{
		Name: "other-model", Namespace: service.Namespace, UID: "other-model-uid",
	}}
	foreignLegacy := &netv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:            legacyMonitoringNetworkPolicyName(service),
			Namespace:       service.Namespace,
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(foreignService, v1alpha2.LLMInferenceServiceGVK)},
		},
		Spec: netv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{},
			PolicyTypes: []netv1.PolicyType{netv1.PolicyTypeIngress},
			Ingress:     []netv1.NetworkPolicyIngressRule{{}},
		},
	}
	ownerlessScheduler := &netv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ingressNetworkPolicyName(service, networkPolicyModeStrict, networkPolicyComponentScheduler),
			Namespace: service.Namespace,
		},
		Spec: netv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"seed": "strict"}}},
	}
	// The per-component egress names close the finalizer's bounded name list,
	// so a foreign policy on the pd egress name is a conflict in the last
	// position after the first (legacy monitoring) and middle (strict
	// scheduler) ones.
	foreignEgressPD := &netv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:            allowAllEgressNetworkPolicyName(service, networkPolicyComponentPD),
			Namespace:       service.Namespace,
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(foreignService, v1alpha2.LLMInferenceServiceGVK)},
		},
		Spec: netv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"seed": "foreign"}}},
	}
	owned := []*netv1.NetworkPolicy{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:            workloadNetworkPolicyName(service),
				Namespace:       service.Namespace,
				OwnerReferences: ownedByController(service),
			},
			Spec: netv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"seed": "strict"}}},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:            ingressNetworkPolicyName(service, networkPolicyModeStrict, networkPolicyComponentMain),
				Namespace:       service.Namespace,
				OwnerReferences: ownedByController(service),
			},
			Spec: netv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"seed": "strict"}}},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:            ingressNetworkPolicyName(service, networkPolicyModeDefault, networkPolicyComponentPrefill),
				Namespace:       service.Namespace,
				OwnerReferences: ownedByController(service),
			},
			Spec: netv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"seed": "default"}}},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:            tokenizerNetworkPolicyName(service),
				Namespace:       service.Namespace,
				OwnerReferences: ownedByController(service),
			},
			Spec: netv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"seed": "strict"}}},
		},
		legacyGeneralEgressNetworkPolicy(service),
		legacyAllowAllNetworkPolicy(service),
		expectedTracingNetworkPolicy(service, netv1.NetworkPolicyPeer{}, 0, false),
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:            allowAllEgressNetworkPolicyName(service, networkPolicyComponentMain),
				Namespace:       service.Namespace,
				OwnerReferences: ownedByController(service),
			},
			Spec: netv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"seed": "egress"}}},
		},
	}
	objects := make([]client.Object, 0, 3+len(owned))
	objects = append(objects, foreignLegacy, ownerlessScheduler, foreignEgressPD)
	for _, policy := range owned {
		objects = append(objects, policy)
	}
	cl := newNetworkPolicyTestClient(t, objects...)

	conflicts := []*netv1.NetworkPolicy{foreignLegacy, ownerlessScheduler, foreignEgressPD}
	before := make([]*netv1.NetworkPolicy, len(conflicts))
	for i, policy := range conflicts {
		before[i] = &netv1.NetworkPolicy{}
		if err := cl.Get(t.Context(), client.ObjectKeyFromObject(policy), before[i]); err != nil {
			t.Fatalf("failed to read conflicting policy %s: %v", policy.Name, err)
		}
	}

	reconciler := &LLMISVCReconciler{Client: cl}
	for attempt := range 2 {
		if err := cleanupLLMISVCNetworkPolicies(t.Context(), reconciler, service); err != nil {
			t.Fatalf("cleanup attempt %d failed on ownership conflicts: %v", attempt+1, err)
		}
	}

	for i, policy := range conflicts {
		after := &netv1.NetworkPolicy{}
		if err := cl.Get(t.Context(), client.ObjectKeyFromObject(policy), after); err != nil {
			t.Fatalf("conflicting policy %s was deleted: %v", policy.Name, err)
		}
		if !reflect.DeepEqual(after, before[i]) {
			t.Fatalf("conflicting policy %s was mutated: before=%#v after=%#v", policy.Name, before[i], after)
		}
	}
	for _, policy := range owned {
		if err := cl.Get(t.Context(), client.ObjectKeyFromObject(policy), &netv1.NetworkPolicy{}); !apierrors.IsNotFound(err) {
			t.Fatalf("owned policy %s remained after cleanup: %v", policy.Name, err)
		}
	}
}

// TestCleanupNetworkPoliciesRetriesBoundedEgressNameDeletionFailures covers
// the finalizer's failure contract for the per-component allow-all egress
// names: an API failure deleting one bounded name fails the finalization —
// keeping the finalizer, the failed name, and the owned names after it in
// place — and the retried pass completes the cleanup.
func TestCleanupNetworkPoliciesRetriesBoundedEgressNameDeletionFailures(t *testing.T) {
	now := metav1.Now()
	service := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{
		Name:              "terminating-service",
		Namespace:         "team-a",
		UID:               "terminating-service-uid",
		DeletionTimestamp: &now,
	}}
	mainSelector := metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "terminating-service"}}
	ownedEgress := func(component string) *netv1.NetworkPolicy {
		return &netv1.NetworkPolicy{
			ObjectMeta: metav1.ObjectMeta{
				Name:            allowAllEgressNetworkPolicyName(service, component),
				Namespace:       service.Namespace,
				OwnerReferences: ownedByController(service),
			},
			Spec: netv1.NetworkPolicySpec{PodSelector: mainSelector},
		}
	}
	deleteErr := errors.New("transient egress NetworkPolicy Delete failure")
	failDelete := true
	cl := newNetworkPolicyTestClientWithInterceptor(t, interceptor.Funcs{
		Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			if policy, ok := obj.(*netv1.NetworkPolicy); ok && failDelete && policy.Name == allowAllEgressNetworkPolicyName(service, networkPolicyComponentMain) {
				return deleteErr
			}
			return c.Delete(ctx, obj, opts...)
		},
	}, ownedEgress(networkPolicyComponentMain), ownedEgress(networkPolicyComponentScheduler))
	r := &LLMISVCReconciler{Client: cl}

	if err := cleanupLLMISVCNetworkPolicies(t.Context(), r, service); !errors.Is(err, deleteErr) {
		t.Fatalf("cleanup error = %v, want the egress deletion failure %v", err, deleteErr)
	}
	// The failed pass leaves every owned egress policy in place: the failed
	// name survives its failed deletion, and the names after it in the bounded
	// list were never attempted. The finalizer stays until a retry succeeds.
	for _, component := range []string{networkPolicyComponentMain, networkPolicyComponentScheduler} {
		if err := cl.Get(t.Context(), types.NamespacedName{
			Name:      allowAllEgressNetworkPolicyName(service, component),
			Namespace: service.Namespace,
		}, &netv1.NetworkPolicy{}); err != nil {
			t.Fatalf("owned egress policy for %s must survive the failed cleanup pass: %v", component, err)
		}
	}

	failDelete = false
	if err := cleanupLLMISVCNetworkPolicies(t.Context(), r, service); err != nil {
		t.Fatalf("retry failed after the egress deletion recovered: %v", err)
	}
	for _, component := range []string{networkPolicyComponentMain, networkPolicyComponentScheduler} {
		if err := cl.Get(t.Context(), types.NamespacedName{
			Name:      allowAllEgressNetworkPolicyName(service, component),
			Namespace: service.Namespace,
		}, &netv1.NetworkPolicy{}); !apierrors.IsNotFound(err) {
			t.Fatalf("owned egress policy for %s remained after the successful retry: %v", component, err)
		}
	}
}

func TestDeleteAndCleanupNetworkPoliciesPropagateAPIErrors(t *testing.T) {
	service := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{
		Name: "model", Namespace: "team-a", UID: "model-uid",
	}}
	legacy := &netv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{
		Name: legacyMonitoringNetworkPolicyName(service), Namespace: service.Namespace,
		OwnerReferences: []metav1.OwnerReference{
			*metav1.NewControllerRef(service, v1alpha2.LLMInferenceServiceGVK),
		},
	}}

	t.Run("get failure is not classified as ownership conflict", func(t *testing.T) {
		getErr := errors.New("transient NetworkPolicy Get failure")
		cl := newNetworkPolicyTestClientWithInterceptor(t, interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*netv1.NetworkPolicy); ok && key.Name == legacy.Name {
					return getErr
				}
				return c.Get(ctx, key, obj, opts...)
			},
		}, legacy)
		reconciler := &LLMISVCReconciler{Client: cl}
		if err := deleteOwnedNetworkPolicy(t.Context(), reconciler, service, legacy); !errors.Is(err, getErr) {
			t.Fatalf("delete error = %v, want Get error %v", err, getErr)
		}
		if err := cleanupLLMISVCNetworkPolicies(t.Context(), reconciler, service); !errors.Is(err, getErr) {
			t.Fatalf("cleanup error = %v, want Get error %v", err, getErr)
		}
	})

	t.Run("delete failure propagates through deletion and cleanup", func(t *testing.T) {
		deleteErr := errors.New("transient NetworkPolicy Delete failure")
		cl := newNetworkPolicyTestClientWithInterceptor(t, interceptor.Funcs{
			Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
				if policy, ok := obj.(*netv1.NetworkPolicy); ok && policy.Name == legacy.Name {
					return deleteErr
				}
				return c.Delete(ctx, obj, opts...)
			},
		}, legacy)
		reconciler := &LLMISVCReconciler{Client: cl}
		if err := deleteOwnedNetworkPolicy(t.Context(), reconciler, service, legacy); !errors.Is(err, deleteErr) {
			t.Fatalf("delete error = %v, want API error %v", err, deleteErr)
		}
		if err := cleanupLLMISVCNetworkPolicies(t.Context(), reconciler, service); !errors.Is(err, deleteErr) {
			t.Fatalf("cleanup error = %v, want API error %v", err, deleteErr)
		}
		if err := cl.Get(t.Context(), client.ObjectKeyFromObject(legacy), &netv1.NetworkPolicy{}); err != nil {
			t.Fatalf("policy unexpectedly disappeared despite Delete failure: %v", err)
		}
	})
}

func TestReconcileLLMISVCNetworkPoliciesReportsLegacyRemovalFailuresAfterIngress(t *testing.T) {
	service := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{
		Name: "model", Namespace: "team-a", UID: "model-uid",
	}}
	legacy := &netv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{
		Name: legacyMonitoringNetworkPolicyName(service), Namespace: service.Namespace,
		OwnerReferences: []metav1.OwnerReference{
			*metav1.NewControllerRef(service, v1alpha2.LLMInferenceServiceGVK),
		},
	}}
	mainSelector := metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "model"}}

	for _, tt := range []struct {
		name      string
		getErr    error
		deleteErr error
	}{
		{name: "Get failure", getErr: errors.New("legacy policy Get failed")},
		{name: "Delete failure", deleteErr: errors.New("legacy policy Delete failed")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			failGet := tt.getErr != nil
			funcs := interceptor.Funcs{}
			if tt.getErr != nil {
				funcs.Get = func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if _, ok := obj.(*netv1.NetworkPolicy); ok && failGet && key.Name == legacy.Name {
						return tt.getErr
					}
					return c.Get(ctx, key, obj, opts...)
				}
			}
			if tt.deleteErr != nil {
				funcs.Delete = func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
					if policy, ok := obj.(*netv1.NetworkPolicy); ok && policy.Name == legacy.Name {
						return tt.deleteErr
					}
					return c.Delete(ctx, obj, opts...)
				}
			}
			cl := newNetworkPolicyTestClientWithInterceptor(t, funcs, legacy,
				ownedDeployment(service, mainDeploymentName(service), mainSelector.DeepCopy(), mainSelector.MatchLabels))
			r := &LLMISVCReconciler{Client: cl, EventRecorder: record.NewFakeRecorder(16)}

			err := reconcileLLMISVCNetworkPolicies(t.Context(), r, service, &Config{})
			wantErr := tt.getErr
			if wantErr == nil {
				wantErr = tt.deleteErr
			}
			if !errors.Is(err, wantErr) {
				t.Fatalf("reconcile error = %v, want reported legacy API error %v", err, wantErr)
			}
			if errors.Is(err, errNetworkPolicyOwnershipConflict) {
				t.Fatalf("real API error was misclassified as an ownership conflict: %v", err)
			}
			// The legacy policy is superseded ingress coverage, so its removal
			// runs only after the desired ingress set was created: the baseline
			// exists even though the legacy removal failed and will be retried.
			baseline := &netv1.NetworkPolicy{}
			if err := cl.Get(t.Context(), types.NamespacedName{
				Name:      ingressNetworkPolicyName(service, networkPolicyModeDefault, networkPolicyComponentMain),
				Namespace: service.Namespace,
			}, baseline); err != nil {
				t.Fatalf("baseline policy was not created before the legacy removal: %v", err)
			}
			if !reflect.DeepEqual(baseline.Spec.PodSelector, mainSelector) {
				t.Fatalf("baseline selector got %#v, want the stored Deployment selector", baseline.Spec.PodSelector)
			}
			// The verification read must not go through the failing interceptor.
			failGet = false
			if err := cl.Get(t.Context(), client.ObjectKeyFromObject(legacy), &netv1.NetworkPolicy{}); err != nil {
				t.Fatalf("legacy policy must survive a failed removal for the retry: %v", err)
			}
		})
	}
}

func TestReconcileLLMISVCNetworkPoliciesDefersLegacyConflictUntilBaselineExists(t *testing.T) {
	for _, strict := range []bool{false, true} {
		t.Run(fmt.Sprintf("strict-%t", strict), func(t *testing.T) {
			service := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{
				Name: "model", Namespace: "team-a", UID: "model-uid",
			}}
			legacy := &netv1.NetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      legacyMonitoringNetworkPolicyName(service),
					Namespace: service.Namespace,
					Labels:    map[string]string{"test-owner": "unowned"},
				},
				Spec: netv1.NetworkPolicySpec{
					PodSelector: metav1.LabelSelector{},
					PolicyTypes: []netv1.PolicyType{netv1.PolicyTypeIngress},
					Ingress:     []netv1.NetworkPolicyIngressRule{{}},
				},
			}
			mainSelector := metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "model"}}
			cl := newNetworkPolicyTestClient(t, legacy,
				ownedDeployment(service, mainDeploymentName(service), mainSelector.DeepCopy(), mainSelector.MatchLabels))
			before := &netv1.NetworkPolicy{}
			if err := cl.Get(t.Context(), client.ObjectKeyFromObject(legacy), before); err != nil {
				t.Fatal(err)
			}
			r := &LLMISVCReconciler{Client: cl, EventRecorder: record.NewFakeRecorder(32)}
			config := &Config{FeatureGates: FeatureGates{StrictNetworkPolicy: strict}}

			err := reconcileLLMISVCNetworkPolicies(t.Context(), r, service, config)
			if !errors.Is(err, errNetworkPolicyOwnershipConflict) {
				t.Fatalf("reconcile error = %v, want deferred legacy ownership conflict", err)
			}
			for _, name := range []string{
				"NetworkPolicy " + service.Namespace + "/" + legacy.Name,
				"LLMInferenceService " + service.Namespace + "/" + service.Name,
			} {
				if !strings.Contains(err.Error(), name) {
					t.Fatalf("reconcile error %q does not include %q", err, name)
				}
			}

			after := &netv1.NetworkPolicy{}
			if err := cl.Get(t.Context(), client.ObjectKeyFromObject(legacy), after); err != nil {
				t.Fatalf("legacy policy was deleted: %v", err)
			}
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("legacy policy was mutated: before=%#v after=%#v", before, after)
			}

			// With the main Deployment stored, the baseline of either mode is its
			// per-component main ingress policy.
			baselineNames := []string{ingressNetworkPolicyName(service, networkPolicyModeStrict, networkPolicyComponentMain)}
			if !strict {
				baselineNames = []string{ingressNetworkPolicyName(service, networkPolicyModeDefault, networkPolicyComponentMain)}
			}
			for _, name := range baselineNames {
				policy := &netv1.NetworkPolicy{}
				if err := cl.Get(t.Context(), types.NamespacedName{Name: name, Namespace: service.Namespace}, policy); err != nil {
					t.Fatalf("baseline policy %s was not created: %v", name, err)
				}
				if !metav1.IsControlledBy(policy, service) {
					t.Fatalf("baseline policy %s was not created as service-owned", name)
				}
			}
		})
	}
}

func TestReconcileLLMISVCNetworkPoliciesSkipsLegacyRemovalWhenIngressFails(t *testing.T) {
	service := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{
		Name: "model", Namespace: "team-a", UID: "model-uid",
	}}
	legacy := &netv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{
		Name: legacyMonitoringNetworkPolicyName(service), Namespace: service.Namespace,
	}}
	mainSelector := metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "model"}}
	createErr := errors.New("baseline NetworkPolicy Create failed")
	failCreate := true
	cl := newNetworkPolicyTestClientWithInterceptor(t, interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if policy, ok := obj.(*netv1.NetworkPolicy); ok && failCreate && policy.Name == ingressNetworkPolicyName(service, networkPolicyModeDefault, networkPolicyComponentMain) {
				return createErr
			}
			return c.Create(ctx, obj, opts...)
		},
	}, legacy, ownedDeployment(service, mainDeploymentName(service), mainSelector.DeepCopy(), mainSelector.MatchLabels))
	r := &LLMISVCReconciler{Client: cl, EventRecorder: record.NewFakeRecorder(16)}

	// The desired ingress set could not be created, so the superseded legacy
	// policy — prior ingress coverage — must not be pruned, and its ownership
	// conflict must not even be attempted: only the creation failure reports.
	err := reconcileLLMISVCNetworkPolicies(t.Context(), r, service, &Config{})
	if !errors.Is(err, createErr) {
		t.Fatalf("reconcile error %v does not contain the baseline failure", err)
	}
	if errors.Is(err, errNetworkPolicyOwnershipConflict) {
		t.Fatalf("reconcile error %v must not contain a legacy conflict that was never attempted", err)
	}
	if err := cl.Get(t.Context(), client.ObjectKeyFromObject(legacy), &netv1.NetworkPolicy{}); err != nil {
		t.Fatalf("legacy policy must be retained while ingress creation fails: %v", err)
	}

	// Once the baseline exists, the legacy removal runs and reports the
	// ownerless policy as an ownership conflict without deleting it.
	failCreate = false
	err = reconcileLLMISVCNetworkPolicies(t.Context(), r, service, &Config{})
	if !errors.Is(err, errNetworkPolicyOwnershipConflict) {
		t.Fatalf("reconcile error %v does not contain the legacy ownership conflict", err)
	}
	baseline := &netv1.NetworkPolicy{}
	if err := cl.Get(t.Context(), types.NamespacedName{
		Name:      ingressNetworkPolicyName(service, networkPolicyModeDefault, networkPolicyComponentMain),
		Namespace: service.Namespace,
	}, baseline); err != nil {
		t.Fatalf("baseline policy was not created on the retry: %v", err)
	}
	if err := cl.Get(t.Context(), client.ObjectKeyFromObject(legacy), &netv1.NetworkPolicy{}); err != nil {
		t.Fatalf("ownerless legacy policy must never be deleted: %v", err)
	}
}

// TestReconcileLLMISVCNetworkPoliciesPreservesIngressCoverageWhenInventoryFails
// covers the create-before-prune contract: an inventory read failure must not
// prune any prior ingress coverage — stale component names, historical names,
// the combined allow-all policy, or the legacy monitoring policy — and the
// egress lifecycle resolves its own selector inventory, so the same failure
// also retains the prior egress coverage for the retry, which completes the
// migration.
func TestReconcileLLMISVCNetworkPoliciesPreservesIngressCoverageWhenInventoryFails(t *testing.T) {
	service := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{
		Name: "model", Namespace: "team-a", UID: "model-uid",
	}}
	mainSelector := metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "model"}}
	ownedPolicy := func(name string) *netv1.NetworkPolicy {
		return &netv1.NetworkPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: service.Namespace, OwnerReferences: ownedByController(service)},
			Spec:       netv1.NetworkPolicySpec{PodSelector: mainSelector},
		}
	}
	// Prior coverage from earlier reconciles: the desired default-mode main
	// policy, a stale default-mode policy of a component that left the
	// topology, a strict-mode policy of a mode switch, a pre-selector strict
	// name, the combined allow-all and legacy monitoring policies, and the
	// general egress policy.
	seeded := []client.Object{
		ownedDeployment(service, mainDeploymentName(service), mainSelector.DeepCopy(), mainSelector.MatchLabels),
		ownedPolicy(ingressNetworkPolicyName(service, networkPolicyModeDefault, networkPolicyComponentMain)),
		ownedPolicy(ingressNetworkPolicyName(service, networkPolicyModeDefault, networkPolicyComponentScheduler)),
		ownedPolicy(ingressNetworkPolicyName(service, networkPolicyModeStrict, networkPolicyComponentMain)),
		ownedPolicy(workloadNetworkPolicyName(service)),
		legacyAllowAllNetworkPolicy(service),
		legacyGeneralEgressNetworkPolicy(service),
		ownedPolicy(legacyMonitoringNetworkPolicyName(service)),
	}
	inventoryErr := errors.New("transient Deployment read failure")
	failInventory := true
	cl := newNetworkPolicyTestClientWithInterceptor(t, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*appsv1.Deployment); ok && failInventory && key.Name == mainDeploymentName(service) {
				return inventoryErr
			}
			return c.Get(ctx, key, obj, opts...)
		},
	}, seeded...)
	r := &LLMISVCReconciler{Client: cl, EventRecorder: record.NewFakeRecorder(16)}

	err := reconcileLLMISVCNetworkPolicies(t.Context(), r, service, &Config{})
	if !errors.Is(err, inventoryErr) {
		t.Fatalf("reconcile error = %v, want the inventory read failure %v", err, inventoryErr)
	}
	// The desired ingress set was not established, so every policy that
	// carries ingress coverage must be retained for the retry.
	for _, name := range []string{
		ingressNetworkPolicyName(service, networkPolicyModeDefault, networkPolicyComponentMain),
		ingressNetworkPolicyName(service, networkPolicyModeDefault, networkPolicyComponentScheduler),
		ingressNetworkPolicyName(service, networkPolicyModeStrict, networkPolicyComponentMain),
		workloadNetworkPolicyName(service),
		allowAllNetworkPolicyName(service),
		legacyMonitoringNetworkPolicyName(service),
	} {
		if err := cl.Get(t.Context(), types.NamespacedName{Name: name, Namespace: service.Namespace}, &netv1.NetworkPolicy{}); err != nil {
			t.Fatalf("ingress coverage %s must be retained while the inventory fails: %v", name, err)
		}
	}
	// The egress lifecycle resolves its own selector inventory, so the same read
	// failure keeps it from establishing the allow-all replacement: the legacy
	// general egress policy must be retained — its deletion is gated on the
	// replacement being ready — instead of being removed unconditionally.
	if err := cl.Get(t.Context(), types.NamespacedName{Name: egressNetworkPolicyName(service), Namespace: service.Namespace}, &netv1.NetworkPolicy{}); err != nil {
		t.Fatalf("legacy general egress policy must be retained while the egress replacement cannot be established: %v", err)
	}

	// The retry completes the migration: the desired default set replaces the
	// stale, historical, combined, and monitoring policies.
	failInventory = false
	if err := reconcileLLMISVCNetworkPolicies(t.Context(), r, service, &Config{}); err != nil {
		t.Fatalf("retry failed after the inventory recovered: %v", err)
	}
	defaultMain := &netv1.NetworkPolicy{}
	if err := cl.Get(t.Context(), types.NamespacedName{
		Name:      ingressNetworkPolicyName(service, networkPolicyModeDefault, networkPolicyComponentMain),
		Namespace: service.Namespace,
	}, defaultMain); err != nil {
		t.Fatalf("desired default policy was not created on the retry: %v", err)
	}
	if !reflect.DeepEqual(defaultMain.Spec.PodSelector, mainSelector) {
		t.Fatalf("default policy selector got %#v, want the stored Deployment selector", defaultMain.Spec.PodSelector)
	}
	// The retry also establishes the allow-all egress replacement, which is
	// what retires the legacy general egress policy.
	egressMain := &netv1.NetworkPolicy{}
	if err := cl.Get(t.Context(), types.NamespacedName{
		Name:      allowAllEgressNetworkPolicyName(service, networkPolicyComponentMain),
		Namespace: service.Namespace,
	}, egressMain); err != nil {
		t.Fatalf("allow-all egress policy was not created on the retry: %v", err)
	}
	if !reflect.DeepEqual(egressMain.Spec.PodSelector, mainSelector) {
		t.Fatalf("allow-all egress policy selector got %#v, want the stored Deployment selector", egressMain.Spec.PodSelector)
	}
	for _, name := range []string{
		egressNetworkPolicyName(service),
		ingressNetworkPolicyName(service, networkPolicyModeDefault, networkPolicyComponentScheduler),
		ingressNetworkPolicyName(service, networkPolicyModeStrict, networkPolicyComponentMain),
		workloadNetworkPolicyName(service),
		allowAllNetworkPolicyName(service),
		legacyMonitoringNetworkPolicyName(service),
	} {
		if err := cl.Get(t.Context(), types.NamespacedName{Name: name, Namespace: service.Namespace}, &netv1.NetworkPolicy{}); !apierrors.IsNotFound(err) {
			t.Fatalf("policy %s remained after the successful retry: %v", name, err)
		}
	}
}

// TestReconcileLLMISVCNetworkPoliciesRetainsAllPriorIngressUntilReplacementCompletes
// covers the create-before-delete contract against explicit intercepted write
// failures, with a multi-component desired set: a Create failure on a later
// component and an Update failure on a desired-name policy must each retain
// every previous ingress grant — the stale per-component names of the other
// mode, the pre-selector strict names, the branch-created combined allow-all
// policy, and the legacy monitoring policy — until a retry establishes the
// complete replacement set and cleans the bounded stale names. The egress
// lifecycle is independent of the failed ingress writes: it establishes the
// per-component allow-all replacement, which retires the legacy general
// egress policy.
func TestReconcileLLMISVCNetworkPoliciesRetainsAllPriorIngressUntilReplacementCompletes(t *testing.T) {
	newService := func() *v1alpha2.LLMInferenceService {
		return &v1alpha2.LLMInferenceService{
			ObjectMeta: metav1.ObjectMeta{Name: "model", Namespace: "team-a", UID: "model-uid"},
			Spec: v1alpha2.LLMInferenceServiceSpec{Router: &v1alpha2.RouterSpec{
				Scheduler: &v1alpha2.SchedulerSpec{Template: &corev1.PodSpec{}},
			}},
		}
	}
	mainSelector := metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "model"}}
	schedulerSelector := metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "model-scheduler"}}
	storedComponents := func(service *v1alpha2.LLMInferenceService) []client.Object {
		return []client.Object{
			ownedDeployment(service, mainDeploymentName(service), mainSelector.DeepCopy(), mainSelector.MatchLabels),
			ownedDeployment(service, schedulerDeploymentName(service), schedulerSelector.DeepCopy(), schedulerSelector.MatchLabels),
		}
	}
	ownedPolicy := func(service *v1alpha2.LLMInferenceService, name string, selector metav1.LabelSelector) *netv1.NetworkPolicy {
		return &netv1.NetworkPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: service.Namespace, OwnerReferences: ownedByController(service)},
			Spec:       netv1.NetworkPolicySpec{PodSelector: selector},
		}
	}
	// Every ingress grant an earlier reconcile of either mode or an earlier
	// build may have left behind; the failing pass must retain all of them.
	priorIngressNames := func(service *v1alpha2.LLMInferenceService) []string {
		return []string{
			ingressNetworkPolicyName(service, networkPolicyModeDefault, networkPolicyComponentMain),
			ingressNetworkPolicyName(service, networkPolicyModeDefault, networkPolicyComponentScheduler),
			workloadNetworkPolicyName(service),
			allowAllNetworkPolicyName(service),
			legacyMonitoringNetworkPolicyName(service),
		}
	}

	t.Run("create failure on a later component", func(t *testing.T) {
		service := newService()
		seeded := append(storedComponents(service),
			ownedPolicy(service, ingressNetworkPolicyName(service, networkPolicyModeDefault, networkPolicyComponentMain), mainSelector),
			ownedPolicy(service, ingressNetworkPolicyName(service, networkPolicyModeDefault, networkPolicyComponentScheduler), schedulerSelector),
			ownedPolicy(service, workloadNetworkPolicyName(service), mainSelector),
			legacyAllowAllNetworkPolicy(service),
			legacyGeneralEgressNetworkPolicy(service),
			ownedPolicy(service, legacyMonitoringNetworkPolicyName(service), mainSelector),
		)
		createErr := errors.New("strict scheduler NetworkPolicy Create failed")
		failCreate := true
		cl := newNetworkPolicyTestClientWithInterceptor(t, interceptor.Funcs{
			Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				if policy, ok := obj.(*netv1.NetworkPolicy); ok && failCreate &&
					policy.Name == ingressNetworkPolicyName(service, networkPolicyModeStrict, networkPolicyComponentScheduler) {
					return createErr
				}
				return c.Create(ctx, obj, opts...)
			},
		}, seeded...)
		r := &LLMISVCReconciler{Client: cl, EventRecorder: record.NewFakeRecorder(16)}
		config := &Config{FeatureGates: FeatureGates{StrictNetworkPolicy: true}}

		err := reconcileLLMISVCNetworkPolicies(t.Context(), r, service, config)
		if !errors.Is(err, createErr) {
			t.Fatalf("reconcile error = %v, want the strict scheduler creation failure %v", err, createErr)
		}
		// The desired set is incomplete, so every previous ingress grant is
		// retained for the retry — including the combined allow-all policy,
		// which earlier builds also used as ingress coverage.
		for _, name := range priorIngressNames(service) {
			if err := cl.Get(t.Context(), types.NamespacedName{Name: name, Namespace: service.Namespace}, &netv1.NetworkPolicy{}); err != nil {
				t.Fatalf("prior ingress grant %s must be retained until the replacement completes: %v", name, err)
			}
		}
		// The egress lifecycle is independent of the failed ingress creation:
		// it established the per-component allow-all egress replacement for
		// both stored components, which is what retires the legacy general
		// egress policy.
		for _, component := range []string{networkPolicyComponentMain, networkPolicyComponentScheduler} {
			egressPolicy := &netv1.NetworkPolicy{}
			if err := cl.Get(t.Context(), types.NamespacedName{
				Name:      allowAllEgressNetworkPolicyName(service, component),
				Namespace: service.Namespace,
			}, egressPolicy); err != nil {
				t.Fatalf("allow-all egress policy for %s must exist despite the ingress creation failure: %v", component, err)
			}
		}
		if err := cl.Get(t.Context(), types.NamespacedName{Name: egressNetworkPolicyName(service), Namespace: service.Namespace}, &netv1.NetworkPolicy{}); !apierrors.IsNotFound(err) {
			t.Fatalf("legacy general egress policy must be removed once its replacement exists: %v", err)
		}
		// Create-before-delete allows a partial desired set: the first strict
		// policy may already exist while the second one is being retried.
		if err := cl.Get(t.Context(), types.NamespacedName{
			Name: ingressNetworkPolicyName(service, networkPolicyModeStrict, networkPolicyComponentMain), Namespace: service.Namespace,
		}, &netv1.NetworkPolicy{}); err != nil {
			t.Fatalf("partial desired set must be allowed to exist: %v", err)
		}

		// The retry establishes the complete replacement set and cleans every
		// bounded stale name.
		failCreate = false
		if err := reconcileLLMISVCNetworkPolicies(t.Context(), r, service, config); err != nil {
			t.Fatalf("retry failed after the creation recovered: %v", err)
		}
		for _, name := range []string{
			ingressNetworkPolicyName(service, networkPolicyModeStrict, networkPolicyComponentMain),
			ingressNetworkPolicyName(service, networkPolicyModeStrict, networkPolicyComponentScheduler),
		} {
			policy := &netv1.NetworkPolicy{}
			if err := cl.Get(t.Context(), types.NamespacedName{Name: name, Namespace: service.Namespace}, policy); err != nil {
				t.Fatalf("strict policy %s was not created on the retry: %v", name, err)
			}
		}
		for _, name := range priorIngressNames(service) {
			if err := cl.Get(t.Context(), types.NamespacedName{Name: name, Namespace: service.Namespace}, &netv1.NetworkPolicy{}); !apierrors.IsNotFound(err) {
				t.Fatalf("stale ingress grant %s remained after the successful retry: %v", name, err)
			}
		}
	})

	t.Run("update failure on a desired-name policy", func(t *testing.T) {
		service := newService()
		// A strict-mode policy of an earlier reconcile left with a stale
		// selector after the stored Deployment kept its original immutable
		// selector: the desired name still carries prior ingress coverage that
		// must survive a failed update.
		staleStrictMain := ownedPolicy(service,
			ingressNetworkPolicyName(service, networkPolicyModeStrict, networkPolicyComponentMain),
			metav1.LabelSelector{MatchLabels: map[string]string{"app": "stale"}})
		seeded := append(storedComponents(service),
			staleStrictMain,
			ownedPolicy(service, ingressNetworkPolicyName(service, networkPolicyModeDefault, networkPolicyComponentScheduler), schedulerSelector),
			legacyAllowAllNetworkPolicy(service),
			legacyGeneralEgressNetworkPolicy(service),
			ownedPolicy(service, legacyMonitoringNetworkPolicyName(service), mainSelector),
		)
		updateErr := errors.New("strict main NetworkPolicy Update failed")
		failUpdate := true
		cl := newNetworkPolicyTestClientWithInterceptor(t, interceptor.Funcs{
			Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				if policy, ok := obj.(*netv1.NetworkPolicy); ok && failUpdate &&
					policy.Name == ingressNetworkPolicyName(service, networkPolicyModeStrict, networkPolicyComponentMain) {
					return updateErr
				}
				return c.Update(ctx, obj, opts...)
			},
		}, seeded...)
		r := &LLMISVCReconciler{Client: cl, EventRecorder: record.NewFakeRecorder(16)}
		config := &Config{FeatureGates: FeatureGates{StrictNetworkPolicy: true}}

		err := reconcileLLMISVCNetworkPolicies(t.Context(), r, service, config)
		if !errors.Is(err, updateErr) {
			t.Fatalf("reconcile error = %v, want the strict main update failure %v", err, updateErr)
		}
		// The desired set is incomplete, so the other-mode stale name, the
		// combined allow-all policy, and the legacy monitoring policy are all
		// retained, and the desired-name policy keeps its prior coverage
		// unmutated for the retry.
		for _, name := range []string{
			ingressNetworkPolicyName(service, networkPolicyModeDefault, networkPolicyComponentScheduler),
			allowAllNetworkPolicyName(service),
			legacyMonitoringNetworkPolicyName(service),
		} {
			if err := cl.Get(t.Context(), types.NamespacedName{Name: name, Namespace: service.Namespace}, &netv1.NetworkPolicy{}); err != nil {
				t.Fatalf("prior ingress grant %s must be retained until the replacement completes: %v", name, err)
			}
		}
		strictMain := &netv1.NetworkPolicy{}
		if err := cl.Get(t.Context(), types.NamespacedName{
			Name: ingressNetworkPolicyName(service, networkPolicyModeStrict, networkPolicyComponentMain), Namespace: service.Namespace,
		}, strictMain); err != nil {
			t.Fatalf("desired-name policy must survive the failed update: %v", err)
		}
		if !reflect.DeepEqual(strictMain.Spec.PodSelector, staleStrictMain.Spec.PodSelector) {
			t.Fatalf("desired-name policy was mutated despite the failed update: got %#v want %#v",
				strictMain.Spec.PodSelector, staleStrictMain.Spec.PodSelector)
		}
		if err := cl.Get(t.Context(), types.NamespacedName{Name: egressNetworkPolicyName(service), Namespace: service.Namespace}, &netv1.NetworkPolicy{}); !apierrors.IsNotFound(err) {
			t.Fatalf("legacy general egress policy must be removed once its replacement exists: %v", err)
		}

		// The retry completes the replacement: the desired-name policy is
		// repaired onto the stored selector and the bounded stale names go.
		failUpdate = false
		if err := reconcileLLMISVCNetworkPolicies(t.Context(), r, service, config); err != nil {
			t.Fatalf("retry failed after the update recovered: %v", err)
		}
		strictMain = &netv1.NetworkPolicy{}
		if err := cl.Get(t.Context(), types.NamespacedName{
			Name: ingressNetworkPolicyName(service, networkPolicyModeStrict, networkPolicyComponentMain), Namespace: service.Namespace,
		}, strictMain); err != nil {
			t.Fatalf("strict main policy was not repaired on the retry: %v", err)
		}
		if !reflect.DeepEqual(strictMain.Spec.PodSelector, mainSelector) {
			t.Fatalf("repaired strict main selector got %#v, want the stored Deployment selector", strictMain.Spec.PodSelector)
		}
		for _, name := range []string{
			ingressNetworkPolicyName(service, networkPolicyModeDefault, networkPolicyComponentScheduler),
			allowAllNetworkPolicyName(service),
			legacyMonitoringNetworkPolicyName(service),
		} {
			if err := cl.Get(t.Context(), types.NamespacedName{Name: name, Namespace: service.Namespace}, &netv1.NetworkPolicy{}); !apierrors.IsNotFound(err) {
				t.Fatalf("stale ingress grant %s remained after the successful retry: %v", name, err)
			}
		}
	})
}

// TestReconcileLLMISVCNetworkPoliciesStrictModePrunesTopologyAndHistoricalNames
// covers stale-name pruning within one mode: pre-selector strict names and
// other-mode names are removed, and a component that leaves the selector
// inventory (topology convergence) loses its strict policy while the
// remaining components keep theirs.
func TestReconcileLLMISVCNetworkPoliciesStrictModePrunesTopologyAndHistoricalNames(t *testing.T) {
	service := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{
		Name: "model", Namespace: "team-a", UID: "model-uid",
	}}
	mainSelector := metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "model"}}
	schedulerSelector := metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "model-scheduler"}}
	ownedPolicy := func(name string) *netv1.NetworkPolicy {
		return &netv1.NetworkPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: service.Namespace, OwnerReferences: ownedByController(service)},
			Spec:       netv1.NetworkPolicySpec{PodSelector: mainSelector},
		}
	}
	schedulerDeployment := ownedDeployment(service, schedulerDeploymentName(service), schedulerSelector.DeepCopy(), schedulerSelector.MatchLabels)
	config := &Config{FeatureGates: FeatureGates{StrictNetworkPolicy: true}}

	cl := newNetworkPolicyTestClient(t,
		ownedDeployment(service, mainDeploymentName(service), mainSelector.DeepCopy(), mainSelector.MatchLabels),
		schedulerDeployment,
		// Pre-selector strict names and a default-mode policy left behind by
		// earlier reconciles and mode switches.
		ownedPolicy(workloadNetworkPolicyName(service)),
		ownedPolicy(schedulerNetworkPolicyName(service)),
		ownedPolicy(tokenizerNetworkPolicyName(service)),
		ownedPolicy(ingressNetworkPolicyName(service, networkPolicyModeDefault, networkPolicyComponentPrefill)),
	)
	r := &LLMISVCReconciler{Client: cl, EventRecorder: record.NewFakeRecorder(16)}

	if err := reconcileLLMISVCNetworkPolicies(t.Context(), r, service, config); err != nil {
		t.Fatalf("strict reconcile with the full topology failed: %v", err)
	}
	for _, name := range []string{
		ingressNetworkPolicyName(service, networkPolicyModeStrict, networkPolicyComponentMain),
		ingressNetworkPolicyName(service, networkPolicyModeStrict, networkPolicyComponentScheduler),
	} {
		policy := &netv1.NetworkPolicy{}
		if err := cl.Get(t.Context(), types.NamespacedName{Name: name, Namespace: service.Namespace}, policy); err != nil {
			t.Fatalf("strict policy %s was not created: %v", name, err)
		}
		if !metav1.IsControlledBy(policy, service) {
			t.Fatalf("strict policy %s is not controlled by the service", name)
		}
	}
	for _, name := range []string{
		workloadNetworkPolicyName(service),
		schedulerNetworkPolicyName(service),
		tokenizerNetworkPolicyName(service),
		ingressNetworkPolicyName(service, networkPolicyModeDefault, networkPolicyComponentPrefill),
	} {
		if err := cl.Get(t.Context(), types.NamespacedName{Name: name, Namespace: service.Namespace}, &netv1.NetworkPolicy{}); !apierrors.IsNotFound(err) {
			t.Fatalf("stale policy %s remained after the strict reconcile: %v", name, err)
		}
	}

	// Topology convergence: when the scheduler Deployment is gone, its strict
	// policy is pruned while the main policy keeps covering its Pods.
	if err := cl.Delete(t.Context(), schedulerDeployment); err != nil {
		t.Fatal(err)
	}
	if err := reconcileLLMISVCNetworkPolicies(t.Context(), r, service, config); err != nil {
		t.Fatalf("strict reconcile after the topology change failed: %v", err)
	}
	if err := cl.Get(t.Context(), types.NamespacedName{
		Name:      ingressNetworkPolicyName(service, networkPolicyModeStrict, networkPolicyComponentScheduler),
		Namespace: service.Namespace,
	}, &netv1.NetworkPolicy{}); !apierrors.IsNotFound(err) {
		t.Fatalf("strict scheduler policy remained after the component left the inventory: %v", err)
	}
	mainPolicy := &netv1.NetworkPolicy{}
	if err := cl.Get(t.Context(), types.NamespacedName{
		Name:      ingressNetworkPolicyName(service, networkPolicyModeStrict, networkPolicyComponentMain),
		Namespace: service.Namespace,
	}, mainPolicy); err != nil {
		t.Fatalf("strict main policy was lost by the topology change: %v", err)
	}
	if !reflect.DeepEqual(mainPolicy.Spec.PodSelector, mainSelector) {
		t.Fatalf("strict main selector got %#v, want the stored Deployment selector", mainPolicy.Spec.PodSelector)
	}
}

// TestReconcileLLMISVCNetworkPoliciesStoppedManagedSchedulerDoesNotErrorLoop
// covers the force-stop state Task 2 warned about: the service keeps its
// managed scheduler configured while the workload reconcilers have deleted
// the scheduler Deployment. The stopped state must reconcile without an error
// loop and without widening any source: no scheduler peer, no broad fallback,
// and — with no stored components — no managed policy at all.
func TestReconcileLLMISVCNetworkPoliciesStoppedManagedSchedulerDoesNotErrorLoop(t *testing.T) {
	service := &v1alpha2.LLMInferenceService{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "model",
			Namespace:   "team-a",
			UID:         "model-uid",
			Annotations: map[string]string{constants.StopAnnotationKey: "true"},
		},
		Spec: v1alpha2.LLMInferenceServiceSpec{Router: &v1alpha2.RouterSpec{
			Scheduler: &v1alpha2.SchedulerSpec{Template: &corev1.PodSpec{}},
		}},
	}
	mainSelector := metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "model"}}
	staleDefaultMain := &netv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:            ingressNetworkPolicyName(service, networkPolicyModeDefault, networkPolicyComponentMain),
			Namespace:       service.Namespace,
			OwnerReferences: ownedByController(service),
		},
		Spec: netv1.NetworkPolicySpec{PodSelector: mainSelector},
	}
	cl := newNetworkPolicyTestClient(t, staleDefaultMain)
	r := &LLMISVCReconciler{Client: cl, EventRecorder: record.NewFakeRecorder(16)}

	if err := reconcileLLMISVCNetworkPolicies(t.Context(), r, service, &Config{FeatureGates: FeatureGates{StrictNetworkPolicy: true}}); err != nil {
		t.Fatalf("a stopped managed scheduler must not fail the reconcile: %v", err)
	}
	// The stopped service has no stored components, so nothing is created and
	// the policy from before the stop is pruned with the empty inventory.
	policies := &netv1.NetworkPolicyList{}
	if err := cl.List(t.Context(), policies, client.InNamespace(service.Namespace)); err != nil {
		t.Fatal(err)
	}
	if len(policies.Items) != 0 {
		t.Fatalf("stopped service must leave no managed NetworkPolicies, got %#v", policies.Items)
	}
	for _, name := range []string{allowAllNetworkPolicyName(service), egressNetworkPolicyName(service)} {
		if err := cl.Get(t.Context(), types.NamespacedName{Name: name, Namespace: service.Namespace}, &netv1.NetworkPolicy{}); !apierrors.IsNotFound(err) {
			t.Fatalf("no broad fallback may be created for a stopped service: %v", err)
		}
	}
}

func TestReconcileLLMISVCNetworkPoliciesDoesNotAdoptActivePolicyConflict(t *testing.T) {
	service := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{
		Name: "model", Namespace: "team-a", UID: "model-uid",
	}}
	foreignService := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{
		Name: "other-model", Namespace: service.Namespace, UID: "other-model-uid",
	}}
	foreignActivePolicy := &netv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ingressNetworkPolicyName(service, networkPolicyModeDefault, networkPolicyComponentMain),
			Namespace: service.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(foreignService, v1alpha2.LLMInferenceServiceGVK),
			},
		},
		Spec: netv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"seed": "foreign"}}},
	}
	mainSelector := metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "model"}}
	cl := newNetworkPolicyTestClient(t, foreignActivePolicy,
		ownedDeployment(service, mainDeploymentName(service), mainSelector.DeepCopy(), mainSelector.MatchLabels))
	before := &netv1.NetworkPolicy{}
	if err := cl.Get(t.Context(), client.ObjectKeyFromObject(foreignActivePolicy), before); err != nil {
		t.Fatal(err)
	}
	r := &LLMISVCReconciler{Client: cl, EventRecorder: record.NewFakeRecorder(16)}

	err := reconcileLLMISVCNetworkPolicies(t.Context(), r, service, &Config{})
	if err == nil {
		t.Fatal("active-policy ownership conflict was not returned")
	}
	if errors.Is(err, errNetworkPolicyOwnershipConflict) {
		t.Fatalf("active-policy conflict was incorrectly classified as a deferrable legacy conflict: %v", err)
	}
	after := &netv1.NetworkPolicy{}
	if err := cl.Get(t.Context(), client.ObjectKeyFromObject(foreignActivePolicy), after); err != nil {
		t.Fatalf("foreign active policy was deleted: %v", err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("foreign active policy was mutated/adopted: before=%#v after=%#v", before, after)
	}
}

func TestReconcileTracingConflictRetainsTracingAndReconcilesIngressIndependently(t *testing.T) {
	endpoint := "https://collector.example.com:4317"
	service := &v1alpha2.LLMInferenceService{
		ObjectMeta: metav1.ObjectMeta{
			Name: "model", Namespace: "team-a", UID: "model-uid",
			Annotations: map[string]string{
				constants.EnableTracingEgressNetworkPolicyAnnotationKey: "true",
			},
		},
		Spec: v1alpha2.LLMInferenceServiceSpec{Tracing: &v1alpha2.TracingSpec{ExporterEndpoint: &endpoint}},
	}
	legacy := &netv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: legacyMonitoringNetworkPolicyName(service), Namespace: service.Namespace},
		Spec: netv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{},
			PolicyTypes: []netv1.PolicyType{netv1.PolicyTypeIngress},
			Ingress:     []netv1.NetworkPolicyIngressRule{{}},
		},
	}
	mainSelector := metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "model"}}
	foreignService := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{
		Name: "other-model", Namespace: service.Namespace, UID: "other-model-uid",
	}}
	foreignTracing := expectedTracingNetworkPolicy(service, netv1.NetworkPolicyPeer{}, 0, false)
	foreignTracing.OwnerReferences = []metav1.OwnerReference{
		*metav1.NewControllerRef(foreignService, v1alpha2.LLMInferenceServiceGVK),
	}
	// This foreign additive grant is deliberately retained; the test verifies
	// error visibility and independence, not traffic isolation.
	foreignTracing.Spec.Egress = []netv1.NetworkPolicyEgressRule{{}}
	objects := []client.Object{
		legacy,
		ownedDeployment(service, mainDeploymentName(service), mainSelector.DeepCopy(), mainSelector.MatchLabels),
		legacyAllowAllNetworkPolicy(service),
		foreignTracing,
	}
	cl := newNetworkPolicyTestClient(t, objects...)
	legacyBefore := &netv1.NetworkPolicy{}
	foreignBefore := &netv1.NetworkPolicy{}
	for _, item := range []struct {
		object *netv1.NetworkPolicy
		before *netv1.NetworkPolicy
	}{{legacy, legacyBefore}, {foreignTracing, foreignBefore}} {
		if err := cl.Get(t.Context(), client.ObjectKeyFromObject(item.object), item.before); err != nil {
			t.Fatal(err)
		}
	}
	r := &LLMISVCReconciler{Client: cl, EventRecorder: record.NewFakeRecorder(32)}

	err := reconcileLLMISVCNetworkPolicies(t.Context(), r, service, &Config{})
	var tracingConflict *tracingNetworkPolicyNotOwnedError
	if !errors.As(err, &tracingConflict) {
		t.Fatalf("reconcile error %v does not contain active tracing ownership conflict", err)
	}
	// The egress state failed on the tracing conflict, so the superseded
	// legacy removal is deferred rather than attempted: the reconcile error
	// carries the tracing conflict without a deferrable legacy conflict.
	if errors.Is(err, errNetworkPolicyOwnershipConflict) {
		t.Fatalf("reconcile error %v must defer the legacy removal while the egress state fails", err)
	}

	// Ingress was reconciled independently of the tracing failure.
	defaultMain := &netv1.NetworkPolicy{}
	if err := cl.Get(t.Context(), types.NamespacedName{
		Name:      ingressNetworkPolicyName(service, networkPolicyModeDefault, networkPolicyComponentMain),
		Namespace: service.Namespace,
	}, defaultMain); err != nil {
		t.Fatalf("ingress policy was not reconciled despite the tracing conflict: %v", err)
	}
	if !reflect.DeepEqual(defaultMain.Spec.PodSelector, mainSelector) {
		t.Fatalf("ingress policy selector got %#v, want the stored Deployment selector", defaultMain.Spec.PodSelector)
	}
	if !reflect.DeepEqual(defaultMain.Spec.PolicyTypes, []netv1.PolicyType{netv1.PolicyTypeIngress}) {
		t.Fatalf("ingress policy types got %v", defaultMain.Spec.PolicyTypes)
	}
	// The branch-created combined policy is superseded ingress coverage, so
	// its removal waits for a successful egress state: it is retained — like
	// the legacy monitoring policy — alongside the reported tracing conflict,
	// and no deny-all or general egress fallback is created.
	for _, name := range []string{allowAllNetworkPolicyName(service), legacyMonitoringNetworkPolicyName(service)} {
		if err := cl.Get(t.Context(), types.NamespacedName{Name: name, Namespace: service.Namespace}, &netv1.NetworkPolicy{}); err != nil {
			t.Fatalf("superseded policy %s must be retained while the egress state fails: %v", name, err)
		}
	}
	if err := cl.Get(t.Context(), types.NamespacedName{Name: egressNetworkPolicyName(service), Namespace: service.Namespace}, &netv1.NetworkPolicy{}); !apierrors.IsNotFound(err) {
		t.Fatalf("no general egress fallback may be created: %v", err)
	}
	for _, item := range []struct {
		object *netv1.NetworkPolicy
		before *netv1.NetworkPolicy
	}{{legacy, legacyBefore}, {foreignTracing, foreignBefore}} {
		after := &netv1.NetworkPolicy{}
		if err := cl.Get(t.Context(), client.ObjectKeyFromObject(item.object), after); err != nil {
			t.Fatalf("conflicting policy %s was deleted: %v", item.object.Name, err)
		}
		if !reflect.DeepEqual(after, item.before) {
			t.Fatalf("conflicting policy %s was mutated: before=%#v after=%#v", item.object.Name, item.before, after)
		}
	}
}

// TestReconcileLLMISVCNetworkPoliciesReportsBroadEgressCleanupFailures covers
// the ownership-checked removal of the broad egress policies earlier builds
// created: an API failure and a foreign policy both surface as reconcile
// errors instead of successful tracing establishment, and a later retry
// completes the cleanup.
func TestReconcileLLMISVCNetworkPoliciesReportsBroadEgressCleanupFailures(t *testing.T) {
	newService := func() *v1alpha2.LLMInferenceService {
		return &v1alpha2.LLMInferenceService{
			ObjectMeta: metav1.ObjectMeta{
				Name: "model", Namespace: "team-a", UID: "model-uid",
				Annotations: map[string]string{
					constants.EnableTracingEgressNetworkPolicyAnnotationKey: "true",
				},
			},
			Spec: v1alpha2.LLMInferenceServiceSpec{Tracing: &v1alpha2.TracingSpec{}},
		}
	}
	mainSelector := metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "model"}}
	seededDeployment := func(service *v1alpha2.LLMInferenceService) client.Object {
		return ownedDeployment(service, mainDeploymentName(service), mainSelector.DeepCopy(), mainSelector.MatchLabels)
	}

	t.Run("delete failure is reported and retried", func(t *testing.T) {
		service := newService()
		deleteErr := errors.New("transient broad egress Delete failure")
		failDelete := true
		cl := newNetworkPolicyTestClientWithInterceptor(t, interceptor.Funcs{
			Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
				if policy, ok := obj.(*netv1.NetworkPolicy); ok && policy.Name == allowAllNetworkPolicyName(service) && failDelete {
					failDelete = false
					return deleteErr
				}
				return c.Delete(ctx, obj, opts...)
			},
		}, seededDeployment(service), legacyAllowAllNetworkPolicy(service))
		r := &LLMISVCReconciler{Client: cl, EventRecorder: record.NewFakeRecorder(16)}

		err := reconcileLLMISVCNetworkPolicies(t.Context(), r, service, &Config{})
		if !errors.Is(err, deleteErr) {
			t.Fatalf("reconcile error = %v, want broad egress cleanup failure %v", err, deleteErr)
		}
		// Tracing was written, but the cleanup failure means it must not be
		// reported as successfully established.
		if err := cl.Get(t.Context(), types.NamespacedName{Name: tracingNetworkPolicyName(service), Namespace: service.Namespace}, &netv1.NetworkPolicy{}); err != nil {
			t.Fatalf("tracing policy should exist alongside the reported cleanup error: %v", err)
		}
		if err := cl.Get(t.Context(), types.NamespacedName{Name: allowAllNetworkPolicyName(service), Namespace: service.Namespace}, &netv1.NetworkPolicy{}); err != nil {
			t.Fatalf("broad egress policy should survive the failed delete: %v", err)
		}
		if err := cl.Get(t.Context(), types.NamespacedName{Name: egressNetworkPolicyName(service), Namespace: service.Namespace}, &netv1.NetworkPolicy{}); !apierrors.IsNotFound(err) {
			t.Fatalf("no general egress fallback may be created: %v", err)
		}

		if err := reconcileLLMISVCNetworkPolicies(t.Context(), r, service, &Config{}); err != nil {
			t.Fatalf("retry failed after the broad egress delete recovered: %v", err)
		}
		if err := cl.Get(t.Context(), types.NamespacedName{Name: allowAllNetworkPolicyName(service), Namespace: service.Namespace}, &netv1.NetworkPolicy{}); !apierrors.IsNotFound(err) {
			t.Fatalf("broad egress policy remained after the successful retry: %v", err)
		}
	})

	t.Run("foreign broad egress policy is reported and left in place", func(t *testing.T) {
		service := newService()
		foreignService := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{
			Name: "other-model", Namespace: service.Namespace, UID: "other-model-uid",
		}}
		foreignBroadEgress := legacyAllowAllNetworkPolicy(service).DeepCopy()
		foreignBroadEgress.OwnerReferences = []metav1.OwnerReference{
			*metav1.NewControllerRef(foreignService, v1alpha2.LLMInferenceServiceGVK),
		}
		cl := newNetworkPolicyTestClient(t, seededDeployment(service), foreignBroadEgress)
		before := &netv1.NetworkPolicy{}
		if err := cl.Get(t.Context(), client.ObjectKeyFromObject(foreignBroadEgress), before); err != nil {
			t.Fatal(err)
		}
		r := &LLMISVCReconciler{Client: cl, EventRecorder: record.NewFakeRecorder(16)}

		err := reconcileLLMISVCNetworkPolicies(t.Context(), r, service, &Config{})
		if !errors.Is(err, errNetworkPolicyOwnershipConflict) {
			t.Fatalf("reconcile error = %v, want broad egress ownership conflict", err)
		}
		// Ingress and tracing still reconcile independently of the conflict.
		for _, name := range []string{
			ingressNetworkPolicyName(service, networkPolicyModeDefault, networkPolicyComponentMain),
			tracingNetworkPolicyName(service),
		} {
			if err := cl.Get(t.Context(), types.NamespacedName{Name: name, Namespace: service.Namespace}, &netv1.NetworkPolicy{}); err != nil {
				t.Fatalf("policy %s should exist despite the broad egress conflict: %v", name, err)
			}
		}
		after := &netv1.NetworkPolicy{}
		if err := cl.Get(t.Context(), client.ObjectKeyFromObject(foreignBroadEgress), after); err != nil {
			t.Fatalf("foreign broad egress policy was deleted: %v", err)
		}
		if !reflect.DeepEqual(after, before) {
			t.Fatalf("foreign broad egress policy was mutated: before=%#v after=%#v", before, after)
		}
	})
}

// TestReconcileLLMISVCNetworkPoliciesRepairsTamperedAndDeletedPolicies covers
// the repair half of the lifecycle contract: a desired policy whose spec was
// tampered with is restored, and a desired policy deleted out of band is
// recreated, without disturbing the rest of the desired set.
func TestReconcileLLMISVCNetworkPoliciesRepairsTamperedAndDeletedPolicies(t *testing.T) {
	service := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{
		Name: "model", Namespace: "team-a", UID: "model-uid",
	}}
	mainSelector := metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "model"}}
	schedulerSelector := metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "model-scheduler"}}
	cl := newNetworkPolicyTestClient(t,
		ownedDeployment(service, mainDeploymentName(service), mainSelector.DeepCopy(), mainSelector.MatchLabels),
		ownedDeployment(service, schedulerDeploymentName(service), schedulerSelector.DeepCopy(), schedulerSelector.MatchLabels),
	)
	r := &LLMISVCReconciler{Client: cl, EventRecorder: record.NewFakeRecorder(16)}
	config := &Config{FeatureGates: FeatureGates{StrictNetworkPolicy: true}}
	if err := reconcileLLMISVCNetworkPolicies(t.Context(), r, service, config); err != nil {
		t.Fatalf("initial strict reconcile failed: %v", err)
	}

	// Tamper the main policy's selector and delete the scheduler policy out of
	// band, then reconcile again.
	strictMainName := ingressNetworkPolicyName(service, networkPolicyModeStrict, networkPolicyComponentMain)
	strictSchedulerName := ingressNetworkPolicyName(service, networkPolicyModeStrict, networkPolicyComponentScheduler)
	tampered := &netv1.NetworkPolicy{}
	if err := cl.Get(t.Context(), types.NamespacedName{Name: strictMainName, Namespace: service.Namespace}, tampered); err != nil {
		t.Fatal(err)
	}
	tampered.Spec.PodSelector = metav1.LabelSelector{MatchLabels: map[string]string{"tampered": "true"}}
	tampered.Spec.Ingress = nil
	if err := cl.Update(t.Context(), tampered); err != nil {
		t.Fatal(err)
	}
	if err := cl.Delete(t.Context(), &netv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{
		Name: strictSchedulerName, Namespace: service.Namespace,
	}}); err != nil {
		t.Fatal(err)
	}

	if err := reconcileLLMISVCNetworkPolicies(t.Context(), r, service, config); err != nil {
		t.Fatalf("repair reconcile failed: %v", err)
	}
	repairedMain := &netv1.NetworkPolicy{}
	if err := cl.Get(t.Context(), types.NamespacedName{Name: strictMainName, Namespace: service.Namespace}, repairedMain); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(repairedMain.Spec.PodSelector, mainSelector) {
		t.Fatalf("tampered selector was not repaired: got %#v want %#v", repairedMain.Spec.PodSelector, mainSelector)
	}
	if len(repairedMain.Spec.Ingress) == 0 {
		t.Fatalf("tampered ingress rules were not repaired: %#v", repairedMain.Spec.Ingress)
	}
	recreatedScheduler := &netv1.NetworkPolicy{}
	if err := cl.Get(t.Context(), types.NamespacedName{Name: strictSchedulerName, Namespace: service.Namespace}, recreatedScheduler); err != nil {
		t.Fatalf("deleted policy was not recreated: %v", err)
	}
	if !reflect.DeepEqual(recreatedScheduler.Spec.PodSelector, schedulerSelector) {
		t.Fatalf("recreated scheduler selector got %#v, want the stored Deployment selector", recreatedScheduler.Spec.PodSelector)
	}

	// The egress lifecycle repairs its per-component allow-all grants the same
	// way: tamper the main grant's selector, types, and rules and delete the
	// scheduler grant out of band, then reconcile again.
	egressMainName := allowAllEgressNetworkPolicyName(service, networkPolicyComponentMain)
	egressSchedulerName := allowAllEgressNetworkPolicyName(service, networkPolicyComponentScheduler)
	tamperedEgress := &netv1.NetworkPolicy{}
	if err := cl.Get(t.Context(), types.NamespacedName{Name: egressMainName, Namespace: service.Namespace}, tamperedEgress); err != nil {
		t.Fatal(err)
	}
	tamperedEgress.Spec.PodSelector = metav1.LabelSelector{MatchLabels: map[string]string{"tampered": "true"}}
	tamperedEgress.Spec.PolicyTypes = []netv1.PolicyType{netv1.PolicyTypeIngress, netv1.PolicyTypeEgress}
	tamperedEgress.Spec.Ingress = []netv1.NetworkPolicyIngressRule{{}}
	tamperedEgress.Spec.Egress = []netv1.NetworkPolicyEgressRule{{Ports: []netv1.NetworkPolicyPort{networkPolicyPort(corev1.ProtocolTCP, 443)}}}
	if err := cl.Update(t.Context(), tamperedEgress); err != nil {
		t.Fatal(err)
	}
	if err := cl.Delete(t.Context(), &netv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{
		Name: egressSchedulerName, Namespace: service.Namespace,
	}}); err != nil {
		t.Fatal(err)
	}

	if err := reconcileLLMISVCNetworkPolicies(t.Context(), r, service, config); err != nil {
		t.Fatalf("egress repair reconcile failed: %v", err)
	}
	repairedEgressMain := &netv1.NetworkPolicy{}
	if err := cl.Get(t.Context(), types.NamespacedName{Name: egressMainName, Namespace: service.Namespace}, repairedEgressMain); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(repairedEgressMain.Spec.PodSelector, mainSelector) {
		t.Fatalf("tampered egress selector was not repaired: got %#v want %#v", repairedEgressMain.Spec.PodSelector, mainSelector)
	}
	if !reflect.DeepEqual(repairedEgressMain.Spec.PolicyTypes, []netv1.PolicyType{netv1.PolicyTypeEgress}) {
		t.Fatalf("tampered egress types were not repaired: %v", repairedEgressMain.Spec.PolicyTypes)
	}
	if len(repairedEgressMain.Spec.Egress) != 1 || len(repairedEgressMain.Spec.Egress[0].To) != 0 || len(repairedEgressMain.Spec.Egress[0].Ports) != 0 {
		t.Fatalf("tampered egress rules were not repaired into an explicit allow-all rule: %#v", repairedEgressMain.Spec.Egress)
	}
	if repairedEgressMain.Spec.Ingress != nil {
		t.Fatalf("repaired egress grant must not create ingress rules: %#v", repairedEgressMain.Spec.Ingress)
	}
	recreatedEgressScheduler := &netv1.NetworkPolicy{}
	if err := cl.Get(t.Context(), types.NamespacedName{Name: egressSchedulerName, Namespace: service.Namespace}, recreatedEgressScheduler); err != nil {
		t.Fatalf("deleted egress grant was not recreated: %v", err)
	}
	if !reflect.DeepEqual(recreatedEgressScheduler.Spec.PodSelector, schedulerSelector) {
		t.Fatalf("recreated egress selector got %#v, want the stored Deployment selector", recreatedEgressScheduler.Spec.PodSelector)
	}
}

func TestNetworkPolicyMonitoringIngressPreservedRegardlessOfStrictGate(t *testing.T) {
	svc := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "models"}}
	inventory := networkPolicySelectorInventory{
		{
			Component: networkPolicyComponentMain, Class: networkPolicyRoleWorkload,
			Selector: metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "test"}},
		},
		{
			Component: networkPolicyComponentScheduler, Class: networkPolicyRoleScheduler,
			Selector: metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "test-scheduler"}},
		},
	}
	for _, tt := range []struct {
		name           string
		config         *Config
		wantMonitoring bool
	}{
		{name: "missing config", wantMonitoring: true},
		{name: "gate off", config: &Config{MonitoringNamespace: "from-dsci"}, wantMonitoring: true},
		{name: "gate on", config: &Config{FeatureGates: FeatureGates{StrictNetworkPolicy: true}, MonitoringNamespace: "from-dsci"}, wantMonitoring: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, policy := range expectedStrictIngressNetworkPolicies(svc, inventory, nil, nil, nil, tt.config) {
				found := false
				for _, rule := range policy.Spec.Ingress {
					for _, peer := range rule.From {
						if peer.NamespaceSelector != nil {
							ns := peer.NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"]
							if ns == "from-dsci" || (tt.config == nil && ns == defaultRHOAIMonitoringNamespace) {
								found = true
							}
							if !tt.wantMonitoring {
								t.Fatalf("gate off admits namespace %q", ns)
							}
						}
					}
				}
				if found != tt.wantMonitoring {
					t.Fatalf("policy %s monitoring peer = %v, want %v", policy.Name, found, tt.wantMonitoring)
				}
			}
		})
	}
}

// TestStrictNetworkPoliciesOmitMonitoringPeersWhenMonitoringIsDisabled covers
// the monitoring-off half of the strict traffic contract: with
// LLMISVC_MONITORING_DISABLED set, strict policies admit no Prometheus
// namespace while the workload-peer rules stay intact, so the switch removes
// only the monitoring exception instead of the whole policy set.
func TestStrictNetworkPoliciesOmitMonitoringPeersWhenMonitoringIsDisabled(t *testing.T) {
	monitoringDisabled = true
	defer func() { monitoringDisabled = false }()

	service := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "models"}}
	inventory := networkPolicySelectorInventory{
		{
			Component: networkPolicyComponentMain, Class: networkPolicyRoleWorkload,
			Selector: metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "test"}},
		},
		{
			Component: networkPolicyComponentScheduler, Class: networkPolicyRoleScheduler,
			Selector: metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "test-scheduler"}},
		},
	}

	policies := expectedStrictIngressNetworkPolicies(service, inventory, nil, nil, nil, nil)
	if len(policies) != 2 {
		t.Fatalf("got %d policies, want one per inventory entry", len(policies))
	}
	workloadPeerRuleFound := false
	for _, policy := range policies {
		for _, rule := range policy.Spec.Ingress {
			for _, peer := range rule.From {
				if peer.NamespaceSelector != nil {
					t.Fatalf("policy %s admits namespace %q while monitoring is disabled",
						policy.Name, peer.NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"])
				}
			}
			if len(rule.Ports) == 3 && rule.Ports[0].EndPort != nil {
				workloadPeerRuleFound = true
			}
		}
	}
	if !workloadPeerRuleFound {
		t.Fatalf("the workload-peer rule must survive a disabled monitoring switch: %#v", policies[0].Spec.Ingress)
	}
}
