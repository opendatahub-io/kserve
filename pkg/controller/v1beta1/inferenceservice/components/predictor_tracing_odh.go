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
	"github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
	"github.com/kserve/kserve/pkg/constants"
)

// resolveServerTypeForDistro refines the server type used for predictor tracing
// injection for ODH/RHOAI ServingRuntimes. Upstream detection (the
// serving.kserve.io/server-type annotation and the runtime-name fallback) does
// not recognize ODH runtimes, which instead advertise their type through the
// opendatahub.io/kserve-runtime pod annotation. When upstream detection already
// resolved a server type it is authoritative and returned unchanged.
func resolveServerTypeForDistro(serverType string, sRuntime v1alpha1.ServingRuntimeSpec) string {
	if serverType != "" {
		return serverType
	}
	switch sRuntime.Annotations[constants.ODHKserveRuntimeAnnotation] {
	case constants.ODHKserveRuntimeVLLM:
		return constants.ServerTypeVLLMServer
	case constants.ServerTypeMLServer:
		// The ODH opendatahub.io/kserve-runtime value "mlserver" matches the
		// upstream ServerTypeMLServer server type verbatim.
		return constants.ServerTypeMLServer
	}
	return serverType
}
