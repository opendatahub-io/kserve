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
package deployment

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	errors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"

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
			name: "missing kserve-container returns error",
			componentMeta: metav1.ObjectMeta{
				Name:      "no-container-transformer",
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
									Name:  "some-other-container",
									Image: "other:latest",
								},
							},
						},
					},
				},
			},
			expectError: true,
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

			// Check CA bundle volume
			if tt.expectVolume {
				var caVolumeFound bool
				for _, v := range podSpec.Volumes {
					if v.Name == constants.ServiceCaBundleVolumeName {
						caVolumeFound = true
						assert.NotNil(t, v.ConfigMap)
						assert.Equal(t, constants.OpenShiftServiceCaConfigMapName, v.ConfigMap.Name)
						break
					}
				}
				assert.True(t, caVolumeFound, "expected openshift-service-ca-bundle volume")

				// Check transformer serving-cert volume
				var tlsVolumeFound bool
				for _, v := range podSpec.Volumes {
					if v.Name == constants.TransformerTLSVolumeName {
						tlsVolumeFound = true
						require.NotNil(t, v.Secret, "transformer-tls volume should be a Secret volume")
						assert.Equal(t, tt.componentMeta.Name+constants.ServingCertSecretSuffix, v.Secret.SecretName)
						break
					}
				}
				assert.True(t, tlsVolumeFound, "expected transformer-tls volume")
			}

			// Check kserve-container has volume mount and env vars,
			// and verify kube-rbac-proxy / oauth-proxy is NOT present.
			for _, container := range podSpec.Containers {
				assert.NotEqual(t, constants.KubeRbacContainerName, container.Name,
					"kube-rbac-proxy should NOT be present in transformer deployment")
				assert.NotEqual(t, constants.OauthProxyContainerName, container.Name,
					"oauth-proxy should NOT be present in transformer deployment")

				if container.Name == constants.InferenceServiceContainerName {
					if tt.expectEnvVars {
						// CA bundle volume mount
						var caMountFound bool
						for _, vm := range container.VolumeMounts {
							if vm.Name == constants.ServiceCaBundleVolumeName {
								caMountFound = true
								assert.Equal(t, constants.ServiceCaBundleMountPath, vm.MountPath)
								assert.True(t, vm.ReadOnly)
								break
							}
						}
						assert.True(t, caMountFound, "expected CA bundle volume mount on kserve-container")

						// Serving-cert volume mount
						var tlsMountFound bool
						for _, vm := range container.VolumeMounts {
							if vm.Name == constants.TransformerTLSVolumeName {
								tlsMountFound = true
								assert.Equal(t, constants.TransformerTLSMountPath, vm.MountPath)
								assert.True(t, vm.ReadOnly)
								break
							}
						}
						assert.True(t, tlsMountFound, "expected transformer-tls volume mount on kserve-container")

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
						assert.Equal(t, constants.TransformerTLSMountPath+"/tls.crt", envMap[constants.TransformerTLSCertEnvVar])
						assert.Equal(t, constants.TransformerTLSMountPath+"/tls.key", envMap[constants.TransformerTLSKeyEnvVar])

						// --predictor_use_ssl arg
						assert.Contains(t, container.Args, constants.ArgumentPredictorUseSSL,
							"expected --predictor_use_ssl arg on kserve-container")

						// HTTPS container port
						var httpsPortFound bool
						for _, port := range container.Ports {
							if port.ContainerPort == constants.TransformerHTTPSPort && port.Protocol == corev1.ProtocolTCP {
								httpsPortFound = true
								break
							}
						}
						assert.True(t, httpsPortFound,
							"expected HTTPS container port %d on kserve-container", constants.TransformerHTTPSPort)
					}
				} else {
					// Other containers should NOT have the TLS env vars or ports
					for _, env := range container.Env {
						assert.NotEqual(t, constants.PredictorHostEnvVar, env.Name,
							"container %q should not have %s env var", container.Name, constants.PredictorHostEnvVar)
					}
					for _, port := range container.Ports {
						assert.NotEqual(t, constants.TransformerHTTPSPort, port.ContainerPort,
							"container %q should not have HTTPS port %d", container.Name, constants.TransformerHTTPSPort)
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

// TestTransformerTLSPortAndProbeOverride verifies that the serving port and
// probes are correctly overridden when TLS is injected. All cases below carry
// the ODHKserveRawAuth=true annotation, which is the guard that triggers
// mountTransformerTLSInfrastructure — without it, no port/probe override
// occurs and the transformer keeps the framework default (8080).
func TestTransformerTLSPortAndProbeOverride(t *testing.T) {
	baseMeta := func() metav1.ObjectMeta {
		return metav1.ObjectMeta{
			Name:      "my-isvc-transformer",
			Namespace: "test-ns",
			Labels: map[string]string{
				constants.KServiceComponentLabel:      string(v1beta1.TransformerComponent),
				constants.InferenceServicePodLabelKey: "my-isvc",
			},
			Annotations: map[string]string{
				constants.ODHKserveRawAuth: "true",
			},
		}
	}

	tests := []struct {
		name             string
		args             []string
		existingProbe    *corev1.Probe
		expectedPort     int32
		expectedHttpPort string // expected --http_port value in args after hook
	}{
		{
			name: "no --http_port: defaults to 8443 and overrides probe",
			args: nil,
			existingProbe: &corev1.Probe{
				ProbeHandler: corev1.ProbeHandler{
					TCPSocket: &corev1.TCPSocketAction{
						Port: intstr.IntOrString{IntVal: 8080},
					},
				},
			},
			expectedPort:     constants.TransformerHTTPSPort,
			expectedHttpPort: strconv.Itoa(int(constants.TransformerHTTPSPort)),
		},
		{
			name: "framework-injected --http_port 8080 is overridden to 8443",
			args: []string{"--model_name", "sentiment-analysis", "--http_port", constants.InferenceServiceDefaultHttpPort},
			existingProbe: &corev1.Probe{
				ProbeHandler: corev1.ProbeHandler{
					TCPSocket: &corev1.TCPSocketAction{
						Port: intstr.IntOrString{IntVal: 8080},
					},
				},
			},
			expectedPort:     constants.TransformerHTTPSPort,
			expectedHttpPort: strconv.Itoa(int(constants.TransformerHTTPSPort)),
		},
		{
			name: "user --http_port=9090 is respected",
			args: []string{"--http_port", "9090"},
			existingProbe: &corev1.Probe{
				ProbeHandler: corev1.ProbeHandler{
					TCPSocket: &corev1.TCPSocketAction{
						Port: intstr.IntOrString{IntVal: 9090},
					},
				},
			},
			expectedPort:     9090,
			expectedHttpPort: "9090",
		},
		{
			name: "user --http_port=8443 equals form",
			args: []string{"--http_port=8443"},
			existingProbe: &corev1.Probe{
				ProbeHandler: corev1.ProbeHandler{
					TCPSocket: &corev1.TCPSocketAction{
						Port: intstr.IntOrString{IntVal: 8080},
					},
				},
			},
			expectedPort:     8443,
			expectedHttpPort: "8443",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deployment := &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:           constants.InferenceServiceContainerName,
									Image:          "transformer:latest",
									Args:           tt.args,
									ReadinessProbe: tt.existingProbe,
								},
							},
						},
					},
				},
			}

			err := mountTransformerTLSInfrastructure(deployment, baseMeta())
			require.NoError(t, err)

			container := deployment.Spec.Template.Spec.Containers[0]

			// Verify container port
			var portFound bool
			for _, port := range container.Ports {
				if port.ContainerPort == tt.expectedPort {
					portFound = true
					break
				}
			}
			assert.True(t, portFound, "expected container port %d", tt.expectedPort)

			// Verify readiness probe port was patched
			require.NotNil(t, container.ReadinessProbe)
			require.NotNil(t, container.ReadinessProbe.TCPSocket)
			assert.Equal(t, tt.expectedPort, container.ReadinessProbe.TCPSocket.Port.IntVal,
				"readiness probe should target port %d", tt.expectedPort)

			// Verify --http_port in args
			argVal, ok := getArgValue(container.Args, constants.ArgumentHttpPort)
			assert.True(t, ok, "expected --http_port in args")
			assert.Equal(t, tt.expectedHttpPort, argVal)
		})
	}
}

