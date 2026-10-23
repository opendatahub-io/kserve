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
	"errors"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type getErrorReader struct {
	client.Reader
	err error
}

func (r getErrorReader) Get(context.Context, client.ObjectKey, client.Object, ...client.GetOption) error {
	return r.err
}

func TestWorkloadArgsFromSettings(t *testing.T) {
	tests := []struct {
		name        string
		settings    Settings
		wantMin     string
		wantCipher  string
		wantOpenSSL string
		wantCurves  string
		wantErr     bool
	}{
		{
			name:     "intermediate",
			settings: Settings{ProfileSpec: *configv1.TLSProfiles[configv1.TLSProfileIntermediateType]},
			wantMin:  "VersionTLS12",
			wantCipher: "TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256," +
				"TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384," +
				"TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256,TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256",
			wantOpenSSL: "ECDHE-ECDSA-AES128-GCM-SHA256,ECDHE-RSA-AES128-GCM-SHA256," +
				"ECDHE-ECDSA-AES256-GCM-SHA384,ECDHE-RSA-AES256-GCM-SHA384," +
				"ECDHE-ECDSA-CHACHA20-POLY1305,ECDHE-RSA-CHACHA20-POLY1305",
			wantCurves: "4588,29,23,24",
		},
		{
			name:       "modern omits ciphers",
			settings:   Settings{ProfileSpec: *configv1.TLSProfiles[configv1.TLSProfileModernType]},
			wantMin:    "VersionTLS13",
			wantCurves: "4588,29,23,24",
		},
		{
			name: "preserves OpenSSL cipher names for Python",
			settings: Settings{ProfileSpec: configv1.TLSProfileSpec{
				MinTLSVersion: configv1.VersionTLS12,
				Ciphers:       []string{"ECDHE-RSA-AES128-GCM-SHA256"},
			}},
			wantMin:     "VersionTLS12",
			wantCipher:  "TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256",
			wantOpenSSL: "ECDHE-RSA-AES128-GCM-SHA256",
		},
		{
			name: "canonicalizes CHACHA20 aliases for Python",
			settings: Settings{ProfileSpec: configv1.TLSProfileSpec{
				MinTLSVersion: configv1.VersionTLS12,
				Ciphers: []string{
					"ECDHE-ECDSA-CHACHA20-POLY1305-SHA256",
					"ECDHE-RSA-CHACHA20-POLY1305-SHA256",
				},
			}},
			wantMin: "VersionTLS12",
			wantCipher: "TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256," +
				"TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256",
			wantOpenSSL: "ECDHE-ECDSA-CHACHA20-POLY1305,ECDHE-RSA-CHACHA20-POLY1305",
		},
		{
			name: "unsupported custom ciphers fail",
			settings: Settings{ProfileSpec: configv1.TLSProfileSpec{
				MinTLSVersion: configv1.VersionTLS12,
				Ciphers:       []string{"NOT-A-GO-CIPHER"},
			}},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := WorkloadArgsFromSettings(tt.settings)
			if (err != nil) != tt.wantErr {
				t.Fatalf("WorkloadArgsFromSettings() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil {
				return
			}
			if got.MinVersion != tt.wantMin || got.CipherSuites != tt.wantCipher || got.OpenSSLCipherSuites != tt.wantOpenSSL || got.CurveIDs != tt.wantCurves {
				t.Fatalf("got %+v, want min=%q ciphers=%q OpenSSL=%q curves=%q", got, tt.wantMin, tt.wantCipher, tt.wantOpenSSL, tt.wantCurves)
			}
		})
	}
}

func TestCurveIDsFromNames(t *testing.T) {
	groups := []configv1.TLSGroup{
		configv1.TLSGroupSecP256r1,
		"unsupported",
		configv1.TLSGroupX25519,
		configv1.TLSGroupX25519MLKEM768,
		configv1.TLSGroupSecP256r1MLKEM768,
		configv1.TLSGroupSecP384r1MLKEM1024,
	}
	if got := CurveIDsFromNames(groups, false); got != "23,29,4588,4587,4589" {
		t.Fatalf("CurveIDsFromNames(non-FIPS) = %q, want %q", got, "23,29,4588,4587,4589")
	}
	if got := CurveIDsFromNames(groups, true); got != "23,4587,4589" {
		t.Fatalf("CurveIDsFromNames(FIPS) = %q, want %q", got, "23,4587,4589")
	}
}

func TestWorkloadArgsFromSettingsRejectsGroupsWithoutSupportedCurve(t *testing.T) {
	settings := Settings{ProfileSpec: configv1.TLSProfileSpec{
		MinTLSVersion: configv1.VersionTLS12,
		Groups:        []configv1.TLSGroup{"not-a-supported-group"},
	}}
	if _, err := WorkloadArgsFromSettings(settings); err == nil {
		t.Fatal("expected an error when no profile group can be passed to the workload")
	}
}

