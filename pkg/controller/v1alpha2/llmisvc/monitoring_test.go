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

	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

var _ = Describe("LLMInferenceService Monitoring", func() {
	Context("Monitoring Reconciliation", func() {
		It("should create monitoring resources when llmisvc is created", func(ctx SpecContext) {
			// given
			svcName := "test-llm-monitoring"
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

			// then - verify ServiceAccount is created
			waitForMetricsReaderServiceAccount(ctx, testNs.Name)

			// then - verify Secret is created
			expectedSecret := waitForMetricsReaderSASecret(ctx, testNs.Name)
			Expect(expectedSecret.Annotations).To(HaveKeyWithValue("kubernetes.io/service-account.name", "kserve-metrics-reader-sa"))

			// then - verify ClusterRoleBinding is created
			expectedClusterRoleBinding := waitForMetricsReaderRoleBinding(ctx, testNs.Name)
			Expect(expectedClusterRoleBinding.Subjects).To(HaveLen(1))
			Expect(expectedClusterRoleBinding.Subjects[0].Kind).To(Equal("ServiceAccount"))
			Expect(expectedClusterRoleBinding.Subjects[0].Name).To(Equal("kserve-metrics-reader-sa"))
			Expect(expectedClusterRoleBinding.Subjects[0].Namespace).To(Equal(testNs.Name))
			Expect(expectedClusterRoleBinding.RoleRef.Kind).To(Equal("ClusterRole"))
			Expect(expectedClusterRoleBinding.RoleRef.Name).To(Equal("kserve-metrics-reader-cluster-role"))

			// then - verify per-service PodMonitor is created
			waitForVLLMEnginePodMonitor(ctx, svcName, testNs.Name)

			// then - verify ServiceMonitor is created
			expectedServiceMonitor := waitForSchedulerServiceMonitor(ctx, testNs.Name)
			Expect(expectedServiceMonitor.Spec.Endpoints).To(HaveLen(1))
			Expect(expectedServiceMonitor.Spec.Endpoints[0].Port).To(Equal("metrics"))
			Expect(expectedServiceMonitor.Spec.Endpoints[0].Authorization.Credentials.Name).To(Equal("kserve-metrics-reader-sa-secret"))
		})

		It("should skip cleanup when an llmisvc is deleted but other llmisvc exist in namespace", func(ctx SpecContext) {
			// given
			svcName := "test-llm-cleanup-skip"
			testNs := NewTestNamespace(ctx, envTest)

			// Create first LLMInferenceService
			llmSvc1 := LLMInferenceService(svcName+"-1",
				InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
				WithModelURI("hf://facebook/opt-125m"),
			)

			// Create second LLMInferenceService
			llmSvc2 := LLMInferenceService(svcName+"-2",
				InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
				WithModelURI("hf://facebook/opt-125m"),
			)

			// when - create both services
			Expect(envTest.Create(ctx, llmSvc1)).To(Succeed())
			Expect(envTest.Create(ctx, llmSvc2)).To(Succeed())

			// Verify monitoring resources are created
			waitForAllMonitoringResources(ctx, svcName+"-1", testNs.Name)
			waitForVLLMEnginePodMonitor(ctx, svcName+"-2", testNs.Name)

			// when - delete only the first service
			Expect(envTest.Delete(ctx, llmSvc1)).To(Succeed())

			// then - shared monitoring resources should still exist (because second service exists)
			// The per-service PodMonitor for svc-2 must survive; svc-1's monitor is GC-owned.
			expectedServiceAccount := &corev1.ServiceAccount{}
			expectedSecret := &corev1.Secret{}
			expectedClusterRoleBinding := &rbacv1.ClusterRoleBinding{}
			expectedPodMonitor := &monitoringv1.PodMonitor{}
			expectedServiceMonitor := &monitoringv1.ServiceMonitor{}

			Consistently(func(g Gomega, ctx context.Context) {
				g.Expect(envTest.Get(ctx, types.NamespacedName{
					Name:      "kserve-metrics-reader-sa",
					Namespace: testNs.Name,
				}, expectedServiceAccount)).To(Succeed())

				g.Expect(envTest.Get(ctx, types.NamespacedName{
					Name:      "kserve-metrics-reader-sa-secret",
					Namespace: testNs.Name,
				}, expectedSecret)).To(Succeed())

				g.Expect(envTest.Get(ctx, types.NamespacedName{
					Name: kmeta.ChildName("kserve-metrics-reader-role-binding-", testNs.Name),
				}, expectedClusterRoleBinding)).Should(Succeed())

				// The second service's per-service PodMonitor must survive.
				g.Expect(envTest.Get(ctx, types.NamespacedName{
					Name:      kmeta.ChildName(svcName+"-2", "-kserve-llmisvc-engine-default"),
					Namespace: testNs.Name,
				}, expectedPodMonitor)).Should(Succeed())

				g.Expect(envTest.Get(ctx, types.NamespacedName{
					Name:      "kserve-llm-isvc-scheduler",
					Namespace: testNs.Name,
				}, expectedServiceMonitor)).Should(Succeed())
			}).WithContext(ctx).Should(Succeed())
		})

		It("should perform cleanup when the last llmisvc is deleted", func(ctx SpecContext) {
			// given
			svcName := "test-llm-cleanup-last"
			testNs := NewTestNamespace(ctx, envTest)

			llmSvc := LLMInferenceService(svcName,
				InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
				WithModelURI("hf://facebook/opt-125m"),
			)

			// when - create service
			Expect(envTest.Create(ctx, llmSvc)).To(Succeed())

			// Verify monitoring resources are created
			waitForAllMonitoringResources(ctx, svcName, testNs.Name)

			// when - delete the last (and only) service
			Expect(envTest.Delete(ctx, llmSvc)).To(Succeed())

			// then - RBAC resources and ServiceMonitor should be deleted
			// Note: per-service PodMonitors are owned by ownerReference and cleaned up by
			// Kubernetes GC in real clusters; envtest does not run GC so we omit that assertion.
			Eventually(func(ctx context.Context) bool {
				serviceAccount := &corev1.ServiceAccount{}
				err := envTest.Get(ctx, types.NamespacedName{
					Name:      "kserve-metrics-reader-sa",
					Namespace: testNs.Name,
				}, serviceAccount)
				return err != nil && apierrors.IsNotFound(err)
			}).WithContext(ctx).Should(BeTrue(), "monitoring ServiceAccount should be deleted")

			Eventually(func(ctx context.Context) bool {
				secret := &corev1.Secret{}
				err := envTest.Get(ctx, types.NamespacedName{
					Name:      "kserve-metrics-reader-sa-secret",
					Namespace: testNs.Name,
				}, secret)
				return err != nil && apierrors.IsNotFound(err)
			}).WithContext(ctx).Should(BeTrue(), "monitoring Secret should be deleted")

			Eventually(func(ctx context.Context) bool {
				clusterRoleBinding := &rbacv1.ClusterRoleBinding{}
				err := envTest.Get(ctx, types.NamespacedName{
					Name: kmeta.ChildName("kserve-metrics-reader-role-binding-", testNs.Name),
				}, clusterRoleBinding)
				return err != nil && apierrors.IsNotFound(err)
			}).WithContext(ctx).Should(BeTrue(), "monitoring ClusterRoleBinding should be deleted")

			Eventually(func(ctx context.Context) bool {
				serviceMonitor := &monitoringv1.ServiceMonitor{}
				err := envTest.Get(ctx, types.NamespacedName{
					Name:      "kserve-llm-isvc-scheduler",
					Namespace: testNs.Name,
				}, serviceMonitor)
				return err != nil && apierrors.IsNotFound(err)
			}).WithContext(ctx).Should(BeTrue(), "monitoring ServiceMonitor should be deleted")
		})
	})
})

