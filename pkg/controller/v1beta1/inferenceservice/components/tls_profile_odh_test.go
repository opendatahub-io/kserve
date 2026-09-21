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

package components

import (
	"context"
	"strings"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestInjectTLSSecurityProfile(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := configv1.Install(scheme); err != nil {
		t.Fatal(err)
	}
	apiServer := &configv1.APIServer{
		Spec: configv1.APIServerSpec{
			TLSAdherence: configv1.TLSAdherencePolicyStrictAllComponents,
			TLSSecurityProfile: &configv1.TLSSecurityProfile{
				Type: configv1.TLSProfileCustomType,
				Custom: &configv1.CustomTLSProfile{TLSProfileSpec: configv1.TLSProfileSpec{
					MinTLSVersion: configv1.VersionTLS12,
					Ciphers:       []string{"ECDHE-RSA-AES128-GCM-SHA256", "ECDHE-RSA-AES256-GCM-SHA384"},
				}},
			},
		},
	}
	apiServer.Name = apiServerName
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(apiServer).Build()
	podSpec := &corev1.PodSpec{Containers: []corev1.Container{{
		Name: "predictor",
		Env: []corev1.EnvVar{
			{Name: "KSERVE_TLS_CERT_FILE", Value: "/certs/tls.crt"},
			{Name: "KSERVE_TLS_KEY_FILE", Value: "/certs/tls.key"},
		},
	}, {Name: "sidecar"}}}

	if err := injectTLSSecurityProfile(context.Background(), reader, podSpec); err != nil {
		t.Fatal(err)
	}
	assertEnv(t, podSpec.Containers[0], tlsMinVersionEnv, "VersionTLS12")
	assertEnv(t, podSpec.Containers[0], tlsCiphersEnv, "ECDHE-RSA-AES128-GCM-SHA256:ECDHE-RSA-AES256-GCM-SHA384")
	assertNoEnv(t, podSpec.Containers[1], tlsMinVersionEnv)
	assertNoEnv(t, podSpec.Containers[1], tlsCiphersEnv)
}

func TestInjectTLSSecurityProfileUsesIntermediateWhenAPIUnavailable(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := configv1.Install(scheme); err != nil {
		t.Fatal(err)
	}
	reader := fake.NewClientBuilder().WithScheme(scheme).Build()
	podSpec := &corev1.PodSpec{Containers: []corev1.Container{{Name: "predictor", Args: []string{
		"--ssl_certfile=/certs/tls.crt", "--ssl_keyfile", "/certs/tls.key",
	}}}}

	if err := injectTLSSecurityProfile(context.Background(), reader, podSpec); err != nil {
		t.Fatal(err)
	}
	intermediate := configv1.TLSProfiles[configv1.TLSProfileIntermediateType]
	assertEnv(t, podSpec.Containers[0], tlsMinVersionEnv, string(intermediate.MinTLSVersion))
	assertEnv(t, podSpec.Containers[0], tlsCiphersEnv, strings.Join(intermediate.Ciphers, ":"))
}

func TestInjectTLSSecurityProfilePreservesWorkloadOnTransientError(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := configv1.Install(scheme); err != nil {
		t.Fatal(err)
	}
	reader := fake.NewClientBuilder().
		WithScheme(scheme).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
				return apierrors.NewServiceUnavailable("APIServer profile is temporarily unavailable")
			},
		}).
		Build()
	podSpec := &corev1.PodSpec{Containers: []corev1.Container{{
		Name: "predictor",
		Args: []string{"--ssl-certfile", "/certs/tls.crt", "--ssl-keyfile=/certs/tls.key"},
		Env: []corev1.EnvVar{
			{Name: tlsMinVersionEnv, Value: "VersionTLS13"},
			{Name: tlsCiphersEnv, Value: ""},
		},
	}}}

	if err := injectTLSSecurityProfile(context.Background(), reader, podSpec); err == nil {
		t.Fatal("expected transient APIServer error to be returned")
	}
	assertEnv(t, podSpec.Containers[0], tlsMinVersionEnv, "VersionTLS13")
	assertEnv(t, podSpec.Containers[0], tlsCiphersEnv, "")
}

func TestInjectTLSSecurityProfileSkipsNonTLSContainers(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := configv1.Install(scheme); err != nil {
		t.Fatal(err)
	}
	reader := fake.NewClientBuilder().
		WithScheme(scheme).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
				t.Fatal("APIServer profile should not be read for a non-TLS workload")
				return nil
			},
		}).
		Build()
	podSpec := &corev1.PodSpec{Containers: []corev1.Container{{Name: "predictor"}, {Name: "sidecar"}}}

	if err := injectTLSSecurityProfile(context.Background(), reader, podSpec); err != nil {
		t.Fatal(err)
	}
	for _, container := range podSpec.Containers {
		assertNoEnv(t, container, tlsMinVersionEnv)
		assertNoEnv(t, container, tlsCiphersEnv)
	}
}

func assertEnv(t *testing.T, container corev1.Container, name, want string) {
	t.Helper()
	for _, env := range container.Env {
		if env.Name == name {
			if env.Value != want {
				t.Fatalf("%s = %q, want %q", name, env.Value, want)
			}
			return
		}
	}
	t.Fatalf("environment variable %s not found", name)
}

func assertNoEnv(t *testing.T, container corev1.Container, name string) {
	t.Helper()
	for _, env := range container.Env {
		if env.Name == name {
			t.Fatalf("environment variable %s unexpectedly found", name)
		}
	}
}
