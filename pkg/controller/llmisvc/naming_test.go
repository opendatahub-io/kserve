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
	"testing"
)

func TestSafeChildName(t *testing.T) {
	tests := []struct {
		name               string
		parentName         string
		suffix             string
		wantMaxLength      int
		wantContainsSuffix bool
	}{
		{
			name:               "short name unchanged",
			parentName:         "my-service",
			suffix:             "-kserve-mn",
			wantMaxLength:      maxKubernetesNameLength - kubernetesGeneratedSuffixLength,
			wantContainsSuffix: true,
		},
		{
			name:               "long name truncated with hash",
			parentName:         "llmisvc-model-deepseek-v2-lite-3965b7a6-v1alpha1",
			suffix:             "-kserve-mn",
			wantMaxLength:      maxKubernetesNameLength - kubernetesGeneratedSuffixLength,
			wantContainsSuffix: true,
		},
		{
			name:               "very long name with prefill suffix",
			parentName:         "llmisvc-model-deepseek-v2-lite-3965b7a6-v1alpha1",
			suffix:             "-kserve-mn-prefill",
			wantMaxLength:      maxKubernetesNameLength - kubernetesGeneratedSuffixLength,
			wantContainsSuffix: true,
		},
		{
			name:               "exact limit",
			parentName:         "a1234567890123456789012345678901234567", // 39 chars
			suffix:             "-kserve-mn",                             // 10 chars
			wantMaxLength:      maxKubernetesNameLength - kubernetesGeneratedSuffixLength,
			wantContainsSuffix: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := SafeChildName(tt.parentName, tt.suffix)

			// Check length constraint (must leave room for Kubernetes-generated suffixes)
			if len(result) > tt.wantMaxLength {
				t.Errorf("SafeChildName() result length = %d, want <= %d (to leave room for K8s suffixes)", len(result), tt.wantMaxLength)
			}

			// Check suffix is preserved
			if tt.wantContainsSuffix {
				if len(result) < len(tt.suffix) || result[len(result)-len(tt.suffix):] != tt.suffix {
					t.Errorf("SafeChildName() result %q does not end with suffix %q", result, tt.suffix)
				}
			}

			// Verify result + K8s suffix would fit
			simulatedPodName := result + "-f4b4c86d6-0" // Simulate StatefulSet pod name
			if len(simulatedPodName) > maxKubernetesNameLength {
				t.Errorf("SafeChildName() result %q + K8s suffix = %q (length %d) exceeds max %d",
					result, simulatedPodName, len(simulatedPodName), maxKubernetesNameLength)
			}

			t.Logf("SafeChildName(%q, %q) = %q (length %d, with K8s suffix: %d)",
				tt.parentName, tt.suffix, result, len(result), len(simulatedPodName))
		})
	}
}

func TestSafeChildName_Uniqueness(t *testing.T) {
	// Test that different long parent names produce different results
	parent1 := "llmisvc-model-deepseek-v2-lite-3965b7a6-v1alpha1"
	parent2 := "llmisvc-model-deepseek-v2-lite-3965b7a6-v1alpha2"
	suffix := "-kserve-mn"

	result1 := SafeChildName(parent1, suffix)
	result2 := SafeChildName(parent2, suffix)

	if result1 == result2 {
		t.Errorf("SafeChildName() produced same result for different parent names: %q", result1)
	}

	t.Logf("Parent1: %q -> %q", parent1, result1)
	t.Logf("Parent2: %q -> %q", parent2, result2)
}