var _ = Describe("PodMonitor TLS configuration", func() {
	// Each It block creates its own namespace for full isolation.
	// TLS configuration is driven by llmSvcHasTlsRotationEnabled / sidecarTlsRotationEnabled
	// and is observed via the InsecureSkipVerify and CA fields on the PodMonitor.

	It("should use rotation-enabled config when Spec.Template is nil and no sidecar", func(ctx SpecContext) {
		svcName := "tls-cfg-nil-template"
		testNs := NewTestNamespace(ctx, envTest)

		llmSvc := LLMInferenceService(svcName,
			InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
			WithModelURI("hf://facebook/opt-125m"),
			// Spec.Template intentionally omitted — nil triggers the FIPS-safe fallback.
		)
		Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
		defer func() { testNs.DeleteAndWait(ctx, llmSvc) }()

		pm := waitForPerServicePodMonitor(ctx, svcName, testNs.Name)
		tlsCfg := pm.Spec.PodMetricsEndpoints[0].TLSConfig
		Expect(tlsCfg.InsecureSkipVerify).To(HaveValue(BeFalse()))
		Expect(tlsCfg.CA.Secret).ToNot(BeNil())
		Expect(tlsCfg.CA.Secret.Name).To(Equal(kmeta.ChildName(svcName, "-kserve-self-signed-certs")))
	})

	It("should use rotation-enabled config (FIPS fallback) when main container is absent", func(ctx SpecContext) {
		svcName := "tls-cfg-no-main"
		testNs := NewTestNamespace(ctx, envTest)

		llmSvc := LLMInferenceService(svcName,
			InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
			WithModelURI("hf://facebook/opt-125m"),
			WithTemplate(&corev1.PodSpec{
				Containers: []corev1.Container{{
					Name:  "not-main",
					Image: "test-image:latest",
				}},
			}),
		)
		Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
		defer func() { testNs.DeleteAndWait(ctx, llmSvc) }()

		pm := waitForPerServicePodMonitor(ctx, svcName, testNs.Name)
		tlsCfg := pm.Spec.PodMetricsEndpoints[0].TLSConfig
		Expect(tlsCfg.InsecureSkipVerify).To(HaveValue(BeFalse()))
		Expect(tlsCfg.CA.Secret).ToNot(BeNil())
		Expect(tlsCfg.CA.Secret.Name).To(Equal(kmeta.ChildName(svcName, "-kserve-self-signed-certs")))
	})

	It("should set InsecureSkipVerify=false and CA when flag is present — bare form", func(ctx SpecContext) {
		svcName := "tls-cfg-flag-bare"
		testNs := NewTestNamespace(ctx, envTest)

		llmSvc := LLMInferenceService(svcName,
			InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
			WithModelURI("hf://facebook/opt-125m"),
			WithTemplate(&corev1.PodSpec{
				Containers: []corev1.Container{{
					Name:    "main",
					Command: []string{"vllm", "--enable-ssl-refresh"},
				}},
			}),
		)
		Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
		defer func() { testNs.DeleteAndWait(ctx, llmSvc) }()

		pm := waitForPerServicePodMonitor(ctx, svcName, testNs.Name)
		tlsCfg := pm.Spec.PodMetricsEndpoints[0].TLSConfig
		Expect(tlsCfg.InsecureSkipVerify).To(HaveValue(BeFalse()))
		Expect(tlsCfg.CA.Secret).ToNot(BeNil())
		Expect(tlsCfg.CA.Secret.Name).To(Equal(kmeta.ChildName(svcName, "-kserve-self-signed-certs")))
	})

	It("should set InsecureSkipVerify=false and CA when flag is present — =true form", func(ctx SpecContext) {
		svcName := "tls-cfg-flag-eq-true"
		testNs := NewTestNamespace(ctx, envTest)

		llmSvc := LLMInferenceService(svcName,
			InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
			WithModelURI("hf://facebook/opt-125m"),
			WithTemplate(&corev1.PodSpec{
				Containers: []corev1.Container{{
					Name:    "main",
					Command: []string{"vllm", "--enable-ssl-refresh=true"},
				}},
			}),
		)
		Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
		defer func() { testNs.DeleteAndWait(ctx, llmSvc) }()

		pm := waitForPerServicePodMonitor(ctx, svcName, testNs.Name)
		tlsCfg := pm.Spec.PodMetricsEndpoints[0].TLSConfig
		Expect(tlsCfg.InsecureSkipVerify).To(HaveValue(BeFalse()))
		Expect(tlsCfg.CA.Secret).ToNot(BeNil())
	})

	It("should set InsecureSkipVerify=true when sidecar present but no version annotation", func(ctx SpecContext) {
		svcName := "tls-cfg-sidecar-no-ver"
		testNs := NewTestNamespace(ctx, envTest)

		llmSvc := LLMInferenceService(svcName,
			InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
			WithModelURI("hf://facebook/opt-125m"),
			WithTemplate(&corev1.PodSpec{
				InitContainers: []corev1.Container{{
					Name:  constants.LLMISVCRoutingSidecarContainerName,
					Image: "test-routing-sidecar:latest",
					Args:  []string{"--secure-proxy=true"},
				}},
			}),
			// No WithSpecAnnotations — routing-sidecar-version absent.
		)
		Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
		defer func() { testNs.DeleteAndWait(ctx, llmSvc) }()

		pm := waitForPerServicePodMonitor(ctx, svcName, testNs.Name)
		tlsCfg := pm.Spec.PodMetricsEndpoints[0].TLSConfig
		Expect(tlsCfg.InsecureSkipVerify).To(HaveValue(BeTrue()))
		Expect(tlsCfg.CA.Secret).To(BeNil())
		Expect(tlsCfg.CA.ConfigMap).To(BeNil())
	})

	It("should set InsecureSkipVerify=true when sidecar version is below 0.7.0", func(ctx SpecContext) {
		svcName := "tls-cfg-sidecar-old-ver"
		testNs := NewTestNamespace(ctx, envTest)

		llmSvc := LLMInferenceService(svcName,
			InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
			WithModelURI("hf://facebook/opt-125m"),
			WithSpecAnnotations(map[string]string{
				"llm-d.ai/routing-sidecar-version": "0.6.9", // one patch below llmisvc.SidecarCertRotationMinVersionStr
			}),
			WithTemplate(&corev1.PodSpec{
				InitContainers: []corev1.Container{{
					Name:  constants.LLMISVCRoutingSidecarContainerName,
					Image: "test-routing-sidecar:latest",
					Args:  []string{"--secure-proxy=true"},
				}},
			}),
		)
		Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
		defer func() { testNs.DeleteAndWait(ctx, llmSvc) }()

		pm := waitForPerServicePodMonitor(ctx, svcName, testNs.Name)
		tlsCfg := pm.Spec.PodMetricsEndpoints[0].TLSConfig
		Expect(tlsCfg.InsecureSkipVerify).To(HaveValue(BeTrue()))
		Expect(tlsCfg.CA.Secret).To(BeNil())
		Expect(tlsCfg.CA.ConfigMap).To(BeNil())
	})

	It("should set InsecureSkipVerify=true when sidecar version meets minimum but --secure-proxy=false", func(ctx SpecContext) {
		svcName := "tls-cfg-sidecar-flag-false"
		testNs := NewTestNamespace(ctx, envTest)

		llmSvc := LLMInferenceService(svcName,
			InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
			WithModelURI("hf://facebook/opt-125m"),
			WithSpecAnnotations(map[string]string{
				"llm-d.ai/routing-sidecar-version": llmisvc.SidecarCertRotationMinVersionStr,
			}),
			WithTemplate(&corev1.PodSpec{
				InitContainers: []corev1.Container{{
					Name:  constants.LLMISVCRoutingSidecarContainerName,
					Image: "test-routing-sidecar:latest",
					Args:  []string{"--secure-proxy=false"},
				}},
			}),
		)
		Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
		defer func() { testNs.DeleteAndWait(ctx, llmSvc) }()

		pm := waitForPerServicePodMonitor(ctx, svcName, testNs.Name)
		tlsCfg := pm.Spec.PodMetricsEndpoints[0].TLSConfig
		Expect(tlsCfg.InsecureSkipVerify).To(HaveValue(BeTrue()))
		Expect(tlsCfg.CA.Secret).To(BeNil())
		Expect(tlsCfg.CA.ConfigMap).To(BeNil())
	})

	It("should set InsecureSkipVerify=false and CA when sidecar version meets minimum and --secure-proxy=true", func(ctx SpecContext) {
		svcName := "tls-cfg-sidecar-both-pass"
		testNs := NewTestNamespace(ctx, envTest)

		llmSvc := LLMInferenceService(svcName,
			InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
			WithModelURI("hf://facebook/opt-125m"),
			WithSpecAnnotations(map[string]string{
				"llm-d.ai/routing-sidecar-version": llmisvc.SidecarCertRotationMinVersionStr,
			}),
			WithTemplate(&corev1.PodSpec{
				InitContainers: []corev1.Container{{
					Name:  constants.LLMISVCRoutingSidecarContainerName,
					Image: "test-routing-sidecar:latest",
					Args:  []string{"--secure-proxy=true"},
				}},
			}),
		)
		Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
		defer func() { testNs.DeleteAndWait(ctx, llmSvc) }()

		pm := waitForPerServicePodMonitor(ctx, svcName, testNs.Name)
		tlsCfg := pm.Spec.PodMetricsEndpoints[0].TLSConfig
		Expect(tlsCfg.InsecureSkipVerify).To(HaveValue(BeFalse()))
		Expect(tlsCfg.CA.Secret).ToNot(BeNil())
		Expect(tlsCfg.CA.Secret.Name).To(Equal(kmeta.ChildName(svcName, "-kserve-self-signed-certs")))
	})
})

