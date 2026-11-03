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
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	igwapi "sigs.k8s.io/gateway-api-inference-extension/api/v1"

	igwapiv1alpha2 "github.com/kserve/kserve/pkg/apis/gie/v1alpha2pool"
	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
	"github.com/kserve/kserve/pkg/constants"
)

func TestExpectedStrictWorkloadNetworkPolicyAllowsSharedPoolSiblingScheduler(t *testing.T) {
	target := &v1alpha2.LLMInferenceService{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "routerless-target",
			Namespace: "team-a",
			UID:       types.UID("target"),
		},
		Spec: v1alpha2.LLMInferenceServiceSpec{WorkloadSpec: v1alpha2.WorkloadSpec{Labels: map[string]string{"pool": "shared"}}},
	}
	sibling := &v1alpha2.LLMInferenceService{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pool-owner",
			Namespace: "team-a",
			UID:       types.UID("owner"),
		},
		Spec: v1alpha2.LLMInferenceServiceSpec{
			Router: &v1alpha2.RouterSpec{
				Gateway: &v1alpha2.GatewaySpec{Refs: []v1alpha2.GatewayObjectReference{{
					UntypedObjectReference: v1alpha2.UntypedObjectReference{
						Name:      "shared-gateway",
						Namespace: "gateway-ns",
					},
				}}},
				Scheduler: &v1alpha2.SchedulerSpec{
					Template: &corev1.PodSpec{},
					Pool: &v1alpha2.InferencePoolSpec{Spec: &igwapi.InferencePoolSpec{
						Selector:    igwapi.LabelSelector{MatchLabels: map[igwapi.LabelKey]igwapi.LabelValue{"pool": "shared"}},
						TargetPorts: []igwapi.Port{{Number: 8007}},
					}},
				},
			},
		},
	}
	siblingSchedulerSelector := metav1.LabelSelector{MatchLabels: map[string]string{
		"app.kubernetes.io/component": "llminferenceservice-router-scheduler",
		"app.kubernetes.io/name":      "pool-owner",
	}}

	r := &LLMISVCReconciler{Client: networkPolicySelectorTestClient(t,
		target,
		sibling,
		ownedDeployment(sibling, schedulerDeploymentName(sibling), siblingSchedulerSelector.DeepCopy(), siblingSchedulerSelector.MatchLabels),
	)}

	namespaces, ports, schedulerPeers, err := r.gatewayNamespacesForNetworkPolicy(t.Context(), target, "managed-gateway", nil)
	if err != nil {
		t.Fatalf("gateway peer discovery failed: %v", err)
	}

	found := false
	for _, peer := range schedulerPeers {
		if peer.peer.PodSelector == nil || !reflect.DeepEqual(*peer.peer.PodSelector, siblingSchedulerSelector) {
			continue
		}
		if len(peer.ports) != 1 || peer.ports[0] != 8007 {
			t.Fatalf("shared-pool sibling ports got %#v, want 8007", peer.ports)
		}
		found = true
	}
	if !found {
		t.Fatalf("scheduler peers did not admit the sibling's stored scheduler Deployment selector: %#v", schedulerPeers)
	}

	targetSelector := metav1.LabelSelector{MatchLabels: map[string]string{"pool": "shared"}}
	inventory := networkPolicySelectorInventory{
		{Component: networkPolicyComponentMain, Class: networkPolicyRoleWorkload, Selector: targetSelector},
	}
	policies := expectedStrictIngressNetworkPolicies(target, inventory, namespaces, ports, schedulerPeers, nil)
	if len(policies) != 1 {
		t.Fatalf("got %d policies, want one per workload inventory entry", len(policies))
	}
	ruleFound := false
	for _, rule := range policies[0].Spec.Ingress {
		for _, source := range rule.From {
			if source.PodSelector == nil || !reflect.DeepEqual(*source.PodSelector, siblingSchedulerSelector) {
				continue
			}
			if len(rule.Ports) != 1 || rule.Ports[0].Port.IntValue() != 8007 {
				t.Fatalf("shared-pool sibling ports got %#v, want 8007", rule.Ports)
			}
			ruleFound = true
		}
	}
	if !ruleFound {
		t.Fatalf("workload policy did not admit the shared-pool sibling scheduler: %#v", policies[0].Spec.Ingress)
	}
}

