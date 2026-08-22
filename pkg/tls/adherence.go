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
	"context"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

const (
	adherenceFetchTimeout = 10 * time.Second
	adherenceRequeueDelay = 5 * time.Second
)

const (
	adherenceStrictAllComponents          = "StrictAllComponents"
	adherenceNoOpinion                    = "NoOpinion"
	adherenceLegacyAdheringComponentsOnly = "LegacyAdheringComponentsOnly"
)

var apiServerGVR = schema.GroupVersionResource{
	Group: "config.openshift.io", Version: "v1", Resource: "apiservers",
}

// Settings captures the cluster TLS profile and adherence policy used to resolve TLS options.
type Settings struct {
	ProfileSpec configv1.TLSProfileSpec
	Adherence   string
}

func fetchTLSAdherence(ctx context.Context, cfg *rest.Config) (string, bool) {
	if cfg == nil {
		return "", false
	}
	fetchCtx, cancel := context.WithTimeout(ctx, adherenceFetchTimeout)
	defer cancel()

	dc, err := dynamic.NewForConfig(cfg)
	if err != nil {
		log.Info("Unable to create dynamic client for TLS adherence", "error", err)
		return "", false
	}
	obj, err := dc.Resource(apiServerGVR).Get(fetchCtx, apiServerName, metav1.GetOptions{})
	if err != nil {
		return "", false
	}
	adherence, found, err := unstructured.NestedString(obj.Object, "spec", "tlsAdherence")
	if err != nil {
		return "", false
	}
	if !found {
		return "", true
	}
	return adherence, true
}

func adherenceForResolution(adherence string, ok bool) string {
	if ok {
		return adherence
	}
	return adherenceNoOpinion
}

func shouldHonorClusterTLSProfile(adherence string) bool {
	switch adherence {
	case adherenceNoOpinion, adherenceLegacyAdheringComponentsOnly:
		return false
	default:
		// StrictAllComponents, empty, and unknown future values honor the cluster profile.
		return true
	}
}

func settingsFromAPIServer(apiServer *configv1.APIServer, adherence string) Settings {
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