var _ = Describe("PodMonitor structure", func() {
	It("should create PodMonitors with a per-service name derived from the llmisvc name", func(ctx SpecContext) {
		svcName := "my-model"
		testNs := NewTestNamespace(ctx, envTest)

		llmSvc := LLMInferenceService(svcName,
			InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
			WithModelURI("hf://facebook/opt-125m"),
		)
		Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
		defer func() { testNs.DeleteAndWait(ctx, llmSvc) }()

		// Default monitor (without kserve_ relabeling).
		waitForPerServicePodMonitor(ctx, svcName, testNs.Name)

		// Relabeling variant (backward compatibility suffix).
		relabeledPM := &monitoringv1.PodMonitor{}
		Eventually(func(_ Gomega, ctx context.Context) error {
			return envTest.Get(ctx, types.NamespacedName{
				Name:      kmeta.ChildName(svcName, "-kserve-llmisvc-engine"),
				Namespace: testNs.Name,
			}, relabeledPM)
		}).WithContext(ctx).Should(Succeed())

		// Legacy shared names must never appear.
		assertPodMonitorGone(ctx, "kserve-llm-isvc-vllm-engine-default", testNs.Name)
		assertPodMonitorGone(ctx, "kserve-llm-isvc-vllm-engine", testNs.Name)
	})

	It("should scope the PodMonitor pod selector to the owning llmisvc", func(ctx SpecContext) {
		svcName := "svc-a"
		testNs := NewTestNamespace(ctx, envTest)

		llmSvc := LLMInferenceService(svcName,
			InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
			WithModelURI("hf://facebook/opt-125m"),
		)
		Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
		defer func() { testNs.DeleteAndWait(ctx, llmSvc) }()

		pm := waitForPerServicePodMonitor(ctx, svcName, testNs.Name)
		Expect(pm.Spec.Selector.MatchLabels).To(HaveKeyWithValue("app.kubernetes.io/name", svcName))
		Expect(pm.Spec.Selector.MatchLabels).To(HaveKeyWithValue("app.kubernetes.io/part-of", "llminferenceservice"))
	})

	It("should set an owner reference pointing back to the LLMInferenceService", func(ctx SpecContext) {
		svcName := "owner-ref-svc"
		testNs := NewTestNamespace(ctx, envTest)

		llmSvc := LLMInferenceService(svcName,
			InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
			WithModelURI("hf://facebook/opt-125m"),
		)
		Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
		defer func() { testNs.DeleteAndWait(ctx, llmSvc) }()

		// Reload to get the server-assigned UID.
		Expect(envTest.Get(ctx, types.NamespacedName{
			Name:      svcName,
			Namespace: testNs.Name,
		}, llmSvc)).To(Succeed())

		pm := waitForPerServicePodMonitor(ctx, svcName, testNs.Name)
		Expect(pm.OwnerReferences).To(HaveLen(1))
		ownerRef := pm.OwnerReferences[0]
		Expect(ownerRef.Kind).To(Equal("LLMInferenceService"))
		Expect(ownerRef.APIVersion).To(Equal("serving.kserve.io/v1alpha2"))
		Expect(ownerRef.Name).To(Equal(llmSvc.GetName()))
		Expect(ownerRef.Controller).ToNot(BeNil())
		Expect(*ownerRef.Controller).To(BeTrue())
	})
})

