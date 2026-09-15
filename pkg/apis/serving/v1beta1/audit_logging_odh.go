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
	"encoding/json"
	"fmt"
	"strings"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/kserve/kserve/pkg/constants"
)

func defaultPlatformInferenceService(ctx context.Context, isvc *InferenceService, configMap *corev1.ConfigMap) error {
	req, err := admission.RequestFromContext(ctx)
	if err != nil {
		return fmt.Errorf("unable to determine InferenceService admission operation for audit logging defaults: %w", err)
	}

	switch req.Operation {
	case admissionv1.Create:
		return defaultAuditLoggingOnCreate(isvc, configMap)
	case admissionv1.Update:
		return restoreAuditLoggingOnUpdate(isvc, req.OldObject.Raw)
	default:
		return nil
	}
}

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
	profile := effectiveAuditLoggingProfile(config.AuditLoggingProfile)
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

func restoreAuditLoggingOnUpdate(isvc *InferenceService, oldObject []byte) error {
	if _, present := isvc.Annotations[constants.ODHKserveAuditLoggingProfile]; present || len(oldObject) == 0 {
		return nil
	}

	var oldIsvc InferenceService
	if err := json.Unmarshal(oldObject, &oldIsvc); err != nil {
		return fmt.Errorf("unable to restore persisted audit logging setting: %w", err)
	}
	if oldValue, present := oldIsvc.Annotations[constants.ODHKserveAuditLoggingProfile]; present {
		if isvc.Annotations == nil {
			isvc.Annotations = map[string]string{}
		}
		isvc.Annotations[constants.ODHKserveAuditLoggingProfile] = oldValue
	}
	return nil
}

func validatePlatformInferenceService(isvc *InferenceService) error {
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
			return fmt.Errorf("annotation %q is only supported on InferenceService metadata, not %s annotations", constants.ODHKserveAuditLoggingProfile, component.name)
		}
	}

	auditValue, present := isvc.Annotations[constants.ODHKserveAuditLoggingProfile]
	if !present {
		return nil
	}

	profile := constants.AuditLoggingProfile(auditValue)
	if err := validateAuditLoggingProfile(profile); err != nil {
		return err
	}
	if profile == constants.AuditLoggingProfileNone {
		return nil
	}
	if !strings.EqualFold(isvc.Annotations[constants.ODHKserveRawAuth], "true") {
		return fmt.Errorf("audit logging annotation %q requires authentication annotation %q to be true", constants.ODHKserveAuditLoggingProfile, constants.ODHKserveRawAuth)
	}
	if isvc.Annotations[constants.DeploymentMode] != string(constants.Standard) {
		return fmt.Errorf("audit logging annotation %q is only supported in %s deployment mode", constants.ODHKserveAuditLoggingProfile, constants.Standard)
	}
	return nil
}

func effectiveAuditLoggingProfile(profile constants.AuditLoggingProfile) constants.AuditLoggingProfile {
	if profile == "" {
		return constants.AuditLoggingProfileNone
	}
	return profile
}

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
