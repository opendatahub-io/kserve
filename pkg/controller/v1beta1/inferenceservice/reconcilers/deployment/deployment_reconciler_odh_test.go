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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

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

			// Check kserve-container has volume mount and env vars,
			// and verify kube-rbac-proxy / oauth-proxy is NOT present.
			for _, container := range podSpec.Containers {
				assert.NotEqual(t, constants.KubeRbacContainerName, container.Name,
					"kube-rbac-proxy should NOT be present in transformer deployment")
				assert.NotEqual(t, constants.OauthProxyContainerName, container.Name,
					"oauth-proxy should NOT be present in transformer deployment")

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

						// --predictor_use_ssl arg
						assert.Contains(t, container.Args, constants.ArgumentPredictorUseSSL,
							"expected --predictor_use_ssl arg on kserve-container")
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

func TestCreateRawDeploymentODHAuditLogging(t *testing.T) {
	tests := []struct {
		name                string
		profile             constants.AuditLoggingProfile
		manageAuditLogging  bool
		annotations         map[string]string
		existingDeployment  *appsv1.Deployment
		wantAuditArgs       []string
		wantNoISVCPatch     bool
		wantConfiguredProxy bool
		wantPreservedProxy  bool
		wantWarning         bool
		wantProxyArgs       []string
	}{
		{
			name:               "metadata configures a new predictor proxy",
			profile:            constants.AuditLoggingProfileMetadata,
			manageAuditLogging: true,
			annotations: map[string]string{
				constants.ODHKserveRawAuth: "true",
			},
			wantAuditArgs: []string{
				"--audit-log-profile=metadata",
				"--audit-resource-name=test-isvc",
				"--audit-resource-namespace=test-ns",
				"--audit-resource-type=InferenceService",
				"--audit-ai-provider=KServe",
			},
		},
		{
			name:               "none configures a new predictor without audit arguments",
			profile:            constants.AuditLoggingProfileNone,
			manageAuditLogging: true,
			annotations: map[string]string{
				constants.ODHKserveRawAuth: "true",
			},
		},
		{
			name:               "none removes an existing audit configuration",
			profile:            constants.AuditLoggingProfileNone,
			manageAuditLogging: true,
			annotations: map[string]string{
				constants.ODHKserveRawAuth: "true",
			},
			existingDeployment: deploymentWithAuthProxyImage("outdated-proxy",
				"--v=8",
				"--audit-log-enabled",
				"--audit-isvc-name=test-isvc",
				"--audit-isvc-namespace=test-ns",
				"--audit-resource-name=stale",
				"--audit-resource-namespace=stale",
				"--audit-resource-type=LegacyType",
				"--audit-ai-provider=LegacyProvider",
				"--audit-future-option=stale",
			),
			wantConfiguredProxy: true,
		},
		{
			name:               "none removes unknown audit arguments",
			profile:            constants.AuditLoggingProfileNone,
			manageAuditLogging: true,
			annotations: map[string]string{
				constants.ODHKserveRawAuth: "true",
			},
			existingDeployment:  deploymentWithAuthProxyImage("outdated-proxy", "--audit-future-option=unchanged"),
			wantConfiguredProxy: true,
		},
		{
			name:               "metadata enables an existing unaudited predictor",
			profile:            constants.AuditLoggingProfileMetadata,
			manageAuditLogging: true,
			annotations: map[string]string{
				constants.ODHKserveRawAuth: "true",
			},
			existingDeployment: deploymentWithAuthProxy(),
			wantAuditArgs: []string{
				"--audit-log-profile=metadata",
				"--audit-resource-name=test-isvc",
				"--audit-resource-namespace=test-ns",
				"--audit-resource-type=InferenceService",
				"--audit-ai-provider=KServe",
			},
		},
		{
			name:               "metadata replaces stale duplicate and unknown audit arguments",
			profile:            constants.AuditLoggingProfileMetadata,
			manageAuditLogging: true,
			annotations: map[string]string{
				constants.ODHKserveRawAuth: "true",
			},
			existingDeployment: deploymentWithAuthProxyImage("outdated-proxy",
				"--v=7",
				"--audit-log-enabled",
				"--audit-log-profile=none",
				"--audit-log-profile=metadata",
				"--audit-isvc-name=spoofed",
				"--audit-isvc-namespace=stale",
				"--audit-resource-name=stale",
				"--audit-resource-namespace=stale",
				"--audit-resource-type=LegacyType",
				"--audit-ai-provider=LegacyProvider",
				"--audit-future-option=stale",
			),
			wantAuditArgs: []string{
				"--audit-log-profile=metadata",
				"--audit-resource-name=test-isvc",
				"--audit-resource-namespace=test-ns",
				"--audit-resource-type=InferenceService",
				"--audit-ai-provider=KServe",
			},
			wantConfiguredProxy: true,
		},
		{
			name:               "metadata canonicalizes reordered audit arguments on a differently imaged proxy",
			profile:            constants.AuditLoggingProfileMetadata,
			manageAuditLogging: true,
			annotations: map[string]string{
				constants.ODHKserveRawAuth: "true",
			},
			existingDeployment: deploymentWithAuthProxyImage("outdated-proxy",
				"--audit-ai-provider=KServe",
				"--audit-resource-type=InferenceService",
				"--audit-resource-namespace=test-ns",
				"--audit-resource-name=test-isvc",
				"--audit-log-profile=metadata",
			),
			wantAuditArgs: []string{
				"--audit-log-profile=metadata",
				"--audit-resource-name=test-isvc",
				"--audit-resource-namespace=test-ns",
				"--audit-resource-type=InferenceService",
				"--audit-ai-provider=KServe",
			},
			wantConfiguredProxy: true,
		},
		{
			name:               "explicit opt-in migrates a legacy oauth proxy",
			profile:            constants.AuditLoggingProfileMetadata,
			manageAuditLogging: true,
			annotations: map[string]string{
				constants.ODHKserveRawAuth:           "true",
				constants.ODHAuthProxyTypeAnnotation: constants.KubeRbacProxyType,
			},
			existingDeployment: deploymentWithNamedAuthProxy(constants.OauthProxyContainerName, "legacy-oauth"),
			wantAuditArgs: []string{
				"--audit-log-profile=metadata",
				"--audit-resource-name=test-isvc",
				"--audit-resource-namespace=test-ns",
				"--audit-resource-type=InferenceService",
				"--audit-ai-provider=KServe",
			},
			wantConfiguredProxy: true,
		},
		{
			name:    "annotationless predictor does not infer audit state",
			profile: constants.AuditLoggingProfileNone,
			annotations: map[string]string{
				constants.DeploymentMode:   string(constants.Standard),
				constants.ODHKserveRawAuth: "true",
			},
			existingDeployment: deploymentWithAuthProxy(
				"--legacy-unrelated-arg",
				"--audit-log-enabled",
				"--audit-isvc-name=test-isvc",
				"--audit-isvc-namespace=test-ns",
				"--audit-use-forwarded-for",
				"--audit-future-option=unchanged",
			),
			wantAuditArgs: []string{
				"--audit-log-enabled",
				"--audit-isvc-name=test-isvc",
				"--audit-isvc-namespace=test-ns",
				"--audit-use-forwarded-for",
				"--audit-future-option=unchanged",
			},
			wantNoISVCPatch:    true,
			wantPreservedProxy: true,
			wantProxyArgs: []string{
				"--legacy-unrelated-arg",
				"--audit-log-enabled",
				"--audit-isvc-name=test-isvc",
				"--audit-isvc-namespace=test-ns",
				"--audit-use-forwarded-for",
				"--audit-future-option=unchanged",
			},
		},
		{
			name:    "annotationless predictor remains unaudited without patching parent",
			profile: constants.AuditLoggingProfileNone,
			annotations: map[string]string{
				constants.DeploymentMode:   string(constants.Standard),
				constants.ODHKserveRawAuth: "true",
			},
			existingDeployment: deploymentWithAuthProxy("--legacy-unrelated-arg"),
			wantNoISVCPatch:    true,
			wantPreservedProxy: true,
			wantProxyArgs:      []string{"--legacy-unrelated-arg"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &mockClientForAuthProxyDetection{
				existingDeployment: tt.existingDeployment,
				deploymentNotFound: tt.existingDeployment == nil,
			}
			clientset := fake.NewSimpleClientset(&corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: constants.InferenceServiceConfigMapName, Namespace: constants.KServeNamespace},
				Data:       map[string]string{oauthProxyISVCConfigKey: oauthProxyConfig},
			})
			meta := metav1.ObjectMeta{
				Name:        "test-predictor",
				Namespace:   "test-ns",
				Annotations: tt.annotations,
				Labels: map[string]string{
					constants.InferenceServicePodLabelKey: "test-isvc",
				},
			}
			deployments, authProxyPreserved, err := createRawDeploymentODH(
				t.Context(), client, clientset, constants.InferenceServiceResource,
				meta, metav1.ObjectMeta{}, &v1beta1.ComponentExtensionSpec{},
				&corev1.PodSpec{Containers: []corev1.Container{{Name: constants.InferenceServiceContainerName}}}, nil, nil,
				tt.profile,
				tt.manageAuditLogging,
			)
			require.NoError(t, err)
			require.Len(t, deployments, 1)

			var proxy *corev1.Container
			for i := range deployments[0].Spec.Template.Spec.Containers {
				if deployments[0].Spec.Template.Spec.Containers[i].Name == constants.KubeRbacContainerName {
					proxy = &deployments[0].Spec.Template.Spec.Containers[i]
					break
				}
			}
			require.NotNil(t, proxy)
			actualAuditArgs := managedAuditArgs(proxy.Args)
			if len(tt.wantAuditArgs) == 0 {
				assert.Empty(t, actualAuditArgs)
			} else {
				assert.Equal(t, tt.wantAuditArgs, actualAuditArgs)
			}
			assert.Equal(t, tt.wantWarning, authProxyPreserved)
			if tt.wantProxyArgs != nil {
				assert.Equal(t, tt.wantProxyArgs, proxy.Args)
			} else {
				assert.NotContains(t, proxy.Args, "--legacy-unrelated-arg")
				assert.NotContains(t, proxy.Args, "--audit-future-option=unchanged")
				assert.NotContains(t, proxy.Args, "--audit-use-forwarded-for")
			}
			if tt.wantConfiguredProxy {
				assert.Equal(t, constants.OauthProxyImage, proxy.Image)
			}
			assert.Equal(t, 1, client.inferenceServiceGets,
				"proxy generation or configured-image preservation should refresh SAR ownership")
			if tt.wantNoISVCPatch {
				assert.Nil(t, client.patchedInferenceService)
			}
		})
	}
}

