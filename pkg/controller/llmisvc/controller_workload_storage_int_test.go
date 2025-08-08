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
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"knative.dev/pkg/apis"
	"knative.dev/pkg/kmeta"
	lwsapi "sigs.k8s.io/lws/api/leaderworkerset/v1"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
	"github.com/kserve/kserve/pkg/constants"
	. "github.com/kserve/kserve/pkg/controller/llmisvc/fixture"
	"github.com/kserve/kserve/pkg/utils"
)

var _ = Describe("LLMInferenceService Controller - Storage configuration", func() {
	Context("Single node", func() {
		It("should configure direct PVC mount when model uri starts with pvc://", func(ctx SpecContext) {
			// given
			svcName := "test-llm-storage-pvc"
			nsName := kmeta.ChildName(svcName, "-test")
			namespace := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: nsName,
				},
			}
			Expect(envTest.Client.Create(ctx, namespace)).To(Succeed())
			Expect(envTest.Client.Create(ctx, IstioShadowService(svcName, nsName))).To(Succeed())
			defer func() {
				envTest.DeleteAll(namespace)
			}()

			modelURL, err := apis.ParseURL("pvc://facebook-models/opt-125m")
			Expect(err).ToNot(HaveOccurred())

			llmSvc := &v1alpha1.LLMInferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      svcName,
					Namespace: nsName,
				},
				Spec: v1alpha1.LLMInferenceServiceSpec{
					Model: v1alpha1.LLMModelSpec{
						Name: ptr.To("foo"),
						URI:  *modelURL,
					},
					WorkloadSpec: v1alpha1.WorkloadSpec{},
					Router: &v1alpha1.RouterSpec{
						Route:     &v1alpha1.GatewayRoutesSpec{},
						Gateway:   &v1alpha1.GatewaySpec{},
						Scheduler: &v1alpha1.SchedulerSpec{},
					},
					Prefill: &v1alpha1.WorkloadSpec{},
				},
			}

			// when
			Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
			defer func() {
				Expect(envTest.Delete(ctx, llmSvc)).To(Succeed())
			}()

			// then
			expectedMainDeployment := &appsv1.Deployment{}
			Eventually(func(g Gomega, ctx context.Context) error {
				return envTest.Get(ctx, types.NamespacedName{
					Name:      svcName + "-kserve",
					Namespace: nsName,
				}, expectedMainDeployment)
			}).WithContext(ctx).Should(Succeed())

			expectedPrefillDeployment := &appsv1.Deployment{}
			Eventually(func(g Gomega, ctx context.Context) error {
				return envTest.Get(ctx, types.NamespacedName{
					Name:      svcName + "-kserve-prefill",
					Namespace: nsName,
				}, expectedPrefillDeployment)
			}).WithContext(ctx).Should(Succeed())

			validatePvcStorageIsConfigured(expectedMainDeployment)
			validatePvcStorageIsConfigured(expectedPrefillDeployment)
		})

		It("should configure a modelcar when model uri starts with oci://", func(ctx SpecContext) {
			// given
			svcName := "test-llm-storage-oci"
			nsName := kmeta.ChildName(svcName, "-test")
			namespace := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: nsName,
				},
			}
			Expect(envTest.Client.Create(ctx, namespace)).To(Succeed())
			Expect(envTest.Client.Create(ctx, IstioShadowService(svcName, nsName))).To(Succeed())
			defer func() {
				envTest.DeleteAll(namespace)
			}()

			modelURL, err := apis.ParseURL("oci://registry.io/user-id/repo-id:tag")
			Expect(err).ToNot(HaveOccurred())

			llmSvc := &v1alpha1.LLMInferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      svcName,
					Namespace: nsName,
				},
				Spec: v1alpha1.LLMInferenceServiceSpec{
					Model: v1alpha1.LLMModelSpec{
						Name: ptr.To("foo"),
						URI:  *modelURL,
					},
					WorkloadSpec: v1alpha1.WorkloadSpec{},
					Router: &v1alpha1.RouterSpec{
						Route:     &v1alpha1.GatewayRoutesSpec{},
						Gateway:   &v1alpha1.GatewaySpec{},
						Scheduler: &v1alpha1.SchedulerSpec{},
					},
					Prefill: &v1alpha1.WorkloadSpec{},
				},
			}

			// when
			Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
			defer func() {
				Expect(envTest.Delete(ctx, llmSvc)).To(Succeed())
			}()

			// then
			expectedMainDeployment := &appsv1.Deployment{}
			Eventually(func(g Gomega, ctx context.Context) error {
				return envTest.Get(ctx, types.NamespacedName{
					Name:      svcName + "-kserve",
					Namespace: nsName,
				}, expectedMainDeployment)
			}).WithContext(ctx).Should(Succeed())

			expectedPrefillDeployment := &appsv1.Deployment{}
			Eventually(func(g Gomega, ctx context.Context) error {
				return envTest.Get(ctx, types.NamespacedName{
					Name:      svcName + "-kserve-prefill",
					Namespace: nsName,
				}, expectedPrefillDeployment)
			}).WithContext(ctx).Should(Succeed())

			validateOciStorageIsConfigured(expectedMainDeployment)
			validateOciStorageIsConfigured(expectedPrefillDeployment)
		})

		It("should use storage-initializer to download model when uri starts with hf://", func(ctx SpecContext) {
			// given
			svcName := "test-llm-storage-hf"
			nsName := kmeta.ChildName(svcName, "-test")
			namespace := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: nsName,
				},
			}
			Expect(envTest.Client.Create(ctx, namespace)).To(Succeed())
			defer func() {
				envTest.DeleteAll(namespace)
			}()

			modelURL, err := apis.ParseURL("hf://user-id/repo-id:tag")
			Expect(err).ToNot(HaveOccurred())

			llmSvc := &v1alpha1.LLMInferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      svcName,
					Namespace: nsName,
				},
				Spec: v1alpha1.LLMInferenceServiceSpec{
					Model: v1alpha1.LLMModelSpec{
						Name: ptr.To("foo"),
						URI:  *modelURL,
					},
					WorkloadSpec: v1alpha1.WorkloadSpec{},
					Router: &v1alpha1.RouterSpec{
						Route:     &v1alpha1.GatewayRoutesSpec{},
						Gateway:   &v1alpha1.GatewaySpec{},
						Scheduler: &v1alpha1.SchedulerSpec{},
					},
					Prefill: &v1alpha1.WorkloadSpec{},
				},
			}

			// when
			Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
			defer func() {
				Expect(envTest.Delete(ctx, llmSvc)).To(Succeed())
			}()

			// then
			expectedMainDeployment := &appsv1.Deployment{}
			Eventually(func(g Gomega, ctx context.Context) error {
				return envTest.Get(ctx, types.NamespacedName{
					Name:      svcName + "-kserve",
					Namespace: nsName,
				}, expectedMainDeployment)
			}).WithContext(ctx).Should(Succeed())

			expectedPrefillDeployment := &appsv1.Deployment{}
			Eventually(func(g Gomega, ctx context.Context) error {
				return envTest.Get(ctx, types.NamespacedName{
					Name:      svcName + "-kserve-prefill",
					Namespace: nsName,
				}, expectedPrefillDeployment)
			}).WithContext(ctx).Should(Succeed())

			validateStorageInitializerIsConfigured(expectedMainDeployment, "hf://user-id/repo-id:tag")
			validateStorageInitializerIsConfigured(expectedPrefillDeployment, "hf://user-id/repo-id:tag")
		})

		It("should use storage-initializer to download model when uri starts with s3://", func(ctx SpecContext) {
			// given
			svcName := "test-llm-storage-s3"
			nsName := kmeta.ChildName(svcName, "-test")
			namespace := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: nsName,
				},
			}
			Expect(envTest.Client.Create(ctx, namespace)).To(Succeed())
			defer func() {
				envTest.DeleteAll(namespace)
			}()

			modelURL, err := apis.ParseURL("s3://user-id/repo-id:tag")
			Expect(err).ToNot(HaveOccurred())

			llmSvc := &v1alpha1.LLMInferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      svcName,
					Namespace: nsName,
				},
				Spec: v1alpha1.LLMInferenceServiceSpec{
					Model: v1alpha1.LLMModelSpec{
						Name: ptr.To("foo"),
						URI:  *modelURL,
					},
					WorkloadSpec: v1alpha1.WorkloadSpec{},
					Router: &v1alpha1.RouterSpec{
						Route:     &v1alpha1.GatewayRoutesSpec{},
						Gateway:   &v1alpha1.GatewaySpec{},
						Scheduler: &v1alpha1.SchedulerSpec{},
					},
					Prefill: &v1alpha1.WorkloadSpec{},
				},
			}

			// when
			Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
			defer func() {
				Expect(envTest.Delete(ctx, llmSvc)).To(Succeed())
			}()

			// then
			expectedMainDeployment := &appsv1.Deployment{}
			Eventually(func(g Gomega, ctx context.Context) error {
				return envTest.Get(ctx, types.NamespacedName{
					Name:      svcName + "-kserve",
					Namespace: nsName,
				}, expectedMainDeployment)
			}).WithContext(ctx).Should(Succeed())

			expectedPrefillDeployment := &appsv1.Deployment{}
			Eventually(func(g Gomega, ctx context.Context) error {
				return envTest.Get(ctx, types.NamespacedName{
					Name:      svcName + "-kserve-prefill",
					Namespace: nsName,
				}, expectedPrefillDeployment)
			}).WithContext(ctx).Should(Succeed())

			validateStorageInitializerIsConfigured(expectedMainDeployment, "s3://user-id/repo-id:tag")
			validateStorageInitializerIsConfigured(expectedPrefillDeployment, "s3://user-id/repo-id:tag")
		})
	})

	Context("Multi node", func() {
		It("should configure direct PVC mount when model uri starts with pvc://", func(ctx SpecContext) {
			// given
			svcName := "test-llm-storage-pvc-mn"
			nsName := kmeta.ChildName(svcName, "-test")
			namespace := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: nsName,
				},
			}
			Expect(envTest.Client.Create(ctx, namespace)).To(Succeed())
			Expect(envTest.Client.Create(ctx, IstioShadowService(svcName, nsName))).To(Succeed())
			defer func() {
				envTest.DeleteAll(namespace)
			}()

			modelURL, err := apis.ParseURL("pvc://facebook-models/opt-125m")
			Expect(err).ToNot(HaveOccurred())

			llmSvc := &v1alpha1.LLMInferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      svcName,
					Namespace: nsName,
				},
				Spec: v1alpha1.LLMInferenceServiceSpec{
					Model: v1alpha1.LLMModelSpec{
						Name: ptr.To("foo"),
						URI:  *modelURL,
					},
					WorkloadSpec: v1alpha1.WorkloadSpec{
						Worker: &corev1.PodSpec{Containers: []corev1.Container{}},
						Parallelism: &v1alpha1.ParallelismSpec{
							Data:      ptr.To[int32](1),
							DataLocal: ptr.To[int32](1),
						},
					},
					Router: &v1alpha1.RouterSpec{
						Route:     &v1alpha1.GatewayRoutesSpec{},
						Gateway:   &v1alpha1.GatewaySpec{},
						Scheduler: &v1alpha1.SchedulerSpec{},
					},
					Prefill: &v1alpha1.WorkloadSpec{
						Worker: &corev1.PodSpec{Containers: []corev1.Container{}},
						Parallelism: &v1alpha1.ParallelismSpec{
							Data:      ptr.To[int32](1),
							DataLocal: ptr.To[int32](1),
						},
					},
				},
			}

			// when
			Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
			defer func() {
				Expect(envTest.Delete(ctx, llmSvc)).To(Succeed())
			}()

			// then
			expectedMainLWS := &lwsapi.LeaderWorkerSet{}
			Eventually(func(g Gomega, ctx context.Context) error {
				return envTest.Get(ctx, types.NamespacedName{
					Name:      svcName + "-kserve-mn",
					Namespace: nsName,
				}, expectedMainLWS)
			}).WithContext(ctx).Should(Succeed())

			expectedPrefillLWS := &lwsapi.LeaderWorkerSet{}
			Eventually(func(g Gomega, ctx context.Context) error {
				return envTest.Get(ctx, types.NamespacedName{
					Name:      svcName + "-kserve-mn-prefill",
					Namespace: nsName,
				}, expectedPrefillLWS)
			}).WithContext(ctx).Should(Succeed())

			validatePvcStorageIsConfiguredForLWS(expectedMainLWS)
			validatePvcStorageIsConfiguredForLWS(expectedPrefillLWS)
		})

		It("should configure a modelcar when model uri starts with oci://", func(ctx SpecContext) {
			// given
			svcName := "test-llm-storage-oci-mn"
			nsName := kmeta.ChildName(svcName, "-test")
			namespace := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: nsName,
				},
			}
			Expect(envTest.Client.Create(ctx, namespace)).To(Succeed())
			Expect(envTest.Client.Create(ctx, IstioShadowService(svcName, nsName))).To(Succeed())
			defer func() {
				envTest.DeleteAll(namespace)
			}()

			modelURL, err := apis.ParseURL("oci://registry.io/user-id/repo-id:tag")
			Expect(err).ToNot(HaveOccurred())

			llmSvc := &v1alpha1.LLMInferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      svcName,
					Namespace: nsName,
				},
				Spec: v1alpha1.LLMInferenceServiceSpec{
					Model: v1alpha1.LLMModelSpec{
						Name: ptr.To("foo"),
						URI:  *modelURL,
					},
					WorkloadSpec: v1alpha1.WorkloadSpec{
						Worker: &corev1.PodSpec{
							Containers: []corev1.Container{},
						},
						Parallelism: &v1alpha1.ParallelismSpec{
							Data:      ptr.To[int32](1),
							DataLocal: ptr.To[int32](1),
						},
					},
					Router: &v1alpha1.RouterSpec{
						Route:     &v1alpha1.GatewayRoutesSpec{},
						Gateway:   &v1alpha1.GatewaySpec{},
						Scheduler: &v1alpha1.SchedulerSpec{},
					},
					Prefill: &v1alpha1.WorkloadSpec{
						Worker: &corev1.PodSpec{Containers: []corev1.Container{}},
						Parallelism: &v1alpha1.ParallelismSpec{
							Data:      ptr.To[int32](1),
							DataLocal: ptr.To[int32](1),
						},
					},
				},
			}

			// when
			Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
			defer func() {
				Expect(envTest.Delete(ctx, llmSvc)).To(Succeed())
			}()

			// then
			expectedMainLWS := &lwsapi.LeaderWorkerSet{}
			Eventually(func(g Gomega, ctx context.Context) error {
				return envTest.Get(ctx, types.NamespacedName{
					Name:      svcName + "-kserve-mn",
					Namespace: nsName,
				}, expectedMainLWS)
			}).WithContext(ctx).Should(Succeed())

			expectedPrefillLWS := &lwsapi.LeaderWorkerSet{}
			Eventually(func(g Gomega, ctx context.Context) error {
				return envTest.Get(ctx, types.NamespacedName{
					Name:      svcName + "-kserve-mn-prefill",
					Namespace: nsName,
				}, expectedPrefillLWS)
			}).WithContext(ctx).Should(Succeed())

			validateOciStorageIsConfiguredForLWS(expectedMainLWS)
			validateOciStorageIsConfiguredForLWS(expectedPrefillLWS)
		})

		It("should use storage-initializer to download model when uri starts with hf://", func(ctx SpecContext) {
			// given
			svcName := "test-llm-storage-hf-mn"
			nsName := kmeta.ChildName(svcName, "-test")
			namespace := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: nsName,
				},
			}
			Expect(envTest.Client.Create(ctx, namespace)).To(Succeed())
			Expect(envTest.Client.Create(ctx, IstioShadowService(svcName, nsName))).To(Succeed())
			defer func() {
				envTest.DeleteAll(namespace)
			}()

			modelURL, err := apis.ParseURL("hf://user-id/repo-id:tag")
			Expect(err).ToNot(HaveOccurred())

			llmSvc := &v1alpha1.LLMInferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      svcName,
					Namespace: nsName,
				},
				Spec: v1alpha1.LLMInferenceServiceSpec{
					Model: v1alpha1.LLMModelSpec{
						Name: ptr.To("foo"),
						URI:  *modelURL,
					},
					WorkloadSpec: v1alpha1.WorkloadSpec{
						Worker: &corev1.PodSpec{
							Containers: []corev1.Container{},
						},
						Parallelism: &v1alpha1.ParallelismSpec{
							Data:      ptr.To[int32](1),
							DataLocal: ptr.To[int32](1),
						},
					},
					Router: &v1alpha1.RouterSpec{
						Route:     &v1alpha1.GatewayRoutesSpec{},
						Gateway:   &v1alpha1.GatewaySpec{},
						Scheduler: &v1alpha1.SchedulerSpec{},
					},
					Prefill: &v1alpha1.WorkloadSpec{
						Worker: &corev1.PodSpec{Containers: []corev1.Container{}},
						Parallelism: &v1alpha1.ParallelismSpec{
							Data:      ptr.To[int32](1),
							DataLocal: ptr.To[int32](1),
						},
					},
				},
			}

			// when
			Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
			defer func() {
				Expect(envTest.Delete(ctx, llmSvc)).To(Succeed())
			}()

			// then
			expectedMainLWS := &lwsapi.LeaderWorkerSet{}
			Eventually(func(g Gomega, ctx context.Context) error {
				return envTest.Get(ctx, types.NamespacedName{
					Name:      svcName + "-kserve-mn",
					Namespace: nsName,
				}, expectedMainLWS)
			}).WithContext(ctx).Should(Succeed())

			expectedPrefillLWS := &lwsapi.LeaderWorkerSet{}
			Eventually(func(g Gomega, ctx context.Context) error {
				return envTest.Get(ctx, types.NamespacedName{
					Name:      svcName + "-kserve-mn-prefill",
					Namespace: nsName,
				}, expectedPrefillLWS)
			}).WithContext(ctx).Should(Succeed())

			validateStorageInitializerIsConfiguredForLWS(expectedMainLWS, "hf://user-id/repo-id:tag")
			validateStorageInitializerIsConfiguredForLWS(expectedPrefillLWS, "hf://user-id/repo-id:tag")
		})

		It("should use storage-initializer to download model when uri starts with s3://", func(ctx SpecContext) {
			// given
			svcName := "test-llm-storage-s3-mn"
			nsName := kmeta.ChildName(svcName, "-test")
			namespace := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: nsName,
				},
			}
			Expect(envTest.Client.Create(ctx, namespace)).To(Succeed())
			Expect(envTest.Client.Create(ctx, IstioShadowService(svcName, nsName))).To(Succeed())
			defer func() {
				envTest.DeleteAll(namespace)
			}()

			modelURL, err := apis.ParseURL("s3://user-id/repo-id:tag")
			Expect(err).ToNot(HaveOccurred())

			llmSvc := &v1alpha1.LLMInferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      svcName,
					Namespace: nsName,
				},
				Spec: v1alpha1.LLMInferenceServiceSpec{
					Model: v1alpha1.LLMModelSpec{
						Name: ptr.To("foo"),
						URI:  *modelURL,
					},
					WorkloadSpec: v1alpha1.WorkloadSpec{
						Worker: &corev1.PodSpec{
							Containers: []corev1.Container{},
						},
						Parallelism: &v1alpha1.ParallelismSpec{
							Data:      ptr.To[int32](1),
							DataLocal: ptr.To[int32](1),
						},
					},
					Router: &v1alpha1.RouterSpec{
						Route:     &v1alpha1.GatewayRoutesSpec{},
						Gateway:   &v1alpha1.GatewaySpec{},
						Scheduler: &v1alpha1.SchedulerSpec{},
					},
					Prefill: &v1alpha1.WorkloadSpec{
						Worker: &corev1.PodSpec{Containers: []corev1.Container{}},
						Parallelism: &v1alpha1.ParallelismSpec{
							Data:      ptr.To[int32](1),
							DataLocal: ptr.To[int32](1),
						},
					},
				},
			}

			// when
			Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
			defer func() {
				Expect(envTest.Delete(ctx, llmSvc)).To(Succeed())
			}()

			// then
			expectedMainLWS := &lwsapi.LeaderWorkerSet{}
			Eventually(func(g Gomega, ctx context.Context) error {
				return envTest.Get(ctx, types.NamespacedName{
					Name:      svcName + "-kserve-mn",
					Namespace: nsName,
				}, expectedMainLWS)
			}).WithContext(ctx).Should(Succeed())

			expectedPrefillLWS := &lwsapi.LeaderWorkerSet{}
			Eventually(func(g Gomega, ctx context.Context) error {
				return envTest.Get(ctx, types.NamespacedName{
					Name:      svcName + "-kserve-mn-prefill",
					Namespace: nsName,
				}, expectedPrefillLWS)
			}).WithContext(ctx).Should(Succeed())

			validateStorageInitializerIsConfiguredForLWS(expectedMainLWS, "s3://user-id/repo-id:tag")
			validateStorageInitializerIsConfiguredForLWS(expectedPrefillLWS, "s3://user-id/repo-id:tag")
		})
	})
})

