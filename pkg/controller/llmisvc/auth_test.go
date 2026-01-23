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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
)

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