func TestTransformerTLSLivenessProbePatched(t *testing.T) {
	meta := metav1.ObjectMeta{
		Name:      "my-isvc-transformer",
		Namespace: "test-ns",
		Labels: map[string]string{
			constants.KServiceComponentLabel:      string(v1beta1.TransformerComponent),
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
							Image: "transformer:latest",
							ReadinessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									TCPSocket: &corev1.TCPSocketAction{
										Port: intstr.IntOrString{IntVal: 8080},
									},
								},
							},
							LivenessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									TCPSocket: &corev1.TCPSocketAction{
										Port: intstr.IntOrString{IntVal: 8080},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	err := mountTransformerTLSInfrastructure(deployment, meta)
	require.NoError(t, err)

	container := deployment.Spec.Template.Spec.Containers[0]
	assert.Equal(t, constants.TransformerHTTPSPort, container.ReadinessProbe.TCPSocket.Port.IntVal)
	assert.Equal(t, constants.TransformerHTTPSPort, container.LivenessProbe.TCPSocket.Port.IntVal)
}

// mockClientForAuthProxyDetection is a mock client for testing auth proxy preservation
type mockClientForAuthProxyDetection struct {
	kclient.Client
	existingDeployment *appsv1.Deployment
	deploymentNotFound bool
}

func (m *mockClientForAuthProxyDetection) Get(ctx context.Context, key kclient.ObjectKey, obj kclient.Object, opts ...kclient.GetOption) error {
	switch o := obj.(type) {
	case *appsv1.Deployment:
		if m.deploymentNotFound {
			return errors.NewNotFound(appsv1.Resource("deployments"), key.Name)
		}
		if m.existingDeployment != nil {
			*o = *m.existingDeployment.DeepCopy()
		}
	case *v1beta1.InferenceService:
		o.ObjectMeta = metav1.ObjectMeta{
			Name:      key.Name,
			Namespace: key.Namespace,
			UID:       "test-uid-12345",
		}
	}
	return nil
}

func (m *mockClientForAuthProxyDetection) Update(ctx context.Context, obj kclient.Object, opts ...kclient.UpdateOption) error {
	return nil
}

func (m *mockClientForAuthProxyDetection) Create(ctx context.Context, obj kclient.Object, opts ...kclient.CreateOption) error {
	return nil
}

func TestGetExistingAuthProxyType(t *testing.T) {
	tests := []struct {
		name               string
		existingDeployment *appsv1.Deployment
		deploymentNotFound bool
		expectedName       string
		expectedImage      string
		expectErr          bool
	}{
		{
			name:               "deployment not found returns empty string",
			deploymentNotFound: true,
			expectedName:       "",
			expectedImage:      "",
		},
		{
			name: "deployment with oauth-proxy container",
			existingDeployment: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{Name: constants.InferenceServiceContainerName},
								{Name: constants.OauthProxyContainerName, Image: "quay.io/oauth-proxy:v1"},
							},
						},
					},
				},
			},
			expectedName:  constants.OauthProxyContainerName,
			expectedImage: "quay.io/oauth-proxy:v1",
		},
		{
			name: "deployment with kube-rbac-proxy container",
			existingDeployment: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{Name: constants.InferenceServiceContainerName},
								{Name: constants.KubeRbacContainerName, Image: "quay.io/kube-rbac-proxy:v2"},
							},
						},
					},
				},
			},
			expectedName:  constants.KubeRbacContainerName,
			expectedImage: "quay.io/kube-rbac-proxy:v2",
		},
		{
			name: "deployment without any auth proxy",
			existingDeployment: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{Name: constants.InferenceServiceContainerName},
							},
						},
					},
				},
			},
			expectedName:  "",
			expectedImage: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &mockClientForAuthProxyDetection{
				existingDeployment: tt.existingDeployment,
				deploymentNotFound: tt.deploymentNotFound,
			}

			resultName, resultImage, _, err := getExistingAuthProxyType(t.Context(), client, "test-ns", "test-deployment")

			if tt.expectErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expectedName, resultName)
				assert.Equal(t, tt.expectedImage, resultImage)
			}
		})
	}
}

