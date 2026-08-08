//go:build !distro

/*
Copyright 2023 The KServe Authors.

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

package inferencegraph

import (
	corev1 "k8s.io/api/core/v1"
)

// expectedReadinessProbeScheme returns the expected readiness probe scheme for non-OCP builds.
func expectedReadinessProbeScheme() corev1.URIScheme {
	return corev1.URISchemeHTTP
}

// expectedPlatformVolumeMounts returns no volume mounts for non-OCP builds.
func expectedPlatformVolumeMounts() []corev1.VolumeMount {
	return nil
}

// expectedPlatformEnvVars returns no env vars for non-OCP builds.
func expectedPlatformEnvVars() []corev1.EnvVar {
	return nil
}

// expectedPlatformVolumes returns no volumes for non-OCP builds.
func expectedPlatformVolumes() []corev1.Volume {
	return nil
}
