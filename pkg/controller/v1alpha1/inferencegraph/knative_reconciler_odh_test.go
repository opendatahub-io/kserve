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
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"

	"github.com/kserve/kserve/pkg/constants"
)

func TestCustomizeRouterKnativeService(t *testing.T) {
	scenarios := []struct {
		name        string
		annotations map[string]string
		config      *RouterConfig
		expectedEnv []corev1.EnvVar
	}{
		{
			name:        "service CA bundle without propagated headers",
			config:      &odhTestRouterConfig,
			expectedEnv: []corev1.EnvVar{odhSSLCertFileEnv},
		},
		{
			name:        "SSL_CERT_FILE precedes PROPAGATE_HEADERS",
			config:      &odhTestRouterConfigWithHeaders,
			expectedEnv: []corev1.EnvVar{odhSSLCertFileEnv, odhPropagateHeadersEnv},
		},
		{
			name:        "auth annotation does not change the Knative router",
			annotations: map[string]string{constants.ODHKserveRawAuth: "true"},
			config:      &odhTestRouterConfigWithHeaders,
			expectedEnv: []corev1.EnvVar{odhSSLCertFileEnv, odhPropagateHeadersEnv},
		},
	}

	for _, tt := range scenarios {
		t.Run(tt.name, func(t *testing.T) {
			graph := odhTestGraph(tt.annotations, "custom-sa")

			ksvc := createKnativeService(graph.ObjectMeta, graph, tt.config)
			customizeRouterKnativeService(graph, ksvc)

			podSpec := ksvc.Spec.Template.Spec.PodSpec
			router := podSpec.Containers[0]
			if diff := cmp.Diff([]string{"--graph-json", odhTestGraphJSON(t, graph)}, router.Args); diff != "" {
				t.Errorf("unexpected router args (-want +got): %v", diff)
			}
			if diff := cmp.Diff(tt.expectedEnv, router.Env); diff != "" {
				t.Errorf("unexpected router env (-want +got): %v", diff)
			}
			if diff := cmp.Diff([]corev1.VolumeMount{odhServiceCaBundleMount}, router.VolumeMounts); diff != "" {
				t.Errorf("unexpected router volume mounts (-want +got): %v", diff)
			}
			if diff := cmp.Diff([]corev1.Volume{odhServiceCaBundleVolume}, podSpec.Volumes); diff != "" {
				t.Errorf("unexpected volumes (-want +got): %v", diff)
			}
			if router.ReadinessProbe.HTTPGet.Scheme != corev1.URISchemeHTTP {
				t.Errorf("Knative router readiness probe should stay on HTTP, got %q", router.ReadinessProbe.HTTPGet.Scheme)
			}
			if podSpec.ServiceAccountName != "custom-sa" {
				t.Errorf("Knative router should keep spec.serviceAccountName, got %q", podSpec.ServiceAccountName)
			}
		})
	}
}

func TestCustomizeRouterKnativeServiceToleratesNil(t *testing.T) {
	customizeRouterKnativeService(odhTestGraph(nil, ""), nil)
}
