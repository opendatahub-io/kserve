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
)

const (
	tlsMinVersionEnv = "KSERVE_TLS_MIN_VERSION"
	tlsCiphersEnv    = "KSERVE_TLS_CIPHERS"
	apiServerName    = "cluster"
)

func injectTLSSecurityProfile(ctx context.Context, reader client.Reader, podSpec *corev1.PodSpec) error {
	apiServer := &configv1.APIServer{}
	if err := reader.Get(ctx, client.ObjectKey{Name: apiServerName}, apiServer); err != nil {
		if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) || runtime.IsNotRegisteredError(err) ||
			apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
			// The OpenShift profile API is unavailable on generic Kubernetes clusters and
			// to controllers without profile RBAC. Leave those workloads unchanged.
			return nil
		}
		return err
	}
	profileSpec := effectiveTLSProfileSpec(apiServer)

	for i := range podSpec.Containers {
		setContainerEnv(&podSpec.Containers[i], tlsMinVersionEnv, string(profileSpec.MinTLSVersion))
		setContainerEnv(&podSpec.Containers[i], tlsCiphersEnv, strings.Join(profileSpec.Ciphers, ":"))
	}
	return nil
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
