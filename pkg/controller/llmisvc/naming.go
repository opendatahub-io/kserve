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
	"crypto/sha256"
	"encoding/hex"
)

const (
	// maxKubernetesNameLength is the maximum length for Kubernetes resource names and labels
	maxKubernetesNameLength = 63
	// hashLength is the number of characters from the hash to append when truncating
	hashLength = 8
	// kubernetesGeneratedSuffixLength reserves space for suffixes that Kubernetes controllers
	// add automatically (e.g., StatefulSet pod hash, ReplicaSet hash). This ensures that
	// child resources created by Kubernetes don't exceed the 63-character limit.
	// Examples: "-f4b4c86d6" (10 chars), "-0" for StatefulSet pods (2 chars)
	kubernetesGeneratedSuffixLength = 12
)

// SafeChildName creates a child resource name by appending a suffix to the parent name.
// Unlike kmeta.ChildName, this function reserves additional space for Kubernetes-generated
// suffixes (like StatefulSet pod hashes) to ensure that child resources created by
// Kubernetes controllers don't exceed the 63-character limit.
//
// The function enforces: len(result) + kubernetesGeneratedSuffixLength <= 63
//
// Example:
//   - Short name: "my-service" + "-kserve-mn" = "my-service-kserve-mn"
//   - Long name: "llmisvc-model-deepseek-v2-lite-3965b7a6-v1alpha1" + "-kserve-mn" =
//     "llmisvc-model-deepseek-v2-lite-396a1b2c3d4-kserve-mn" (truncated + hash)
//     Leaves room for Kubernetes to add "-f4b4c86d6-0" without exceeding 63 chars
func SafeChildName(parentName, suffix string) string {
	proposed := parentName + suffix

	// Reserve space for Kubernetes-generated suffixes (e.g., StatefulSet pod hash)
	maxAllowedLength := maxKubernetesNameLength - kubernetesGeneratedSuffixLength

	// If the name fits within the limit (leaving room for K8s suffixes), use it as-is
	if len(proposed) <= maxAllowedLength {
		return proposed
	}

	// Calculate how much space we have for the parent name
	// We need to reserve space for: hash (8) + suffix + Kubernetes suffixes (12)
	maxParentLength := maxAllowedLength - hashLength - len(suffix)

	// Truncate the parent name
	truncatedParent := parentName[:maxParentLength]

	// Generate a hash of the full parent name to ensure uniqueness
	hash := sha256.Sum256([]byte(parentName))
	hashStr := hex.EncodeToString(hash[:])[:hashLength]

	// Combine: truncated-parent + hash + suffix
	return truncatedParent + hashStr + suffix
}
