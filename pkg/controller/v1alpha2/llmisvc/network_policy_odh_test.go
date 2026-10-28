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
	netv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
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

func TestNetworkPolicyPodIdentityIsProtectedAndPerService(t *testing.T) {
	first := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{Name: "first", Namespace: "team-a"}}
	second := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{Name: "second", Namespace: "team-a"}}
	labels := map[string]string{
		networkPolicyServiceIDLabel: "user-value",
		networkPolicyRoleLabel:      "user-role",
	}
	applyNetworkPolicyLabels(labels, first, networkPolicyRoleWorkload)
	if labels[networkPolicyServiceIDLabel] != networkPolicyServiceID(first) || labels[networkPolicyRoleLabel] != networkPolicyRoleWorkload {
		t.Fatalf("protected labels were not applied: %#v", labels)
	}
	if networkPolicyServiceID(first) == networkPolicyServiceID(second) {
		t.Fatal("different services unexpectedly share a policy identity")
	}

	template := corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{
		networkPolicyRoleLabel: "user-role",
	}}}
	applyNetworkPolicyPodIdentity(&template, first, networkPolicyRoleScheduler)
	if got := template.Labels[networkPolicyRoleLabel]; got != networkPolicyRoleScheduler {
		t.Fatalf("role label got %q, want %q", got, networkPolicyRoleScheduler)
	}
}

func TestExpectedWorkloadNetworkPolicyUsesReferencedGatewayNamespacesAndProtectedSelector(t *testing.T) {
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
	policy := expectedWorkloadNetworkPolicy(service, gatewayPeerNamespaces(service, "managed-gateway"), []int32{defaultWorkloadPort, 8007}, nil, nil)
	if got, want := policy.Spec.PodSelector.MatchLabels[networkPolicyServiceIDLabel], networkPolicyServiceID(service); got != want {
		t.Fatalf("policy selector identity got %q, want %q", got, want)
	}
	if len(policy.Spec.Ingress) < 1 {
		t.Fatal("expected gateway ingress rule")
	}
	if got := gatewayPeerNamespaces(service, "managed-gateway"); !reflect.DeepEqual(got, []string{"team-a", "shared-gateway"}) {
		t.Fatalf("gateway namespaces got %v", got)
	}
	if len(policy.Spec.Ingress[0].Ports) != 2 || policy.Spec.Ingress[0].Ports[0].Port.IntValue() != int(defaultWorkloadPort) || policy.Spec.Ingress[0].Ports[1].Port.IntValue() != 8007 {
		t.Fatalf("gateway ports got %#v", policy.Spec.Ingress[0].Ports)
	}
	if len(policy.Spec.Ingress) < 2 {
		t.Fatalf("expected scheduler ingress rule, got %#v", policy.Spec.Ingress)
	}
	if got := policy.Spec.Ingress[1].Ports; len(got) != 2 || got[0].Port.IntValue() != int(defaultWorkloadPort) || got[1].Port.IntValue() != 8007 {
		t.Fatalf("scheduler target ports got %#v, want 8000 and 8007", got)
	}
	if got := policy.Spec.Ingress[len(policy.Spec.Ingress)-1].Ports; len(got) != 3 || got[0].EndPort == nil || *got[0].EndPort != maxNetworkPolicyPort {
		t.Fatalf("same-service peer ports got %#v, want explicit all-protocol ranges", got)
	}
}