func TestCopyAuthProxyFromExisting(t *testing.T) {
	existingContainer := corev1.Container{
		Name:  constants.KubeRbacContainerName,
		Image: "quay.io/opendatahub/odh-kube-auth-proxy@sha256:originalimage",
		Args:  []string{"--arg1", "--arg2"},
		Ports: []corev1.ContainerPort{
			{Name: "https", ContainerPort: 8443},
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: "proxy-tls", MountPath: "/etc/tls/private"},
			{Name: "test-sar-config", MountPath: "/etc/kube-rbac-proxy", ReadOnly: true},
		},
	}

	existingVolumes := []corev1.Volume{
		{
			Name: "proxy-tls",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{SecretName: "test-cert"},
			},
		},
		{
			Name: "test-sar-config",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: "test-sar-config"},
				},
			},
		},
	}

	trueVal := true
	falseVal := false
	existingDeployment := &appsv1.Deployment{
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					AutomountServiceAccountToken: &trueVal,
					Containers: []corev1.Container{
						{
							Name:  constants.InferenceServiceContainerName,
							Image: "test-image",
							VolumeMounts: []corev1.VolumeMount{
								{Name: "proxy-tls", MountPath: "/etc/tls/private"},
							},
						},
						existingContainer,
					},
					Volumes: existingVolumes,
				},
			},
		},
	}

	userVolume := corev1.Volume{
		Name: "user-data",
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{},
		},
	}
	userVolumeMount := corev1.VolumeMount{Name: "user-data", MountPath: "/data"}

	desiredDeployment := &appsv1.Deployment{
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					AutomountServiceAccountToken: &falseVal,
					Containers: []corev1.Container{
						{
							Name:         constants.InferenceServiceContainerName,
							Image:        "test-image",
							VolumeMounts: []corev1.VolumeMount{userVolumeMount},
						},
					},
					Volumes: []corev1.Volume{userVolume},
				},
			},
		},
	}

	copyAuthProxyFromExisting(existingDeployment, desiredDeployment, constants.KubeRbacContainerName)

	var foundContainer *corev1.Container
	for i, c := range desiredDeployment.Spec.Template.Spec.Containers {
		if c.Name == constants.KubeRbacContainerName {
			foundContainer = &desiredDeployment.Spec.Template.Spec.Containers[i]
			break
		}
	}
	require.NotNil(t, foundContainer, "auth proxy container should be copied")
	assert.Equal(t, existingContainer.Image, foundContainer.Image)
	assert.Equal(t, existingContainer.Args, foundContainer.Args)

	// 1 user volume + 2 auth proxy volumes (proxy-tls, test-sar-config)
	assert.Len(t, desiredDeployment.Spec.Template.Spec.Volumes, 3)
	volumeNames := make([]string, 0, len(desiredDeployment.Spec.Template.Spec.Volumes))
	for _, v := range desiredDeployment.Spec.Template.Spec.Volumes {
		volumeNames = append(volumeNames, v.Name)
	}
	assert.Contains(t, volumeNames, "user-data", "user volume should be preserved")
	assert.Contains(t, volumeNames, "proxy-tls", "proxy-tls volume should be added")
	assert.Contains(t, volumeNames, "test-sar-config", "sar-config volume should be added")

	require.NotNil(t, desiredDeployment.Spec.Template.Spec.AutomountServiceAccountToken)
	assert.True(t, *desiredDeployment.Spec.Template.Spec.AutomountServiceAccountToken)

	var kserveContainer *corev1.Container
	for i, c := range desiredDeployment.Spec.Template.Spec.Containers {
		if c.Name == constants.InferenceServiceContainerName {
			kserveContainer = &desiredDeployment.Spec.Template.Spec.Containers[i]
			break
		}
	}
	require.NotNil(t, kserveContainer)
	mountNames := make([]string, 0, len(kserveContainer.VolumeMounts))
	for _, vm := range kserveContainer.VolumeMounts {
		mountNames = append(mountNames, vm.Name)
	}
	assert.Contains(t, mountNames, "user-data", "user volume mount should be preserved")
	assert.Contains(t, mountNames, "proxy-tls", "proxy-tls mount should be added")
}

