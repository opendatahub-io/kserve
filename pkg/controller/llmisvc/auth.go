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
	"k8s.io/utils/env"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
)

// authDisabled indicates whether authentication is globally disabled for LLMInferenceService.
// When set to "true", all LLMInferenceService resources will skip auth checks.
var authDisabled, _ = env.GetBool("LLMISVC_AUTH_DISABLED", false)

// isAuthEnabledForService checks if authentication is enabled for the given LLMInferenceService.
// It first checks the global LLMISVC_AUTH_DISABLED environment variable, then falls back to
// the per-resource annotation.
func isAuthEnabledForService(llmSvc *v1alpha1.LLMInferenceService) bool {
	if authDisabled {
		return false
	}
	return llmSvc.IsAuthEnabled()
}
