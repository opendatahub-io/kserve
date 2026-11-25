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
// Workloads keep their serving defaults for NoOpinion and
// LegacyAdheringComponentsOnly adherence or when the APIServer object is
// unavailable. Read errors fail the reconciliation.
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

	workloadTLS, honored, err := resolveWorkloadArgs(ctx, reader)
	if err != nil {
		return err
	}
	if !honored {
		return nil
	}
	for _, i := range targets {
		setContainerEnv(&podSpec.Containers[i], tlsProfileProviderEnv, tlsProfileProvider)
		setContainerEnv(&podSpec.Containers[i], tlsMinVersionEnv, workloadTLS.MinVersion)
		setContainerEnv(&podSpec.Containers[i], tlsCiphersEnv, workloadTLS.OpenSSLCipherSuites)
	}
	return nil
}

func hasTLSCredentials(container *corev1.Container) bool {
	hasCert, hasKey := false, false
	for _, env := range container.Env {
		switch env.Name {
		case constants.TransformerTLSCertEnvVar:
			hasCert = hasCert || env.Value != "" || env.ValueFrom != nil
		case constants.TransformerTLSKeyEnvVar:
			hasKey = hasKey || env.Value != "" || env.ValueFrom != nil
		}
	}
	args := append(append([]string(nil), container.Command...), container.Args...)
	// Python argparse applies explicit flags over environment defaults; repeated flags use the last value.
	for i, arg := range args {
		switch {
		case arg == "--ssl_certfile":
			hasCert = hasNonEmptyFlagValue(args, i)
		case strings.HasPrefix(arg, "--ssl_certfile="):
			hasCert = strings.TrimPrefix(arg, "--ssl_certfile=") != ""
		case arg == "--ssl_keyfile":
			hasKey = hasNonEmptyFlagValue(args, i)
		case strings.HasPrefix(arg, "--ssl_keyfile="):
			hasKey = strings.TrimPrefix(arg, "--ssl_keyfile=") != ""
		}
	}
	return hasCert && hasKey
}

func hasNonEmptyFlagValue(args []string, index int) bool {
	return index+1 < len(args) && args[index+1] != "" && !strings.HasPrefix(args[index+1], "--")
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
