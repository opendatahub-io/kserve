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

	configv1 "github.com/openshift/api/config/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kserve/kserve/pkg/constants"
)

const (
	tlsMinVersionEnv = "KSERVE_TLS_MIN_VERSION"
	tlsCiphersEnv    = "KSERVE_TLS_CIPHERS"
	apiServerName    = "cluster"
)

func injectTLSSecurityProfile(ctx context.Context, reader client.Reader, podSpec *corev1.PodSpec, additionalTLSContainers ...string) error {
	tlsContainers := make(map[string]struct{}, len(additionalTLSContainers))
	for _, name := range additionalTLSContainers {
		tlsContainers[name] = struct{}{}
	}
	for i := range podSpec.Containers {
		if containerHasTLSCredentials(&podSpec.Containers[i]) {
			tlsContainers[podSpec.Containers[i].Name] = struct{}{}
		}
	}
	if len(tlsContainers) == 0 {
		return nil
	}

	apiServer := &configv1.APIServer{}
	var profileSpec *configv1.TLSProfileSpec
	if err := reader.Get(ctx, client.ObjectKey{Name: apiServerName}, apiServer); err != nil {
		if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) || runtime.IsNotRegisteredError(err) {
			// Use hardened defaults when the OpenShift profile API does not exist. Return
			// transient and authorization errors so reconciliation preserves the currently
			// deployed profile instead of silently replacing it with a weaker fallback.
			profileSpec = configv1.TLSProfiles[configv1.TLSProfileIntermediateType]
		} else {
			return err
		}
	} else {
		profileSpec = effectiveTLSProfileSpec(apiServer)
	}

	for i := range podSpec.Containers {
		if _, ok := tlsContainers[podSpec.Containers[i].Name]; !ok {
			continue
		}
		setContainerEnv(&podSpec.Containers[i], tlsMinVersionEnv, string(profileSpec.MinTLSVersion))
		setContainerEnv(&podSpec.Containers[i], tlsCiphersEnv, strings.Join(profileSpec.Ciphers, ":"))
	}
	return nil
}

func containerHasTLSCredentials(container *corev1.Container) bool {
	hasCert, hasKey := false, false
	for _, env := range container.Env {
		switch env.Name {
		case constants.TransformerTLSCertEnvVar:
			hasCert = true
		case constants.TransformerTLSKeyEnvVar:
			hasKey = true
		}
	}
	for i := range len(container.Args) {
		arg := container.Args[i]
		switch {
		case arg == "--ssl_certfile" || arg == "--ssl-certfile" ||
			strings.HasPrefix(arg, "--ssl_certfile=") || strings.HasPrefix(arg, "--ssl-certfile="):
			hasCert = true
		case arg == "--ssl_keyfile" || arg == "--ssl-keyfile" ||
			strings.HasPrefix(arg, "--ssl_keyfile=") || strings.HasPrefix(arg, "--ssl-keyfile="):
			hasKey = true
		}
	}
	return hasCert && hasKey
}

func effectiveTLSProfileSpec(apiServer *configv1.APIServer) *configv1.TLSProfileSpec {
	if apiServer.Spec.TLSAdherence == configv1.TLSAdherencePolicyNoOpinion ||
		apiServer.Spec.TLSAdherence == configv1.TLSAdherencePolicyLegacyAdheringComponentsOnly {
		return configv1.TLSProfiles[configv1.TLSProfileIntermediateType]
	}

	profile := apiServer.Spec.TLSSecurityProfile
	if profile == nil {
		return configv1.TLSProfiles[configv1.TLSProfileIntermediateType]
	}
	if profile.Type == configv1.TLSProfileCustomType && profile.Custom != nil {
		return &profile.Custom.TLSProfileSpec
	}
	if spec, ok := configv1.TLSProfiles[profile.Type]; ok {
		return spec
	}
	return configv1.TLSProfiles[configv1.TLSProfileIntermediateType]
}

func setContainerEnv(container *corev1.Container, name, value string) {
	for i := range container.Env {
		if container.Env[i].Name == name {
			container.Env[i].Value = value
			container.Env[i].ValueFrom = nil
			return
		}
	}
	container.Env = append(container.Env, corev1.EnvVar{Name: name, Value: value})
}
