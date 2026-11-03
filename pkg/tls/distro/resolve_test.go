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
	"strings"
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

func TestResolveClusterProfile_AuthorizationErrorsFailClosed(t *testing.T) {
	gr := schema.GroupResource{Group: "config.openshift.io", Resource: "apiservers"}
	for _, tc := range []struct {
		name string
		err  error
		is   func(error) bool
	}{
		{"Forbidden", apierrors.NewForbidden(gr, "cluster", nil), apierrors.IsForbidden},
		{"Unauthorized", apierrors.NewUnauthorized("not authenticated"), apierrors.IsUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := resolveClusterProfile(context.Background(), &fakeAPIServerClient{err: tc.err})
			if !tc.is(err) {
				t.Fatalf("resolveClusterProfile() error = %v, expected authorization error", err)
			}
			if !strings.Contains(err.Error(), "ensure ClusterRole grants get/list/watch on config.openshift.io/apiservers") {
				t.Fatalf("resolveClusterProfile() error missing RBAC hint: %v", err)
			}
		})
	}
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

func TestResolveClusterProfile_MalformedProfileErrorsWhenHonored(t *testing.T) {
	tests := []struct {
		name    string
		profile *configv1.TLSSecurityProfile
		wantErr string
	}{
		{
			name: "nil custom block",
			profile: &configv1.TLSSecurityProfile{
				Type: configv1.TLSProfileCustomType,
			},
			wantErr: string(configv1.TLSProfileCustomType),
		},
		{
			name: "unknown minTLSVersion",
			profile: &configv1.TLSSecurityProfile{
				Type: configv1.TLSProfileCustomType,
				Custom: &configv1.CustomTLSProfile{TLSProfileSpec: configv1.TLSProfileSpec{
					MinTLSVersion: configv1.TLSProtocolVersion("VersionTLS99"),
				}},
			},
			wantErr: "VersionTLS99",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			apiServer := &configv1.APIServer{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Spec: configv1.APIServerSpec{
					TLSAdherence:       configv1.TLSAdherencePolicyStrictAllComponents,
					TLSSecurityProfile: tc.profile,
				},
			}
			if _, err := resolveClusterProfile(context.Background(), &fakeAPIServerClient{obj: apiServer}); err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("resolveClusterProfile() error = %v, want error containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestResolveClusterProfile_EmptyProfileTypeMeansUnset(t *testing.T) {
	apiServer := &configv1.APIServer{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec: configv1.APIServerSpec{
			TLSAdherence:       configv1.TLSAdherencePolicyStrictAllComponents,
			TLSSecurityProfile: &configv1.TLSSecurityProfile{Type: ""},
		},
	}
	result, err := resolveClusterProfile(context.Background(), &fakeAPIServerClient{obj: apiServer})
	if err != nil {
		t.Fatalf("resolveClusterProfile() error = %v, empty Type means an unset profile", err)
	}
	assertIntermediateTLS(t, result)
}

func TestResolveClusterProfile_UnknownProfileTypeUsesIntermediateFallback(t *testing.T) {
	apiServer := &configv1.APIServer{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec: configv1.APIServerSpec{
			TLSAdherence:       configv1.TLSAdherencePolicyStrictAllComponents,
			TLSSecurityProfile: &configv1.TLSSecurityProfile{Type: configv1.TLSProfileType("Bogus")},
		},
	}
	result, err := resolveClusterProfile(context.Background(), &fakeAPIServerClient{obj: apiServer})
	if err != nil {
		t.Fatalf("resolveClusterProfile() error = %v, want Intermediate fallback", err)
	}
	assertIntermediateTLS(t, result)
}

func TestResolveClusterProfile_NonHonoringAdherenceIgnoresMalformedProfile(t *testing.T) {
	apiServer := &configv1.APIServer{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec: configv1.APIServerSpec{
			TLSAdherence:       configv1.TLSAdherencePolicyNoOpinion,
			TLSSecurityProfile: &configv1.TLSSecurityProfile{Type: configv1.TLSProfileType("Bogus")},
		},
	}
	result, err := resolveClusterProfile(context.Background(), &fakeAPIServerClient{obj: apiServer})
	if err != nil {
		t.Fatalf("resolveClusterProfile() error = %v, expected Intermediate fallback", err)
	}
	assertIntermediateTLS(t, result)
}

func TestResolveClusterProfile_OldHonorsConfiguredMinimumAndCiphers(t *testing.T) {
	apiServer := &configv1.APIServer{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec: configv1.APIServerSpec{
			TLSAdherence:       configv1.TLSAdherencePolicyStrictAllComponents,
			TLSSecurityProfile: &configv1.TLSSecurityProfile{Type: configv1.TLSProfileOldType},
		},
	}
	result, err := resolveClusterProfile(context.Background(), &fakeAPIServerClient{obj: apiServer})
	if err != nil {
		t.Fatalf("resolveClusterProfile() error = %v", err)
	}
	cfg := newTestTLSConfig()
	result.TLSOpts[0](cfg)
	if cfg.MinVersion != tls.VersionTLS10 {
		t.Fatalf("expected Old profile minimum TLS 1.0 honored exactly, got %d", cfg.MinVersion)
	}
	if len(cfg.CipherSuites) == 0 {
		t.Fatal("expected explicit cipher suites from the Old profile")
	}
	for _, id := range cfg.CipherSuites {
		if id >= 0x1300 && id <= 0x13ff {
			t.Fatalf("expected no TLS 1.3-only cipher suites below TLS 1.3, got 0x%04x", id)
		}
	}
	// Spot-check both runtime tables feed the usable set: a secure suite and an
	// insecure one (Old's DES-CBC3-SHA alias).
	hasGCM, has3DES := false, false
	for _, id := range cfg.CipherSuites {
		if id == tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256 {
			hasGCM = true
		}
		if id == tls.TLS_RSA_WITH_3DES_EDE_CBC_SHA {
			has3DES = true
		}
	}
	if !hasGCM {
		t.Fatal("expected TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256 in Old profile cipher suites")
	}
	if !has3DES {
		t.Fatal("expected TLS_RSA_WITH_3DES_EDE_CBC_SHA (DES-CBC3-SHA) in Old profile cipher suites")
	}
}

func TestResolveClusterProfile_CustomSubTLS12MinimumHonoredExactly(t *testing.T) {
	for _, tc := range []struct {
		name    string
		version configv1.TLSProtocolVersion
		want    uint16
	}{
		{"VersionTLS10", configv1.VersionTLS10, tls.VersionTLS10},
		{"VersionTLS11", configv1.VersionTLS11, tls.VersionTLS11},
	} {
		t.Run(tc.name, func(t *testing.T) {
			apiServer := &configv1.APIServer{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Spec: configv1.APIServerSpec{
					TLSAdherence: configv1.TLSAdherencePolicyStrictAllComponents,
					TLSSecurityProfile: &configv1.TLSSecurityProfile{
						Type: configv1.TLSProfileCustomType,
						Custom: &configv1.CustomTLSProfile{TLSProfileSpec: configv1.TLSProfileSpec{
							MinTLSVersion: tc.version,
							Ciphers:       []string{"ECDHE-RSA-AES128-GCM-SHA256"},
						}},
					},
				},
			}
			result, err := resolveClusterProfile(context.Background(), &fakeAPIServerClient{obj: apiServer})
			if err != nil {
				t.Fatalf("resolveClusterProfile() error = %v", err)
			}
			cfg := newTestTLSConfig()
			result.TLSOpts[0](cfg)
			if cfg.MinVersion != tc.want {
				t.Fatalf("expected %s honored exactly (%d), got %d", tc.name, tc.want, cfg.MinVersion)
			}
		})
	}
}

func TestResolveClusterProfile_TLS13OnlyCiphersUnusableBelowTLS13(t *testing.T) {
	apiServer := &configv1.APIServer{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec: configv1.APIServerSpec{
			TLSAdherence: configv1.TLSAdherencePolicyStrictAllComponents,
			TLSSecurityProfile: &configv1.TLSSecurityProfile{
				Type: configv1.TLSProfileCustomType,
				Custom: &configv1.CustomTLSProfile{TLSProfileSpec: configv1.TLSProfileSpec{
					MinTLSVersion: configv1.VersionTLS12,
					Ciphers:       []string{"TLS_AES_128_GCM_SHA256", "TLS_CHACHA20_POLY1305_SHA256"},
				}},
			},
		},
	}
	if _, err := resolveClusterProfile(context.Background(), &fakeAPIServerClient{obj: apiServer}); err == nil {
		t.Fatal("expected error when all ciphers are TLS 1.3-only at min version 1.2")
	}
}

func TestResolveClusterProfile_PrunesUnusableCiphersKeepingUsable(t *testing.T) {
	apiServer := &configv1.APIServer{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec: configv1.APIServerSpec{
			TLSAdherence: configv1.TLSAdherencePolicyStrictAllComponents,
			TLSSecurityProfile: &configv1.TLSSecurityProfile{
				Type: configv1.TLSProfileCustomType,
				Custom: &configv1.CustomTLSProfile{TLSProfileSpec: configv1.TLSProfileSpec{
					MinTLSVersion: configv1.VersionTLS12,
					Ciphers:       []string{"TLS_AES_128_GCM_SHA256", "DHE-RSA-AES128-GCM-SHA256", "ECDHE-RSA-AES128-GCM-SHA256"},
				}},
			},
		},
	}
	result, err := resolveClusterProfile(context.Background(), &fakeAPIServerClient{obj: apiServer})
	if err != nil {
		t.Fatalf("resolveClusterProfile() error = %v", err)
	}
	cfg := newTestTLSConfig()
	result.TLSOpts[0](cfg)
	if len(cfg.CipherSuites) != 1 || cfg.CipherSuites[0] != tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256 {
		t.Fatalf("expected only the usable ECDHE suite to survive pruning, got %v", cfg.CipherSuites)
	}
}

func TestResolveClusterProfile_RejectsRC4Ciphers(t *testing.T) {
	apiServer := &configv1.APIServer{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec: configv1.APIServerSpec{
			TLSAdherence: configv1.TLSAdherencePolicyStrictAllComponents,
			TLSSecurityProfile: &configv1.TLSSecurityProfile{
				Type: configv1.TLSProfileCustomType,
				Custom: &configv1.CustomTLSProfile{TLSProfileSpec: configv1.TLSProfileSpec{
					MinTLSVersion: configv1.VersionTLS12,
					Ciphers:       []string{"TLS_RSA_WITH_RC4_128_SHA", "ECDHE-RSA-AES128-GCM-SHA256"},
				}},
			},
		},
	}
	result, err := resolveClusterProfile(context.Background(), &fakeAPIServerClient{obj: apiServer})
	if err != nil {
		t.Fatalf("resolveClusterProfile() error = %v", err)
	}
	cfg := newTestTLSConfig()
	result.TLSOpts[0](cfg)
	if len(cfg.CipherSuites) != 1 || cfg.CipherSuites[0] != tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256 {
		t.Fatalf("expected the RC4 entry to be dropped and the usable suite kept, got %v", cfg.CipherSuites)
	}

	rc4Only := apiServer.DeepCopy()
	rc4Only.Spec.TLSSecurityProfile.Custom.Ciphers = []string{"TLS_RSA_WITH_RC4_128_SHA"}
	if _, err := resolveClusterProfile(context.Background(), &fakeAPIServerClient{obj: rc4Only}); err == nil {
		t.Fatal("expected an all-RC4 cipher list to be rejected")
	}
}

func TestResolveClusterProfile_RetainsImplementedCustomCiphers(t *testing.T) {
	// Custom profiles may request any cipher implemented by the runtime.
	apiServer := &configv1.APIServer{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec: configv1.APIServerSpec{
			TLSAdherence: configv1.TLSAdherencePolicyStrictAllComponents,
			TLSSecurityProfile: &configv1.TLSSecurityProfile{
				Type: configv1.TLSProfileCustomType,
				Custom: &configv1.CustomTLSProfile{TLSProfileSpec: configv1.TLSProfileSpec{
					MinTLSVersion: configv1.VersionTLS12,
					Ciphers:       []string{"TLS_ECDHE_RSA_WITH_3DES_EDE_CBC_SHA", "ECDHE-RSA-AES128-GCM-SHA256"},
				}},
			},
		},
	}
	result, err := resolveClusterProfile(context.Background(), &fakeAPIServerClient{obj: apiServer})
	if err != nil {
		t.Fatalf("resolveClusterProfile() error = %v", err)
	}
	cfg := newTestTLSConfig()
	result.TLSOpts[0](cfg)
	if len(cfg.CipherSuites) != 2 || cfg.CipherSuites[0] != tls.TLS_ECDHE_RSA_WITH_3DES_EDE_CBC_SHA || cfg.CipherSuites[1] != tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256 {
		t.Fatalf("expected both runtime-supported custom suites to be retained, got %v", cfg.CipherSuites)
	}
}

func TestResolveClusterProfile_CustomWithNoSupportedCiphersErrors(t *testing.T) {
	apiServer := &configv1.APIServer{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec: configv1.APIServerSpec{
			TLSAdherence: configv1.TLSAdherencePolicyStrictAllComponents,
			TLSSecurityProfile: &configv1.TLSSecurityProfile{
				Type: configv1.TLSProfileCustomType,
				Custom: &configv1.CustomTLSProfile{TLSProfileSpec: configv1.TLSProfileSpec{
					MinTLSVersion: configv1.VersionTLS12,
					Ciphers:       []string{"UNSUPPORTED-CIPHER"},
				}},
			},
		},
	}
	if _, err := resolveClusterProfile(context.Background(), &fakeAPIServerClient{obj: apiServer}); err == nil {
		t.Fatal("expected unsupported custom cipher error")
	}
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
	minVersion, ciphers, err := parseProfile(profile)
	if err != nil {
		t.Fatalf("parseProfile() error = %v", err)
	}
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

func newTestTLSConfig() *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS12}
}
