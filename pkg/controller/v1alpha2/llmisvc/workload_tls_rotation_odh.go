//go:build distro

/*
Copyright 2025 The KServe Authors.

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

package llmisvc

import (
	"regexp"

	"github.com/coreos/go-semver/semver"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
	"github.com/kserve/kserve/pkg/utils"
)

// routingSidecarVersionAnnotation is the annotation key used to record the routing sidecar
// version in llmSvc.Spec.Annotations. A dedicated key avoids ambiguity with
// app.kubernetes.io/version, which may refer to the engine (vLLM) version.
const routingSidecarVersionAnnotation = "llm-d.ai/routing-sidecar-version"

// SidecarCertRotationMinVersionStr is the minimum routing sidecar version that supports
// automatic TLS certificate rotation. Services with an older sidecar must use InsecureSkipVerify=true.
const SidecarCertRotationMinVersionStr = "0.7.0"

var sidecarCertRotationMinVersion = semver.New(SidecarCertRotationMinVersionStr)

// enableSslRefreshRegexp matches all boolean-true pflag forms of --enable-ssl-refresh in either
// a standalone Command entry or embedded in a bash script.
//
// The built-in config templates emit the flag as a bare word inside a multi-line bash script
// passed as the third element of ["/bin/bash", "-c", "<script>"]. In that form the flag
// appears at the start of a line with optional leading whitespace and a trailing " \" shell
// line-continuation, e.g. "              --enable-ssl-refresh \".
//
// (?m) makes ^ match at line starts so the pattern finds the flag within the script string.
// The trailing boundary (\s+|\\|$) matches the space(s) before "\" (bash form), a bare "\"
// if whitespace was stripped, or end-of-entry (standalone arg form).
// The value set accepts exactly the boolean-true strings that pflag/strconv.ParseBool recognises:
// 1, t, T, true, True, TRUE.
var enableSslRefreshRegexp = regexp.MustCompile(`(?m)^\s*--enable-ssl-refresh(=(1|t|T|true|True|TRUE))?(\s+|\\|$)`)

// llmSvcHasSidecar does a naive check to determine if the workloads of an LLMIsvc
// may include a routing sidecar
func llmSvcHasSidecar(llmSvc *v1alpha2.LLMInferenceService) bool {
	if llmSvc.Spec.Prefill != nil {
		return true
	}

	mainPodSpec := llmSvc.Spec.Template
	secondaryPodSpec := llmSvc.Spec.Worker

	if mainPodSpec != nil {
		return hasRoutingSidecar(*mainPodSpec)
	}

	if secondaryPodSpec != nil {
		return hasRoutingSidecar(*secondaryPodSpec)
	}

	return false
}

// llmSvcHasTlsRotationEnabled returns true when the decode pod supports TLS certificate rotation,
// either via --enable-ssl-refresh on the main container or via a routing sidecar that meets
// the version and configuration gates for cert rotation.
//
// Only llmSvc.Spec.Template (the decode pod) is inspected; the worker spec is not examined
// because requests reach decode pods first. When a routing sidecar is present the check is
// delegated to sidecarTlsRotationEnabled. Returns true (InsecureSkipVerify=false) when the
// main container is absent or the flag cannot be determined, for the stricter FIPS-safe posture.
func llmSvcHasTlsRotationEnabled(llmSvc *v1alpha2.LLMInferenceService) bool {
	if llmSvcHasSidecar(llmSvc) {
		return sidecarTlsRotationEnabled(llmSvc)
	}

	if llmSvc.Spec.Template == nil {
		// No decode pod template: assume rotation enabled for the stricter FIPS-safe posture.
		return true
	}

	container := utils.GetContainerWithName(llmSvc.Spec.Template, "main")
	if container == nil {
		// No main container found: assume rotation enabled for the stricter FIPS-safe posture.
		return true
	}

	for _, cmdEntry := range container.Command {
		if enableSslRefreshRegexp.MatchString(cmdEntry) {
			return true
		}
	}

	return false
}

// sidecarTlsRotationEnabled returns true when the routing sidecar is at version
// >= sidecarCertRotationMinVersion AND has --secure-proxy=true in its init container args.
// Both conditions must hold; either failing alone returns false (InsecureSkipVerify=true).
//
// The version is read from llmSvc.Spec.Annotations[routingSidecarVersionAnnotation]; these
// annotations are propagated to the workload pod template by the workload reconciler.
// --secure-proxy is always present in the sidecar args (set to true or false by the config
// overlay), so an exact string match for --secure-proxy=true is used rather than a multi-form
// pflag regex.
func sidecarTlsRotationEnabled(llmSvc *v1alpha2.LLMInferenceService) bool {
	versionStr, ok := llmSvc.Spec.Annotations[routingSidecarVersionAnnotation]
	if !ok || versionStr == "" {
		return false
	}

	v, err := semver.NewVersion(versionStr)
	if err != nil {
		return false
	}

	if v.Compare(*sidecarCertRotationMinVersion) < 0 {
		return false
	}

	sidecar := routingSidecar(llmSvc.Spec.Template)
	if sidecar == nil {
		return false
	}

	for _, arg := range sidecar.Args {
		if arg == "--secure-proxy=true" {
			return true
		}
	}

	return false
}
