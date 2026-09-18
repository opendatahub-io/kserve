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

package inferenceservice

import (
	"context"
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"knative.dev/pkg/apis"

	"github.com/kserve/kserve/pkg/apis/serving/v1beta1"
	"github.com/kserve/kserve/pkg/constants"
)

const (
	auditLoggingConfiguredCondition apis.ConditionType = "AuditLoggingConfigured"

	auditLoggingReasonDisabled                  = "AuditLoggingDisabled"
	auditLoggingReasonEnabled                   = "AuditLoggingEnabled"
	auditLoggingReasonAuthenticationRequired    = "AuthenticationRequired"
	auditLoggingReasonUnsupportedDeploymentMode = "UnsupportedDeploymentMode"
	auditLoggingReasonComponentAnnotation       = "ComponentAnnotationIgnored"
	auditLoggingReasonProxyMigration            = "ProxyMigrationRequired"
	auditLoggingReasonReconciling               = "AuditLoggingReconciling"
	auditLoggingReasonReconciliationDisabled    = "ReconciliationDisabled"
	auditLoggingReasonMultipleIssues            = "MultipleConfigurationIssues"
	auditLoggingReasonInvalidProfile            = "InvalidProfile"
)

type auditLoggingIssue struct {
	reason  string
	message string
	blocks  bool
}

type auditLoggingResolution struct {
	requestedProfile constants.AuditLoggingProfile
	desiredProfile   constants.AuditLoggingProfile
	condition        *apis.Condition
}

// reconcilePlatformInferenceService resolves and records distro-specific
// InferenceService policy for controller-owned Standard workloads.
func (r *InferenceServiceReconciler) reconcilePlatformInferenceService(
	ctx context.Context,
	isvc *v1beta1.InferenceService,
	deploymentMode constants.DeploymentModeType,
	reconciliationPaused bool,
) (constants.AuditLoggingProfile, bool, error) {
	if deploymentMode == constants.ModelMeshDeployment {
		return constants.AuditLoggingProfileNone, false, nil
	}

	_, explicitProfile := isvc.Annotations[constants.ODHKserveAuditLoggingProfile]
	manageProfile := explicitProfile || isvc.Status.GetCondition(auditLoggingConfiguredCondition) != nil
	if !manageProfile {
		// Annotation-less services without controller-owned status predate audit
		// management. Do not claim ownership or change their pod template.
		return constants.AuditLoggingProfileNone, false, nil
	}

	existingProxyType, observedProfile, err := r.auditLoggingDeploymentState(ctx, isvc)
	if err != nil {
		return constants.AuditLoggingProfileNone, false, err
	}

	resolution := resolveAuditLoggingPolicy(isvc, deploymentMode, existingProxyType, observedProfile, reconciliationPaused)
	applyAuditLoggingResolution(isvc, resolution, r.Recorder)
	return resolution.desiredProfile, true, nil
}

// auditLoggingDeploymentState reports the predictor's current proxy type and
// the audit profile actually represented by a kube-rbac-proxy container.
func (r *InferenceServiceReconciler) auditLoggingDeploymentState(
	ctx context.Context,
	isvc *v1beta1.InferenceService,
) (string, constants.AuditLoggingProfile, error) {
	deployment := &appsv1.Deployment{}
	name := constants.PredictorServiceName(isvc.Name, isvc.Spec.Predictor.Name)
	if err := r.Get(ctx, types.NamespacedName{Namespace: isvc.Namespace, Name: name}, deployment); err != nil {
		if apierrors.IsNotFound(err) {
			return "", constants.AuditLoggingProfileNone, nil
		}
		return "", constants.AuditLoggingProfileNone, fmt.Errorf("failed to inspect predictor deployment %s/%s: %w", isvc.Namespace, name, err)
	}

	for _, container := range deployment.Spec.Template.Spec.Containers {
		switch container.Name {
		case constants.KubeRbacContainerName:
			for _, arg := range container.Args {
				if arg == "--audit-log-profile=metadata" {
					return constants.KubeRbacContainerName, constants.AuditLoggingProfileMetadata, nil
				}
			}
			return constants.KubeRbacContainerName, constants.AuditLoggingProfileNone, nil
		case constants.OauthProxyContainerName:
			return constants.OauthProxyContainerName, constants.AuditLoggingProfileNone, nil
		}
	}

	return "", constants.AuditLoggingProfileNone, nil
}

