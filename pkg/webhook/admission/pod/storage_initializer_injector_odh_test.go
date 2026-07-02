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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kserve/kserve/pkg/constants"
	kserveTypes "github.com/kserve/kserve/pkg/types"
	"github.com/kserve/kserve/pkg/utils"
)

func TestInjectImageVolume(t *testing.T) {
	// Test when annotation key is not set
	{
		pod := &corev1.Pod{}
		mi := &StorageInitializerInjector{}
		err := mi.InjectImageVolume(pod)
		if err != nil {
			t.Errorf("Expected nil error but got %v", err)
		}
		if len(pod.Spec.Containers) != 0 {
			t.Errorf("Expected no containers but got %d", len(pod.Spec.Containers))
		}
	}

	// Test when srcURI does not start with OciURIPrefix
	{
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					constants.StorageInitializerSourceUriInternalAnnotationKey: "s3://bucket/model",
				},
			},
		}
		mi := &StorageInitializerInjector{}
		err := mi.InjectImageVolume(pod)
		if err != nil {
			t.Errorf("Expected nil error but got %v", err)
		}
		if len(pod.Spec.Containers) != 0 {
			t.Errorf("Expected no containers but got %d", len(pod.Spec.Containers))
		}
	}

	// Test when srcURI starts with OciURIPrefix
	{
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					constants.StorageInitializerSourceUriInternalAnnotationKey: constants.OciURIPrefix + "ghcr.io/org/model:v1",
				},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{Name: constants.InferenceServiceContainerName},
				},
			},
		}
		mi := &StorageInitializerInjector{
			config: &kserveTypes.StorageInitializerConfig{},
		}
		err := mi.InjectImageVolume(pod)
		if err != nil {
			t.Errorf("Expected nil error but got %v", err)
		}

		// Check that an image volume has been attached
		if len(pod.Spec.Volumes) != 1 || pod.Spec.Volumes[0].Name != constants.StorageInitializerVolumeName {
			t.Errorf("Expected one volume with name %s, but got %v", constants.StorageInitializerVolumeName, pod.Spec.Volumes)
		}

		// Verify it's an image volume, not emptyDir
		if pod.Spec.Volumes[0].Image == nil {
			t.Error("Expected image volume but got nil")
		} else {
			if pod.Spec.Volumes[0].Image.Reference != "ghcr.io/org/model:v1" {
				t.Errorf("Expected image reference ghcr.io/org/model:v1 but got %s", pod.Spec.Volumes[0].Image.Reference)
			}
			if pod.Spec.Volumes[0].Image.PullPolicy != corev1.PullIfNotPresent {
				t.Errorf("Expected PullIfNotPresent but got %v", pod.Spec.Volumes[0].Image.PullPolicy)
			}
		}

		// Check that NO sidecar container was injected (unlike modelcar)
		if len(pod.Spec.Containers) != 1 {
			t.Errorf("Expected one container (user container only, no sidecar) but got %d", len(pod.Spec.Containers))
		}

		// Check that NO init container was injected (unlike modelcar)
		if len(pod.Spec.InitContainers) != 0 {
			t.Errorf("Expected zero init containers but got %d", len(pod.Spec.InitContainers))
		}

		// Check volume mount in user container
		if len(pod.Spec.Containers[0].VolumeMounts) != 1 {
			t.Errorf("Expected one volume mount in user container but got %d", len(pod.Spec.Containers[0].VolumeMounts))
		} else {
			mount := pod.Spec.Containers[0].VolumeMounts[0]
			if mount.Name != constants.StorageInitializerVolumeName {
				t.Errorf("Expected mount name %s but got %s", constants.StorageInitializerVolumeName, mount.Name)
			}
			if mount.MountPath != constants.DefaultModelLocalMountPath {
				t.Errorf("Expected mount path %s but got %s", constants.DefaultModelLocalMountPath, mount.MountPath)
			}
			if !mount.ReadOnly {
				t.Error("Expected ReadOnly mount but got ReadWrite")
			}
			if mount.SubPath != "models" {
				t.Errorf("Expected SubPath 'models' but got '%s'", mount.SubPath)
			}
		}

		// Check that ShareProcessNamespace is NOT set (unlike modelcar)
		if pod.Spec.ShareProcessNamespace != nil {
			t.Errorf("Expected ShareProcessNamespace to be nil but got %v", *pod.Spec.ShareProcessNamespace)
		}
	}
}