func TestGatewayNamespacesForNetworkPolicySkipsStoppedSiblingScheduler(t *testing.T) {
	target := &v1alpha2.LLMInferenceService{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "routerless-target",
			Namespace: "team-a",
			UID:       types.UID("target"),
		},
		Spec: v1alpha2.LLMInferenceServiceSpec{
			WorkloadSpec: v1alpha2.WorkloadSpec{Labels: map[string]string{"pool": "shared"}},
			Router: &v1alpha2.RouterSpec{Scheduler: &v1alpha2.SchedulerSpec{
				Pool: &v1alpha2.InferencePoolSpec{Ref: &corev1.LocalObjectReference{Name: "ext-pool"}},
			}},
		},
	}
	// A force-stopped shared-pool sibling: its pool selector still matches the
	// target's workload labels, but its managed scheduler Deployment is
	// deliberately absent while it is stopped.
	stoppedSibling := &v1alpha2.LLMInferenceService{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "stopped-pool-owner",
			Namespace:   "team-a",
			UID:         types.UID("stopped-owner"),
			Annotations: map[string]string{constants.StopAnnotationKey: "true"},
		},
		Spec: v1alpha2.LLMInferenceServiceSpec{
			Router: &v1alpha2.RouterSpec{
				Scheduler: &v1alpha2.SchedulerSpec{
					Template: &corev1.PodSpec{},
					Pool: &v1alpha2.InferencePoolSpec{Spec: &igwapi.InferencePoolSpec{
						Selector:    igwapi.LabelSelector{MatchLabels: map[igwapi.LabelKey]igwapi.LabelValue{"pool": "shared"}},
						TargetPorts: []igwapi.Port{{Number: 8007}},
					}},
				},
			},
		},
	}

	// The externally referenced pool exists and selects the same shared pool,
	// so the target's routing resolves through it instead of an unregistered
	// optional v1alpha2 pool API.
	extPool := &igwapi.InferencePool{
		ObjectMeta: metav1.ObjectMeta{Name: "ext-pool", Namespace: "team-a"},
		Spec: igwapi.InferencePoolSpec{
			Selector:    igwapi.LabelSelector{MatchLabels: map[igwapi.LabelKey]igwapi.LabelValue{"pool": "shared"}},
			TargetPorts: []igwapi.Port{{Number: 8007}},
		},
	}

	r := &LLMISVCReconciler{Client: networkPolicySelectorTestClient(t, target, stoppedSibling, extPool)}

	_, _, schedulerPeers, err := r.gatewayNamespacesForNetworkPolicy(t.Context(), target, "managed-gateway", nil)
	if err != nil {
		t.Fatalf("a stopped shared-pool sibling must not fail peer discovery: %v", err)
	}
	for _, peer := range schedulerPeers {
		if peer.peer.PodSelector == nil {
			t.Fatalf("scheduler peer with an empty PodSelector would admit every source: %#v", peer)
		}
	}
	// The stopped sibling still counts as the matched scheduler of the shared
	// pool, so the same-namespace third-party EPP fallback must not widen the
	// source while its scheduler Deployment is absent.
	if len(schedulerPeers) != 0 {
		t.Fatalf("a stopped sibling must yield no scheduler peer and no namespace fallback, got %#v", schedulerPeers)
	}
}