// resolveAuditLoggingPolicy determines the requested and desired profiles
// without mutating the InferenceService or its generated workloads.
func resolveAuditLoggingPolicy(
	isvc *v1beta1.InferenceService,
	deploymentMode constants.DeploymentModeType,
	existingProxyType string,
	observedProfile constants.AuditLoggingProfile,
	reconciliationPaused bool,
) auditLoggingResolution {
	requestedProfile := constants.AuditLoggingProfileNone
	issues := make([]auditLoggingIssue, 0, 4)

	if value, present := isvc.Annotations[constants.ODHKserveAuditLoggingProfile]; present {
		switch constants.AuditLoggingProfile(value) {
		case constants.AuditLoggingProfileNone:
			requestedProfile = constants.AuditLoggingProfileNone
		case constants.AuditLoggingProfileMetadata:
			requestedProfile = constants.AuditLoggingProfileMetadata
		default:
			issues = append(issues, auditLoggingIssue{
				reason:  auditLoggingReasonInvalidProfile,
				message: fmt.Sprintf("Audit logging profile %q is invalid; supported values are %q and %q.", value, constants.AuditLoggingProfileNone, constants.AuditLoggingProfileMetadata),
				blocks:  true,
			})
		}
	}

	if components := auditLoggingAnnotatedComponents(isvc); len(components) > 0 {
		issues = append(issues, auditLoggingIssue{
			reason: auditLoggingReasonComponentAnnotation,
			message: fmt.Sprintf(
				"Audit logging annotation %q on %s component metadata is ignored; set it on InferenceService metadata instead.",
				constants.ODHKserveAuditLoggingProfile,
				strings.Join(components, ", "),
			),
		})
	}

	desiredProfile := constants.AuditLoggingProfileNone
	if requestedProfile == constants.AuditLoggingProfileMetadata {
		if deploymentMode != constants.Standard {
			issues = append(issues, auditLoggingIssue{
				reason:  auditLoggingReasonUnsupportedDeploymentMode,
				message: fmt.Sprintf("Metadata audit logging is supported only in %s deployment mode; the current mode is %s.", constants.Standard, deploymentMode),
				blocks:  true,
			})
		}
		if !strings.EqualFold(isvc.Annotations[constants.ODHKserveRawAuth], "true") {
			issues = append(issues, auditLoggingIssue{
				reason:  auditLoggingReasonAuthenticationRequired,
				message: fmt.Sprintf("Enable %s to activate metadata audit logging.", constants.ODHKserveRawAuth),
				blocks:  true,
			})
		}
		if !hasBlockingAuditLoggingIssue(issues) &&
			existingProxyType == constants.OauthProxyContainerName &&
			isvc.Annotations[constants.ODHAuthProxyTypeAnnotation] != constants.KubeRbacProxyType {
			issues = append(issues, auditLoggingIssue{
				reason: auditLoggingReasonProxyMigration,
				message: fmt.Sprintf(
					"Set %s=%s to migrate the existing oauth-proxy before metadata audit logging can be enabled.",
					constants.ODHAuthProxyTypeAnnotation,
					constants.KubeRbacProxyType,
				),
				blocks: true,
			})
		}
		if !hasBlockingAuditLoggingIssue(issues) {
			desiredProfile = constants.AuditLoggingProfileMetadata
		}
	}

	if observedProfile == "" {
		observedProfile = constants.AuditLoggingProfileNone
	}
	if reconciliationPaused && desiredProfile != observedProfile {
		issues = append(issues, auditLoggingIssue{
			reason:  auditLoggingReasonReconciliationDisabled,
			message: "Audit logging changes are pending because automatic reconciliation is disabled for this InferenceService.",
			blocks:  true,
		})
	} else if !hasBlockingAuditLoggingIssue(issues) && desiredProfile != observedProfile {
		issues = append(issues, auditLoggingIssue{
			reason: auditLoggingReasonReconciling,
			message: fmt.Sprintf(
				"Audit logging profile %q is pending reconciliation; the predictor Deployment currently has profile %q.",
				desiredProfile,
				observedProfile,
			),
			blocks: true,
		})
	}

	condition := auditLoggingCondition(desiredProfile, issues)
	return auditLoggingResolution{
		requestedProfile: requestedProfile,
		desiredProfile:   desiredProfile,
		condition:        condition,
	}
}