func TestOauthProxyPreservation(t *testing.T) {
	oauthProxyConfig := fmt.Sprintf(`{"image": "%s", "memoryRequest": "%s", "memoryLimit": "%s", "cpuRequest": "%s", "cpuLimit": "%s"}`,
		constants.OauthProxyImage,
		constants.OauthProxyResourceMemoryRequest,
		constants.OauthProxyResourceMemoryLimit,
		constants.OauthProxyResourceCPURequest,
		constants.OauthProxyResourceCPULimit,
	)

	tests := []struct {
		name                      string
		existingDeployment        *appsv1.Deployment
		deploymentNotFound        bool
		annotations               map[string]string
		expectKubeRbacProxy       bool
		expectOauthProxyPreserved bool
		expectedProxyImage        string
	}{
		{
			name:               "new ISVC with auth enabled gets kube-rbac-proxy",
			deploymentNotFound: true,
			annotations: map[string]string{
				constants.ODHKserveRawAuth: "true",
			},
			expectKubeRbacProxy:       true,
			expectOauthProxyPreserved: false,
			expectedProxyImage:        constants.OauthProxyImage,
		},
		{
			name: "existing ISVC with oauth-proxy is preserved",
			existingDeployment: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{Name: constants.InferenceServiceContainerName},
								{Name: constants.OauthProxyContainerName, Image: "quay.io/oauth-proxy:old"},
							},
						},
					},
				},
			},
			annotations: map[string]string{
				constants.ODHKserveRawAuth: "true",
			},
			expectKubeRbacProxy:       false,
			expectOauthProxyPreserved: true,
		},
		{
			name: "existing ISVC with oauth-proxy and migration annotation gets kube-rbac-proxy",
			existingDeployment: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{Name: constants.InferenceServiceContainerName},
								{Name: constants.OauthProxyContainerName, Image: "quay.io/oauth-proxy:old"},
							},
						},
					},
				},
			},
			annotations: map[string]string{
				constants.ODHKserveRawAuth:           "true",
				constants.ODHAuthProxyTypeAnnotation: constants.KubeRbacProxyType,
			},
			expectKubeRbacProxy:       true,
			expectOauthProxyPreserved: false,
			expectedProxyImage:        constants.OauthProxyImage,
		},
		{
			name: "existing ISVC with kube-rbac-proxy matching config image regenerates normally",
			existingDeployment: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{Name: constants.InferenceServiceContainerName},
								{Name: constants.KubeRbacContainerName, Image: constants.OauthProxyImage},
							},
						},
					},
				},
			},
			annotations: map[string]string{
				constants.ODHKserveRawAuth: "true",
			},
			expectKubeRbacProxy:       true,
			expectOauthProxyPreserved: false,
			expectedProxyImage:        constants.OauthProxyImage,
		},
		{
			name: "existing ISVC with kube-rbac-proxy different image is preserved",
			existingDeployment: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{Name: constants.InferenceServiceContainerName},
								{Name: constants.KubeRbacContainerName, Image: "quay.io/different/image:v1.0.0"},
							},
						},
					},
				},
			},
			annotations: map[string]string{
				constants.ODHKserveRawAuth: "true",
			},
			expectKubeRbacProxy:       true,
			expectOauthProxyPreserved: false,
			expectedProxyImage:        "quay.io/different/image:v1.0.0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &mockClientForAuthProxyDetection{
				existingDeployment: tt.existingDeployment,
				deploymentNotFound: tt.deploymentNotFound,
			}

			clientset := fake.NewSimpleClientset(&corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      constants.InferenceServiceConfigMapName,
					Namespace: constants.KServeNamespace,
				},
				Data: map[string]string{
					oauthProxyISVCConfigKey: oauthProxyConfig,
				},
			})

			objectMeta := metav1.ObjectMeta{
				Name:        "test-predictor",
				Namespace:   "test-ns",
				Annotations: tt.annotations,
				Labels: map[string]string{
					constants.InferenceServicePodLabelKey: "test-isvc",
				},
			}

			podSpec := &corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:  constants.InferenceServiceContainerName,
						Image: "test-image",
						Ports: []corev1.ContainerPort{
							{ContainerPort: 8080},
						},
					},
				},
			}

			deploymentList, _, err := createRawDeploymentODH(
				t.Context(),
				client,
				clientset,
				constants.InferenceServiceResource,
				objectMeta,
				metav1.ObjectMeta{},
				&v1beta1.ComponentExtensionSpec{},
				podSpec,
				nil,
				nil,
			)

			require.NoError(t, err)
			require.Len(t, deploymentList, 1)

			deployment := deploymentList[0]
			var kubeRbacProxyContainer *corev1.Container
			for i, container := range deployment.Spec.Template.Spec.Containers {
				if container.Name == constants.KubeRbacContainerName {
					kubeRbacProxyContainer = &deployment.Spec.Template.Spec.Containers[i]
					break
				}
			}

			hasKubeRbacProxy := kubeRbacProxyContainer != nil
			assert.Equal(t, tt.expectKubeRbacProxy, hasKubeRbacProxy,
				"kube-rbac-proxy presence mismatch")

			if tt.expectOauthProxyPreserved {
				assert.False(t, hasKubeRbacProxy, "oauth-proxy should be preserved, kube-rbac-proxy should not be added")
			}

			if tt.expectedProxyImage != "" && kubeRbacProxyContainer != nil {
				assert.Equal(t, tt.expectedProxyImage, kubeRbacProxyContainer.Image,
					"kube-rbac-proxy image mismatch")
			}
		})
	}
}

