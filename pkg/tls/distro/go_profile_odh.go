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

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// InjectGoTLSArgs projects the effective cluster TLS policy into a Go TLS
// workload as command line arguments. It is the Go twin of
// InjectPythonTLSProfile: the workload does not resolve or watch the cluster
// profile itself; its owner reconciles and passes the resolved values.
// Cipher suites are omitted for TLS 1.3 profiles because crypto/tls manages
// TLS 1.3 cipher selection internally. Workloads on clusters where the
// APIServer object is unavailable (including non-OpenShift clusters) keep
// their serving defaults.
func InjectGoTLSArgs(ctx context.Context, reader client.Reader, container *corev1.Container) error {
	if container == nil {
		return nil
	}
	workloadTLS, apiServerReadable, err := resolveWorkloadArgs(ctx, reader)
	if isAPIServerAccessDenied(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !apiServerReadable {
		return nil
	}
	container.Args = append(container.Args, "--tls-min-version", workloadTLS.MinVersion)
	if workloadTLS.CipherSuites != "" {
		container.Args = append(container.Args, "--tls-cipher-suites", workloadTLS.CipherSuites)
	}
	return nil
}
