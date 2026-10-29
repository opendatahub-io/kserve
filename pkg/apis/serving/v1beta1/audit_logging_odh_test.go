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

package v1beta1

import (
	"strings"
	"testing"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/kserve/kserve/pkg/constants"
)

func TestAuditLoggingAdmissionPolicy(t *testing.T) {
	tests := []struct {
		name         string
		operation    admissionv1.Operation
		annotations  map[string]string
		globalConfig string
		wantPresent  bool
		wantValue    string
		wantWarnings []string
		wantError    string
	}{
		{
			name:         "create snapshots metadata global profile",
			operation:    admissionv1.Create,
			annotations:  authenticatedStandardAnnotations(),
			globalConfig: `{"auditLoggingProfile":"metadata"}`,
			wantPresent:  true,
			wantValue:    "metadata",
		},
		{
			name:         "metadata global profile requires authentication",
			operation:    admissionv1.Create,
			annotations:  standardAnnotations(),
			globalConfig: `{"auditLoggingProfile":"metadata"}`,
			wantPresent:  true,
			wantValue:    "metadata",
			wantWarnings: []string{"requires authentication"},
		},
		{
			name:      "explicit metadata without authentication is admitted with warning",
			operation: admissionv1.Create,
			annotations: map[string]string{
				constants.DeploymentMode:               string(constants.Standard),
				constants.ODHKserveAuditLoggingProfile: "metadata",
			},
			globalConfig: `{"auditLoggingProfile":"none"}`,
			wantPresent:  true,
			wantValue:    "metadata",
			wantWarnings: []string{"requires authentication"},
		},
		{
			name:         "create leaves none global profile annotationless",
			operation:    admissionv1.Create,
			annotations:  standardAnnotations(),
			globalConfig: `{"auditLoggingProfile":"none"}`,
		},
		{
			name:         "create treats omitted profile as none",
			operation:    admissionv1.Create,
			annotations:  standardAnnotations(),
			globalConfig: `{}`,
		},
		{
			name:      "create ignores metadata global profile outside standard mode",
			operation: admissionv1.Create,
			annotations: map[string]string{
				constants.DeploymentMode: string(constants.Knative),
			},
			globalConfig: `{"auditLoggingProfile":"metadata"}`,
		},
		{
			name:      "create ignores metadata global profile in modelmesh mode",
			operation: admissionv1.Create,
			annotations: map[string]string{
				constants.DeploymentMode: string(constants.ModelMeshDeployment),
			},
			globalConfig: `{"auditLoggingProfile":"metadata"}`,
		},
		{
			name:      "explicit none overrides metadata global profile",
			operation: admissionv1.Create,
			annotations: map[string]string{
				constants.DeploymentMode:               string(constants.Standard),
				constants.ODHKserveAuditLoggingProfile: "none",
			},
			globalConfig: `{"auditLoggingProfile":"metadata"}`,
			wantPresent:  true,
			wantValue:    "none",
		},
		{
			name:      "explicit metadata overrides none global profile",
			operation: admissionv1.Create,
			annotations: map[string]string{
				constants.DeploymentMode:               string(constants.Standard),
				constants.ODHKserveRawAuth:             "true",
				constants.ODHKserveAuditLoggingProfile: "metadata",
			},
			globalConfig: `{"auditLoggingProfile":"none"}`,
			wantPresent:  true,
			wantValue:    "metadata",
		},
		{
			name:      "request is rejected until implemented",
			operation: admissionv1.Create,
			annotations: map[string]string{
				constants.DeploymentMode:               string(constants.Standard),
				constants.ODHKserveRawAuth:             "true",
				constants.ODHKserveAuditLoggingProfile: "request",
			},
			wantPresent: true,
			wantValue:   "request",
			wantError:   `must be one of "none" or "metadata"`,
		},
		{
			name:      "profile matching is case sensitive",
			operation: admissionv1.Create,
			annotations: map[string]string{
				constants.DeploymentMode:               string(constants.Standard),
				constants.ODHKserveRawAuth:             "true",
				constants.ODHKserveAuditLoggingProfile: "Metadata",
			},
			wantPresent: true,
			wantValue:   "Metadata",
			wantError:   `must be one of "none" or "metadata"`,
		},
		{
			name:      "metadata is limited to standard mode",
			operation: admissionv1.Create,
			annotations: map[string]string{
				constants.DeploymentMode:               string(constants.Knative),
				constants.ODHKserveRawAuth:             "true",
				constants.ODHKserveAuditLoggingProfile: "metadata",
			},
			wantPresent:  true,
			wantValue:    "metadata",
			wantWarnings: []string{"only supported"},
		},
		{
			name:      "explicit metadata warns in modelmesh mode",
			operation: admissionv1.Create,
			annotations: map[string]string{
				constants.DeploymentMode:               string(constants.ModelMeshDeployment),
				constants.ODHKserveRawAuth:             "true",
				constants.ODHKserveAuditLoggingProfile: "metadata",
			},
			globalConfig: `{"auditLoggingProfile":"none"}`,
			wantPresent:  true,
			wantValue:    "metadata",
			wantWarnings: []string{"only supported"},
		},
		{
			name:        "update keeps removed annotation absent",
			operation:   admissionv1.Update,
			annotations: authenticatedStandardAnnotations(),
		},
		{
			name:        "legacy update remains annotationless",
			operation:   admissionv1.Update,
			annotations: standardAnnotations(),
		},
		{
			name:         "malformed platform configuration is rejected",
			operation:    admissionv1.Create,
			annotations:  standardAnnotations(),
			globalConfig: `{`,
			wantError:    "unable to parse OpenShift audit logging configuration",
		},
		{
			name:         "invalid global request profile is rejected",
			operation:    admissionv1.Create,
			annotations:  authenticatedStandardAnnotations(),
			globalConfig: `{"auditLoggingProfile":"request"}`,
			wantError:    `must be one of "none" or "metadata"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isvc := &InferenceService{ObjectMeta: metav1.ObjectMeta{Annotations: cloneAnnotations(tt.annotations)}}
			req := admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
				Operation: tt.operation,
			}}
			ctx := admission.NewContextWithRequest(t.Context(), req)
			configMap := &corev1.ConfigMap{Data: map[string]string{OpenShiftConfigName: tt.globalConfig}}

			err := defaultPlatformInferenceService(ctx, isvc, configMap)
			var warnings admission.Warnings
			if err == nil {
				warnings, err = validatePlatformInferenceService(isvc)
			}
			if tt.wantError == "" && err != nil {
				t.Fatalf("audit admission error = %v", err)
			}
			if tt.wantError != "" && (err == nil || !strings.Contains(err.Error(), tt.wantError)) {
				t.Fatalf("audit admission error = %v, want substring %q", err, tt.wantError)
			}
			joinedWarnings := strings.Join(warnings, "\n")
			for _, wantWarning := range tt.wantWarnings {
				if !strings.Contains(joinedWarnings, wantWarning) {
					t.Fatalf("audit admission warnings = %q, want substring %q", joinedWarnings, wantWarning)
				}
			}
			if len(tt.wantWarnings) == 0 && len(warnings) != 0 {
				t.Fatalf("audit admission warnings = %q, want none", joinedWarnings)
			}

			value, present := isvc.Annotations[constants.ODHKserveAuditLoggingProfile]
			if present != tt.wantPresent || present && value != tt.wantValue {
				t.Fatalf("audit annotation = %q, present %t; want %q, present %t", value, present, tt.wantValue, tt.wantPresent)
			}
		})
	}
}

func TestAuditLoggingDefaultRequiresAdmissionRequest(t *testing.T) {
	isvc := &InferenceService{ObjectMeta: metav1.ObjectMeta{Annotations: standardAnnotations()}}
	if err := defaultPlatformInferenceService(t.Context(), isvc, &corev1.ConfigMap{}); err == nil {
		t.Fatal("expected an error when the admission request is missing")
	}
}

func TestAuditLoggingWarnsForComponentOverride(t *testing.T) {
	isvc := &InferenceService{
		ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
			constants.DeploymentMode:               string(constants.Standard),
			constants.ODHKserveRawAuth:             "true",
			constants.ODHKserveAuditLoggingProfile: "metadata",
		}},
		Spec: InferenceServiceSpec{Predictor: PredictorSpec{
			ComponentExtensionSpec: ComponentExtensionSpec{Annotations: map[string]string{
				constants.ODHKserveAuditLoggingProfile: "none",
			}},
		}},
	}

	warnings, err := validatePlatformInferenceService(isvc)
	if err != nil {
		t.Fatalf("validatePlatformInferenceService() error = %v, want nil", err)
	}
	if joined := strings.Join(warnings, "\n"); !strings.Contains(joined, "only supported on InferenceService metadata") {
		t.Fatalf("validatePlatformInferenceService() warnings = %q, want component annotation warning", joined)
	}
}

func standardAnnotations() map[string]string {
	return map[string]string{constants.DeploymentMode: string(constants.Standard)}
}

func authenticatedStandardAnnotations() map[string]string {
	annotations := standardAnnotations()
	annotations[constants.ODHKserveRawAuth] = "true"
	return annotations
}

func cloneAnnotations(annotations map[string]string) map[string]string {
	result := make(map[string]string, len(annotations))
	for key, value := range annotations {
		result[key] = value
	}
	return result
}
