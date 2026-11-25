//go:build !distro

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
	corev1 "k8s.io/api/core/v1"

	kserveTypes "github.com/kserve/kserve/pkg/types"
)

// applyPlatformStorageCABundle is a no-op in upstream builds. Distribution builds
// (compiled with -tags distro) can default the storage-initializer to a platform
// CA bundle.
func applyPlatformStorageCABundle(_ string, _ *corev1.Container, _ *corev1.PodSpec, _ *corev1.Container, _ *kserveTypes.StorageInitializerConfig) {
}