func TestWorkloadArgsFromSettingsRejectsGroupsWithoutFIPSApprovedCurve(t *testing.T) {
	settings := Settings{ProfileSpec: configv1.TLSProfileSpec{
		MinTLSVersion: configv1.VersionTLS13,
		Groups:        []configv1.TLSGroup{configv1.TLSGroupX25519, configv1.TLSGroupX25519MLKEM768},
	}}
	if _, err := workloadArgsFromSettings(settings, true); err == nil {
		t.Fatal("expected an error when no configured group is approved in FIPS mode")
	}
}

func TestResolveWorkloadArgsAdherenceFallback(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := configv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	client := fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(&configv1.APIServer{
		ObjectMeta: metav1.ObjectMeta{Name: apiServerName},
		Spec: configv1.APIServerSpec{
			TLSAdherence:       configv1.TLSAdherencePolicyNoOpinion,
			TLSSecurityProfile: &configv1.TLSSecurityProfile{Type: configv1.TLSProfileModernType},
		},
	}).Build()

	got, err := ResolveWorkloadArgs(t.Context(), client)
	if err != nil {
		t.Fatal(err)
	}
	if got.MinVersion != "VersionTLS12" || got.CipherSuites == "" {
		t.Fatalf("NoOpinion should use Intermediate defaults, got %+v", got)
	}
}

func TestResolveWorkloadArgsFallbackOnlyWhenAPIServerIsUnavailable(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := configv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	baseReader := fakeclient.NewClientBuilder().WithScheme(scheme).Build()
	noMatch := &apiMeta.NoKindMatchError{GroupKind: schema.GroupKind{Group: "config.openshift.io", Kind: "APIServer"}}
	for name, err := range map[string]error{
		"no match":     noMatch,
		"forbidden":    apierrors.NewForbidden(schema.GroupResource{Group: "config.openshift.io", Resource: "apiservers"}, "cluster", nil),
		"unauthorized": apierrors.NewUnauthorized("credentials rejected"),
		"unexpected":   errors.New("temporary client failure"),
	} {
		t.Run(name, func(t *testing.T) {
			got, resolveErr := ResolveWorkloadArgs(t.Context(), getErrorReader{Reader: baseReader, err: err})
			if name == "no match" {
				if resolveErr != nil {
					t.Fatalf("NoMatch should use Intermediate defaults: %v", resolveErr)
				}
				if got.MinVersion != "VersionTLS12" || got.CipherSuites == "" {
					t.Fatalf("NoMatch fallback = %+v, want Intermediate defaults", got)
				}
				return
			}
			if resolveErr == nil {
				t.Fatalf("expected lookup failure to be returned, got args %+v", got)
			}
		})
	}
}

func TestResolveWorkloadArgsInvalidStrictProfile(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := configv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	client := fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(&configv1.APIServer{
		ObjectMeta: metav1.ObjectMeta{Name: apiServerName},
		Spec: configv1.APIServerSpec{
			TLSAdherence: configv1.TLSAdherencePolicyStrictAllComponents,
			TLSSecurityProfile: &configv1.TLSSecurityProfile{Type: configv1.TLSProfileCustomType, Custom: &configv1.CustomTLSProfile{
				TLSProfileSpec: configv1.TLSProfileSpec{MinTLSVersion: "InvalidTLSVersion"},
			}},
		},
	}).Build()

	if _, err := ResolveWorkloadArgs(t.Context(), client); err == nil {
		t.Fatal("invalid strict profile should fail workload resolution")
	}
}

func TestResolveWorkloadArgsStrictGroups(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := configv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	client := fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(&configv1.APIServer{
		ObjectMeta: metav1.ObjectMeta{Name: apiServerName},
		Spec: configv1.APIServerSpec{
			TLSAdherence: configv1.TLSAdherencePolicyStrictAllComponents,
			TLSSecurityProfile: &configv1.TLSSecurityProfile{Type: configv1.TLSProfileCustomType, Custom: &configv1.CustomTLSProfile{
				TLSProfileSpec: configv1.TLSProfileSpec{
					MinTLSVersion: configv1.VersionTLS13,
					Groups:        []configv1.TLSGroup{configv1.TLSGroupSecP256r1, configv1.TLSGroupX25519},
				},
			}},
		},
	}).Build()

	got, err := ResolveWorkloadArgs(t.Context(), client)
	if err != nil {
		t.Fatal(err)
	}
	if got.MinVersion != "VersionTLS13" || got.CurveIDs != "23" {
		t.Fatalf("ResolveWorkloadArgs() = %+v, want strict-FIPS TLS 1.3 with supported group 23", got)
	}
}

func TestResolveWorkloadArgsInvalidStrictProfileType(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := configv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	client := fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(&configv1.APIServer{
		ObjectMeta: metav1.ObjectMeta{Name: apiServerName},
		Spec: configv1.APIServerSpec{
			TLSAdherence:       configv1.TLSAdherencePolicyStrictAllComponents,
			TLSSecurityProfile: &configv1.TLSSecurityProfile{Type: configv1.TLSProfileType("FutureProfile")},
		},
	}).Build()

	if _, err := ResolveWorkloadArgs(t.Context(), client); err == nil {
		t.Fatal("unknown strict TLS profile type should fail resolution")
	}
}
