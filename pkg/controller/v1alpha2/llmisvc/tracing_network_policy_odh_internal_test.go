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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"knative.dev/pkg/kmeta"
	"knative.dev/pkg/network"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
	"github.com/kserve/kserve/pkg/constants"
)

func TestOtlpPeerForEndpoint(t *testing.T) {
	tests := []struct {
		name      string
		endpoint  string
		namespace string
		wantNS    string
		wantPort  int32
		wantPeer  bool
	}{
		{name: "same namespace", endpoint: "http://otel-collector:4317", namespace: "team-a"},
		{name: "cross namespace", endpoint: "http://jaeger.observability.svc." + network.GetClusterDomainName() + ":4317", namespace: "team-a", wantNS: "observability", wantPort: 4317, wantPeer: true},
		{name: "cross namespace with search path", endpoint: "http://jaeger.observability:4317", namespace: "team-a", wantNS: "observability", wantPort: 4317, wantPeer: true},
		{name: "custom port", endpoint: "http://jaeger.observability.svc:4318", namespace: "team-a", wantNS: "observability", wantPort: 4318, wantPeer: true},
		{name: "trailing dot", endpoint: "http://jaeger.observability.svc." + network.GetClusterDomainName() + ".:4317", namespace: "team-a", wantNS: "observability", wantPort: 4317, wantPeer: true},
		{name: "unparseable", endpoint: "not a URL", namespace: "team-a"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			peer, port, hasPeer := otlpPeerForEndpoint(tt.endpoint, tt.namespace)
			if hasPeer != tt.wantPeer || port != tt.wantPort {
				t.Fatalf("got peer=%v port=%d, want peer=%v port=%d", hasPeer, port, tt.wantPeer, tt.wantPort)
			}
			if hasPeer && peer.NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] != tt.wantNS {
				t.Fatalf("namespace got %q want %q", peer.NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"], tt.wantNS)
			}
		})
	}
}

func TestTracingNetworkPolicyNameIsPerService(t *testing.T) {
	llmSvc := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{Name: "svc-a", Namespace: "team-a"}}
	if got, want := tracingNetworkPolicyName(llmSvc), kmeta.ChildName("svc-a", tracingNetworkPolicySuffix); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestExpectedTracingNetworkPolicy(t *testing.T) {
	llmSvc := &v1alpha2.LLMInferenceService{
		ObjectMeta: metav1.ObjectMeta{Name: "svc-a", Namespace: "team-a"},
		Spec: v1alpha2.LLMInferenceServiceSpec{Tracing: &v1alpha2.TracingSpec{
			ExporterEndpoint: ptr.To("http://jaeger.observability.svc.cluster.local:4318"),
		}},
	}

	np := expectedTracingNetworkPolicy(llmSvc)
	if got, want := np.Name, "svc-a-otlp-egress"; got != want {
		t.Fatalf("name got %q want %q", got, want)
	}
	if got, want := np.Namespace, "team-a"; got != want {
		t.Fatalf("namespace got %q want %q", got, want)
	}
	if got, want := np.Labels[constants.KubernetesComponentLabelKey], tracingNPComponentLabel; got != want {
		t.Fatalf("component label got %q want %q", got, want)
	}
	if got := np.OwnerReferences; len(got) != 1 || got[0].Name != llmSvc.Name || got[0].Controller == nil || !*got[0].Controller {
		t.Fatalf("unexpected owner references: %#v", got)
	}
	if got, want := np.Spec.PodSelector.MatchLabels, map[string]string{
		constants.KubernetesPartOfLabelKey:  constants.LLMInferenceServicePartOfValue,
		constants.KubernetesAppNameLabelKey: "svc-a",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("pod selector got %v want %v", got, want)
	}
	if !reflect.DeepEqual(np.Spec.PolicyTypes, []netv1.PolicyType{netv1.PolicyTypeEgress}) {
		t.Fatalf("policy types got %v", np.Spec.PolicyTypes)
	}
	if got, want := len(np.Spec.Egress), 4; got != want {
		t.Fatalf("egress rule count got %d want %d", got, want)
	}

	if got, want := np.Spec.Egress[0].Ports, []netv1.NetworkPolicyPort{
		{Protocol: ptr.To(corev1.ProtocolUDP), Port: ptr.To(intstr.FromInt32(53))},
		{Protocol: ptr.To(corev1.ProtocolTCP), Port: ptr.To(intstr.FromInt32(53))},
		{Protocol: ptr.To(corev1.ProtocolUDP), Port: ptr.To(intstr.FromInt32(5353))},
		{Protocol: ptr.To(corev1.ProtocolTCP), Port: ptr.To(intstr.FromInt32(5353))},
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("DNS ports got %v want %v", got, want)
	}
	if got, want := np.Spec.Egress[1].Ports, []netv1.NetworkPolicyPort{
		{Protocol: ptr.To(corev1.ProtocolTCP), Port: ptr.To(intstr.FromInt32(443))},
		{Protocol: ptr.To(corev1.ProtocolTCP), Port: ptr.To(intstr.FromInt32(6443))},
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("API ports got %v want %v", got, want)
	}
	if np.Spec.Egress[2].To[0].PodSelector == nil || len(np.Spec.Egress[2].To[0].PodSelector.MatchLabels) != 0 {
		t.Fatalf("same-namespace rule is not unrestricted")
	}
	if got, want := len(np.Spec.Egress[3].To), 1; got != want {
		t.Fatalf("OTLP peer count got %d want %d", got, want)
	}
	if got, want := np.Spec.Egress[3].Ports, []netv1.NetworkPolicyPort{{
		Protocol: ptr.To(corev1.ProtocolTCP),
		Port:     ptr.To(intstr.FromInt32(4318)),
	}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("OTLP ports got %v want %v", got, want)
	}
	for _, peer := range np.Spec.Egress[3].To {
		if peer.PodSelector != nil || peer.NamespaceSelector == nil {
			t.Fatalf("OTLP peer has unexpected selectors: %#v", peer)
		}
		if peer.NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] != "observability" {
			t.Fatalf("unexpected namespace selector: %v", peer.NamespaceSelector.MatchLabels)
		}
	}
}
