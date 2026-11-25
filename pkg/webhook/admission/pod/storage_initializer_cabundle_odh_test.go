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

package pod

import (
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kserve/kserve/pkg/apis/serving/v1beta1"
	"github.com/kserve/kserve/pkg/constants"
	"github.com/kserve/kserve/pkg/credentials"
	"github.com/kserve/kserve/pkg/utils"
)

// withPlatformStorageInitializerDefaults adds the default CA bundle to the pod spec a
// shared test expects, so upstream scenarios keep their expectations build-neutral.
func withPlatformStorageInitializerDefaults(spec corev1.PodSpec) corev1.PodSpec {
	expected := spec.DeepCopy()
	utils.DefaultODHCustomCABundle(expected, utils.GetInitContainerWithName(expected, constants.StorageInitializerContainerName), "")
	return *expected
}

func TestStorageInitializerDefaultODHCustomCABundle(t *testing.T) {
	scenarios := map[string]struct {
		srcURI           string
		configuredBundle string
		// wantConfigMap is the ConfigMap mounted through the CA bundle volume, or empty for none.
		wantConfigMap string
	}{
		"https source": {
			srcURI:        "https://models.example.test/model.tar",
			wantConfigMap: constants.ODHKServeCustomCABundleConfigMapName,
		},
		"hf source": {
			srcURI:        "hf://org/model",
			wantConfigMap: constants.ODHKServeCustomCABundleConfigMapName,
		},
		"gs source": {
			srcURI: "gs://foo",
		},
		// An explicitly configured bundle is mounted once, by the upstream path.
		"https source with a configured bundle": {
			srcURI:           "https://models.example.test/model.tar",
			configuredBundle: "my-ca-bundle",
			wantConfigMap:    constants.DefaultGlobalCaBundleConfigMapName,
		},
	}

	for name, scenario := range scenarios {
		t.Run(name, func(t *testing.T) {
			config := *storageInitializerConfig
			config.CaBundleConfigMapName = scenario.configuredBundle
			injector := &StorageInitializerInjector{
				credentialBuilder: credentials.NewCredentialBuilder(c, clientset, &corev1.ConfigMap{
					Data: map[string]string{},
				}),
				config:          &config,
				openshiftConfig: &v1beta1.OpenShiftConfig{},
				client:          c,
			}
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "default",
					Annotations: map[string]string{
						constants.StorageInitializerSourceUriInternalAnnotationKey: scenario.srcURI,
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: constants.InferenceServiceContainerName}},
				},
			}

			require.NoError(t, injector.InjectStorageInitializer(t.Context(), pod))

			initContainer := utils.GetInitContainerWithName(&pod.Spec, constants.StorageInitializerContainerName)
			require.NotNil(t, initContainer)

			var mounted []string
			for _, volume := range pod.Spec.Volumes {
				if volume.Name == CaBundleVolumeName && volume.ConfigMap != nil {
					mounted = append(mounted, volume.ConfigMap.Name)
				}
			}
			configMapEnv, _ := utils.GetEnvVarValue(initContainer.Env, constants.CaBundleConfigMapNameEnvVarKey)

			if scenario.wantConfigMap == "" {
				require.Empty(t, mounted)
				require.Empty(t, configMapEnv)
				return
			}
			require.Equal(t, []string{scenario.wantConfigMap}, mounted)
			require.Equal(t, scenario.wantConfigMap, configMapEnv)

			// Admission can run more than once for a pod.
			volumes := len(pod.Spec.Volumes)
			require.NoError(t, injector.InjectStorageInitializer(t.Context(), pod))
			require.Len(t, pod.Spec.Volumes, volumes)
		})
	}
}
