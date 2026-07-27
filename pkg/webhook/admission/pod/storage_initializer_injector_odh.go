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
	"errors"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"

	"github.com/kserve/kserve/pkg/apis/serving/v1beta1"
	"github.com/kserve/kserve/pkg/constants"
	"github.com/kserve/kserve/pkg/utils"
)

// InjectImageVolume injects a Kubernetes image volume for OCI model storage.
// This method mounts the OCI image directly as a read-only volume to the inference
// container using native Kubernetes image volumes (requires K8s 1.33+ with ImageVolume
// feature gate enabled for image volumes + mount subpath). This is an alternative to
// InjectModelcar that doesn't require sidecar containers or process namespace sharing.
// This method is idempotent so can be called multiple times.
func (mi *StorageInitializerInjector) InjectImageVolume(pod *corev1.Pod) error {
	srcURI, ok := pod.Annotations[constants.StorageInitializerSourceUriInternalAnnotationKey]
	if !ok {
		return nil
	}

	// Only inject image volume for OCI URIs
	if !strings.HasPrefix(srcURI, constants.OciURIPrefix) {
		return nil
	}

	// Find the kserve-container (this is the model inference server) and worker-container
	userContainer := utils.GetContainerWithName(&pod.Spec, constants.InferenceServiceContainerName)
	workerContainer := utils.GetContainerWithName(&pod.Spec, constants.WorkerContainerName)

	if userContainer == nil {
		if workerContainer == nil {
			return fmt.Errorf("Invalid configuration: cannot find container: %s", constants.InferenceServiceContainerName)
		} else {
			// Use worker container for multi-node scenarios
			if err := utils.ConfigureImageVolumeToContainer(srcURI, &pod.Spec, constants.WorkerContainerName, constants.DefaultModelLocalMountPath); err != nil {
				return err
			}
		}
	} else {
		if err := utils.ConfigureImageVolumeToContainer(srcURI, &pod.Spec, constants.InferenceServiceContainerName, constants.DefaultModelLocalMountPath); err != nil {
			return err
		}
	}

	// Configure image volume for transformer container if it exists
	if utils.GetContainerWithName(&pod.Spec, constants.TransformerContainerName) != nil {
		return utils.ConfigureImageVolumeToContainer(srcURI, &pod.Spec, constants.TransformerContainerName, constants.DefaultModelLocalMountPath)
	}

	return nil
}

func getOVMSVersioningImage(openshiftConfig *v1beta1.OpenShiftConfig) (string, error) {
	if openshiftConfig == nil || openshiftConfig.OvmsVersioningImage == "" {
		return "", errors.New("ovmsVersioningImage not configured in inferenceservice-config openshiftConfig")
	}
	if !strings.Contains(openshiftConfig.OvmsVersioningImage, "@sha256:") {
		return "", fmt.Errorf("ovmsVersioningImage must be a digest-pinned reference (e.g. @sha256:...), got: %s", openshiftConfig.OvmsVersioningImage)
	}
	return openshiftConfig.OvmsVersioningImage, nil
}
