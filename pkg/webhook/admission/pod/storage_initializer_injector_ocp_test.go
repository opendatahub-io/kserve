//go:build distro

/*
Copyright 2021 The KServe Authors.

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

package pod

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"knative.dev/pkg/kmp"

	"github.com/kserve/kserve/pkg/constants"
	"github.com/kserve/kserve/pkg/credentials"
)

func TestOVMSAutoVersioning(t *testing.T) {
	scenarios := map[string]struct {
		original *corev1.Pod
		expected *corev1.Pod
	}{
		"OVMS auto-versioning annotation not present": {
			original: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						constants.StorageInitializerSourceUriInternalAnnotationKey: "gs://foo",
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name: constants.InferenceServiceContainerName,
						},
					},
				},
			},
			expected: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						constants.StorageInitializerSourceUriInternalAnnotationKey: "gs://foo",
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name: constants.InferenceServiceContainerName,
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      constants.StorageInitializerVolumeName,
									MountPath: constants.DefaultModelLocalMountPath,
									ReadOnly:  true,
								},
							},
						},
					},
					InitContainers: []corev1.Container{
						{
							Name:  constants.StorageInitializerContainerName,
							Image: constants.StorageInitializerContainerImage + ":" + constants.StorageInitializerContainerImageVersion,
							Args:  []string{"gs://foo", constants.DefaultModelLocalMountPath},
							Env: []corev1.EnvVar{
								{Name: "HF_HUB_ENABLE_HF_TRANSFER", Value: "1"},
								{Name: "HF_XET_HIGH_PERFORMANCE", Value: "1"},
								{Name: "HF_XET_NUM_CONCURRENT_RANGE_GETS", Value: "8"},
							},
							Resources:                resourceRequirement,
							TerminationMessagePolicy: "FallbackToLogsOnError",
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      constants.StorageInitializerVolumeName,
									MountPath: constants.DefaultModelLocalMountPath,
								},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: constants.StorageInitializerVolumeName,
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
							},
						},
					},
				},
			},
		},
		"OVMS auto-versioning annotation present with valid version": {
			original: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						constants.StorageInitializerSourceUriInternalAnnotationKey: "gs://foo",
						constants.OVMSAutoVersioningAnnotationKey:                  "1",
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name: constants.InferenceServiceContainerName,
						},
					},
				},
			},
			expected: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						constants.StorageInitializerSourceUriInternalAnnotationKey: "gs://foo",
						constants.OVMSAutoVersioningAnnotationKey:                  "1",
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name: constants.InferenceServiceContainerName,
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      constants.StorageInitializerVolumeName,
									MountPath: constants.DefaultModelLocalMountPath,
									ReadOnly:  true,
								},
							},
						},
					},
					InitContainers: []corev1.Container{
						{
							Name:      constants.StorageInitializerContainerName,
							Image:     constants.StorageInitializerContainerImage + ":" + constants.StorageInitializerContainerImageVersion,
							Args:      []string{"gs://foo", constants.DefaultModelLocalMountPath},
							Resources: resourceRequirement,
							Env: []corev1.EnvVar{
								{Name: "HF_HUB_ENABLE_HF_TRANSFER", Value: "1"},
								{Name: "HF_XET_HIGH_PERFORMANCE", Value: "1"},
								{Name: "HF_XET_NUM_CONCURRENT_RANGE_GETS", Value: "8"},
							},
							TerminationMessagePolicy: "FallbackToLogsOnError",
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      constants.StorageInitializerVolumeName,
									MountPath: constants.DefaultModelLocalMountPath,
								},
							},
						},
						{
							Name:    constants.OVMSVersioningContainerName,
							Image:   "registry.redhat.io/ubi9/ubi-micro:latest",
							Command: []string{"/bin/sh"},
							Args: []string{
								"-c",
								`
# OVMS Auto-Versioning Script
# This script reorganizes model files to match OVMS expected directory structure

MODEL_DIR="/mnt/models"
VERSION="1"
VERSIONED_DIR="${MODEL_DIR}/${VERSION}"

echo "Starting OVMS auto-versioning: organizing models for version ${VERSION}"

# Check if model directory exists and has content
if [ ! -d "${MODEL_DIR}" ] || [ -z "$(ls -A ${MODEL_DIR} 2>/dev/null)" ]; then
  echo "No models found in ${MODEL_DIR}, skipping versioning"
  exit 0
fi

# Check if versioned directory already exists
if [ -d "${VERSIONED_DIR}" ]; then
  echo "Version directory ${VERSIONED_DIR} already exists, skipping reorganization"
  exit 0
fi

echo "Creating versioned directory: ${VERSIONED_DIR}"
mkdir -p "${VERSIONED_DIR}"

# Move all files and directories to the versioned directory by ignoring itself
mv "${MODEL_DIR}"/* "${VERSIONED_DIR}/" 2>/dev/null || true

echo "Successfully organized models into versioned directory structure"
echo "Models are now available at: ${VERSIONED_DIR}"
echo "Running ls -la ${VERSIONED_DIR}"
ls -la ${VERSIONED_DIR}
`,
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      constants.StorageInitializerVolumeName,
									MountPath: constants.DefaultModelLocalMountPath,
									ReadOnly:  false,
								},
							},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("50m"),
									corev1.ResourceMemory: resource.MustParse("64Mi"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("100m"),
									corev1.ResourceMemory: resource.MustParse("128Mi"),
								},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: constants.StorageInitializerVolumeName,
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
							},
						},
					},
				},
			},
		},
	}

	for name, scenario := range scenarios {
		injector := &StorageInitializerInjector{
			credentialBuilder: credentials.NewCredentialBuilder(c, clientset, &corev1.ConfigMap{
				Data: map[string]string{},
			}),
			config: storageInitializerConfig,
			client: c,
		}
		if err := injector.InjectStorageInitializer(t.Context(), scenario.original); err != nil {
			t.Errorf("Test %q unexpected result: %s", name, err)
		}
		if diff, _ := kmp.SafeDiff(scenario.expected.Spec, scenario.original.Spec); diff != "" {
			t.Errorf("Test %q unexpected result (-want +got): %v", name, diff)
		}
	}
}

func TestOVMSAutoVersioningInvalidValues(t *testing.T) {
	scenarios := []struct {
		name            string
		annotationValue string
		expectError     bool
	}{
		{
			name:            "invalid value - not a number",
			annotationValue: "invalid",
			expectError:     true,
		},
		{
			name:            "invalid value - zero",
			annotationValue: "0",
			expectError:     true,
		},
		{
			name:            "invalid value - negative",
			annotationValue: "-1",
			expectError:     true,
		},
		{
			name:            "valid value - positive integer",
			annotationValue: "1",
			expectError:     false,
		},
		{
			name:            "valid value - larger positive integer",
			annotationValue: "10",
			expectError:     false,
		},
	}

	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						constants.StorageInitializerSourceUriInternalAnnotationKey: "gs://foo",
						constants.OVMSAutoVersioningAnnotationKey:                  scenario.annotationValue,
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name: constants.InferenceServiceContainerName,
						},
					},
				},
			}

			injector := &StorageInitializerInjector{
				credentialBuilder: credentials.NewCredentialBuilder(c, clientset, &corev1.ConfigMap{
					Data: map[string]string{},
				}),
				config: storageInitializerConfig,
				client: c,
			}

			err := injector.InjectStorageInitializer(t.Context(), pod)
			if scenario.expectError && err == nil {
				t.Errorf("Expected error but got none")
			}
			if !scenario.expectError && err != nil {
				t.Errorf("Expected no error but got: %v", err)
			}
		})
	}
}

func TestOVMSAutoVersioningIdempotency(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				constants.StorageInitializerSourceUriInternalAnnotationKey: "gs://foo",
				constants.OVMSAutoVersioningAnnotationKey:                  "1",
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name: constants.InferenceServiceContainerName,
				},
			},
		},
	}

	injector := &StorageInitializerInjector{
		credentialBuilder: credentials.NewCredentialBuilder(c, clientset, &corev1.ConfigMap{
			Data: map[string]string{},
		}),
		config: storageInitializerConfig,
		client: c,
	}

	// First injection
	err := injector.InjectStorageInitializer(t.Context(), pod)
	if err != nil {
		t.Fatalf("First injection failed: %v", err)
	}

	// Count init containers after first injection
	firstInjectionCount := len(pod.Spec.InitContainers)

	// Second injection should be idempotent
	err = injector.InjectStorageInitializer(t.Context(), pod)
	if err != nil {
		t.Fatalf("Second injection failed: %v", err)
	}

	// Count should be the same
	if len(pod.Spec.InitContainers) != firstInjectionCount {
		t.Errorf("Expected %d init containers after second injection, got %d", firstInjectionCount, len(pod.Spec.InitContainers))
	}

	// Verify OVMS versioning container exists and is present only once
	ovmsContainerCount := 0
	for _, container := range pod.Spec.InitContainers {
		if container.Name == constants.OVMSVersioningContainerName {
			ovmsContainerCount++
		}
	}

	if ovmsContainerCount != 1 {
		t.Errorf("Expected exactly 1 OVMS versioning container, got %d", ovmsContainerCount)
	}
}
