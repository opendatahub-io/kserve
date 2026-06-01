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

// applyPlatformPodSpecDefaults injects the OpenShift service CA bundle volume, mount,
// and SSL_CERT_FILE environment variable into the pod spec. It also sets the readiness
// probe scheme to HTTPS.
func applyPlatformPodSpecDefaults(podSpec *corev1.PodSpec) {
	if len(podSpec.Containers) == 0 {
		return
	}
	podSpec.Containers[0].VolumeMounts = append(podSpec.Containers[0].VolumeMounts,
		corev1.VolumeMount{
			Name:      "openshift-service-ca-bundle",
			MountPath: "/etc/odh/openshift-service-ca-bundle",
		},
	)

	podSpec.Containers[0].Env = append(podSpec.Containers[0].Env,
		corev1.EnvVar{
			Name:  "SSL_CERT_FILE",
			Value: "/etc/odh/openshift-service-ca-bundle/service-ca.crt",
		},
	)

	podSpec.Volumes = append(podSpec.Volumes,
		corev1.Volume{
			Name: "openshift-service-ca-bundle",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: constants.OpenShiftServiceCaConfigMapName,
					},
				},
			},
		},
	)

	// In ODH, the readiness probe is using HTTPS
	podSpec.Containers[0].ReadinessProbe.HTTPGet.Scheme = corev1.URISchemeHTTPS
}