func TestCreateRawDeploymentODHPreservesAnnotationlessConfiguredProxy(t *testing.T) {
	clientset := fake.NewSimpleClientset(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: constants.InferenceServiceConfigMapName, Namespace: constants.KServeNamespace},
		Data:       map[string]string{oauthProxyISVCConfigKey: oauthProxyConfig},
	})
	meta := metav1.ObjectMeta{
		Name:      "test-predictor",
		Namespace: "test-ns",
		Annotations: map[string]string{
			constants.DeploymentMode:   string(constants.Standard),
			constants.ODHKserveRawAuth: "true",
		},
		Labels: map[string]string{
			constants.InferenceServicePodLabelKey: "test-isvc",
		},
	}
	podSpec := &corev1.PodSpec{Containers: []corev1.Container{{Name: constants.InferenceServiceContainerName}}}

	initialClient := &mockClientForAuthProxyDetection{deploymentNotFound: true}
	initial, _, err := createRawDeploymentODH(
		t.Context(), initialClient, clientset, constants.InferenceServiceResource,
		meta, metav1.ObjectMeta{}, &v1beta1.ComponentExtensionSpec{}, podSpec, nil, nil,
		constants.AuditLoggingProfileNone, false,
	)
	require.NoError(t, err)
	require.Len(t, initial, 1)

	existing := initial[0].DeepCopy()
	var existingProxy *corev1.Container
	for i := range existing.Spec.Template.Spec.Containers {
		if existing.Spec.Template.Spec.Containers[i].Name == constants.KubeRbacContainerName {
			existingProxy = &existing.Spec.Template.Spec.Containers[i]
			break
		}
	}
	require.NotNil(t, existingProxy)
	assert.Equal(t, constants.OauthProxyImage, existingProxy.Image)
	existingProxy.Args = append(existingProxy.Args,
		"--audit-log-enabled",
		"--audit-isvc-name=test-isvc",
		"--audit-isvc-namespace=test-ns",
		"--audit-future-option=unchanged",
	)
	wantTemplate := existing.Spec.Template.DeepCopy()
	sarConfigMapName := "test-isvc-" + constants.OauthProxySARCMName
	require.NoError(t, clientset.CoreV1().ConfigMaps("test-ns").Delete(
		t.Context(), sarConfigMapName, metav1.DeleteOptions{},
	))

	reconcileClient := &mockClientForAuthProxyDetection{existingDeployment: existing}
	reconciled, authProxyPreserved, err := createRawDeploymentODH(
		t.Context(), reconcileClient, clientset, constants.InferenceServiceResource,
		meta, metav1.ObjectMeta{}, &v1beta1.ComponentExtensionSpec{}, podSpec, nil, nil,
		constants.AuditLoggingProfileNone, false,
	)
	require.NoError(t, err)
	require.Len(t, reconciled, 1)
	assert.False(t, authProxyPreserved)
	assert.Equal(t, *wantTemplate, reconciled[0].Spec.Template)
	assert.Equal(t, 1, reconcileClient.inferenceServiceGets)
	assert.Nil(t, reconcileClient.patchedInferenceService)
	_, err = clientset.CoreV1().ConfigMaps("test-ns").Get(t.Context(), sarConfigMapName, metav1.GetOptions{})
	require.NoError(t, err)
}