var _ = Describe("LLMInferenceService Multi-Service PodMonitor", func() {
	It("should produce distinct PodMonitors scoped to each service", func(ctx SpecContext) {
		testNs := NewTestNamespace(ctx, envTest)

		llmSvcA := LLMInferenceService("svc-a",
			InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
			WithModelURI("hf://facebook/opt-125m"),
		)
		llmSvcB := LLMInferenceService("svc-b",
			InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
			WithModelURI("hf://facebook/opt-125m"),
		)

		Expect(envTest.Create(ctx, llmSvcA)).To(Succeed())
		Expect(envTest.Create(ctx, llmSvcB)).To(Succeed())
		defer func() {
			testNs.DeleteAndWait(ctx, llmSvcA)
			testNs.DeleteAndWait(ctx, llmSvcB)
		}()

		pmA := &monitoringv1.PodMonitor{}
		pmB := &monitoringv1.PodMonitor{}

		Eventually(func(g Gomega, ctx context.Context) {
			g.Expect(envTest.Get(ctx, types.NamespacedName{
				Name:      kmeta.ChildName("svc-a", "-kserve-llmisvc-engine-default"),
				Namespace: testNs.Name,
			}, pmA)).To(Succeed())

			g.Expect(envTest.Get(ctx, types.NamespacedName{
				Name:      kmeta.ChildName("svc-b", "-kserve-llmisvc-engine-default"),
				Namespace: testNs.Name,
			}, pmB)).To(Succeed())
		}).WithContext(ctx).Should(Succeed())

		Expect(pmA.Spec.Selector.MatchLabels).To(HaveKeyWithValue("app.kubernetes.io/name", "svc-a"))
		Expect(pmB.Spec.Selector.MatchLabels).To(HaveKeyWithValue("app.kubernetes.io/name", "svc-b"))

		// Legacy shared names must never appear.
		assertPodMonitorGone(ctx, "kserve-llm-isvc-vllm-engine-default", testNs.Name)
		assertPodMonitorGone(ctx, "kserve-llm-isvc-vllm-engine", testNs.Name)
	})

	It("should delete old shared PodMonitor names during the first reconcile (migration)", func(ctx SpecContext) {
		svcName := "migration-svc"
		testNs := NewTestNamespace(ctx, envTest)

		// Pre-create the legacy namespace-wide PodMonitor (simulates an upgrade from the old operator).
		legacyPM := &monitoringv1.PodMonitor{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "kserve-llm-isvc-vllm-engine-default",
				Namespace: testNs.Name,
			},
			Spec: monitoringv1.PodMonitorSpec{
				Selector:            metav1.LabelSelector{},
				PodMetricsEndpoints: []monitoringv1.PodMetricsEndpoint{},
			},
		}
		Expect(envTest.Create(ctx, legacyPM)).To(Succeed())

		llmSvc := LLMInferenceService(svcName,
			InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
			WithModelURI("hf://facebook/opt-125m"),
		)
		Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
		defer func() { testNs.DeleteAndWait(ctx, llmSvc) }()

		// The reconciler must delete the legacy monitor and create the per-service one.
		Eventually(func(ctx context.Context) bool {
			pm := &monitoringv1.PodMonitor{}
			err := envTest.Get(ctx, types.NamespacedName{
				Name:      "kserve-llm-isvc-vllm-engine-default",
				Namespace: testNs.Name,
			}, pm)
			return apierrors.IsNotFound(err)
		}).WithContext(ctx).Should(BeTrue(), "legacy PodMonitor should be deleted by reconciler")

		Eventually(func(_ Gomega, ctx context.Context) error {
			pm := &monitoringv1.PodMonitor{}
			return envTest.Get(ctx, types.NamespacedName{
				Name:      kmeta.ChildName(svcName, "-kserve-llmisvc-engine-default"),
				Namespace: testNs.Name,
			}, pm)
		}).WithContext(ctx).Should(Succeed(), "per-service PodMonitor should be created")
	})
})

