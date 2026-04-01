//go:build distro

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

	"github.com/kserve/kserve/pkg/constants"
)

// expectedReadinessProbeScheme returns the expected readiness probe scheme for OCP builds.
func expectedReadinessProbeScheme() corev1.URIScheme {
	return corev1.URISchemeHTTPS
}

// expectedPlatformVolumeMounts returns the CA bundle volume mount expected on OCP.
func expectedPlatformVolumeMounts() []corev1.VolumeMount {
	return []corev1.VolumeMount{
		{
			Name:      "openshift-service-ca-bundle",
			MountPath: "/etc/odh/openshift-service-ca-bundle",
		},
	}
}

// expectedPlatformEnvVars returns the SSL_CERT_FILE env var expected on OCP.
func expectedPlatformEnvVars() []corev1.EnvVar {
	return []corev1.EnvVar{
		{
			Name:  "SSL_CERT_FILE",
			Value: "/etc/odh/openshift-service-ca-bundle/service-ca.crt",
		},
	}
}

// expectedPlatformVolumes returns the CA bundle volume expected on OCP.
func expectedPlatformVolumes() []corev1.Volume {
	return []corev1.Volume{
		{
			Name: "openshift-service-ca-bundle",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: constants.OpenShiftServiceCaConfigMapName,
					},
				},
			},
		},
	}
}
