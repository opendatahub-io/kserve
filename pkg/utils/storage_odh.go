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
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"

	"github.com/kserve/kserve/pkg/constants"
)

// ConfigureImageVolumeToContainer configures the OCI image specified in modelUri as a Kubernetes
// image volume and mounts it to the specified target container. This is an alternative to the
// modelcar approach that uses native Kubernetes image volumes (requires K8s 1.33+ with ImageVolume
// feature gate enabled for image volumes + mount subpath).
//
// The configuration includes:
//   - Adding a Kubernetes image volume to the PodSpec referencing the OCI image
//   - Mounting the image volume as a read-only volume to the target container at modelPath
//   - No sidecar containers or process namespace sharing required (simpler than modelcar)
//
// Parameters:
//   - modelUri: The URI specifying the model image location (e.g., "oci://ghcr.io/org/model:v1")
//   - podSpec: The PodSpec to modify
//   - targetContainerName: The name of the container to mount the image volume to
//   - modelPath: The path where the image volume should be mounted inside the container
//     (e.g., /mnt/models). The OCI image contents will be available directly at this path.
//
// Returns:
//   - error: An error if the target container is not found or if configuration fails; otherwise, nil.
func ConfigureImageVolumeToContainer(modelUri string, podSpec *corev1.PodSpec, targetContainerName string, modelPath string) error {
	targetContainer := GetContainerWithName(podSpec, targetContainerName)
	if targetContainer == nil {
		return fmt.Errorf("no container found with name %s", targetContainerName)
	}

	imageRef := strings.TrimPrefix(modelUri, constants.OciURIPrefix)
	volName := constants.StorageInitializerVolumeName

	// Validate existing mounts and check if already configured
	for _, m := range targetContainer.VolumeMounts {
		if m.Name == volName {
			if m.MountPath == modelPath {
				// Already correctly mounted - idempotent
				return nil
			}
			// Same volume, different path - user error
			return fmt.Errorf("volume %q already mounted at %q, cannot also mount at %q", volName, m.MountPath, modelPath)
		} else if m.MountPath == modelPath {
			// Different volume trying to use our mount path
			return fmt.Errorf("mountPath %q already used by volume %q", modelPath, m.Name)
		}
	}

	// Idempotent: add pod-level Volume only if it doesn't already exist.
	// This is distinct from the VolumeMount check above - the Volume is pod-scoped,
	// while VolumeMounts are per-container.
	volumeExists := false
	for _, v := range podSpec.Volumes {
		if v.Name == volName {
			// Validate it's the correct type and reference
			if v.Image == nil {
				return fmt.Errorf("volume %q already exists but is not an image volume", volName)
			}
			if v.Image.Reference != imageRef {
				return fmt.Errorf("volume %q already exists with different image reference %q (expected %q)", volName, v.Image.Reference, imageRef)
			}
			volumeExists = true
			break
		}
	}
	if !volumeExists {
		podSpec.Volumes = append(podSpec.Volumes, corev1.Volume{
			Name: volName,
			VolumeSource: corev1.VolumeSource{
				Image: &corev1.ImageVolumeSource{
					Reference:  imageRef,
					PullPolicy: corev1.PullIfNotPresent,
				},
			},
		})
	}

	// Mount the /models subdirectory from the image, matching modelcar's convention
	// that expects model files at /models/* inside the OCI image.
	targetContainer.VolumeMounts = append(targetContainer.VolumeMounts, corev1.VolumeMount{
		Name:      volName,
		MountPath: modelPath,
		ReadOnly:  true,
		SubPath:   "models",
	})

	return nil
}
