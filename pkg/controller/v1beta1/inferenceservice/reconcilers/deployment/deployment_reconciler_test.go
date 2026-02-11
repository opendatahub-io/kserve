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

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	errors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/ptr"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"

	"k8s.io/client-go/kubernetes/fake"

	"github.com/kserve/kserve/pkg/apis/serving/v1beta1"
	"github.com/kserve/kserve/pkg/constants"
	isvcutils "github.com/kserve/kserve/pkg/controller/v1beta1/inferenceservice/utils"
	"github.com/kserve/kserve/pkg/utils"
)

const oauthProxyISVCConfigKey = "oauthProxy"

func TestCreateDefaultDeployment(t *testing.T) {
	type args struct {
		clientset        kubernetes.Interface
		objectMeta       metav1.ObjectMeta
		workerObjectMeta metav1.ObjectMeta
		componentExt     *v1beta1.ComponentExtensionSpec
		podSpec          *corev1.PodSpec
		workerPodSpec    *corev1.PodSpec
	}
	testInput := map[string]args{
		"defaultDeployment": {
			objectMeta: metav1.ObjectMeta{
				Name:      "default-predictor",
				Namespace: "default-predictor-namespace",
				Annotations: map[string]string{
					"annotation": "annotation-value",
				},
				Labels: map[string]string{
					constants.DeploymentMode:  string(constants.RawDeployment),
					constants.AutoscalerClass: string(constants.DefaultAutoscalerClass),
				},
			},
			workerObjectMeta: metav1.ObjectMeta{},
			componentExt:     &v1beta1.ComponentExtensionSpec{},
			podSpec: &corev1.PodSpec{
				Volumes: []corev1.Volume{
					{
						Name: "default-predictor-example-volume",
					},
				},
				Containers: []corev1.Container{
					{
						Name:  constants.InferenceServiceContainerName,
						Image: "default-predictor-example-image",
						Env: []corev1.EnvVar{
							{Name: "default-predictor-example-env", Value: "example-env"},
						},
					},
				},
			},
			workerPodSpec: nil,
		},
		"multiNode-deployment": {
			objectMeta: metav1.ObjectMeta{
				Name:      "default-predictor",
				Namespace: "default-predictor-namespace",
				Annotations: map[string]string{
					"annotation": "annotation-value",
				},
				Labels: map[string]string{
					constants.DeploymentMode:  string(constants.RawDeployment),
					constants.AutoscalerClass: string(constants.AutoscalerClassNone),
				},
			},
			workerObjectMeta: metav1.ObjectMeta{
				Name:      "worker-predictor",
				Namespace: "worker-predictor-namespace",
				Annotations: map[string]string{
					"annotation": "annotation-value",
				},
				Labels: map[string]string{
					constants.DeploymentMode:  string(constants.RawDeployment),
					constants.AutoscalerClass: string(constants.AutoscalerClassNone),
				},
			},
			componentExt: &v1beta1.ComponentExtensionSpec{},
			podSpec: &corev1.PodSpec{
				Volumes: []corev1.Volume{
					{
						Name: "default-predictor-example-volume",
					},
				},
				Containers: []corev1.Container{
					{
						Name:  constants.InferenceServiceContainerName,
						Image: "default-predictor-example-image",
						Env: []corev1.EnvVar{
							{Name: "TENSOR_PARALLEL_SIZE", Value: "1"},
							{Name: "MODEL_NAME"},
							{Name: "PIPELINE_PARALLEL_SIZE", Value: "2"},
							{Name: "RAY_NODE_COUNT", Value: "2"},
							{Name: "REQUEST_GPU_COUNT", Value: "1"},
						},
						Resources: corev1.ResourceRequirements{
							Limits: corev1.ResourceList{
								constants.NvidiaGPUResourceType: resource.MustParse("1"),
							},
							Requests: corev1.ResourceList{
								constants.NvidiaGPUResourceType: resource.MustParse("1"),
							},
						},
					},
				},
			},
			workerPodSpec: &corev1.PodSpec{
				Volumes: []corev1.Volume{
					{
						Name: "worker-predictor-example-volume",
					},
				},
				Containers: []corev1.Container{
					{
						Name:  "worker-container",
						Image: "worker-predictor-example-image",
						Env: []corev1.EnvVar{
							{Name: "worker-predictor-example-env", Value: "example-env"},
							{Name: "RAY_NODE_COUNT", Value: "2"},
							{Name: "REQUEST_GPU_COUNT", Value: "1"},
							{Name: "ISVC_NAME"},
						},
						Resources: corev1.ResourceRequirements{
							Limits: corev1.ResourceList{
								constants.NvidiaGPUResourceType: resource.MustParse("1"),
							},
							Requests: corev1.ResourceList{
								constants.NvidiaGPUResourceType: resource.MustParse("1"),
							},
						},
					},
				},
			},
		},
	}

	expectedDeploymentPodSpecs := map[string][]*appsv1.Deployment{
		"defaultDeployment": {
			&appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "default-predictor",
					Namespace: "default-predictor-namespace",
					Annotations: map[string]string{
						"annotation": "annotation-value",
					},
					Labels: map[string]string{
						constants.RawDeploymentAppLabel: "isvc.default-predictor",
						constants.AutoscalerClass:       string(constants.AutoscalerClassHPA),
						constants.DeploymentMode:        string(constants.RawDeployment),
					},
				},
				Spec: appsv1.DeploymentSpec{
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							constants.RawDeploymentAppLabel: "isvc.default-predictor",
						},
					},
					Strategy: appsv1.DeploymentStrategy{
						Type: appsv1.RollingUpdateDeploymentStrategyType,
						RollingUpdate: &appsv1.RollingUpdateDeployment{
							MaxUnavailable: intStrPtr("25%"),
							MaxSurge:       intStrPtr("25%"),
						},
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "default-predictor",
							Namespace: "default-predictor-namespace",
							Annotations: map[string]string{
								"annotation": "annotation-value",
							},
							Labels: map[string]string{
								constants.RawDeploymentAppLabel: "isvc.default-predictor",
								constants.AutoscalerClass:       string(constants.AutoscalerClassHPA),
								constants.DeploymentMode:        string(constants.RawDeployment),
							},
						},
						Spec: corev1.PodSpec{
							Volumes:                      []corev1.Volume{{Name: "default-predictor-example-volume"}},
							AutomountServiceAccountToken: BoolPtr(false),
							Containers: []corev1.Container{
								{
									Name:  constants.InferenceServiceContainerName,
									Image: "default-predictor-example-image",
									Env: []corev1.EnvVar{
										{Name: "default-predictor-example-env", Value: "example-env"},
									},
									ImagePullPolicy:          "IfNotPresent",
									TerminationMessagePolicy: "File",
									TerminationMessagePath:   "/dev/termination-log",
									ReadinessProbe: &corev1.Probe{
										ProbeHandler: corev1.ProbeHandler{
											TCPSocket: &corev1.TCPSocketAction{
												Port: intstr.IntOrString{IntVal: 8080},
												Host: "",
											},
										},
										TimeoutSeconds:   1,
										PeriodSeconds:    10,
										SuccessThreshold: 1,
										FailureThreshold: 3,
									},
								},
							},
						},
					},
				},
			},
			nil,
		},
		"multiNode-deployment": {
			&appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "default-predictor",
					Namespace: "default-predictor-namespace",
					Annotations: map[string]string{
						"annotation": "annotation-value",
					},
					Labels: map[string]string{
						"app":                               "isvc.default-predictor",
						"serving.kserve.io/autoscalerClass": "none",
						"serving.kserve.io/deploymentMode":  "RawDeployment",
					},
				},
				Spec: appsv1.DeploymentSpec{
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							"app": "isvc.default-predictor",
						},
					},
					Strategy: appsv1.DeploymentStrategy{
						Type: appsv1.RollingUpdateDeploymentStrategyType,
						RollingUpdate: &appsv1.RollingUpdateDeployment{
							MaxUnavailable: intStrPtr("25%"),
							MaxSurge:       intStrPtr("25%"),
						},
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "default-predictor",
							Namespace: "default-predictor-namespace",
							Annotations: map[string]string{
								"annotation": "annotation-value",
							},
							Labels: map[string]string{
								"app":                               "isvc.default-predictor",
								"serving.kserve.io/autoscalerClass": "none",
								"serving.kserve.io/deploymentMode":  "RawDeployment",
							},
						},
						Spec: corev1.PodSpec{
							Volumes:                      []corev1.Volume{{Name: "default-predictor-example-volume"}},
							AutomountServiceAccountToken: BoolPtr(false),
							Containers: []corev1.Container{
								{
									Name:  constants.InferenceServiceContainerName,
									Image: "default-predictor-example-image",
									Env: []corev1.EnvVar{
										{Name: "TENSOR_PARALLEL_SIZE", Value: "1"},
										{Name: "MODEL_NAME"},
										{Name: "PIPELINE_PARALLEL_SIZE", Value: "2"},
										{Name: "RAY_NODE_COUNT", Value: "2"},
										{Name: "REQUEST_GPU_COUNT", Value: "1"},
									},
									Resources: corev1.ResourceRequirements{
										Limits: corev1.ResourceList{
											constants.NvidiaGPUResourceType: resource.MustParse("1"),
										},
										Requests: corev1.ResourceList{
											constants.NvidiaGPUResourceType: resource.MustParse("1"),
										},
									},
									ImagePullPolicy:          "IfNotPresent",
									TerminationMessagePolicy: "File",
									TerminationMessagePath:   "/dev/termination-log",
									ReadinessProbe: &corev1.Probe{
										ProbeHandler: corev1.ProbeHandler{
											TCPSocket: &corev1.TCPSocketAction{
												Port: intstr.IntOrString{IntVal: 8080},
												Host: "",
											},
										},
										TimeoutSeconds:   1,
										PeriodSeconds:    10,
										SuccessThreshold: 1,
										FailureThreshold: 3,
									},
								},
							},
						},
					},
				},
			},
			&appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "worker-predictor",
					Namespace: "worker-predictor-namespace",
					Annotations: map[string]string{
						"annotation": "annotation-value",
					},
					Labels: map[string]string{
						constants.RawDeploymentAppLabel: "isvc.default-predictor-worker",
						constants.AutoscalerClass:       string(constants.AutoscalerClassNone),
						constants.DeploymentMode:        string(constants.RawDeployment),
					},
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: int32Ptr(1),
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							constants.RawDeploymentAppLabel: "isvc.default-predictor-worker",
						},
					},
					Strategy: appsv1.DeploymentStrategy{
						Type: appsv1.RollingUpdateDeploymentStrategyType,
						RollingUpdate: &appsv1.RollingUpdateDeployment{
							MaxUnavailable: intStrPtr("0%"),
							MaxSurge:       intStrPtr("100%"),
						},
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "worker-predictor",
							Namespace: "worker-predictor-namespace",
							Annotations: map[string]string{
								"annotation": "annotation-value",
							},
							Labels: map[string]string{
								constants.RawDeploymentAppLabel: "isvc.default-predictor-worker",
								constants.AutoscalerClass:       string(constants.AutoscalerClassNone),
								constants.DeploymentMode:        string(constants.RawDeployment),
							},
						},
						Spec: corev1.PodSpec{
							Volumes:                      []corev1.Volume{{Name: "worker-predictor-example-volume"}},
							AutomountServiceAccountToken: BoolPtr(false),
							Containers: []corev1.Container{
								{
									Name:  "worker-container",
									Image: "worker-predictor-example-image",
									Env: []corev1.EnvVar{
										{Name: "worker-predictor-example-env", Value: "example-env"},
										{Name: "RAY_NODE_COUNT", Value: "2"},
										{Name: "REQUEST_GPU_COUNT", Value: "1"},
										{Name: "ISVC_NAME"},
									},
									Resources: corev1.ResourceRequirements{
										Limits: corev1.ResourceList{
											constants.NvidiaGPUResourceType: resource.MustParse("1"),
										},
										Requests: corev1.ResourceList{
											constants.NvidiaGPUResourceType: resource.MustParse("1"),
										},
									},
									ImagePullPolicy:          "IfNotPresent",
									TerminationMessagePolicy: "File",
									TerminationMessagePath:   "/dev/termination-log",
								},
							},
						},
					},
				},
			},
		},
	}

	tests := []struct {
		name        string
		args        args
		expected    []*appsv1.Deployment
		expectedErr error
	}{
		{
			name: "default deployment",
			args: args{
				objectMeta:       testInput["defaultDeployment"].objectMeta,
				workerObjectMeta: testInput["defaultDeployment"].workerObjectMeta,
				componentExt:     testInput["defaultDeployment"].componentExt,
				podSpec:          testInput["defaultDeployment"].podSpec,
				workerPodSpec:    testInput["defaultDeployment"].workerPodSpec,
			},
			expected:    expectedDeploymentPodSpecs["defaultDeployment"],
			expectedErr: nil,
		},
		{
			name: "multiNode-deployment",
			args: args{
				objectMeta:       testInput["multiNode-deployment"].objectMeta,
				workerObjectMeta: testInput["multiNode-deployment"].workerObjectMeta,
				componentExt:     testInput["multiNode-deployment"].componentExt,
				podSpec:          testInput["multiNode-deployment"].podSpec,
				workerPodSpec:    testInput["multiNode-deployment"].workerPodSpec,
			},
			expected:    expectedDeploymentPodSpecs["multiNode-deployment"],
			expectedErr: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := createRawDeployment(tt.args.objectMeta, tt.args.workerObjectMeta, tt.args.componentExt, tt.args.podSpec, tt.args.workerPodSpec)
			assert.Equal(t, tt.expectedErr, err)
			if len(got) == 0 {
				t.Errorf("Got empty deployment")
			}

			for i, deploy := range got {
				if diff := cmp.Diff(tt.expected[i], deploy, cmpopts.IgnoreFields(appsv1.Deployment{}, "Spec.Template.Spec.SecurityContext"),
					cmpopts.IgnoreFields(appsv1.Deployment{}, "Spec.Template.Spec.RestartPolicy"),
					cmpopts.IgnoreFields(appsv1.Deployment{}, "Spec.Template.Spec.TerminationGracePeriodSeconds"),
					cmpopts.IgnoreFields(appsv1.Deployment{}, "Spec.Template.Spec.DNSPolicy"),
					cmpopts.IgnoreFields(appsv1.Deployment{}, "Spec.Template.Spec.AutomountServiceAccountToken"),
					cmpopts.IgnoreFields(appsv1.Deployment{}, "Spec.Template.Spec.SchedulerName"),
					cmpopts.IgnoreFields(appsv1.Deployment{}, "Spec.RevisionHistoryLimit"),
					cmpopts.IgnoreFields(appsv1.Deployment{}, "Spec.ProgressDeadlineSeconds")); diff != "" {
					t.Errorf("Test %q unexpected deployment (-want +got): %v", tt.name, diff)
				}
			}
		})
	}

	// deepCopyArgs creates a deep copy of the provided args struct.
	// It ensures that nested pointers (componentExt, podSpec, workerPodSpec) are properly duplicated
	// to avoid unintended side effects when the original struct is modified.
	deepCopyArgs := func(src args) args {
		dst := args{
			objectMeta:       src.objectMeta,
			workerObjectMeta: src.workerObjectMeta,
		}
		if src.componentExt != nil {
			dst.componentExt = src.componentExt.DeepCopy()
		}
		if src.podSpec != nil {
			dst.podSpec = src.podSpec.DeepCopy()
		}
		if src.workerPodSpec != nil {
			dst.workerPodSpec = src.workerPodSpec.DeepCopy()
		}
		return dst
	}

	getDefaultArgs := func() args {
		return deepCopyArgs(testInput["multiNode-deployment"])
	}
	getDefaultExpectedDeployment := func() []*appsv1.Deployment {
		return deepCopyDeploymentList(expectedDeploymentPodSpecs["multiNode-deployment"])
	}

	// pipelineParallelSize test
	objectMeta_tests := []struct {
		name           string
		modifyArgs     func(args) args
		modifyExpected func([]*appsv1.Deployment) []*appsv1.Deployment
		expectedErr    error
	}{
		{
			name: "Set RAY_NODE_COUNT to 3 when pipelineParallelSize is 3 and tensorParallelSize is 1, with 2 worker node replicas",
			modifyArgs: func(updatedArgs args) args {
				if updatedArgs.podSpec.Containers[0].Name == constants.InferenceServiceContainerName {
					isvcutils.AddEnvVarToPodSpec(updatedArgs.podSpec, constants.InferenceServiceContainerName, constants.PipelineParallelSizeEnvName, "3")
					isvcutils.AddEnvVarToPodSpec(updatedArgs.podSpec, constants.InferenceServiceContainerName, constants.RayNodeCountEnvName, "3")
				}
				if updatedArgs.workerPodSpec.Containers[0].Name == constants.WorkerContainerName {
					isvcutils.AddEnvVarToPodSpec(updatedArgs.workerPodSpec, constants.WorkerContainerName, constants.RayNodeCountEnvName, "3")
				}
				return updatedArgs
			},
			modifyExpected: func(updatedExpected []*appsv1.Deployment) []*appsv1.Deployment {
				// updatedExpected[0] is default deployment, updatedExpected[1] is worker node deployment
				addEnvVarToDeploymentSpec(&updatedExpected[0].Spec, constants.InferenceServiceContainerName, constants.PipelineParallelSizeEnvName, "3")
				addEnvVarToDeploymentSpec(&updatedExpected[0].Spec, constants.InferenceServiceContainerName, constants.RayNodeCountEnvName, "3")
				addEnvVarToDeploymentSpec(&updatedExpected[1].Spec, constants.WorkerContainerName, constants.RayNodeCountEnvName, "3")
				updatedExpected[1].Spec.Replicas = int32Ptr(2)
				return updatedExpected
			},
		},
	}

	for _, tt := range objectMeta_tests {
		t.Run(tt.name, func(t *testing.T) {
			// retrieve args, expected
			ttArgs := getDefaultArgs()
			ttExpected := getDefaultExpectedDeployment()

			// update objectMeta using modify func
			got, err := createRawDeployment(ttArgs.objectMeta, ttArgs.workerObjectMeta, ttArgs.componentExt, tt.modifyArgs(ttArgs).podSpec, tt.modifyArgs(ttArgs).workerPodSpec)
			assert.Equal(t, tt.expectedErr, err)
			if len(got) == 0 {
				t.Errorf("Got empty deployment")
			}

			// update expected value using modifyExpected func
			expected := tt.modifyExpected(ttExpected)

			for i, deploy := range got {
				if diff := cmp.Diff(expected[i], deploy, cmpopts.IgnoreFields(appsv1.Deployment{}, "Spec.Template.Spec.SecurityContext"),
					cmpopts.IgnoreFields(appsv1.Deployment{}, "Spec.Template.Spec.RestartPolicy"),
					cmpopts.IgnoreFields(appsv1.Deployment{}, "Spec.Template.Spec.TerminationGracePeriodSeconds"),
					cmpopts.IgnoreFields(appsv1.Deployment{}, "Spec.Template.Spec.DNSPolicy"),
					cmpopts.IgnoreFields(appsv1.Deployment{}, "Spec.Template.Spec.AutomountServiceAccountToken"),
					cmpopts.IgnoreFields(appsv1.Deployment{}, "Spec.Template.Spec.SchedulerName"),
					cmpopts.IgnoreFields(appsv1.Deployment{}, "Spec.RevisionHistoryLimit"),
					cmpopts.IgnoreFields(appsv1.Deployment{}, "Spec.ProgressDeadlineSeconds")); diff != "" {
					t.Errorf("Test %q unexpected deployment (-want +got): %v", tt.name, diff)
				}
			}
		})
	}

	// tensor-parallel-size test
	podSpec_tests := []struct {
		name                       string
		modifyPodSpecArgs          func(args) args
		modifyWorkerPodSpecArgs    func(args) args
		modifyObjectMetaArgs       func(args) args
		modifyWorkerObjectMetaArgs func(args) args
		modifyExpected             func([]*appsv1.Deployment) []*appsv1.Deployment
		expectedErr                error
	}{
		{
			name: "Use the value of GPU in resources request of container",
			modifyPodSpecArgs: func(updatedArgs args) args {
				// Overwrite the environment variable
				for j, envVar := range updatedArgs.podSpec.Containers[0].Env {
					if envVar.Name == constants.RequestGPUCountEnvName {
						updatedArgs.podSpec.Containers[0].Env[j].Value = "5"
						break
					}
				}
				return updatedArgs
			},
			modifyWorkerPodSpecArgs: func(updatedArgs args) args {
				// Overwrite the environment variable
				for j, envVar := range updatedArgs.workerPodSpec.Containers[0].Env {
					if envVar.Name == constants.RequestGPUCountEnvName {
						updatedArgs.workerPodSpec.Containers[0].Env[j].Value = "5"
						break
					}
				}
				return updatedArgs
			},
			modifyObjectMetaArgs:       func(updatedArgs args) args { return updatedArgs },
			modifyWorkerObjectMetaArgs: func(updatedArgs args) args { return updatedArgs },
			modifyExpected: func(updatedExpected []*appsv1.Deployment) []*appsv1.Deployment {
				// Overwrite the environment variable
				for j, envVar := range updatedExpected[0].Spec.Template.Spec.Containers[0].Env {
					if envVar.Name == constants.RequestGPUCountEnvName {
						updatedExpected[0].Spec.Template.Spec.Containers[0].Env[j].Value = "5"
						continue
					}
				}
				for j, envVar := range updatedExpected[1].Spec.Template.Spec.Containers[0].Env {
					if envVar.Name == constants.RequestGPUCountEnvName {
						updatedExpected[1].Spec.Template.Spec.Containers[0].Env[j].Value = "5"
						break
					}
				}

				for _, deploy := range updatedExpected {
					deploy.Spec.Template.Spec.Containers[0].Resources = corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							constants.NvidiaGPUResourceType: resource.MustParse("5"),
						},
						Requests: corev1.ResourceList{
							constants.NvidiaGPUResourceType: resource.MustParse("5"),
						},
					}
				}

				return updatedExpected
			},
		},
		{
			name: "Use specified gpuResourceType if it is in gpuResourceTypeList",
			modifyPodSpecArgs: func(updatedArgs args) args {
				intelGPUResourceType := corev1.ResourceName(constants.IntelGPUResourceType)
				updatedArgs.podSpec.Containers[0].Resources.Requests = corev1.ResourceList{
					intelGPUResourceType: resource.MustParse("3"),
				}
				updatedArgs.podSpec.Containers[0].Resources.Limits = corev1.ResourceList{
					intelGPUResourceType: resource.MustParse("3"),
				}

				for j, envVar := range updatedArgs.podSpec.Containers[0].Env {
					if envVar.Name == constants.RequestGPUCountEnvName {
						updatedArgs.podSpec.Containers[0].Env[j].Value = "3"
						break
					}
				}
				return updatedArgs
			},
			modifyWorkerPodSpecArgs: func(updatedArgs args) args {
				for j, envVar := range updatedArgs.workerPodSpec.Containers[0].Env {
					if envVar.Name == constants.RequestGPUCountEnvName {
						updatedArgs.workerPodSpec.Containers[0].Env[j].Value = "3"
						break
					}
				}
				return updatedArgs
			},
			modifyObjectMetaArgs:       func(updatedArgs args) args { return updatedArgs },
			modifyWorkerObjectMetaArgs: func(updatedArgs args) args { return updatedArgs },
			modifyExpected: func(updatedExpected []*appsv1.Deployment) []*appsv1.Deployment {
				// Overwrite the environment variable
				for j, envVar := range updatedExpected[0].Spec.Template.Spec.Containers[0].Env {
					if envVar.Name == constants.RequestGPUCountEnvName {
						updatedExpected[0].Spec.Template.Spec.Containers[0].Env[j].Value = "3"
						continue
					}
				}
				for j, envVar := range updatedExpected[1].Spec.Template.Spec.Containers[0].Env {
					if envVar.Name == constants.RequestGPUCountEnvName {
						updatedExpected[1].Spec.Template.Spec.Containers[0].Env[j].Value = "3"
						break
					}
				}

				updatedExpected[0].Spec.Template.Spec.Containers[0].Resources = corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						constants.IntelGPUResourceType: resource.MustParse("3"),
					},
					Limits: corev1.ResourceList{
						constants.IntelGPUResourceType: resource.MustParse("3"),
					},
				}
				// worker node will use default gpuResourceType (NvidiaGPUResourceType)
				updatedExpected[1].Spec.Template.Spec.Containers[0].Resources = corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						constants.NvidiaGPUResourceType: resource.MustParse("3"),
					},
					Limits: corev1.ResourceList{
						constants.NvidiaGPUResourceType: resource.MustParse("3"),
					},
				}

				return updatedExpected
			},
		},
		{
			name: "Use a custom gpuResourceType specified in annotations, even when it is not listed in the default gpuResourceTypeList",
			modifyPodSpecArgs: func(updatedArgs args) args {
				updatedArgs.podSpec.Containers[0].Resources = corev1.ResourceRequirements{}
				updatedArgs.podSpec.Containers[0].Resources.Requests = corev1.ResourceList{
					"custom.com/gpu": resource.MustParse("3"),
				}
				updatedArgs.podSpec.Containers[0].Resources.Limits = corev1.ResourceList{
					"custom.com/gpu": resource.MustParse("3"),
				}

				for j, envVar := range updatedArgs.podSpec.Containers[0].Env {
					if envVar.Name == constants.RequestGPUCountEnvName {
						updatedArgs.podSpec.Containers[0].Env[j].Value = "3"
						break
					}
				}
				return updatedArgs
			},
			modifyWorkerPodSpecArgs: func(updatedArgs args) args {
				updatedArgs.workerPodSpec.Containers[0].Resources = corev1.ResourceRequirements{}
				updatedArgs.workerPodSpec.Containers[0].Resources.Requests = corev1.ResourceList{
					"custom.com/gpu": resource.MustParse("3"),
				}
				updatedArgs.workerPodSpec.Containers[0].Resources.Limits = corev1.ResourceList{
					"custom.com/gpu": resource.MustParse("3"),
				}

				for j, envVar := range updatedArgs.workerPodSpec.Containers[0].Env {
					if envVar.Name == constants.RequestGPUCountEnvName {
						updatedArgs.workerPodSpec.Containers[0].Env[j].Value = "3"
						break
					}
				}
				return updatedArgs
			},
			modifyObjectMetaArgs: func(updatedArgs args) args {
				updatedArgs.objectMeta.Annotations[constants.CustomGPUResourceTypesAnnotationKey] = "[\"custom.com/gpu\"]"
				return updatedArgs
			},
			modifyWorkerObjectMetaArgs: func(updatedArgs args) args {
				updatedArgs.workerObjectMeta.Annotations[constants.CustomGPUResourceTypesAnnotationKey] = "[\"custom.com/gpu\"]"
				return updatedArgs
			},
			modifyExpected: func(updatedExpected []*appsv1.Deployment) []*appsv1.Deployment {
				for _, deployment := range updatedExpected {
					deployment.Annotations[constants.CustomGPUResourceTypesAnnotationKey] = "[\"custom.com/gpu\"]"
					deployment.Spec.Template.Annotations[constants.CustomGPUResourceTypesAnnotationKey] = "[\"custom.com/gpu\"]"
					deployment.Spec.Template.Spec.Containers[0].Resources = corev1.ResourceRequirements{}
				}

				for j, envVar := range updatedExpected[0].Spec.Template.Spec.Containers[0].Env {
					if envVar.Name == constants.RequestGPUCountEnvName {
						updatedExpected[0].Spec.Template.Spec.Containers[0].Env[j].Value = "3"
						continue
					}
				}
				for j, envVar := range updatedExpected[1].Spec.Template.Spec.Containers[0].Env {
					if envVar.Name == constants.RequestGPUCountEnvName {
						updatedExpected[1].Spec.Template.Spec.Containers[0].Env[j].Value = "3"
						break
					}
				}
				updatedExpected[0].Spec.Template.Spec.Containers[0].Resources = corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						"custom.com/gpu": resource.MustParse("3"),
					},
					Limits: corev1.ResourceList{
						"custom.com/gpu": resource.MustParse("3"),
					},
				}
				updatedExpected[1].Spec.Template.Spec.Containers[0].Resources = corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						"custom.com/gpu": resource.MustParse("3"),
					},
					Limits: corev1.ResourceList{
						"custom.com/gpu": resource.MustParse("3"),
					},
				}

				return updatedExpected
			},
		},
		{
			name: "Allow multiple custom gpuResourceTypes from annotations, even when they are not listed in the default gpuResourceTypeList",
			modifyPodSpecArgs: func(updatedArgs args) args {
				updatedArgs.podSpec.Containers[0].Resources = corev1.ResourceRequirements{}
				updatedArgs.podSpec.Containers[0].Resources.Requests = corev1.ResourceList{
					"custom.com/gpu": resource.MustParse("3"),
				}
				updatedArgs.podSpec.Containers[0].Resources.Limits = corev1.ResourceList{
					"custom.com/gpu": resource.MustParse("3"),
				}

				for j, envVar := range updatedArgs.podSpec.Containers[0].Env {
					if envVar.Name == constants.RequestGPUCountEnvName {
						updatedArgs.podSpec.Containers[0].Env[j].Value = "3"
						break
					}
				}
				return updatedArgs
			},
			modifyWorkerPodSpecArgs: func(updatedArgs args) args {
				updatedArgs.workerPodSpec.Containers[0].Resources = corev1.ResourceRequirements{}
				updatedArgs.workerPodSpec.Containers[0].Resources.Requests = corev1.ResourceList{
					"custom.com/gpu2": resource.MustParse("3"),
				}
				updatedArgs.workerPodSpec.Containers[0].Resources.Limits = corev1.ResourceList{
					"custom.com/gpu2": resource.MustParse("3"),
				}

				for j, envVar := range updatedArgs.workerPodSpec.Containers[0].Env {
					if envVar.Name == constants.RequestGPUCountEnvName {
						updatedArgs.workerPodSpec.Containers[0].Env[j].Value = "3"
						break
					}
				}
				return updatedArgs
			},
			modifyObjectMetaArgs: func(updatedArgs args) args {
				updatedArgs.objectMeta.Annotations[constants.CustomGPUResourceTypesAnnotationKey] = "[\"custom.com/gpu\", \"custom.com/gpu2\"]"
				return updatedArgs
			},
			modifyWorkerObjectMetaArgs: func(updatedArgs args) args {
				updatedArgs.workerObjectMeta.Annotations[constants.CustomGPUResourceTypesAnnotationKey] = "[\"custom.com/gpu\", \"custom.com/gpu2\"]"
				return updatedArgs
			},
			modifyExpected: func(updatedExpected []*appsv1.Deployment) []*appsv1.Deployment {
				// Overwrite the environment variable

				for _, deployment := range updatedExpected {
					// serving.kserve.io/gpu-resource-types: '["gpu-type1", "gpu-type2", "gpu-type3"]'
					deployment.Annotations[constants.CustomGPUResourceTypesAnnotationKey] = "[\"custom.com/gpu\", \"custom.com/gpu2\"]"
					deployment.Spec.Template.Annotations[constants.CustomGPUResourceTypesAnnotationKey] = "[\"custom.com/gpu\", \"custom.com/gpu2\"]"
					deployment.Spec.Template.Spec.Containers[0].Resources = corev1.ResourceRequirements{}
				}

				for j, envVar := range updatedExpected[0].Spec.Template.Spec.Containers[0].Env {
					if envVar.Name == constants.RequestGPUCountEnvName {
						updatedExpected[0].Spec.Template.Spec.Containers[0].Env[j].Value = "3"
						continue
					}
				}
				for j, envVar := range updatedExpected[1].Spec.Template.Spec.Containers[0].Env {
					if envVar.Name == constants.RequestGPUCountEnvName {
						updatedExpected[1].Spec.Template.Spec.Containers[0].Env[j].Value = "3"
						break
					}
				}

				updatedExpected[0].Spec.Template.Spec.Containers[0].Resources = corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						"custom.com/gpu": resource.MustParse("3"),
					},
					Limits: corev1.ResourceList{
						"custom.com/gpu": resource.MustParse("3"),
					},
				}
				updatedExpected[1].Spec.Template.Spec.Containers[0].Resources = corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						"custom.com/gpu2": resource.MustParse("3"),
					},
					Limits: corev1.ResourceList{
						"custom.com/gpu2": resource.MustParse("3"),
					},
				}

				return updatedExpected
			},
		},
	}

	for _, tt := range podSpec_tests {
		t.Run(tt.name, func(t *testing.T) {
			// retrieve args, expected
			ttArgs := getDefaultArgs()
			ttExpected := getDefaultExpectedDeployment()

			// update objectMeta using modify func
			got, err := createRawDeployment(tt.modifyObjectMetaArgs(ttArgs).objectMeta, tt.modifyWorkerObjectMetaArgs(ttArgs).workerObjectMeta, ttArgs.componentExt, tt.modifyPodSpecArgs(ttArgs).podSpec, tt.modifyWorkerPodSpecArgs(ttArgs).workerPodSpec)
			assert.Equal(t, tt.expectedErr, err)

			if len(got) == 0 {
				t.Errorf("Got empty deployment")
			}

			// update expected value using modifyExpected func
			expected := tt.modifyExpected(ttExpected)

			for i, deploy := range got {
				if diff := cmp.Diff(expected[i], deploy, cmpopts.IgnoreFields(appsv1.Deployment{}, "Spec.Template.Spec.SecurityContext"),
					cmpopts.IgnoreFields(appsv1.Deployment{}, "Spec.Template.Spec.RestartPolicy"),
					cmpopts.IgnoreFields(appsv1.Deployment{}, "Spec.Template.Spec.TerminationGracePeriodSeconds"),
					cmpopts.IgnoreFields(appsv1.Deployment{}, "Spec.Template.Spec.DNSPolicy"),
					cmpopts.IgnoreFields(appsv1.Deployment{}, "Spec.Template.Spec.AutomountServiceAccountToken"),
					cmpopts.IgnoreFields(appsv1.Deployment{}, "Spec.Template.Spec.SchedulerName"),
					cmpopts.IgnoreFields(appsv1.Deployment{}, "Spec.RevisionHistoryLimit"),
					cmpopts.IgnoreFields(appsv1.Deployment{}, "Spec.ProgressDeadlineSeconds")); diff != "" {
					t.Errorf("Test %q unexpected deployment (-want +got): %v", tt.name, diff)
				}
			}
		})
	}
}

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
						constants.DeploymentMode:  string(constants.RawDeployment),
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
						constants.DeploymentMode:  string(constants.RawDeployment),
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
						constants.DeploymentMode:  string(constants.RawDeployment),
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
			deployments, _, err := createRawDeploymentODH(
				t.Context(),
				tt.args.client,
				tt.args.clientset,
				constants.InferenceServiceResource,
				tt.args.objectMeta,
				tt.args.workerObjectMeta,
				tt.args.componentExt,
				tt.args.podSpec,
				tt.args.workerPodSpec,
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

func TestCheckDeploymentExist(t *testing.T) {
	type fields struct {
		client kclient.Client
	}
	type args struct {
		deployment *appsv1.Deployment
		existing   *appsv1.Deployment
		getErr     error
	}
	tests := []struct {
		name         string
		args         args
		wantResult   constants.CheckResultType
		wantExisting *appsv1.Deployment
		wantErr      bool
	}{
		{
			name: "deployment not found returns CheckResultCreate",
			args: args{
				deployment: &appsv1.Deployment{
					ObjectMeta: metav1.ObjectMeta{Name: "foo", Namespace: "bar"},
				},
				getErr: errors.NewNotFound(appsv1.Resource("deployment"), "foo"),
			},
			wantResult:   constants.CheckResultCreate,
			wantExisting: nil,
			wantErr:      false,
		},
		{
			name: "get error returns CheckResultUnknown",
			args: args{
				deployment: &appsv1.Deployment{
					ObjectMeta: metav1.ObjectMeta{Name: "foo", Namespace: "bar"},
				},
				getErr: fmt.Errorf("some error"), //nolint
			},
			wantResult:   constants.CheckResultUnknown,
			wantExisting: nil,
			wantErr:      true,
		},
		{
			name: "deployment exists and is equivalent returns CheckResultExisted",
			args: args{
				deployment: &appsv1.Deployment{
					ObjectMeta: metav1.ObjectMeta{Name: "foo", Namespace: "bar"},
					Spec: appsv1.DeploymentSpec{
						Selector: &metav1.LabelSelector{
							MatchLabels: map[string]string{"app": "foo"},
						},
						Template: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "foo"}},
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{
									{Name: "c", Image: "img"},
								},
							},
						},
					},
				},
				existing: &appsv1.Deployment{
					ObjectMeta: metav1.ObjectMeta{Name: "foo", Namespace: "bar"},
					Spec: appsv1.DeploymentSpec{
						Selector: &metav1.LabelSelector{
							MatchLabels: map[string]string{"app": "foo"},
						},
						Template: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "foo"}},
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{
									{Name: "c", Image: "img"},
								},
							},
						},
					},
				},
				getErr: nil,
			},
			wantResult:   constants.CheckResultExisted,
			wantExisting: &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "foo", Namespace: "bar"}},
			wantErr:      false,
		},
		{
			name: "deployment exists and is different returns CheckResultUpdate",
			args: args{
				deployment: &appsv1.Deployment{
					ObjectMeta: metav1.ObjectMeta{Name: "foo", Namespace: "bar"},
					Spec: appsv1.DeploymentSpec{
						Selector: &metav1.LabelSelector{
							MatchLabels: map[string]string{"app": "foo"},
						},
						Template: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "foo"}},
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{
									{Name: "c", Image: "img1"},
								},
							},
						},
					},
				},
				existing: &appsv1.Deployment{
					ObjectMeta: metav1.ObjectMeta{Name: "foo", Namespace: "bar"},
					Spec: appsv1.DeploymentSpec{
						Selector: &metav1.LabelSelector{
							MatchLabels: map[string]string{"app": "foo"},
						},
						Template: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "foo"}},
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{
									{Name: "c", Image: "img2"},
								},
							},
						},
					},
				},
				getErr: nil,
			},
			wantResult:   constants.CheckResultUpdate,
			wantExisting: &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "foo", Namespace: "bar"}},
			wantErr:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := &mockClientForCheckDeploymentExist{
				getDeployment: tt.args.existing,
				getErr:        tt.args.getErr,
			}
			r := &DeploymentReconciler{
				client: mockClient,
			}
			ctx := t.Context()
			gotResult, gotExisting, err := r.checkDeploymentExist(ctx, mockClient, tt.args.deployment)
			if (err != nil) != tt.wantErr {
				t.Errorf("checkDeploymentExist() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if gotResult != tt.wantResult {
				t.Errorf("checkDeploymentExist() gotResult = %v, want %v", gotResult, tt.wantResult)
			}
			// Only check name/namespace for gotExisting
			if tt.wantExisting != nil && gotExisting != nil {
				if gotExisting.Name != tt.args.deployment.Name || gotExisting.Namespace != tt.args.deployment.Namespace {
					t.Errorf("checkDeploymentExist() gotExisting = %v, want %v", gotExisting, tt.wantExisting)
				}
			}
			if tt.wantExisting == nil && gotExisting != nil {
				t.Errorf("checkDeploymentExist() gotExisting = %v, want nil", gotExisting)
			}
		})
	}
}

