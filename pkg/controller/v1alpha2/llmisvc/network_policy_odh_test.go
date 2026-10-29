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
	"fmt"
	"reflect"
	"strconv"
	"testing"

	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"

	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

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

func TestExpectedAllowAllNetworkPolicySelectsAllServicePodsAndAllowsAllTraffic(t *testing.T) {
	service := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{Name: "model", Namespace: "team-a"}}
	policy := expectedAllowAllNetworkPolicy(service)
	if policy.Name != allowAllNetworkPolicyName(service) {
		t.Fatalf("policy name got %q, want %q", policy.Name, allowAllNetworkPolicyName(service))
	}
	if !reflect.DeepEqual(policy.Spec.PolicyTypes, []netv1.PolicyType{netv1.PolicyTypeIngress, netv1.PolicyTypeEgress}) {
		t.Fatalf("policy types got %v", policy.Spec.PolicyTypes)
	}
	wantSelector := metav1.LabelSelector{MatchLabels: map[string]string{
		networkPolicyServiceIDLabel: networkPolicyServiceID(service),
	}}
	if !reflect.DeepEqual(policy.Spec.PodSelector, wantSelector) {
		t.Fatalf("pod selector got %#v, want role-free service identity selector", policy.Spec.PodSelector)
	}
	if len(policy.Spec.Ingress) != 1 || len(policy.Spec.Ingress[0].From) != 0 || len(policy.Spec.Ingress[0].Ports) != 0 {
		t.Fatalf("ingress is not explicit allow-all: %#v", policy.Spec.Ingress)
	}
	if len(policy.Spec.Egress) != 1 || len(policy.Spec.Egress[0].To) != 0 || len(policy.Spec.Egress[0].Ports) != 0 {
		t.Fatalf("egress is not explicit allow-all: %#v", policy.Spec.Egress)
	}
}

func newNetworkPolicyTestClient(t *testing.T, objects ...client.Object) client.WithWatch {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := netv1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add NetworkPolicy scheme: %v", err)
	}
	if err := v1alpha2.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add LLMInferenceService scheme: %v", err)
	}
	return clientfake.NewClientBuilder().WithScheme(scheme).WithRESTMapper(networkPolicyTestRESTMapper()).WithObjects(objects...).Build()
}

func networkPolicyTestRESTMapper() meta.RESTMapper {
	mapper := meta.NewDefaultRESTMapper(nil)
	mapper.Add(netv1.SchemeGroupVersion.WithKind("NetworkPolicy"), meta.RESTScopeNamespace)
	return mapper
}

func TestReconcileLLMISVCNetworkPoliciesDefaultModeCreatesAllowAllAndRemovesStrict(t *testing.T) {
	service := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{
		Name:      "model",
		Namespace: "team-a",
		UID:       "model-uid",
	}}
	// Seed the strict-mode policies plus the legacy policy to simulate a
	// migration from strict mode (and from the RHAI-4088 gate) to default mode.
	seeded := []client.Object{
		expectedWorkloadNetworkPolicy(service, nil, nil, nil, nil),
		expectedSchedulerNetworkPolicy(service, nil, nil),
		expectedTokenizerNetworkPolicy(service),
		expectedDefaultEgressNetworkPolicy(service),
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

	allowAll := &netv1.NetworkPolicy{}
	if err := cl.Get(t.Context(), types.NamespacedName{Name: allowAllNetworkPolicyName(service), Namespace: service.Namespace}, allowAll); err != nil {
		t.Fatalf("allow-all NetworkPolicy was not created: %v", err)
	}
	if !metav1.IsControlledBy(allowAll, service) {
		t.Fatalf("allow-all NetworkPolicy is not controlled by the service")
	}
	for _, name := range []string{
		workloadNetworkPolicyName(service),
		schedulerNetworkPolicyName(service),
		tokenizerNetworkPolicyName(service),
		egressNetworkPolicyName(service),
		legacyMonitoringNetworkPolicyName(service),
	} {
		err := cl.Get(t.Context(), types.NamespacedName{Name: name, Namespace: service.Namespace}, &netv1.NetworkPolicy{})
		if !apierrors.IsNotFound(err) {
			t.Fatalf("policy %s remained after default-mode reconcile: %v", name, err)
		}
	}
}

func TestReconcileLLMISVCNetworkPoliciesStrictModeReplacesAllowAll(t *testing.T) {
	service := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{
		Name:      "model",
		Namespace: "team-a",
		UID:       "model-uid",
	}}
	cl := newNetworkPolicyTestClient(t, expectedAllowAllNetworkPolicy(service))
	r := &LLMISVCReconciler{Client: cl, EventRecorder: record.NewFakeRecorder(16)}

	config := &Config{FeatureGates: FeatureGates{StrictNetworkPolicy: true}}
	if err := reconcileLLMISVCNetworkPolicies(t.Context(), r, service, config); err != nil {
		t.Fatalf("strict-mode reconcile failed: %v", err)
	}

	for _, name := range []string{
		workloadNetworkPolicyName(service),
		schedulerNetworkPolicyName(service),
		tokenizerNetworkPolicyName(service),
		egressNetworkPolicyName(service),
	} {
		if err := cl.Get(t.Context(), types.NamespacedName{Name: name, Namespace: service.Namespace}, &netv1.NetworkPolicy{}); err != nil {
			t.Fatalf("strict policy %s was not created: %v", name, err)
		}
	}
	err := cl.Get(t.Context(), types.NamespacedName{Name: allowAllNetworkPolicyName(service), Namespace: service.Namespace}, &netv1.NetworkPolicy{})
	if !apierrors.IsNotFound(err) {
		t.Fatalf("allow-all NetworkPolicy remained after strict-mode reconcile: %v", err)
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
			cl := newNetworkPolicyTestClient(t, defaultDeny)
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
					allowAll := &netv1.NetworkPolicy{}
					if err := cl.Get(t.Context(), client.ObjectKeyFromObject(expectedAllowAllNetworkPolicy(service)), allowAll); err != nil {
						t.Fatalf("tracing=%t: missing ingress grant: %v", tracing, err)
					}
					want := expectedAllowAllNetworkPolicy(service)
					if tracing {
						want = expectedAllowAllIngressNetworkPolicy(service)
					}
					if !reflect.DeepEqual(allowAll.Spec, want.Spec) {
						t.Fatalf("tracing=%t: unexpected default policy: %#v", tracing, allowAll.Spec)
					}

					policies := &netv1.NetworkPolicyList{}
					if err := cl.List(t.Context(), policies, client.InNamespace(service.Namespace)); err != nil {
						t.Fatal(err)
					}
					wantCount := 2 // namespace default-deny + service allow-all
					if tracing {
						wantCount++ // restricted tracing egress
						if err := cl.Get(t.Context(), client.ObjectKey{Name: tracingNetworkPolicyName(service), Namespace: service.Namespace}, &netv1.NetworkPolicy{}); err != nil {
							t.Fatal(err)
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
	strict := expectedWorkloadNetworkPolicy(service, nil, nil, nil, nil)
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

func TestNetworkPolicyMonitoringIngressPreservedRegardlessOfStrictGate(t *testing.T) {
	svc := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "models"}}
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
