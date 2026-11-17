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
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kservetls "github.com/kserve/kserve/pkg/tls"
)

// InjectGoTLSArgs projects the effective cluster TLS policy into a Go TLS
// workload as command line arguments. It is the Go twin of
// InjectPythonTLSProfile: the workload does not resolve or watch the cluster
// profile itself; its owner reconciles and passes the resolved values.
// Cipher suites are omitted for TLS 1.3 profiles because crypto/tls manages
// TLS 1.3 cipher selection internally. Curve preferences are projected as
// decimal crypto/tls CurveID values when the profile specifies groups.
// Workloads keep their serving defaults
// when adherence is NoOpinion or LegacyAdheringComponentsOnly, or when the
// APIServer object is unavailable (including non-OpenShift clusters). Read
// errors fail the reconciliation so a missing RBAC grant is visible.
func InjectGoTLSArgs(ctx context.Context, reader client.Reader, container *corev1.Container) error {
	if container == nil {
		return nil
	}
	workloadTLS, honored, err := resolveWorkloadArgs(ctx, reader)
	if err != nil {
		return err
	}
	if !honored {
		return nil
	}
	// The workload validates the projected arguments at startup and exits on
	// rejection, which would roll out a crashlooping pod. Validate here with
	// the same rules the Go workloads apply so an incompatible profile (for
	// example the Old profile's TLS 1.0 minimum or its insecure cipher names)
	// fails the reconciliation with a clear error instead.
	if err := kservetls.Validate(workloadTLS.MinVersion, workloadTLS.CipherSuites); err != nil {
		return fmt.Errorf("cluster TLS profile cannot be served by Go workloads: %w", err)
	}
	container.Args = append(container.Args, "--tls-min-version", workloadTLS.MinVersion)
	if workloadTLS.CipherSuites != "" {
		container.Args = append(container.Args, "--tls-cipher-suites", workloadTLS.CipherSuites)
	}
	if workloadTLS.CurveIDs != "" {
		container.Args = append(container.Args, "--tls-curve-preferences", workloadTLS.CurveIDs)
	}
	return nil
}
