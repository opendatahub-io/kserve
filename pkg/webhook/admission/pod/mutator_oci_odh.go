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
)

// getServerTypeFromPod reads the server type from the pod's opendatahub.io/kserve-runtime annotation.
// This annotation is propagated from the ServingRuntime during component reconciliation.
// Returns empty string if the annotation is not present.
func getServerTypeFromPod(pod *corev1.Pod) string {
	if pod == nil || pod.Annotations == nil {
		return ""
	}
	return pod.Annotations[constants.ODHKserveRuntimeAnnotation]
}

// getOciStorageMutator returns the appropriate mutator for OCI storage based on the runtime type.
// In distro builds, MLServer runtime uses image volumes while others use modelcar.
// The annotation check is deferred to execution time to avoid TOCTOU issues (CWE-367).
func getOciStorageMutator(pod *corev1.Pod, storageInitializer *StorageInitializerInjector) func(*corev1.Pod) error {
	return func(p *corev1.Pod) error {
		// Read server type from pod annotation at execution time, after prior mutators have run
		serverType := getServerTypeFromPod(p)

		if serverType == constants.ServerTypeMLServer {
			return storageInitializer.InjectImageVolume(p)
		}
		return storageInitializer.InjectModelcar(p)
	}
}
