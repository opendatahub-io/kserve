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
)

const (
	platformTracingCollectorService = "data-science-collector-collector-headless"
	platformTracingPort             = "4317"
	tracesCertificateEnvVar         = "OTEL_EXPORTER_OTLP_TRACES_CERTIFICATE"
	globalCertificateEnvVar         = "OTEL_EXPORTER_OTLP_CERTIFICATE"
	tracesEndpointEnvVar            = "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"
	genericEndpointEnvVar           = "OTEL_EXPORTER_OTLP_ENDPOINT"
	platformTracingCertFile         = constants.ServiceCaBundleMountPath + "/" + constants.ServiceCaBundleCertFile
)

// configureTracingTLS adds the OpenShift service-ca bundle and points OTLP trace
// exporters at it when both the configured and effective container endpoints use
// the platform-managed collector.
func configureTracingTLS(t *v1alpha2.TracingSpec, podSpec *corev1.PodSpec) {
	if t == nil || podSpec == nil || !isPlatformTracingCollectorEndpoint(ptr.Deref(t.ExporterEndpoint, "")) {
		return
	}

	needsServiceCA := false
	for i := range podSpec.Containers {
		container := &podSpec.Containers[i]
		endpoint, ok := effectiveOTLPTraceEndpoint(container)
		if !ok || !isPlatformTracingCollectorEndpoint(endpoint) {
			continue
		}
		needsServiceCA = configureTraceCertificateEnvVars(container) || needsServiceCA
	}
	if !needsServiceCA {
		return
	}

	ensureServiceCaVolume(podSpec)

	for i := range podSpec.Containers {
		container := &podSpec.Containers[i]
		endpoint, ok := effectiveOTLPTraceEndpoint(container)
		if !ok || !isPlatformTracingCollectorEndpoint(endpoint) || !hasPlatformTracingCertificate(container.Env) {
			continue
		}
		ensureServiceCaMount(container)
	}
}

