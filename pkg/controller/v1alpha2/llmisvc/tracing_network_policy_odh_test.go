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

package llmisvc_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"knative.dev/pkg/kmeta"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
	"github.com/kserve/kserve/pkg/constants"
	. "github.com/kserve/kserve/pkg/controller/v1alpha2/llmisvc/fixture"
)

var _ = Describe("LLMInferenceService tracing NetworkPolicy", func() {
	It("creates a comprehensive per-service OTLP egress policy", func(ctx SpecContext) {
		GinkgoT().Setenv("MONITORING_NAMESPACE", "test-monitoring-ns")
		testNs := NewTestNamespace(ctx, envTest)
		llmSvc := LLMInferenceService("test-llm-tracing-netpol",
			InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
			WithModelURI("hf://facebook/opt-125m"),
		)
		llmSvc.Spec.Tracing = &v1alpha2.TracingSpec{}

		Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
		defer testNs.DeleteAndWait(ctx, llmSvc)

		np := waitForTracingNetworkPolicy(ctx, testNs.Name, llmSvc.Name)
		Expect(np.Labels).To(HaveKeyWithValue(constants.KubernetesComponentLabelKey, "llm-tracing"))
		Expect(np.Labels).To(HaveKeyWithValue(constants.KubernetesPartOfLabelKey, constants.LLMInferenceServicePartOfValue))
		Expect(np.Labels).To(HaveKeyWithValue(constants.KubernetesAppNameLabelKey, llmSvc.Name))
		Expect(np.OwnerReferences).To(HaveLen(1))
		Expect(np.OwnerReferences[0].Name).To(Equal(llmSvc.Name))
		Expect(np.OwnerReferences[0].Controller).To(Equal(ptr.To(true)))
		Expect(np.Spec.PolicyTypes).To(Equal([]netv1.PolicyType{netv1.PolicyTypeEgress}))
		Expect(np.Spec.PodSelector.MatchLabels).To(Equal(map[string]string{
			constants.KubernetesPartOfLabelKey:  constants.LLMInferenceServicePartOfValue,
			constants.KubernetesAppNameLabelKey: llmSvc.Name,
		}))
		Expect(np.Spec.Egress).To(HaveLen(4))
		Expect(np.Spec.Egress[0].Ports).To(ContainElements(
			netv1.NetworkPolicyPort{Protocol: ptr.To(corev1.ProtocolUDP), Port: ptr.To(intstr.FromInt32(53))},
			netv1.NetworkPolicyPort{Protocol: ptr.To(corev1.ProtocolTCP), Port: ptr.To(intstr.FromInt32(53))},
			netv1.NetworkPolicyPort{Protocol: ptr.To(corev1.ProtocolUDP), Port: ptr.To(intstr.FromInt32(5353))},
			netv1.NetworkPolicyPort{Protocol: ptr.To(corev1.ProtocolTCP), Port: ptr.To(intstr.FromInt32(5353))},
		))
		Expect(np.Spec.Egress[1].Ports).To(ContainElements(
			netv1.NetworkPolicyPort{Protocol: ptr.To(corev1.ProtocolTCP), Port: ptr.To(intstr.FromInt32(443))},
			netv1.NetworkPolicyPort{Protocol: ptr.To(corev1.ProtocolTCP), Port: ptr.To(intstr.FromInt32(6443))},
		))
		Expect(np.Spec.Egress[2].To).To(HaveLen(1))
		Expect(np.Spec.Egress[2].To[0].PodSelector.MatchLabels).To(BeEmpty())
		Expect(np.Spec.Egress[3].Ports).To(ContainElement(
			netv1.NetworkPolicyPort{Protocol: ptr.To(corev1.ProtocolTCP), Port: ptr.To(intstr.FromInt32(4317))},
		))
		Expect(namespaceNamesFromEgressRule(np.Spec.Egress[3])).To(ConsistOf("redhat-ods-monitoring", "test-monitoring-ns"))
		Expect(np.Spec.Egress[3].To[0].PodSelector.MatchLabels).To(HaveKeyWithValue(
			constants.KubernetesAppNameLabelKey, "data-science-collector-collector"))
		Expect(np.Spec.Egress[3].To[0].PodSelector.MatchLabels).To(HaveKeyWithValue(
			constants.KubernetesComponentLabelKey, "opentelemetry-collector"))

		monitoringNP := &netv1.NetworkPolicy{}
		Expect(envTest.Get(ctx, types.NamespacedName{
			Name:      kmeta.ChildName(llmSvc.Name, "-prometheus-scraping"),
			Namespace: testNs.Name,
		}, monitoringNP)).To(Succeed())
	})

	It("removes the policy when tracing is cleared", func(ctx SpecContext) {
		testNs := NewTestNamespace(ctx, envTest)
		llmSvc := LLMInferenceService("test-llm-tracing-clear",
			InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
			WithModelURI("hf://facebook/opt-125m"),
		)
		llmSvc.Spec.Tracing = &v1alpha2.TracingSpec{}
		Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
		defer testNs.DeleteAndWait(ctx, llmSvc)
		waitForTracingNetworkPolicy(ctx, testNs.Name, llmSvc.Name)

		updated := &v1alpha2.LLMInferenceService{}
		Expect(envTest.Get(ctx, types.NamespacedName{Name: llmSvc.Name, Namespace: testNs.Name}, updated)).To(Succeed())
		updated.Spec.Tracing = nil
		Expect(envTest.Update(ctx, updated)).To(Succeed())

		Eventually(func(g Gomega, ctx context.Context) {
			err := envTest.Get(ctx, types.NamespacedName{
				Name:      kmeta.ChildName(llmSvc.Name, "-otlp-egress"),
				Namespace: testNs.Name,
			}, &netv1.NetworkPolicy{})
			g.Expect(apierrors.IsNotFound(err)).To(BeTrue())
		}).WithContext(ctx).Should(Succeed())
	})

	It("does not create the policy for a force-stopped service", func(ctx SpecContext) {
		testNs := NewTestNamespace(ctx, envTest)
		llmSvc := LLMInferenceService("test-llm-tracing-stopped",
			InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
			WithModelURI("hf://facebook/opt-125m"),
			WithAnnotations(map[string]string{constants.StopAnnotationKey: "true"}),
		)
		llmSvc.Spec.Tracing = &v1alpha2.TracingSpec{}
		Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
		defer testNs.DeleteAndWait(ctx, llmSvc)

		Consistently(func(g Gomega, ctx context.Context) {
			err := envTest.Get(ctx, types.NamespacedName{
				Name:      kmeta.ChildName(llmSvc.Name, "-otlp-egress"),
				Namespace: testNs.Name,
			}, &netv1.NetworkPolicy{})
			g.Expect(apierrors.IsNotFound(err)).To(BeTrue())
		}).WithContext(ctx).Should(Succeed())
	})

	It("deletes the policy when the service is deleted", func(ctx SpecContext) {
		testNs := NewTestNamespace(ctx, envTest)
		llmSvc := LLMInferenceService("test-llm-tracing-delete",
			InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
			WithModelURI("hf://facebook/opt-125m"),
		)
		llmSvc.Spec.Tracing = &v1alpha2.TracingSpec{}
		Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
		waitForTracingNetworkPolicy(ctx, testNs.Name, llmSvc.Name)

		testNs.DeleteAndWait(ctx, llmSvc)
		Eventually(func(g Gomega, ctx context.Context) {
			err := envTest.Get(ctx, types.NamespacedName{
				Name:      kmeta.ChildName(llmSvc.Name, "-otlp-egress"),
				Namespace: testNs.Name,
			}, &netv1.NetworkPolicy{})
			g.Expect(apierrors.IsNotFound(err)).To(BeTrue())
		}).WithContext(ctx).Should(Succeed())
	})
})

func waitForTracingNetworkPolicy(ctx context.Context, namespace, serviceName string) *netv1.NetworkPolicy {
	np := &netv1.NetworkPolicy{}
	Eventually(func(_ Gomega, ctx context.Context) error {
		return envTest.Get(ctx, types.NamespacedName{
			Name:      kmeta.ChildName(serviceName, "-otlp-egress"),
			Namespace: namespace,
		}, np)
	}).WithContext(ctx).Should(Succeed())
	return np
}

func namespaceNamesFromEgressRule(rule netv1.NetworkPolicyEgressRule) []string {
	namespaces := make([]string, 0, len(rule.To))
	for _, peer := range rule.To {
		if peer.NamespaceSelector == nil {
			continue
		}
		if namespace, ok := peer.NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"]; ok {
			namespaces = append(namespaces, namespace)
		}
	}
	return namespaces
}
