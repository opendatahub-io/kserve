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
	configv1 "github.com/openshift/api/config/v1"
)

// Settings captures the cluster TLS profile and adherence policy used to resolve TLS options.
type Settings struct {
	ProfileSpec configv1.TLSProfileSpec
	Adherence   configv1.TLSAdherencePolicy
}

func shouldHonorClusterTLSProfile(adherence configv1.TLSAdherencePolicy) bool {
	switch adherence {
	case configv1.TLSAdherencePolicyNoOpinion, configv1.TLSAdherencePolicyLegacyAdheringComponentsOnly:
		return false
	default:
		return true
	}
}

func settingsFromAPIServer(apiServer *configv1.APIServer) Settings {
	adherence := apiServer.Spec.TLSAdherence
	profileSpec := *resolveProfileSpec(apiServer.Spec.TLSSecurityProfile)
	if !shouldHonorClusterTLSProfile(adherence) {
		profileSpec = *configv1.TLSProfiles[configv1.TLSProfileIntermediateType]
	}
	return Settings{
		ProfileSpec: profileSpec,
		Adherence:   adherence,
	}
}

func settingsEqual(a, b Settings) bool {
	return a.Adherence == b.Adherence && profileSpecEqual(a.ProfileSpec, b.ProfileSpec)
}

func profileSpecEqual(a, b configv1.TLSProfileSpec) bool {
	return a.MinTLSVersion == b.MinTLSVersion &&
		stringSlicesEqual(a.Ciphers, b.Ciphers)
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
