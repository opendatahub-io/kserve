//go:build !distro

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
)

// resolveServerTypeForDistro returns the server type used for predictor tracing
// injection. In upstream builds the detection performed by the caller is
// authoritative, so the value is returned unchanged.
func resolveServerTypeForDistro(serverType string, _ v1alpha1.ServingRuntimeSpec) string {
	return serverType
}