func TestCreateRawDeploymentODHDoesNotImplicitlyMigrateOAuthProxy(t *testing.T) {
	existing := deploymentWithNamedAuthProxy(
		constants.OauthProxyContainerName,
		"legacy-oauth",
		"--legacy-oauth-argument",
	)
	client := &mockClientForAuthProxyDetection{existingDeployment: existing}
	clientset := fake.NewSimpleClientset(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: constants.InferenceServiceConfigMapName, Namespace: constants.KServeNamespace},
		Data:       map[string]string{oauthProxyISVCConfigKey: oauthProxyConfig},
	})
	meta := metav1.ObjectMeta{
		Name:        "test-predictor",
		Namespace:   "test-ns",
		Annotations: map[string]string{constants.ODHKserveRawAuth: "true"},
		Labels:      map[string]string{constants.InferenceServicePodLabelKey: "test-isvc"},
	}

	deployments, authProxyPreserved, err := createRawDeploymentODH(
		t.Context(), client, clientset, constants.InferenceServiceResource,
		meta, metav1.ObjectMeta{}, &v1beta1.ComponentExtensionSpec{},
		&corev1.PodSpec{Containers: []corev1.Container{{Name: constants.InferenceServiceContainerName}}}, nil, nil,
		constants.AuditLoggingProfileMetadata,
		true,
	)
	require.NoError(t, err)
	require.Len(t, deployments, 1)
	assert.True(t, authProxyPreserved)

	var oauthProxy *corev1.Container
	for i := range deployments[0].Spec.Template.Spec.Containers {
		container := &deployments[0].Spec.Template.Spec.Containers[i]
		assert.NotEqual(t, constants.KubeRbacContainerName, container.Name)
		if container.Name == constants.OauthProxyContainerName {
			oauthProxy = container
		}
	}
	require.NotNil(t, oauthProxy)
	assert.Equal(t, []string{"--legacy-oauth-argument"}, oauthProxy.Args)
}

