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
	"context"
	"crypto/tls"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type fakeAPIServerClient struct {
	err error
	obj *configv1.APIServer
}

func (f *fakeAPIServerClient) Get(_ context.Context, _ client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
	if f.err != nil {
		return f.err
	}
	if f.obj == nil {
		return apierrors.NewNotFound(schema.GroupResource{Group: "config.openshift.io", Resource: "apiservers"}, "cluster")
	}
	*(obj.(*configv1.APIServer)) = *f.obj
	return nil
}

func (f *fakeAPIServerClient) List(_ context.Context, _ client.ObjectList, _ ...client.ListOption) error {
	return nil
}

func TestResolveClusterProfile_Forbidden_UsesHardenedDefaults(t *testing.T) {
	gr := schema.GroupResource{Group: "config.openshift.io", Resource: "apiservers"}
	result, err := resolveClusterProfile(context.Background(), &fakeAPIServerClient{
		err: apierrors.NewForbidden(gr, "cluster", nil),
	})
	if err != nil {
		t.Fatalf("resolveClusterProfile() error = %v, expected graceful fallback", err)
	}
	if result.ProfileFetched {
		t.Fatal("expected ProfileFetched=false on Forbidden")
	}
	if result.APIAvailable {
		t.Fatal("expected APIAvailable=false on Forbidden")
	}
	assertIntermediateTLS(t, result)
}

func TestResolveClusterProfile_Unauthorized_UsesHardenedDefaults(t *testing.T) {
	result, err := resolveClusterProfile(context.Background(), &fakeAPIServerClient{
		err: apierrors.NewUnauthorized("not authenticated"),
	})
	if err != nil {
		t.Fatalf("resolveClusterProfile() error = %v, expected graceful fallback", err)
	}
	if result.ProfileFetched {
		t.Fatal("expected ProfileFetched=false on Unauthorized")
	}
	if result.APIAvailable {
		t.Fatal("expected APIAvailable=false on Unauthorized")
	}
	assertIntermediateTLS(t, result)
}

func TestResolveClusterProfile_TransientError_EnablesWatcherSelfHeal(t *testing.T) {
	result, err := resolveClusterProfile(context.Background(), &fakeAPIServerClient{
		err: apierrors.NewServiceUnavailable("api down"),
	})
	if err != nil {
		t.Fatalf("resolveClusterProfile() error = %v, expected graceful fallback", err)
	}
	if result.ProfileFetched {
		t.Fatal("expected ProfileFetched=false after transient retry exhaustion")
	}
	if !result.APIAvailable {
		t.Fatal("expected APIAvailable=true after transient retry exhaustion")
	}
	assertIntermediateTLS(t, result)
}

func TestResolveClusterProfile_NilTLSProfile_NoPanic(t *testing.T) {
	apiServer := &configv1.APIServer{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec: configv1.APIServerSpec{
			TLSAdherence: configv1.TLSAdherencePolicyStrictAllComponents,
		},
	}
	result, err := resolveClusterProfile(context.Background(), &fakeAPIServerClient{obj: apiServer})
	if err != nil {
		t.Fatalf("resolveClusterProfile() error = %v", err)
	}
	if !result.ProfileFetched {
		t.Fatal("expected ProfileFetched=true")
	}
	if !result.APIAvailable {
		t.Fatal("expected APIAvailable=true")
	}
	assertIntermediateTLS(t, result)
}

func TestFinalizeClusterTLSOpts_TLS13IgnoresCiphers(t *testing.T) {
	profile := &configv1.TLSSecurityProfile{
		Type: configv1.TLSProfileCustomType,
		Custom: &configv1.CustomTLSProfile{
			TLSProfileSpec: configv1.TLSProfileSpec{
				MinTLSVersion: configv1.VersionTLS13,
				Ciphers:       []string{"TLS_AES_128_GCM_SHA256"},
			},
		},
	}
	minVersion, ciphers := parseProfile(profile)
	minVersion, ciphers = finalizeClusterTLSOpts(minVersion, ciphers)
	if minVersion != tls.VersionTLS13 {
		t.Fatalf("expected TLS 1.3, got %d", minVersion)
	}
	if len(ciphers) != 0 {
		t.Fatalf("expected cipher suites to be ignored for TLS 1.3, got %v", ciphers)
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	tlsOptsFrom(minVersion, ciphers)[0](cfg)
	if cfg.MinVersion != tls.VersionTLS13 {
		t.Fatalf("expected TLS 1.3 in tls.Config, got %d", cfg.MinVersion)
	}
	if len(cfg.CipherSuites) != 0 {
		t.Fatalf("expected no cipher suites in tls.Config, got %v", cfg.CipherSuites)
	}
}

func assertIntermediateTLS(t *testing.T, result Result) {
	t.Helper()
	if len(result.TLSOpts) == 0 {
		t.Fatal("expected TLS options")
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	result.TLSOpts[0](cfg)
	if cfg.MinVersion != tls.VersionTLS12 {
		t.Fatalf("expected TLS 1.2 fallback, got %d", cfg.MinVersion)
	}
}
