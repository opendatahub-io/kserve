//go:build distro

/*
Copyright 2025 The KServe Authors.

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

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
	"github.com/kserve/kserve/pkg/constants"
	. "github.com/kserve/kserve/pkg/controller/v1alpha2/llmisvc/fixture"
)

var _ = Describe("LLMInferenceService Monitoring NetworkPolicy", func() {
	Context("NetworkPolicy Reconciliation", func() {
		It("should create a NetworkPolicy allowing Prometheus scraping when llmisvc is created", func(ctx SpecContext) {
			// given
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
			np := waitForMonitoringNetworkPolicy(ctx, testNs.Name)

			Expect(np.Labels).To(HaveKeyWithValue(constants.KubernetesComponentLabelKey, "llm-monitoring"))
			Expect(np.Labels).To(HaveKeyWithValue(constants.KubernetesPartOfLabelKey, constants.LLMInferenceServicePartOfValue))

			Expect(np.Spec.PodSelector.MatchLabels).To(HaveKeyWithValue(
				constants.KubernetesPartOfLabelKey, constants.LLMInferenceServicePartOfValue))

			Expect(np.Spec.PolicyTypes).To(ConsistOf(netv1.PolicyTypeIngress))
			Expect(np.Spec.Ingress).To(HaveLen(1))

			ingressRule := np.Spec.Ingress[0]
			Expect(ingressRule.From).To(HaveLen(1))
			Expect(ingressRule.From[0].NamespaceSelector.MatchLabels).To(
				HaveKeyWithValue("kubernetes.io/metadata.name", "redhat-ods-monitoring"))

			Expect(ingressRule.Ports).To(HaveLen(2))
			Expect(ingressRule.Ports).To(ContainElements(
				netv1.NetworkPolicyPort{
					Protocol: ptr.To(corev1.ProtocolTCP),
					Port:     ptr.To(intstr.FromInt32(8000)),
				},
				netv1.NetworkPolicyPort{
					Protocol: ptr.To(corev1.ProtocolTCP),
					Port:     ptr.To(intstr.FromInt32(9090)),
				},
			))
		})

		It("should keep NetworkPolicy when one of multiple llmisvcs is deleted", func(ctx SpecContext) {
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

			waitForMonitoringNetworkPolicy(ctx, testNs.Name)

			// when - delete only the first service
			Expect(envTest.Delete(ctx, llmSvc1)).To(Succeed())

			// then - NetworkPolicy should still exist
			np := &netv1.NetworkPolicy{}
			Consistently(func(g Gomega, ctx context.Context) {
				g.Expect(envTest.Get(ctx, types.NamespacedName{
					Name:      "kserve-llm-isvc-prometheus-scraping",
					Namespace: testNs.Name,
				}, np)).To(Succeed())
			}).WithContext(ctx).Should(Succeed())
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

			waitForMonitoringNetworkPolicy(ctx, testNs.Name)

			// when - delete the last (and only) service
			Expect(envTest.Delete(ctx, llmSvc)).To(Succeed())

			// then - NetworkPolicy should be deleted
			Eventually(func(ctx context.Context) bool {
				np := &netv1.NetworkPolicy{}
				err := envTest.Get(ctx, types.NamespacedName{
					Name:      "kserve-llm-isvc-prometheus-scraping",
					Namespace: testNs.Name,
				}, np)
				return err != nil && apierrors.IsNotFound(err)
			}).WithContext(ctx).Should(BeTrue(), "monitoring NetworkPolicy should be deleted")
		})
	})
})

func waitForMonitoringNetworkPolicy(ctx context.Context, nsName string) *netv1.NetworkPolicy {
	np := &netv1.NetworkPolicy{}
	Eventually(func(_ Gomega, ctx context.Context) error {
		return envTest.Get(ctx, types.NamespacedName{
			Name:      "kserve-llm-isvc-prometheus-scraping",
			Namespace: nsName,
		}, np)
	}).WithContext(ctx).Should(Succeed())

	return np
}
