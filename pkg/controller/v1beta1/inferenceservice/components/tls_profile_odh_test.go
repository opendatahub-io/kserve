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
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
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
	podSpec := &corev1.PodSpec{Containers: []corev1.Container{{Name: "predictor"}, {Name: "sidecar"}}}

	if err := injectTLSSecurityProfile(context.Background(), reader, podSpec); err != nil {
		t.Fatal(err)
	}
	for _, container := range podSpec.Containers {
		assertEnv(t, container, tlsMinVersionEnv, "VersionTLS12")
		assertEnv(t, container, tlsCiphersEnv, "ECDHE-RSA-AES128-GCM-SHA256:ECDHE-RSA-AES256-GCM-SHA384")
	}
}

func TestInjectTLSSecurityProfileSkipsUnavailableAPI(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := configv1.Install(scheme); err != nil {
		t.Fatal(err)
	}
	reader := fake.NewClientBuilder().WithScheme(scheme).Build()
	podSpec := &corev1.PodSpec{Containers: []corev1.Container{{Name: "predictor"}}}

	if err := injectTLSSecurityProfile(context.Background(), reader, podSpec); err != nil {
		t.Fatal(err)
	}
	if len(podSpec.Containers[0].Env) != 0 {
		t.Fatalf("expected workload environment to remain unchanged, got %#v", podSpec.Containers[0].Env)
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
