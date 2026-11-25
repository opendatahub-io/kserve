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

// applyPlatformStorageCABundle makes the storage-initializer trust the namespace's
// odh-kserve-custom-ca-bundle for hf:// downloads when no CA bundle is configured
// for it. current is the live storage-initializer, or nil.
func applyPlatformStorageCABundle(current *corev1.Container, podSpec *corev1.PodSpec, initContainer *corev1.Container, storageConfig *kserveTypes.StorageInitializerConfig) {
	if initContainer == nil || !addsDefaultCABundle(current, initContainer.Args) {
		return
	}
	mountPath := ""
	if storageConfig != nil {
		mountPath = storageConfig.CaBundleVolumeMountPath
	}
	utils.DefaultODHCustomCABundle(podSpec, initContainer, mountPath)
}

// addsDefaultCABundle reports whether the storage-initializer gets the default CA
// bundle. It returns true when there is no live init container, when the live
// container downloads a different set of arguments, or when the live container
// already carries the default bundle. A live container that runs the same downloads
// without it keeps its pod template, so a controller upgrade does not restart its pod.
func addsDefaultCABundle(current *corev1.Container, args []string) bool {
	if current == nil || !slices.Equal(current.Args, args) {
		return true
	}
	name, _ := utils.GetEnvVarValue(current.Env, constants.CaBundleConfigMapNameEnvVarKey)
	return name == constants.ODHKServeCustomCABundleConfigMapName
}
