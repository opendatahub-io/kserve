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

package llmisvc

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/utils/ptr"
	"knative.dev/pkg/apis"
	igwapi "sigs.k8s.io/gateway-api-inference-extension/api/v1alpha2"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
)

func TestMergeSpecs(t *testing.T) {
	tests := []struct {
		name    string
		cfgs    []v1alpha1.LLMInferenceServiceSpec
		want    v1alpha1.LLMInferenceServiceSpec
		wantErr bool
	}{
		{
			name:    "no configs",
			cfgs:    []v1alpha1.LLMInferenceServiceSpec{},
			want:    v1alpha1.LLMInferenceServiceSpec{},
			wantErr: false,
		},
		{
			name: "single config",
			cfgs: []v1alpha1.LLMInferenceServiceSpec{
				{Model: v1alpha1.LLMModelSpec{URI: apis.URL{Path: "model-a"}}},
			},
			want:    v1alpha1.LLMInferenceServiceSpec{Model: v1alpha1.LLMModelSpec{URI: apis.URL{Path: "model-a"}}},
			wantErr: false,
		},
		{
			name: "two configs simple merge",
			cfgs: []v1alpha1.LLMInferenceServiceSpec{
				{Model: v1alpha1.LLMModelSpec{URI: apis.URL{Path: "model-a"}}},
			},
			want: v1alpha1.LLMInferenceServiceSpec{
				Model: v1alpha1.LLMModelSpec{URI: apis.URL{Path: "model-a"}},
			},
			wantErr: false,
		},
		{
			name: "two configs with override",
			cfgs: []v1alpha1.LLMInferenceServiceSpec{
				{
					Model: v1alpha1.LLMModelSpec{URI: apis.URL{Path: "model-a"}},
					WorkloadSpec: v1alpha1.WorkloadSpec{
						Replicas: ptr.To[int32](1),
					},
				},
				{
					Model: v1alpha1.LLMModelSpec{URI: apis.URL{Path: "model-b"}},
					WorkloadSpec: v1alpha1.WorkloadSpec{
						Replicas: ptr.To[int32](2),
					},
				},
			},
			want: v1alpha1.LLMInferenceServiceSpec{
				Model: v1alpha1.LLMModelSpec{URI: apis.URL{Path: "model-b"}},
				WorkloadSpec: v1alpha1.WorkloadSpec{
					Replicas: ptr.To[int32](2),
				},
			},
			wantErr: false,
		},
		{
			name: "three configs chained merge",
			cfgs: []v1alpha1.LLMInferenceServiceSpec{
				{Model: v1alpha1.LLMModelSpec{URI: apis.URL{Path: "model-a"}}},
				{
					Model: v1alpha1.LLMModelSpec{URI: apis.URL{Path: "model-b"}},
				},
			},
			want: v1alpha1.LLMInferenceServiceSpec{
				Model: v1alpha1.LLMModelSpec{URI: apis.URL{Path: "model-b"}},
			},
			wantErr: false,
		},
		{
			name: "deep merge with podspec template",
			cfgs: []v1alpha1.LLMInferenceServiceSpec{
				// Base configuration
				{
					WorkloadSpec: v1alpha1.WorkloadSpec{
						Replicas: ptr.To[int32](1),
						Template: &corev1.PodSpec{
							InitContainers: []corev1.Container{
								{
									Name:  "storage-initializer",
									Image: "kserve/storage-initializer:latest",
									Resources: corev1.ResourceRequirements{
										Requests: corev1.ResourceList{
											corev1.ResourceStorage: resource.MustParse("1Mi"),
										},
									},
								},
							},
							Containers: []corev1.Container{
								{
									Name:  "kserve-container",
									Image: "base:0.1",
									Resources: corev1.ResourceRequirements{
										Requests: corev1.ResourceList{
											corev1.ResourceCPU: resource.MustParse("1"),
										},
									},
								},
							},
							Tolerations: []corev1.Toleration{
								{Key: "team", Operator: corev1.TolerationOpEqual, Value: "a"},
							},
						},
					},
				},
				// Override configuration
				{
					WorkloadSpec: v1alpha1.WorkloadSpec{
						Replicas: ptr.To[int32](2),
						Template: &corev1.PodSpec{
							InitContainers: []corev1.Container{
								{
									Name: "storage-initializer",
									Resources: corev1.ResourceRequirements{
										Requests: corev1.ResourceList{
											corev1.ResourceStorage: resource.MustParse("1Gi"),
										},
									},
								},
							},
							Containers: []corev1.Container{
								// This container should replace the base one due to the same name
								{
									Name:  "kserve-container",
									Image: "override:1.0",
									Env: []corev1.EnvVar{
										{Name: "FOO", Value: "bar"},
									},
									Resources: corev1.ResourceRequirements{
										Requests: corev1.ResourceList{
											corev1.ResourceCPU: resource.MustParse("2"), // Override CPU
										},
									},
								},
								// This is a new container that should be added
								{
									Name:  "transformer",
									Image: "transformer:latest",
								},
							},
							// Tolerations should be REPLACED, not merged, as there is no patchMergeKey
							Tolerations: []corev1.Toleration{
								{Key: "gpu", Operator: corev1.TolerationOpExists},
							},
							PriorityClassName: "high-priority", // Add a new field
						},
					},
				},
			},
			// Expected result of the merge
			want: v1alpha1.LLMInferenceServiceSpec{
				WorkloadSpec: v1alpha1.WorkloadSpec{
					Replicas: ptr.To[int32](2),
					Template: &corev1.PodSpec{
						InitContainers: []corev1.Container{
							{
								Name:  "storage-initializer",
								Image: "kserve/storage-initializer:latest", // Image is preserved from base
								Resources: corev1.ResourceRequirements{ // Resources are updated from override
									Requests: corev1.ResourceList{
										corev1.ResourceStorage: resource.MustParse("1Gi"),
									},
								},
							},
						},
						Containers: []corev1.Container{
							{
								Name:  "kserve-container",
								Image: "override:1.0",
								Env: []corev1.EnvVar{
									{Name: "FOO", Value: "bar"},
								},
								Resources: corev1.ResourceRequirements{
									Requests: corev1.ResourceList{
										corev1.ResourceCPU: resource.MustParse("2"),
									},
								},
							},
							{
								Name:  "transformer",
								Image: "transformer:latest",
							},
						},
						// Tolerations slice is replaced by the override
						Tolerations: []corev1.Toleration{
							{Key: "gpu", Operator: corev1.TolerationOpExists},
						},
						PriorityClassName: "high-priority",
					},
				},
			},
			wantErr: false,
		},
		{
			name: "merge with prefill spec",
			cfgs: []v1alpha1.LLMInferenceServiceSpec{
				// Base has only a decode workload
				{
					WorkloadSpec: v1alpha1.WorkloadSpec{
						Replicas: ptr.To[int32](1),
						Template: &corev1.PodSpec{Containers: []corev1.Container{{Name: "kserve-container", Image: "decode:0.1"}}},
					},
				},
				// Override adds a prefill workload
				{
					Prefill: &v1alpha1.WorkloadSpec{
						Replicas: ptr.To[int32](4),
						Template: &corev1.PodSpec{Containers: []corev1.Container{{Name: "kserve-container", Image: "prefill:0.1"}}},
					},
				},
			},
			want: v1alpha1.LLMInferenceServiceSpec{
				// Base workload spec is preserved
				WorkloadSpec: v1alpha1.WorkloadSpec{
					Replicas: ptr.To[int32](1),
					Template: &corev1.PodSpec{Containers: []corev1.Container{{Name: "kserve-container", Image: "decode:0.1"}}},
				},
				// Prefill spec is added
				Prefill: &v1alpha1.WorkloadSpec{
					Replicas: ptr.To[int32](4),
					Template: &corev1.PodSpec{Containers: []corev1.Container{{Name: "kserve-container", Image: "prefill:0.1"}}},
				},
			},
		},
		{
			name: "merge with worker spec",
			cfgs: []v1alpha1.LLMInferenceServiceSpec{
				// Base has the main head/decode template
				{
					WorkloadSpec: v1alpha1.WorkloadSpec{
						Template: &corev1.PodSpec{Containers: []corev1.Container{{Name: "kserve-container", Image: "head:0.1"}}},
					},
				},
				// Override adds a worker spec
				{
					WorkloadSpec: v1alpha1.WorkloadSpec{
						Worker: &corev1.PodSpec{Containers: []corev1.Container{{Name: "kserve-container", Image: "worker:0.1"}}},
					},
				},
			},
			want: v1alpha1.LLMInferenceServiceSpec{
				WorkloadSpec: v1alpha1.WorkloadSpec{
					// Head template is preserved
					Template: &corev1.PodSpec{Containers: []corev1.Container{{Name: "kserve-container", Image: "head:0.1"}}},
					// Worker spec is added
					Worker: &corev1.PodSpec{Containers: []corev1.Container{{Name: "kserve-container", Image: "worker:0.1"}}},
				},
			},
		},
		{
			name: "merge with parallelism spec",
			cfgs: []v1alpha1.LLMInferenceServiceSpec{
				// Base defines tensor parallelism
				{
					WorkloadSpec: v1alpha1.WorkloadSpec{
						Parallelism: &v1alpha1.ParallelismSpec{
							Tensor: ptr.To[int64](2),
						},
					},
				},
				// Override defines pipeline parallelism
				{
					WorkloadSpec: v1alpha1.WorkloadSpec{
						Parallelism: &v1alpha1.ParallelismSpec{
							Pipeline: ptr.To[int64](4),
						},
					},
				},
			},
			want: v1alpha1.LLMInferenceServiceSpec{
				WorkloadSpec: v1alpha1.WorkloadSpec{
					// Both parallelism values should be present
					Parallelism: &v1alpha1.ParallelismSpec{
						Tensor:   ptr.To[int64](2),
						Pipeline: ptr.To[int64](4),
					},
				},
			},
		},
		{
			name: "deep merge of prefill spec",
			cfgs: []v1alpha1.LLMInferenceServiceSpec{
				// Base defines a prefill workload with replicas and a container with a resource request
				{
					Prefill: &v1alpha1.WorkloadSpec{
						Replicas: ptr.To[int32](2),
						Template: &corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "prefill-container",
									Image: "prefill:0.1",
									Resources: corev1.ResourceRequirements{
										Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
									},
								},
							},
						},
					},
				},
				// Override changes replica count and adds an environment variable to the container
				{
					Prefill: &v1alpha1.WorkloadSpec{
						Replicas: ptr.To[int32](4),
						Template: &corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name: "prefill-container",
									Env: []corev1.EnvVar{
										{Name: "PREFILL_MODE", Value: "FAST"},
									},
								},
							},
						},
					},
				},
			},
			want: v1alpha1.LLMInferenceServiceSpec{
				Prefill: &v1alpha1.WorkloadSpec{
					Replicas: ptr.To[int32](4), // Replicas are overridden
					Template: &corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "prefill-container",
								Image: "prefill:0.1", // Image is preserved from base
								Env: []corev1.EnvVar{ // Env var is added from override
									{Name: "PREFILL_MODE", Value: "FAST"},
								},
								Resources: corev1.ResourceRequirements{ // Resources are preserved from base
									Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
								},
							},
						},
					},
				},
			},
		},
		{
			name: "4 chained merge router, epp, multi node",
			cfgs: []v1alpha1.LLMInferenceServiceSpec{
				{
					Router: &v1alpha1.RouterSpec{
						Route:   &v1alpha1.GatewayRoutesSpec{},
						Gateway: &v1alpha1.GatewaySpec{},
					},
				},
				{
					WorkloadSpec: v1alpha1.WorkloadSpec{
						Parallelism: &v1alpha1.ParallelismSpec{
							Tensor:   ptr.To[int64](1),
							Pipeline: ptr.To[int64](1),
						},
						Worker: &corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name: "main",
									Resources: corev1.ResourceRequirements{
										Requests: corev1.ResourceList{
											corev1.ResourceCPU: resource.MustParse("1"),
											"nvidia.com/gpu":   resource.MustParse("1"),
										},
									},
								},
							},
						},
					},
				},
				{
					Router: &v1alpha1.RouterSpec{
						Scheduler: &v1alpha1.SchedulerSpec{
							Pool: &v1alpha1.InferencePoolSpec{
								Spec: &igwapi.InferencePoolSpec{
									TargetPortNumber: 0,
									EndpointPickerConfig: igwapi.EndpointPickerConfig{
										ExtensionRef: &igwapi.Extension{
											ExtensionConnection: igwapi.ExtensionConnection{
												FailureMode: ptr.To(igwapi.FailClose),
											},
										},
									},
								},
							},
							Template: &corev1.PodSpec{
								Containers: []corev1.Container{
									{
										Name: "main",
									},
								},
							},
						},
					},
				},
				{
					WorkloadSpec: v1alpha1.WorkloadSpec{
						Parallelism: &v1alpha1.ParallelismSpec{
							Tensor:   ptr.To[int64](4),
							Pipeline: ptr.To[int64](2),
						},
						Worker: &corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name: "main",
									Resources: corev1.ResourceRequirements{
										Requests: corev1.ResourceList{
											corev1.ResourceCPU: resource.MustParse("1"),
											"nvidia.com/gpu":   resource.MustParse("4"),
										},
									},
								},
							},
						},
					},
				},
			},
			want: v1alpha1.LLMInferenceServiceSpec{
				Router: &v1alpha1.RouterSpec{
					Route:   &v1alpha1.GatewayRoutesSpec{},
					Gateway: &v1alpha1.GatewaySpec{},
					Scheduler: &v1alpha1.SchedulerSpec{
						Pool: &v1alpha1.InferencePoolSpec{
							Spec: &igwapi.InferencePoolSpec{
								TargetPortNumber: 0,
								EndpointPickerConfig: igwapi.EndpointPickerConfig{
									ExtensionRef: &igwapi.Extension{
										ExtensionConnection: igwapi.ExtensionConnection{
											FailureMode: ptr.To(igwapi.FailClose),
										},
									},
								},
							},
						},
						Template: &corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name: "main",
								},
							},
						},
					},
				},
				WorkloadSpec: v1alpha1.WorkloadSpec{
					Parallelism: &v1alpha1.ParallelismSpec{
						Tensor:   ptr.To[int64](4),
						Pipeline: ptr.To[int64](2),
					},
					Worker: &corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name: "main",
								Resources: corev1.ResourceRequirements{
									Requests: corev1.ResourceList{
										corev1.ResourceCPU: resource.MustParse("1"),
										"nvidia.com/gpu":   resource.MustParse("4"),
									},
								},
							},
						},
					},
				},
			},
		},
		{
			name: "merge requests and limits",
			cfgs: []v1alpha1.LLMInferenceServiceSpec{
				{
					Router: &v1alpha1.RouterSpec{
						Route:   &v1alpha1.GatewayRoutesSpec{},
						Gateway: &v1alpha1.GatewaySpec{},
					},
				},
				{
					WorkloadSpec: v1alpha1.WorkloadSpec{
						Parallelism: &v1alpha1.ParallelismSpec{
							Tensor:   ptr.To[int64](1),
							Pipeline: ptr.To[int64](1),
						},
						Worker: &corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name: "main",
									Resources: corev1.ResourceRequirements{
										Requests: corev1.ResourceList{
											corev1.ResourceCPU: resource.MustParse("1"),
											"nvidia.com/gpu":   resource.MustParse("1"),
										},
										Limits: corev1.ResourceList{
											"nvidia.com/gpu": resource.MustParse("1"),
										},
									},
									Env: []corev1.EnvVar{
										{Name: "a", Value: "1"},
										{Name: "z", Value: "42"},
									},
									Args: []string{
										"a", "b",
									},
								},
							},
						},
					},
				},
				{
					WorkloadSpec: v1alpha1.WorkloadSpec{
						Worker: &corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name: "main",
									Resources: corev1.ResourceRequirements{
										Requests: corev1.ResourceList{
											corev1.ResourceMemory: resource.MustParse("1Gi"),
										},
										Limits: corev1.ResourceList{
											corev1.ResourceCPU: resource.MustParse("2"),
										},
									},
									Env: []corev1.EnvVar{
										{Name: "b", Value: "2"},
										{Name: "z", Value: ""},
									},
									Args: []string{
										"x", "y",
									},
								},
							},
						},
					},
				},
			},
			want: v1alpha1.LLMInferenceServiceSpec{
				Router: &v1alpha1.RouterSpec{
					Route:   &v1alpha1.GatewayRoutesSpec{},
					Gateway: &v1alpha1.GatewaySpec{},
				},
				WorkloadSpec: v1alpha1.WorkloadSpec{
					Parallelism: &v1alpha1.ParallelismSpec{
						Tensor:   ptr.To[int64](1),
						Pipeline: ptr.To[int64](1),
					},
					Worker: &corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name: "main",
								Resources: corev1.ResourceRequirements{
									Requests: corev1.ResourceList{
										corev1.ResourceMemory: resource.MustParse("1Gi"),
										corev1.ResourceCPU:    resource.MustParse("1"),
										"nvidia.com/gpu":      resource.MustParse("1"),
									},
									Limits: corev1.ResourceList{
										corev1.ResourceCPU: resource.MustParse("2"),
										"nvidia.com/gpu":   resource.MustParse("1"),
									},
								},
								Env: []corev1.EnvVar{
									{Name: "b", Value: "2"},
									{Name: "a", Value: "1"},
									{Name: "z", Value: "42"},
								},
								Args: []string{
									"x", "y",
								},
							},
						},
					},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := MergeSpecs(tt.cfgs...)
			if (err != nil) != tt.wantErr {
				t.Errorf("MergeSpecs() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("MergeSpecs() got = \n%#v\n, want \n%#v\nDiff (-want, +got):\n%s", got, tt.want, diff)
			}
		})
	}
}

