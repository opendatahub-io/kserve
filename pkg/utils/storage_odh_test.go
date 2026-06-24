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

package utils

import (
	"testing"

	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	"github.com/kserve/kserve/pkg/constants"
)

func TestConfigureImageVolumeToContainer(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	createTestPodSpec := func() *corev1.PodSpec {
		return &corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: constants.InferenceServiceContainerName},
			},
		}
	}

	t.Run("SuccessfullyConfigureImageVolume", func(t *testing.T) {
		podSpec := createTestPodSpec()
		modelUri := constants.OciURIPrefix + "ghcr.io/org/model:v1"
		modelPath := constants.DefaultModelLocalMountPath

		err := ConfigureImageVolumeToContainer(modelUri, podSpec, constants.InferenceServiceContainerName, modelPath)
		g.Expect(err).ToNot(gomega.HaveOccurred())

		// Verify volume was created
		g.Expect(podSpec.Volumes).To(gomega.HaveLen(1))
		g.Expect(podSpec.Volumes[0].Name).To(gomega.Equal(constants.StorageInitializerVolumeName))
		g.Expect(podSpec.Volumes[0].Image).ToNot(gomega.BeNil())
		g.Expect(podSpec.Volumes[0].Image.Reference).To(gomega.Equal("ghcr.io/org/model:v1"))
		g.Expect(podSpec.Volumes[0].Image.PullPolicy).To(gomega.Equal(corev1.PullIfNotPresent))

		// Verify volume mount was added to container
		container := GetContainerWithName(podSpec, constants.InferenceServiceContainerName)
		g.Expect(container).ToNot(gomega.BeNil())
		g.Expect(container.VolumeMounts).To(gomega.HaveLen(1))
		g.Expect(container.VolumeMounts[0].Name).To(gomega.Equal(constants.StorageInitializerVolumeName))
		g.Expect(container.VolumeMounts[0].MountPath).To(gomega.Equal(modelPath))
		g.Expect(container.VolumeMounts[0].ReadOnly).To(gomega.BeTrue())
		g.Expect(container.VolumeMounts[0].SubPath).To(gomega.Equal("models"))
	})

	t.Run("IdempotentWhenAlreadyConfigured", func(t *testing.T) {
		podSpec := createTestPodSpec()
		modelUri := constants.OciURIPrefix + "ghcr.io/org/model:v1"
		modelPath := constants.DefaultModelLocalMountPath

		// Configure once
		err := ConfigureImageVolumeToContainer(modelUri, podSpec, constants.InferenceServiceContainerName, modelPath)
		g.Expect(err).ToNot(gomega.HaveOccurred())

		// Configure again - should be idempotent
		err = ConfigureImageVolumeToContainer(modelUri, podSpec, constants.InferenceServiceContainerName, modelPath)
		g.Expect(err).ToNot(gomega.HaveOccurred())

		// Should still have only 1 volume and 1 mount
		g.Expect(podSpec.Volumes).To(gomega.HaveLen(1))
		container := GetContainerWithName(podSpec, constants.InferenceServiceContainerName)
		g.Expect(container.VolumeMounts).To(gomega.HaveLen(1))
	})

	t.Run("ErrorWhenContainerNotFound", func(t *testing.T) {
		podSpec := createTestPodSpec()
		modelUri := constants.OciURIPrefix + "ghcr.io/org/model:v1"

		err := ConfigureImageVolumeToContainer(modelUri, podSpec, "nonexistent-container", constants.DefaultModelLocalMountPath)
		g.Expect(err).To(gomega.HaveOccurred())
		g.Expect(err.Error()).To(gomega.ContainSubstring("no container found"))
	})

	t.Run("ErrorWhenVolumeSameNameDifferentImage", func(t *testing.T) {
		podSpec := createTestPodSpec()
		// Pre-add a volume with same name but different type
		podSpec.Volumes = []corev1.Volume{
			{
				Name: constants.StorageInitializerVolumeName,
				VolumeSource: corev1.VolumeSource{
					EmptyDir: &corev1.EmptyDirVolumeSource{},
				},
			},
		}

		modelUri := constants.OciURIPrefix + "ghcr.io/org/model:v1"
		err := ConfigureImageVolumeToContainer(modelUri, podSpec, constants.InferenceServiceContainerName, constants.DefaultModelLocalMountPath)
		g.Expect(err).To(gomega.HaveOccurred())
		g.Expect(err.Error()).To(gomega.ContainSubstring("not an image volume"))
	})

	t.Run("ErrorWhenDifferentImageReference", func(t *testing.T) {
		podSpec := createTestPodSpec()
		// Pre-add volume with different image reference
		podSpec.Volumes = []corev1.Volume{
			{
				Name: constants.StorageInitializerVolumeName,
				VolumeSource: corev1.VolumeSource{
					Image: &corev1.ImageVolumeSource{
						Reference:  "ghcr.io/org/different:v1",
						PullPolicy: corev1.PullIfNotPresent,
					},
				},
			},
		}

		modelUri := constants.OciURIPrefix + "ghcr.io/org/model:v1"
		err := ConfigureImageVolumeToContainer(modelUri, podSpec, constants.InferenceServiceContainerName, constants.DefaultModelLocalMountPath)
		g.Expect(err).To(gomega.HaveOccurred())
		g.Expect(err.Error()).To(gomega.ContainSubstring("different image reference"))
	})

	t.Run("ErrorWhenSameVolumeDifferentMountPath", func(t *testing.T) {
		podSpec := createTestPodSpec()
		modelUri := constants.OciURIPrefix + "ghcr.io/org/model:v1"

		// Configure with first mount path
		err := ConfigureImageVolumeToContainer(modelUri, podSpec, constants.InferenceServiceContainerName, "/mnt/models")
		g.Expect(err).ToNot(gomega.HaveOccurred())

		// Try to mount same volume at different path
		err = ConfigureImageVolumeToContainer(modelUri, podSpec, constants.InferenceServiceContainerName, "/opt/models")
		g.Expect(err).To(gomega.HaveOccurred())
		g.Expect(err.Error()).To(gomega.ContainSubstring("already mounted"))
	})

	t.Run("ConfigureWorkerContainer", func(t *testing.T) {
		podSpec := &corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: constants.WorkerContainerName},
			},
		}
		modelUri := constants.OciURIPrefix + "ghcr.io/org/model:v1"
		modelPath := constants.DefaultModelLocalMountPath

		err := ConfigureImageVolumeToContainer(modelUri, podSpec, constants.WorkerContainerName, modelPath)
		g.Expect(err).ToNot(gomega.HaveOccurred())

		container := GetContainerWithName(podSpec, constants.WorkerContainerName)
		g.Expect(container).ToNot(gomega.BeNil())
		g.Expect(container.VolumeMounts).To(gomega.HaveLen(1))
		g.Expect(container.VolumeMounts[0].MountPath).To(gomega.Equal(modelPath))
	})

	t.Run("ConfigureTransformerContainer", func(t *testing.T) {
		podSpec := &corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: constants.TransformerContainerName},
			},
		}
		modelUri := constants.OciURIPrefix + "ghcr.io/org/transformer:v1"
		modelPath := constants.DefaultModelLocalMountPath

		err := ConfigureImageVolumeToContainer(modelUri, podSpec, constants.TransformerContainerName, modelPath)
		g.Expect(err).ToNot(gomega.HaveOccurred())

		container := GetContainerWithName(podSpec, constants.TransformerContainerName)
		g.Expect(container).ToNot(gomega.BeNil())
		g.Expect(container.VolumeMounts).To(gomega.HaveLen(1))
	})
}