// waitForMetricsReaderServiceAccount waits until the metrics reader ServiceAccount appears.
func waitForMetricsReaderServiceAccount(ctx context.Context, nsName string) {
	expectedServiceAccount := &corev1.ServiceAccount{}
	Eventually(func(_ Gomega, ctx context.Context) error {
		return envTest.Get(ctx, types.NamespacedName{
			Name:      "kserve-metrics-reader-sa",
			Namespace: nsName,
		}, expectedServiceAccount)
	}).WithContext(ctx).Should(Succeed())
}

// waitForMetricsReaderSASecret waits until the metrics reader token Secret appears and returns it.
func waitForMetricsReaderSASecret(ctx context.Context, nsName string) *corev1.Secret {
	expectedSecret := &corev1.Secret{}
	Eventually(func(_ Gomega, ctx context.Context) error {
		return envTest.Get(ctx, types.NamespacedName{
			Name:      "kserve-metrics-reader-sa-secret",
			Namespace: nsName,
		}, expectedSecret)
	}).WithContext(ctx).Should(Succeed())

	return expectedSecret
}

// waitForMetricsReaderRoleBinding waits until the metrics reader ClusterRoleBinding appears and returns it.
func waitForMetricsReaderRoleBinding(ctx context.Context, nsName string) *rbacv1.ClusterRoleBinding {
	expectedClusterRoleBinding := &rbacv1.ClusterRoleBinding{}
	Eventually(func(_ Gomega, ctx context.Context) error {
		return envTest.Get(ctx, types.NamespacedName{
			Name: kmeta.ChildName("kserve-metrics-reader-role-binding-", nsName),
		}, expectedClusterRoleBinding)
	}).WithContext(ctx).Should(Succeed())

	return expectedClusterRoleBinding
}

