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
	"strconv"
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
	if _, present := isvc.Annotations[constants.ODHKserveAuditLogging]; present {
		return nil
	}
	if isvc.Annotations[constants.DeploymentMode] != string(constants.Standard) {
		return nil
	}

	config, err := NewOpenShiftConfig(configMap)
	if err != nil {
		return fmt.Errorf("unable to parse OpenShift audit logging configuration: %w", err)
	}
	if isvc.Annotations == nil {
		isvc.Annotations = map[string]string{}
	}
	isvc.Annotations[constants.ODHKserveAuditLogging] = strconv.FormatBool(config.EnableAuditLogging)
	return nil
}

func restoreAuditLoggingOnUpdate(isvc *InferenceService, oldObject []byte) error {
	if _, present := isvc.Annotations[constants.ODHKserveAuditLogging]; present || len(oldObject) == 0 {
		return nil
	}

	var oldIsvc InferenceService
	if err := json.Unmarshal(oldObject, &oldIsvc); err != nil {
		return fmt.Errorf("unable to restore persisted audit logging setting: %w", err)
	}
	if oldValue, present := oldIsvc.Annotations[constants.ODHKserveAuditLogging]; present {
		if isvc.Annotations == nil {
			isvc.Annotations = map[string]string{}
		}
		isvc.Annotations[constants.ODHKserveAuditLogging] = oldValue
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
		if _, present := component.annotations[constants.ODHKserveAuditLogging]; present {
			return fmt.Errorf("annotation %q is only supported on InferenceService metadata, not %s annotations", constants.ODHKserveAuditLogging, component.name)
		}
	}

	auditValue, present := isvc.Annotations[constants.ODHKserveAuditLogging]
	if !present {
		return nil
	}

	auditEnabled := strings.EqualFold(auditValue, "true")
	if !auditEnabled && !strings.EqualFold(auditValue, "false") {
		return fmt.Errorf("annotation %q must be true or false, got %q", constants.ODHKserveAuditLogging, auditValue)
	}
	if !auditEnabled {
		return nil
	}
	if !strings.EqualFold(isvc.Annotations[constants.ODHKserveRawAuth], "true") {
		return fmt.Errorf("audit logging annotation %q requires authentication annotation %q to be true", constants.ODHKserveAuditLogging, constants.ODHKserveRawAuth)
	}
	if isvc.Annotations[constants.DeploymentMode] != string(constants.Standard) {
		return fmt.Errorf("audit logging annotation %q is only supported in %s deployment mode", constants.ODHKserveAuditLogging, constants.Standard)
	}
	return nil
}