func validatePvcStorageIsConfigured(deployment *appsv1.Deployment) {
	validatePvcStorageForPodSpec(&deployment.Spec.Template.Spec)
}

func validateOciStorageIsConfigured(deployment *appsv1.Deployment) {
	validateOciStorageForPodSpec(&deployment.Spec.Template.Spec)
}

func validateStorageInitializerIsConfigured(deployment *appsv1.Deployment, storageUri string) {
	validateStorageInitializerForPodSpec(&deployment.Spec.Template.Spec, storageUri)
}

func validatePvcStorageIsConfiguredForLWS(lws *lwsapi.LeaderWorkerSet) {
	workerSpec := lws.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec
	validatePvcStorageForPodSpec(&workerSpec)
}

func validatePvcStorageForPodSpec(podSpec *corev1.PodSpec) {
	mainContainer := utils.GetContainerWithName(podSpec, "main")
	Expect(mainContainer).ToNot(BeNil())

	Expect(podSpec.Volumes).To(ContainElement(And(
		HaveField("Name", constants.PvcSourceMountName),
		HaveField("VolumeSource.PersistentVolumeClaim.ClaimName", "facebook-models"),
	)))

	Expect(mainContainer.VolumeMounts).To(ContainElement(And(
		HaveField("Name", constants.PvcSourceMountName),
		HaveField("MountPath", constants.DefaultModelLocalMountPath),
		HaveField("ReadOnly", BeTrue()),
		HaveField("SubPath", "opt-125m"),
	)))
}