func TestReplaceVariables(t *testing.T) {
	tests := []struct {
		name    string
		llmSvc  *v1alpha1.LLMInferenceService
		cfg     *v1alpha1.LLMInferenceServiceConfig
		want    *v1alpha1.LLMInferenceServiceConfig
		wantErr bool
	}{
		{
			name: "Replace model name",
			cfg: &v1alpha1.LLMInferenceServiceConfig{
				Spec: v1alpha1.LLMInferenceServiceSpec{
					Model: v1alpha1.LLMModelSpec{
						Name: ptr.To("{{ .Spec.Model.Name }}"),
					},
					WorkloadSpec: v1alpha1.WorkloadSpec{
						Template: &corev1.PodSpec{
							Containers: []corev1.Container{
								{Args: []string{
									"--served-model-name",
									"{{ .Spec.Model.Name }}",
								}},
							},
						},
					},
				},
			},
			llmSvc: &v1alpha1.LLMInferenceService{
				Spec: v1alpha1.LLMInferenceServiceSpec{
					Model: v1alpha1.LLMModelSpec{
						Name: ptr.To("meta-llama/Llama-3.2-3B-Instruct"),
					},
				},
			},
			want: &v1alpha1.LLMInferenceServiceConfig{
				Spec: v1alpha1.LLMInferenceServiceSpec{
					Model: v1alpha1.LLMModelSpec{
						Name: ptr.To("meta-llama/Llama-3.2-3B-Instruct"),
					},
					WorkloadSpec: v1alpha1.WorkloadSpec{
						Template: &corev1.PodSpec{
							Containers: []corev1.Container{
								{Args: []string{
									"--served-model-name",
									"meta-llama/Llama-3.2-3B-Instruct",
								}},
							},
						},
					},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ReplaceVariables(tt.llmSvc, tt.cfg)
			if (err != nil) != tt.wantErr {
				t.Errorf("ReplaceVariables() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("ReplaceVariables() got = %#v, want %#v\nDiff:\n%s", got, tt.want, diff)
			}
		})
	}
}
