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

package tls

import (
	"testing"

	configv1 "github.com/openshift/api/config/v1"
)

func TestShouldHonorClusterTLSProfile(t *testing.T) {
	tests := []struct {
		adherence string
		want      bool
	}{
		{adherenceStrictAllComponents, true},
		{"", true},
		{adherenceNoOpinion, false},
		{adherenceLegacyAdheringComponentsOnly, false},
		{"FuturePolicy", true},
	}

	for _, tt := range tests {
		t.Run(tt.adherence, func(t *testing.T) {
			if got := shouldHonorClusterTLSProfile(tt.adherence); got != tt.want {
				t.Fatalf("shouldHonorClusterTLSProfile(%q) = %v, want %v", tt.adherence, got, tt.want)
			}
		})
	}
}

func TestSettingsEqual(t *testing.T) {
	intermediate := *configv1.TLSProfiles[configv1.TLSProfileIntermediateType]
	modern := *configv1.TLSProfiles[configv1.TLSProfileModernType]

	a := Settings{ProfileSpec: intermediate, Adherence: adherenceNoOpinion}
	b := Settings{ProfileSpec: intermediate, Adherence: adherenceNoOpinion}
	if !settingsEqual(a, b) {
		t.Fatal("expected equal settings")
	}

	c := Settings{ProfileSpec: modern, Adherence: adherenceStrictAllComponents}
	if settingsEqual(a, c) {
		t.Fatal("expected different settings")
	}
}

func TestResolveProfileSpec(t *testing.T) {
	modern := configv1.TLSProfiles[configv1.TLSProfileModernType]
	got := resolveProfileSpec(&configv1.TLSSecurityProfile{Type: configv1.TLSProfileModernType})
	if got.MinTLSVersion != modern.MinTLSVersion {
		t.Fatalf("resolveProfileSpec modern min version = %q, want %q", got.MinTLSVersion, modern.MinTLSVersion)
	}
}

func TestSettingsFromAPIServer_NoOpinionOverridesOldProfile(t *testing.T) {
	apiServer := &configv1.APIServer{
		Spec: configv1.APIServerSpec{
			TLSSecurityProfile: &configv1.TLSSecurityProfile{Type: configv1.TLSProfileOldType},
		},
	}
	settings := settingsFromAPIServer(apiServer, adherenceNoOpinion)
	intermediate := *configv1.TLSProfiles[configv1.TLSProfileIntermediateType]
	if settings.ProfileSpec.MinTLSVersion != intermediate.MinTLSVersion {
		t.Fatalf("expected Intermediate profile under NoOpinion, got %q", settings.ProfileSpec.MinTLSVersion)
	}
	if settings.Adherence != adherenceNoOpinion {
		t.Fatalf("expected adherence %q, got %q", adherenceNoOpinion, settings.Adherence)
	}
}

func TestAdherenceForResolution_FetchFailureUsesNoOpinion(t *testing.T) {
	got := adherenceForResolution("", false)
	if got != adherenceNoOpinion {
		t.Fatalf("expected %q on fetch failure, got %q", adherenceNoOpinion, got)
	}
}

func TestAdherenceForResolution_PreservesSuccessfulRead(t *testing.T) {
	got := adherenceForResolution(adherenceStrictAllComponents, true)
	if got != adherenceStrictAllComponents {
		t.Fatalf("expected %q, got %q", adherenceStrictAllComponents, got)
	}
}

func TestSettingsFromAPIServer_EmptyAdherenceHonorsOldProfile(t *testing.T) {
	apiServer := &configv1.APIServer{
		Spec: configv1.APIServerSpec{
			TLSSecurityProfile: &configv1.TLSSecurityProfile{Type: configv1.TLSProfileOldType},
		},
	}
	settings := settingsFromAPIServer(apiServer, "")
	old := *configv1.TLSProfiles[configv1.TLSProfileOldType]
	if settings.ProfileSpec.MinTLSVersion != old.MinTLSVersion {
		t.Fatalf("expected Old profile when adherence is empty, got %q", settings.ProfileSpec.MinTLSVersion)
	}
}