// waitForVLLMEnginePodMonitor waits until the per-service PodMonitor for svcName appears.
func waitForVLLMEnginePodMonitor(ctx context.Context, svcName, nsName string) {
	expectedPodMonitor := &monitoringv1.PodMonitor{}
	Eventually(func(_ Gomega, ctx context.Context) error {
		return envTest.Get(ctx, types.NamespacedName{
			Name:      kmeta.ChildName(svcName, "-kserve-llmisvc-engine-default"),
			Namespace: nsName,
		}, expectedPodMonitor)
	}).WithContext(ctx).Should(Succeed())
}

// waitForSchedulerServiceMonitor waits until the shared scheduler ServiceMonitor appears and returns it.
func waitForSchedulerServiceMonitor(ctx context.Context, nsName string) *monitoringv1.ServiceMonitor {
	expectedServiceMonitor := &monitoringv1.ServiceMonitor{}
	Eventually(func(_ Gomega, ctx context.Context) error {
		return envTest.Get(ctx, types.NamespacedName{
			Name:      "kserve-llm-isvc-scheduler",
			Namespace: nsName,
		}, expectedServiceMonitor)
	}).WithContext(ctx).Should(Succeed())

	return expectedServiceMonitor
}

// waitForAllMonitoringResources waits until the RBAC resources, per-service PodMonitor for svcName,
// and the shared ServiceMonitor are all present.
func waitForAllMonitoringResources(ctx context.Context, svcName, nsName string) {
	waitForMetricsReaderServiceAccount(ctx, nsName)
	waitForMetricsReaderSASecret(ctx, nsName)
	waitForMetricsReaderRoleBinding(ctx, nsName)
	waitForVLLMEnginePodMonitor(ctx, svcName, nsName)
	waitForSchedulerServiceMonitor(ctx, nsName)
}

// waitForPerServicePodMonitor waits for the per-service PodMonitor for svcName to appear and returns it.
func waitForPerServicePodMonitor(ctx context.Context, svcName, nsName string) *monitoringv1.PodMonitor {
	pm := &monitoringv1.PodMonitor{}
	Eventually(func(_ Gomega, ctx context.Context) error {
		return envTest.Get(ctx, types.NamespacedName{
			Name:      kmeta.ChildName(svcName, "-kserve-llmisvc-engine-default"),
			Namespace: nsName,
		}, pm)
	}).WithContext(ctx).Should(Succeed())
	return pm
}

// assertPodMonitorGone asserts that a PodMonitor with the given name does not exist and does not
// appear within a short consistency window.
func assertPodMonitorGone(ctx context.Context, name, nsName string) {
	Consistently(func(ctx context.Context) bool {
		pm := &monitoringv1.PodMonitor{}
		err := envTest.Get(ctx, types.NamespacedName{Name: name, Namespace: nsName}, pm)
		return apierrors.IsNotFound(err)
	}).WithContext(ctx).Should(BeTrue(), "PodMonitor %s should not exist", name)
}
