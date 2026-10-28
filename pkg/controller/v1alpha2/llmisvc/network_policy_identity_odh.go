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

package llmisvc

import (
	"crypto/sha256"
	"encoding/hex"

	corev1 "k8s.io/api/core/v1"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
)

const (
	// These labels are written after user metadata has been propagated. They
	// are deliberately separate from routing labels (app/name and pool
	// selectors), so shared InferencePools can continue to select workloads
	// from more than one LLMInferenceService.
	networkPolicyServiceIDLabel = "serving.kserve.io/llmisvc-network-policy-id"
	networkPolicyRoleLabel      = "serving.kserve.io/llmisvc-network-policy-role"

	networkPolicyRoleWorkload  = "workload"
	networkPolicyRoleScheduler = "scheduler"
	networkPolicyRoleTokenizer = "tokenizer"
)

func networkPolicyServiceID(llmSvc *v1alpha2.LLMInferenceService) string {
	digest := sha256.Sum256([]byte(llmSvc.GetNamespace() + "/" + llmSvc.GetName()))
	return hex.EncodeToString(digest[:])[:16]
}

// applyNetworkPolicyPodIdentity adds the controller-owned identity used by
// NetworkPolicy selectors. It must run after user labels are propagated so a
// user supplied routing label cannot retarget the policy.
func applyNetworkPolicyPodIdentity(template *corev1.PodTemplateSpec, llmSvc *v1alpha2.LLMInferenceService, role string) {
	if template == nil || llmSvc == nil {
		return
	}
	if template.Labels == nil {
		template.Labels = map[string]string{}
	}
	applyNetworkPolicyLabels(template.Labels, llmSvc, role)
}

func applyNetworkPolicyLabels(labels map[string]string, llmSvc *v1alpha2.LLMInferenceService, role string) {
	if labels == nil || llmSvc == nil {
		return
	}
	labels[networkPolicyServiceIDLabel] = networkPolicyServiceID(llmSvc)
	labels[networkPolicyRoleLabel] = role
}
