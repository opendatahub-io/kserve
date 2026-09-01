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
	"encoding/json"

	istioapi "istio.io/client-go/pkg/apis/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"knative.dev/pkg/kmeta"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
	"github.com/kserve/kserve/pkg/constants"
	"github.com/kserve/kserve/pkg/controller/v1alpha2/llmisvc"
	. "github.com/kserve/kserve/pkg/controller/v1alpha2/llmisvc/fixture"
)

// istioCACertificatePath is the default path used by newIstioTlsSettings when rotation is enabled.
// It mirrors the default of IstioCACertificatePath in router_platform_networking_odh.go.
const istioCACertificatePath = "/var/run/secrets/kubernetes.io/serviceaccount/service-ca.crt"

var _ = Describe("LLMInferenceService TLS Toggle", func() {
	Context("When enableLLMInferenceServiceTLS is false in ConfigMap", func() {
		It("should keep cert secret and should use HTTP port name", func(ctx SpecContext) {
			svcName := "test-llm-tls-off"
			testNs := NewTestNamespace(ctx, envTest)

			// Patch the global ConfigMap to disable TLS
			cfgMap := &corev1.ConfigMap{}
			cfgMapKey := types.NamespacedName{
				Name:      constants.InferenceServiceConfigMapName,
				Namespace: constants.KServeNamespace,
			}
			Expect(envTest.Get(ctx, cfgMapKey, cfgMap)).To(Succeed())

			originalIngress := cfgMap.Data["ingress"]
			var ingressCfg map[string]interface{}
			Expect(json.Unmarshal([]byte(originalIngress), &ingressCfg)).To(Succeed())
			ingressCfg["enableLLMInferenceServiceTLS"] = false
			updatedIngress, err := json.Marshal(ingressCfg)
			Expect(err).ToNot(HaveOccurred())
			cfgMap.Data["ingress"] = string(updatedIngress)
			Expect(envTest.Client.Update(ctx, cfgMap)).To(Succeed())

			DeferCleanup(func(ctx context.Context) {
				Expect(envTest.Get(ctx, cfgMapKey, cfgMap)).To(Succeed())
				cfgMap.Data["ingress"] = originalIngress
				Expect(envTest.Client.Update(ctx, cfgMap)).To(Succeed())
			})

			// Create configs and LLMInferenceService
			modelConfig := LLMInferenceServiceConfig("model-fb-opt-125m",
				InNamespace[*v1alpha2.LLMInferenceServiceConfig](testNs.Name),
				WithConfigModelName("facebook/opt-125m"),
				WithConfigModelURI("hf://facebook/opt-125m"),
			)
			workloadConfig := LLMInferenceServiceConfig("workload-single-cpu",
				InNamespace[*v1alpha2.LLMInferenceServiceConfig](testNs.Name),
				WithConfigWorkloadTemplate(&corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "main",
						Image: "quay.io/test/vllm:latest",
					}},
				}),
			)
			Expect(envTest.Client.Create(ctx, modelConfig)).To(Succeed())
			Expect(envTest.Client.Create(ctx, workloadConfig)).To(Succeed())

			llmSvc := LLMInferenceService(svcName,
				InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
				WithModelURI("hf://facebook/opt-125m"),
			)
			Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
			defer func() {
				Expect(envTest.Delete(ctx, llmSvc)).To(Succeed())
			}()

			// Verify: cert secret still created (kept for volume mount stability)
			Eventually(func(g Gomega, ctx context.Context) {
				secret := &corev1.Secret{}
				err := envTest.Get(ctx, types.NamespacedName{
					Name:      kmeta.ChildName(svcName, "-kserve-self-signed-certs"),
					Namespace: testNs.Name,
				}, secret)
				g.Expect(err).ToNot(HaveOccurred(), "cert secret should exist even when TLS is disabled")
			}).WithContext(ctx).Should(Succeed())

			// Verify: workload service has HTTP port name
			Eventually(func(g Gomega, ctx context.Context) {
				svc := &corev1.Service{}
				err := envTest.Get(ctx, types.NamespacedName{
					Name:      kmeta.ChildName(svcName, "-kserve-workload-svc"),
					Namespace: testNs.Name,
				}, svc)
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(svc.Spec.Ports).To(HaveLen(1))
				g.Expect(svc.Spec.Ports[0].Name).To(Equal("http"))
				g.Expect(*svc.Spec.Ports[0].AppProtocol).To(Equal("http"))
			}).WithContext(ctx).Should(Succeed())

			// Verify: status URL uses http scheme
			Eventually(func(g Gomega, ctx context.Context) {
				err := envTest.Get(ctx, types.NamespacedName{
					Name:      svcName,
					Namespace: testNs.Name,
				}, llmSvc)
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(llmSvc.Status.Addresses).ToNot(BeEmpty())
				g.Expect(llmSvc.Status.Addresses[0].URL.Scheme).To(Equal("http"))
			}).WithContext(ctx).Should(Succeed())
		})
	})
})