func TestDeploymentReconcilerCondition(t *testing.T) {
	oauthProxyConfig := fmt.Sprintf(`{"image": "%s", "memoryRequest": "%s", "memoryLimit": "%s", "cpuRequest": "%s", "cpuLimit": "%s"}`,
		constants.OauthProxyImage,
		constants.OauthProxyResourceMemoryRequest,
		constants.OauthProxyResourceMemoryLimit,
		constants.OauthProxyResourceCPURequest,
		constants.OauthProxyResourceCPULimit,
	)

	tests := []struct {
		name               string
		existingDeployment *appsv1.Deployment
		deploymentNotFound bool
		annotations        map[string]string
		expectCondition    bool
		expectedReason     string
	}{
		{
			name:               "new ISVC does not set condition",
			deploymentNotFound: true,
			annotations: map[string]string{
				constants.ODHKserveRawAuth: "true",
			},
			expectCondition: false,
		},
		{
			name: "existing ISVC with oauth-proxy sets AuthProxyPreserved condition",
			existingDeployment: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{Name: constants.InferenceServiceContainerName},
								{Name: constants.OauthProxyContainerName},
							},
						},
					},
				},
			},
			annotations: map[string]string{
				constants.ODHKserveRawAuth: "true",
			},
			expectCondition: true,
			expectedReason:  "AuthProxyPreserved",
		},
		{
			name: "existing ISVC with kube-rbac-proxy matching config does NOT set condition",
			existingDeployment: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{Name: constants.InferenceServiceContainerName},
								{Name: constants.KubeRbacContainerName, Image: constants.OauthProxyImage},
							},
						},
					},
				},
			},
			annotations: map[string]string{
				constants.ODHKserveRawAuth: "true",
			},
			expectCondition: false,
		},
		{
			name: "existing ISVC with kube-rbac-proxy different image sets AuthProxyPreserved condition",
			existingDeployment: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{Name: constants.InferenceServiceContainerName},
								{Name: constants.KubeRbacContainerName, Image: "quay.io/different/image:v1.0.0"},
							},
						},
					},
				},
			},
			annotations: map[string]string{
				constants.ODHKserveRawAuth: "true",
			},
			expectCondition: true,
			expectedReason:  "AuthProxyPreserved",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &mockClientForAuthProxyDetection{
				existingDeployment: tt.existingDeployment,
				deploymentNotFound: tt.deploymentNotFound,
			}

			clientset := fake.NewSimpleClientset(&corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      constants.InferenceServiceConfigMapName,
					Namespace: constants.KServeNamespace,
				},
				Data: map[string]string{
					oauthProxyISVCConfigKey: oauthProxyConfig,
				},
			})

			objectMeta := metav1.ObjectMeta{
				Name:        "test-predictor",
				Namespace:   "test-ns",
				Annotations: tt.annotations,
				Labels: map[string]string{
					constants.InferenceServicePodLabelKey: "test-isvc",
				},
			}

			podSpec := &corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:  constants.InferenceServiceContainerName,
						Image: "test-image",
						Ports: []corev1.ContainerPort{
							{ContainerPort: 8080},
						},
					},
				},
			}

			reconciler, err := NewDeploymentReconciler(
				t.Context(),
				client,
				clientset,
				nil,
				constants.InferenceServiceResource,
				objectMeta,
				metav1.ObjectMeta{},
				&v1beta1.ComponentExtensionSpec{},
				podSpec,
				nil,
				nil,
			)

			require.NoError(t, err)
			require.NotNil(t, reconciler)

			cond, condType := reconciler.GetAuthProxyCondition()
			if tt.expectCondition {
				require.NotNil(t, cond, "expected condition to be set")
				assert.Equal(t, tt.expectedReason, cond.Reason)
				assert.Equal(t, corev1.ConditionFalse, cond.Status)
				assert.Equal(t, v1beta1.LatestDeploymentReady, condType)
			} else {
				assert.Nil(t, cond, "expected condition to be nil")
			}
		})
	}
}

func TestGetAuthProxyConditionNoCondition(t *testing.T) {
	reconciler := &DeploymentReconciler{}
	cond, condType := reconciler.GetAuthProxyCondition()
	assert.Nil(t, cond)
	assert.Empty(t, condType)
}

// Tests for OAuth proxy always added to new deployments
func TestNewRawDeploymentWithAuthDisabled_IncludesOAuthProxy(t *testing.T) {
	oauthProxyConfig := fmt.Sprintf(`{"image": "%s", "memoryRequest": "%s", "memoryLimit": "%s", "cpuRequest": "%s", "cpuLimit": "%s"}`,
		constants.OauthProxyImage,
		constants.OauthProxyResourceMemoryRequest,
		constants.OauthProxyResourceMemoryLimit,
		constants.OauthProxyResourceCPURequest,
		constants.OauthProxyResourceCPULimit,
	)

	client := &mockClientForCheckDeploymentExist{
		getErr: errors.NewNotFound(appsv1.Resource("deployment"), "default-predictor"),
	}
	clientset := fake.NewSimpleClientset(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: constants.InferenceServiceConfigMapName, Namespace: constants.KServeNamespace},
		Data: map[string]string{
			oauthProxyISVCConfigKey: oauthProxyConfig,
		},
	})

	objectMeta := metav1.ObjectMeta{
		Name:      "default-predictor",
		Namespace: "default-predictor-namespace",
		Annotations: map[string]string{
			constants.ODHKserveRawAuth: "false", // Auth disabled
		},
		Labels: map[string]string{
			constants.DeploymentMode:  string(constants.Standard),
			constants.AutoscalerClass: string(constants.DefaultAutoscalerClass),
		},
	}

	deployments, _, err := createRawDeploymentODH(
		context.TODO(),
		client,
		clientset,
		constants.InferenceServiceResource,
		objectMeta,
		metav1.ObjectMeta{},
		&v1beta1.ComponentExtensionSpec{},
		&corev1.PodSpec{},
		nil,
		nil,
	)

	require.NoError(t, err)
	require.NotEmpty(t, deployments)

	// Verify OAuth proxy container is present even though auth is disabled
	containers := deployments[0].Spec.Template.Spec.Containers
	oauthProxyFound := false
	for _, container := range containers {
		if container.Name == constants.KubeRbacContainerName {
			oauthProxyFound = true
			break
		}
	}
	assert.True(t, oauthProxyFound, "OAuth proxy should be present in new deployment even with auth disabled")

	// Verify AutomountServiceAccountToken is set
	assert.NotNil(t, deployments[0].Spec.Template.Spec.AutomountServiceAccountToken)
	assert.True(t, *deployments[0].Spec.Template.Spec.AutomountServiceAccountToken)

	// Verify volumes are mounted
	volumes := deployments[0].Spec.Template.Spec.Volumes
	tlsVolumeFound := false
	sarVolumeFound := false
	for _, vol := range volumes {
		if vol.Name == "proxy-tls" {
			tlsVolumeFound = true
		}
		if vol.Name == constants.OauthProxySARCMName {
			sarVolumeFound = true
		}
	}
	assert.True(t, tlsVolumeFound, "TLS volume should be mounted")
	assert.True(t, sarVolumeFound, "SAR ConfigMap volume should be mounted")
}

