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

package distro

import (
	"context"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kserve/kserve/pkg/constants"
)

const (
	tlsProfileProviderEnv = "KSERVE_TLS_PROFILE_PROVIDER"
	tlsProfileProvider    = "openshift"
	tlsMinVersionEnv      = "KSERVE_TLS_MIN_VERSION"
	tlsCiphersEnv         = "KSERVE_TLS_CIPHERS"
)

// InjectPythonTLSProfile configures containers that already serve HTTPS
// through the KServe SDK's certificate arguments or environment. The upstream
// Python server owns TLS profile application through its provider interface.
func InjectPythonTLSProfile(ctx context.Context, reader client.Reader, podSpec *corev1.PodSpec) error {
	targets := make([]int, 0, len(podSpec.Containers))
	for i := range podSpec.Containers {
		if hasTLSCredentials(&podSpec.Containers[i]) {
			targets = append(targets, i)
		}
	}
	if len(targets) == 0 {
		return nil
	}

	workloadTLS, err := ResolveWorkloadArgs(ctx, reader)
	if err != nil {
		return err
	}
	for _, i := range targets {
		setContainerEnv(&podSpec.Containers[i], tlsProfileProviderEnv, tlsProfileProvider)
		setContainerEnv(&podSpec.Containers[i], tlsMinVersionEnv, workloadTLS.MinVersion)
		setContainerEnv(&podSpec.Containers[i], tlsCiphersEnv, workloadTLS.CipherSuites)
	}
	return nil
}

func hasTLSCredentials(container *corev1.Container) bool {
	hasCert, hasKey := false, false
	for _, env := range container.Env {
		switch env.Name {
		case constants.TransformerTLSCertEnvVar:
			hasCert = true
		case constants.TransformerTLSKeyEnvVar:
			hasKey = true
		}
	}
	for _, arg := range append(append([]string(nil), container.Command...), container.Args...) {
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
