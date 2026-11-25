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
	corev1 "k8s.io/api/core/v1"

	"github.com/kserve/kserve/pkg/constants"
	"github.com/kserve/kserve/pkg/utils"
)

// defaultODHCustomCABundle makes the injected storage initializer trust the
// namespace's odh-kserve-custom-ca-bundle for https:// and hf:// downloads when no
// CA bundle is configured for it. It only runs at pod admission, so existing pods
// and the workload's pod template are never changed.
func (mi *StorageInitializerInjector) defaultODHCustomCABundle(pod *corev1.Pod) {
	initContainer := utils.GetInitContainerWithName(&pod.Spec, constants.StorageInitializerContainerName)
	mountPath := ""
	if mi.config != nil {
		mountPath = mi.config.CaBundleVolumeMountPath
	}
	utils.DefaultODHCustomCABundle(&pod.Spec, initContainer, mountPath)
}
