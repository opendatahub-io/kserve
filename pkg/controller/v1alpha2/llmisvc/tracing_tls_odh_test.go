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
	"testing"

	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
	"github.com/kserve/kserve/pkg/constants"
)

const platformTracingEndpoint = "https://data-science-collector-collector-headless.redhat-ods-monitoring.svc.cluster.local:4317"

func TestConfigureTracingTLSForScheduler(t *testing.T) {
	g := NewWithT(t)
	tracing := &v1alpha2.TracingSpec{ExporterEndpoint: ptr.To(platformTracingEndpoint)}
	podSpec := &corev1.PodSpec{Containers: []corev1.Container{{Name: "main"}}}

	injectSchedulerTracing(tracing, "test-ns", "my-llm", &podSpec.Containers[0])
	configureTracingTLS(tracing, podSpec)
	configureTracingTLS(tracing, podSpec)

	assertTracingTLSConfigured(g, podSpec, "main")
	g.Expect(podSpec.Containers[0].Args).To(ContainElement("--tracing=true"))
}

func TestConfigureTracingTLSForVLLM(t *testing.T) {
	g := NewWithT(t)
	tracing := &v1alpha2.TracingSpec{ExporterEndpoint: ptr.To(platformTracingEndpoint)}
	podSpec := &corev1.PodSpec{Containers: []corev1.Container{{Name: "main"}}}

	injectServerTracingIntoPodSpec(tracing, "test-ns", "my-llm", "-decode", podSpec)

	assertTracingTLSConfigured(g, podSpec, "main")
	g.Expect(podSpec.Containers[0].Args).To(ContainElement("--otlp-traces-endpoint"))
	g.Expect(podSpec.Containers[0].Args).To(ContainElement(platformTracingEndpoint))
}

func TestConfigureTracingTLSPreservesCustomCertificate(t *testing.T) {
	g := NewWithT(t)
	tracing := &v1alpha2.TracingSpec{ExporterEndpoint: ptr.To(platformTracingEndpoint)}
	podSpec := &corev1.PodSpec{Containers: []corev1.Container{{
		Name: "main",
		Env: []corev1.EnvVar{
			{Name: "OTEL_EXPORTER_OTLP_ENDPOINT", Value: platformTracingEndpoint},
			{Name: globalCertificateEnvVar, Value: "/custom/service-ca.crt"},
		},
	}}}

	configureTracingTLS(tracing, podSpec)

	g.Expect(podSpec.Volumes).To(BeEmpty())
	g.Expect(podSpec.Containers[0].VolumeMounts).To(BeEmpty())
	got := envToMap(podSpec.Containers[0].Env)
	g.Expect(got).To(HaveKeyWithValue(globalCertificateEnvVar, "/custom/service-ca.crt"))
	g.Expect(got).NotTo(HaveKey(tracesCertificateEnvVar))
}

func TestConfigureTracingTLSSkipsNonPlatformEndpoint(t *testing.T) {
	for _, endpoint := range []string{
		"http://data-science-collector-collector-headless.redhat-ods-monitoring.svc.cluster.local:4317",
		"https://collector.example.com:4317",
		"https://data-science-collector-collector.redhat-ods-monitoring.svc.cluster.local:4317",
	} {
		t.Run(endpoint, func(t *testing.T) {
			g := NewWithT(t)
			podSpec := &corev1.PodSpec{Containers: []corev1.Container{{
				Name: "main",
				Env:  []corev1.EnvVar{{Name: "OTEL_EXPORTER_OTLP_ENDPOINT", Value: endpoint}},
			}}}
			before := podSpec.DeepCopy()

			configureTracingTLS(&v1alpha2.TracingSpec{ExporterEndpoint: ptr.To(endpoint)}, podSpec)

			g.Expect(podSpec).To(Equal(before))
		})
	}
}

func assertTracingTLSConfigured(g *WithT, podSpec *corev1.PodSpec, containerName string) {
	containerIndex := -1
	for i := range podSpec.Containers {
		if podSpec.Containers[i].Name == containerName {
			containerIndex = i
			break
		}
	}
	g.Expect(containerIndex).NotTo(Equal(-1))
	container := &podSpec.Containers[containerIndex]

	g.Expect(podSpec.Volumes).To(ContainElement(corev1.Volume{
		Name: constants.ServiceCaBundleVolumeName,
		VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
			LocalObjectReference: corev1.LocalObjectReference{Name: constants.OpenShiftServiceCaConfigMapName},
		}},
	}))
	g.Expect(container.VolumeMounts).To(ContainElement(corev1.VolumeMount{
		Name:      constants.ServiceCaBundleVolumeName,
		MountPath: constants.ServiceCaBundleMountPath,
		ReadOnly:  true,
	}))
	env := envToMap(container.Env)
	g.Expect(env).To(HaveKeyWithValue("OTEL_EXPORTER_OTLP_ENDPOINT", platformTracingEndpoint))
	g.Expect(env).To(HaveKeyWithValue(tracesCertificateEnvVar, platformTracingCertFile))
	g.Expect(podSpec.Volumes).To(HaveLen(1))
	g.Expect(container.VolumeMounts).To(HaveLen(1))
}
