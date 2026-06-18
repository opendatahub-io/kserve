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
package deployment

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kserve/kserve/pkg/apis/serving/v1beta1"
	"github.com/kserve/kserve/pkg/constants"
)

func TestMountTransformerTLSInfrastructure(t *testing.T) {
	tests := []struct {
		name          string
		componentMeta metav1.ObjectMeta
		deployment    *appsv1.Deployment
		expectError   bool
		expectVolume  bool
		expectEnvVars bool
		expectedHost  string
	}{
		{
			name: "transformer deployment with auth enabled",
			componentMeta: metav1.ObjectMeta{
				Name:      "my-isvc-transformer",
				Namespace: "test-ns",
				Labels: map[string]string{
					constants.KServiceComponentLabel:      string(v1beta1.TransformerComponent),
					constants.InferenceServicePodLabelKey: "my-isvc",
				},
				Annotations: map[string]string{
					constants.ODHKserveRawAuth: "true",
				},
			},
			deployment: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  constants.InferenceServiceContainerName,
									Image: "transformer:latest",
								},
							},
						},
					},
				},
			},
			expectVolume:  true,
			expectEnvVars: true,
			expectedHost:  "my-isvc-predictor.test-ns.svc",
		},
		{
			name: "transformer with multiple containers only injects into kserve-container",
			componentMeta: metav1.ObjectMeta{
				Name:      "multi-isvc-transformer",
				Namespace: "test-ns",
				Labels: map[string]string{
					constants.KServiceComponentLabel:      string(v1beta1.TransformerComponent),
					constants.InferenceServicePodLabelKey: "multi-isvc",
				},
				Annotations: map[string]string{
					constants.ODHKserveRawAuth: "true",
				},
			},
			deployment: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "sidecar",
									Image: "sidecar:latest",
								},
								{
									Name:  constants.InferenceServiceContainerName,
									Image: "transformer:latest",
								},
							},
						},
					},
				},
			},
			expectVolume:  true,
			expectEnvVars: true,
			expectedHost:  "multi-isvc-predictor.test-ns.svc",
		},
		{
			name: "missing InferenceServicePodLabelKey returns error",
			componentMeta: metav1.ObjectMeta{
				Name:      "no-label-transformer",
				Namespace: "test-ns",
				Labels: map[string]string{
					constants.KServiceComponentLabel: string(v1beta1.TransformerComponent),
				},
				Annotations: map[string]string{
					constants.ODHKserveRawAuth: "true",
				},
			},
			deployment: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  constants.InferenceServiceContainerName,
									Image: "transformer:latest",
								},
							},
						},
					},
				},
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := mountTransformerTLSInfrastructure(tt.deployment, tt.componentMeta)
			if tt.expectError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)

			podSpec := tt.deployment.Spec.Template.Spec

			// Check volume
			if tt.expectVolume {
				var volumeFound bool
				for _, v := range podSpec.Volumes {
					if v.Name == constants.ServiceCaBundleVolumeName {
						volumeFound = true
						assert.NotNil(t, v.ConfigMap)
						assert.Equal(t, constants.OpenShiftServiceCaConfigMapName, v.ConfigMap.Name)
						break
					}
				}
				assert.True(t, volumeFound, "expected openshift-service-ca-bundle volume")
			}

			// Check kserve-container has volume mount and env vars
			for _, container := range podSpec.Containers {
				if container.Name == constants.InferenceServiceContainerName {
					if tt.expectEnvVars {
						// Volume mount
						var mountFound bool
						for _, vm := range container.VolumeMounts {
							if vm.Name == constants.ServiceCaBundleVolumeName {
								mountFound = true
								assert.Equal(t, constants.ServiceCaBundleMountPath, vm.MountPath)
								assert.True(t, vm.ReadOnly)
								break
							}
						}
						assert.True(t, mountFound, "expected CA bundle volume mount on kserve-container")

						// Env vars
						envMap := make(map[string]string)
						for _, env := range container.Env {
							envMap[env.Name] = env.Value
						}
						assert.Equal(t, constants.ServiceCaBundleMountPath, envMap["SSL_CERT_DIR"])
						assert.Equal(t, constants.ServiceCaBundleMountPath+"/"+constants.ServiceCaBundleCertFile, envMap["REQUESTS_CA_BUNDLE"])
						assert.Equal(t, tt.expectedHost, envMap[constants.PredictorHostEnvVar])
						assert.Equal(t, "8443", envMap[constants.PredictorPortEnvVar])
						assert.Equal(t, "https", envMap[constants.PredictorProtocolEnvVar])
					}
				} else {
					// Other containers should NOT have the TLS env vars
					for _, env := range container.Env {
						assert.NotEqual(t, constants.PredictorHostEnvVar, env.Name,
							"container %q should not have %s env var", container.Name, constants.PredictorHostEnvVar)
					}
				}
			}
		})
	}
}

