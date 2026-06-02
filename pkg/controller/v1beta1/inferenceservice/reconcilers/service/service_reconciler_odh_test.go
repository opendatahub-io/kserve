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

	customizeService(svc, meta)

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

	customizeService(svc, meta)

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

	customizeService(svc, meta)

	assert.Equal(t, int32(constants.OauthProxyPort), svc.Spec.Ports[0].Port)
	assert.Equal(t, "https", svc.Spec.Ports[0].Name)
	assert.Equal(t, intstr.IntOrString{Type: intstr.String, StrVal: "https"}, svc.Spec.Ports[0].TargetPort)
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

	customizeService(svc, meta)

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

	customizeService(svc, meta)

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
