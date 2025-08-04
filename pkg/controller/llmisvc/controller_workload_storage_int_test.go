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
})

func validatePvcStorageIsConfigured(deployment *appsv1.Deployment) {
	mainContainer := utils.GetContainerWithName(&deployment.Spec.Template.Spec, "main")
	Expect(mainContainer).ToNot(BeNil())

	Expect(mainContainer.Command).To(ContainElement(constants.DefaultModelLocalMountPath))
	Expect(deployment.Spec.Template.Spec.Volumes).To(ContainElement(And(
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

func validateOciStorageIsConfigured(deployment *appsv1.Deployment) {
	// Check the main container and modelcar container are present.
	mainContainer := utils.GetContainerWithName(&deployment.Spec.Template.Spec, "main")
	Expect(mainContainer).ToNot(BeNil())
	modelcarContainer := utils.GetContainerWithName(&deployment.Spec.Template.Spec, constants.ModelcarContainerName)
	Expect(modelcarContainer).ToNot(BeNil())

	// Check container are sharing resources.
	Expect(deployment.Spec.Template.Spec.ShareProcessNamespace).To(Not(BeNil()))
	Expect(*deployment.Spec.Template.Spec.ShareProcessNamespace).To(BeTrue())

	// Check the model server is directed to the mount point of the OCI container
	Expect(mainContainer.Command).To(ContainElement(constants.DefaultModelLocalMountPath))

	// Check the model server has an envvar indicating that the model may not be mounted immediately.
	Expect(mainContainer.Env).To(ContainElement(And(
		HaveField("Name", constants.ModelInitModeEnv),
		HaveField("Value", "async"),
	)))

	// Check OCI init container for the pre-fetch
	Expect(deployment.Spec.Template.Spec.InitContainers).To(ContainElement(And(
		HaveField("Name", constants.ModelcarInitContainerName),
		HaveField("Resources.Limits", And(HaveKey(corev1.ResourceCPU), HaveKey(corev1.ResourceMemory))),
		HaveField("Resources.Requests", And(HaveKey(corev1.ResourceCPU), HaveKey(corev1.ResourceMemory))),
	)))

	// Basic check of empty dir volume is configured (shared mount between the containers)
	Expect(deployment.Spec.Template.Spec.Volumes).To(ContainElement(HaveField("Name", constants.StorageInitializerVolumeName)))

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

func validateStorageInitializerIsConfigured(deployment *appsv1.Deployment, storageUri string) {
	// Check the volume to store the model exists
	Expect(deployment.Spec.Template.Spec.Volumes).To(ContainElement(And(
		HaveField("Name", constants.StorageInitializerVolumeName),
		HaveField("EmptyDir", Not(BeNil())),
	)))

	// Check the storage-initializer container is present.
	Expect(deployment.Spec.Template.Spec.InitContainers).To(ContainElement(And(
		HaveField("Name", constants.StorageInitializerContainerName),
		HaveField("Args", ContainElements(storageUri, constants.DefaultModelLocalMountPath)),
		HaveField("VolumeMounts", ContainElement(And(
			HaveField("Name", constants.StorageInitializerVolumeName),
			HaveField("MountPath", constants.DefaultModelLocalMountPath),
		))),
	)))

	// Check the main container has the model mounted
	mainContainer := utils.GetContainerWithName(&deployment.Spec.Template.Spec, "main")
	Expect(mainContainer).ToNot(BeNil())
	Expect(mainContainer.Command).To(ContainElement(constants.DefaultModelLocalMountPath))
	Expect(mainContainer.VolumeMounts).To(ContainElement(And(
		HaveField("Name", constants.StorageInitializerVolumeName),
		HaveField("MountPath", constants.DefaultModelLocalMountPath),
		HaveField("ReadOnly", BeTrue()),
	)))
}
