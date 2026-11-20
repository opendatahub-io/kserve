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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

func TestExpectedSchedulerDeploymentInjectsTracingTLS(t *testing.T) {
	g := NewWithT(t)
	llmSvc := &v1alpha2.LLMInferenceService{
		ObjectMeta: metav1.ObjectMeta{Name: "trace-test", Namespace: "test-ns"},
		Spec: v1alpha2.LLMInferenceServiceSpec{
			Tracing: &v1alpha2.TracingSpec{ExporterEndpoint: ptr.To(platformTracingEndpoint)},
			Router: &v1alpha2.RouterSpec{
				Scheduler: &v1alpha2.SchedulerSpec{
					Template: &corev1.PodSpec{Containers: []corev1.Container{{Name: "main", Image: "epp"}}},
				},
			},
		},
	}
	reconciler := &LLMISVCReconciler{Client: roleTestClient(t, roleTestInferenceServiceConfigMap())}

	deployment, err := reconciler.expectedSchedulerDeployment(t.Context(), llmSvc)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(deployment).NotTo(BeNil())

	assertTracingTLSConfigured(g, &deployment.Spec.Template.Spec, "main")
	mainContainer := &deployment.Spec.Template.Spec.Containers[0]
	g.Expect(envToMap(mainContainer.Env)).To(HaveKeyWithValue(globalCertificateEnvVar, platformTracingCertFile))
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

func TestConfigureTracingTLSUsesEffectiveContainerEndpoint(t *testing.T) {
	g := NewWithT(t)
	tracing := &v1alpha2.TracingSpec{ExporterEndpoint: ptr.To(platformTracingEndpoint)}
	podSpec := &corev1.PodSpec{Containers: []corev1.Container{
		{
			Name: "platform-exporter",
			Env:  []corev1.EnvVar{{Name: genericEndpointEnvVar, Value: platformTracingEndpoint}},
		},
		{
			Name: "external-exporter",
			Env: []corev1.EnvVar{
				{Name: genericEndpointEnvVar, Value: platformTracingEndpoint},
				{Name: tracesEndpointEnvVar, Value: "https://collector.example.com:4317"},
			},
		},
	}}

	configureTracingTLS(tracing, podSpec)

	assertTracingTLSConfigured(g, podSpec, "platform-exporter")
	external := podSpec.Containers[1]
	externalEnv := envToMap(external.Env)
	g.Expect(externalEnv).NotTo(HaveKey(tracesCertificateEnvVar))
	g.Expect(externalEnv).NotTo(HaveKey(globalCertificateEnvVar))
	g.Expect(external.VolumeMounts).To(BeEmpty())
}

func TestConfigureTracingTLSTreatsEmptyCertificatesAsUnset(t *testing.T) {
	g := NewWithT(t)
	podSpec := &corev1.PodSpec{Containers: []corev1.Container{{
		Name: "main",
		Env: []corev1.EnvVar{
			{Name: genericEndpointEnvVar, Value: platformTracingEndpoint},
			{Name: tracesCertificateEnvVar},
			{Name: globalCertificateEnvVar, Value: ""},
		},
	}}}

	configureTracingTLS(&v1alpha2.TracingSpec{ExporterEndpoint: ptr.To(platformTracingEndpoint)}, podSpec)

	assertTracingTLSConfigured(g, podSpec, "main")
	g.Expect(envToMap(podSpec.Containers[0].Env)).To(HaveKeyWithValue(globalCertificateEnvVar, platformTracingCertFile))
}

func TestConfigureTracingTLSMountsPreconfiguredPlatformCertificate(t *testing.T) {
	g := NewWithT(t)
	podSpec := &corev1.PodSpec{Containers: []corev1.Container{{
		Name: "main",
		Env: []corev1.EnvVar{
			{Name: genericEndpointEnvVar, Value: platformTracingEndpoint},
			{Name: tracesCertificateEnvVar, Value: platformTracingCertFile},
		},
	}}}

	configureTracingTLS(&v1alpha2.TracingSpec{ExporterEndpoint: ptr.To(platformTracingEndpoint)}, podSpec)

	assertTracingTLSConfigured(g, podSpec, "main")
}

func TestConfigureTracingTLSReplacesConflictingServiceCAMountAndVolume(t *testing.T) {
	g := NewWithT(t)
	podSpec := &corev1.PodSpec{
		Volumes: []corev1.Volume{{
			Name: constants.ServiceCaBundleVolumeName,
			VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
				SecretName: "unrelated-secret",
			}},
		}},
		Containers: []corev1.Container{{
			Name: "main",
			Env:  []corev1.EnvVar{{Name: genericEndpointEnvVar, Value: platformTracingEndpoint}},
			VolumeMounts: []corev1.VolumeMount{
				{Name: "custom-ca", MountPath: constants.ServiceCaBundleMountPath},
				{Name: constants.ServiceCaBundleVolumeName, MountPath: constants.ServiceCaBundleMountPath},
			},
		}},
	}

	configureTracingTLS(&v1alpha2.TracingSpec{ExporterEndpoint: ptr.To(platformTracingEndpoint)}, podSpec)

	g.Expect(podSpec.Volumes).To(ContainElement(corev1.Volume{
		Name: constants.ServiceCaBundleVolumeName,
		VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
			LocalObjectReference: corev1.LocalObjectReference{Name: constants.OpenShiftServiceCaConfigMapName},
		}},
	}))
	g.Expect(podSpec.Containers[0].VolumeMounts).To(Equal([]corev1.VolumeMount{{
		Name:      constants.ServiceCaBundleVolumeName,
		MountPath: constants.ServiceCaBundleMountPath,
		ReadOnly:  true,
	}}))
}

func TestConfigureTracingTLSMirrorsTracesCertificateForVLLM(t *testing.T) {
	g := NewWithT(t)
	podSpec := &corev1.PodSpec{Containers: []corev1.Container{{
		Name: "main",
		Env: []corev1.EnvVar{
			{Name: genericEndpointEnvVar, Value: platformTracingEndpoint},
			{Name: tracesCertificateEnvVar, Value: "/custom/ca.crt"},
		},
	}}}

	configureTracingTLS(&v1alpha2.TracingSpec{ExporterEndpoint: ptr.To(platformTracingEndpoint)}, podSpec)

	g.Expect(envToMap(podSpec.Containers[0].Env)).To(HaveKeyWithValue(globalCertificateEnvVar, "/custom/ca.crt"))
	g.Expect(podSpec.Volumes).To(BeEmpty())
	g.Expect(podSpec.Containers[0].VolumeMounts).To(BeEmpty())
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
