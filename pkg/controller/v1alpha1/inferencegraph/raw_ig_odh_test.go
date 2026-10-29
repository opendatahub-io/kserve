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

package inferencegraph

import (
	"encoding/json"
	"testing"

	"github.com/google/go-cmp/cmp"
	"google.golang.org/protobuf/proto"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
	"github.com/kserve/kserve/pkg/constants"
)

const odhTestGraphName = "basic-ig"

var (
	odhTestRouterConfig = RouterConfig{
		Image:         "kserve/router:v0.10.0",
		CpuRequest:    "100m",
		CpuLimit:      "100m",
		MemoryRequest: "100Mi",
		MemoryLimit:   "500Mi",
	}

	odhTestRouterConfigWithHeaders = RouterConfig{
		Image:         "kserve/router:v0.10.0",
		CpuRequest:    "100m",
		CpuLimit:      "100m",
		MemoryRequest: "100Mi",
		MemoryLimit:   "500Mi",
		Headers: map[string][]string{
			"propagate": {
				"Authorization",
				"Intuit_tid",
			},
		},
	}

	odhSSLCertFileEnv = corev1.EnvVar{
		Name:  "SSL_CERT_FILE",
		Value: constants.ServiceCaBundleMountPath + "/" + constants.ServiceCaBundleCertFile,
	}

	odhPropagateHeadersEnv = corev1.EnvVar{
		Name:  "PROPAGATE_HEADERS",
		Value: "Authorization,Intuit_tid",
	}

	odhServiceCaBundleMount = corev1.VolumeMount{
		Name:      constants.ServiceCaBundleVolumeName,
		MountPath: constants.ServiceCaBundleMountPath,
	}

	odhServiceCaBundleVolume = corev1.Volume{
		Name: constants.ServiceCaBundleVolumeName,
		VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: constants.OpenShiftServiceCaConfigMapName,
				},
			},
		},
	}
)

func odhTestGraph(annotations map[string]string, serviceAccountName string) *v1alpha1.InferenceGraph {
	return &v1alpha1.InferenceGraph{
		ObjectMeta: metav1.ObjectMeta{
			Name:        odhTestGraphName,
			Namespace:   "basic-ig-namespace",
			Annotations: annotations,
		},
		Spec: v1alpha1.InferenceGraphSpec{
			Nodes: map[string]v1alpha1.InferenceRouter{
				v1alpha1.GraphRootNodeName: {
					RouterType: v1alpha1.Sequence,
					Steps: []v1alpha1.InferenceStep{
						{
							InferenceTarget: v1alpha1.InferenceTarget{
								ServiceURL: "http://someservice.example.com",
							},
						},
					},
				},
			},
			ServiceAccountName: serviceAccountName,
		},
	}
}

func odhTestGraphJSON(t *testing.T, graph *v1alpha1.InferenceGraph) string {
	t.Helper()
	bytes, err := json.Marshal(graph.Spec)
	if err != nil {
		t.Fatalf("failed to marshal graph spec: %v", err)
	}
	return string(bytes)
}

func odhExpectedRouterPodSpec(args []string, env []corev1.EnvVar, serviceAccountName string, automountServiceAccountToken bool) *corev1.PodSpec {
	readinessProbe := constants.GetRouterReadinessProbe()
	readinessProbe.HTTPGet.Scheme = corev1.URISchemeHTTPS

	return &corev1.PodSpec{
		Containers: []corev1.Container{
			{
				Image: "kserve/router:v0.10.0",
				Name:  odhTestGraphName,
				Args:  args,
				Env:   env,
				Resources: corev1.ResourceRequirements{
					Limits: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("100m"),
						corev1.ResourceMemory: resource.MustParse("500Mi"),
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("100m"),
						corev1.ResourceMemory: resource.MustParse("100Mi"),
					},
				},
				ReadinessProbe: readinessProbe,
				SecurityContext: &corev1.SecurityContext{
					Privileged:               proto.Bool(false),
					RunAsNonRoot:             proto.Bool(true),
					ReadOnlyRootFilesystem:   proto.Bool(true),
					AllowPrivilegeEscalation: proto.Bool(false),
					Capabilities: &corev1.Capabilities{
						Drop: []corev1.Capability{corev1.Capability("ALL")},
					},
				},
				VolumeMounts: []corev1.VolumeMount{odhServiceCaBundleMount},
			},
		},
		Volumes:                      []corev1.Volume{odhServiceCaBundleVolume},
		AutomountServiceAccountToken: proto.Bool(automountServiceAccountToken),
		ServiceAccountName:           serviceAccountName,
		ImagePullSecrets:             []corev1.LocalObjectReference{},
	}
}