var _ = Describe("Scheduler DestinationRule TLS Gating", func() {
	// waitForSchedulerDR waits until the scheduler DestinationRule appears and returns it.
	waitForSchedulerDR := func(ctx context.Context, svcName, nsName string) *istioapi.DestinationRule {
		dr := &istioapi.DestinationRule{}
		Eventually(func(_ Gomega, ctx context.Context) error {
			return envTest.Get(ctx, types.NamespacedName{
				Name:      kmeta.ChildName(svcName, "-kserve-scheduler"),
				Namespace: nsName,
			}, dr)
		}).WithContext(ctx).Should(Succeed())
		return dr
	}

	// assertSchedulerDRGone verifies the scheduler DestinationRule does not exist.
	assertSchedulerDRGone := func(ctx context.Context, svcName, nsName string) {
		Consistently(func(ctx context.Context) bool {
			dr := &istioapi.DestinationRule{}
			err := envTest.Get(ctx, types.NamespacedName{
				Name:      kmeta.ChildName(svcName, "-kserve-scheduler"),
				Namespace: nsName,
			}, dr)
			return apierrors.IsNotFound(err)
		}).WithContext(ctx).Should(BeTrue(), "scheduler DestinationRule should not exist when Router is nil")
	}

	It("should not create a scheduler DestinationRule when Router is nil", func(ctx SpecContext) {
		svcName := "sched-dr-no-router"
		testNs := NewTestNamespace(ctx, envTest)

		// LLMInferenceService with no router — reconciler deletes (or never creates) the DR.
		llmSvc := LLMInferenceService(svcName,
			InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
			WithModelURI("hf://facebook/opt-125m"),
		)
		Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
		defer func() { testNs.DeleteAndWait(ctx, llmSvc) }()

		assertSchedulerDRGone(ctx, svcName, testNs.Name)
	})

	It("should produce InsecureSkipVerify=false with CA when scheduler Template is nil (FIPS fallback)", func(ctx SpecContext) {
		svcName := "sched-dr-no-template"
		testNs := NewTestNamespace(ctx, envTest)

		// Scheduler is configured with a version meeting the minimum but no Template.
		// schedulerTlsRotationEnabled returns true (strict posture).
		llmSvc := LLMInferenceService(svcName,
			InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
			WithModelURI("hf://facebook/opt-125m"),
			WithManagedRoute(),
			WithManagedGateway(),
			WithSchedulerVersion(llmisvc.SchedulerCertRotationMinVersionStr),
		)
		Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
		defer func() { testNs.DeleteAndWait(ctx, llmSvc) }()

		dr := waitForSchedulerDR(ctx, svcName, testNs.Name)
		Expect(dr.Spec.GetTrafficPolicy().GetTls().GetInsecureSkipVerify().GetValue()).To(BeFalse())
		Expect(dr.Spec.GetTrafficPolicy().GetTls().GetCaCertificates()).To(Equal(istioCACertificatePath))
	})

	It("should produce InsecureSkipVerify=true when version annotation is absent", func(ctx SpecContext) {
		svcName := "sched-dr-no-version"
		testNs := NewTestNamespace(ctx, envTest)

		// The shared scheduler preset injects app.kubernetes.io/version="0.9.0". Setting an
		// explicit empty version on the LLMInferenceService spec overrides it in the final
		// merge layer, so schedulerTlsRotationEnabled sees no usable version and returns false.
		llmSvc := LLMInferenceService(svcName,
			InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
			WithModelURI("hf://facebook/opt-125m"),
			WithManagedRoute(),
			WithManagedGateway(),
			WithSchedulerVersion(""),
			WithSchedulerCommand("scheduler", "--enable-cert-reload=true"),
		)
		Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
		defer func() { testNs.DeleteAndWait(ctx, llmSvc) }()

		dr := waitForSchedulerDR(ctx, svcName, testNs.Name)
		Expect(dr.Spec.GetTrafficPolicy().GetTls().GetInsecureSkipVerify().GetValue()).To(BeTrue())
		Expect(dr.Spec.GetTrafficPolicy().GetTls().GetCaCertificates()).To(BeEmpty())
	})

	It("should produce InsecureSkipVerify=true when scheduler version is below 0.7.0", func(ctx SpecContext) {
		svcName := "sched-dr-old-version"
		testNs := NewTestNamespace(ctx, envTest)

		llmSvc := LLMInferenceService(svcName,
			InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
			WithModelURI("hf://facebook/opt-125m"),
			WithManagedRoute(),
			WithManagedGateway(),
			WithSchedulerVersion("0.6.9"), // one patch below llmisvc.SchedulerCertRotationMinVersionStr
			WithSchedulerCommand("scheduler", "--enable-cert-reload=true"),
		)
		Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
		defer func() { testNs.DeleteAndWait(ctx, llmSvc) }()

		dr := waitForSchedulerDR(ctx, svcName, testNs.Name)
		Expect(dr.Spec.GetTrafficPolicy().GetTls().GetInsecureSkipVerify().GetValue()).To(BeTrue())
		Expect(dr.Spec.GetTrafficPolicy().GetTls().GetCaCertificates()).To(BeEmpty())
	})

	It("should produce InsecureSkipVerify=true when version meets minimum but flag is absent", func(ctx SpecContext) {
		svcName := "sched-dr-no-flag"
		testNs := NewTestNamespace(ctx, envTest)

		llmSvc := LLMInferenceService(svcName,
			InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
			WithModelURI("hf://facebook/opt-125m"),
			WithManagedRoute(),
			WithManagedGateway(),
			WithSchedulerVersion(llmisvc.SchedulerCertRotationMinVersionStr),
			WithSchedulerCommand("scheduler" /* no --enable-cert-reload */),
		)
		Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
		defer func() { testNs.DeleteAndWait(ctx, llmSvc) }()

		dr := waitForSchedulerDR(ctx, svcName, testNs.Name)
		Expect(dr.Spec.GetTrafficPolicy().GetTls().GetInsecureSkipVerify().GetValue()).To(BeTrue())
		Expect(dr.Spec.GetTrafficPolicy().GetTls().GetCaCertificates()).To(BeEmpty())
	})

	It("should produce InsecureSkipVerify=false with CA when both gates pass — =true form", func(ctx SpecContext) {
		svcName := "sched-dr-eq-true"
		testNs := NewTestNamespace(ctx, envTest)

		llmSvc := LLMInferenceService(svcName,
			InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
			WithModelURI("hf://facebook/opt-125m"),
			WithManagedRoute(),
			WithManagedGateway(),
			WithSchedulerVersion(llmisvc.SchedulerCertRotationMinVersionStr),
			WithSchedulerCommand("scheduler", "--enable-cert-reload=true"),
		)
		Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
		defer func() { testNs.DeleteAndWait(ctx, llmSvc) }()

		dr := waitForSchedulerDR(ctx, svcName, testNs.Name)
		Expect(dr.Spec.GetTrafficPolicy().GetTls().GetInsecureSkipVerify().GetValue()).To(BeFalse())
		Expect(dr.Spec.GetTrafficPolicy().GetTls().GetCaCertificates()).To(Equal(istioCACertificatePath))
	})

	It("should produce InsecureSkipVerify=false with CA when both gates pass — bare form", func(ctx SpecContext) {
		// This is the critical regression case: the old exact-string check
		// (cmdEntry == "--enable-cert-reload=true") would reject the bare flag.
		svcName := "sched-dr-bare-flag"
		testNs := NewTestNamespace(ctx, envTest)

		llmSvc := LLMInferenceService(svcName,
			InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
			WithModelURI("hf://facebook/opt-125m"),
			WithManagedRoute(),
			WithManagedGateway(),
			WithSchedulerVersion(llmisvc.SchedulerCertRotationMinVersionStr),
			WithSchedulerCommand("scheduler", "--enable-cert-reload"),
		)
		Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
		defer func() { testNs.DeleteAndWait(ctx, llmSvc) }()

		dr := waitForSchedulerDR(ctx, svcName, testNs.Name)
		Expect(dr.Spec.GetTrafficPolicy().GetTls().GetInsecureSkipVerify().GetValue()).To(BeFalse())
		Expect(dr.Spec.GetTrafficPolicy().GetTls().GetCaCertificates()).To(Equal(istioCACertificatePath))
	})
})