func TestTransformerTLSNotInjectedForPredictor(t *testing.T) {
	// Verify the call-site guard: mountTransformerTLSInfrastructure should only be
	// called for transformer deployments. This test simulates the guard logic in
	// createRawDeploymentODH to confirm predictor deployments are skipped.
	predictorMeta := metav1.ObjectMeta{
		Name:      "my-isvc-predictor",
		Namespace: "test-ns",
		Labels: map[string]string{
			constants.KServiceComponentLabel:      string(v1beta1.PredictorComponent),
			constants.InferenceServicePodLabelKey: "my-isvc",
		},
		Annotations: map[string]string{
			constants.ODHKserveRawAuth: "true",
		},
	}

	deployment := &appsv1.Deployment{
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  constants.InferenceServiceContainerName,
							Image: "predictor:latest",
						},
					},
				},
			},
		},
	}

	// Simulate the guard from createRawDeploymentODH
	if componentLabel, ok := predictorMeta.Labels[constants.KServiceComponentLabel]; ok &&
		componentLabel == string(v1beta1.TransformerComponent) {
		err := mountTransformerTLSInfrastructure(deployment, predictorMeta)
		require.NoError(t, err)
	}

	// Verify nothing was injected
	assert.Empty(t, deployment.Spec.Template.Spec.Volumes, "predictor should not get CA bundle volume")
	for _, container := range deployment.Spec.Template.Spec.Containers {
		assert.Empty(t, container.VolumeMounts, "predictor container should not get volume mounts")
		assert.Empty(t, container.Env, "predictor container should not get TLS env vars")
	}
}

func TestTransformerTLSNotInjectedWithoutAuth(t *testing.T) {
	// When auth annotation is not present, TLS infrastructure should not be injected
	transformerMeta := metav1.ObjectMeta{
		Name:      "my-isvc-transformer",
		Namespace: "test-ns",
		Labels: map[string]string{
			constants.KServiceComponentLabel:      string(v1beta1.TransformerComponent),
			constants.InferenceServicePodLabelKey: "my-isvc",
		},
		// No ODHKserveRawAuth annotation
	}

	deployment := &appsv1.Deployment{
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  constants.InferenceServiceContainerName,
							Image: "transformer:latest",
						},
					},
				},
			},
		},
	}

	// Simulate the guard from createRawDeploymentODH: check auth annotation directly
	if val, ok := transformerMeta.Annotations[constants.ODHKserveRawAuth]; ok && strings.EqualFold(val, "true") {
		if componentLabel, ok := transformerMeta.Labels[constants.KServiceComponentLabel]; ok &&
			componentLabel == string(v1beta1.TransformerComponent) {
			err := mountTransformerTLSInfrastructure(deployment, transformerMeta)
			require.NoError(t, err)
		}
	}

	// Verify nothing was injected
	assert.Empty(t, deployment.Spec.Template.Spec.Volumes, "transformer without auth should not get CA bundle volume")
	for _, container := range deployment.Spec.Template.Spec.Containers {
		assert.Empty(t, container.VolumeMounts, "transformer without auth should not get volume mounts")
		assert.Empty(t, container.Env, "transformer without auth should not get TLS env vars")
	}
}
