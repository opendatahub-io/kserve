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
	"encoding/json"
	"strings"
	"testing"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/kserve/kserve/pkg/constants"
)

func TestAuditLoggingAdmissionPolicy(t *testing.T) {
	tests := []struct {
		name           string
		operation      admissionv1.Operation
		annotations    map[string]string
		oldAnnotations map[string]string
		globalConfig   string
		wantPresent    bool
		wantValue      string
		wantError      string
	}{
		{
			name:         "create snapshots enabled global setting",
			operation:    admissionv1.Create,
			annotations:  authenticatedStandardAnnotations(),
			globalConfig: `{"enableAuditLogging":true}`,
			wantPresent:  true,
			wantValue:    "true",
		},
		{
			name:         "global setting requires authentication",
			operation:    admissionv1.Create,
			annotations:  standardAnnotations(),
			globalConfig: `{"enableAuditLogging":true}`,
			wantPresent:  true,
			wantValue:    "true",
			wantError:    "requires authentication",
		},
		{
			name:         "create snapshots disabled global setting",
			operation:    admissionv1.Create,
			annotations:  standardAnnotations(),
			globalConfig: `{}`,
			wantPresent:  true,
			wantValue:    "false",
		},
		{
			name:      "explicit false overrides enabled global setting",
			operation: admissionv1.Create,
			annotations: map[string]string{
				constants.DeploymentMode:        string(constants.Standard),
				constants.ODHKserveAuditLogging: "false",
			},
			globalConfig: `{"enableAuditLogging":true}`,
			wantPresent:  true,
			wantValue:    "false",
		},
		{
			name:      "explicit true overrides disabled global setting",
			operation: admissionv1.Create,
			annotations: map[string]string{
				constants.DeploymentMode:        string(constants.Standard),
				constants.ODHKserveRawAuth:      "true",
				constants.ODHKserveAuditLogging: "TRUE",
			},
			globalConfig: `{"enableAuditLogging":false}`,
			wantPresent:  true,
			wantValue:    "TRUE",
		},
		{
			name:      "audit logging is limited to standard mode",
			operation: admissionv1.Create,
			annotations: map[string]string{
				constants.DeploymentMode:        string(constants.Knative),
				constants.ODHKserveRawAuth:      "true",
				constants.ODHKserveAuditLogging: "true",
			},
			wantPresent: true,
			wantValue:   "true",
			wantError:   "only supported",
		},
		{
			name:      "invalid override is rejected",
			operation: admissionv1.Create,
			annotations: map[string]string{
				constants.DeploymentMode:        string(constants.Standard),
				constants.ODHKserveAuditLogging: "sometimes",
			},
			wantPresent: true,
			wantValue:   "sometimes",
			wantError:   "must be true or false",
		},
		{
			name:           "update restores persisted setting when annotation is removed",
			operation:      admissionv1.Update,
			annotations:    authenticatedStandardAnnotations(),
			oldAnnotations: map[string]string{constants.ODHKserveAuditLogging: "true"},
			wantPresent:    true,
			wantValue:      "true",
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isvc := &InferenceService{ObjectMeta: metav1.ObjectMeta{Annotations: cloneAnnotations(tt.annotations)}}
			oldObject, err := json.Marshal(&InferenceService{ObjectMeta: metav1.ObjectMeta{Annotations: tt.oldAnnotations}})
			if err != nil {
				t.Fatalf("marshal old InferenceService: %v", err)
			}
			req := admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
				Operation: tt.operation,
				OldObject: runtime.RawExtension{Raw: oldObject},
			}}
			ctx := admission.NewContextWithRequest(t.Context(), req)
			configMap := &corev1.ConfigMap{Data: map[string]string{OpenShiftConfigName: tt.globalConfig}}

			err = defaultPlatformInferenceService(ctx, isvc, configMap)
			if err == nil {
				err = validatePlatformInferenceService(isvc)
			}
			if tt.wantError == "" && err != nil {
				t.Fatalf("audit admission error = %v", err)
			}
			if tt.wantError != "" && (err == nil || !strings.Contains(err.Error(), tt.wantError)) {
				t.Fatalf("audit admission error = %v, want substring %q", err, tt.wantError)
			}

			value, present := isvc.Annotations[constants.ODHKserveAuditLogging]
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

func TestAuditLoggingRejectsComponentOverride(t *testing.T) {
	isvc := &InferenceService{
		ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
			constants.DeploymentMode:        string(constants.Standard),
			constants.ODHKserveRawAuth:      "true",
			constants.ODHKserveAuditLogging: "true",
		}},
		Spec: InferenceServiceSpec{Predictor: PredictorSpec{
			ComponentExtensionSpec: ComponentExtensionSpec{Annotations: map[string]string{
				constants.ODHKserveAuditLogging: "false",
			}},
		}},
	}

	err := validatePlatformInferenceService(isvc)
	if err == nil || !strings.Contains(err.Error(), "only supported on InferenceService metadata") {
		t.Fatalf("validatePlatformInferenceService() error = %v, want component annotation rejection", err)
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