func validateOciStorageIsConfiguredForLWS(lws *lwsapi.LeaderWorkerSet) {
	workerSpec := lws.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec
	validateOciStorageForPodSpec(&workerSpec)
}

func validateOciStorageForPodSpec(podSpec *corev1.PodSpec) {
	// Check the main container and modelcar container are present.
	mainContainer := utils.GetContainerWithName(podSpec, "main")
	Expect(mainContainer).ToNot(BeNil())
	modelcarContainer := utils.GetContainerWithName(podSpec, constants.ModelcarContainerName)
	Expect(modelcarContainer).ToNot(BeNil())

	// Check container are sharing resources.
	Expect(podSpec.ShareProcessNamespace).To(Not(BeNil()))
	Expect(*podSpec.ShareProcessNamespace).To(BeTrue())

	// Check the model server has an envvar indicating that the model may not be mounted immediately.
	Expect(mainContainer.Env).To(ContainElement(And(
		HaveField("Name", constants.ModelInitModeEnv),
		HaveField("Value", "async"),
	)))

	// Check OCI init container for the pre-fetch
	Expect(podSpec.InitContainers).To(ContainElement(And(
		HaveField("Name", constants.ModelcarInitContainerName),
		HaveField("Resources.Limits", And(HaveKey(corev1.ResourceCPU), HaveKey(corev1.ResourceMemory))),
		HaveField("Resources.Requests", And(HaveKey(corev1.ResourceCPU), HaveKey(corev1.ResourceMemory))),
	)))

	// Basic check of empty dir volume is configured (shared mount between the containers)
	Expect(podSpec.Volumes).To(ContainElement(HaveField("Name", constants.StorageInitializerVolumeName)))

	// Check that the empty-dir volume is mounted to the modelcar and main container (shared storage)
	Expect(mainContainer.VolumeMounts).To(ContainElement(And(
		HaveField("Name", constants.StorageInitializerVolumeName),
		HaveField("MountPath", "/mnt"),
	)))
	Expect(modelcarContainer.VolumeMounts).To(ContainElement(And(
		HaveField("Name", constants.StorageInitializerVolumeName),
		HaveField("MountPath", "/mnt"),
		HaveField("ReadOnly", false),
	)))
}

