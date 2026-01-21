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
	"os"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
)

func TestGetBoolEnvOrDefault(t *testing.T) {
	tests := []struct {
		name     string
		key      string
		envValue string
		fallback bool
		want     bool
	}{
		{
			name:     "empty string returns fallback true",
			key:      "TEST_BOOL_ENV_EMPTY_TRUE",
			envValue: "",
			fallback: true,
			want:     true,
		},
		{
			name:     "empty string returns fallback false",
			key:      "TEST_BOOL_ENV_EMPTY_FALSE",
			envValue: "",
			fallback: false,
			want:     false,
		},
		{
			name:     "unset env returns fallback true",
			key:      "TEST_BOOL_ENV_UNSET_TRUE",
			envValue: "",
			fallback: true,
			want:     true,
		},
		{
			name:     "true returns true",
			key:      "TEST_BOOL_ENV_TRUE_LOWER",
			envValue: "true",
			fallback: false,
			want:     true,
		},
		{
			name:     "TRUE returns true (case insensitive)",
			key:      "TEST_BOOL_ENV_TRUE_UPPER",
			envValue: "TRUE",
			fallback: false,
			want:     true,
		},
		{
			name:     "True returns true (case insensitive)",
			key:      "TEST_BOOL_ENV_TRUE_MIXED",
			envValue: "True",
			fallback: false,
			want:     true,
		},
		{
			name:     "1 returns true",
			key:      "TEST_BOOL_ENV_ONE",
			envValue: "1",
			fallback: false,
			want:     true,
		},
		{
			name:     "false returns false",
			key:      "TEST_BOOL_ENV_FALSE_LOWER",
			envValue: "false",
			fallback: true,
			want:     false,
		},
		{
			name:     "FALSE returns false",
			key:      "TEST_BOOL_ENV_FALSE_UPPER",
			envValue: "FALSE",
			fallback: true,
			want:     false,
		},
		{
			name:     "0 returns false",
			key:      "TEST_BOOL_ENV_ZERO",
			envValue: "0",
			fallback: true,
			want:     false,
		},
		{
			name:     "invalid value returns fallback",
			key:      "TEST_BOOL_ENV_INVALID",
			envValue: "invalid",
			fallback: true,
			want:     false,
		},
		{
			name:     "yes returns false (not true/1)",
			key:      "TEST_BOOL_ENV_YES",
			envValue: "yes",
			fallback: true,
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set up environment
			if tt.envValue != "" {
				t.Setenv(tt.key, tt.envValue)
			} else {
				os.Unsetenv(tt.key)
			}

			got := getBoolEnvOrDefault(tt.key, tt.fallback)
			if got != tt.want {
				t.Errorf("getBoolEnvOrDefault(%q, %v) = %v, want %v", tt.key, tt.fallback, got, tt.want)
			}
		})
	}
}

func TestIsAuthEnabledForService(t *testing.T) {
	// Save original value and restore after test
	originalAuthDisabled := authDisabled
	defer func() { authDisabled = originalAuthDisabled }()

	tests := []struct {
		name         string
		authDisabled bool
		annotation   string
		want         bool
	}{
		{
			name:         "global auth disabled, no annotation",
			authDisabled: true,
			annotation:   "",
			want:         false,
		},
		{
			name:         "global auth disabled, annotation true",
			authDisabled: true,
			annotation:   "true",
			want:         false,
		},
		{
			name:         "global auth disabled, annotation false",
			authDisabled: true,
			annotation:   "false",
			want:         false,
		},
		{
			name:         "global auth enabled, no annotation (default auth enabled)",
			authDisabled: false,
			annotation:   "",
			want:         true,
		},
		{
			name:         "global auth enabled, annotation true",
			authDisabled: false,
			annotation:   "true",
			want:         true,
		},
		{
			name:         "global auth enabled, annotation false",
			authDisabled: false,
			annotation:   "false",
			want:         false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			authDisabled = tt.authDisabled

			llmSvc := &v1alpha1.LLMInferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test",
					Namespace: "default",
				},
			}
			if tt.annotation != "" {
				llmSvc.Annotations = map[string]string{
					"security.opendatahub.io/enable-auth": tt.annotation,
				}
			}

			got := isAuthEnabledForService(llmSvc)
			if got != tt.want {
				t.Errorf("isAuthEnabledForService() = %v, want %v (authDisabled=%v, annotation=%q)",
					got, tt.want, tt.authDisabled, tt.annotation)
			}
		})
	}
}
