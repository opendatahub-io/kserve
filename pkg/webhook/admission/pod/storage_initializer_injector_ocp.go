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
	"fmt"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/kserve/kserve/pkg/constants"
)

// injectPlatformExtensions injects OCP-specific pod mutations after storage
// initialization. Currently this handles OVMS auto-versioning.
func (mi *StorageInitializerInjector) injectPlatformExtensions(pod *corev1.Pod) error {
	return mi.InjectOVMSAutoVersioning(pod)
}

// InjectOVMSAutoVersioning injects an init container to reorganize model files
// into OVMS-compatible versioned directory structure when the storage.kserve.io/ovms-auto-versioning
// annotation is present. This is required because OVMS expects models in versioned directories
// like /mnt/models/1/, /mnt/models/2/, etc.
func (mi *StorageInitializerInjector) InjectOVMSAutoVersioning(pod *corev1.Pod) error {
	// Check if OVMS auto-versioning annotation is present
	versionString, ok := pod.Annotations[constants.OVMSAutoVersioningAnnotationKey]
	if !ok {
		return nil
	}

	// Validate the version is a positive integer
	version, err := strconv.Atoi(versionString)
	if err != nil || version <= 0 {
		return fmt.Errorf("invalid OVMS auto-versioning annotation value '%s': must be a positive integer", versionString)
	}

	// Don't inject if OVMS versioning container already exists
	for _, container := range pod.Spec.InitContainers {
		if container.Name == constants.OVMSVersioningContainerName {
			return nil
		}
	}

	// Create the OVMS versioning init container
	ovmsVersioningContainer := corev1.Container{
		Name:    constants.OVMSVersioningContainerName,
		Image:   "registry.redhat.io/ubi9/ubi-micro:latest",
		Command: []string{"/bin/sh"},
		Args: []string{
			"-c",
			fmt.Sprintf(`
# OVMS Auto-Versioning Script
# This script reorganizes model files to match OVMS expected directory structure

MODEL_DIR="%s"
VERSION="%s"
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
`, constants.DefaultModelLocalMountPath, versionString),
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
	}

	// Add the OVMS versioning init container to the pod
	pod.Spec.InitContainers = append(pod.Spec.InitContainers, ovmsVersioningContainer)
	return nil
}