func TestNewRawDeploymentWithAuthEnabled_IncludesOAuthProxy(t *testing.T) {
	oauthProxyConfig := fmt.Sprintf(`{"image": "%s", "memoryRequest": "%s", "memoryLimit": "%s", "cpuRequest": "%s", "cpuLimit": "%s"}`,
		constants.OauthProxyImage,
		constants.OauthProxyResourceMemoryRequest,
		constants.OauthProxyResourceMemoryLimit,
		constants.OauthProxyResourceCPURequest,
		constants.OauthProxyResourceCPULimit,
	)

	client := &mockClientForCheckDeploymentExist{
		getErr: errors.NewNotFound(appsv1.Resource("deployment"), "auth-enabled-predictor"),
	}
	clientset := fake.NewSimpleClientset(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: constants.InferenceServiceConfigMapName, Namespace: constants.KServeNamespace},
		Data: map[string]string{
			oauthProxyISVCConfigKey: oauthProxyConfig,
		},
	})

	objectMeta := metav1.ObjectMeta{
		Name:      "auth-enabled-predictor",
		Namespace: "default-predictor-namespace",
		Annotations: map[string]string{
			constants.ODHKserveRawAuth: "true", // Auth enabled
		},
		Labels: map[string]string{
			constants.DeploymentMode:  string(constants.Standard),
			constants.AutoscalerClass: string(constants.DefaultAutoscalerClass),
		},
	}

	deployments, _, err := createRawDeploymentODH(
		context.TODO(),
		client,
		clientset,
		constants.InferenceServiceResource,
		objectMeta,
		metav1.ObjectMeta{},
		&v1beta1.ComponentExtensionSpec{},
		&corev1.PodSpec{},
		nil,
		nil,
	)

	require.NoError(t, err)
	require.NotEmpty(t, deployments)

	// Verify OAuth proxy container is present
	containers := deployments[0].Spec.Template.Spec.Containers
	oauthProxyFound := false
	for _, container := range containers {
		if container.Name == constants.KubeRbacContainerName {
			oauthProxyFound = true
			break
		}
	}
	assert.True(t, oauthProxyFound, "OAuth proxy should be present in new deployment with auth enabled")
}

func TestExistingRawDeploymentWithAuthDisabled_NoOAuthProxyAdded(t *testing.T) {
	oauthProxyConfig := fmt.Sprintf(`{"image": "%s", "memoryRequest": "%s", "memoryLimit": "%s", "cpuRequest": "%s", "cpuLimit": "%s"}`,
		constants.OauthProxyImage,
		constants.OauthProxyResourceMemoryRequest,
		constants.OauthProxyResourceMemoryLimit,
		constants.OauthProxyResourceCPURequest,
		constants.OauthProxyResourceCPULimit,
	)

	existingDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "existing-predictor",
			Namespace: "default-predictor-namespace",
		},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: constants.InferenceServiceContainerName},
					},
				},
			},
		},
	}

	client := &mockClientForCheckDeploymentExist{
		getDeployment: existingDeployment,
		getErr:        nil,
	}
	clientset := fake.NewSimpleClientset(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: constants.InferenceServiceConfigMapName, Namespace: constants.KServeNamespace},
		Data: map[string]string{
			oauthProxyISVCConfigKey: oauthProxyConfig,
		},
	})

	objectMeta := metav1.ObjectMeta{
		Name:      "existing-predictor",
		Namespace: "default-predictor-namespace",
		Annotations: map[string]string{
			constants.ODHKserveRawAuth: "false",
		},
		Labels: map[string]string{
			constants.DeploymentMode:  string(constants.Standard),
			constants.AutoscalerClass: string(constants.DefaultAutoscalerClass),
		},
	}

	deployments, _, err := createRawDeploymentODH(
		context.TODO(),
		client,
		clientset,
		constants.InferenceServiceResource,
		objectMeta,
		metav1.ObjectMeta{},
		&v1beta1.ComponentExtensionSpec{},
		&corev1.PodSpec{},
		nil,
		nil,
	)

	require.NoError(t, err)
	require.NotEmpty(t, deployments)

	// Verify OAuth proxy is NOT added to existing deployment with auth disabled
	containers := deployments[0].Spec.Template.Spec.Containers
	oauthProxyFound := false
	for _, container := range containers {
		if container.Name == constants.KubeRbacContainerName {
			oauthProxyFound = true
			break
		}
	}
	assert.False(t, oauthProxyFound, "OAuth proxy should NOT be added to existing deployment with auth disabled")
}