func TestNewDeploymentReconciler(t *testing.T) {
	type fields struct {
		client       kclient.Client
		clientset    kubernetes.Interface
		resourceType constants.ResourceType
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
				client: &mockClientForOauthProxyDetection{deploymentNotFound: true},
				scheme: nil,
				objectMeta: metav1.ObjectMeta{
					Name:      "test-predictor",
					Namespace: "test-ns",
					Labels: map[string]string{
						constants.DeploymentMode:  string(constants.RawDeployment),
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
				client: &mockClientForOauthProxyDetection{deploymentNotFound: true},
				scheme: nil,
				objectMeta: metav1.ObjectMeta{
					Name:      "test-predictor",
					Namespace: "test-ns",
					Labels: map[string]string{
						constants.DeploymentMode:  string(constants.RawDeployment),
						constants.AutoscalerClass: string(constants.AutoscalerClassNone),
					},
					Annotations: map[string]string{},
				},
				workerMeta: metav1.ObjectMeta{
					Name:      "worker-predictor",
					Namespace: "test-ns",
					Labels: map[string]string{
						constants.DeploymentMode:  string(constants.RawDeployment),
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
				tt.fields.clientset,
				tt.fields.scheme,
				tt.fields.resourceType,
				tt.fields.objectMeta,
				tt.fields.workerMeta,
				tt.fields.componentExt,
				tt.fields.podSpec,
				tt.fields.workerPod,
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

// mockClientForCheckDeploymentExist is a minimal mock for kclient.Client for checkDeploymentExist
type mockClientForCheckDeploymentExist struct {
	kclient.Client
	getDeployment *appsv1.Deployment
	getErr        error
}

func (m *mockClientForCheckDeploymentExist) Get(ctx context.Context, key kclient.ObjectKey, obj kclient.Object, opts ...kclient.GetOption) error {
	if m.getErr != nil {
		return m.getErr
	}

	// Handle different object types
	switch o := obj.(type) {
	case *appsv1.Deployment:
		if m.getDeployment != nil {
			*o = *m.getDeployment.DeepCopy()
		}
	case *v1beta1.InferenceService:
		// For InferenceService, create a minimal mock object with required fields
		o.ObjectMeta = metav1.ObjectMeta{
			Name:      key.Name,
			Namespace: key.Namespace,
			UID:       "test-uid-12345",
		}
		o.TypeMeta = metav1.TypeMeta{
			APIVersion: "serving.kserve.io/v1beta1",
			Kind:       "InferenceService",
		}
	}
	return nil
}

func (m *mockClientForCheckDeploymentExist) Update(ctx context.Context, obj kclient.Object, opts ...kclient.UpdateOption) error {
	// Simulate dry-run update always succeeds
	return nil
}

func intStrPtr(s string) *intstr.IntOrString {
	v := intstr.FromString(s)
	return &v
}

func int32Ptr(i int32) *int32 {
	val := i
	return &val
}

func BoolPtr(b bool) *bool {
	val := b
	return &val
}

// Function to add a new environment variable to a specific container in the DeploymentSpec
func addEnvVarToDeploymentSpec(deploymentSpec *appsv1.DeploymentSpec, containerName, envName, envValue string) {
	// Iterate over the containers in the PodTemplateSpec to find the specified container
	for i, container := range deploymentSpec.Template.Spec.Containers {
		if container.Name == containerName {
			if _, exists := utils.GetEnvVarValue(container.Env, envName); exists {
				// Overwrite the environment variable
				for j, envVar := range container.Env {
					if envVar.Name == envName {
						deploymentSpec.Template.Spec.Containers[i].Env[j].Value = envValue
						break
					}
				}
			} else {
				// Add the new environment variable to the Env field if it does not exist
				container.Env = append(container.Env, corev1.EnvVar{
					Name:  envName,
					Value: envValue,
				})
				deploymentSpec.Template.Spec.Containers[i].Env = container.Env
			}
		}
	}
}

// deepCopyDeploymentList creates a deep copy of a slice of Deployment pointers.
// This ensures that modifications to the original slice or its elements do not affect the copied slice.
func deepCopyDeploymentList(src []*appsv1.Deployment) []*appsv1.Deployment {
	if src == nil {
		return nil
	}
	copied := make([]*appsv1.Deployment, len(src))
	for i, deployment := range src {
		if deployment != nil {
			copied[i] = deployment.DeepCopy()
		}
	}
	return copied
}

// mockClientForOauthProxyDetection is a mock client for testing oauth-proxy container detection
type mockClientForOauthProxyDetection struct {
	kclient.Client
	existingDeployment *appsv1.Deployment
	deploymentNotFound bool // Only affects deployment lookups, not InferenceService
}

func (m *mockClientForOauthProxyDetection) Get(ctx context.Context, key kclient.ObjectKey, obj kclient.Object, opts ...kclient.GetOption) error {
	switch o := obj.(type) {
	case *appsv1.Deployment:
		if m.deploymentNotFound {
			return errors.NewNotFound(appsv1.Resource("deployments"), key.Name)
		}
		if m.existingDeployment != nil {
			*o = *m.existingDeployment.DeepCopy()
		}
	case *v1beta1.InferenceService:
		// Return a minimal mock InferenceService for SAR ConfigMap creation
		o.ObjectMeta = metav1.ObjectMeta{
			Name:      key.Name,
			Namespace: key.Namespace,
			UID:       "test-uid-12345",
		}
	}
	return nil
}

func (m *mockClientForOauthProxyDetection) Update(ctx context.Context, obj kclient.Object, opts ...kclient.UpdateOption) error {
	return nil
}

func (m *mockClientForOauthProxyDetection) Create(ctx context.Context, obj kclient.Object, opts ...kclient.CreateOption) error {
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
			expectErr:          false,
		},
		{
			name: "deployment with oauth-proxy container returns oauth-proxy and image",
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
			expectErr:     false,
		},
		{
			name: "deployment with kube-rbac-proxy container returns kube-rbac-proxy and image",
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
			expectErr:     false,
		},
		{
			name: "deployment without any auth proxy returns empty string",
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
			expectErr:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &mockClientForOauthProxyDetection{
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
		expectKubeRbacProxy       bool   // Whether kube-rbac-proxy container should be present
		expectOauthProxyPreserved bool   // Whether oauth-proxy is preserved (kube-rbac-proxy NOT added)
		expectedProxyImage        string // Expected image of kube-rbac-proxy (empty = don't check)
	}{
		{
			name:               "new ISVC with auth enabled gets kube-rbac-proxy",
			deploymentNotFound: true,
			annotations: map[string]string{
				constants.ODHKserveRawAuth: "true",
			},
			expectKubeRbacProxy:       true,
			expectOauthProxyPreserved: false,
			expectedProxyImage:        constants.OauthProxyImage, // Newly generated uses config image
		},
		{
			name: "existing ISVC with oauth-proxy is preserved (no kube-rbac-proxy added)",
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
			expectedProxyImage:        "", // No kube-rbac-proxy expected
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
			expectedProxyImage:        constants.OauthProxyImage, // Migrated = newly generated
		},
		{
			name: "existing ISVC with kube-rbac-proxy matching config image regenerates normally",
			existingDeployment: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{Name: constants.InferenceServiceContainerName},
								{Name: constants.KubeRbacContainerName, Image: constants.OauthProxyImage}, // Same as config
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
			expectedProxyImage:        constants.OauthProxyImage, // Regenerated with config image
		},
		{
			name: "existing ISVC with kube-rbac-proxy different image is preserved",
			existingDeployment: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{Name: constants.InferenceServiceContainerName},
								{Name: constants.KubeRbacContainerName, Image: "quay.io/different/image:v1.0.0"}, // Different from config
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
			expectedProxyImage:        "quay.io/different/image:v1.0.0", // Preserved = keeps original image
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &mockClientForOauthProxyDetection{
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
				"kube-rbac-proxy presence mismatch: expected %v, got %v", tt.expectKubeRbacProxy, hasKubeRbacProxy)

			if tt.expectOauthProxyPreserved {
				// When oauth-proxy is preserved, the new deployment should NOT have kube-rbac-proxy
				assert.False(t, hasKubeRbacProxy, "oauth-proxy should be preserved, kube-rbac-proxy should not be added")
			}

			// Verify the container image if expected
			if tt.expectedProxyImage != "" && kubeRbacProxyContainer != nil {
				assert.Equal(t, tt.expectedProxyImage, kubeRbacProxyContainer.Image,
					"kube-rbac-proxy image mismatch")
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

	existingDeployment := &appsv1.Deployment{
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					AutomountServiceAccountToken: ptr.To(true),
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

	desiredDeployment := &appsv1.Deployment{
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					AutomountServiceAccountToken: ptr.To(false),
					Containers: []corev1.Container{
						{
							Name:  constants.InferenceServiceContainerName,
							Image: "test-image",
						},
					},
				},
			},
		},
	}

	copyAuthProxyFromExisting(existingDeployment, desiredDeployment, constants.KubeRbacContainerName)

	// Verify container was copied
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

	// Verify volumes were copied
	assert.Len(t, desiredDeployment.Spec.Template.Spec.Volumes, 2)

	// Verify AutomountServiceAccountToken is true
	require.NotNil(t, desiredDeployment.Spec.Template.Spec.AutomountServiceAccountToken)
	assert.True(t, *desiredDeployment.Spec.Template.Spec.AutomountServiceAccountToken)

	// Verify kserve-container has proxy-tls volume mount
	var kserveContainer *corev1.Container
	for i, c := range desiredDeployment.Spec.Template.Spec.Containers {
		if c.Name == constants.InferenceServiceContainerName {
			kserveContainer = &desiredDeployment.Spec.Template.Spec.Containers[i]
			break
		}
	}
	require.NotNil(t, kserveContainer)
	hasProxyTlsMount := false
	for _, vm := range kserveContainer.VolumeMounts {
		if vm.Name == "proxy-tls" {
			hasProxyTlsMount = true
			break
		}
	}
	assert.True(t, hasProxyTlsMount, "kserve-container should have proxy-tls mount")
}

func TestAuthProxyPreservationCopiesContainerToDesired(t *testing.T) {
	oauthProxyConfig := fmt.Sprintf(`{"image": "%s", "memoryRequest": "%s", "memoryLimit": "%s", "cpuRequest": "%s", "cpuLimit": "%s"}`,
		constants.OauthProxyImage,
		constants.OauthProxyResourceMemoryRequest,
		constants.OauthProxyResourceMemoryLimit,
		constants.OauthProxyResourceCPURequest,
		constants.OauthProxyResourceCPULimit,
	)

	existingKubeRbacProxyContainer := corev1.Container{
		Name:  constants.KubeRbacContainerName,
		Image: "quay.io/opendatahub/odh-kube-auth-proxy@sha256:originalimage",
		Args: []string{
			"--secure-listen-address=:8443",
			"--upstream=http://localhost:8080",
		},
		Ports: []corev1.ContainerPort{
			{Name: "https", ContainerPort: 8443},
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: "proxy-tls", MountPath: "/etc/tls/private"},
			{Name: "test-isvc-kube-rbac-proxy-sar-config", MountPath: "/etc/kube-rbac-proxy", ReadOnly: true},
		},
	}

	existingVolumes := []corev1.Volume{
		{
			Name: "proxy-tls",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{SecretName: "test-predictor-serving-cert"},
			},
		},
		{
			Name: "test-isvc-kube-rbac-proxy-sar-config",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: "test-isvc-kube-rbac-proxy-sar-config"},
				},
			},
		},
	}

	existingDeployment := &appsv1.Deployment{
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					AutomountServiceAccountToken: ptr.To(true),
					Containers: []corev1.Container{
						{
							Name:  constants.InferenceServiceContainerName,
							Image: "test-image",
							VolumeMounts: []corev1.VolumeMount{
								{Name: "proxy-tls", MountPath: "/etc/tls/private"},
							},
						},
						existingKubeRbacProxyContainer,
					},
					Volumes: existingVolumes,
				},
			},
		},
	}

	client := &mockClientForOauthProxyDetection{
		existingDeployment: existingDeployment,
		deploymentNotFound: false,
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
		Name:      "test-predictor",
		Namespace: "test-ns",
		Annotations: map[string]string{
			constants.ODHKserveRawAuth: "true",
		},
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

	deploymentList, authProxyPreserved, err := createRawDeploymentODH(
		t.Context(),
		client,
		clientset,
		constants.InferenceServiceResource,
		objectMeta,
		metav1.ObjectMeta{},
		&v1beta1.ComponentExtensionSpec{},
		podSpec,
		nil,
	)

	require.NoError(t, err)
	require.Len(t, deploymentList, 1)
	assert.True(t, authProxyPreserved, "authProxyPreserved should be true")

	deployment := deploymentList[0]

	// Verify the kube-rbac-proxy container was copied to the desired deployment
	var foundKubeRbacProxy *corev1.Container
	for i, container := range deployment.Spec.Template.Spec.Containers {
		if container.Name == constants.KubeRbacContainerName {
			foundKubeRbacProxy = &deployment.Spec.Template.Spec.Containers[i]
			break
		}
	}
	require.NotNil(t, foundKubeRbacProxy, "kube-rbac-proxy container should be present in desired deployment")
	assert.Equal(t, existingKubeRbacProxyContainer.Image, foundKubeRbacProxy.Image,
		"preserved container should have original image")
	assert.Equal(t, existingKubeRbacProxyContainer.Args, foundKubeRbacProxy.Args,
		"preserved container should have original args")

	// Verify volumes were copied
	assert.Len(t, deployment.Spec.Template.Spec.Volumes, 2, "should have 2 volumes")

	// Verify AutomountServiceAccountToken is set
	require.NotNil(t, deployment.Spec.Template.Spec.AutomountServiceAccountToken)
	assert.True(t, *deployment.Spec.Template.Spec.AutomountServiceAccountToken,
		"AutomountServiceAccountToken should be true for preserved auth proxy")

	// Verify kserve-container has the proxy-tls volume mount
	var kserveContainer *corev1.Container
	for i, container := range deployment.Spec.Template.Spec.Containers {
		if container.Name == constants.InferenceServiceContainerName {
			kserveContainer = &deployment.Spec.Template.Spec.Containers[i]
			break
		}
	}
	require.NotNil(t, kserveContainer)
	hasProxyTlsMount := false
	for _, vm := range kserveContainer.VolumeMounts {
		if vm.Name == "proxy-tls" {
			hasProxyTlsMount = true
			break
		}
	}
	assert.True(t, hasProxyTlsMount, "kserve-container should have proxy-tls volume mount")
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
			name: "existing ISVC with kube-rbac-proxy matching config image does NOT set condition",
			existingDeployment: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{Name: constants.InferenceServiceContainerName},
								{Name: constants.KubeRbacContainerName, Image: constants.OauthProxyImage}, // Same as config = regenerate
							},
						},
					},
				},
			},
			annotations: map[string]string{
				constants.ODHKserveRawAuth: "true",
			},
			expectCondition: false, // Not preserved - regenerated normally
		},
		{
			name: "existing ISVC with kube-rbac-proxy different image sets AuthProxyPreserved condition",
			existingDeployment: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{Name: constants.InferenceServiceContainerName},
								{Name: constants.KubeRbacContainerName, Image: "quay.io/different/image:v1.0.0"}, // Different = preserve
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
			client := &mockClientForOauthProxyDetection{
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
			)

			require.NoError(t, err)
			require.NotNil(t, reconciler)

			if tt.expectCondition {
				require.NotNil(t, reconciler.Condition, "expected Condition to be set")
				assert.Equal(t, tt.expectedReason, reconciler.Condition.Reason)
				assert.Equal(t, corev1.ConditionFalse, reconciler.Condition.Status)
				assert.Equal(t, v1beta1.LatestDeploymentReady, reconciler.ConditionType)
			} else {
				assert.Nil(t, reconciler.Condition, "expected Condition to be nil")
			}
		})
	}
}