func TestManagedAuditArgumentBoundaries(t *testing.T) {
	tests := []struct {
		name string
		arg  string
		want bool
	}{
		{name: "old option", arg: "--audit-isvc-name=test-isvc", want: true},
		{name: "current option", arg: "--audit-resource-name=test-isvc", want: true},
		{name: "unknown option", arg: "--audit-future-option=value", want: true},
		{name: "audit text in value", arg: "--upstream=https://example.test/--audit-target", want: false},
		{name: "similar option name", arg: "--auditing-enabled=true", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isManagedAuditArg(tt.arg))
		})
	}

	args := []string{
		"--audit-log-enabled",
		"--audit-log-enabled",
		"--audit-log-profile=metadata",
		"--audit-log-profile=none",
		"--audit-isvc-name=old",
		"--audit-isvc-name=duplicate",
		"--audit-resource-name=current",
		"--audit-resource-name=duplicate",
		"--audit-future-option=unknown",
		"--audit-future-option=duplicate",
		"--upstream=https://example.test/--audit-target",
		"--v=4",
	}
	assert.Equal(t, []string{
		"--upstream=https://example.test/--audit-target",
		"--v=4",
	}, removeManagedAuditArgs(args))
}

func TestCustomizeAuthProxyArgsAuditSettings(t *testing.T) {
	generated := []string{
		"--audit-log-enabled",
		"--audit-log-enabled",
		"--audit-log-profile=none",
		"--audit-log-profile=metadata",
		"--audit-isvc-name=old",
		"--audit-isvc-namespace=old",
		"--audit-resource-name=current",
		"--audit-resource-namespace=current",
		"--audit-resource-type=LegacyType",
		"--audit-ai-provider=LegacyProvider",
		"--audit-future-option=unknown",
		"--audit-future-option=duplicate",
		"--upstream=https://example.test/--audit-target",
	}
	tests := []struct {
		name        string
		profile     constants.AuditLoggingProfile
		manage      bool
		annotations map[string]string
		want        []string
	}{
		{
			name:    "metadata collapses all managed arguments",
			profile: constants.AuditLoggingProfileMetadata,
			manage:  true,
			want: []string{
				"--upstream=https://example.test/--audit-target",
				"--audit-log-profile=metadata",
				"--audit-resource-name=test-isvc",
				"--audit-resource-namespace=test-ns",
				"--audit-resource-type=InferenceService",
				"--audit-ai-provider=KServe",
			},
		},
		{
			name:    "none removes all managed arguments",
			profile: constants.AuditLoggingProfileNone,
			manage:  true,
			want:    []string{"--upstream=https://example.test/--audit-target"},
		},
		{
			name:        "preserve setting leaves arguments untouched",
			profile:     constants.AuditLoggingProfileNone,
			annotations: map[string]string{},
			want:        generated,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			meta := metav1.ObjectMeta{
				Namespace:   "test-ns",
				Annotations: tt.annotations,
				Labels: map[string]string{
					constants.InferenceServicePodLabelKey: "test-isvc",
				},
			}
			assert.Equal(t, tt.want, customizeAuthProxyArgs(tt.profile, tt.manage, meta, generated, "fallback-isvc"))
		})
	}
}

func deploymentWithAuthProxy(args ...string) *appsv1.Deployment {
	return deploymentWithAuthProxyImage(constants.OauthProxyImage, args...)
}

func deploymentWithAuthProxyImage(image string, args ...string) *appsv1.Deployment {
	return deploymentWithNamedAuthProxy(constants.KubeRbacContainerName, image, args...)
}

func deploymentWithNamedAuthProxy(name, image string, args ...string) *appsv1.Deployment {
	return &appsv1.Deployment{Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
		Containers: []corev1.Container{
			{Name: constants.InferenceServiceContainerName},
			{Name: name, Image: image, Args: args},
		},
	}}}}
}
