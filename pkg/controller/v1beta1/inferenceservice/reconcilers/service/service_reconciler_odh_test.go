//go:build distro

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

package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/kserve/kserve/pkg/apis/serving/v1beta1"
	"github.com/kserve/kserve/pkg/constants"
)

func TestCustomizeServiceAddsServingCertAnnotation(t *testing.T) {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-predictor",
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{{Port: 80}},
		},
	}
	meta := metav1.ObjectMeta{Name: "test-predictor"}

	customizeService(svc, meta, nil)

	assert.Equal(t, "test-predictor"+constants.ServingCertSecretSuffix,
		svc.Annotations[constants.OpenshiftServingCertAnnotation])
}

func TestCustomizeServiceInferenceGraphPort(t *testing.T) {
	svc := &corev1.Service{
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{{Port: 80}},
		},
	}
	meta := metav1.ObjectMeta{
		Name: "test-ig",
		Labels: map[string]string{
			constants.InferenceGraphLabel: "my-graph",
		},
	}

	customizeService(svc, meta, nil)

	assert.Equal(t, int32(443), svc.Spec.Ports[0].Port)
}

func TestCustomizeServiceAuthProxyPort(t *testing.T) {
	svc := &corev1.Service{
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{Name: "http", Port: constants.CommonDefaultHttpPort},
			},
		},
	}
	meta := metav1.ObjectMeta{
		Name: "test-predictor",
		Annotations: map[string]string{
			constants.ODHKserveRawAuth: "true",
		},
	}

	customizeService(svc, meta, nil)

	assert.Equal(t, int32(constants.OauthProxyPort), svc.Spec.Ports[0].Port)
	assert.Equal(t, "https", svc.Spec.Ports[0].Name)
	assert.Equal(t, intstr.IntOrString{Type: intstr.String, StrVal: "https"}, svc.Spec.Ports[0].TargetPort)

	// Transformer service with auth gets its own HTTPS port (native TLS, not auth proxy).
	transformerSvc := &corev1.Service{
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{Name: "http", Port: constants.CommonDefaultHttpPort},
			},
		},
	}
	transformerMeta := metav1.ObjectMeta{
		Name: "test-transformer",
		Labels: map[string]string{
			constants.KServiceComponentLabel: string(v1beta1.TransformerComponent),
		},
		Annotations: map[string]string{
			constants.ODHKserveRawAuth: "true",
		},
	}

	customizeService(transformerSvc, transformerMeta, nil)

	assert.Equal(t, constants.TransformerHTTPSPort, transformerSvc.Spec.Ports[0].Port,
		"transformer service with auth should use the transformer HTTPS port")
	assert.Equal(t, "https", transformerSvc.Spec.Ports[0].Name,
		"transformer service port should be named https for reencrypt route")
	assert.Equal(t, intstr.IntOrString{Type: intstr.Int, IntVal: constants.TransformerHTTPSPort},
		transformerSvc.Spec.Ports[0].TargetPort,
		"transformer service target port should default to the transformer HTTPS port")

	// Transformer with a user-supplied --http_port: Service .Port stays at the
	// OCP-expected HTTPS port, but .TargetPort follows the pod's actual listener.
	customPortSvc := &corev1.Service{
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{Name: "http", Port: constants.CommonDefaultHttpPort},
			},
		},
	}
	customPodSpec := &corev1.PodSpec{
		Containers: []corev1.Container{
			{
				Name: constants.InferenceServiceContainerName,
				Args: []string{constants.ArgumentHttpPort, "9000"},
			},
		},
	}

	customizeService(customPortSvc, transformerMeta, customPodSpec)

	assert.Equal(t, constants.TransformerHTTPSPort, customPortSvc.Spec.Ports[0].Port,
		"Service .Port stays at the OCP-expected HTTPS port")
	assert.Equal(t, intstr.IntOrString{Type: intstr.Int, IntVal: 9000},
		customPortSvc.Spec.Ports[0].TargetPort,
		"Service .TargetPort should follow the user-supplied --http_port")

	// TargetPort resolution edge cases. In every case the Service .Port must stay
	// at the OCP-expected HTTPS port; only .TargetPort tracks the pod's listener.
	targetPortCases := []struct {
		name           string
		args           []string
		wantTargetPort int32
	}{
		{
			name:           "default --http_port stays on the HTTPS port",
			args:           []string{constants.ArgumentHttpPort, constants.InferenceServiceDefaultHttpPort},
			wantTargetPort: constants.TransformerHTTPSPort,
		},
		{
			name:           "equals form is honored",
			args:           []string{constants.ArgumentHttpPort + "=9000"},
			wantTargetPort: 9000,
		},
		{
			name:           "non-numeric --http_port falls back to the HTTPS port",
			args:           []string{constants.ArgumentHttpPort, "not-a-port"},
			wantTargetPort: constants.TransformerHTTPSPort,
		},
		{
			name:           "zero --http_port falls back to the HTTPS port",
			args:           []string{constants.ArgumentHttpPort, "0"},
			wantTargetPort: constants.TransformerHTTPSPort,
		},
		{
			name:           "negative --http_port falls back to the HTTPS port",
			args:           []string{constants.ArgumentHttpPort, "-1"},
			wantTargetPort: constants.TransformerHTTPSPort,
		},
		{
			name:           "oversized --http_port falls back to the HTTPS port",
			args:           []string{constants.ArgumentHttpPort + "=65536"},
			wantTargetPort: constants.TransformerHTTPSPort,
		},
	}
	for _, tc := range targetPortCases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &corev1.Service{
				Spec: corev1.ServiceSpec{
					Ports: []corev1.ServicePort{
						{Name: "http", Port: constants.CommonDefaultHttpPort},
					},
				},
			}
			podSpec := &corev1.PodSpec{
				Containers: []corev1.Container{
					{Name: constants.InferenceServiceContainerName, Args: tc.args},
				},
			}

			customizeService(svc, transformerMeta, podSpec)

			assert.Equal(t, constants.TransformerHTTPSPort, svc.Spec.Ports[0].Port,
				"Service .Port must stay at the OCP-expected HTTPS port")
			assert.Equal(t, intstr.IntOrString{Type: intstr.Int, IntVal: tc.wantTargetPort},
				svc.Spec.Ports[0].TargetPort)
		})
	}

	// A podSpec without a kserve-container cannot resolve a serving port, so the
	// TargetPort falls back to the transformer HTTPS port.
	t.Run("missing kserve-container falls back to the HTTPS port", func(t *testing.T) {
		svc := &corev1.Service{
			Spec: corev1.ServiceSpec{
				Ports: []corev1.ServicePort{
					{Name: "http", Port: constants.CommonDefaultHttpPort},
				},
			},
		}
		podSpec := &corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "some-sidecar", Args: []string{constants.ArgumentHttpPort, "9000"}},
			},
		}

		customizeService(svc, transformerMeta, podSpec)

		assert.Equal(t, constants.TransformerHTTPSPort, svc.Spec.Ports[0].Port)
		assert.Equal(t, intstr.IntOrString{Type: intstr.Int, IntVal: constants.TransformerHTTPSPort},
			svc.Spec.Ports[0].TargetPort)
	})
}