func TestCustomizeRouterPodSpec(t *testing.T) {
	authEnabled := map[string]string{constants.ODHKserveRawAuth: "true"}
	authDisabled := map[string]string{constants.ODHKserveRawAuth: "false"}

	scenarios := []struct {
		name     string
		graph    *v1alpha1.InferenceGraph
		config   *RouterConfig
		expected func(graphJSON string) *corev1.PodSpec
	}{
		{
			name:   "TLS and service CA bundle go ahead of the upstream args and env",
			graph:  odhTestGraph(nil, ""),
			config: &odhTestRouterConfig,
			expected: func(graphJSON string) *corev1.PodSpec {
				return odhExpectedRouterPodSpec(
					[]string{"--enable-tls", "--graph-json", graphJSON},
					[]corev1.EnvVar{odhSSLCertFileEnv},
					"default", false)
			},
		},
		{
			name:   "SSL_CERT_FILE precedes PROPAGATE_HEADERS",
			graph:  odhTestGraph(nil, ""),
			config: &odhTestRouterConfigWithHeaders,
			expected: func(graphJSON string) *corev1.PodSpec {
				return odhExpectedRouterPodSpec(
					[]string{"--enable-tls", "--graph-json", graphJSON},
					[]corev1.EnvVar{odhSSLCertFileEnv, odhPropagateHeadersEnv},
					"default", false)
			},
		},
		{
			name:   "auth args follow the graph JSON and the router runs as the auth-verifier",
			graph:  odhTestGraph(authEnabled, ""),
			config: &odhTestRouterConfigWithHeaders,
			expected: func(graphJSON string) *corev1.PodSpec {
				return odhExpectedRouterPodSpec(
					[]string{"--enable-tls", "--graph-json", graphJSON, "--enable-auth", "--inferencegraph-name", odhTestGraphName},
					[]corev1.EnvVar{odhSSLCertFileEnv, odhPropagateHeadersEnv},
					odhTestGraphName+"-auth-verifier", true)
			},
		},
		{
			name:   "auth annotation other than true keeps auth off",
			graph:  odhTestGraph(authDisabled, ""),
			config: &odhTestRouterConfig,
			expected: func(graphJSON string) *corev1.PodSpec {
				return odhExpectedRouterPodSpec(
					[]string{"--enable-tls", "--graph-json", graphJSON},
					[]corev1.EnvVar{odhSSLCertFileEnv},
					"default", false)
			},
		},
		{
			name:   "the default ServiceAccount replaces spec.serviceAccountName",
			graph:  odhTestGraph(nil, "custom-sa"),
			config: &odhTestRouterConfig,
			expected: func(graphJSON string) *corev1.PodSpec {
				return odhExpectedRouterPodSpec(
					[]string{"--enable-tls", "--graph-json", graphJSON},
					[]corev1.EnvVar{odhSSLCertFileEnv},
					"default", false)
			},
		},
	}

	for _, tt := range scenarios {
		t.Run(tt.name, func(t *testing.T) {
			result := createInferenceGraphPodSpec(tt.graph, tt.config)
			customizeRouterPodSpec(tt.graph, result)

			if diff := cmp.Diff(tt.expected(odhTestGraphJSON(t, tt.graph)), result); diff != "" {
				t.Errorf("unexpected router pod spec (-want +got): %v", diff)
			}
		})
	}
}

func TestCustomizeRouterPodSpecToleratesMissingPodSpec(t *testing.T) {
	graph := odhTestGraph(map[string]string{constants.ODHKserveRawAuth: "true"}, "")

	customizeRouterPodSpec(graph, nil)

	empty := &corev1.PodSpec{}
	customizeRouterPodSpec(graph, empty)
	if diff := cmp.Diff(&corev1.PodSpec{}, empty); diff != "" {
		t.Errorf("pod spec without containers should stay untouched (-want +got): %v", diff)
	}

	withoutProbe := &corev1.PodSpec{Containers: []corev1.Container{{Name: odhTestGraphName}}}
	customizeRouterPodSpec(graph, withoutProbe)
	if withoutProbe.Containers[0].ReadinessProbe != nil {
		t.Errorf("readiness probe should not be created, got %v", withoutProbe.Containers[0].ReadinessProbe)
	}
}