// auditLoggingAnnotatedComponents returns component names carrying an ignored
// component-level audit logging annotation in stable predictor-first order.
func auditLoggingAnnotatedComponents(isvc *v1beta1.InferenceService) []string {
	components := make([]string, 0, 3)
	if _, present := isvc.Spec.Predictor.Annotations[constants.ODHKserveAuditLoggingProfile]; present {
		components = append(components, "predictor")
	}
	if isvc.Spec.Transformer != nil {
		if _, present := isvc.Spec.Transformer.Annotations[constants.ODHKserveAuditLoggingProfile]; present {
			components = append(components, "transformer")
		}
	}
	if isvc.Spec.Explainer != nil {
		if _, present := isvc.Spec.Explainer.Annotations[constants.ODHKserveAuditLoggingProfile]; present {
			components = append(components, "explainer")
		}
	}
	return components
}

// hasBlockingAuditLoggingIssue reports whether audit logging must remain
// disabled because at least one prerequisite is unmet.
func hasBlockingAuditLoggingIssue(issues []auditLoggingIssue) bool {
	for _, issue := range issues {
		if issue.blocks {
			return true
		}
	}
	return false
}

// auditLoggingCondition creates the advisory condition for a resolved policy.
func auditLoggingCondition(desiredProfile constants.AuditLoggingProfile, issues []auditLoggingIssue) *apis.Condition {
	condition := &apis.Condition{Type: auditLoggingConfiguredCondition}
	if len(issues) == 0 {
		condition.Status = corev1.ConditionTrue
		if desiredProfile == constants.AuditLoggingProfileMetadata {
			condition.Reason = auditLoggingReasonEnabled
			condition.Message = "Metadata audit logging is enabled."
		} else {
			condition.Reason = auditLoggingReasonDisabled
			condition.Message = "Audit logging is disabled."
		}
		return condition
	}

	condition.Status = corev1.ConditionFalse
	condition.Reason = issues[0].reason
	if len(issues) > 1 {
		condition.Reason = auditLoggingReasonMultipleIssues
	}
	messages := make([]string, 0, len(issues))
	for _, issue := range issues {
		messages = append(messages, issue.message)
	}
	condition.Message = strings.Join(messages, " ")
	return condition
}

// applyAuditLoggingResolution records the advisory audit condition and emits a Warning Event
// only when the advisory failure condition changes.
func applyAuditLoggingResolution(isvc *v1beta1.InferenceService, resolution auditLoggingResolution, recorder record.EventRecorder) {
	previous := isvc.Status.GetCondition(auditLoggingConfiguredCondition)
	changedFailure := resolution.condition.Status == corev1.ConditionFalse &&
		(previous == nil || previous.Status != resolution.condition.Status || previous.Reason != resolution.condition.Reason || previous.Message != resolution.condition.Message)

	isvc.Status.SetCondition(auditLoggingConfiguredCondition, resolution.condition)

	if changedFailure && recorder != nil {
		recorder.Event(isvc, corev1.EventTypeWarning, resolution.condition.Reason, resolution.condition.Message)
	}
}
