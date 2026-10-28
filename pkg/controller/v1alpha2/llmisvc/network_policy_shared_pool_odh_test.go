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
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	igwapi "sigs.k8s.io/gateway-api-inference-extension/api/v1"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
)

func TestExpectedWorkloadNetworkPolicyAllowsSharedPoolSiblingScheduler(t *testing.T) {
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

	r := &LLMISVCReconciler{Client: selectorTestClient(t)}
	if err := r.Create(t.Context(), target); err != nil {
		t.Fatalf("failed to create target service: %v", err)
	}
	if err := r.Create(t.Context(), sibling); err != nil {
		t.Fatalf("failed to create sibling service: %v", err)
	}

	namespaces, ports, schedulerPeers, err := r.gatewayNamespacesForNetworkPolicy(t.Context(), target, "managed-gateway", nil)
	if err != nil {
		t.Fatalf("gateway peer discovery failed: %v", err)
	}
	policy := expectedWorkloadNetworkPolicy(target, namespaces, ports, schedulerPeers, nil)

	found := false
	for _, rule := range policy.Spec.Ingress {
		for _, source := range rule.From {
			if source.PodSelector == nil || source.PodSelector.MatchLabels[networkPolicyServiceIDLabel] != networkPolicyServiceID(sibling) {
				continue
			}
			if source.PodSelector.MatchLabels[networkPolicyRoleLabel] != networkPolicyRoleScheduler {
				t.Fatalf("shared-pool sibling source role got %q", source.PodSelector.MatchLabels[networkPolicyRoleLabel])
			}
			if len(rule.Ports) != 1 || rule.Ports[0].Port.IntValue() != 8007 {
				t.Fatalf("shared-pool sibling ports got %#v, want 8007", rule.Ports)
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("workload policy did not admit the protected sibling scheduler: %#v", policy.Spec.Ingress)
	}
}

func TestExpectedWorkloadNetworkPolicyUsesScopedExternalPoolFallback(t *testing.T) {
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

	r := &LLMISVCReconciler{Client: selectorTestClient(t)}
	if err := r.Create(t.Context(), service); err != nil {
		t.Fatalf("failed to create service: %v", err)
	}
	pool := &igwapi.InferencePool{
		ObjectMeta: metav1.ObjectMeta{Name: "external-pool", Namespace: "team-a"},
		Spec: igwapi.InferencePoolSpec{
			Selector:    igwapi.LabelSelector{MatchLabels: map[igwapi.LabelKey]igwapi.LabelValue{"pool": "external"}},
			TargetPorts: []igwapi.Port{{Number: 8111}},
		},
	}
	if err := r.Create(t.Context(), pool); err != nil {
		t.Fatalf("failed to create external pool: %v", err)
	}

	namespaces, ports, schedulerPeers, err := r.gatewayNamespacesForNetworkPolicy(t.Context(), service, "managed-gateway", nil)
	if err != nil {
		t.Fatalf("gateway peer discovery failed: %v", err)
	}
	policy := expectedWorkloadNetworkPolicy(service, namespaces, ports, schedulerPeers, nil)

	found := false
	for _, rule := range policy.Spec.Ingress {
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
		t.Fatalf("workload policy did not include scoped external pool fallback: %#v", policy.Spec.Ingress)
	}
}
