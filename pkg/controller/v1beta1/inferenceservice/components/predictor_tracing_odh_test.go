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

package components

import (
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
	"github.com/kserve/kserve/pkg/apis/serving/v1beta1"
	"github.com/kserve/kserve/pkg/constants"
	"github.com/kserve/kserve/pkg/tracing"
	"github.com/kserve/kserve/pkg/utils"
)

func TestResolveServerTypeForDistro(t *testing.T) {
	tests := []struct {
		name       string
		serverType string
		sRuntime   v1alpha1.ServingRuntimeSpec
		expected   string
	}{
		{
			name:       "detects vLLM from ODH kserve-runtime annotation",
			serverType: "",
			sRuntime: v1alpha1.ServingRuntimeSpec{
				ServingRuntimePodSpec: v1alpha1.ServingRuntimePodSpec{
					Annotations: map[string]string{
						constants.ODHKserveRuntimeAnnotation: constants.ODHKserveRuntimeVLLM,
					},
				},
			},
			expected: constants.ServerTypeVLLMServer,
		},
		{
			name:       "detects MLServer from ODH kserve-runtime annotation",
			serverType: "",
			sRuntime: v1alpha1.ServingRuntimeSpec{
				ServingRuntimePodSpec: v1alpha1.ServingRuntimePodSpec{
					Annotations: map[string]string{
						constants.ODHKserveRuntimeAnnotation: constants.ServerTypeMLServer,
					},
				},
			},
			expected: constants.ServerTypeMLServer,
		},
		{
			name:       "upstream-detected server type is authoritative",
			serverType: constants.ServerTypeTritonServer,
			sRuntime: v1alpha1.ServingRuntimeSpec{
				ServingRuntimePodSpec: v1alpha1.ServingRuntimePodSpec{
					Annotations: map[string]string{
						constants.ODHKserveRuntimeAnnotation: constants.ODHKserveRuntimeVLLM,
					},
				},
			},
			expected: constants.ServerTypeTritonServer,
		},
		{
			name:       "non-vLLM annotation value is not mapped",
			serverType: "",
			sRuntime: v1alpha1.ServingRuntimeSpec{
				ServingRuntimePodSpec: v1alpha1.ServingRuntimePodSpec{
					Annotations: map[string]string{
						constants.ODHKserveRuntimeAnnotation: "triton",
					},
				},
			},
			expected: "",
		},
		{
			name:       "no annotations returns empty",
			serverType: "",
			sRuntime:   v1alpha1.ServingRuntimeSpec{},
			expected:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, resolveServerTypeForDistro(tt.serverType, tt.sRuntime))
		})
	}
}

// TestBuildPredictorResources_InjectsTracingForODHRuntime exercises the full
// predictor resource build against ODH-style ServingRuntimes that identify their
// server type only through the opendatahub.io/kserve-runtime pod annotation (no
// upstream serving.kserve.io/server-type annotation and a non-upstream runtime
// name). It asserts the runtime-specific tracing configuration is injected.
func TestBuildPredictorResources_InjectsTracingForODHRuntime(t *testing.T) {
	const namespace = "default"
	const endpoint = "http://otel-collector:4317"

	tests := []struct {
		name        string
		runtimeName string
		modelFormat string
		odhRuntime  string
		assertFn    func(t *testing.T, c *corev1.Container)
	}{
		{
			name:        "vLLM injects tracing CLI args",
			runtimeName: "vllm-cuda-runtime",
			modelFormat: "vLLM",
			odhRuntime:  constants.ODHKserveRuntimeVLLM,
			assertFn: func(t *testing.T, c *corev1.Container) {
				assert.Contains(t, c.Args, "--otlp-traces-endpoint")
				assert.Contains(t, c.Args, endpoint)
				assert.Contains(t, c.Args, "--collect-detailed-traces")
				assert.Contains(t, c.Args, "all")
			},
		},
		{
			name:        "MLServer injects tracing env var",
			runtimeName: "mlserver-runtime",
			modelFormat: "sklearn",
			odhRuntime:  constants.ServerTypeMLServer,
			assertFn: func(t *testing.T, c *corev1.Container) {
				value, ok := utils.GetEnvVarValue(c.Env, tracing.EnvMLServerTracingServer)
				assert.True(t, ok, "expected %s env var", tracing.EnvMLServerTracingServer)
				assert.Equal(t, endpoint, value)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := runtime.NewScheme()
			assert.NoError(t, v1alpha1.AddToScheme(s))
			assert.NoError(t, v1beta1.AddToScheme(s))
			assert.NoError(t, appsv1.AddToScheme(s))

			servingRuntime := &v1alpha1.ServingRuntime{
				ObjectMeta: metav1.ObjectMeta{Name: tt.runtimeName, Namespace: namespace},
				Spec: v1alpha1.ServingRuntimeSpec{
					SupportedModelFormats: []v1alpha1.SupportedModelFormat{
						{Name: tt.modelFormat, AutoSelect: ptr.To(true)},
					},
					ServingRuntimePodSpec: v1alpha1.ServingRuntimePodSpec{
						// ODH advertises the runtime type here, not via metadata annotations.
						Annotations: map[string]string{
							constants.ODHKserveRuntimeAnnotation: tt.odhRuntime,
						},
						Containers: []corev1.Container{
							{
								Name:  constants.InferenceServiceContainerName,
								Image: tt.runtimeName + "-image:latest",
								Args:  []string{"--port=8080", "--model=/mnt/models"},
							},
						},
					},
					Disabled: ptr.To(false),
				},
			}

			fakeClient := fake.NewClientBuilder().WithScheme(s).WithObjects(servingRuntime).Build()

			p := &Predictor{
				client:                 fakeClient,
				scheme:                 s,
				inferenceServiceConfig: &v1beta1.InferenceServicesConfig{},
				deploymentMode:         constants.Standard,
				Log:                    logr.Discard(),
			}

			isvc := &v1beta1.InferenceService{
				ObjectMeta: metav1.ObjectMeta{Name: "my-isvc", Namespace: namespace},
				Spec: v1beta1.InferenceServiceSpec{
					Predictor: v1beta1.PredictorSpec{
						Model: &v1beta1.ModelSpec{
							Runtime:     ptr.To(tt.runtimeName),
							ModelFormat: v1beta1.ModelFormat{Name: tt.modelFormat},
						},
					},
					Tracing: &v1beta1.TracingSpec{
						ExporterEndpoint: ptr.To(endpoint),
					},
				},
			}

			resources, err := p.buildPredictorResources(t.Context(), isvc, false)
			assert.NoError(t, err)
			assert.NotNil(t, resources)

			var predContainer *corev1.Container
			for i := range resources.podSpec.Containers {
				if resources.podSpec.Containers[i].Name == constants.InferenceServiceContainerName {
					predContainer = &resources.podSpec.Containers[i]
					break
				}
			}
			assert.NotNil(t, predContainer, "expected predictor container %q in pod spec", constants.InferenceServiceContainerName)
			if predContainer != nil {
				tt.assertFn(t, predContainer)
			}
		})
	}
}
