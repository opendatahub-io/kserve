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

package testing

import (
	istioclientv1 "istio.io/client-go/pkg/apis/networking/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// registerLegacyDistroSchemes registers OCP-specific schemes for the legacy
// SetupEnvTest path (Istio v1 networking APIs).
func registerLegacyDistroSchemes(s *runtime.Scheme) {
	if err := istioclientv1.SchemeBuilder.AddToScheme(s); err != nil {
		log.Error(err, "Failed to add istio v1 scheme")
	}
}