func TestCustomizeServiceTransformerWithoutAuthKeepsHTTP(t *testing.T) {
	svc := &corev1.Service{
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{Name: "http", Port: constants.CommonDefaultHttpPort},
			},
		},
	}
	meta := metav1.ObjectMeta{
		Name: "test-transformer-no-auth",
		Labels: map[string]string{
			constants.KServiceComponentLabel: string(v1beta1.TransformerComponent),
		},
		// No ODHKserveRawAuth annotation
	}

	customizeService(svc, meta, nil)

	assert.Equal(t, int32(constants.CommonDefaultHttpPort), svc.Spec.Ports[0].Port,
		"transformer without auth should keep the default HTTP port")
	assert.Equal(t, "http", svc.Spec.Ports[0].Name,
		"transformer without auth should keep 'http' port name")
}

func TestCustomizeServiceNoAuthProxyWithoutAnnotation(t *testing.T) {
	svc := &corev1.Service{
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{Name: "http", Port: constants.CommonDefaultHttpPort},
			},
		},
	}
	meta := metav1.ObjectMeta{Name: "test-predictor"}

	customizeService(svc, meta, nil)

	assert.Equal(t, int32(constants.CommonDefaultHttpPort), svc.Spec.Ports[0].Port)
}

func TestCustomizeServiceInferenceGraphIgnoresAuthProxy(t *testing.T) {
	svc := &corev1.Service{
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{{Port: 80}},
		},
	}
	meta := metav1.ObjectMeta{
		Name: "test-ig",
		Labels: map[string]string{
			constants.InferenceGraphLabel: "my-graph",
		},
		Annotations: map[string]string{
			constants.ODHKserveRawAuth: "true",
		},
	}

	customizeService(svc, meta, nil)

	// InferenceGraph takes precedence - port should be 443, not the auth proxy port.
	assert.Equal(t, int32(443), svc.Spec.Ports[0].Port)
	assert.NotEqual(t, "https", svc.Spec.Ports[0].Name)
}

func TestCustomizeHeadSvcAddsServingCertAnnotation(t *testing.T) {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-head-1",
		},
	}

	customizeHeadSvc(svc, "test-predictor")

	assert.Equal(t, "test-predictor"+constants.ServingCertSecretSuffix,
		svc.Annotations[constants.OpenshiftServingCertAnnotation])
}

// TestCreateServiceEndToEndWithDistro verifies that the distro hooks are actually wired
// through the createService path, not just unit-tested in isolation.
func TestCreateServiceEndToEndWithDistro(t *testing.T) {
	componentMeta := metav1.ObjectMeta{
		Name:      "test-predictor",
		Namespace: "default",
		Annotations: map[string]string{
			"annotation": "value",
		},
	}
	podSpec := &corev1.PodSpec{
		Containers: []corev1.Container{
			{
				Name:  "kserve-container",
				Image: "test-image",
				Ports: []corev1.ContainerPort{{ContainerPort: 8080, Protocol: corev1.ProtocolTCP}},
			},
		},
	}

	services := createService(componentMeta, &v1beta1.ComponentExtensionSpec{}, podSpec, false, &v1beta1.ServiceConfig{})

	require.Len(t, services, 1)
	svc := services[0]

	// Verify the distro hook was wired - serving cert annotation must be present.
	assert.Equal(t, "test-predictor"+constants.ServingCertSecretSuffix,
		svc.Annotations[constants.OpenshiftServingCertAnnotation],
		"customizeService hook must be wired through createService")
}

// TestCreateHeadlessSvcEndToEndWithDistro verifies the head service hook is wired through createHeadlessSvc.
func TestCreateHeadlessSvcEndToEndWithDistro(t *testing.T) {
	componentMeta := metav1.ObjectMeta{
		Name:      "test-predictor",
		Namespace: "default",
		Labels: map[string]string{
			constants.InferenceServiceGenerationPodLabelKey: "1",
		},
	}

	svc := createHeadlessSvc(componentMeta)

	assert.Equal(t, "test-predictor"+constants.ServingCertSecretSuffix,
		svc.Annotations[constants.OpenshiftServingCertAnnotation],
		"customizeHeadSvc hook must be wired through createHeadlessSvc")
}