func TestExpectedStrictWorkloadNetworkPolicyUsesScopedExternalPoolFallback(t *testing.T) {
	service := &v1alpha2.LLMInferenceService{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "external-pool-target",
			Namespace: "team-a",
			UID:       types.UID("external-target"),
		},
		Spec: v1alpha2.LLMInferenceServiceSpec{
			WorkloadSpec: v1alpha2.WorkloadSpec{Labels: map[string]string{"pool": "external"}},
			Router: &v1alpha2.RouterSpec{Scheduler: &v1alpha2.SchedulerSpec{
				Pool: &v1alpha2.InferencePoolSpec{Ref: &corev1.LocalObjectReference{Name: "external-pool"}},
			}},
		},
	}
	pool := &igwapi.InferencePool{
		ObjectMeta: metav1.ObjectMeta{Name: "external-pool", Namespace: "team-a"},
		Spec: igwapi.InferencePoolSpec{
			Selector:    igwapi.LabelSelector{MatchLabels: map[igwapi.LabelKey]igwapi.LabelValue{"pool": "external"}},
			TargetPorts: []igwapi.Port{{Number: 8111}},
		},
	}

	r := &LLMISVCReconciler{Client: networkPolicySelectorTestClient(t, service, pool)}

	namespaces, ports, schedulerPeers, err := r.gatewayNamespacesForNetworkPolicy(t.Context(), service, "managed-gateway", nil)
	if err != nil {
		t.Fatalf("gateway peer discovery failed: %v", err)
	}

	targetSelector := metav1.LabelSelector{MatchLabels: map[string]string{"pool": "external"}}
	inventory := networkPolicySelectorInventory{
		{Component: networkPolicyComponentMain, Class: networkPolicyRoleWorkload, Selector: targetSelector},
	}
	policies := expectedStrictIngressNetworkPolicies(service, inventory, namespaces, ports, schedulerPeers, nil)
	if len(policies) != 1 {
		t.Fatalf("got %d policies, want one per workload inventory entry", len(policies))
	}

	found := false
	for _, rule := range policies[0].Spec.Ingress {
		for _, source := range rule.From {
			if source.NamespaceSelector == nil || source.NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] != service.Namespace {
				continue
			}
			if len(rule.Ports) != 1 || rule.Ports[0].Port.IntValue() != 8111 {
				t.Fatalf("external pool fallback ports got %#v, want 8111", rule.Ports)
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("workload policy did not include scoped external pool fallback: %#v", policies[0].Spec.Ingress)
	}
}

// TestGatewayNamespacesForNetworkPolicyResolvesV1Alpha2Pool covers the
// v1alpha2 InferencePool fallback of shared-pool discovery: when the pool
// referenced by the service exists only in the legacy x-k8s.io API, its
// selector and single target port resolve through that API, and workload-peer
// admission is scoped to the v1alpha2 pool's target port.
func TestGatewayNamespacesForNetworkPolicyResolvesV1Alpha2Pool(t *testing.T) {
	service := &v1alpha2.LLMInferenceService{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "legacy-pool-target",
			Namespace: "team-a",
			UID:       types.UID("legacy-target"),
		},
		Spec: v1alpha2.LLMInferenceServiceSpec{
			WorkloadSpec: v1alpha2.WorkloadSpec{Labels: map[string]string{"pool": "legacy"}},
			Router: &v1alpha2.RouterSpec{Scheduler: &v1alpha2.SchedulerSpec{
				Pool: &v1alpha2.InferencePoolSpec{Ref: &corev1.LocalObjectReference{Name: "legacy-pool"}},
			}},
		},
	}
	legacyPool := &igwapiv1alpha2.InferencePool{
		ObjectMeta: metav1.ObjectMeta{Name: "legacy-pool", Namespace: "team-a"},
		Spec: igwapiv1alpha2.InferencePoolSpec{
			Selector:         map[igwapiv1alpha2.LabelKey]igwapiv1alpha2.LabelValue{"pool": "legacy"},
			TargetPortNumber: 8123,
		},
	}

	r := &LLMISVCReconciler{Client: networkPolicySelectorTestClient(t, service, legacyPool)}

	_, ports, _, err := r.gatewayNamespacesForNetworkPolicy(t.Context(), service, "managed-gateway", nil)
	if err != nil {
		t.Fatalf("gateway peer discovery failed: %v", err)
	}
	if len(ports) != 1 || ports[0] != 8123 {
		t.Fatalf("v1alpha2 pool ports got %#v, want the pool's target port 8123", ports)
	}
}
