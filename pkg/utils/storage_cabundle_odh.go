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

package utils

import (
	"strings"

	corev1 "k8s.io/api/core/v1"

	"github.com/kserve/kserve/pkg/constants"
)

const (
	// odhCABundleVolumeName matches the volume name the storage initializer injectors
	// use for the CA bundle ConfigMap, so the two never mount it twice.
	odhCABundleVolumeName = "cabundle-cert"

	httpsURIPrefix = "https://"

	// Literal names: pkg/credentials/s3 depends on this package.
	awsCABundleEnvVar          = "AWS_CA_BUNDLE"
	awsCABundleConfigMapEnvVar = "AWS_CA_BUNDLE_CONFIGMAP"
	requestsCABundleEnvVar     = "REQUESTS_CA_BUNDLE"
	sslCertFileEnvVar          = "SSL_CERT_FILE"
)

// DefaultODHCustomCABundle makes a storage initializer that downloads only from
// https:// or hf:// sources trust the namespace's odh-kserve-custom-ca-bundle
// ConfigMap, which odh-model-controller builds from the platform trusted CA bundle.
// It mounts the ConfigMap and sets the variables the storage initializer reads to
// combine the bundle with its default trust store.
//
// It is a default, never an override: the pod spec is left unchanged when the
// container already carries CA bundle settings, when a volume or mount would
// collide, or when any download uses another scheme. The volume is optional, so a
// namespace without the ConfigMap still starts the pod on the default trust store.
//
// It reports whether the pod spec was changed.
func DefaultODHCustomCABundle(podSpec *corev1.PodSpec, initContainer *corev1.Container, mountPath string) bool {
	if podSpec == nil || initContainer == nil || !downloadsOnlyOverTLS(initContainer.Args) {
		return false
	}
	if hasExplicitCABundle(initContainer.Env) {
		return false
	}
	if mountPath == "" {
		mountPath = constants.DefaultCaBundleVolumeMountPath
	}
	for _, volume := range podSpec.Volumes {
		if volume.Name == odhCABundleVolumeName {
			return false
		}
	}
	for _, volumeMount := range initContainer.VolumeMounts {
		if volumeMount.Name == odhCABundleVolumeName || volumeMount.MountPath == mountPath {
			return false
		}
	}

	optional := true
	podSpec.Volumes = append(podSpec.Volumes, corev1.Volume{
		Name: odhCABundleVolumeName,
		VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: constants.ODHKServeCustomCABundleConfigMapName},
				Optional:             &optional,
			},
		},
	})
	initContainer.VolumeMounts = append(initContainer.VolumeMounts, corev1.VolumeMount{
		Name:      odhCABundleVolumeName,
		MountPath: mountPath,
		ReadOnly:  true,
	})
	initContainer.Env = append(initContainer.Env,
		corev1.EnvVar{Name: constants.CaBundleConfigMapNameEnvVarKey, Value: constants.ODHKServeCustomCABundleConfigMapName},
		corev1.EnvVar{Name: constants.CaBundleVolumeMountPathEnvVarKey, Value: mountPath},
	)
	return true
}

// downloadsOnlyOverTLS reports whether the storage initializer arguments name at
// least one source and every source is an https:// or hf:// URI. Other schemes are
// excluded because their download paths treat the CA bundle variables differently;
// s3:// for instance fails when the bundle file is missing.
func downloadsOnlyOverTLS(args []string) bool {
	found := false
	for _, arg := range args {
		scheme, _, isURI := strings.Cut(arg, "://")
		if !isURI {
			continue
		}
		switch scheme + "://" {
		case httpsURIPrefix, constants.HfURIPrefix:
			found = true
		default:
			return false
		}
	}
	return found
}

// hasExplicitCABundle reports whether the container already carries trust settings
// that the default must not replace. S3 credentials that point at the same ConfigMap
// are compatible: they name the bundle without mounting it for other schemes.
func hasExplicitCABundle(env []corev1.EnvVar) bool {
	s3ConfigMap, hasS3ConfigMap := "", false
	hasS3Bundle := false
	for _, envVar := range env {
		switch envVar.Name {
		case constants.CaBundleConfigMapNameEnvVarKey, constants.CaBundleVolumeMountPathEnvVarKey,
			requestsCABundleEnvVar, sslCertFileEnvVar:
			return true
		case awsCABundleConfigMapEnvVar:
			s3ConfigMap, hasS3ConfigMap = envVar.Value, true
		case awsCABundleEnvVar:
			hasS3Bundle = true
		}
	}
	if hasS3ConfigMap {
		return s3ConfigMap != constants.ODHKServeCustomCABundleConfigMapName
	}
	return hasS3Bundle
}
