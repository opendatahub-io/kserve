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
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"knative.dev/pkg/kmeta"
	lwsapi "sigs.k8s.io/lws/api/leaderworkerset/v1"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
	"github.com/kserve/kserve/pkg/constants"
	. "github.com/kserve/kserve/pkg/controller/v1alpha2/llmisvc/fixture"
)

var _ = Describe("LLMInferenceService HardwareProfile injection", func() {
	Context("Single-node Deployment", func() {
		It("should not modify Deployment when no HWP annotation is set (LLM-1)", func(ctx SpecContext) {
			// given
			svcName := "test-llm-hwp-no-annotation"
			testNs := NewTestNamespace(ctx, envTest, WithIstioShadowService(svcName))

			llmSvc := LLMInferenceService(svcName,
				InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
				WithModelURI("hf://facebook/opt-125m"),
				WithTemplate(&corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: constants.LLMInferenceServiceMainContainerName, Image: "test:latest"},
					},
				}),
				WithManagedRoute(),
				WithManagedGateway(),
			)

			// when
			Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
			defer func() {
				testNs.DeleteAndWait(ctx, llmSvc)
			}()

			// then
			dep := &appsv1.Deployment{}
			Eventually(func(g Gomega, ctx context.Context) error {
				return envTest.Get(ctx, types.NamespacedName{
					Name:      kmeta.ChildName(svcName, "-kserve"),
					Namespace: testNs.Name,
				}, dep)
			}).WithContext(ctx).Should(Succeed())

			mainContainer := findContainer(dep.Spec.Template.Spec.Containers, constants.LLMInferenceServiceMainContainerName)
			Expect(mainContainer).NotTo(BeNil())
			Expect(mainContainer.Resources.Requests).To(Or(BeNil(), BeEmpty()))
			Expect(dep.Labels).NotTo(HaveKey(constants.KueueQueueNameLabel))
		})

		It("should abort reconciliation when HWP is not found and Deployment is never created (LLM-2)", func(ctx SpecContext) {
			// given
			svcName := "test-llm-hwp-not-found"
			testNs := NewTestNamespace(ctx, envTest, WithIstioShadowService(svcName))

			llmSvc := LLMInferenceService(svcName,
				InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
				WithModelURI("hf://facebook/opt-125m"),
				WithTemplate(&corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: constants.LLMInferenceServiceMainContainerName, Image: "test:latest"},
					},
				}),
				WithHardwareProfileAnnotation("missing-hwp"),
				WithManagedRoute(),
				WithManagedGateway(),
			)

			// when
			Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
			defer func() {
				testNs.DeleteAndWait(ctx, llmSvc)
			}()

			depName := types.NamespacedName{
				Name:      kmeta.ChildName(svcName, "-kserve"),
				Namespace: testNs.Name,
			}

			// then - Deployment is never created while HWP is missing
			Consistently(func(g Gomega, ctx context.Context) {
				dep := &appsv1.Deployment{}
				err := envTest.Get(ctx, depName, dep)
				g.Expect(apierrors.IsNotFound(err)).To(BeTrue(), "Deployment should not be created while HWP is missing")
			}).WithContext(ctx).Should(Succeed())

			// sub-step: create the missing HWP → reconciliation unblocked
			hwp := HardwareProfile("missing-hwp", testNs.Name, HWPResourceSpec(
				[]string{"nvidia.com/gpu", "1"},
			))
			Expect(envTest.Create(ctx, hwp)).To(Succeed())
			defer envTest.Delete(ctx, hwp) //nolint:errcheck

			Eventually(func(g Gomega, ctx context.Context) error {
				dep := &appsv1.Deployment{}
				return envTest.Get(ctx, depName, dep)
			}).WithContext(ctx).Should(Succeed(), "Deployment should be created after HWP is available")
		})

		It("should inject resources from HWP into main container (LLM-3)", func(ctx SpecContext) {
			// given
			svcName := "test-llm-hwp-resources"
			testNs := NewTestNamespace(ctx, envTest, WithIstioShadowService(svcName))

			hwp := HardwareProfile("hwp-resources", testNs.Name, HWPResourceSpec(
				[]string{"cpu", "8"},
				[]string{"memory", "16Gi"},
				[]string{"nvidia.com/gpu", "4"},
			))
			Expect(envTest.Create(ctx, hwp)).To(Succeed())
			defer envTest.Delete(ctx, hwp) //nolint:errcheck

			llmSvc := LLMInferenceService(svcName,
				InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
				WithModelURI("hf://facebook/opt-125m"),
				WithTemplate(&corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: constants.LLMInferenceServiceMainContainerName, Image: "test:latest"},
					},
				}),
				WithHardwareProfileAnnotation("hwp-resources"),
				WithManagedRoute(),
				WithManagedGateway(),
			)
			originalSpec := llmSvc.Spec.DeepCopy()

			// when
			Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
			defer func() {
				testNs.DeleteAndWait(ctx, llmSvc)
			}()

			// then
			dep := &appsv1.Deployment{}
			Eventually(func(g Gomega, ctx context.Context) error {
				return envTest.Get(ctx, types.NamespacedName{
					Name:      kmeta.ChildName(svcName, "-kserve"),
					Namespace: testNs.Name,
				}, dep)
			}).WithContext(ctx).Should(Succeed())

			mainContainer := findContainer(dep.Spec.Template.Spec.Containers, constants.LLMInferenceServiceMainContainerName)
			Expect(mainContainer).NotTo(BeNil())
			Expect(mainContainer.Resources.Requests[corev1.ResourceCPU]).To(Equal(resource.MustParse("8")))
			Expect(mainContainer.Resources.Requests[corev1.ResourceMemory]).To(Equal(resource.MustParse("16Gi")))
			Expect(mainContainer.Resources.Requests["nvidia.com/gpu"]).To(Equal(resource.MustParse("4")))
			// Guaranteed QoS: limits equal requests
			Expect(mainContainer.Resources.Limits[corev1.ResourceCPU]).To(Equal(resource.MustParse("8")))
			Expect(mainContainer.Resources.Limits["nvidia.com/gpu"]).To(Equal(resource.MustParse("4")))

			// LLMis spec must not be mutated
			updatedLLMSvc := &v1alpha2.LLMInferenceService{}
			Expect(envTest.Get(ctx, types.NamespacedName{Name: svcName, Namespace: testNs.Name}, updatedLLMSvc)).To(Succeed())
			Expect(updatedLLMSvc.Spec).To(Equal(*originalSpec))
		})

		It("should inject node scheduling from HWP into Deployment pod template (LLM-4)", func(ctx SpecContext) {
			// given
			svcName := "test-llm-hwp-node-sched"
			testNs := NewTestNamespace(ctx, envTest, WithIstioShadowService(svcName))

			hwp := HardwareProfile("hwp-node", testNs.Name, HWPNodeSpec(
				map[string]interface{}{"nvidia.com/gpu.product": "A100-PCIE-80GB"},
				[]interface{}{
					map[string]interface{}{
						"key":      "nvidia.com/gpu",
						"operator": "Exists",
						"effect":   "NoSchedule",
					},
				},
			))
			Expect(envTest.Create(ctx, hwp)).To(Succeed())
			defer envTest.Delete(ctx, hwp) //nolint:errcheck

			llmSvc := LLMInferenceService(svcName,
				InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
				WithModelURI("hf://facebook/opt-125m"),
				WithTemplate(&corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: constants.LLMInferenceServiceMainContainerName, Image: "test:latest"},
					},
				}),
				WithHardwareProfileAnnotation("hwp-node"),
				WithManagedRoute(),
				WithManagedGateway(),
			)

			// when
			Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
			defer func() {
				testNs.DeleteAndWait(ctx, llmSvc)
			}()

			// then
			dep := &appsv1.Deployment{}
			Eventually(func(g Gomega, ctx context.Context) error {
				return envTest.Get(ctx, types.NamespacedName{
					Name:      kmeta.ChildName(svcName, "-kserve"),
					Namespace: testNs.Name,
				}, dep)
			}).WithContext(ctx).Should(Succeed())

			Expect(dep.Spec.Template.Spec.NodeSelector).To(HaveKeyWithValue("nvidia.com/gpu.product", "A100-PCIE-80GB"))
			Expect(dep.Spec.Template.Spec.Tolerations).To(ContainElement(
				corev1.Toleration{
					Key:      "nvidia.com/gpu",
					Operator: corev1.TolerationOpExists,
					Effect:   corev1.TaintEffectNoSchedule,
				},
			))
		})

		It("should set Kueue label on Deployment from HWP (LLM-5)", func(ctx SpecContext) {
			// given
			svcName := "test-llm-hwp-kueue"
			testNs := NewTestNamespace(ctx, envTest, WithIstioShadowService(svcName))

			hwp := HardwareProfile("hwp-kueue", testNs.Name, HWPKueueSpec("llm-queue"))
			Expect(envTest.Create(ctx, hwp)).To(Succeed())
			defer envTest.Delete(ctx, hwp) //nolint:errcheck

			llmSvc := LLMInferenceService(svcName,
				InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
				WithModelURI("hf://facebook/opt-125m"),
				WithTemplate(&corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: constants.LLMInferenceServiceMainContainerName, Image: "test:latest"},
					},
				}),
				WithHardwareProfileAnnotation("hwp-kueue"),
				WithManagedRoute(),
				WithManagedGateway(),
			)

			// when
			Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
			defer func() {
				testNs.DeleteAndWait(ctx, llmSvc)
			}()

			// then
			dep := &appsv1.Deployment{}
			Eventually(func(g Gomega, ctx context.Context) error {
				return envTest.Get(ctx, types.NamespacedName{
					Name:      kmeta.ChildName(svcName, "-kserve"),
					Namespace: testNs.Name,
				}, dep)
			}).WithContext(ctx).Should(Succeed())

			Expect(dep.Labels).To(HaveKeyWithValue(constants.KueueQueueNameLabel, "llm-queue"))
			Expect(dep.Spec.Template.Labels).To(HaveKeyWithValue(constants.KueueQueueNameLabel, "llm-queue"))
		})

		It("should apply node scheduling and Kueue label even when main container is absent (LLM-6)", func(ctx SpecContext) {
			// given — template has a differently-named container ("server")
			svcName := "test-llm-hwp-no-main"
			testNs := NewTestNamespace(ctx, envTest, WithIstioShadowService(svcName))

			combinedSpec := map[string]interface{}{
				"identifiers": []interface{}{
					map[string]interface{}{"identifier": "nvidia.com/gpu", "defaultCount": "2"},
				},
				"schedulingSpec": map[string]interface{}{
					"type": "Node",
					"node": map[string]interface{}{
						"nodeSelector": map[string]interface{}{"tier": "gpu"},
					},
				},
			}
			hwp := HardwareProfile("hwp-no-main", testNs.Name, combinedSpec)
			Expect(envTest.Create(ctx, hwp)).To(Succeed())
			defer envTest.Delete(ctx, hwp) //nolint:errcheck

			llmSvc := LLMInferenceService(svcName,
				InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
				WithModelURI("hf://facebook/opt-125m"),
				WithTemplate(&corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "server", Image: "test:latest"},
					},
				}),
				WithHardwareProfileAnnotation("hwp-no-main"),
				WithManagedRoute(),
				WithManagedGateway(),
			)

			// when
			Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
			defer func() {
				testNs.DeleteAndWait(ctx, llmSvc)
			}()

			// then
			dep := &appsv1.Deployment{}
			Eventually(func(g Gomega, ctx context.Context) error {
				return envTest.Get(ctx, types.NamespacedName{
					Name:      kmeta.ChildName(svcName, "-kserve"),
					Namespace: testNs.Name,
				}, dep)
			}).WithContext(ctx).Should(Succeed())

			// "server" container resources are not modified (no resource injection without "main")
			serverContainer := findContainer(dep.Spec.Template.Spec.Containers, "server")
			Expect(serverContainer).NotTo(BeNil())
			Expect(serverContainer.Resources.Requests).To(Or(BeNil(), BeEmpty()))

			// Node scheduling is still applied
			Expect(dep.Spec.Template.Spec.NodeSelector).To(HaveKeyWithValue("tier", "gpu"))
		})

		It("should give LLMis-specified resource priority over HWP (LLM-7)", func(ctx SpecContext) {
			// given
			svcName := "test-llm-hwp-resource-priority"
			testNs := NewTestNamespace(ctx, envTest, WithIstioShadowService(svcName))

			hwp := HardwareProfile("hwp-resource-prio", testNs.Name, HWPResourceSpec(
				[]string{"cpu", "8"},
			))
			Expect(envTest.Create(ctx, hwp)).To(Succeed())
			defer envTest.Delete(ctx, hwp) //nolint:errcheck

			llmSvc := LLMInferenceService(svcName,
				InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
				WithModelURI("hf://facebook/opt-125m"),
				WithTemplate(&corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  constants.LLMInferenceServiceMainContainerName,
							Image: "test:latest",
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")},
								Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")},
							},
						},
					},
				}),
				WithHardwareProfileAnnotation("hwp-resource-prio"),
				WithManagedRoute(),
				WithManagedGateway(),
			)

			// when
			Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
			defer func() {
				testNs.DeleteAndWait(ctx, llmSvc)
			}()

			// then — IS-specified cpu "2" wins over HWP cpu "8"
			dep := &appsv1.Deployment{}
			Eventually(func(g Gomega, ctx context.Context) error {
				return envTest.Get(ctx, types.NamespacedName{
					Name:      kmeta.ChildName(svcName, "-kserve"),
					Namespace: testNs.Name,
				}, dep)
			}).WithContext(ctx).Should(Succeed())

			mainContainer := findContainer(dep.Spec.Template.Spec.Containers, constants.LLMInferenceServiceMainContainerName)
			Expect(mainContainer).NotTo(BeNil())
			Expect(mainContainer.Resources.Requests[corev1.ResourceCPU]).To(Equal(resource.MustParse("2")))
		})

		It("should give LLMis-specified nodeSelector priority over HWP (LLM-8)", func(ctx SpecContext) {
			// given
			svcName := "test-llm-hwp-node-priority"
			testNs := NewTestNamespace(ctx, envTest, WithIstioShadowService(svcName))

			hwp := HardwareProfile("hwp-node-prio", testNs.Name, HWPNodeSpec(
				map[string]interface{}{
					"zone": "eu-west",
					"tier": "gpu",
				},
				nil,
			))
			Expect(envTest.Create(ctx, hwp)).To(Succeed())
			defer envTest.Delete(ctx, hwp) //nolint:errcheck

			llmSvc := LLMInferenceService(svcName,
				InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
				WithModelURI("hf://facebook/opt-125m"),
				WithTemplate(&corev1.PodSpec{
					NodeSelector: map[string]string{"zone": "us-east"},
					Containers: []corev1.Container{
						{Name: constants.LLMInferenceServiceMainContainerName, Image: "test:latest"},
					},
				}),
				WithHardwareProfileAnnotation("hwp-node-prio"),
				WithManagedRoute(),
				WithManagedGateway(),
			)

			// when
			Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
			defer func() {
				testNs.DeleteAndWait(ctx, llmSvc)
			}()

			// then
			dep := &appsv1.Deployment{}
			Eventually(func(g Gomega, ctx context.Context) error {
				return envTest.Get(ctx, types.NamespacedName{
					Name:      kmeta.ChildName(svcName, "-kserve"),
					Namespace: testNs.Name,
				}, dep)
			}).WithContext(ctx).Should(Succeed())

			Expect(dep.Spec.Template.Spec.NodeSelector).To(HaveKeyWithValue("zone", "us-east"), "LLMis value should win")
			Expect(dep.Spec.Template.Spec.NodeSelector).To(HaveKeyWithValue("tier", "gpu"), "HWP-only key should be added")
		})

		It("should give LLMis-specified Kueue label priority over HWP (LLM-9)", func(ctx SpecContext) {
			// given
			svcName := "test-llm-hwp-kueue-priority"
			testNs := NewTestNamespace(ctx, envTest, WithIstioShadowService(svcName))

			hwp := HardwareProfile("hwp-kueue-prio", testNs.Name, HWPKueueSpec("hwp-queue"))
			Expect(envTest.Create(ctx, hwp)).To(Succeed())
			defer envTest.Delete(ctx, hwp) //nolint:errcheck

			llmSvc := LLMInferenceService(svcName,
				InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
				WithModelURI("hf://facebook/opt-125m"),
				WithTemplate(&corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: constants.LLMInferenceServiceMainContainerName, Image: "test:latest"},
					},
				}),
				WithLabels(map[string]string{constants.KueueQueueNameLabel: "user-queue"}),
				WithHardwareProfileAnnotation("hwp-kueue-prio"),
				WithManagedRoute(),
				WithManagedGateway(),
			)

			// when
			Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
			defer func() {
				testNs.DeleteAndWait(ctx, llmSvc)
			}()

			// then
			dep := &appsv1.Deployment{}
			Eventually(func(g Gomega, ctx context.Context) error {
				return envTest.Get(ctx, types.NamespacedName{
					Name:      kmeta.ChildName(svcName, "-kserve"),
					Namespace: testNs.Name,
				}, dep)
			}).WithContext(ctx).Should(Succeed())

			Expect(dep.Labels).To(HaveKeyWithValue(constants.KueueQueueNameLabel, "user-queue"), "LLMis label should win")
		})
	})

	Context("Multi-Node LeaderWorkerSet", func() {
		It("should inject GPU resources into leader and worker templates (LWS-1)", func(ctx SpecContext) {
			// given
			svcName := "test-lws-hwp-resources"
			testNs := NewTestNamespace(ctx, envTest, WithIstioShadowService(svcName))

			hwp := HardwareProfile("hwp-lws-gpu", testNs.Name, HWPResourceSpec(
				[]string{"nvidia.com/gpu", "4"},
			))
			Expect(envTest.Create(ctx, hwp)).To(Succeed())
			defer envTest.Delete(ctx, hwp) //nolint:errcheck

			llmSvc := LLMInferenceService(svcName,
				InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
				WithModelURI("hf://facebook/opt-125m"),
				WithReplicas(1),
				WithParallelism(ParallelismSpec(
					WithDataParallelism(2),
					WithDataLocalParallelism(2),
				)),
				WithTemplate(SimpleWorkerPodSpec()),
				WithWorker(SimpleWorkerPodSpec()),
				WithHardwareProfileAnnotation("hwp-lws-gpu"),
				WithManagedRoute(),
				WithManagedGateway(),
			)

			// when
			Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
			defer func() {
				testNs.DeleteAndWait(ctx, llmSvc)
			}()

			// then
			lws := &lwsapi.LeaderWorkerSet{}
			Eventually(func(g Gomega, ctx context.Context) error {
				return envTest.Get(ctx, types.NamespacedName{
					Name:      svcName + "-kserve-mn",
					Namespace: testNs.Name,
				}, lws)
			}).WithContext(ctx).Should(Succeed())

			// Leader template GPU
			Expect(lws.Spec.LeaderWorkerTemplate.LeaderTemplate).NotTo(BeNil())
			leaderMain := findContainer(lws.Spec.LeaderWorkerTemplate.LeaderTemplate.Spec.Containers, constants.LLMInferenceServiceMainContainerName)
			Expect(leaderMain).NotTo(BeNil())
			Expect(leaderMain.Resources.Requests["nvidia.com/gpu"]).To(Equal(resource.MustParse("4")))

			// Worker template GPU
			workerMain := findContainer(lws.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec.Containers, constants.LLMInferenceServiceMainContainerName)
			Expect(workerMain).NotTo(BeNil())
			Expect(workerMain.Resources.Requests["nvidia.com/gpu"]).To(Equal(resource.MustParse("4")))
		})

		It("should inject node scheduling into leader and worker pod specs (LWS-2)", func(ctx SpecContext) {
			// given
			svcName := "test-lws-hwp-node"
			testNs := NewTestNamespace(ctx, envTest, WithIstioShadowService(svcName))

			hwp := HardwareProfile("hwp-lws-node", testNs.Name, HWPNodeSpec(
				map[string]interface{}{"nvidia.com/gpu.product": "A100-PCIE-80GB"},
				[]interface{}{
					map[string]interface{}{
						"key":      "nvidia.com/gpu",
						"operator": "Exists",
						"effect":   "NoSchedule",
					},
				},
			))
			Expect(envTest.Create(ctx, hwp)).To(Succeed())
			defer envTest.Delete(ctx, hwp) //nolint:errcheck

			llmSvc := LLMInferenceService(svcName,
				InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
				WithModelURI("hf://facebook/opt-125m"),
				WithReplicas(1),
				WithParallelism(ParallelismSpec(
					WithDataParallelism(2),
					WithDataLocalParallelism(2),
				)),
				WithTemplate(SimpleWorkerPodSpec()),
				WithWorker(SimpleWorkerPodSpec()),
				WithHardwareProfileAnnotation("hwp-lws-node"),
				WithManagedRoute(),
				WithManagedGateway(),
			)

			// when
			Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
			defer func() {
				testNs.DeleteAndWait(ctx, llmSvc)
			}()

			// then
			lws := &lwsapi.LeaderWorkerSet{}
			Eventually(func(g Gomega, ctx context.Context) error {
				return envTest.Get(ctx, types.NamespacedName{
					Name:      svcName + "-kserve-mn",
					Namespace: testNs.Name,
				}, lws)
			}).WithContext(ctx).Should(Succeed())

			expectedTol := corev1.Toleration{
				Key:      "nvidia.com/gpu",
				Operator: corev1.TolerationOpExists,
				Effect:   corev1.TaintEffectNoSchedule,
			}

			// Leader
			Expect(lws.Spec.LeaderWorkerTemplate.LeaderTemplate).NotTo(BeNil())
			Expect(lws.Spec.LeaderWorkerTemplate.LeaderTemplate.Spec.NodeSelector).To(HaveKeyWithValue("nvidia.com/gpu.product", "A100-PCIE-80GB"))
			Expect(lws.Spec.LeaderWorkerTemplate.LeaderTemplate.Spec.Tolerations).To(ContainElement(expectedTol))

			// Worker
			Expect(lws.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec.NodeSelector).To(HaveKeyWithValue("nvidia.com/gpu.product", "A100-PCIE-80GB"))
			Expect(lws.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec.Tolerations).To(ContainElement(expectedTol))
		})

		It("should set Kueue label on LWS top-level, leader, and worker metadata (LWS-3)", func(ctx SpecContext) {
			// given
			svcName := "test-lws-hwp-kueue"
			testNs := NewTestNamespace(ctx, envTest, WithIstioShadowService(svcName))

			hwp := HardwareProfile("hwp-lws-kueue", testNs.Name, HWPKueueSpec("test-queue"))
			Expect(envTest.Create(ctx, hwp)).To(Succeed())
			defer envTest.Delete(ctx, hwp) //nolint:errcheck

			llmSvc := LLMInferenceService(svcName,
				InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
				WithModelURI("hf://facebook/opt-125m"),
				WithReplicas(1),
				WithParallelism(ParallelismSpec(
					WithDataParallelism(2),
					WithDataLocalParallelism(2),
				)),
				WithTemplate(SimpleWorkerPodSpec()),
				WithWorker(SimpleWorkerPodSpec()),
				WithHardwareProfileAnnotation("hwp-lws-kueue"),
				WithManagedRoute(),
				WithManagedGateway(),
			)

			// when
			Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
			defer func() {
				testNs.DeleteAndWait(ctx, llmSvc)
			}()

			// then
			lws := &lwsapi.LeaderWorkerSet{}
			Eventually(func(g Gomega, ctx context.Context) error {
				return envTest.Get(ctx, types.NamespacedName{
					Name:      svcName + "-kserve-mn",
					Namespace: testNs.Name,
				}, lws)
			}).WithContext(ctx).Should(Succeed())

			Expect(lws.Labels).To(HaveKeyWithValue(constants.KueueQueueNameLabel, "test-queue"))
			Expect(lws.Spec.LeaderWorkerTemplate.LeaderTemplate.Labels).To(HaveKeyWithValue(constants.KueueQueueNameLabel, "test-queue"))
			Expect(lws.Spec.LeaderWorkerTemplate.WorkerTemplate.Labels).To(HaveKeyWithValue(constants.KueueQueueNameLabel, "test-queue"))
		})

		It("should abort reconciliation when HWP is not found on multi-node path — LWS not created (LWS-4)", func(ctx SpecContext) {
			// given
			svcName := "test-lws-hwp-not-found"
			testNs := NewTestNamespace(ctx, envTest, WithIstioShadowService(svcName))

			llmSvc := LLMInferenceService(svcName,
				InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
				WithModelURI("hf://facebook/opt-125m"),
				WithReplicas(1),
				WithParallelism(ParallelismSpec(
					WithDataParallelism(2),
					WithDataLocalParallelism(2),
				)),
				WithTemplate(SimpleWorkerPodSpec()),
				WithWorker(SimpleWorkerPodSpec()),
				WithHardwareProfileAnnotation("missing-lws-hwp"),
				WithManagedRoute(),
				WithManagedGateway(),
			)

			// when
			Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
			defer func() {
				testNs.DeleteAndWait(ctx, llmSvc)
			}()

			lwsName := types.NamespacedName{
				Name:      svcName + "-kserve-mn",
				Namespace: testNs.Name,
			}

			// then - LWS is never created while HWP is missing
			Consistently(func(g Gomega, ctx context.Context) {
				lws := &lwsapi.LeaderWorkerSet{}
				err := envTest.Get(ctx, lwsName, lws)
				g.Expect(apierrors.IsNotFound(err)).To(BeTrue(), "LWS should not be created while HWP is missing")
			}).WithContext(ctx).Should(Succeed())

			// sub-step: create the missing HWP → LWS eventually created
			hwp := HardwareProfile("missing-lws-hwp", testNs.Name, HWPResourceSpec(
				[]string{"nvidia.com/gpu", "1"},
			))
			Expect(envTest.Create(ctx, hwp)).To(Succeed())
			defer envTest.Delete(ctx, hwp) //nolint:errcheck

			Eventually(func(g Gomega, ctx context.Context) error {
				lws := &lwsapi.LeaderWorkerSet{}
				return envTest.Get(ctx, lwsName, lws)
			}).WithContext(ctx).Should(Succeed(), "LWS should be created after HWP is available")
		})

		It("should apply HWP stanzas to both main and prefill LWS (LWS-5)", func(ctx SpecContext) {
			// given
			svcName := "test-lws-hwp-prefill"
			testNs := NewTestNamespace(ctx, envTest, WithIstioShadowService(svcName))

			hwp := HardwareProfile("hwp-lws-prefill", testNs.Name, func() map[string]interface{} {
				// Combined spec: GPU resource + node scheduling
				return map[string]interface{}{
					"identifiers": []interface{}{
						map[string]interface{}{"identifier": "nvidia.com/gpu", "defaultCount": "4"},
					},
					"schedulingSpec": map[string]interface{}{
						"type": "Node",
						"node": map[string]interface{}{
							"nodeSelector": map[string]interface{}{
								"nvidia.com/gpu.product": "A100-PCIE-80GB",
							},
						},
					},
				}
			}())
			Expect(envTest.Create(ctx, hwp)).To(Succeed())
			defer envTest.Delete(ctx, hwp) //nolint:errcheck

			llmSvc := LLMInferenceService(svcName,
				InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
				WithModelURI("hf://facebook/opt-125m"),
				WithReplicas(1),
				WithParallelism(ParallelismSpec(
					WithDataParallelism(2),
					WithDataLocalParallelism(2),
				)),
				WithTemplate(SimpleWorkerPodSpec()),
				WithWorker(SimpleWorkerPodSpec()),
				WithPrefill(SimpleWorkerPodSpec()),
				WithPrefillWorker(SimpleWorkerPodSpec()),
				WithPrefillReplicas(1),
				WithPrefillParallelism(ParallelismSpec(
					WithDataParallelism(2),
					WithDataLocalParallelism(2),
				)),
				WithHardwareProfileAnnotation("hwp-lws-prefill"),
				WithManagedRoute(),
				WithManagedGateway(),
			)

			// when
			Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
			defer func() {
				testNs.DeleteAndWait(ctx, llmSvc)
			}()

			// then — check main LWS
			mainLWS := &lwsapi.LeaderWorkerSet{}
			Eventually(func(g Gomega, ctx context.Context) error {
				return envTest.Get(ctx, types.NamespacedName{
					Name:      svcName + "-kserve-mn",
					Namespace: testNs.Name,
				}, mainLWS)
			}).WithContext(ctx).Should(Succeed())

			// Main LWS: leader GPU
			leaderMain := findContainer(mainLWS.Spec.LeaderWorkerTemplate.LeaderTemplate.Spec.Containers, constants.LLMInferenceServiceMainContainerName)
			Expect(leaderMain).NotTo(BeNil())
			Expect(leaderMain.Resources.Requests["nvidia.com/gpu"]).To(Equal(resource.MustParse("4")))
			Expect(mainLWS.Spec.LeaderWorkerTemplate.LeaderTemplate.Spec.NodeSelector).To(HaveKeyWithValue("nvidia.com/gpu.product", "A100-PCIE-80GB"))

			// Main LWS: worker GPU + nodeSelector
			workerMain := findContainer(mainLWS.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec.Containers, constants.LLMInferenceServiceMainContainerName)
			Expect(workerMain).NotTo(BeNil())
			Expect(workerMain.Resources.Requests["nvidia.com/gpu"]).To(Equal(resource.MustParse("4")))
			Expect(mainLWS.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec.NodeSelector).To(HaveKeyWithValue("nvidia.com/gpu.product", "A100-PCIE-80GB"))

			// then — check prefill LWS
			prefillLWS := &lwsapi.LeaderWorkerSet{}
			Eventually(func(g Gomega, ctx context.Context) error {
				return envTest.Get(ctx, types.NamespacedName{
					Name:      kmeta.ChildName(svcName, "-kserve-mn-prefill"),
					Namespace: testNs.Name,
				}, prefillLWS)
			}).WithContext(ctx).Should(Succeed())

			// Prefill LWS: leader GPU
			prefillLeaderMain := findContainer(prefillLWS.Spec.LeaderWorkerTemplate.LeaderTemplate.Spec.Containers, constants.LLMInferenceServiceMainContainerName)
			Expect(prefillLeaderMain).NotTo(BeNil())
			Expect(prefillLeaderMain.Resources.Requests["nvidia.com/gpu"]).To(Equal(resource.MustParse("4")))
			Expect(prefillLWS.Spec.LeaderWorkerTemplate.LeaderTemplate.Spec.NodeSelector).To(HaveKeyWithValue("nvidia.com/gpu.product", "A100-PCIE-80GB"))

			// Prefill LWS: worker GPU + nodeSelector
			prefillWorkerMain := findContainer(prefillLWS.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec.Containers, constants.LLMInferenceServiceMainContainerName)
			Expect(prefillWorkerMain).NotTo(BeNil())
			Expect(prefillWorkerMain.Resources.Requests["nvidia.com/gpu"]).To(Equal(resource.MustParse("4")))
			Expect(prefillLWS.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec.NodeSelector).To(HaveKeyWithValue("nvidia.com/gpu.product", "A100-PCIE-80GB"))
		})
	})
})

// findContainer returns the container with the given name, or nil if not found.
func findContainer(containers []corev1.Container, name string) *corev1.Container {
	for i := range containers {
		if containers[i].Name == name {
			return &containers[i]
		}
	}
	return nil
}
