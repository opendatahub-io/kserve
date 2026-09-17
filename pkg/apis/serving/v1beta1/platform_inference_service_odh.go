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
	"context"
	"fmt"
	"strings"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/kserve/kserve/pkg/constants"
)

// defaultPlatformInferenceService applies ODH-specific InferenceService defaults.
// Audit logging defaults are snapshotted only when the resource is created so
// updates cannot disturb annotation-less legacy services or restore a removed
// per-service setting.
func defaultPlatformInferenceService(ctx context.Context, isvc *InferenceService, configMap *corev1.ConfigMap) error {
	req, err := admission.RequestFromContext(ctx)
	if err != nil {
		return fmt.Errorf("unable to determine InferenceService admission operation for audit logging defaults: %w", err)
	}

	if req.Operation != admissionv1.Create {
		return nil
	}
	return defaultAuditLoggingOnCreate(isvc, configMap)
}

// defaultAuditLoggingOnCreate snapshots the global metadata profile onto new
// Standard InferenceServices unless the user supplied an explicit profile.
func defaultAuditLoggingOnCreate(isvc *InferenceService, configMap *corev1.ConfigMap) error {
	if _, present := isvc.Annotations[constants.ODHKserveAuditLoggingProfile]; present {
		return nil
	}
	if isvc.Annotations[constants.DeploymentMode] != string(constants.Standard) {
		return nil
	}

	config, err := NewOpenShiftConfig(configMap)
	if err != nil {
		return fmt.Errorf("unable to parse OpenShift audit logging configuration: %w", err)
	}
	profile := config.AuditLoggingProfile
	if profile == "" {
		profile = constants.AuditLoggingProfileNone
	}
	if err := validateAuditLoggingProfile(profile); err != nil {
		return err
	}
	if profile == constants.AuditLoggingProfileNone {
		return nil
	}
	if isvc.Annotations == nil {
		isvc.Annotations = map[string]string{}
	}
	isvc.Annotations[constants.ODHKserveAuditLoggingProfile] = string(profile)
	return nil
}

// validatePlatformInferenceService rejects unsupported top-level profile values
// and returns non-blocking admission warnings for configurations that the
// reconciler will safely disable or ignore.
func validatePlatformInferenceService(isvc *InferenceService) (admission.Warnings, error) {
	var warnings admission.Warnings
	type annotatedComponent struct {
		name        string
		annotations map[string]string
	}
	componentAnnotations := []annotatedComponent{{name: "predictor", annotations: isvc.Spec.Predictor.Annotations}}
	if isvc.Spec.Transformer != nil {
		componentAnnotations = append(componentAnnotations, annotatedComponent{name: "transformer", annotations: isvc.Spec.Transformer.Annotations})
	}
	if isvc.Spec.Explainer != nil {
		componentAnnotations = append(componentAnnotations, annotatedComponent{name: "explainer", annotations: isvc.Spec.Explainer.Annotations})
	}
	for _, component := range componentAnnotations {
		if _, present := component.annotations[constants.ODHKserveAuditLoggingProfile]; present {
			warnings = append(warnings, fmt.Sprintf("annotation %q is only supported on InferenceService metadata; the value on %s annotations will be ignored", constants.ODHKserveAuditLoggingProfile, component.name))
		}
	}

	auditValue, present := isvc.Annotations[constants.ODHKserveAuditLoggingProfile]
	if !present {
		return warnings, nil
	}

	profile := constants.AuditLoggingProfile(auditValue)
	if err := validateAuditLoggingProfile(profile); err != nil {
		return warnings, err
	}
	if profile == constants.AuditLoggingProfileNone {
		return warnings, nil
	}
	if !strings.EqualFold(isvc.Annotations[constants.ODHKserveRawAuth], "true") {
		warnings = append(warnings, fmt.Sprintf("audit logging annotation %q requires authentication annotation %q to be true; audit logging will remain disabled", constants.ODHKserveAuditLoggingProfile, constants.ODHKserveRawAuth))
	}
	if isvc.Annotations[constants.DeploymentMode] != string(constants.Standard) {
		warnings = append(warnings, fmt.Sprintf("audit logging annotation %q is only supported in %s deployment mode; audit logging will remain disabled", constants.ODHKserveAuditLoggingProfile, constants.Standard))
	}
	return warnings, nil
}

// validateAuditLoggingProfile validates the supported annotation and global
// configuration values. Annotations are not covered by CRD enum validation.
func validateAuditLoggingProfile(profile constants.AuditLoggingProfile) error {
	switch profile {
	case constants.AuditLoggingProfileNone, constants.AuditLoggingProfileMetadata:
		return nil
	default:
		return fmt.Errorf("audit logging profile must be one of %q or %q, got %q",
			constants.AuditLoggingProfileNone,
			constants.AuditLoggingProfileMetadata,
			profile,
		)
	}
}
