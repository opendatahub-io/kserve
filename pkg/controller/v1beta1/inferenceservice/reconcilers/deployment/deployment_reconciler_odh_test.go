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

package deployment

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/kserve/kserve/pkg/apis/serving/v1beta1"
	"github.com/kserve/kserve/pkg/constants"
)

const oauthProxyISVCConfigKey = "oauthProxy"

func newFakeClient(objs ...kclient.Object) kclient.Client {
	s := runtime.NewScheme()
	_ = v1beta1.AddToScheme(s)
	_ = corev1.AddToScheme(s)
	_ = appsv1.AddToScheme(s)
	return fakeclient.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build()
}

// mockClientForAuthProxyDetection is a mock for getExistingAuthProxyType tests
// that controls deployment lookup behavior independent of other object types.
type mockClientForAuthProxyDetection struct {
	kclient.Client
	existingDeployment *appsv1.Deployment
	deploymentNotFound bool
}

func (m *mockClientForAuthProxyDetection) Get(_ context.Context, key kclient.ObjectKey, obj kclient.Object, _ ...kclient.GetOption) error {
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

func (m *mockClientForAuthProxyDetection) Update(_ context.Context, _ kclient.Object, _ ...kclient.UpdateOption) error {
	return nil
}

func (m *mockClientForAuthProxyDetection) Create(_ context.Context, _ kclient.Object, _ ...kclient.CreateOption) error {
	return nil
}

func TestOauthProxyUpstreamTimeout(t *testing.T) {
	type args struct {
		client           kclient.Client
		objectMeta       metav1.ObjectMeta
		workerObjectMeta metav1.ObjectMeta
		componentExt     *v1beta1.ComponentExtensionSpec
		podSpec          *corev1.PodSpec
		workerPodSpec    *corev1.PodSpec
		expectedTimeout  string
	}

	tests := []struct {
		name string
		args args
	}{
		{
			name: "default deployment",
			args: args{
				client: newFakeClient(
					&corev1.ConfigMap{
						ObjectMeta: metav1.ObjectMeta{Name: constants.InferenceServiceConfigMapName, Namespace: constants.KServeNamespace},
						Data: map[string]string{
							oauthProxyISVCConfigKey: `{"image": "quay.io/opendatahub/odh-kube-auth-proxy@sha256:dcb09fbabd8811f0956ef612a0c9ddd5236804b9bd6548a0647d2b531c9d01b3", "memoryRequest": "64Mi", "memoryLimit": "128Mi", "cpuRequest": "100m", "cpuLimit": "200m"}`,
						},
					},
					&v1beta1.InferenceService{ObjectMeta: metav1.ObjectMeta{Name: "default-predictor", Namespace: "default-predictor-namespace"}},
				),
				objectMeta: metav1.ObjectMeta{
					Name:      "default-predictor",
					Namespace: "default-predictor-namespace",
					Annotations: map[string]string{
						constants.ODHKserveRawAuth: "true",
					},
					Labels: map[string]string{
						constants.DeploymentMode:  string(constants.Standard),
						constants.AutoscalerClass: string(constants.DefaultAutoscalerClass),
					},
				},
				workerObjectMeta: metav1.ObjectMeta{},
				componentExt:     &v1beta1.ComponentExtensionSpec{},
				podSpec:          &corev1.PodSpec{},
				workerPodSpec:    nil,
				expectedTimeout:  "",
			},
		},
		{
			name: "deployment with oauth proxy upstream timeout defined in oauth proxy config",
			args: args{
				client: newFakeClient(
					&corev1.ConfigMap{
						ObjectMeta: metav1.ObjectMeta{Name: constants.InferenceServiceConfigMapName, Namespace: constants.KServeNamespace},
						Data: map[string]string{
							oauthProxyISVCConfigKey: `{"image": "quay.io/opendatahub/odh-kube-auth-proxy@sha256:dcb09fbabd8811f0956ef612a0c9ddd5236804b9bd6548a0647d2b531c9d01b3", "memoryRequest": "64Mi", "memoryLimit": "128Mi", "cpuRequest": "100m", "cpuLimit": "200m", "upstreamTimeoutSeconds": "20"}`,
						},
					},
					&v1beta1.InferenceService{ObjectMeta: metav1.ObjectMeta{Name: "config-timeout-predictor", Namespace: "config-timeout-predictor-namespace"}},
				),
				objectMeta: metav1.ObjectMeta{
					Name:      "config-timeout-predictor",
					Namespace: "config-timeout-predictor-namespace",
					Annotations: map[string]string{
						constants.ODHKserveRawAuth: "true",
					},
					Labels: map[string]string{
						constants.DeploymentMode:  string(constants.Standard),
						constants.AutoscalerClass: string(constants.DefaultAutoscalerClass),
					},
				},
				workerObjectMeta: metav1.ObjectMeta{},
				componentExt:     &v1beta1.ComponentExtensionSpec{},
				podSpec:          &corev1.PodSpec{},
				workerPodSpec:    nil,
				expectedTimeout:  "20s",
			},
		},
		{
			name: "deployment with oauth proxy upstream timeout defined in component spec",
			args: args{
				client: newFakeClient(
					&corev1.ConfigMap{
						ObjectMeta: metav1.ObjectMeta{Name: constants.InferenceServiceConfigMapName, Namespace: constants.KServeNamespace},
						Data: map[string]string{
							oauthProxyISVCConfigKey: `{"image": "quay.io/opendatahub/odh-kube-auth-proxy@sha256:dcb09fbabd8811f0956ef612a0c9ddd5236804b9bd6548a0647d2b531c9d01b3", "memoryRequest": "64Mi", "memoryLimit": "128Mi", "cpuRequest": "100m", "cpuLimit": "200m", "upstreamTimeoutSeconds": "20"}`,
						},
					},
					&v1beta1.InferenceService{ObjectMeta: metav1.ObjectMeta{Name: "config-timeout-predictor", Namespace: "config-timeout-predictor-namespace"}},
				),
				objectMeta: metav1.ObjectMeta{
					Name:      "config-timeout-predictor",
					Namespace: "config-timeout-predictor-namespace",
					Annotations: map[string]string{
						constants.ODHKserveRawAuth: "true",
					},
					Labels: map[string]string{
						constants.DeploymentMode:  string(constants.Standard),
						constants.AutoscalerClass: string(constants.DefaultAutoscalerClass),
					},
				},
				workerObjectMeta: metav1.ObjectMeta{},
				componentExt: &v1beta1.ComponentExtensionSpec{
					TimeoutSeconds: ptr.To[int64](40),
				},
				podSpec:         &corev1.PodSpec{},
				workerPodSpec:   nil,
				expectedTimeout: "40s",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deployments, _, err := buildDeployments(
				t.Context(),
				tt.args.client,
				tt.args.objectMeta,
				tt.args.workerObjectMeta,
				tt.args.componentExt,
				tt.args.podSpec,
				tt.args.workerPodSpec,
				nil, // deployConfig
			)
			require.NoError(t, err)
			require.NotEmpty(t, deployments)

			oauthProxyContainerFound := false
			containers := deployments[0].Spec.Template.Spec.Containers
			for _, container := range containers {
				if container.Name == "kube-rbac-proxy" {
					oauthProxyContainerFound = true
					if tt.args.expectedTimeout == "" {
						for _, arg := range container.Args {
							assert.NotContains(t, arg, "upstream-timeout")
						}
					} else {
						require.Contains(t, container.Args, "--upstream-timeout="+tt.args.expectedTimeout)
					}
				}
			}
			require.True(t, oauthProxyContainerFound)
		})
	}
}

func TestNewDeploymentReconciler(t *testing.T) {
	type fields struct {
		client       kclient.Client
		scheme       *runtime.Scheme
		objectMeta   metav1.ObjectMeta
		workerMeta   metav1.ObjectMeta
		componentExt *v1beta1.ComponentExtensionSpec
		podSpec      *corev1.PodSpec
		workerPod    *corev1.PodSpec
	}
	tests := []struct {
		name        string
		fields      fields
		wantErr     bool
		wantWorkers int
	}{
		{
			name: "default deployment",
			fields: fields{
				client: newFakeClient(
					&corev1.ConfigMap{
						ObjectMeta: metav1.ObjectMeta{Name: constants.InferenceServiceConfigMapName, Namespace: constants.KServeNamespace},
						Data: map[string]string{
							oauthProxyISVCConfigKey: `{"image": "quay.io/test/proxy:latest", "memoryRequest": "64Mi", "memoryLimit": "128Mi", "cpuRequest": "100m", "cpuLimit": "200m"}`,
						},
					},
					&v1beta1.InferenceService{ObjectMeta: metav1.ObjectMeta{Name: "test-predictor", Namespace: "test-ns"}},
				),
				scheme: nil,
				objectMeta: metav1.ObjectMeta{
					Name:      "test-predictor",
					Namespace: "test-ns",
					Labels: map[string]string{
						constants.DeploymentMode:  string(constants.Standard),
						constants.AutoscalerClass: string(constants.DefaultAutoscalerClass),
					},
					Annotations: map[string]string{},
				},
				workerMeta:   metav1.ObjectMeta{},
				componentExt: &v1beta1.ComponentExtensionSpec{},
				podSpec: &corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  constants.InferenceServiceContainerName,
							Image: "test-image",
						},
					},
				},
				workerPod: nil,
			},
			wantErr:     false,
			wantWorkers: 1,
		},
		{
			name: "multi-node deployment",
			fields: fields{
				client: newFakeClient(
					&corev1.ConfigMap{
						ObjectMeta: metav1.ObjectMeta{Name: constants.InferenceServiceConfigMapName, Namespace: constants.KServeNamespace},
						Data: map[string]string{
							oauthProxyISVCConfigKey: `{"image": "quay.io/test/proxy:latest", "memoryRequest": "64Mi", "memoryLimit": "128Mi", "cpuRequest": "100m", "cpuLimit": "200m"}`,
						},
					},
					&v1beta1.InferenceService{ObjectMeta: metav1.ObjectMeta{Name: "test-predictor", Namespace: "test-ns"}},
				),
				scheme: nil,
				objectMeta: metav1.ObjectMeta{
					Name:      "test-predictor",
					Namespace: "test-ns",
					Labels: map[string]string{
						constants.DeploymentMode:  string(constants.Standard),
						constants.AutoscalerClass: string(constants.AutoscalerClassNone),
					},
					Annotations: map[string]string{},
				},
				workerMeta: metav1.ObjectMeta{
					Name:      "worker-predictor",
					Namespace: "test-ns",
					Labels: map[string]string{
						constants.DeploymentMode:  string(constants.Standard),
						constants.AutoscalerClass: string(constants.AutoscalerClassNone),
					},
					Annotations: map[string]string{},
				},
				componentExt: &v1beta1.ComponentExtensionSpec{},
				podSpec: &corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  constants.InferenceServiceContainerName,
							Image: "test-image",
							Env: []corev1.EnvVar{
								{Name: constants.RayNodeCountEnvName, Value: "2"},
								{Name: constants.RequestGPUCountEnvName, Value: "1"},
							},
						},
					},
				},
				workerPod: &corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  constants.WorkerContainerName,
							Image: "worker-image",
							Env: []corev1.EnvVar{
								{Name: constants.RequestGPUCountEnvName, Value: "1"},
							},
						},
					},
				},
			},
			wantErr:     false,
			wantWorkers: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NewDeploymentReconciler(
				t.Context(),
				tt.fields.client,
				tt.fields.scheme,
				tt.fields.objectMeta,
				tt.fields.workerMeta,
				tt.fields.componentExt,
				tt.fields.podSpec,
				tt.fields.workerPod,
				nil, // deployConfig
			)

			if (err != nil) != tt.wantErr {
				t.Errorf("NewDeploymentReconciler() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if err == nil && got != nil {
				if len(got.DeploymentList) != tt.wantWorkers {
					t.Errorf("DeploymentList length = %v, want %v", len(got.DeploymentList), tt.wantWorkers)
				}
				if got.componentExt != tt.fields.componentExt {
					t.Errorf("componentExt pointer mismatch")
				}
			}
		})
	}
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
		annotations               map[string]string
		expectKubeRbacProxy       bool
		expectOauthProxyPreserved bool
		expectedProxyImage        string
	}{
		{
			name: "new ISVC with auth enabled gets kube-rbac-proxy",
			// No existing deployment pre-populated in fake client
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
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-predictor",
					Namespace: "test-ns",
				},
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
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-predictor",
					Namespace: "test-ns",
				},
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
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-predictor",
					Namespace: "test-ns",
				},
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
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-predictor",
					Namespace: "test-ns",
				},
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
			fakeObjs := []kclient.Object{
				&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      constants.InferenceServiceConfigMapName,
						Namespace: constants.KServeNamespace,
					},
					Data: map[string]string{
						oauthProxyISVCConfigKey: oauthProxyConfig,
					},
				},
				&v1beta1.InferenceService{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-isvc",
						Namespace: "test-ns",
						UID:       "test-uid-12345",
					},
				},
			}
			if tt.existingDeployment != nil {
				fakeObjs = append(fakeObjs, tt.existingDeployment)
			}
			client := newFakeClient(fakeObjs...)

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

			deploymentList, _, err := buildDeployments(
				t.Context(),
				client,
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
		annotations        map[string]string
		expectCondition    bool
		expectedReason     string
	}{
		{
			name: "new ISVC does not set condition",
			// No existing deployment
			annotations: map[string]string{
				constants.ODHKserveRawAuth: "true",
			},
			expectCondition: false,
		},
		{
			name: "existing ISVC with oauth-proxy sets AuthProxyPreserved condition",
			existingDeployment: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-predictor",
					Namespace: "test-ns",
				},
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
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-predictor",
					Namespace: "test-ns",
				},
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
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-predictor",
					Namespace: "test-ns",
				},
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
			fakeObjs := []kclient.Object{
				&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      constants.InferenceServiceConfigMapName,
						Namespace: constants.KServeNamespace,
					},
					Data: map[string]string{
						oauthProxyISVCConfigKey: oauthProxyConfig,
					},
				},
				&v1beta1.InferenceService{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-isvc",
						Namespace: "test-ns",
						UID:       "test-uid-12345",
					},
				},
			}
			if tt.existingDeployment != nil {
				fakeObjs = append(fakeObjs, tt.existingDeployment)
			}
			client := newFakeClient(fakeObjs...)

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
				nil,
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

