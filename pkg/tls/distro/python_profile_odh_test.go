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

package distro

import (
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/kserve/kserve/pkg/constants"
)

func TestInjectTLSSecurityProfileProvider(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, configv1.AddToScheme(scheme))
	reader := fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(&configv1.APIServer{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec: configv1.APIServerSpec{
			TLSAdherence: configv1.TLSAdherencePolicyStrictAllComponents,
			TLSSecurityProfile: &configv1.TLSSecurityProfile{Type: configv1.TLSProfileCustomType, Custom: &configv1.CustomTLSProfile{
				TLSProfileSpec: configv1.TLSProfileSpec{
					MinTLSVersion: configv1.VersionTLS12,
					Ciphers:       []string{"ECDHE-RSA-AES128-GCM-SHA256"},
				},
			}},
		},
	}).Build()
	podSpec := &corev1.PodSpec{Containers: []corev1.Container{{
		Name: constants.InferenceServiceContainerName,
		Env: []corev1.EnvVar{
			{Name: constants.TransformerTLSCertEnvVar, Value: "/tls/tls.crt"},
			{Name: constants.TransformerTLSKeyEnvVar, Value: "/tls/tls.key"},
		},
	}, {Name: "sidecar"}}}

	require.NoError(t, InjectPythonTLSProfile(t.Context(), reader, podSpec))
	require.Equal(t, map[string]string{
		constants.TransformerTLSCertEnvVar: "/tls/tls.crt",
		constants.TransformerTLSKeyEnvVar:  "/tls/tls.key",
		tlsProfileProviderEnv:              tlsProfileProvider,
		tlsMinVersionEnv:                   "VersionTLS12",
		tlsCiphersEnv:                      "TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256",
	}, envValues(podSpec.Containers[0]))
	require.Empty(t, podSpec.Containers[1].Env)
}

func TestInjectTLSSecurityProfileProviderSkipsNonTLSContainers(t *testing.T) {
	podSpec := &corev1.PodSpec{Containers: []corev1.Container{{Name: "sidecar"}}}

	require.NoError(t, InjectPythonTLSProfile(t.Context(), nil, podSpec))
	require.Empty(t, podSpec.Containers[0].Env)
}

func envValues(container corev1.Container) map[string]string {
	values := make(map[string]string, len(container.Env))
	for _, env := range container.Env {
		values[env.Name] = env.Value
	}
	return values
}
