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

	"github.com/stretchr/testify/assert"
)

func TestRefValidationResult_CombinedMessage(t *testing.T) {
	tests := []struct {
		name     string
		issues   []string
		expected string
	}{
		{
			name:     "no issues",
			issues:   []string{},
			expected: "",
		},
		{
			name:     "single issue",
			issues:   []string{"HTTPRoute default/test-route does not exist"},
			expected: "HTTPRoute default/test-route does not exist",
		},
		{
			name: "multiple issues",
			issues: []string{
				"HTTPRoute default/test-route does not exist",
				"Gateway default/test-gateway does not exist",
				"Gateway default/parent-gateway does not exist",
			},
			expected: "- HTTPRoute default/test-route does not exist\n- Gateway default/test-gateway does not exist\n- Gateway default/parent-gateway does not exist",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := &RefValidationResult{}
			for _, issue := range tt.issues {
				result.AddNotFoundIssue(issue)
			}

			actual := result.CombinedMessage()
			assert.Equal(t, tt.expected, actual)
		})
	}
}

func TestRefValidationResult_IsValid(t *testing.T) {
	tests := []struct {
		name     string
		issues   []string
		expected bool
	}{
		{
			name:     "no issues should be valid",
			issues:   []string{},
			expected: true,
		},
		{
			name:     "with issues should be invalid",
			issues:   []string{"some error"},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := &RefValidationResult{}
			for _, issue := range tt.issues {
				result.AddNotFoundIssue(issue)
			}

			actual := result.IsValid()
			assert.Equal(t, tt.expected, actual)
		})
	}
}

func TestRefValidationResult_IssueTypes(t *testing.T) {
	t.Run("should track not found issues", func(t *testing.T) {
		result := &RefValidationResult{}
		result.AddNotFoundIssue("HTTPRoute default/test does not exist")

		assert.False(t, result.IsValid())
		assert.True(t, result.HasNotFoundIssues())
		assert.False(t, result.HasMisconfiguredIssues())
	})

	t.Run("should track misconfigured issues", func(t *testing.T) {
		result := &RefValidationResult{}
		result.AddMisconfiguredIssue("HTTPRoute default/test does not target correct service")

		assert.False(t, result.IsValid())
		assert.False(t, result.HasNotFoundIssues())
		assert.True(t, result.HasMisconfiguredIssues())
	})

	t.Run("should track mixed issue types", func(t *testing.T) {
		result := &RefValidationResult{}
		result.AddNotFoundIssue("HTTPRoute default/test does not exist")
		result.AddMisconfiguredIssue("HTTPRoute default/other does not target correct service")

		assert.False(t, result.IsValid())
		assert.True(t, result.HasNotFoundIssues())
		assert.True(t, result.HasMisconfiguredIssues())

		expected := "- HTTPRoute default/test does not exist\n- HTTPRoute default/other does not target correct service"
		assert.Equal(t, expected, result.CombinedMessage())
	})
}