func TestExistingRawDeploymentWithAuthEnabled_PreservesOAuthProxy(t *testing.T) {
	oauthProxyConfig := fmt.Sprintf(`{"image": "%s", "memoryRequest": "%s", "memoryLimit": "%s", "cpuRequest": "%s", "cpuLimit": "%s"}`,
		constants.OauthProxyImage,
		constants.OauthProxyResourceMemoryRequest,
		constants.OauthProxyResourceMemoryLimit,
		constants.OauthProxyResourceCPURequest,
		constants.OauthProxyResourceCPULimit,
	)

	existingDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "existing-auth-predictor",
			Namespace: "default-predictor-namespace",
		},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: constants.InferenceServiceContainerName},
						{Name: constants.KubeRbacContainerName},
					},
				},
			},
		},
	}

	client := &mockClientForCheckDeploymentExist{
		getDeployment: existingDeployment,
		getErr:        nil,
	}
	clientset := fake.NewSimpleClientset(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: constants.InferenceServiceConfigMapName, Namespace: constants.KServeNamespace},
		Data: map[string]string{
			oauthProxyISVCConfigKey: oauthProxyConfig,
		},
	})

	objectMeta := metav1.ObjectMeta{
		Name:      "existing-auth-predictor",
		Namespace: "default-predictor-namespace",
		Annotations: map[string]string{
			constants.ODHKserveRawAuth: "true",
		},
		Labels: map[string]string{
			constants.DeploymentMode:  string(constants.Standard),
			constants.AutoscalerClass: string(constants.DefaultAutoscalerClass),
		},
	}

	deployments, _, err := createRawDeploymentODH(
		context.TODO(),
		client,
		clientset,
		constants.InferenceServiceResource,
		objectMeta,
		metav1.ObjectMeta{},
		&v1beta1.ComponentExtensionSpec{},
		&corev1.PodSpec{},
		nil,
		nil,
	)

	require.NoError(t, err)
	require.NotEmpty(t, deployments)

	// Verify OAuth proxy is still present in existing deployment with auth enabled
	containers := deployments[0].Spec.Template.Spec.Containers
	oauthProxyFound := false
	for _, container := range containers {
		if container.Name == constants.KubeRbacContainerName {
			oauthProxyFound = true
			break
		}
	}
	assert.True(t, oauthProxyFound, "OAuth proxy should be preserved in existing deployment with auth enabled")
}

func TestNewInferenceGraph_NoOAuthProxy(t *testing.T) {
	oauthProxyConfig := fmt.Sprintf(`{"image": "%s", "memoryRequest": "%s", "memoryLimit": "%s", "cpuRequest": "%s", "cpuLimit": "%s"}`,
		constants.OauthProxyImage,
		constants.OauthProxyResourceMemoryRequest,
		constants.OauthProxyResourceMemoryLimit,
		constants.OauthProxyResourceCPURequest,
		constants.OauthProxyResourceCPULimit,
	)

	client := &mockClientForCheckDeploymentExist{
		getErr: errors.NewNotFound(appsv1.Resource("deployment"), "ig-predictor"),
	}
	clientset := fake.NewSimpleClientset(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: constants.InferenceServiceConfigMapName, Namespace: constants.KServeNamespace},
		Data: map[string]string{
			oauthProxyISVCConfigKey: oauthProxyConfig,
		},
	})

	objectMeta := metav1.ObjectMeta{
		Name:      "ig-predictor",
		Namespace: "default-predictor-namespace",
		Annotations: map[string]string{
			constants.ODHKserveRawAuth: "false",
		},
		Labels: map[string]string{
			constants.DeploymentMode:  string(constants.Standard),
			constants.AutoscalerClass: string(constants.DefaultAutoscalerClass),
		},
	}

	deployments, _, err := createRawDeploymentODH(
		context.TODO(),
		client,
		clientset,
		constants.InferenceGraphResource,
		objectMeta,
		metav1.ObjectMeta{},
		&v1beta1.ComponentExtensionSpec{},
		&corev1.PodSpec{},
		nil,
		nil,
	)

	require.NoError(t, err)
	require.NotEmpty(t, deployments)

	// Verify OAuth proxy is NOT added to InferenceGraph
	containers := deployments[0].Spec.Template.Spec.Containers
	oauthProxyFound := false
	for _, container := range containers {
		if container.Name == constants.KubeRbacContainerName {
			oauthProxyFound = true
			break
		}
	}
	assert.False(t, oauthProxyFound, "OAuth proxy should NOT be added to InferenceGraph resources")

	// Verify TLS volumes ARE still mounted (for serving cert)
	volumes := deployments[0].Spec.Template.Spec.Volumes
	tlsVolumeFound := false
	for _, vol := range volumes {
		if vol.Name == "proxy-tls" {
			tlsVolumeFound = true
			break
		}
	}
	assert.True(t, tlsVolumeFound, "TLS volume should be mounted for InferenceGraph")
}

