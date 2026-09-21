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

package inferenceservice

import (
	"strings"

	configv1 "github.com/openshift/api/config/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/kserve/kserve/pkg/constants"
)

func addExpectedTLSSecurityProfile(podSpec *corev1.PodSpec) {
	profile := configv1.TLSProfiles[configv1.TLSProfileIntermediateType]
	for i := range podSpec.Containers {
		container := &podSpec.Containers[i]
		if !expectedContainerHasTLSCredentials(container) {
			continue
		}
		setExpectedTLSEnv(container, "KSERVE_TLS_MIN_VERSION", string(profile.MinTLSVersion))
		setExpectedTLSEnv(container, "KSERVE_TLS_CIPHERS", strings.Join(profile.Ciphers, ":"))
	}
}

func expectedContainerHasTLSCredentials(container *corev1.Container) bool {
	hasCert, hasKey := false, false
	for _, env := range container.Env {
		switch env.Name {
		case constants.TransformerTLSCertEnvVar:
			hasCert = true
		case constants.TransformerTLSKeyEnvVar:
			hasKey = true
		}
	}
	for _, arg := range container.Args {
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

func setExpectedTLSEnv(container *corev1.Container, name, value string) {
	for i := range container.Env {
		if container.Env[i].Name == name {
			container.Env[i].Value = value
			container.Env[i].ValueFrom = nil
			return
		}
	}
	container.Env = append(container.Env, corev1.EnvVar{Name: name, Value: value})
}
