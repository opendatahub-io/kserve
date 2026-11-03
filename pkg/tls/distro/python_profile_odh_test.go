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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
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
	}, {Name: "sidecar"}, {
		Name: "underscore-server",
		Args: []string{"--ssl_certfile", "/tls/tls.crt", "--ssl_keyfile", "/tls/tls.key"},
	}, {
		Name: "dash-server",
		Args: []string{"--ssl-certfile=/tls/tls.crt", "--ssl-keyfile=/tls/tls.key"},
	}, {
		Name: "empty-env-server",
		Env: []corev1.EnvVar{
			{Name: constants.TransformerTLSCertEnvVar},
			{Name: constants.TransformerTLSKeyEnvVar},
		},
	}, {
		Name: "empty-equals-server",
		Args: []string{"--ssl_certfile=", "--ssl_keyfile="},
	}, {
		Name: "empty-separated-server",
		Args: []string{"--ssl_certfile", "", "--ssl_keyfile", ""},
	}, {
		Name: "missing-separated-cert-value-server",
		Args: []string{"--ssl_certfile", "--ssl_keyfile=/tls/tls.key"},
	}, {
		Name: "value-from-server",
		Env: []corev1.EnvVar{
			{
				Name: constants.TransformerTLSCertEnvVar,
				ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: "tls"},
					Key:                  "tls.crt",
				}},
			},
			{
				Name: constants.TransformerTLSKeyEnvVar,
				ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: "tls"},
					Key:                  "tls.key",
				}},
			},
		},
	}, {
		Name: "empty-equals-overrides-env-server",
		Env: []corev1.EnvVar{
			{Name: constants.TransformerTLSCertEnvVar, Value: "/tls/tls.crt"},
			{Name: constants.TransformerTLSKeyEnvVar, Value: "/tls/tls.key"},
		},
		Args: []string{"--ssl_certfile=", "--ssl_keyfile="},
	}, {
		Name: "empty-separated-overrides-env-server",
		Env: []corev1.EnvVar{
			{Name: constants.TransformerTLSCertEnvVar, Value: "/tls/tls.crt"},
			{Name: constants.TransformerTLSKeyEnvVar, Value: "/tls/tls.key"},
		},
		Args: []string{"--ssl_certfile", "", "--ssl_keyfile", ""},
	}, {
		Name: "repeated-flags-last-empty-server",
		Env: []corev1.EnvVar{
			{Name: constants.TransformerTLSCertEnvVar, Value: "/tls/tls.crt"},
			{Name: constants.TransformerTLSKeyEnvVar, Value: "/tls/tls.key"},
		},
		Args: []string{
			"--ssl_certfile=/tls/tls.crt", "--ssl_certfile=",
			"--ssl_keyfile=/tls/tls.key", "--ssl_keyfile=",
		},
	}, {
		Name: "repeated-flags-last-non-empty-server",
		Args: []string{
			"--ssl_certfile=", "--ssl_certfile=/tls/tls.crt",
			"--ssl_keyfile=", "--ssl_keyfile=/tls/tls.key",
		},
	}}}

	require.NoError(t, InjectPythonTLSProfile(t.Context(), reader, podSpec))
	require.Equal(t, map[string]string{
		constants.TransformerTLSCertEnvVar: "/tls/tls.crt",
		constants.TransformerTLSKeyEnvVar:  "/tls/tls.key",
		tlsProfileProviderEnv:              tlsProfileProvider,
		tlsMinVersionEnv:                   "VersionTLS12",
		tlsCiphersEnv:                      "ECDHE-RSA-AES128-GCM-SHA256",
	}, envValues(podSpec.Containers[0]))
	require.Empty(t, podSpec.Containers[1].Env)
	require.NotContains(t, envValues(podSpec.Containers[4]), tlsProfileProviderEnv)
	require.NotContains(t, envValues(podSpec.Containers[5]), tlsProfileProviderEnv)
	require.NotContains(t, envValues(podSpec.Containers[6]), tlsProfileProviderEnv)
	require.NotContains(t, envValues(podSpec.Containers[7]), tlsProfileProviderEnv)
	require.Equal(t, tlsProfileProvider, envValues(podSpec.Containers[8])[tlsProfileProviderEnv])
	for _, i := range []int{9, 10, 11} {
		require.NotContains(t, envValues(podSpec.Containers[i]), tlsProfileProviderEnv)
	}
	require.Equal(t, tlsProfileProvider, envValues(podSpec.Containers[12])[tlsProfileProviderEnv])
	require.Equal(t, map[string]string{
		tlsProfileProviderEnv: tlsProfileProvider,
		tlsMinVersionEnv:      "VersionTLS12",
		tlsCiphersEnv:         "ECDHE-RSA-AES128-GCM-SHA256",
	}, envValues(podSpec.Containers[2]))
	require.Empty(t, podSpec.Containers[3].Env)
}

func TestInjectTLSSecurityProfileProviderSkipsUnavailableAPIServer(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, configv1.AddToScheme(scheme))
	missing := fakeclient.NewClientBuilder().WithScheme(scheme).Build()
	noMatch := &apiMeta.NoKindMatchError{GroupKind: schema.GroupKind{Group: "config.openshift.io", Kind: "APIServer"}}
	forbidden := apierrors.NewForbidden(schema.GroupResource{Group: "config.openshift.io", Resource: "apiservers"}, "cluster", nil)
	tests := []struct {
		name      string
		reader    client.Reader
		wantError bool
	}{
		{name: "nil reader"},
		{name: "missing APIServer", reader: missing},
		{name: "unregistered APIServer", reader: getErrorReader{err: noMatch}},
		{name: "forbidden APIServer", reader: getErrorReader{err: forbidden}},
		{
			name:      "unexpected APIServer error",
			reader:    getErrorReader{err: apierrors.NewServiceUnavailable("APIServer read failed")},
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			podSpec := &corev1.PodSpec{Containers: []corev1.Container{{
				Name: constants.InferenceServiceContainerName,
				Env: []corev1.EnvVar{
					{Name: constants.TransformerTLSCertEnvVar, Value: "/tls/tls.crt"},
					{Name: constants.TransformerTLSKeyEnvVar, Value: "/tls/tls.key"},
				},
			}}}

			err := InjectPythonTLSProfile(t.Context(), tt.reader, podSpec)
			if tt.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, map[string]string{
				constants.TransformerTLSCertEnvVar: "/tls/tls.crt",
				constants.TransformerTLSKeyEnvVar:  "/tls/tls.key",
			}, envValues(podSpec.Containers[0]))
		})
	}
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
