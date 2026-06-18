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

package pod

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"knative.dev/pkg/kmp"

	"github.com/kserve/kserve/pkg/constants"
)

func TestOVMSAutoVersioning(t *testing.T) {
	injector := &StorageInitializerInjector{}

	ovmsContainer := corev1.Container{
		Name:    constants.OVMSVersioningContainerName,
		Image:   OVMSVersioningDefaultImage,
		Command: []string{"/bin/sh"},
		Args: []string{
			"-c",
			`MODEL_DIR="/mnt/models"
VERSION="1"
VERSIONED_DIR="${MODEL_DIR}/${VERSION}"

if [ ! -d "${MODEL_DIR}" ] || [ -z "$(ls -A "${MODEL_DIR}" 2>/dev/null)" ]; then
  exit 0
fi

if [ -d "${VERSIONED_DIR}" ]; then
  exit 0
fi

mkdir -p "${VERSIONED_DIR}"

# Move regular files/dirs and hidden entries (dotfiles) - plain glob misses the latter.
for f in "${MODEL_DIR}"/* "${MODEL_DIR}"/.[!.]* "${MODEL_DIR}"/..?*; do
  [ -e "$f" ] && mv "$f" "${VERSIONED_DIR}/"
done
`,
		},
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      constants.StorageInitializerVolumeName,
				MountPath: constants.DefaultModelLocalMountPath,
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
	}

	scenarios := map[string]struct {
		original *corev1.Pod
		expected *corev1.Pod
	}{
		"annotation absent - no versioning container injected": {
			original: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: constants.InferenceServiceContainerName}},
				},
			},
			expected: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: constants.InferenceServiceContainerName}},
				},
			},
		},
		"annotation present - versioning container appended": {
			original: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						constants.OVMSAutoVersioningAnnotationKey: "1",
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: constants.InferenceServiceContainerName}},
				},
			},
			expected: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						constants.OVMSAutoVersioningAnnotationKey: "1",
					},
				},
				Spec: corev1.PodSpec{
					Containers:     []corev1.Container{{Name: constants.InferenceServiceContainerName}},
					InitContainers: []corev1.Container{ovmsContainer},
				},
			},
		},
	}

	for name, scenario := range scenarios {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, injector.injectOVMSAutoVersioning(scenario.original))
			if diff, _ := kmp.SafeDiff(scenario.expected.Spec, scenario.original.Spec); diff != "" {
				t.Errorf("unexpected pod spec (-want +got):\n%v", diff)
			}
		})
	}
}

func TestOVMSAutoVersioningInvalidAnnotationValues(t *testing.T) {
	injector := &StorageInitializerInjector{}

	cases := map[string]struct {
		value       string
		expectError bool
	}{
		"not a number": {value: "invalid", expectError: true},
		"zero":         {value: "0", expectError: true},
		"negative":     {value: "-1", expectError: true},
		"version 1":    {value: "1", expectError: false},
		"version 10":   {value: "10", expectError: false},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						constants.OVMSAutoVersioningAnnotationKey: tc.value,
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: constants.InferenceServiceContainerName}},
				},
			}
			err := injector.injectOVMSAutoVersioning(pod)
			if tc.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestOVMSAutoVersioningIdempotent(t *testing.T) {
	injector := &StorageInitializerInjector{}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				constants.OVMSAutoVersioningAnnotationKey: "1",
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: constants.InferenceServiceContainerName}},
		},
	}

	require.NoError(t, injector.injectOVMSAutoVersioning(pod), "first injection")
	countAfterFirst := len(pod.Spec.InitContainers)

	require.NoError(t, injector.injectOVMSAutoVersioning(pod), "second injection")
	assert.Equal(t, countAfterFirst, len(pod.Spec.InitContainers), "init container count should not change on second injection")

	var ovmsCount int
	for _, c := range pod.Spec.InitContainers {
		if c.Name == constants.OVMSVersioningContainerName {
			ovmsCount++
		}
	}
	assert.Equal(t, 1, ovmsCount, "expected exactly one OVMS versioning container")
}
