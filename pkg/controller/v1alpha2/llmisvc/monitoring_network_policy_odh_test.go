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
	"reflect"
	"slices"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	netv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"knative.dev/pkg/kmeta"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
	"github.com/kserve/kserve/pkg/constants"
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

		It("creates per-component ingress-only policies without broad egress", func(ctx SpecContext) {
			testNs := NewTestNamespace(ctx, envTest)
			llmSvc := LLMInferenceService("test-llm-netpol-allow-all",
				InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
				WithModelURI("hf://facebook/opt-125m"),
			)

			Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
			defer testNs.DeleteAndWait(ctx, llmSvc)

			// The default mode selects every stored component through its own
			// policy; the main Deployment's Pods must keep full ingress coverage.
			defaultMain := waitForNetworkPolicy(ctx, testNs.Name, kmeta.ChildName(llmSvc.Name, "-networkpolicy-default-main"))
			Expect(metav1.IsControlledBy(defaultMain, llmSvc)).To(BeTrue(), "per-component policy must be owned by the service")
			main := &appsv1.Deployment{}
			Expect(envTest.Get(ctx, types.NamespacedName{
				Name:      kmeta.ChildName(llmSvc.Name, "-kserve"),
				Namespace: testNs.Name,
			}, main)).To(Succeed())
			Expect(defaultMain.Spec.PodSelector).To(Equal(*main.Spec.Selector), "policy must select the stored main Deployment's Pods")
			Expect(defaultMain.Spec.PolicyTypes).To(ConsistOf(netv1.PolicyTypeIngress))
			Expect(defaultMain.Spec.Ingress).To(Equal([]netv1.NetworkPolicyIngressRule{{}}))
			Expect(defaultMain.Spec.Egress).To(BeEmpty(), "default-mode policies must not create egress rules")

			// Default mode keeps an explicit per-component allow-all egress grant:
			// in a namespace with default-deny egress, an absent egress policy is
			// not an allow-all grant.
			egressMain := waitForNetworkPolicy(ctx, testNs.Name, kmeta.ChildName(llmSvc.Name, "-networkpolicy-egress-main"))
			Expect(metav1.IsControlledBy(egressMain, llmSvc)).To(BeTrue(), "egress grant must be owned by the service")
			Expect(egressMain.Spec.PodSelector).To(Equal(*main.Spec.Selector), "egress grant must select the stored main Deployment's Pods")
			Expect(egressMain.Spec.PolicyTypes).To(ConsistOf(netv1.PolicyTypeEgress))
			Expect(egressMain.Spec.Ingress).To(BeEmpty(), "the egress grant must not create ingress rules")
			Expect(egressMain.Spec.Egress).To(Equal([]netv1.NetworkPolicyEgressRule{{}}), "the egress grant must be an explicit allow-all rule")
			for _, suffix := range managedAllowAllEgressPolicySuffixes() {
				if suffix == "-networkpolicy-egress-main" {
					continue
				}
				name := kmeta.ChildName(llmSvc.Name, suffix)
				err := envTest.Get(ctx, types.NamespacedName{Name: name, Namespace: testNs.Name}, &netv1.NetworkPolicy{})
				Expect(apierrors.IsNotFound(err)).To(BeTrue(), "egress policy %s must not exist without a stored component", name)
			}

			// No broad egress grant is created in default mode.
			for _, suffix := range []string{"-networkpolicy-egress", "-networkpolicy-allow-all"} {
				name := kmeta.ChildName(llmSvc.Name, suffix)
				err := envTest.Get(ctx, types.NamespacedName{Name: name, Namespace: testNs.Name}, &netv1.NetworkPolicy{})
				Expect(apierrors.IsNotFound(err)).To(BeTrue(), "broad egress policy %s must not exist in default mode", name)
			}
			for _, suffix := range append(managedIngressPolicySuffixes("strict"), "-networkpolicy-egress") {
				name := kmeta.ChildName(llmSvc.Name, suffix)
				err := envTest.Get(ctx, types.NamespacedName{Name: name, Namespace: testNs.Name}, &netv1.NetworkPolicy{})
				Expect(apierrors.IsNotFound(err)).To(BeTrue(), "strict policy %s must not exist in default mode", name)
			}
			for _, suffix := range []string{"-networkpolicy-workload", "-networkpolicy-scheduler", "-networkpolicy-tokenizer"} {
				name := kmeta.ChildName(llmSvc.Name, suffix)
				err := envTest.Get(ctx, types.NamespacedName{Name: name, Namespace: testNs.Name}, &netv1.NetworkPolicy{})
				Expect(apierrors.IsNotFound(err)).To(BeTrue(), "pre-selector strict policy %s must not exist in default mode", name)
			}
		})

		It("replaces strict policies with the default-mode policies when the gate is disabled", func(ctx SpecContext) {
			EnableStrictNetworkPolicy(ctx, envTest.Client)
			testNs := NewTestNamespace(ctx, envTest)
			llmSvc := LLMInferenceService("test-llm-netpol-toggle",
				InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
				WithModelURI("hf://facebook/opt-125m"),
			)

			Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
			defer testNs.DeleteAndWait(ctx, llmSvc)

			waitForNetworkPolicy(ctx, testNs.Name, kmeta.ChildName(llmSvc.Name, "-networkpolicy-strict-main"))
			// The egress grant is mode-independent: capture it and require it to
			// survive the toggle without churn.
			egressMain := waitForNetworkPolicy(ctx, testNs.Name, kmeta.ChildName(llmSvc.Name, "-networkpolicy-egress-main"))

			PatchLLMISVCConfig(ctx, envTest.Client, func(cfg *llmisvc.LLMISVCConfig) {
				cfg.FeatureGates.StrictNetworkPolicy = false
			})

			Eventually(func(g Gomega, ctx context.Context) {
				err := envTest.Get(ctx, types.NamespacedName{
					Name:      kmeta.ChildName(llmSvc.Name, "-networkpolicy-default-main"),
					Namespace: testNs.Name,
				}, &netv1.NetworkPolicy{})
				g.Expect(err).NotTo(HaveOccurred())
			}).WithContext(ctx).Should(Succeed())

			for _, suffix := range append(managedIngressPolicySuffixes("strict"), "-networkpolicy-egress") {
				name := kmeta.ChildName(llmSvc.Name, suffix)
				Eventually(func(g Gomega, ctx context.Context) {
					err := envTest.Get(ctx, types.NamespacedName{Name: name, Namespace: testNs.Name}, &netv1.NetworkPolicy{})
					g.Expect(apierrors.IsNotFound(err)).To(BeTrue(), "strict policy %s must be removed after the gate is disabled", name)
				}).WithContext(ctx).Should(Succeed())
			}

			// The per-component egress grant survived the toggle unchanged.
			survivedEgress := waitForNetworkPolicy(ctx, testNs.Name, kmeta.ChildName(llmSvc.Name, "-networkpolicy-egress-main"))
			Expect(survivedEgress.UID).To(Equal(egressMain.UID), "the gate toggle must not churn the egress grant")
			Expect(survivedEgress.ResourceVersion).To(Equal(egressMain.ResourceVersion), "the gate toggle must not rewrite the egress grant")
			Expect(survivedEgress.Spec).To(Equal(egressMain.Spec), "the gate toggle must not change the egress grant spec")
		})
	})

	Context("strict mode (strict gate on)", func() {
		BeforeEach(func(ctx SpecContext) {
			EnableStrictNetworkPolicy(ctx, envTest.Client)
		})

		It("creates per-component policies selecting stored component selectors without blanket same-namespace ingress", func(ctx SpecContext) {
			testNs := NewTestNamespace(ctx, envTest)
			llmSvc := LLMInferenceService("test-llm-netpol",
				InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
				WithModelURI("hf://facebook/opt-125m"),
				WithSchedulerCommand("epp"),
				WithManagedTokenizer(),
			)

			Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
			defer testNs.DeleteAndWait(ctx, llmSvc)

			workload := waitForNetworkPolicy(ctx, testNs.Name, kmeta.ChildName(llmSvc.Name, "-networkpolicy-strict-main"))
			Expect(metav1.IsControlledBy(workload, llmSvc)).To(BeTrue(), "workload policy must be owned by the service")
			main := &appsv1.Deployment{}
			Expect(envTest.Get(ctx, types.NamespacedName{
				Name:      kmeta.ChildName(llmSvc.Name, "-kserve"),
				Namespace: testNs.Name,
			}, main)).To(Succeed())
			Expect(workload.Spec.PodSelector).To(Equal(*main.Spec.Selector), "workload policy must select the stored main Deployment's Pods")
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

			scheduler := waitForNetworkPolicy(ctx, testNs.Name, kmeta.ChildName(llmSvc.Name, "-networkpolicy-strict-scheduler"))
			schedulerDeployment := &appsv1.Deployment{}
			Expect(envTest.Get(ctx, types.NamespacedName{
				Name:      kmeta.ChildName(llmSvc.Name, "-kserve-router-scheduler"),
				Namespace: testNs.Name,
			}, schedulerDeployment)).To(Succeed())
			Expect(scheduler.Spec.PodSelector).To(Equal(*schedulerDeployment.Spec.Selector), "scheduler policy must select the stored scheduler Deployment's Pods")

			tokenizer := waitForNetworkPolicy(ctx, testNs.Name, kmeta.ChildName(llmSvc.Name, "-networkpolicy-strict-tokenizer"))
			tokenizerDeployment := &appsv1.Deployment{}
			Expect(envTest.Get(ctx, types.NamespacedName{
				Name:      kmeta.ChildName(llmSvc.Name, "-tokenizer"),
				Namespace: testNs.Name,
			}, tokenizerDeployment)).To(Succeed())
			Expect(tokenizer.Spec.PodSelector).To(Equal(*tokenizerDeployment.Spec.Selector), "tokenizer policy must select the stored tokenizer Deployment's Pods")
			Expect(tokenizer.Spec.Ingress).To(HaveLen(1), "tokenizer must only admit its scheduler")
			Expect(tokenizer.Spec.Ingress[0].From).To(HaveLen(1))
			Expect(tokenizer.Spec.Ingress[0].From[0].PodSelector).NotTo(BeNil())
			Expect(*tokenizer.Spec.Ingress[0].From[0].PodSelector).To(Equal(*schedulerDeployment.Spec.Selector), "tokenizer source must be the stored scheduler selector")

			// Egress is mode-independent: the retired legacy broad names stay
			// absent while every stored component keeps its explicit per-component
			// allow-all egress grant.
			for _, suffix := range []string{"-networkpolicy-egress", "-networkpolicy-allow-all"} {
				name := kmeta.ChildName(llmSvc.Name, suffix)
				err := envTest.Get(ctx, types.NamespacedName{Name: name, Namespace: testNs.Name}, &netv1.NetworkPolicy{})
				Expect(apierrors.IsNotFound(err)).To(BeTrue(), "legacy broad egress policy %s must not exist in strict mode", name)
			}
			for _, component := range []struct {
				key      string
				selector *metav1.LabelSelector
			}{
				{"main", main.Spec.Selector},
				{"scheduler", schedulerDeployment.Spec.Selector},
				{"tokenizer", tokenizerDeployment.Spec.Selector},
			} {
				grant := waitForNetworkPolicy(ctx, testNs.Name, kmeta.ChildName(llmSvc.Name, "-networkpolicy-egress-"+component.key))
				Expect(metav1.IsControlledBy(grant, llmSvc)).To(BeTrue(), "egress grant must be owned by the service")
				Expect(grant.Spec.PodSelector).To(Equal(*component.selector), "egress grant must select the stored %s Deployment's Pods", component.key)
				Expect(grant.Spec.PolicyTypes).To(ConsistOf(netv1.PolicyTypeEgress))
				Expect(grant.Spec.Ingress).To(BeEmpty(), "the egress grant must not create ingress rules")
				Expect(grant.Spec.Egress).To(Equal([]netv1.NetworkPolicyEgressRule{{}}), "the egress grant must be an explicit allow-all rule")
			}
			for _, suffix := range managedAllowAllEgressPolicySuffixes() {
				switch suffix {
				case "-networkpolicy-egress-main", "-networkpolicy-egress-scheduler", "-networkpolicy-egress-tokenizer":
					continue
				}
				name := kmeta.ChildName(llmSvc.Name, suffix)
				err := envTest.Get(ctx, types.NamespacedName{Name: name, Namespace: testNs.Name}, &netv1.NetworkPolicy{})
				Expect(apierrors.IsNotFound(err)).To(BeTrue(), "egress policy %s must not exist without a stored component", name)
			}

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

			workload := waitForNetworkPolicy(ctx, testNs.Name, kmeta.ChildName(llmSvc.Name, "-networkpolicy-strict-main"))
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
			waitForNetworkPolicy(ctx, testNs.Name, kmeta.ChildName(llmSvc.Name, "-networkpolicy-strict-main"))
			Expect(envTest.Delete(ctx, llmSvc)).To(Succeed())

			policySuffixes := slices.Concat(
				managedIngressPolicySuffixes("strict"),
				managedIngressPolicySuffixes("default"),
				managedAllowAllEgressPolicySuffixes(),
				[]string{"-networkpolicy-egress", "-networkpolicy-allow-all"})
			Eventually(func(g Gomega, ctx context.Context) {
				// The suffix lists must be mapped through kmeta.ChildName:
				// checking a raw suffix as an object name would match nothing.
				for _, suffix := range policySuffixes {
					name := kmeta.ChildName(llmSvc.Name, suffix)
					err := envTest.Get(ctx, types.NamespacedName{Name: name, Namespace: testNs.Name}, &netv1.NetworkPolicy{})
					g.Expect(apierrors.IsNotFound(err)).To(BeTrue(), name)
				}
			}).WithContext(ctx).Should(Succeed())
		})

		It("completes finalization with an ownerless legacy policy and preserves it", func(ctx SpecContext) {
			testNs := NewTestNamespace(ctx, envTest)
			llmSvc := LLMInferenceService("test-llm-netpol-ownerless-legacy-cleanup",
				InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
				WithModelURI("hf://facebook/opt-125m"),
			)
			Expect(envTest.Create(ctx, llmSvc)).To(Succeed())

			legacy := &netv1.NetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      kmeta.ChildName(llmSvc.Name, "-prometheus-scraping"),
					Namespace: testNs.Name,
					Labels:    map[string]string{"test-owner": "foreign"},
				},
				Spec: netv1.NetworkPolicySpec{
					PodSelector: metav1.LabelSelector{},
					PolicyTypes: []netv1.PolicyType{netv1.PolicyTypeIngress},
					Ingress:     []netv1.NetworkPolicyIngressRule{{}},
				},
			}
			DeferCleanup(func(ctx SpecContext) {
				if err := envTest.Delete(ctx, legacy); err != nil && !apierrors.IsNotFound(err) {
					Expect(err).NotTo(HaveOccurred())
				}
				testNs.DeleteAndWait(ctx, llmSvc)
			})

			ownedPolicyNames := []string{
				kmeta.ChildName(llmSvc.Name, "-networkpolicy-strict-main"),
			}
			for _, name := range ownedPolicyNames {
				waitForNetworkPolicy(ctx, testNs.Name, name)
			}
			Expect(envTest.Create(ctx, legacy)).To(Succeed())
			legacyBefore := &netv1.NetworkPolicy{}
			Expect(envTest.Get(ctx, types.NamespacedName{Name: legacy.Name, Namespace: legacy.Namespace}, legacyBefore)).To(Succeed())
			Expect(legacyBefore.OwnerReferences).To(BeEmpty(), "legacy policy is intentionally ownerless")

			// This must complete through the service finalizer despite the
			// ownerless same-name policy. Envtest has no garbage collector, so
			// the owned policies are verified after explicit controller cleanup.
			testNs.DeleteAndWait(ctx, llmSvc)
			Eventually(func(g Gomega, ctx context.Context) {
				err := envTest.Get(ctx, types.NamespacedName{Name: llmSvc.Name, Namespace: testNs.Name}, &v1alpha2.LLMInferenceService{})
				g.Expect(apierrors.IsNotFound(err)).To(BeTrue(), "service finalization must complete")
			}).WithContext(ctx).Should(Succeed())

			allManagedPolicySuffixes := slices.Concat(
				managedIngressPolicySuffixes("strict"),
				managedIngressPolicySuffixes("default"),
				managedAllowAllEgressPolicySuffixes(),
				[]string{"-networkpolicy-allow-all", "-otlp-egress"})
			Eventually(func(g Gomega, ctx context.Context) {
				// The suffix lists must be mapped through kmeta.ChildName:
				// checking a raw suffix as an object name would match nothing.
				for _, suffix := range allManagedPolicySuffixes {
					name := kmeta.ChildName(llmSvc.Name, suffix)
					err := envTest.Get(ctx, types.NamespacedName{Name: name, Namespace: testNs.Name}, &netv1.NetworkPolicy{})
					g.Expect(apierrors.IsNotFound(err)).To(BeTrue(), "owned policy %s must be removed", name)
				}
			}).WithContext(ctx).Should(Succeed())

			Eventually(func(g Gomega, ctx context.Context) {
				legacyAfter := &netv1.NetworkPolicy{}
				g.Expect(envTest.Get(ctx, types.NamespacedName{Name: legacy.Name, Namespace: legacy.Namespace}, legacyAfter)).To(Succeed())
				g.Expect(reflect.DeepEqual(legacyAfter, legacyBefore)).To(BeTrue(), "ownerless foreign policy must remain unchanged")
			}).WithContext(ctx).Should(Succeed())
		})
	})

	Context("upgrade from pre-feature workloads and ingress mode toggles", func() {
		BeforeEach(func(ctx SpecContext) {
			PatchLLMISVCConfig(ctx, envTest.Client, func(cfg *llmisvc.LLMISVCConfig) {
				cfg.FeatureGates.StrictNetworkPolicy = false
			})
		})

		getWorkloads := func(ctx SpecContext, namespace, serviceName string) (main, prefill *appsv1.Deployment) {
			GinkgoHelper()
			main = &appsv1.Deployment{}
			prefill = &appsv1.Deployment{}
			Expect(envTest.Get(ctx, types.NamespacedName{
				Name: kmeta.ChildName(serviceName, "-kserve"), Namespace: namespace,
			}, main)).To(Succeed())
			Expect(envTest.Get(ctx, types.NamespacedName{
				Name: kmeta.ChildName(serviceName, "-kserve-prefill"), Namespace: namespace,
			}, prefill)).To(Succeed())
			return main, prefill
		}

		waitForWorkloads := func(ctx SpecContext, namespace, serviceName string) (main, prefill *appsv1.Deployment) {
			GinkgoHelper()
			Eventually(func(g Gomega, ctx context.Context) {
				g.Expect(envTest.Get(ctx, types.NamespacedName{
					Name: kmeta.ChildName(serviceName, "-kserve"), Namespace: namespace,
				}, &appsv1.Deployment{})).To(Succeed())
				g.Expect(envTest.Get(ctx, types.NamespacedName{
					Name: kmeta.ChildName(serviceName, "-kserve-prefill"), Namespace: namespace,
				}, &appsv1.Deployment{})).To(Succeed())
			}).WithContext(ctx).Should(Succeed())
			return getWorkloads(ctx, namespace, serviceName)
		}

		assertWorkloadsUnchanged := func(ctx SpecContext, namespace, serviceName, stage string, beforeMain, beforePrefill *appsv1.Deployment) {
			GinkgoHelper()
			main, prefill := getWorkloads(ctx, namespace, serviceName)
			Expect(main.Spec.Selector).To(Equal(beforeMain.Spec.Selector), "%s: the immutable main Deployment selector must be preserved", stage)
			Expect(main.Spec.Template).To(Equal(beforeMain.Spec.Template), "%s: the main Pod template must not change", stage)
			Expect(prefill.Spec.Selector).To(Equal(beforePrefill.Spec.Selector), "%s: the immutable prefill Deployment selector must be preserved", stage)
			Expect(prefill.Spec.Template).To(Equal(beforePrefill.Spec.Template), "%s: the prefill Pod template must not change", stage)
		}

		assertPoliciesAbsent := func(ctx SpecContext, namespace string, names []string) {
			GinkgoHelper()
			Eventually(func(g Gomega, ctx context.Context) {
				for _, name := range names {
					err := envTest.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, &netv1.NetworkPolicy{})
					g.Expect(apierrors.IsNotFound(err)).To(BeTrue(), "%s must be removed", name)
				}
			}).WithContext(ctx).Should(Succeed())
		}

		childNames := func(serviceName string, suffixes []string) []string {
			names := make([]string, 0, len(suffixes))
			for _, suffix := range suffixes {
				names = append(names, kmeta.ChildName(serviceName, suffix))
			}
			return names
		}

		It("migrates branch-created policies without rolling out pre-feature workloads", func(ctx SpecContext) {
			testNs := NewTestNamespace(ctx, envTest)
			llmSvc := LLMInferenceService("test-llm-netpol-upgrade",
				InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
				WithModelURI("hf://facebook/opt-125m"),
				WithTemplate(SimpleWorkerPodSpec()),
				WithPrefill(SimpleWorkerPodSpec()),
			)
			Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
			defer testNs.DeleteAndWait(ctx, llmSvc)

			// The pre-feature upgrade baseline: workloads stored without the
			// NetworkPolicy identity labels the branch build stamped onto Pod
			// templates and immutable selectors.
			waitForNetworkPolicy(ctx, testNs.Name, kmeta.ChildName(llmSvc.Name, "-networkpolicy-default-main"))
			waitForNetworkPolicy(ctx, testNs.Name, kmeta.ChildName(llmSvc.Name, "-networkpolicy-default-prefill"))
			main, prefill := waitForWorkloads(ctx, testNs.Name, llmSvc.Name)
			for _, deployment := range []*appsv1.Deployment{main, prefill} {
				Expect(deployment.Spec.Template.Labels).NotTo(HaveKey("serving.kserve.io/llmisvc-network-policy-id"))
				Expect(deployment.Spec.Template.Labels).NotTo(HaveKey("serving.kserve.io/llmisvc-network-policy-role"))
				Expect(deployment.Spec.Selector.MatchLabels).NotTo(HaveKey("serving.kserve.io/llmisvc-network-policy-id"))
			}
			// The current P/D presets opt the workload into revision hashing;
			// the revision value must survive the whole upgrade and every mode
			// toggle without a rollout.
			revision := main.Spec.Template.Labels[constants.LLMInferenceServiceRevisionLabelKey]
			Expect(revision).NotTo(BeEmpty(), "the default P/D presets pin a workload revision")
			Expect(prefill.Spec.Template.Labels[constants.LLMInferenceServiceRevisionLabelKey]).To(Equal(revision))

			// Seed the NetworkPolicies the branch build left behind: the combined
			// allow-all policy, the general egress policy, and the legacy
			// monitoring policy, the first two selecting Pods through the removed
			// identity label. These are test-only fixtures for the removed shapes,
			// like the unit-suite legacy helpers.
			branchPolicy := func(suffix string, policyTypes []netv1.PolicyType) *netv1.NetworkPolicy {
				return &netv1.NetworkPolicy{
					ObjectMeta: metav1.ObjectMeta{
						Name:            kmeta.ChildName(llmSvc.Name, suffix),
						Namespace:       testNs.Name,
						OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(llmSvc, v1alpha2.LLMInferenceServiceGVK)},
					},
					Spec: netv1.NetworkPolicySpec{
						PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{
							"serving.kserve.io/llmisvc-network-policy-id": "branch-fixture",
						}},
						PolicyTypes: policyTypes,
						Ingress:     []netv1.NetworkPolicyIngressRule{{}},
						Egress:      []netv1.NetworkPolicyEgressRule{{}},
					},
				}
			}
			legacyMonitoring := &netv1.NetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:            kmeta.ChildName(llmSvc.Name, "-prometheus-scraping"),
					Namespace:       testNs.Name,
					OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(llmSvc, v1alpha2.LLMInferenceServiceGVK)},
				},
				Spec: netv1.NetworkPolicySpec{
					PodSelector: metav1.LabelSelector{},
					PolicyTypes: []netv1.PolicyType{netv1.PolicyTypeIngress},
					Ingress:     []netv1.NetworkPolicyIngressRule{{}},
				},
			}
			Expect(envTest.Create(ctx, branchPolicy("-networkpolicy-allow-all",
				[]netv1.PolicyType{netv1.PolicyTypeIngress, netv1.PolicyTypeEgress}))).To(Succeed())
			Expect(envTest.Create(ctx, branchPolicy("-networkpolicy-egress",
				[]netv1.PolicyType{netv1.PolicyTypeEgress}))).To(Succeed())
			Expect(envTest.Create(ctx, legacyMonitoring)).To(Succeed())

			// A metadata-only touch re-reconciles the upgraded service.
			Expect(updateLLMInferenceServiceWithRetry(ctx, testNs.Name, llmSvc.Name, func(updated *v1alpha2.LLMInferenceService) {
				if updated.Annotations == nil {
					updated.Annotations = map[string]string{}
				}
				updated.Annotations["upgrade-test.kserve.io/touch"] = "1"
			})).To(Succeed())

			// The upgraded controller removes the branch policies once the
			// per-component replacement exists, without touching the workloads.
			assertPoliciesAbsent(ctx, testNs.Name, []string{
				kmeta.ChildName(llmSvc.Name, "-networkpolicy-allow-all"),
				kmeta.ChildName(llmSvc.Name, "-networkpolicy-egress"),
				kmeta.ChildName(llmSvc.Name, "-prometheus-scraping"),
			})
			assertWorkloadsUnchanged(ctx, testNs.Name, llmSvc.Name, "after the policy migration", main, prefill)

			// The mode toggles must not churn the per-component egress grants:
			// capture their identity after the migration and require it after
			// every toggle.
			allowAllEgressNames := childNames(llmSvc.Name, managedAllowAllEgressPolicySuffixes())
			egressGrantUIDs := func() map[string]types.UID {
				GinkgoHelper()
				grants := map[string]types.UID{}
				policies := &netv1.NetworkPolicyList{}
				Expect(envTest.List(ctx, policies, client.InNamespace(testNs.Name))).To(Succeed())
				for i := range policies.Items {
					if slices.Contains(allowAllEgressNames, policies.Items[i].Name) {
						grants[policies.Items[i].Name] = policies.Items[i].UID
					}
				}
				return grants
			}
			grantsAfterMigration := egressGrantUIDs()
			Expect(grantsAfterMigration).To(HaveLen(2), "each stored workload keeps exactly one allow-all egress grant")

			// Toggling the ingress mode changes only the policies.
			EnableStrictNetworkPolicy(ctx, envTest.Client)
			waitForNetworkPolicy(ctx, testNs.Name, kmeta.ChildName(llmSvc.Name, "-networkpolicy-strict-main"))
			assertPoliciesAbsent(ctx, testNs.Name, childNames(llmSvc.Name, slices.Concat(
				managedIngressPolicySuffixes("default"),
				[]string{"-networkpolicy-allow-all", "-networkpolicy-egress"})))
			assertWorkloadsUnchanged(ctx, testNs.Name, llmSvc.Name, "after enabling strict mode", main, prefill)
			Expect(egressGrantUIDs()).To(Equal(grantsAfterMigration), "enabling strict mode must not churn the egress grants")

			PatchLLMISVCConfig(ctx, envTest.Client, func(cfg *llmisvc.LLMISVCConfig) {
				cfg.FeatureGates.StrictNetworkPolicy = false
			})
			waitForNetworkPolicy(ctx, testNs.Name, kmeta.ChildName(llmSvc.Name, "-networkpolicy-default-main"))
			assertPoliciesAbsent(ctx, testNs.Name, childNames(llmSvc.Name, slices.Concat(
				managedIngressPolicySuffixes("strict"),
				[]string{"-networkpolicy-allow-all", "-networkpolicy-egress"})))
			assertWorkloadsUnchanged(ctx, testNs.Name, llmSvc.Name, "after disabling strict mode", main, prefill)
			Expect(egressGrantUIDs()).To(Equal(grantsAfterMigration), "disabling strict mode must not churn the egress grants")

			// After the successful migration every generated policy is either an
			// ingress-only per-component policy with no egress rules or an
			// egress-only per-component allow-all grant with exactly one
			// unrestricted egress rule — one grant per stored workload.
			Consistently(func(g Gomega, ctx context.Context) {
				policies := &netv1.NetworkPolicyList{}
				g.Expect(envTest.List(ctx, policies, client.InNamespace(testNs.Name))).To(Succeed())
				g.Expect(policies.Items).NotTo(BeEmpty())
				egressGrants := 0
				for i := range policies.Items {
					policy := &policies.Items[i]
					if slices.Contains(allowAllEgressNames, policy.Name) {
						egressGrants++
						g.Expect(policy.Spec.PolicyTypes).To(ConsistOf(netv1.PolicyTypeEgress), "%s must be egress-only", policy.Name)
						g.Expect(policy.Spec.Ingress).To(BeEmpty(), "%s must not create ingress rules", policy.Name)
						g.Expect(policy.Spec.Egress).To(HaveLen(1), "%s must carry exactly one allow-all egress rule", policy.Name)
						g.Expect(policy.Spec.Egress[0].To).To(BeEmpty(), "%s must not restrict egress destinations", policy.Name)
						g.Expect(policy.Spec.Egress[0].Ports).To(BeEmpty(), "%s must not restrict egress ports", policy.Name)
						continue
					}
					g.Expect(policy.Spec.PolicyTypes).To(ConsistOf(netv1.PolicyTypeIngress), "%s must be ingress-only", policy.Name)
					g.Expect(policy.Spec.Egress).To(BeEmpty(), "%s must not create egress rules", policy.Name)
				}
				g.Expect(egressGrants).To(Equal(2), "each of the two stored workloads keeps exactly one allow-all egress grant")
			}).WithContext(ctx).Should(Succeed())
		})

		It("keeps an existing-tracing service's workloads stable across mode toggles", func(ctx SpecContext) {
			testNs := NewTestNamespace(ctx, envTest)
			llmSvc := LLMInferenceService("test-llm-netpol-tracing-upgrade",
				InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
				WithModelURI("hf://facebook/opt-125m"),
				WithTemplate(SimpleWorkerPodSpec()),
				WithPrefill(SimpleWorkerPodSpec()),
				WithAnnotations(map[string]string{constants.EnableTracingEgressNetworkPolicyAnnotationKey: "true"}),
			)
			llmSvc.Spec.Tracing = &v1alpha2.TracingSpec{}
			Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
			defer testNs.DeleteAndWait(ctx, llmSvc)

			waitForNetworkPolicy(ctx, testNs.Name, kmeta.ChildName(llmSvc.Name, "-networkpolicy-default-main"))
			tracing := waitForTracingNetworkPolicy(ctx, testNs.Name, llmSvc.Name)
			// The tracing policy keeps selecting through the pre-existing
			// application labels in both ingress modes.
			Expect(tracing.Spec.PodSelector.MatchLabels).To(Equal(map[string]string{
				constants.KubernetesPartOfLabelKey:  constants.LLMInferenceServicePartOfValue,
				constants.KubernetesAppNameLabelKey: llmSvc.Name,
			}))
			// Opted-in tracing removes every owned unrestricted egress grant.
			Eventually(func(g Gomega, ctx context.Context) {
				for _, suffix := range managedAllowAllEgressPolicySuffixes() {
					name := kmeta.ChildName(llmSvc.Name, suffix)
					err := envTest.Get(ctx, types.NamespacedName{Name: name, Namespace: testNs.Name}, &netv1.NetworkPolicy{})
					g.Expect(apierrors.IsNotFound(err)).To(BeTrue(), "unrestricted egress grant %s must not exist while tracing is opted in", name)
				}
			}).WithContext(ctx).Should(Succeed())
			main, prefill := waitForWorkloads(ctx, testNs.Name, llmSvc.Name)

			EnableStrictNetworkPolicy(ctx, envTest.Client)
			waitForNetworkPolicy(ctx, testNs.Name, kmeta.ChildName(llmSvc.Name, "-networkpolicy-strict-main"))
			assertWorkloadsUnchanged(ctx, testNs.Name, llmSvc.Name, "after enabling strict mode", main, prefill)
			strictTracing := waitForTracingNetworkPolicy(ctx, testNs.Name, llmSvc.Name)
			Expect(strictTracing.Spec).To(Equal(tracing.Spec), "the tracing policy must not change with the ingress mode")

			PatchLLMISVCConfig(ctx, envTest.Client, func(cfg *llmisvc.LLMISVCConfig) {
				cfg.FeatureGates.StrictNetworkPolicy = false
			})
			waitForNetworkPolicy(ctx, testNs.Name, kmeta.ChildName(llmSvc.Name, "-networkpolicy-default-main"))
			assertWorkloadsUnchanged(ctx, testNs.Name, llmSvc.Name, "after disabling strict mode", main, prefill)
		})

		It("does not roll out a service pinned to presets without the revision placeholder", func(ctx SpecContext) {
			testNs := NewTestNamespace(ctx, envTest)
			// Namespace-local copies of the P/D template presets, pinned to the
			// pre-revision shape: no serving.kserve.io/llmisvc-revision
			// placeholder in their spec labels.
			pinnedDecode := LLMInferenceServiceConfig("kserve-config-llm-decode-template",
				InNamespace[*v1alpha2.LLMInferenceServiceConfig](testNs.Name),
				WithConfigWorkloadTemplate(SimpleWorkerPodSpec()),
			)
			pinnedPrefill := LLMInferenceServiceConfig("kserve-config-llm-prefill-template",
				InNamespace[*v1alpha2.LLMInferenceServiceConfig](testNs.Name),
			)
			pinnedPrefill.Spec.Prefill = &v1alpha2.WorkloadSpec{Template: SimpleWorkerPodSpec()}
			Expect(envTest.Create(ctx, pinnedDecode)).To(Succeed())
			Expect(envTest.Create(ctx, pinnedPrefill)).To(Succeed())

			llmSvc := LLMInferenceService("test-llm-netpol-pinned",
				InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
				WithModelURI("hf://facebook/opt-125m"),
				WithTemplate(SimpleWorkerPodSpec()),
				WithPrefill(SimpleWorkerPodSpec()),
			)
			Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
			defer testNs.DeleteAndWait(ctx, llmSvc)

			waitForNetworkPolicy(ctx, testNs.Name, kmeta.ChildName(llmSvc.Name, "-networkpolicy-default-main"))
			main, prefill := waitForWorkloads(ctx, testNs.Name, llmSvc.Name)
			for _, deployment := range []*appsv1.Deployment{main, prefill} {
				Expect(deployment.Spec.Template.Labels).NotTo(HaveKey(constants.LLMInferenceServiceRevisionLabelKey),
					"a service pinned to presets without the revision placeholder must not gain a revision label on upgrade")
			}

			EnableStrictNetworkPolicy(ctx, envTest.Client)
			waitForNetworkPolicy(ctx, testNs.Name, kmeta.ChildName(llmSvc.Name, "-networkpolicy-strict-main"))
			assertWorkloadsUnchanged(ctx, testNs.Name, llmSvc.Name, "after enabling strict mode", main, prefill)

			PatchLLMISVCConfig(ctx, envTest.Client, func(cfg *llmisvc.LLMISVCConfig) {
				cfg.FeatureGates.StrictNetworkPolicy = false
			})
			waitForNetworkPolicy(ctx, testNs.Name, kmeta.ChildName(llmSvc.Name, "-networkpolicy-default-main"))
			assertWorkloadsUnchanged(ctx, testNs.Name, llmSvc.Name, "after disabling strict mode", main, prefill)
			for _, deployment := range []*appsv1.Deployment{main, prefill} {
				Expect(deployment.Spec.Template.Labels).NotTo(HaveKey(constants.LLMInferenceServiceRevisionLabelKey),
					"mode toggles must not introduce a revision label")
			}
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

// managedIngressPolicySuffixes lists the per-component policy name suffixes of
// one ingress mode, mirroring the controller's bounded component-key set from
// outside the package.
func managedIngressPolicySuffixes(mode string) []string {
	components := []string{"main", "prefill", "scheduler", "tokenizer", "main-mn", "prefill-mn", "pd"}
	suffixes := make([]string, 0, len(components))
	for _, component := range components {
		suffixes = append(suffixes, "-networkpolicy-"+mode+"-"+component)
	}
	return suffixes
}

// managedAllowAllEgressPolicySuffixes lists the per-component allow-all
// egress policy name suffixes, mirroring the controller's bounded
// component-key set from outside the package. The retired legacy general
// egress suffix "-networkpolicy-egress" carries no component key and is
// deliberately not part of this set.
func managedAllowAllEgressPolicySuffixes() []string {
	components := []string{"main", "prefill", "scheduler", "tokenizer", "main-mn", "prefill-mn", "pd"}
	suffixes := make([]string, 0, len(components))
	for _, component := range components {
		suffixes = append(suffixes, "-networkpolicy-egress-"+component)
	}
	return suffixes
}