func TestInjectImageVolumeMultiNode(t *testing.T) {
	t.Run("Test InjectImageVolume with worker-container only (multi-node scenario)", func(t *testing.T) {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					constants.StorageInitializerSourceUriInternalAnnotationKey: constants.OciURIPrefix + "ghcr.io/org/model:v1",
				},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{Name: constants.WorkerContainerName},
				},
			},
		}
		injector := &StorageInitializerInjector{config: &kserveTypes.StorageInitializerConfig{}}

		err := injector.InjectImageVolume(pod)
		require.NoError(t, err)

		// Verify that image volume was created
		assert.Len(t, pod.Spec.Volumes, 1, "Should have exactly one volume")
		assert.Equal(t, constants.StorageInitializerVolumeName, pod.Spec.Volumes[0].Name)
		assert.NotNil(t, pod.Spec.Volumes[0].Image, "Should be an image volume")

		workerContainer := utils.GetContainerWithName(&pod.Spec, constants.WorkerContainerName)
		assert.NotNil(t, workerContainer, "Worker container should exist")

		// Verify that worker container has the correct volume mount
		found := false
		for _, mount := range workerContainer.VolumeMounts {
			if mount.Name == constants.StorageInitializerVolumeName {
				found = true
				assert.True(t, mount.ReadOnly, "Mount should be read-only")
				assert.Equal(t, "models", mount.SubPath, "SubPath should be 'models'")
				break
			}
		}
		assert.True(t, found, "Worker container should have storage initializer volume mount")

		// Verify NO ShareProcessNamespace (unlike modelcar)
		assert.Nil(t, pod.Spec.ShareProcessNamespace, "ShareProcessNamespace should not be set")
	})

	t.Run("Test InjectImageVolume error when no valid container found", func(t *testing.T) {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					constants.StorageInitializerSourceUriInternalAnnotationKey: constants.OciURIPrefix + "ghcr.io/org/model:v1",
				},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{Name: "some-other-container"},
				},
			},
		}
		injector := &StorageInitializerInjector{config: &kserveTypes.StorageInitializerConfig{}}

		err := injector.InjectImageVolume(pod)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "Invalid configuration: cannot find container: kserve-container")
	})

	t.Run("Test InjectImageVolume prioritizes kserve-container over worker-container", func(t *testing.T) {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					constants.StorageInitializerSourceUriInternalAnnotationKey: constants.OciURIPrefix + "ghcr.io/org/model:v1",
				},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{Name: constants.InferenceServiceContainerName},
					{Name: constants.WorkerContainerName},
				},
			},
		}
		injector := &StorageInitializerInjector{config: &kserveTypes.StorageInitializerConfig{}}

		err := injector.InjectImageVolume(pod)
		require.NoError(t, err)

		kserveContainer := utils.GetContainerWithName(&pod.Spec, constants.InferenceServiceContainerName)
		workerContainer := utils.GetContainerWithName(&pod.Spec, constants.WorkerContainerName)

		assert.NotNil(t, kserveContainer)
		assert.NotNil(t, workerContainer)

		// Check that kserve-container got the volume mount
		kserveHasMount := false
		for _, mount := range kserveContainer.VolumeMounts {
			if mount.Name == constants.StorageInitializerVolumeName {
				kserveHasMount = true
				break
			}
		}
		assert.True(t, kserveHasMount, "kserve-container should have storage initializer volume mount")

		// Worker should NOT have mount when kserve-container is present
		workerHasMount := false
		for _, mount := range workerContainer.VolumeMounts {
			if mount.Name == constants.StorageInitializerVolumeName {
				workerHasMount = true
				break
			}
		}
		assert.False(t, workerHasMount, "worker-container should not have mount when kserve-container exists")
	})

	t.Run("Test InjectImageVolume with transformer container", func(t *testing.T) {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					constants.StorageInitializerSourceUriInternalAnnotationKey: constants.OciURIPrefix + "ghcr.io/org/transformer:v1",
				},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{Name: constants.InferenceServiceContainerName},
					{Name: constants.TransformerContainerName},
				},
			},
		}
		injector := &StorageInitializerInjector{config: &kserveTypes.StorageInitializerConfig{}}

		err := injector.InjectImageVolume(pod)
		require.NoError(t, err)

		// Both kserve and transformer containers should get image volumes
		kserveContainer := utils.GetContainerWithName(&pod.Spec, constants.InferenceServiceContainerName)
		transformerContainer := utils.GetContainerWithName(&pod.Spec, constants.TransformerContainerName)

		assert.NotNil(t, kserveContainer)
		assert.NotNil(t, transformerContainer)

		// Check kserve container has mount
		kserveHasMount := false
		for _, mount := range kserveContainer.VolumeMounts {
			if mount.Name == constants.StorageInitializerVolumeName {
				kserveHasMount = true
				break
			}
		}
		assert.True(t, kserveHasMount, "kserve-container should have storage initializer volume mount")

		// Check transformer container has mount
		transformerHasMount := false
		for _, mount := range transformerContainer.VolumeMounts {
			if mount.Name == constants.StorageInitializerVolumeName {
				transformerHasMount = true
				break
			}
		}
		assert.True(t, transformerHasMount, "Transformer container should have storage initializer volume mount")
	})

	t.Run("Test InjectImageVolume with worker and transformer containers", func(t *testing.T) {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					constants.StorageInitializerSourceUriInternalAnnotationKey: constants.OciURIPrefix + "ghcr.io/org/model:v1",
				},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{Name: constants.WorkerContainerName},
					{Name: constants.TransformerContainerName},
				},
			},
		}
		injector := &StorageInitializerInjector{config: &kserveTypes.StorageInitializerConfig{}}

		err := injector.InjectImageVolume(pod)
		require.NoError(t, err)

		// Check both worker and transformer containers got volume mounts
		workerContainer := utils.GetContainerWithName(&pod.Spec, constants.WorkerContainerName)
		transformerContainer := utils.GetContainerWithName(&pod.Spec, constants.TransformerContainerName)

		assert.NotNil(t, workerContainer)
		assert.NotNil(t, transformerContainer)

		// Both should have volume mounts
		workerHasMount := false
		for _, mount := range workerContainer.VolumeMounts {
			if mount.Name == constants.StorageInitializerVolumeName {
				workerHasMount = true
				break
			}
		}
		assert.True(t, workerHasMount, "Worker container should have storage initializer volume mount")

		transformerHasMount := false
		for _, mount := range transformerContainer.VolumeMounts {
			if mount.Name == constants.StorageInitializerVolumeName {
				transformerHasMount = true
				break
			}
		}
		assert.True(t, transformerHasMount, "Transformer container should have storage initializer volume mount")
	})
}