func isPlatformTracingCollectorEndpoint(endpoint string) bool {
	parsed, err := url.Parse(endpoint)
	if err != nil || !strings.EqualFold(parsed.Scheme, "https") || parsed.Port() != platformTracingPort {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	prefix := platformTracingCollectorService + "."
	suffix := ".svc.cluster.local"
	if !strings.HasPrefix(host, prefix) || !strings.HasSuffix(host, suffix) {
		return false
	}
	namespace := strings.TrimSuffix(strings.TrimPrefix(host, prefix), suffix)
	return namespace != "" && !strings.Contains(namespace, ".")
}

// effectiveOTLPTraceEndpoint resolves the per-signal endpoint before the
// generic endpoint. An unresolved ValueFrom could point at an external
// collector, so it is not safe to inject the platform CA in that case.
func effectiveOTLPTraceEndpoint(container *corev1.Container) (string, bool) {
	if endpoint, found := findEnvVar(container.Env, tracesEndpointEnvVar); found {
		if endpoint.ValueFrom != nil {
			return "", false
		}
		if endpoint.Value != "" {
			return endpoint.Value, true
		}
	}
	if endpoint, found := findEnvVar(container.Env, genericEndpointEnvVar); found {
		if endpoint.ValueFrom != nil {
			return "", false
		}
		if endpoint.Value != "" {
			return endpoint.Value, true
		}
	}
	return "", false
}

// configureTraceCertificateEnvVars preserves explicitly configured trust and
// mirrors a traces-specific certificate to the generic variable used by some
// vLLM exporter versions. Empty variables are treated as unset.
func configureTraceCertificateEnvVars(container *corev1.Container) bool {
	tracesCert, tracesFound := findEnvVar(container.Env, tracesCertificateEnvVar)
	globalCert, globalFound := findEnvVar(container.Env, globalCertificateEnvVar)
	tracesConfigured := isConfiguredEnvVar(tracesCert, tracesFound)
	globalConfigured := isConfiguredEnvVar(globalCert, globalFound)

	switch {
	case tracesConfigured && !globalConfigured:
		container.Env = setEnvVar(container.Env, mirrorEnvVar(globalCertificateEnvVar, tracesCert))
	case !tracesConfigured && !globalConfigured:
		cert := corev1.EnvVar{Name: tracesCertificateEnvVar, Value: platformTracingCertFile}
		container.Env = setEnvVar(container.Env, cert)
		container.Env = setEnvVar(container.Env, corev1.EnvVar{Name: globalCertificateEnvVar, Value: platformTracingCertFile})
	}

	return isPlatformCertificateEnvVar(tracesCert, tracesFound) || isPlatformCertificateEnvVar(globalCert, globalFound) ||
		(!tracesConfigured && !globalConfigured)
}

func isConfiguredEnvVar(envVar corev1.EnvVar, found bool) bool {
	return found && (envVar.ValueFrom != nil || envVar.Value != "")
}

func isPlatformCertificateEnvVar(envVar corev1.EnvVar, found bool) bool {
	return found && envVar.ValueFrom == nil && envVar.Value == platformTracingCertFile
}

func mirrorEnvVar(name string, source corev1.EnvVar) corev1.EnvVar {
	source.Name = name
	return source
}

func findEnvVar(env []corev1.EnvVar, name string) (corev1.EnvVar, bool) {
	for i := range env {
		if env[i].Name == name {
			return env[i], true
		}
	}
	return corev1.EnvVar{}, false
}

func setEnvVar(env []corev1.EnvVar, value corev1.EnvVar) []corev1.EnvVar {
	for i := range env {
		if env[i].Name == value.Name {
			if env[i].Value == "" && env[i].ValueFrom == nil {
				env[i] = value
			}
			return env
		}
	}
	return append(env, value)
}

func hasPlatformTracingCertificate(env []corev1.EnvVar) bool {
	tracesCert, tracesFound := findEnvVar(env, tracesCertificateEnvVar)
	globalCert, globalFound := findEnvVar(env, globalCertificateEnvVar)
	return isPlatformCertificateEnvVar(tracesCert, tracesFound) || isPlatformCertificateEnvVar(globalCert, globalFound)
}

func ensureServiceCaVolume(podSpec *corev1.PodSpec) {
	volume := corev1.Volume{
		Name: constants.ServiceCaBundleVolumeName,
		VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: constants.OpenShiftServiceCaConfigMapName},
			},
		},
	}
	for i := range podSpec.Volumes {
		if podSpec.Volumes[i].Name != constants.ServiceCaBundleVolumeName {
			continue
		}
		if !volumeProvidesServiceCaCert(podSpec.Volumes[i]) {
			// The service-CA volume name is reserved for this platform bundle.
			podSpec.Volumes[i] = volume
		}
		return
	}
	podSpec.Volumes = append(podSpec.Volumes, volume)
}

func volumeProvidesServiceCaCert(volume corev1.Volume) bool {
	if volume.ConfigMap == nil || volume.ConfigMap.Name != constants.OpenShiftServiceCaConfigMapName {
		return false
	}
	if volume.ConfigMap.Optional != nil && *volume.ConfigMap.Optional {
		return false
	}
	if volume.ConfigMap.Items == nil {
		return true
	}
	for _, item := range volume.ConfigMap.Items {
		if item.Key == constants.ServiceCaBundleCertFile && item.Path == constants.ServiceCaBundleCertFile {
			return true
		}
	}
	return false
}

func ensureServiceCaMount(container *corev1.Container) {
	mount := corev1.VolumeMount{
		Name:      constants.ServiceCaBundleVolumeName,
		MountPath: constants.ServiceCaBundleMountPath,
		ReadOnly:  true,
	}
	found := false
	mounts := make([]corev1.VolumeMount, 0, len(container.VolumeMounts)+1)
	for _, existing := range container.VolumeMounts {
		if existing.MountPath != constants.ServiceCaBundleMountPath {
			mounts = append(mounts, existing)
			continue
		}
		if !found {
			mounts = append(mounts, mount)
			found = true
		}
	}
	if !found {
		mounts = append(mounts, mount)
	}
	container.VolumeMounts = mounts
}
