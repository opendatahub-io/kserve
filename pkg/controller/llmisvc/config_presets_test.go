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

package llmisvc_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"knative.dev/pkg/apis"
	"sigs.k8s.io/yaml"

	"github.com/kserve/kserve/pkg/controller/llmisvc"

	kservetesting "github.com/kserve/kserve/pkg/testing"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
)

// TODO(webhook): re-use webhook logic to do the spec merge and validation
func TestPresetFiles(t *testing.T) {
	presetsDir := filepath.Join(kservetesting.ProjectRoot(), "config", "llmisvc")

	llmSvc := beefyLLMInferenceService()
	kserveSystemConfig := llmisvc.Config{
		SystemNamespace:         "kserve",
		IngressGatewayName:      "kserve-ingress-gateway",
		IngressGatewayNamespace: "kserve",
	}

	tt := map[string][]struct {
		name     string
		llmSvc   *v1alpha1.LLMInferenceService
		expected *v1alpha1.LLMInferenceServiceConfig
	}{
		"config-llm-decode-worker-data-parallel.yaml": {
			{
				llmSvc: &v1alpha1.LLMInferenceService{
					Spec: v1alpha1.LLMInferenceServiceSpec{
						Model: v1alpha1.LLMModelSpec{
							Name: ptr.To("llama"),
						},
						WorkloadSpec: v1alpha1.WorkloadSpec{
							Parallelism: &v1alpha1.ParallelismSpec{
								Data:      ptr.To[int32](4),
								DataLocal: ptr.To[int32](2),
								Tensor:    ptr.To[int32](1),
								Expert:    true,
							},
						},
					},
				},
				expected: &v1alpha1.LLMInferenceServiceConfig{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "serving.kserve.io/v1alpha1",
						Kind:       "LLMInferenceServiceConfig",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name: "kserve-config-llm-decode-worker-data-parallel",
					},
					Spec: v1alpha1.LLMInferenceServiceSpec{
						WorkloadSpec: v1alpha1.WorkloadSpec{
							Worker: &corev1.PodSpec{
								Volumes: []corev1.Volume{
									{
										Name: "home",
										VolumeSource: corev1.VolumeSource{
											EmptyDir: &corev1.EmptyDirVolumeSource{},
										},
									},
									{
										Name: "dshm",
										VolumeSource: corev1.VolumeSource{
											EmptyDir: &corev1.EmptyDirVolumeSource{
												Medium:    corev1.StorageMediumMemory,
												SizeLimit: ptr.To(resource.MustParse("1Gi")),
											},
										},
									},
									{
										Name: "model-cache",
										VolumeSource: corev1.VolumeSource{
											EmptyDir: &corev1.EmptyDirVolumeSource{},
										},
									},
								},
								TerminationGracePeriodSeconds: ptr.To(int64(30)),
								InitContainers: []corev1.Container{
									{
										Name:  "llm-d-routing-sidecar",
										Image: "ghcr.io/llm-d/llm-d-routing-sidecar:0.0.6",
										Args:  []string{"--port=8000", "--vllm-port=8001"},
										Ports: []corev1.ContainerPort{
											{
												ContainerPort: 8000,
												Protocol:      corev1.ProtocolTCP,
											},
										},
										RestartPolicy:            ptr.To(corev1.ContainerRestartPolicyAlways),
										TerminationMessagePath:   "/dev/termination-log",
										TerminationMessagePolicy: "FallbackToLogsOnError",
										ImagePullPolicy:          "IfNotPresent",
									},
								},
								Containers: []corev1.Container{
									{
										Name:    "main",
										Image:   "ghcr.io/llm-d/llm-d:0.0.8",
										Command: []string{"/bin/sh", "-c"},
										Ports: []corev1.ContainerPort{
											{
												ContainerPort: 8001,
												Protocol:      corev1.ProtocolTCP,
											},
										},
										VolumeMounts: []corev1.VolumeMount{
											{
												Name:      "home",
												MountPath: "/home",
											},
											{
												Name:      "dshm",
												MountPath: "/dev/shm",
											},
											{
												Name:      "model-cache",
												MountPath: "/models",
											},
										},
										LivenessProbe: &corev1.Probe{
											ProbeHandler: corev1.ProbeHandler{
												HTTPGet: &corev1.HTTPGetAction{
													Path: "/health",
													Port: intstr.FromInt32(8001),
												},
											},
											InitialDelaySeconds: 10,
											PeriodSeconds:       10,
											TimeoutSeconds:      10,
											FailureThreshold:    3,
										},
										ReadinessProbe: &corev1.Probe{
											ProbeHandler: corev1.ProbeHandler{
												HTTPGet: &corev1.HTTPGetAction{
													Path: "/health",
													Port: intstr.FromInt32(8001),
												},
											},
											InitialDelaySeconds: 10,
											PeriodSeconds:       10,
											TimeoutSeconds:      5,
											FailureThreshold:    60,
										},
										SecurityContext: &corev1.SecurityContext{
											AllowPrivilegeEscalation: ptr.To(false),
											Capabilities: &corev1.Capabilities{
												Add: []corev1.Capability{
													"IPC_LOCK",
													"SYS_RAWIO",
												},
											},
										},
										Env: []corev1.EnvVar{
											{
												Name:  "HOME",
												Value: "/home",
											},
											{
												Name:  "VLLM_LOGGING_LEVEL",
												Value: "INFO",
											},
											{
												Name:  "HF_HUB_CACHE",
												Value: "/models",
											},
										},
										TerminationMessagePath:   "/dev/termination-log",
										TerminationMessagePolicy: "FallbackToLogsOnError",
										ImagePullPolicy:          "IfNotPresent",
										Stdin:                    true,
										TTY:                      true,
										Args: []string{`
START_RANK=$(( ${LWS_WORKER_INDEX:-0} * 2 ))
if [ "${LWS_WORKER_INDEX:-0}" -eq 0 ]; then
  #################
  # Leader-only launch
  #################
  vllm serve \
    llama \
    --port 8001 \
    --api-server-count 4 \
    --disable-log-requests \
--enable-expert-parallel \
--tensor-parallel-size 1 \
    --data-parallel-size 4 \
    --data-parallel-size-local 2 \
    --data-parallel-address $(LWS_LEADER_ADDRESS) \
    --data-parallel-rpc-port 5555 \
    --data-parallel-start-rank $START_RANK \
    --trust-remote-code
else
  #################
  # Worker-only launch
  #################
  vllm serve \
    llama \
    --port 8001 \
    --disable-log-requests \
--enable-expert-parallel \
--tensor-parallel-size 1 \
    --data-parallel-size 4 \
    --data-parallel-size-local 2 \
    --data-parallel-address $(LWS_LEADER_ADDRESS) \
    --data-parallel-rpc-port 5555 \
    --data-parallel-start-rank $START_RANK \
    --trust-remote-code \
    --headless
fi`},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	_ = filepath.Walk(presetsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			t.Errorf("Failed to walk %s: %v", path, err)
			return err
		}

		filename := info.Name()
		if info.IsDir() || !strings.HasSuffix(filename, ".yaml") || !strings.HasPrefix(filename, "config-") {
			return nil
		}

		t.Run(filename, func(t *testing.T) {
			filePath := filepath.Join(presetsDir, filename)
			data, err := os.ReadFile(filePath)
			if err != nil {
				t.Errorf("Failed to read file %s: %v", filePath, err)
				return
			}

			config := &v1alpha1.LLMInferenceServiceConfig{}
			if err := yaml.Unmarshal(data, config); err != nil {
				t.Errorf("Failed to unmarshal YAML from %s: %v", filePath, err)
				return
			}

			if config.APIVersion != "serving.kserve.io/v1alpha1" {
				t.Errorf("Expected APIVersion to be 'serving.kserve.io/v1alpha1', got %s", config.APIVersion)
			}
			if config.Kind != "LLMInferenceServiceConfig" {
				t.Errorf("Expected Kind to be 'LLMInferenceServiceConfig', got %s", config.Kind)
			}
			if config.ObjectMeta.Name == "" {
				t.Error("Expected ObjectMeta.Name to be set")
			}

			_, err = llmisvc.ReplaceVariables(llmSvc, config, &kserveSystemConfig)
			if err != nil {
				t.Errorf("ReplaceVariables() failed for %s: %v", filename, err)
			}

			for _, tc := range tt[filename] {
				t.Run(tc.name, func(t *testing.T) {
					t.Parallel()

					out, err := llmisvc.ReplaceVariables(tc.llmSvc, config, &kserveSystemConfig)
					if err != nil {
						t.Errorf("ReplaceVariables() failed for %s: %v", filename, err)
					}

					if !equality.Semantic.DeepEqual(tc.expected, out) {
						diff := cmp.Diff(tc.expected, out)
						t.Errorf("ReplaceVariables() returned unexpected diff (-want +got):\n%s", diff)
						diff = cmp.Diff(tc.expected.Spec.WorkloadSpec.Worker.Containers[0].Args, out.Spec.WorkloadSpec.Worker.Containers[0].Args)
						t.Errorf("ReplaceVariables() returned unexpected diff (-want +got):\n%s", diff)
					}
				})
			}
		})

		return nil
	})
}

func beefyLLMInferenceService() *v1alpha1.LLMInferenceService {
	svcName := "test-llm-preset"
	nsName := "test-llm-preset-test"
	modelURL, _ := apis.ParseURL("hf://facebook/opt-125m")

	return &v1alpha1.LLMInferenceService{
		ObjectMeta: metav1.ObjectMeta{
			Name:      svcName,
			Namespace: nsName,
			Labels: map[string]string{
				"app.kubernetes.io/name":      "llminferenceservice",
				"app.kubernetes.io/instance":  svcName,
				"app.kubernetes.io/component": "inference",
			},
			Annotations: map[string]string{
				"serving.kserve.io/model-uri": modelURL.String(),
			},
		},
		Spec: v1alpha1.LLMInferenceServiceSpec{
			Model: v1alpha1.LLMModelSpec{
				Name: ptr.To("facebook/opt-125m"),
				URI:  *modelURL,
				Storage: &v1alpha1.LLMStorageSpec{
					Path: ptr.To("/models"),
					Parameters: &map[string]string{
						"storageUri": modelURL.String(),
					},
				},
			},
			WorkloadSpec: v1alpha1.WorkloadSpec{
				Replicas: ptr.To[int32](2),
				Parallelism: &v1alpha1.ParallelismSpec{
					Tensor:   ptr.To[int32](2),
					Pipeline: ptr.To[int32](1),
				},
				Template: &corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "kserve-container",
							Image: "ghcr.io/llm-d/llm-d:0.0.8",
							Ports: []corev1.ContainerPort{
								{
									ContainerPort: 8000,
									Name:          "http",
									Protocol:      corev1.ProtocolTCP,
								},
							},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("2"),
									corev1.ResourceMemory: resource.MustParse("4Gi"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("4"),
									corev1.ResourceMemory: resource.MustParse("8Gi"),
								},
							},
							Env: []corev1.EnvVar{
								{
									Name:  "MODEL_NAME",
									Value: "facebook/opt-125m",
								},
								{
									Name:  "VLLM_LOGGING_LEVEL",
									Value: "INFO",
								},
							},
						},
					},
					Tolerations: []corev1.Toleration{
						{
							Key:      "nvidia.com/gpu",
							Operator: corev1.TolerationOpExists,
							Effect:   corev1.TaintEffectNoSchedule,
						},
					},
					NodeSelector: map[string]string{
						"node.kubernetes.io/instance-type": "gpu-node",
					},
				},
				Worker: &corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "kserve-container",
							Image: "ghcr.io/llm-d/llm-d:0.0.8",
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1"),
									corev1.ResourceMemory: resource.MustParse("2Gi"),
									"nvidia.com/gpu":      resource.MustParse("1"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("2"),
									corev1.ResourceMemory: resource.MustParse("4Gi"),
									"nvidia.com/gpu":      resource.MustParse("1"),
								},
							},
						},
					},
				},
			},
			Prefill: &v1alpha1.WorkloadSpec{
				Replicas: ptr.To[int32](1),
				Parallelism: &v1alpha1.ParallelismSpec{
					Tensor:   ptr.To[int32](1),
					Pipeline: ptr.To[int32](1),
				},
				Template: &corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "kserve-container",
							Image: "ghcr.io/llm-d/llm-d:0.0.8",
							Ports: []corev1.ContainerPort{
								{
									ContainerPort: 8000,
									Name:          "http",
									Protocol:      corev1.ProtocolTCP,
								},
							},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("4"),
									corev1.ResourceMemory: resource.MustParse("8Gi"),
									"nvidia.com/gpu":      resource.MustParse("2"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("8"),
									corev1.ResourceMemory: resource.MustParse("16Gi"),
									"nvidia.com/gpu":      resource.MustParse("2"),
								},
							},
						},
					},
				},
			},
			Router: &v1alpha1.RouterSpec{
				Route: &v1alpha1.GatewayRoutesSpec{
					HTTP: &v1alpha1.HTTPRouteSpec{
						Refs: []corev1.LocalObjectReference{
							{Name: "custom-http-route"},
						},
					},
				},
				Gateway: &v1alpha1.GatewaySpec{
					Refs: []v1alpha1.UntypedObjectReference{
						{
							Name:      "kserve-ingress-gateway",
							Namespace: "kserve",
						},
					},
				},
				Scheduler: &v1alpha1.SchedulerSpec{
					Pool: &v1alpha1.InferencePoolSpec{
						Ref: &corev1.LocalObjectReference{
							Name: "custom-inference-pool",
						},
					},
					Template: &corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "scheduler",
								Image: "ghcr.io/llm-d/llm-d-inference-scheduler:0.0.4",
								Ports: []corev1.ContainerPort{
									{
										ContainerPort: 9002,
										Name:          "grpc",
										Protocol:      corev1.ProtocolTCP,
									},
									{
										ContainerPort: 9003,
										Name:          "grpc-health",
										Protocol:      corev1.ProtocolTCP,
									},
								},
								Resources: corev1.ResourceRequirements{
									Requests: corev1.ResourceList{
										corev1.ResourceCPU:    resource.MustParse("256m"),
										corev1.ResourceMemory: resource.MustParse("500Mi"),
									},
								},
								Env: []corev1.EnvVar{
									{Name: "ENABLE_LOAD_AWARE_SCORER", Value: "true"},
									{Name: "POOL_NAME", Value: svcName + "-inference-pool"},
									{Name: "POOL_NAMESPACE", Value: nsName},
								},
							},
						},
					},
				},
			},
		},
	}
}
