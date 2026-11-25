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

package llmisvc

import (
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
	"github.com/kserve/kserve/pkg/constants"
	"github.com/kserve/kserve/pkg/credentials"
	kserveTypes "github.com/kserve/kserve/pkg/types"
	"github.com/kserve/kserve/pkg/utils"
)

// caBundleConfigMaps returns the ConfigMaps mounted through the CA bundle volume.
func caBundleConfigMaps(podSpec *corev1.PodSpec) []string {
	var names []string
	for _, volume := range podSpec.Volumes {
		if volume.Name == CaBundleVolumeName && volume.ConfigMap != nil {
			names = append(names, volume.ConfigMap.Name)
		}
	}
	return names
}

func caBundleEnv(c *corev1.Container) string {
	name, _ := utils.GetEnvVarValue(c.Env, constants.CaBundleConfigMapNameEnvVarKey)
	return name
}

var odhCABundleEnv = corev1.EnvVar{Name: constants.CaBundleConfigMapNameEnvVarKey, Value: constants.ODHKServeCustomCABundleConfigMapName}

func TestAttachHfModelArtifactDefaultCABundle(t *testing.T) {
	t.Parallel()

	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: "default"}}
	r := &LLMISVCReconciler{Client: selectorTestClient(t), Clientset: k8sfake.NewSimpleClientset()}
	llmSvc := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{Name: "opt-125m", Namespace: "default"}}
	storageConfig := &kserveTypes.StorageInitializerConfig{Image: "kserve/storage-initializer:test", CpuRequest: "100m", CpuLimit: "1", MemoryRequest: "200Mi", MemoryLimit: "1Gi"}
	credentialConfig := &credentials.CredentialConfig{}
	modelUri := "hf://facebook/opt-125m"

	liveInit := func(args []string, env ...corev1.EnvVar) corev1.PodSpec {
		return corev1.PodSpec{InitContainers: []corev1.Container{{
			Name:  constants.StorageInitializerContainerName,
			Image: storageConfig.Image,
			Args:  args,
			Env:   env,
		}}}
	}

	tests := []struct {
		name string
		curr corev1.PodSpec
		want bool
	}{
		{
			name: "new service",
			curr: corev1.PodSpec{},
			want: true,
		},
		{
			name: "model uri changed from s3 to hf",
			curr: liveInit([]string{"s3://models/facebook/opt-125m", constants.DefaultModelLocalMountPath}),
			want: true,
		},
		{
			name: "hf repo changed",
			curr: liveInit([]string{"hf://facebook/opt-350m", constants.DefaultModelLocalMountPath}),
			want: true,
		},
		{
			name: "same hf uri on a pod that already has the bundle",
			curr: liveInit([]string{modelUri, constants.DefaultModelLocalMountPath}, odhCABundleEnv),
			want: true,
		},
		{
			// The upgrade case: the pod template of a running service must not change.
			name: "same hf uri on a pod without the bundle",
			curr: liveInit([]string{modelUri, constants.DefaultModelLocalMountPath}),
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			podSpec := &corev1.PodSpec{Containers: []corev1.Container{{Name: "main"}}}
			require.NoError(t, r.attachHfModelArtifact(t.Context(), sa, llmSvc, modelUri, tt.curr, podSpec, storageConfig, credentialConfig, "main", constants.DefaultModelLocalMountPath))

			initContainer := utils.GetInitContainerWithName(podSpec, constants.StorageInitializerContainerName)
			require.NotNil(t, initContainer)
			if !tt.want {
				require.Empty(t, caBundleConfigMaps(podSpec))
				require.Empty(t, caBundleEnv(initContainer))
				return
			}
			require.Equal(t, []string{constants.ODHKServeCustomCABundleConfigMapName}, caBundleConfigMaps(podSpec))
			require.Equal(t, constants.ODHKServeCustomCABundleConfigMapName, caBundleEnv(initContainer))
		})
	}
}

