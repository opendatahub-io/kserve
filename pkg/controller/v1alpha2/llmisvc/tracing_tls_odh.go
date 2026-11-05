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
	"net/url"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
	"github.com/kserve/kserve/pkg/constants"
	"github.com/kserve/kserve/pkg/tracing"
	"github.com/kserve/kserve/pkg/utils"
)

const (
	platformTracingCollectorService = "data-science-collector-collector-headless"
	platformTracingPort             = "4317"
	tracesCertificateEnvVar         = "OTEL_EXPORTER_OTLP_TRACES_CERTIFICATE"
	globalCertificateEnvVar         = "OTEL_EXPORTER_OTLP_CERTIFICATE"
	tracesEndpointEnvVar            = "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"
	platformTracingCertFile         = constants.ServiceCaBundleMountPath + "/" + constants.ServiceCaBundleCertFile
)

// configureTracingTLS adds the OpenShift service-ca bundle and points OTLP trace
// exporters at it when the platform-managed collector endpoint is configured.
func configureTracingTLS(t *v1alpha2.TracingSpec, podSpec *corev1.PodSpec) {
	if t == nil || podSpec == nil || !isPlatformTracingCollectorEndpoint(ptr.Deref(t.ExporterEndpoint, "")) {
		return
	}

	needsServiceCA := false
	for i := range podSpec.Containers {
		container := &podSpec.Containers[i]
		if !usesOTLPTraceEndpoint(container) {
			continue
		}

		if hasEnvVar(container.Env, tracesCertificateEnvVar) || hasEnvVar(container.Env, globalCertificateEnvVar) {
			continue
		}
		container.Env = utils.AppendEnvVarIfNotExists(container.Env, corev1.EnvVar{
			Name:  tracesCertificateEnvVar,
			Value: platformTracingCertFile,
		})
		needsServiceCA = true
	}
	if !needsServiceCA {
		return
	}

	if !hasVolume(podSpec.Volumes, constants.ServiceCaBundleVolumeName) {
		podSpec.Volumes = append(podSpec.Volumes, corev1.Volume{
			Name: constants.ServiceCaBundleVolumeName,
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: constants.OpenShiftServiceCaConfigMapName},
				},
			},
		})
	}

	for i := range podSpec.Containers {
		container := &podSpec.Containers[i]
		if !hasEnvValue(container.Env, tracesCertificateEnvVar, platformTracingCertFile) {
			continue
		}
		if hasVolumeMount(container.VolumeMounts, constants.ServiceCaBundleVolumeName, constants.ServiceCaBundleMountPath) {
			continue
		}
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
			Name:      constants.ServiceCaBundleVolumeName,
			MountPath: constants.ServiceCaBundleMountPath,
			ReadOnly:  true,
		})
	}
}

func isPlatformTracingCollectorEndpoint(endpoint string) bool {
	parsed, err := url.Parse(endpoint)
	if err != nil || !strings.EqualFold(parsed.Scheme, "https") || parsed.Port() != platformTracingPort {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return strings.HasPrefix(host, platformTracingCollectorService+".") && strings.HasSuffix(host, ".svc.cluster.local")
}

func usesOTLPTraceEndpoint(container *corev1.Container) bool {
	return hasEnvVar(container.Env, tracing.EnvOtelExporterEndpoint) || hasEnvVar(container.Env, tracesEndpointEnvVar)
}

func hasEnvVar(env []corev1.EnvVar, name string) bool {
	for i := range env {
		if env[i].Name == name {
			return true
		}
	}
	return false
}

func hasEnvValue(env []corev1.EnvVar, name, value string) bool {
	for i := range env {
		if env[i].Name == name && env[i].Value == value && env[i].ValueFrom == nil {
			return true
		}
	}
	return false
}

func hasVolume(volumes []corev1.Volume, name string) bool {
	for i := range volumes {
		if volumes[i].Name == name {
			return true
		}
	}
	return false
}

func hasVolumeMount(mounts []corev1.VolumeMount, name, path string) bool {
	for i := range mounts {
		if mounts[i].Name == name && mounts[i].MountPath == path {
			return true
		}
	}
	return false
}
