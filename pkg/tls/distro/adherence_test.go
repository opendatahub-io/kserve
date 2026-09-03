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
)

func TestShouldHonorClusterTLSProfile(t *testing.T) {
	tests := []struct {
		name      string
		adherence configv1.TLSAdherencePolicy
		want      bool
	}{
		{"StrictAllComponents", configv1.TLSAdherencePolicyStrictAllComponents, true},
		{"NoOpinion", configv1.TLSAdherencePolicyNoOpinion, false},
		{"LegacyAdheringComponentsOnly", configv1.TLSAdherencePolicyLegacyAdheringComponentsOnly, false},
		{"ZeroValue", configv1.TLSAdherencePolicy(""), false},
		{"FuturePolicy", configv1.TLSAdherencePolicy("FuturePolicy"), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldHonorClusterTLSProfile(tt.adherence); got != tt.want {
				t.Fatalf("shouldHonorClusterTLSProfile(%q) = %v, want %v", tt.adherence, got, tt.want)
			}
		})
	}
}

func TestSettingsEqual(t *testing.T) {
	intermediate := *configv1.TLSProfiles[configv1.TLSProfileIntermediateType]
	modern := *configv1.TLSProfiles[configv1.TLSProfileModernType]

	a := Settings{ProfileSpec: intermediate, Adherence: configv1.TLSAdherencePolicyNoOpinion}
	b := Settings{ProfileSpec: intermediate, Adherence: configv1.TLSAdherencePolicyNoOpinion}
	if !settingsEqual(a, b) {
		t.Fatal("expected equal settings")
	}

	c := Settings{ProfileSpec: modern, Adherence: configv1.TLSAdherencePolicyStrictAllComponents}
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
			TLSAdherence:       configv1.TLSAdherencePolicyNoOpinion,
		},
	}
	settings := settingsFromAPIServer(apiServer)
	intermediate := *configv1.TLSProfiles[configv1.TLSProfileIntermediateType]
	if settings.ProfileSpec.MinTLSVersion != intermediate.MinTLSVersion {
		t.Fatalf("expected Intermediate profile under NoOpinion, got %q", settings.ProfileSpec.MinTLSVersion)
	}
}

func TestSettingsFromAPIServer_StrictHonorsOldProfile(t *testing.T) {
	apiServer := &configv1.APIServer{
		Spec: configv1.APIServerSpec{
			TLSSecurityProfile: &configv1.TLSSecurityProfile{Type: configv1.TLSProfileOldType},
			TLSAdherence:       configv1.TLSAdherencePolicyStrictAllComponents,
		},
	}
	settings := settingsFromAPIServer(apiServer)
	old := *configv1.TLSProfiles[configv1.TLSProfileOldType]
	if settings.ProfileSpec.MinTLSVersion != old.MinTLSVersion {
		t.Fatalf("expected Old profile when adherence is StrictAllComponents, got %q", settings.ProfileSpec.MinTLSVersion)
	}
}
