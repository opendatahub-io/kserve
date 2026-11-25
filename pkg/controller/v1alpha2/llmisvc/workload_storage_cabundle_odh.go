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

package llmisvc

import (
	"slices"

	corev1 "k8s.io/api/core/v1"

	"github.com/kserve/kserve/pkg/constants"
	kserveTypes "github.com/kserve/kserve/pkg/types"
	"github.com/kserve/kserve/pkg/utils"
)

// applyPlatformStorageCABundle gives the storage-initializer of hf:// downloads a CA
// bundle when it has none: the bundle configured for the storage initializer when
// there is one, otherwise the namespace's odh-kserve-custom-ca-bundle. current is
// the live storage-initializer, or nil.
func applyPlatformStorageCABundle(namespace string, current *corev1.Container, podSpec *corev1.PodSpec, initContainer *corev1.Container, storageConfig *kserveTypes.StorageInitializerConfig) {
	if initContainer == nil || !addsCABundle(current, initContainer.Args) {
		return
	}
	// The caller already mounted a configured bundle.
	if slices.ContainsFunc(podSpec.Volumes, func(v corev1.Volume) bool { return v.Name == CaBundleVolumeName }) {
		return
	}
	// The user manages the bundle on the container.
	if _, set := utils.GetEnvVarValue(initContainer.Env, constants.CaBundleConfigMapNameEnvVarKey); set {
		return
	}
	mountPath := ""
	if storageConfig != nil {
		// A configured bundle wins over the platform default.
		if needCaBundleMount(storageConfig.CaBundleConfigMapName, initContainer) {
			injectCaBundle(namespace, podSpec, initContainer, storageConfig)
			return
		}
		mountPath = storageConfig.CaBundleVolumeMountPath
	}
	utils.DefaultODHCustomCABundle(podSpec, initContainer, mountPath)
}

// addsCABundle reports whether the storage-initializer may get a CA bundle. It
// returns true when there is no live init container, when the live container
// downloads a different set of arguments, or when the live container already mounts
// the bundle volume. A live container that runs the same downloads without that
// mount keeps its pod template, so a controller upgrade does not restart its pod.
func addsCABundle(current *corev1.Container, args []string) bool {
	if current == nil || !slices.Equal(current.Args, args) {
		return true
	}
	return slices.ContainsFunc(current.VolumeMounts, func(m corev1.VolumeMount) bool { return m.Name == CaBundleVolumeName })
}
