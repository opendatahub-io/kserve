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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	"knative.dev/pkg/kmeta"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
	"github.com/kserve/kserve/pkg/constants"
	. "github.com/kserve/kserve/pkg/controller/v1alpha2/llmisvc/fixture"
)

var _ = Describe("LLMInferenceService tracing NetworkPolicy", func() {
	It("creates a comprehensive per-service OTLP egress policy", func(ctx SpecContext) {
		testNs := NewTestNamespace(ctx, envTest)
		collectorNs, collectorSvc := createOTLPTestService(ctx, testNs.Name, "otel-collector", map[string]string{"app": "otel-collector"}, 4317)
		defer deleteOTLPTestService(ctx, collectorNs, collectorSvc)
		llmSvc := LLMInferenceService("test-llm-tracing-netpol",
			InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
			WithModelURI("hf://facebook/opt-125m"),
			WithAnnotations(map[string]string{constants.EnableTracingEgressNetworkPolicyAnnotationKey: "true"}),
		)
		llmSvc.Spec.Tracing = &v1alpha2.TracingSpec{ExporterEndpoint: ptr.To("http://otel-collector." + collectorNs.Name + ".svc.cluster.local:4317")}

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
		Expect(np.Spec.Egress[1].Ports).To(HaveLen(1))
		Expect(np.Spec.Egress[1].Ports[0].Protocol).To(Equal(ptr.To(corev1.ProtocolTCP)))
		Expect(np.Spec.Egress[1].To).To(HaveLen(1))
		Expect(np.Spec.Egress[1].To[0].IPBlock).ToNot(BeNil())
		Expect(np.Spec.Egress[2].To).To(HaveLen(1))
		Expect(np.Spec.Egress[2].To[0].PodSelector.MatchLabels).To(BeEmpty())
		Expect(np.Spec.Egress[3].Ports).To(ContainElement(
			netv1.NetworkPolicyPort{Protocol: ptr.To(corev1.ProtocolTCP), Port: ptr.To(intstr.FromInt32(4317))},
		))
		Expect(namespaceNamesFromEgressRule(np.Spec.Egress[3])).To(ConsistOf(collectorNs.Name))
		Expect(np.Spec.Egress[3].To[0].PodSelector.MatchLabels).To(Equal(map[string]string{"app": "otel-collector"}))

		monitoringNP := &netv1.NetworkPolicy{}
		Expect(envTest.Get(ctx, types.NamespacedName{
			Name:      kmeta.ChildName(llmSvc.Name, "-prometheus-scraping"),
			Namespace: testNs.Name,
		}, monitoringNP)).To(Succeed())
	})

	It("allows a custom cross-namespace OTLP endpoint and port", func(ctx SpecContext) {
		testNs := NewTestNamespace(ctx, envTest)
		collectorNs, collectorSvc := createOTLPTestService(ctx, testNs.Name, "jaeger", map[string]string{"app": "jaeger"}, 4318)
		defer deleteOTLPTestService(ctx, collectorNs, collectorSvc)
		llmSvc := LLMInferenceService("test-llm-tracing-custom-endpoint",
			InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
			WithModelURI("hf://facebook/opt-125m"),
			WithAnnotations(map[string]string{constants.EnableTracingEgressNetworkPolicyAnnotationKey: "true"}),
		)
		llmSvc.Spec.Tracing = &v1alpha2.TracingSpec{ExporterEndpoint: ptr.To("http://jaeger." + collectorNs.Name + ".svc:4318")}
		Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
		defer testNs.DeleteAndWait(ctx, llmSvc)

		np := waitForTracingNetworkPolicy(ctx, testNs.Name, llmSvc.Name)
		Expect(np.Spec.Egress).To(HaveLen(4))
		Expect(namespaceNamesFromEgressRule(np.Spec.Egress[3])).To(ConsistOf(collectorNs.Name))
		Expect(np.Spec.Egress[3].To[0].PodSelector.MatchLabels).To(Equal(map[string]string{"app": "jaeger"}))
		Expect(np.Spec.Egress[3].Ports).To(ContainElement(
			netv1.NetworkPolicyPort{Protocol: ptr.To(corev1.ProtocolTCP), Port: ptr.To(intstr.FromInt32(4318))},
		))
	})

	It("updates the OTLP rule when the referenced Service changes or is deleted", func(ctx SpecContext) {
		testNs := NewTestNamespace(ctx, envTest)
		collectorNs, collectorSvc := createOTLPTestService(ctx, testNs.Name, "otel-collector", map[string]string{"app": "otel-collector"}, 4317)
		defer deleteOTLPTestService(ctx, collectorNs, collectorSvc)
		llmSvc := LLMInferenceService("test-llm-tracing-service-change",
			InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
			WithModelURI("hf://facebook/opt-125m"),
			WithAnnotations(map[string]string{constants.EnableTracingEgressNetworkPolicyAnnotationKey: "true"}),
		)
		llmSvc.Spec.Tracing = &v1alpha2.TracingSpec{ExporterEndpoint: ptr.To("http://otel-collector." + collectorNs.Name + ".svc:4317")}
		Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
		defer testNs.DeleteAndWait(ctx, llmSvc)

		np := waitForTracingNetworkPolicy(ctx, testNs.Name, llmSvc.Name)
		Expect(np.Spec.Egress[3].To[0].PodSelector.MatchLabels).To(Equal(map[string]string{"app": "otel-collector"}))

		Expect(retry.RetryOnConflict(retry.DefaultRetry, func() error {
			updated := &corev1.Service{}
			if err := envTest.Get(ctx, types.NamespacedName{Name: collectorSvc.Name, Namespace: collectorNs.Name}, updated); err != nil {
				return err
			}
			updated.Spec.Selector = map[string]string{"app": "otel-collector-v2"}
			return envTest.Update(ctx, updated)
		})).To(Succeed())

		Eventually(func(g Gomega, ctx context.Context) {
			updated := &netv1.NetworkPolicy{}
			g.Expect(envTest.Get(ctx, types.NamespacedName{Name: np.Name, Namespace: testNs.Name}, updated)).To(Succeed())
			g.Expect(updated.Spec.Egress[3].To[0].PodSelector.MatchLabels).To(Equal(map[string]string{"app": "otel-collector-v2"}))
		}).WithContext(ctx).Should(Succeed())

		Expect(envTest.Delete(ctx, collectorSvc)).To(Succeed())
		Eventually(func(g Gomega, ctx context.Context) {
			updated := &netv1.NetworkPolicy{}
			g.Expect(envTest.Get(ctx, types.NamespacedName{Name: np.Name, Namespace: testNs.Name}, updated)).To(Succeed())
			g.Expect(updated.Spec.Egress).To(HaveLen(3))
		}).WithContext(ctx).Should(Succeed())
	})

	It("adds the OTLP rule when the referenced Service is created later", func(ctx SpecContext) {
		testNs := NewTestNamespace(ctx, envTest)
		collectorNs := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: kmeta.ChildName(testNs.Name, "-collector")}}
		Expect(envTest.Create(ctx, collectorNs)).To(Succeed())
		defer func() {
			_ = envTest.Delete(ctx, collectorNs)
		}()

		llmSvc := LLMInferenceService("test-llm-tracing-late-service",
			InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
			WithModelURI("hf://facebook/opt-125m"),
			WithAnnotations(map[string]string{constants.EnableTracingEgressNetworkPolicyAnnotationKey: "true"}),
		)
		llmSvc.Spec.Tracing = &v1alpha2.TracingSpec{ExporterEndpoint: ptr.To("http://otel-collector." + collectorNs.Name + ".svc.cluster.local:4317")}
		Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
		defer testNs.DeleteAndWait(ctx, llmSvc)

		np := waitForTracingNetworkPolicy(ctx, testNs.Name, llmSvc.Name)
		Expect(np.Spec.Egress).To(HaveLen(3))

		collectorSvc := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "otel-collector", Namespace: collectorNs.Name},
			Spec: corev1.ServiceSpec{
				Selector: map[string]string{"app": "otel-collector"},
				Ports:    []corev1.ServicePort{{Name: "otlp", Port: 4317}},
			},
		}
		Expect(envTest.Create(ctx, collectorSvc)).To(Succeed())
		defer func() {
			_ = envTest.Delete(ctx, collectorSvc)
		}()

		Eventually(func(g Gomega, ctx context.Context) {
			updated := &netv1.NetworkPolicy{}
			g.Expect(envTest.Get(ctx, types.NamespacedName{Name: np.Name, Namespace: testNs.Name}, updated)).To(Succeed())
			g.Expect(updated.Spec.Egress).To(HaveLen(4))
			g.Expect(updated.Spec.Egress[3].To[0].PodSelector.MatchLabels).To(Equal(map[string]string{"app": "otel-collector"}))
		}).WithContext(ctx).Should(Succeed())
	})

	It("updates the OTLP rule when the endpoint comes from a baseRef", func(ctx SpecContext) {
		testNs := NewTestNamespace(ctx, envTest)
		collectorNs := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: kmeta.ChildName(testNs.Name, "-collector")}}
		Expect(envTest.Create(ctx, collectorNs)).To(Succeed())
		defer func() {
			_ = envTest.Delete(ctx, collectorNs)
		}()

		config := &v1alpha2.LLMInferenceServiceConfig{
			ObjectMeta: metav1.ObjectMeta{Name: "tracing-config", Namespace: testNs.Name},
			Spec: v1alpha2.LLMInferenceServiceSpec{Tracing: &v1alpha2.TracingSpec{
				ExporterEndpoint: ptr.To("http://otel-collector." + collectorNs.Name + ".svc.cluster.local:4317"),
			}},
		}
		Expect(envTest.Create(ctx, config)).To(Succeed())
		defer func() {
			_ = envTest.Delete(ctx, config)
		}()

		llmSvc := LLMInferenceService("test-llm-tracing-baseref",
			InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
			WithModelURI("hf://facebook/opt-125m"),
			WithBaseRefs(corev1.LocalObjectReference{Name: config.Name}),
			WithAnnotations(map[string]string{constants.EnableTracingEgressNetworkPolicyAnnotationKey: "true"}),
		)
		Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
		defer testNs.DeleteAndWait(ctx, llmSvc)

		np := waitForTracingNetworkPolicy(ctx, testNs.Name, llmSvc.Name)
		Expect(np.Spec.Egress).To(HaveLen(3))

		collectorSvc := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "otel-collector", Namespace: collectorNs.Name},
			Spec: corev1.ServiceSpec{
				Selector: map[string]string{"app": "otel-collector"},
				Ports:    []corev1.ServicePort{{Name: "otlp", Port: 4317}},
			},
		}
		Expect(envTest.Create(ctx, collectorSvc)).To(Succeed())
		defer func() {
			_ = envTest.Delete(ctx, collectorSvc)
		}()

		Eventually(func(g Gomega, ctx context.Context) {
			updated := &netv1.NetworkPolicy{}
			g.Expect(envTest.Get(ctx, types.NamespacedName{Name: np.Name, Namespace: testNs.Name}, updated)).To(Succeed())
			g.Expect(updated.Spec.Egress).To(HaveLen(4))
			g.Expect(updated.Spec.Egress[3].To[0].PodSelector.MatchLabels).To(Equal(map[string]string{"app": "otel-collector"}))
		}).WithContext(ctx).Should(Succeed())
	})

	It("removes the policy when tracing is cleared", func(ctx SpecContext) {
		testNs := NewTestNamespace(ctx, envTest)
		llmSvc := LLMInferenceService("test-llm-tracing-clear",
			InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
			WithModelURI("hf://facebook/opt-125m"),
			WithAnnotations(map[string]string{constants.EnableTracingEgressNetworkPolicyAnnotationKey: "true"}),
		)
		llmSvc.Spec.Tracing = &v1alpha2.TracingSpec{}
		Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
		defer testNs.DeleteAndWait(ctx, llmSvc)
		waitForTracingNetworkPolicy(ctx, testNs.Name, llmSvc.Name)

		Expect(updateLLMInferenceServiceWithRetry(ctx, testNs.Name, llmSvc.Name, func(updated *v1alpha2.LLMInferenceService) {
			updated.Spec.Tracing = nil
		})).To(Succeed())

		Eventually(func(g Gomega, ctx context.Context) {
			err := envTest.Get(ctx, types.NamespacedName{
				Name:      kmeta.ChildName(llmSvc.Name, "-otlp-egress"),
				Namespace: testNs.Name,
			}, &netv1.NetworkPolicy{})
			g.Expect(apierrors.IsNotFound(err)).To(BeTrue())
		}).WithContext(ctx).Should(Succeed())
	})

	It("removes the policy when explicit opt-in is cleared", func(ctx SpecContext) {
		testNs := NewTestNamespace(ctx, envTest)
		llmSvc := LLMInferenceService("test-llm-tracing-opt-in-clear",
			InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
			WithModelURI("hf://facebook/opt-125m"),
			WithAnnotations(map[string]string{constants.EnableTracingEgressNetworkPolicyAnnotationKey: "true"}),
		)
		llmSvc.Spec.Tracing = &v1alpha2.TracingSpec{}
		Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
		defer testNs.DeleteAndWait(ctx, llmSvc)
		waitForTracingNetworkPolicy(ctx, testNs.Name, llmSvc.Name)

		Expect(updateLLMInferenceServiceWithRetry(ctx, testNs.Name, llmSvc.Name, func(updated *v1alpha2.LLMInferenceService) {
			delete(updated.Annotations, constants.EnableTracingEgressNetworkPolicyAnnotationKey)
		})).To(Succeed())

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
			WithAnnotations(map[string]string{
				constants.StopAnnotationKey:                             "true",
				constants.EnableTracingEgressNetworkPolicyAnnotationKey: "true",
			}),
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

	It("leaves policy deletion to garbage collection when the service is deleted", func(ctx SpecContext) {
		testNs := NewTestNamespace(ctx, envTest)
		llmSvc := LLMInferenceService("test-llm-tracing-delete",
			InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
			WithModelURI("hf://facebook/opt-125m"),
			WithAnnotations(map[string]string{constants.EnableTracingEgressNetworkPolicyAnnotationKey: "true"}),
		)
		llmSvc.Spec.Tracing = &v1alpha2.TracingSpec{}
		Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
		waitForTracingNetworkPolicy(ctx, testNs.Name, llmSvc.Name)

		testNs.DeleteAndWait(ctx, llmSvc)
		Expect(envTest.Get(ctx, types.NamespacedName{
			Name:      kmeta.ChildName(llmSvc.Name, "-otlp-egress"),
			Namespace: testNs.Name,
		}, &netv1.NetworkPolicy{})).To(Succeed())

		// envtest does not run garbage collection, so simulate the API server's
		// deletion of the owned policy explicitly.
		Expect(envTest.Delete(ctx, &netv1.NetworkPolicy{
			ObjectMeta: metav1.ObjectMeta{
				Name:      kmeta.ChildName(llmSvc.Name, "-otlp-egress"),
				Namespace: testNs.Name,
			},
		})).To(Succeed())
		Eventually(func(g Gomega, ctx context.Context) {
			err := envTest.Get(ctx, types.NamespacedName{
				Name:      kmeta.ChildName(llmSvc.Name, "-otlp-egress"),
				Namespace: testNs.Name,
			}, &netv1.NetworkPolicy{})
			g.Expect(apierrors.IsNotFound(err)).To(BeTrue())
		}).WithContext(ctx).Should(Succeed())
	})

	It("does not create the policy for tracing without explicit opt-in", func(ctx SpecContext) {
		testNs := NewTestNamespace(ctx, envTest)
		llmSvc := LLMInferenceService("test-llm-tracing-no-egress-policy",
			InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
			WithModelURI("hf://facebook/opt-125m"),
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

func updateLLMInferenceServiceWithRetry(ctx context.Context, namespace, name string, mutate func(*v1alpha2.LLMInferenceService)) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		updated := &v1alpha2.LLMInferenceService{}
		if err := envTest.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, updated); err != nil {
			return err
		}
		mutate(updated)
		return envTest.Update(ctx, updated)
	})
}

func createOTLPTestService(ctx context.Context, baseNamespace, serviceName string, selector map[string]string, servicePort int32) (*corev1.Namespace, *corev1.Service) {
	collectorNs := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: kmeta.ChildName(baseNamespace, "-collector")}}
	Expect(envTest.Create(ctx, collectorNs)).To(Succeed())

	collectorSvc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: serviceName, Namespace: collectorNs.Name},
		Spec: corev1.ServiceSpec{
			Selector: selector,
			Ports:    []corev1.ServicePort{{Name: "otlp", Port: servicePort}},
		},
	}
	Expect(envTest.Create(ctx, collectorSvc)).To(Succeed())
	return collectorNs, collectorSvc
}

func deleteOTLPTestService(ctx context.Context, namespace *corev1.Namespace, service *corev1.Service) {
	_ = envTest.Delete(ctx, service)
	_ = envTest.Delete(ctx, namespace)
}
