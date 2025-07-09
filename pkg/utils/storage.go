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

package utils

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"

	"github.com/kserve/kserve/pkg/constants"
)

// ParsePvcURI parses a PVC URI of the form "pvc://<name>[/path]" into its components.
//
// Parameters:
//   - srcURI: The source URI string, which must begin with the "pvc://" prefix.
//
// Returns:
//   - pvcName: The name of the PVC (<name> part).
//   - pvcPath: The optional <path> component. If not provided, this will be an empty string.
//   - err: An error if strings.Split would return zero.
//
// The function expects the input to follow the "pvc://<name>[/path]" format, however the pvc:// prefix is not validated.
//
// Examples:
//
//	"pvc://myclaim"           => pvcName: "myclaim", pvcPath: "", err: nil
//	"pvc://myclaim/models"    => pvcName: "myclaim", pvcPath: "models", err: nil
//	"pvc://myclaim/models/v1" => pvcName: "myclaim", pvcPath: "models/v1", err: nil
//	"s3://bucket/path"        => pvcName: "s3:", pvcPath: "/bucket/path", err: nil
//	"" (empty string)         => pvcName: "", pvcPath: "", err: nil
func ParsePvcURI(srcURI string) (pvcName string, pvcPath string, err error) {
	parts := strings.Split(strings.TrimPrefix(srcURI, constants.PvcURIPrefix), "/")
	switch len(parts) {
	case 0:
		return "", "", fmt.Errorf("invalid URI must be pvc://<pvcname>/[path]: %s", srcURI)
	case 1:
		pvcName = parts[0]
		pvcPath = ""
	default:
		pvcName = parts[0]
		pvcPath = strings.Join(parts[1:], "/")
	}

	return pvcName, pvcPath, nil
}

// AddModelPvcMount adds a PVC mount to the specified container in the given PodSpec based on the provided modelUri.
// The modelUri must be in the format "pvc://<pvcname>[/path]". Both the VolumeMount and the Volume are named as in
// constants.PvcSourceMountName. The PVC is mounted in the container at constants.DefaultModelLocalMountPath.
// If the mount or volume already exists, it will not be duplicated.
//
// Parameters:
//   - modelUri: The URI specifying the PVC and optional sub-path to mount.
//   - containerName: The name of the container within the PodSpec to which the PVC should be mounted.
//   - readOnly: Whether the mount should be read-only.
//   - podSpec: PodSpec to modify.
//
// Returns:
//   - error: An error if the modelUri is invalid or if any other issue occurs; otherwise, nil.
func AddModelPvcMount(modelUri, containerName string, readOnly bool, podSpec *corev1.PodSpec) error {
	pvcName, pvcPath, err := ParsePvcURI(modelUri)
	if err != nil {
		return err
	}

	mountAdded := false
	for idx := range podSpec.Containers {
		if podSpec.Containers[idx].Name == containerName {
			mountExists := false
			for mountIdx := range podSpec.Containers[idx].VolumeMounts {
				if podSpec.Containers[idx].VolumeMounts[mountIdx].Name == constants.PvcSourceMountName {
					mountExists = true
					mountAdded = true
					break
				}
			}

			if !mountExists {
				pvcSourceVolumeMount := corev1.VolumeMount{
					Name:      constants.PvcSourceMountName,
					MountPath: constants.DefaultModelLocalMountPath,
					// only path to volume's root ("") or folder is supported
					SubPath:  pvcPath,
					ReadOnly: readOnly,
				}

				podSpec.Containers[idx].VolumeMounts = append(podSpec.Containers[idx].VolumeMounts, pvcSourceVolumeMount)
				mountAdded = true
			}

			break
		}
	}

	if mountAdded {
		// add the PVC volume on the pod
		volumeExists := false
		for _, volume := range podSpec.Volumes {
			if volume.Name == constants.PvcSourceMountName {
				volumeExists = true
				break
			}
		}

		if !volumeExists {
			pvcSourceVolume := corev1.Volume{
				Name: constants.PvcSourceMountName,
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: pvcName,
					},
				},
			}
			podSpec.Volumes = append(podSpec.Volumes, pvcSourceVolume)
		}
	}

	return nil
}