func TestNewRawDeploymentWithAuthDisabled_IncludesOAuthProxy(t *testing.T) {
	oauthProxyConfig := fmt.Sprintf(`{"image": "%s", "memoryRequest": "%s", "memoryLimit": "%s", "cpuRequest": "%s", "cpuLimit": "%s"}`,
		constants.OauthProxyImage,
		constants.OauthProxyResourceMemoryRequest,
		constants.OauthProxyResourceMemoryLimit,
		constants.OauthProxyResourceCPURequest,
		constants.OauthProxyResourceCPULimit,
	)

	client := newFakeClient(
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: constants.InferenceServiceConfigMapName, Namespace: constants.KServeNamespace},
			Data: map[string]string{
				oauthProxyISVCConfigKey: oauthProxyConfig,
			},
		},
		&v1beta1.InferenceService{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "default-predictor",
				Namespace: "default-predictor-namespace",
				UID:       "test-uid-12345",
			},
		},
	)

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

	deployments, _, err := buildDeployments(
		t.Context(),
		client,
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
		if vol.Name == fmt.Sprintf("%s-%s", "default-predictor", constants.OauthProxySARCMName) {
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

	client := newFakeClient(
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: constants.InferenceServiceConfigMapName, Namespace: constants.KServeNamespace},
			Data: map[string]string{
				oauthProxyISVCConfigKey: oauthProxyConfig,
			},
		},
		&v1beta1.InferenceService{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "auth-enabled-predictor",
				Namespace: "default-predictor-namespace",
				UID:       "test-uid-12345",
			},
		},
	)

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

	deployments, _, err := buildDeployments(
		t.Context(),
		client,
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

	client := newFakeClient(
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: constants.InferenceServiceConfigMapName, Namespace: constants.KServeNamespace},
			Data: map[string]string{
				oauthProxyISVCConfigKey: oauthProxyConfig,
			},
		},
		&v1beta1.InferenceService{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "existing-predictor",
				Namespace: "default-predictor-namespace",
				UID:       "test-uid-12345",
			},
		},
		existingDeployment,
	)

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

	deployments, _, err := buildDeployments(
		t.Context(),
		client,
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

	client := newFakeClient(
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: constants.InferenceServiceConfigMapName, Namespace: constants.KServeNamespace},
			Data: map[string]string{
				oauthProxyISVCConfigKey: oauthProxyConfig,
			},
		},
		&v1beta1.InferenceService{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "existing-auth-predictor",
				Namespace: "default-predictor-namespace",
				UID:       "test-uid-12345",
			},
		},
		existingDeployment,
	)

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

	deployments, _, err := buildDeployments(
		t.Context(),
		client,
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

	client := newFakeClient(
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: constants.InferenceServiceConfigMapName, Namespace: constants.KServeNamespace},
			Data: map[string]string{
				oauthProxyISVCConfigKey: oauthProxyConfig,
			},
		},
		&v1beta1.InferenceService{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "ig-predictor",
				Namespace: "default-predictor-namespace",
				UID:       "test-uid-12345",
			},
		},
	)

	objectMeta := metav1.ObjectMeta{
		Name:      "ig-predictor",
		Namespace: "default-predictor-namespace",
		Annotations: map[string]string{
			constants.ODHKserveRawAuth: "false",
		},
		Labels: map[string]string{
			constants.DeploymentMode:      string(constants.Standard),
			constants.AutoscalerClass:     string(constants.DefaultAutoscalerClass),
			constants.InferenceGraphLabel: "ig-predictor", // marks this as InferenceGraph
		},
	}

	deployments, _, err := buildDeployments(
		t.Context(),
		client,
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
