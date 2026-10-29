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
	netv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"knative.dev/pkg/kmeta"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
	"github.com/kserve/kserve/pkg/controller/v1alpha2/llmisvc"
	. "github.com/kserve/kserve/pkg/controller/v1alpha2/llmisvc/fixture"
)

var _ = Describe("LLMInferenceService NetworkPolicies", func() {
	Context("default mode (strict gate off)", func() {
		BeforeEach(func(ctx SpecContext) {
			PatchLLMISVCConfig(ctx, envTest.Client, func(cfg *llmisvc.LLMISVCConfig) {
				cfg.FeatureGates.StrictNetworkPolicy = false
			})
		})

		It("creates a single allow-all policy instead of strict policies", func(ctx SpecContext) {
			testNs := NewTestNamespace(ctx, envTest)
			llmSvc := LLMInferenceService("test-llm-netpol-allow-all",
				InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
				WithModelURI("hf://facebook/opt-125m"),
			)

			Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
			defer testNs.DeleteAndWait(ctx, llmSvc)

			allowAll := waitForNetworkPolicy(ctx, testNs.Name, kmeta.ChildName(llmSvc.Name, "-networkpolicy-allow-all"))
			Expect(metav1.IsControlledBy(allowAll, llmSvc)).To(BeTrue(), "allow-all policy must be owned by the service")
			Expect(allowAll.Spec.PolicyTypes).To(ConsistOf(netv1.PolicyTypeIngress, netv1.PolicyTypeEgress))
			Expect(allowAll.Spec.Ingress).To(HaveLen(1))
			Expect(allowAll.Spec.Ingress[0].From).To(BeEmpty())
			Expect(allowAll.Spec.Ingress[0].Ports).To(BeEmpty())
			Expect(allowAll.Spec.Egress).To(HaveLen(1))
			Expect(allowAll.Spec.Egress[0].To).To(BeEmpty())
			Expect(allowAll.Spec.Egress[0].Ports).To(BeEmpty())
			Expect(allowAll.Spec.PodSelector.MatchLabels).To(HaveKey("serving.kserve.io/llmisvc-network-policy-id"))
			Expect(allowAll.Spec.PodSelector.MatchLabels).NotTo(HaveKey("serving.kserve.io/llmisvc-network-policy-role"))

			for _, suffix := range []string{"workload", "scheduler", "tokenizer", "egress"} {
				name := kmeta.ChildName(llmSvc.Name, "-networkpolicy-"+suffix)
				err := envTest.Get(ctx, types.NamespacedName{Name: name, Namespace: testNs.Name}, &netv1.NetworkPolicy{})
				Expect(apierrors.IsNotFound(err)).To(BeTrue(), "strict policy %s must not exist in default mode", name)
			}
		})

		It("replaces strict policies with the allow-all policy when the gate is disabled", func(ctx SpecContext) {
			EnableStrictNetworkPolicy(ctx, envTest.Client)
			testNs := NewTestNamespace(ctx, envTest)
			llmSvc := LLMInferenceService("test-llm-netpol-toggle",
				InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
				WithModelURI("hf://facebook/opt-125m"),
			)

			Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
			defer testNs.DeleteAndWait(ctx, llmSvc)

			waitForNetworkPolicy(ctx, testNs.Name, kmeta.ChildName(llmSvc.Name, "-networkpolicy-workload"))

			PatchLLMISVCConfig(ctx, envTest.Client, func(cfg *llmisvc.LLMISVCConfig) {
				cfg.FeatureGates.StrictNetworkPolicy = false
			})

			Eventually(func(g Gomega, ctx context.Context) {
				err := envTest.Get(ctx, types.NamespacedName{
					Name:      kmeta.ChildName(llmSvc.Name, "-networkpolicy-allow-all"),
					Namespace: testNs.Name,
				}, &netv1.NetworkPolicy{})
				g.Expect(err).NotTo(HaveOccurred())
			}).WithContext(ctx).Should(Succeed())

			for _, suffix := range []string{"workload", "scheduler", "tokenizer", "egress"} {
				name := kmeta.ChildName(llmSvc.Name, "-networkpolicy-"+suffix)
				Eventually(func(g Gomega, ctx context.Context) {
					err := envTest.Get(ctx, types.NamespacedName{Name: name, Namespace: testNs.Name}, &netv1.NetworkPolicy{})
					g.Expect(apierrors.IsNotFound(err)).To(BeTrue(), "strict policy %s must be removed after the gate is disabled", name)
				}).WithContext(ctx).Should(Succeed())
			}
		})
	})

	Context("strict mode (strict gate on)", func() {
		BeforeEach(func(ctx SpecContext) {
			EnableStrictNetworkPolicy(ctx, envTest.Client)
		})

		It("creates protected per-role policies without blanket same-namespace ingress", func(ctx SpecContext) {
			testNs := NewTestNamespace(ctx, envTest)
			llmSvc := LLMInferenceService("test-llm-netpol",
				InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
				WithModelURI("hf://facebook/opt-125m"),
			)

			Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
			defer testNs.DeleteAndWait(ctx, llmSvc)

			workload := waitForNetworkPolicy(ctx, testNs.Name, kmeta.ChildName(llmSvc.Name, "-networkpolicy-workload"))
			Expect(workload.Labels).To(HaveKey("serving.kserve.io/llmisvc-network-policy-id"))
			Expect(workload.Spec.PodSelector.MatchLabels).To(HaveKey("serving.kserve.io/llmisvc-network-policy-id"))
			Expect(workload.Spec.PodSelector.MatchLabels).To(HaveKeyWithValue("serving.kserve.io/llmisvc-network-policy-role", "workload"))
			Expect(workload.Spec.Ingress).NotTo(BeEmpty())
			monitoringAllowed := false
			for _, rule := range workload.Spec.Ingress {
				for _, peer := range rule.From {
					if peer.NamespaceSelector != nil && peer.NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] == "openshift-monitoring" {
						monitoringAllowed = true
					}
				}
			}
			Expect(monitoringAllowed).To(BeTrue(), "strict isolation must preserve monitoring peers")

			for _, rule := range workload.Spec.Ingress {
				Expect(rule.From).NotTo(BeEmpty(), "every ingress rule must identify its source")
				Expect(rule.Ports).NotTo(BeEmpty(), "every ingress rule must identify destination ports")
			}

			waitForNetworkPolicy(ctx, testNs.Name, kmeta.ChildName(llmSvc.Name, "-networkpolicy-scheduler"))
			waitForNetworkPolicy(ctx, testNs.Name, kmeta.ChildName(llmSvc.Name, "-networkpolicy-tokenizer"))
			egress := waitForNetworkPolicy(ctx, testNs.Name, kmeta.ChildName(llmSvc.Name, "-networkpolicy-egress"))
			Expect(egress.Spec.PolicyTypes).To(ConsistOf(netv1.PolicyTypeEgress))
			Expect(egress.Spec.Egress).To(HaveLen(1))
			Expect(egress.Spec.Egress[0].To).To(BeEmpty())
			Expect(egress.Spec.Egress[0].Ports).To(BeEmpty())

			legacy := &netv1.NetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      kmeta.ChildName(llmSvc.Name, "-prometheus-scraping"),
					Namespace: testNs.Name,
					OwnerReferences: []metav1.OwnerReference{
						*metav1.NewControllerRef(llmSvc, v1alpha2.LLMInferenceServiceGVK),
					},
				},
				Spec: netv1.NetworkPolicySpec{
					PodSelector: metav1.LabelSelector{},
					PolicyTypes: []netv1.PolicyType{netv1.PolicyTypeIngress},
					Ingress:     []netv1.NetworkPolicyIngressRule{{}},
				},
			}
			Expect(envTest.Create(ctx, legacy)).To(Succeed())
			Eventually(func(g Gomega, ctx context.Context) {
				legacy := &netv1.NetworkPolicy{}
				err := envTest.Get(ctx, types.NamespacedName{
					Name:      kmeta.ChildName(llmSvc.Name, "-prometheus-scraping"),
					Namespace: testNs.Name,
				}, legacy)
				g.Expect(apierrors.IsNotFound(err)).To(BeTrue())
			}).WithContext(ctx).Should(Succeed())
		})

		It("uses only explicitly referenced Gateway namespaces", func(ctx SpecContext) {
			testNs := NewTestNamespace(ctx, envTest)
			llmSvc := LLMInferenceService("test-llm-netpol-gateway",
				InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
				WithModelURI("hf://facebook/opt-125m"),
				WithGatewayRefs(LLMGatewayRef("custom-gateway", testNs.Name)),
			)
			customGateway := Gateway("custom-gateway",
				InNamespace[*gwapiv1.Gateway](testNs.Name),
				WithListener(gwapiv1.HTTPProtocolType),
			)

			Expect(envTest.Create(ctx, customGateway)).To(Succeed())
			ensureGatewayReady(ctx, envTest.Client, customGateway)
			Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
			defer testNs.DeleteAndWait(ctx, llmSvc)

			workload := waitForNetworkPolicy(ctx, testNs.Name, kmeta.ChildName(llmSvc.Name, "-networkpolicy-workload"))
			gatewayRules := make([]netv1.NetworkPolicyIngressRule, 0)
			for _, rule := range workload.Spec.Ingress {
				for _, peer := range rule.From {
					if peer.NamespaceSelector != nil && peer.NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] == testNs.Name {
						gatewayRules = append(gatewayRules, rule)
						break
					}
				}
			}
			Expect(gatewayRules).To(HaveLen(1))
			Expect(gatewayRules[0].Ports).To(HaveLen(1))
			Expect(gatewayRules[0].Ports[0].Port).NotTo(BeNil())
			Expect(gatewayRules[0].Ports[0].Port.IntValue()).To(Equal(8000))
			Expect(gatewayRules[0].From).To(HaveLen(1))
			Expect(gatewayRules[0].From[0].NamespaceSelector.MatchLabels).To(HaveKeyWithValue(
				"kubernetes.io/metadata.name", testNs.Name))
		})

		It("removes all owned policies when the service is deleted", func(ctx SpecContext) {
			testNs := NewTestNamespace(ctx, envTest)
			llmSvc := LLMInferenceService("test-llm-netpol-cleanup",
				InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
				WithModelURI("hf://facebook/opt-125m"),
			)

			Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
			waitForNetworkPolicy(ctx, testNs.Name, kmeta.ChildName(llmSvc.Name, "-networkpolicy-workload"))
			Expect(envTest.Delete(ctx, llmSvc)).To(Succeed())

			policyNames := []string{
				kmeta.ChildName(llmSvc.Name, "-networkpolicy-workload"),
				kmeta.ChildName(llmSvc.Name, "-networkpolicy-scheduler"),
				kmeta.ChildName(llmSvc.Name, "-networkpolicy-tokenizer"),
				kmeta.ChildName(llmSvc.Name, "-networkpolicy-egress"),
				kmeta.ChildName(llmSvc.Name, "-networkpolicy-allow-all"),
			}
			Eventually(func(g Gomega, ctx context.Context) {
				for _, name := range policyNames {
					err := envTest.Get(ctx, types.NamespacedName{Name: name, Namespace: testNs.Name}, &netv1.NetworkPolicy{})
					g.Expect(apierrors.IsNotFound(err)).To(BeTrue(), name)
				}
			}).WithContext(ctx).Should(Succeed())
		})
	})
})

func waitForNetworkPolicy(ctx context.Context, namespace, name string) *netv1.NetworkPolicy {
	policy := &netv1.NetworkPolicy{}
	Eventually(func(_ Gomega, ctx context.Context) error {
		return envTest.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, policy)
	}).WithContext(ctx).Should(Succeed())
	return policy
}