func TestAttachMultiStorageDownloadsDefaultCABundle(t *testing.T) {
	t.Parallel()

	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: "default"}}
	r := &LLMISVCReconciler{Client: selectorTestClient(t), Clientset: k8sfake.NewSimpleClientset()}
	llmSvc := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{Name: "opt-125m", Namespace: "default"}}
	baseConfig := kserveTypes.StorageInitializerConfig{Image: "kserve/storage-initializer:test", CpuRequest: "100m", CpuLimit: "1", MemoryRequest: "200Mi", MemoryLimit: "1Gi"}
	credentialConfig := &credentials.CredentialConfig{}

	hfPairs := []storageDownloadPair{
		{uri: "hf://facebook/opt-125m", path: constants.DefaultModelLocalMountPath},
		{uri: "hf://org/lora", path: "/mnt/models/lora/a"},
	}
	hfArgs := []string{"hf://facebook/opt-125m", constants.DefaultModelLocalMountPath, "hf://org/lora", "/mnt/models/lora/a"}
	mixedPairs := []storageDownloadPair{
		{uri: "hf://facebook/opt-125m", path: constants.DefaultModelLocalMountPath},
		{uri: "s3://bucket/lora", path: "/mnt/models/lora/a"},
	}

	liveInit := func(args []string, env ...corev1.EnvVar) corev1.PodSpec {
		return corev1.PodSpec{InitContainers: []corev1.Container{{
			Name:  constants.StorageInitializerContainerName,
			Image: baseConfig.Image,
			Args:  args,
			Env:   env,
		}}}
	}

	tests := []struct {
		name             string
		curr             corev1.PodSpec
		pairs            []storageDownloadPair
		configuredBundle string
		want             []string
	}{
		{
			name:  "new service with hf downloads",
			curr:  corev1.PodSpec{},
			pairs: hfPairs,
			want:  []string{constants.ODHKServeCustomCABundleConfigMapName},
		},
		{
			name:  "adapter added to a pod without the bundle",
			curr:  liveInit([]string{"hf://facebook/opt-125m", constants.DefaultModelLocalMountPath}),
			pairs: hfPairs,
			want:  []string{constants.ODHKServeCustomCABundleConfigMapName},
		},
		{
			name:  "same downloads on a pod that already has the bundle",
			curr:  liveInit(hfArgs, odhCABundleEnv),
			pairs: hfPairs,
			want:  []string{constants.ODHKServeCustomCABundleConfigMapName},
		},
		{
			// The upgrade case: the pod template of a running service must not change.
			name:  "same downloads on a pod without the bundle",
			curr:  liveInit(hfArgs),
			pairs: hfPairs,
			want:  nil,
		},
		{
			name:  "new service with an s3 download",
			curr:  corev1.PodSpec{},
			pairs: mixedPairs,
			want:  nil,
		},
		{
			// An explicitly configured bundle is mounted once, by the upstream path.
			name:             "new service with a configured bundle",
			curr:             corev1.PodSpec{},
			pairs:            hfPairs,
			configuredBundle: "my-ca-bundle",
			want:             []string{constants.DefaultGlobalCaBundleConfigMapName},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			storageConfig := baseConfig
			storageConfig.CaBundleConfigMapName = tt.configuredBundle
			podSpec := &corev1.PodSpec{Containers: []corev1.Container{{Name: "main"}}}
			require.NoError(t, r.attachMultiStorageDownloads(t.Context(), sa, llmSvc, tt.curr, podSpec, &storageConfig, credentialConfig, "main", tt.pairs))

			initContainer := utils.GetInitContainerWithName(podSpec, constants.StorageInitializerContainerName)
			require.NotNil(t, initContainer)
			require.Equal(t, tt.want, caBundleConfigMaps(podSpec))
			if len(tt.want) == 0 {
				require.Empty(t, caBundleEnv(initContainer))
				return
			}
			require.Equal(t, tt.want[0], caBundleEnv(initContainer))
		})
	}
}