func validateStorageInitializerIsConfiguredForLWS(lws *lwsapi.LeaderWorkerSet, storageUri string) {
	workerSpec := lws.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec
	validateStorageInitializerForPodSpec(&workerSpec, storageUri)
}

func validateStorageInitializerForPodSpec(podSpec *corev1.PodSpec, storageUri string) {
	// Check the volume to store the model exists
	Expect(podSpec.Volumes).To(ContainElement(And(
		HaveField("Name", constants.StorageInitializerVolumeName),
		HaveField("EmptyDir", Not(BeNil())),
	)))

	// Check the storage-initializer container is present.
	Expect(podSpec.InitContainers).To(ContainElement(And(
		HaveField("Name", constants.StorageInitializerContainerName),
		HaveField("Args", ContainElements(storageUri, constants.DefaultModelLocalMountPath)),
		HaveField("VolumeMounts", ContainElement(And(
			HaveField("Name", constants.StorageInitializerVolumeName),
			HaveField("MountPath", constants.DefaultModelLocalMountPath),
		))),
	)))

	// Check the main container has the model mounted
	mainContainer := utils.GetContainerWithName(podSpec, "main")
	Expect(mainContainer).ToNot(BeNil())
	Expect(mainContainer.VolumeMounts).To(ContainElement(And(
		HaveField("Name", constants.StorageInitializerVolumeName),
		HaveField("MountPath", constants.DefaultModelLocalMountPath),
		HaveField("ReadOnly", BeTrue()),
	)))
}
