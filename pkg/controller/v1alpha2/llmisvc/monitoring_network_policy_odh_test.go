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
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"knative.dev/pkg/kmeta"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
	"github.com/kserve/kserve/pkg/constants"
	. "github.com/kserve/kserve/pkg/controller/v1alpha2/llmisvc/fixture"
)

var _ = Describe("LLMInferenceService Monitoring NetworkPolicy", func() {
	Context("NetworkPolicy Reconciliation", func() {
		It("should create a per-service NetworkPolicy allowing Prometheus scraping when llmisvc is created", func(ctx SpecContext) {
			// given
			GinkgoT().Setenv("MONITORING_NAMESPACE", "test-monitoring-ns")

			svcName := "test-llm-netpol"
			testNs := NewTestNamespace(ctx, envTest)

			llmSvc := LLMInferenceService(svcName,
				InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
				WithModelURI("hf://facebook/opt-125m"),
			)

			// when
			Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
			defer func() {
				testNs.DeleteAndWait(ctx, llmSvc)
			}()

			// then - verify NetworkPolicy is created with correct spec
			np := waitForMonitoringNetworkPolicy(ctx, testNs.Name, svcName)

			Expect(np.Labels).To(HaveKeyWithValue(constants.KubernetesComponentLabelKey, "llm-monitoring"))
			Expect(np.Labels).To(HaveKeyWithValue(constants.KubernetesPartOfLabelKey, constants.LLMInferenceServicePartOfValue))
			Expect(np.Labels).To(HaveKeyWithValue(constants.KubernetesAppNameLabelKey, svcName))
			Expect(np.OwnerReferences).To(HaveLen(1))
			Expect(np.OwnerReferences[0].Name).To(Equal(svcName))
			Expect(np.OwnerReferences[0].Controller).To(Equal(ptr.To(true)))

			Expect(np.Spec.PodSelector.MatchLabels).To(HaveKeyWithValue(
				constants.KubernetesPartOfLabelKey, constants.LLMInferenceServicePartOfValue))
			Expect(np.Spec.PodSelector.MatchLabels).To(HaveKeyWithValue(
				constants.KubernetesAppNameLabelKey, svcName))

			Expect(np.Spec.Ingress).To(HaveLen(3))

			prometheusRule := np.Spec.Ingress[0]
			Expect(prometheusPeerNamespacesFromRule(prometheusRule)).To(ConsistOf(
				"test-monitoring-ns",
				"openshift-monitoring",
				"openshift-user-workload-monitoring",
				"redhat-ods-monitoring",
			))
			Expect(prometheusRule.Ports).To(HaveLen(2))
			Expect(prometheusRule.Ports).To(ContainElements(
				netv1.NetworkPolicyPort{
					Protocol: ptr.To(corev1.ProtocolTCP),
					Port:     ptr.To(intstr.FromInt32(8000)),
				},
				netv1.NetworkPolicyPort{
					Protocol: ptr.To(corev1.ProtocolTCP),
					Port:     ptr.To(intstr.FromInt32(9090)),
				},
			))

			// Rule 2: API Gateway from ingress namespace to reach vLLM and EPP ext_proc
			gatewayRule := np.Spec.Ingress[1]
			Expect(gatewayRule.From).To(HaveLen(1))
			Expect(gatewayRule.From[0].NamespaceSelector.MatchLabels).To(
				HaveKeyWithValue("kubernetes.io/metadata.name", "kserve"))
			Expect(gatewayRule.Ports).To(HaveLen(2))
			Expect(gatewayRule.Ports).To(ContainElements(
				netv1.NetworkPolicyPort{
					Protocol: ptr.To(corev1.ProtocolTCP),
					Port:     ptr.To(intstr.FromInt32(8000)),
				},
				netv1.NetworkPolicyPort{
					Protocol: ptr.To(corev1.ProtocolTCP),
					Port:     ptr.To(intstr.FromInt32(9002)),
				},
			))

			// Rule 3: All pods within the namespace can communicate on any port
			intraNamespaceRule := np.Spec.Ingress[2]
			Expect(intraNamespaceRule.From).To(HaveLen(1))
			Expect(intraNamespaceRule.From[0].PodSelector.MatchLabels).To(BeEmpty())
			Expect(intraNamespaceRule.Ports).To(BeNil())
		})

		It("should include RHOAI monitoring without dropping OpenShift defaults when MONITORING_NAMESPACE is unset", func(ctx SpecContext) {
			// given - operator skips injecting the env var when monitoring is unset
			if old, had := os.LookupEnv("MONITORING_NAMESPACE"); had {
				Expect(os.Unsetenv("MONITORING_NAMESPACE")).To(Succeed())
				DeferCleanup(func() { Expect(os.Setenv("MONITORING_NAMESPACE", old)).To(Succeed()) })
			}

			svcName := "test-llm-netpol-default-ns"
			testNs := NewTestNamespace(ctx, envTest)

			llmSvc := LLMInferenceService(svcName,
				InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
				WithModelURI("hf://facebook/opt-125m"),
			)

			// when
			Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
			defer func() {
				testNs.DeleteAndWait(ctx, llmSvc)
			}()

			// then - the ingress rule targets the default namespaces
			np := waitForMonitoringNetworkPolicy(ctx, testNs.Name, svcName)
			Expect(np.Spec.Ingress).To(HaveLen(3))
			Expect(prometheusPeerNamespacesFromRule(np.Spec.Ingress[0])).To(ConsistOf(
				"openshift-monitoring",
				"openshift-user-workload-monitoring",
				"redhat-ods-monitoring",
			))
		})

		It("should allow a cross-namespace custom gateway.ref without dropping the managed Gateway", func(ctx SpecContext) {
			svcName := "test-llm-netpol-custom-gw"
			testNs := NewTestNamespace(ctx, envTest)
			gwNs := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: kmeta.ChildName(testNs.Name, "-gw")}}
			Expect(envTest.Create(ctx, gwNs)).To(Succeed())
			defer func() {
				_ = envTest.Delete(ctx, gwNs)
			}()

			customGateway := Gateway("my-custom-gateway",
				InNamespace[*gwapiv1.Gateway](gwNs.Name),
				WithListener(gwapiv1.HTTPProtocolType),
				WithAddresses("203.0.113.42"),
			)
			Expect(envTest.Create(ctx, customGateway)).To(Succeed())
			defer func() {
				_ = envTest.Delete(ctx, customGateway)
			}()

			llmSvc := LLMInferenceService(svcName,
				InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
				WithModelURI("hf://facebook/opt-125m"),
				WithManagedRoute(),
				WithGatewayRefs(LLMGatewayRef("my-custom-gateway", gwNs.Name)),
			)

			Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
			defer func() {
				testNs.DeleteAndWait(ctx, llmSvc)
			}()

			np := waitForMonitoringNetworkPolicy(ctx, testNs.Name, svcName)
			gatewayRule := np.Spec.Ingress[1]
			Expect(gatewayPeerNamespaces(gatewayRule)).To(ConsistOf("kserve", gwNs.Name))
			Expect(np.Spec.Ingress[2].From[0].PodSelector.MatchLabels).To(BeEmpty())
		})

		It("should not create the NetworkPolicy when the service is force-stopped", func(ctx SpecContext) {
			// given - a service force-stopped from creation via the stop annotation
			svcName := "test-llm-netpol-stopped"
			testNs := NewTestNamespace(ctx, envTest)

			llmSvc := LLMInferenceService(svcName,
				InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
				WithModelURI("hf://facebook/opt-125m"),
				WithAnnotations(map[string]string{constants.StopAnnotationKey: "true"}),
			)

			// when
			Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
			defer func() {
				testNs.DeleteAndWait(ctx, llmSvc)
			}()

			// then - NetworkPolicy is never created while the service is stopped
			Consistently(func(g Gomega, ctx context.Context) {
				err := envTest.Get(ctx, types.NamespacedName{
					Name:      kmeta.ChildName(svcName, "-prometheus-scraping"),
					Namespace: testNs.Name,
				}, &netv1.NetworkPolicy{})
				g.Expect(apierrors.IsNotFound(err)).To(BeTrue())
			}).WithContext(ctx).Should(Succeed(), "NetworkPolicy should not be created for a force-stopped service")
		})

		It("should keep the other service NetworkPolicy when one of multiple llmisvcs is deleted", func(ctx SpecContext) {
			// given
			svcName := "test-llm-netpol-skip"
			testNs := NewTestNamespace(ctx, envTest)

			llmSvc1 := LLMInferenceService(svcName+"-1",
				InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
				WithModelURI("hf://facebook/opt-125m"),
			)
			llmSvc2 := LLMInferenceService(svcName+"-2",
				InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
				WithModelURI("hf://facebook/opt-125m"),
			)

			// when - create both services
			Expect(envTest.Create(ctx, llmSvc1)).To(Succeed())
			Expect(envTest.Create(ctx, llmSvc2)).To(Succeed())

			waitForMonitoringNetworkPolicy(ctx, testNs.Name, llmSvc1.Name)
			waitForMonitoringNetworkPolicy(ctx, testNs.Name, llmSvc2.Name)

			// when - delete only the first service
			Expect(envTest.Delete(ctx, llmSvc1)).To(Succeed())

			// then - first service policy is removed, second remains
			Eventually(func(g Gomega, ctx context.Context) {
				err := envTest.Get(ctx, types.NamespacedName{
					Name:      kmeta.ChildName(llmSvc1.Name, "-prometheus-scraping"),
					Namespace: testNs.Name,
				}, &netv1.NetworkPolicy{})
				g.Expect(apierrors.IsNotFound(err)).To(BeTrue())
			}).WithContext(ctx).Should(Succeed())

			Consistently(func(g Gomega, ctx context.Context) {
				g.Expect(envTest.Get(ctx, types.NamespacedName{
					Name:      kmeta.ChildName(llmSvc2.Name, "-prometheus-scraping"),
					Namespace: testNs.Name,
				}, &netv1.NetworkPolicy{})).To(Succeed())
			}).WithContext(ctx).Should(Succeed())

			testNs.DeleteAndWait(ctx, llmSvc2)
		})

		It("should cleanup NetworkPolicy when the last llmisvc is deleted", func(ctx SpecContext) {
			// given
			svcName := "test-llm-netpol-last"
			testNs := NewTestNamespace(ctx, envTest)

			llmSvc := LLMInferenceService(svcName,
				InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
				WithModelURI("hf://facebook/opt-125m"),
			)

			// when - create service
			Expect(envTest.Create(ctx, llmSvc)).To(Succeed())

			waitForMonitoringNetworkPolicy(ctx, testNs.Name, svcName)

			// when - delete the last (and only) service
			testNs.DeleteAndWait(ctx, llmSvc)

			// then - NetworkPolicy should be deleted
			Eventually(func(ctx context.Context) bool {
				np := &netv1.NetworkPolicy{}
				err := envTest.Get(ctx, types.NamespacedName{
					Name:      kmeta.ChildName(svcName, "-prometheus-scraping"),
					Namespace: testNs.Name,
				}, np)
				return err != nil && apierrors.IsNotFound(err)
			}).WithContext(ctx).Should(BeTrue(), "monitoring NetworkPolicy should be deleted")
		})
	})
})

func waitForMonitoringNetworkPolicy(ctx context.Context, nsName, svcName string) *netv1.NetworkPolicy {
	np := &netv1.NetworkPolicy{}
	Eventually(func(_ Gomega, ctx context.Context) error {
		return envTest.Get(ctx, types.NamespacedName{
			Name:      kmeta.ChildName(svcName, "-prometheus-scraping"),
			Namespace: nsName,
		}, np)
	}).WithContext(ctx).Should(Succeed())

	return np
}

func prometheusPeerNamespacesFromRule(rule netv1.NetworkPolicyIngressRule) []string {
	return namespaceSelectorPeerNamespaces(rule)
}

func gatewayPeerNamespaces(rule netv1.NetworkPolicyIngressRule) []string {
	return namespaceSelectorPeerNamespaces(rule)
}

func namespaceSelectorPeerNamespaces(rule netv1.NetworkPolicyIngressRule) []string {
	nss := make([]string, 0, len(rule.From))
	for _, from := range rule.From {
		if from.NamespaceSelector == nil {
			continue
		}
		if ns, ok := from.NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"]; ok {
			nss = append(nss, ns)
		}
	}
	return nss
}