func TestSarVolumeNameForDeployment(t *testing.T) {
	tests := []struct {
		name               string
		isvcName           string
		existingDeployment *appsv1.Deployment
		expectedVolumeName string
	}{
		{
			name:               "nil existing deployment returns static name",
			isvcName:           "my-isvc",
			existingDeployment: nil,
			expectedVolumeName: constants.OauthProxySARCMName,
		},
		{
			name:     "existing deployment without legacy volume returns static name",
			isvcName: "my-isvc",
			existingDeployment: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Volumes: []corev1.Volume{
								{Name: "some-other-volume"},
							},
						},
					},
				},
			},
			expectedVolumeName: constants.OauthProxySARCMName,
		},
		{
			name:     "existing deployment with legacy volume preserves it",
			isvcName: "my-isvc",
			existingDeployment: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Volumes: []corev1.Volume{
								{Name: fmt.Sprintf("%s-%s", "my-isvc", constants.OauthProxySARCMName)},
							},
						},
					},
				},
			},
			expectedVolumeName: fmt.Sprintf("%s-%s", "my-isvc", constants.OauthProxySARCMName),
		},
		{
			name:     "existing deployment with static volume name keeps static name",
			isvcName: "my-isvc",
			existingDeployment: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Volumes: []corev1.Volume{
								{Name: constants.OauthProxySARCMName},
							},
						},
					},
				},
			},
			expectedVolumeName: constants.OauthProxySARCMName,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sarVolumeNameForDeployment(tt.isvcName, tt.existingDeployment)
			assert.Equal(t, tt.expectedVolumeName, result)
		})
	}
}

func TestUpgradePreservesLegacyVolumeName(t *testing.T) {
	oauthCfg := fmt.Sprintf(`{"image": "%s", "memoryRequest": "%s", "memoryLimit": "%s", "cpuRequest": "%s", "cpuLimit": "%s"}`,
		constants.OauthProxyImage,
		constants.OauthProxyResourceMemoryRequest,
		constants.OauthProxyResourceMemoryLimit,
		constants.OauthProxyResourceCPURequest,
		constants.OauthProxyResourceCPULimit,
	)

	isvcName := "my-model"
	legacyVolumeName := fmt.Sprintf("%s-%s", isvcName, constants.OauthProxySARCMName)

	tests := []struct {
		name               string
		existingDeployment *appsv1.Deployment
		deploymentNotFound bool
		expectedVolName    string
		description        string
	}{
		{
			name:               "new deployment uses static volume name",
			deploymentNotFound: true,
			expectedVolName:    constants.OauthProxySARCMName,
			description:        "brand new ISVC should get the short static volume name",
		},
		{
			name: "upgrade with legacy volume name preserves it",
			existingDeployment: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{Name: constants.InferenceServiceContainerName},
								{Name: constants.KubeRbacContainerName, Image: constants.OauthProxyImage},
							},
							Volumes: []corev1.Volume{
								{Name: legacyVolumeName},
							},
						},
					},
				},
			},
			expectedVolName: legacyVolumeName,
			description:     "existing ISVC with legacy volume name should keep it to avoid rollout",
		},
		{
			name: "existing deployment already using static name keeps it",
			existingDeployment: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{Name: constants.InferenceServiceContainerName},
								{Name: constants.KubeRbacContainerName, Image: constants.OauthProxyImage},
							},
							Volumes: []corev1.Volume{
								{Name: constants.OauthProxySARCMName},
							},
						},
					},
				},
			},
			expectedVolName: constants.OauthProxySARCMName,
			description:     "existing ISVC already on static name should stay on it",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &mockClientForAuthProxyDetection{
				existingDeployment: tt.existingDeployment,
				deploymentNotFound: tt.deploymentNotFound,
			}

			clientset := fake.NewSimpleClientset(&corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      constants.InferenceServiceConfigMapName,
					Namespace: constants.KServeNamespace,
				},
				Data: map[string]string{
					oauthProxyISVCConfigKey: oauthCfg,
				},
			})

			objectMeta := metav1.ObjectMeta{
				Name:      isvcName + "-predictor",
				Namespace: "test-ns",
				Annotations: map[string]string{
					constants.ODHKserveRawAuth: "true",
				},
				Labels: map[string]string{
					constants.InferenceServicePodLabelKey: isvcName,
				},
			}

			podSpec := &corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:  constants.InferenceServiceContainerName,
						Image: "test-image",
						Ports: []corev1.ContainerPort{
							{ContainerPort: 8080},
						},
					},
				},
			}

			deploymentList, _, err := createRawDeploymentODH(
				t.Context(),
				client,
				clientset,
				constants.InferenceServiceResource,
				objectMeta,
				metav1.ObjectMeta{},
				&v1beta1.ComponentExtensionSpec{},
				podSpec,
				nil,
				nil,
			)

			require.NoError(t, err, tt.description)
			require.Len(t, deploymentList, 1)

			deployment := deploymentList[0]

			// Check the volume name matches expected
			var sarVolFound bool
			for _, vol := range deployment.Spec.Template.Spec.Volumes {
				if vol.Name == tt.expectedVolName {
					sarVolFound = true
					// Verify the ConfigMap reference always uses the full name
					require.NotNil(t, vol.ConfigMap, "SAR volume should reference a ConfigMap")
					expectedCMName := fmt.Sprintf("%s-%s", isvcName, constants.OauthProxySARCMName)
					assert.Equal(t, expectedCMName, vol.ConfigMap.Name,
						"ConfigMap reference should always be the full isvc-prefixed name")
					break
				}
			}
			assert.True(t, sarVolFound, "expected volume %q not found in deployment: %s", tt.expectedVolName, tt.description)

			// Check the container volume mount matches the same name
			for _, c := range deployment.Spec.Template.Spec.Containers {
				if c.Name == constants.KubeRbacContainerName {
					var mountFound bool
					for _, vm := range c.VolumeMounts {
						if vm.Name == tt.expectedVolName && vm.MountPath == "/etc/kube-rbac-proxy" {
							mountFound = true
							break
						}
					}
					assert.True(t, mountFound, "kube-rbac-proxy container should have volume mount %q: %s", tt.expectedVolName, tt.description)
					break
				}
			}
		})
	}
}
