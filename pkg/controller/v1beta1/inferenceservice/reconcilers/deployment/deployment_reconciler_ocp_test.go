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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kserve/kserve/pkg/apis/serving/v1beta1"
	"github.com/kserve/kserve/pkg/constants"
)

const oauthProxyISVCConfigKey = "oauthProxy"

func TestOauthProxyUpstreamTimeout(t *testing.T) {
	type args struct {
		client           kclient.Client
		clientset        kubernetes.Interface
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
				client: &mockClientForCheckDeploymentExist{},
				clientset: fake.NewSimpleClientset(&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{Name: constants.InferenceServiceConfigMapName, Namespace: constants.KServeNamespace},
					Data: map[string]string{
						oauthProxyISVCConfigKey: `{"image": "quay.io/opendatahub/odh-kube-auth-proxy@sha256:dcb09fbabd8811f0956ef612a0c9ddd5236804b9bd6548a0647d2b531c9d01b3", "memoryRequest": "64Mi", "memoryLimit": "128Mi", "cpuRequest": "100m", "cpuLimit": "200m"}`,
					},
				}),
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
				client: &mockClientForCheckDeploymentExist{},
				clientset: fake.NewSimpleClientset(&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{Name: constants.InferenceServiceConfigMapName, Namespace: constants.KServeNamespace},
					Data: map[string]string{
						oauthProxyISVCConfigKey: `{"image": "quay.io/opendatahub/odh-kube-auth-proxy@sha256:dcb09fbabd8811f0956ef612a0c9ddd5236804b9bd6548a0647d2b531c9d01b3", "memoryRequest": "64Mi", "memoryLimit": "128Mi", "cpuRequest": "100m", "cpuLimit": "200m", "upstreamTimeoutSeconds": "20"}`,
					},
				}),
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
				client: &mockClientForCheckDeploymentExist{},
				clientset: fake.NewSimpleClientset(&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{Name: constants.InferenceServiceConfigMapName, Namespace: constants.KServeNamespace},
					Data: map[string]string{
						oauthProxyISVCConfigKey: `{"image": "quay.io/opendatahub/odh-kube-auth-proxy@sha256:dcb09fbabd8811f0956ef612a0c9ddd5236804b9bd6548a0647d2b531c9d01b3", "memoryRequest": "64Mi", "memoryLimit": "128Mi", "cpuRequest": "100m", "cpuLimit": "200m", "upstreamTimeoutSeconds": "20"}`,
					},
				}),
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
					TimeoutSeconds: func(i int64) *int64 { return &i }(40),
				},
				podSpec:         &corev1.PodSpec{},
				workerPodSpec:   nil,
				expectedTimeout: "40s",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deployments, err := buildDeployments(
				t.Context(),
				tt.args.client,
				tt.args.clientset,
				constants.InferenceServiceResource,
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