func TestExpectedWorkloadNetworkPolicyAllowsSameServicePrefillDecodePeers(t *testing.T) {
	service := &v1alpha2.LLMInferenceService{
		ObjectMeta: metav1.ObjectMeta{Name: "prefill-decode", Namespace: "team-a"},
		Spec:       v1alpha2.LLMInferenceServiceSpec{Prefill: &v1alpha2.WorkloadSpec{}},
	}
	policy := expectedWorkloadNetworkPolicy(service, nil, []int32{defaultWorkloadPort}, nil, nil)
	peer := policy.Spec.Ingress[len(policy.Spec.Ingress)-1]
	if len(peer.From) != 1 || peer.From[0].PodSelector == nil {
		t.Fatalf("same-service peer source got %#v", peer.From)
	}
	if got := peer.From[0].PodSelector.MatchLabels[networkPolicyServiceIDLabel]; got != networkPolicyServiceID(service) {
		t.Fatalf("same-service peer identity got %q, want %q", got, networkPolicyServiceID(service))
	}
	if len(peer.Ports) != 3 {
		t.Fatalf("same-service peer ports got %#v, want all protocols", peer.Ports)
	}
}

func TestExpectedSchedulerNetworkPolicyUsesTemplatePorts(t *testing.T) {
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
	policy := expectedSchedulerNetworkPolicy(service, gatewayPeerNamespaces(service, "managed-gateway"), nil)
	if len(policy.Spec.Ingress) < 3 {
		t.Fatalf("expected gateway, monitoring, and workload ingress rules, got %d", len(policy.Spec.Ingress))
	}
	if got := policy.Spec.Ingress[0].Ports[0].Port.IntValue(); got != 9102 {
		t.Fatalf("gateway port got %d, want 9102", got)
	}
	if got := policy.Spec.Ingress[1].Ports[0].Port.IntValue(); got != 9190 {
		t.Fatalf("metrics port got %d, want 9190", got)
	}
	if got := policy.Spec.Ingress[2].Ports[0].Port.IntValue(); got != 5657 {
		t.Fatalf("ZMQ port got %d, want 5657", got)
	}
}

func TestExpectedDefaultEgressNetworkPolicyIsExplicitAllowAll(t *testing.T) {
	service := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{Name: "model", Namespace: "team-a"}}
	policy := expectedDefaultEgressNetworkPolicy(service)
	if !reflect.DeepEqual(policy.Spec.PolicyTypes, []netv1.PolicyType{netv1.PolicyTypeEgress}) {
		t.Fatalf("policy types got %v", policy.Spec.PolicyTypes)
	}
	if len(policy.Spec.Egress) != 1 || len(policy.Spec.Egress[0].To) != 0 || len(policy.Spec.Egress[0].Ports) != 0 {
		t.Fatalf("egress is not explicit allow-all: %#v", policy.Spec.Egress)
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
	policy := expectedDefaultEgressNetworkPolicy(service)

	scheme := runtime.NewScheme()
	if err := netv1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add NetworkPolicy scheme: %v", err)
	}
	client := clientfake.NewClientBuilder().WithScheme(scheme).WithObjects(policy).Build()
	r := &LLMISVCReconciler{Client: client}
	if err := cleanupLLMISVCNetworkPolicies(t.Context(), r, service); err != nil {
		t.Fatalf("cleanup failed: %v", err)
	}

	remaining := &netv1.NetworkPolicy{}
	err := client.Get(t.Context(), types.NamespacedName{Name: policy.Name, Namespace: policy.Namespace}, remaining)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("owned policy remained after cleanup: %v", err)
	}
}

func TestNetworkPolicyMonitoringIngressPreservedWithLegacyGateOff(t *testing.T) {
	svc := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "models"}}
	for _, tt := range []struct {
		name           string
		config         *Config
		wantMonitoring bool
	}{
		{name: "missing config", wantMonitoring: true},
		{name: "gate off", config: &Config{MonitoringNamespace: "from-dsci"}, wantMonitoring: true},
		{name: "gate on", config: &Config{FeatureGates: FeatureGates{MonitoringIngressNetworkPolicy: true}, MonitoringNamespace: "from-dsci"}, wantMonitoring: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, policy := range []*netv1.NetworkPolicy{
				expectedWorkloadNetworkPolicy(svc, nil, nil, nil, tt.config),
				expectedSchedulerNetworkPolicy(svc, nil, tt.config),
			} {
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
