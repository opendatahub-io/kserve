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
	return scheme
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

func TestDeleteOwnedNetworkPolicyHandlesMissingAndOwnedPolicies(t *testing.T) {
	service := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{
		Name: "model", Namespace: "team-a", UID: "model-uid",
	}}
	expected := expectedDefaultEgressNetworkPolicy(service)

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

	ownerless := expectedDefaultEgressNetworkPolicy(service).DeepCopy()
	ownerless.OwnerReferences = nil
	foreign := expectedDefaultEgressNetworkPolicy(service).DeepCopy()
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

func TestCleanupNetworkPoliciesContinuesPastFirstAndMiddleOwnershipConflicts(t *testing.T) {
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
	ownerlessScheduler := expectedSchedulerNetworkPolicy(service, nil, nil)
	ownerlessScheduler.OwnerReferences = nil
	owned := []*netv1.NetworkPolicy{
		expectedWorkloadNetworkPolicy(service, nil, nil, nil, nil),
		expectedTokenizerNetworkPolicy(service),
		expectedDefaultEgressNetworkPolicy(service),
		expectedAllowAllNetworkPolicy(service),
		expectedTracingNetworkPolicy(service, netv1.NetworkPolicyPeer{}, 0, false),
	}
	objects := make([]client.Object, 0, 2+len(owned))
	objects = append(objects, foreignLegacy, ownerlessScheduler)
	for _, policy := range owned {
		objects = append(objects, policy)
	}
	cl := newNetworkPolicyTestClient(t, objects...)

	conflicts := []*netv1.NetworkPolicy{foreignLegacy, ownerlessScheduler}
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

func TestReconcileLLMISVCNetworkPoliciesAbortsOnLegacyAPIErrors(t *testing.T) {
	service := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{
		Name: "model", Namespace: "team-a", UID: "model-uid",
	}}
	legacy := &netv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{
		Name: legacyMonitoringNetworkPolicyName(service), Namespace: service.Namespace,
		OwnerReferences: []metav1.OwnerReference{
			*metav1.NewControllerRef(service, v1alpha2.LLMInferenceServiceGVK),
		},
	}}

	for _, tt := range []struct {
		name      string
		getErr    error
		deleteErr error
	}{
		{name: "Get failure", getErr: errors.New("legacy policy Get failed")},
		{name: "Delete failure", deleteErr: errors.New("legacy policy Delete failed")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			funcs := interceptor.Funcs{}
			if tt.getErr != nil {
				funcs.Get = func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if _, ok := obj.(*netv1.NetworkPolicy); ok && key.Name == legacy.Name {
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
			cl := newNetworkPolicyTestClientWithInterceptor(t, funcs, legacy)
			r := &LLMISVCReconciler{Client: cl, EventRecorder: record.NewFakeRecorder(16)}

			err := reconcileLLMISVCNetworkPolicies(t.Context(), r, service, &Config{})
			wantErr := tt.getErr
			if wantErr == nil {
				wantErr = tt.deleteErr
			}
			if !errors.Is(err, wantErr) {
				t.Fatalf("reconcile error = %v, want immediate legacy API error %v", err, wantErr)
			}
			if errors.Is(err, errNetworkPolicyOwnershipConflict) {
				t.Fatalf("real API error was misclassified as an ownership conflict: %v", err)
			}
			if err := cl.Get(t.Context(), client.ObjectKeyFromObject(expectedAllowAllNetworkPolicy(service)), &netv1.NetworkPolicy{}); !apierrors.IsNotFound(err) {
				t.Fatalf("baseline policy was created despite legacy API error: %v", err)
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
			cl := newNetworkPolicyTestClient(t, legacy)
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

			baselineNames := []string{allowAllNetworkPolicyName(service)}
			if strict {
				baselineNames = []string{
					workloadNetworkPolicyName(service), schedulerNetworkPolicyName(service),
					tokenizerNetworkPolicyName(service), egressNetworkPolicyName(service),
				}
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

func TestReconcileLLMISVCNetworkPoliciesJoinsDeferredLegacyConflictWithLaterError(t *testing.T) {
	service := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{
		Name: "model", Namespace: "team-a", UID: "model-uid",
	}}
	legacy := &netv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{
		Name: legacyMonitoringNetworkPolicyName(service), Namespace: service.Namespace,
	}}
	createErr := errors.New("baseline NetworkPolicy Create failed")
	cl := newNetworkPolicyTestClientWithInterceptor(t, interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if policy, ok := obj.(*netv1.NetworkPolicy); ok && policy.Name == allowAllNetworkPolicyName(service) {
				return createErr
			}
			return c.Create(ctx, obj, opts...)
		},
	}, legacy)
	r := &LLMISVCReconciler{Client: cl, EventRecorder: record.NewFakeRecorder(16)}

	err := reconcileLLMISVCNetworkPolicies(t.Context(), r, service, &Config{})
	if !errors.Is(err, errNetworkPolicyOwnershipConflict) {
		t.Fatalf("reconcile error %v does not contain deferred ownership conflict", err)
	}
	if !errors.Is(err, createErr) {
		t.Fatalf("reconcile error %v does not contain later baseline failure", err)
	}
}

func TestReconcileLLMISVCNetworkPoliciesDoesNotAdoptActivePolicyConflict(t *testing.T) {
	service := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{
		Name: "model", Namespace: "team-a", UID: "model-uid",
	}}
	foreignService := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{
		Name: "other-model", Namespace: service.Namespace, UID: "other-model-uid",
	}}
	foreignActivePolicy := expectedAllowAllNetworkPolicy(service).DeepCopy()
	foreignActivePolicy.OwnerReferences = []metav1.OwnerReference{
		*metav1.NewControllerRef(foreignService, v1alpha2.LLMInferenceServiceGVK),
	}
	cl := newNetworkPolicyTestClient(t, foreignActivePolicy)
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

func TestReconcileTracingConflictFailsClosedAndPreservesIngress(t *testing.T) {
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
	foreignService := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{
		Name: "other-model", Namespace: service.Namespace, UID: "other-model-uid",
	}}
	foreignTracing := expectedTracingNetworkPolicy(service, netv1.NetworkPolicyPeer{}, 0, false)
	foreignTracing.OwnerReferences = []metav1.OwnerReference{
		*metav1.NewControllerRef(foreignService, v1alpha2.LLMInferenceServiceGVK),
	}
	// This foreign additive grant is deliberately retained; the test verifies
	// error visibility and the controller's fallback, not traffic isolation.
	foreignTracing.Spec.Egress = []netv1.NetworkPolicyEgressRule{{}}
	objects := []client.Object{legacy, expectedAllowAllNetworkPolicy(service), foreignTracing}
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
	if !errors.Is(err, errNetworkPolicyOwnershipConflict) {
		t.Fatalf("reconcile error %v does not contain deferred legacy conflict", err)
	}
	var tracingConflict *tracingNetworkPolicyNotOwnedError
	if !errors.As(err, &tracingConflict) {
		t.Fatalf("reconcile error %v does not contain active tracing ownership conflict", err)
	}

	allowIngress := &netv1.NetworkPolicy{}
	if err := cl.Get(t.Context(), client.ObjectKeyFromObject(expectedAllowAllNetworkPolicy(service)), allowIngress); err != nil {
		t.Fatalf("ingress policy was not preserved: %v", err)
	}
	if !reflect.DeepEqual(allowIngress.Spec, expectedAllowAllIngressNetworkPolicy(service).Spec) {
		t.Fatalf("tracing conflict did not preserve default-mode ingress safely: %#v", allowIngress.Spec)
	}
	denyEgress := &netv1.NetworkPolicy{}
	if err := cl.Get(t.Context(), client.ObjectKey{Name: egressNetworkPolicyName(service), Namespace: service.Namespace}, denyEgress); err != nil {
		t.Fatalf("fail-closed egress policy was not installed: %v", err)
	}
	if len(denyEgress.Spec.Egress) != 0 || !reflect.DeepEqual(denyEgress.Spec.PolicyTypes, []netv1.PolicyType{netv1.PolicyTypeEgress}) {
		t.Fatalf("egress fallback is not explicit deny-all: %#v", denyEgress.Spec)
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
