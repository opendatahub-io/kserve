package kservemodule

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/opendatahub-io/kserve-module/pkg/apis/v1alpha1"
)

var (
	telemetryPolicyGVK = schema.GroupVersionKind{
		Group: "extensions.kuadrant.io", Version: "v1alpha1", Kind: "TelemetryPolicy",
	}
	telemetryPolicyListGVK = telemetryPolicyGVK.GroupVersion().WithKind("TelemetryPolicyList")
)

type auditLoggingCondition struct {
	reason  string
	message string
}

const (
	reasonAuditLoggingDisabled            = "Disabled"
	reasonRHCLDependencyUnavailable       = "RHCLDependencyUnavailable"
	reasonGatewayConfigurationUnavailable = "GatewayConfigurationUnavailable"
	reasonAuditLoggingConfigured          = "OCSFAuditLoggingConfigured"
	reasonAuditLoggingReconcileFailed     = "ReconcileFailed"
	ocsfDependencyMessage                 = "LLMInferenceService OCSF audit logging requires RHCL 1.5.1 or later"
)

// reconcileOCSFAuditLogging manages only the TelemetryPolicy identified by the
// KServe ownership metadata. TelemetryPolicy is intentionally handled as an
// unstructured optional dependency: RHCL versions before 1.5.1 either do not
// serve the CRD or serve it without the OCSF stanza.
func (r *KserveModuleReconciler) reconcileOCSFAuditLogging(ctx context.Context, kserve *platformv1alpha1.Kserve) error {
	if kserve.Spec.AuditLoggingProfile != platformv1alpha1.AuditProfileMetadata {
		return r.cleanupOCSFAuditPolicies(ctx, nil, kserve.Name, kserve.UID)
	}

	supported, err := r.telemetryPolicyOCSFSupported(ctx)
	if err != nil || !supported {
		r.ocsfAuditLoggingCondition = &auditLoggingCondition{
			reason:  reasonRHCLDependencyUnavailable,
			message: fmt.Sprintf("%s: %v", ocsfDependencyMessage, err),
		}
		return nil
	}

	gatewayNamespace, gatewayName, err := r.llmInferenceGateway(ctx)
	if err != nil {
		r.ocsfAuditLoggingCondition = &auditLoggingCondition{
			reason:  reasonGatewayConfigurationUnavailable,
			message: err.Error(),
		}
		return nil
	}

	desired := desiredOCSFPolicy(kserve, gatewayNamespace, gatewayName)
	if err := r.ensureOCSFPolicy(ctx, kserve, desired); err != nil {
		r.ocsfAuditLoggingCondition = &auditLoggingCondition{
			reason:  reasonAuditLoggingReconcileFailed,
			message: err.Error(),
		}
		return err
	}
	return r.cleanupOCSFAuditPolicies(ctx, desired, kserve.Name, kserve.UID)
}

func (r *KserveModuleReconciler) telemetryPolicyOCSFSupported(ctx context.Context) (bool, error) {
	reader := r.apiReader
	if reader == nil {
		reader = r.Client
	}

	crd := &apiextensionsv1.CustomResourceDefinition{}
	err := reader.Get(ctx, client.ObjectKey{Name: telemetryPolicyGVKName}, crd)
	if err != nil {
		if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
			return false, fmt.Errorf("TelemetryPolicy CRD is unavailable")
		}
		return false, fmt.Errorf("checking TelemetryPolicy CRD: %w", err)
	}
	if !telemetryPolicyCRDEstablished(crd) {
		return false, fmt.Errorf("TelemetryPolicy CRD is not established")
	}

	for _, version := range crd.Spec.Versions {
		if version.Name == telemetryPolicyGVK.Version && version.Served && supportsOCSF(version.Schema) {
			return true, nil
		}
	}
	return false, fmt.Errorf("TelemetryPolicy CRD does not support spec.logging.default.ocsf")
}

func telemetryPolicyCRDEstablished(crd *apiextensionsv1.CustomResourceDefinition) bool {
	for _, condition := range crd.Status.Conditions {
		if condition.Type == apiextensionsv1.Established && condition.Status == apiextensionsv1.ConditionTrue {
			return true
		}
	}
	return false
}

func supportsOCSF(schema *apiextensionsv1.CustomResourceValidation) bool {
	if schema == nil || schema.OpenAPIV3Schema == nil {
		return false
	}
	paths := [][]string{
		{"spec", "logging", "default", "ocsf", "profile"},
		{"spec", "logging", "default", "ocsf", "actor", "identity"},
		{"spec", "logging", "default", "ocsf", "target", "resourceType"},
		{"spec", "logging", "default", "ocsf", "target", "aiProvider"},
		{"spec", "logging", "default", "ocsf", "target", "requestPath", "namespaceSegment"},
		{"spec", "logging", "default", "ocsf", "target", "requestPath", "nameSegment"},
	}
	for _, path := range paths {
		if !schemaPathExists(schema.OpenAPIV3Schema, path...) {
			return false
		}
	}
	return true
}

func schemaPathExists(root *apiextensionsv1.JSONSchemaProps, path ...string) bool {
	current := root
	for _, name := range path {
		if current == nil {
			return false
		}
		next, ok := current.Properties[name]
		if !ok {
			return false
		}
		current = &next
	}
	return true
}

func (r *KserveModuleReconciler) llmInferenceGateway(ctx context.Context) (string, string, error) {
	config := &corev1.ConfigMap{}
	if err := r.Get(ctx, client.ObjectKey{
		Name: kserveConfigMapName, Namespace: r.getApplicationsNamespace(),
	}, config); err != nil {
		return "", "", fmt.Errorf("reading %s/%s: %w", r.getApplicationsNamespace(), kserveConfigMapName, err)
	}

	var ingress map[string]any
	if err := json.Unmarshal([]byte(config.Data[ingressConfigKeyName]), &ingress); err != nil {
		return "", "", fmt.Errorf("parsing %s ConfigMap key %q: %w", kserveConfigMapName, ingressConfigKeyName, err)
	}
	ref, ok := ingress["kserveIngressGateway"].(string)
	if !ok || strings.TrimSpace(ref) == "" {
		return "", "", fmt.Errorf("%s ConfigMap key %q does not define kserveIngressGateway", kserveConfigMapName, ingressConfigKeyName)
	}
	parts := strings.Split(ref, "/")
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", "", fmt.Errorf("kserveIngressGateway must use <namespace>/<name>, got %q", ref)
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), nil
}

func desiredOCSFPolicy(kserve *platformv1alpha1.Kserve, namespace, gatewayName string) *unstructured.Unstructured {
	policy := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": telemetryPolicyGVK.GroupVersion().String(),
		"kind":       telemetryPolicyGVK.Kind,
		"metadata": map[string]any{
			"name":      telemetryPolicyName,
			"namespace": namespace,
		},
		"spec": map[string]any{
			"targetRef": map[string]any{
				"group": "gateway.networking.k8s.io",
				"kind":  "Gateway",
				"name":  gatewayName,
			},
			"logging": map[string]any{
				"default": map[string]any{
					"ocsf": map[string]any{
						"profile": "aiOperation",
						"actor": map[string]any{
							"identity": "auth.identity.user.username",
							"uid":      "auth.identity.user.uid",
							"groups":   "auth.identity.user.groups",
						},
						"target": map[string]any{
							"resourceType": "LLMInferenceService",
							"aiProvider":   "KServe",
							"requestPath": map[string]any{
								"namespaceSegment": int64(0),
								"nameSegment":      int64(1),
							},
						},
					},
				},
			},
		},
	}}
	policy.SetGroupVersionKind(telemetryPolicyGVK)
	policy.SetLabels(map[string]string{
		telemetryPolicyManagedLabel:   "true",
		telemetryPolicyComponentLabel: telemetryPolicyComponentValue,
	})
	policy.SetAnnotations(map[string]string{
		telemetryPolicyOwnerAnnotation: kserve.Name,
	})
	controller := true
	blockOwnerDeletion := true
	policy.SetOwnerReferences([]metav1.OwnerReference{{
		APIVersion:         platformv1alpha1.GroupVersion.String(),
		Kind:               platformv1alpha1.KserveKind,
		Name:               kserve.Name,
		UID:                kserve.UID,
		Controller:         &controller,
		BlockOwnerDeletion: &blockOwnerDeletion,
	}})
	return policy
}

func (r *KserveModuleReconciler) ensureOCSFPolicy(ctx context.Context, kserve *platformv1alpha1.Kserve, desired *unstructured.Unstructured) error {
	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(telemetryPolicyGVK)
	key := client.ObjectKeyFromObject(desired)
	if err := r.Get(ctx, key, existing); err != nil {
		if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
			return r.Create(ctx, desired)
		}
		return fmt.Errorf("getting KServe-owned TelemetryPolicy %s/%s: %w", key.Namespace, key.Name, err)
	}

	if !telemetryPolicyOwnedBy(existing, kserve.Name, kserve.UID) {
		// Never adopt a same-named user policy. The stable name is only a lookup
		// optimization; ownership metadata is the deletion and update boundary.
		r.ocsfAuditLoggingCondition = &auditLoggingCondition{
			reason:  "PolicyOwnershipConflict",
			message: fmt.Sprintf("TelemetryPolicy %s/%s exists but is not owned by KServe; leaving it unchanged", key.Namespace, key.Name),
		}
		return nil
	}

	if telemetryPolicyDrifted(existing, desired) {
		existing.Object["spec"] = desired.Object["spec"]
		labels := existing.GetLabels()
		if labels == nil {
			labels = map[string]string{}
		}
		for key, value := range desired.GetLabels() {
			labels[key] = value
		}
		existing.SetLabels(labels)
		annotations := existing.GetAnnotations()
		if annotations == nil {
			annotations = map[string]string{}
		}
		for key, value := range desired.GetAnnotations() {
			annotations[key] = value
		}
		existing.SetAnnotations(annotations)
		existing.SetOwnerReferences(desired.GetOwnerReferences())
		if err := r.Update(ctx, existing); err != nil {
			return fmt.Errorf("updating KServe-owned TelemetryPolicy %s/%s: %w", key.Namespace, key.Name, err)
		}
	}
	return nil
}

func telemetryPolicyDrifted(existing, desired *unstructured.Unstructured) bool {
	if !reflect.DeepEqual(existing.Object["spec"], desired.Object["spec"]) {
		return true
	}
	for key, value := range desired.GetLabels() {
		if existing.GetLabels()[key] != value {
			return true
		}
	}
	for key, value := range desired.GetAnnotations() {
		if existing.GetAnnotations()[key] != value {
			return true
		}
	}
	return !reflect.DeepEqual(existing.GetOwnerReferences(), desired.GetOwnerReferences())
}

func telemetryPolicyOwnedBy(policy *unstructured.Unstructured, ownerName string, ownerUID types.UID) bool {
	if policy.GetLabels()[telemetryPolicyManagedLabel] != "true" ||
		policy.GetLabels()[telemetryPolicyComponentLabel] != telemetryPolicyComponentValue ||
		policy.GetAnnotations()[telemetryPolicyOwnerAnnotation] != ownerName {
		return false
	}
	for _, ref := range policy.GetOwnerReferences() {
		if ref.APIVersion != platformv1alpha1.GroupVersion.String() ||
			ref.Kind != platformv1alpha1.KserveKind || ref.Name != ownerName ||
			ref.Controller == nil || !*ref.Controller {
			continue
		}
		return ownerUID == "" || ref.UID == ownerUID
	}
	return false
}

func (r *KserveModuleReconciler) cleanupOCSFAuditPolicies(ctx context.Context, keep *unstructured.Unstructured, ownerName string, ownerUID types.UID) error {
	policies := &unstructured.UnstructuredList{}
	policies.SetGroupVersionKind(telemetryPolicyListGVK)
	if err := r.List(ctx, policies); err != nil {
		if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
			return nil
		}
		return fmt.Errorf("listing KServe-owned TelemetryPolicies: %w", err)
	}

	for i := range policies.Items {
		policy := &policies.Items[i]
		if !telemetryPolicyOwnedBy(policy, ownerName, ownerUID) {
			continue
		}
		if keep != nil && policy.GetName() == keep.GetName() && policy.GetNamespace() == keep.GetNamespace() {
			continue
		}
		if err := r.Delete(ctx, policy); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("deleting KServe-owned TelemetryPolicy %s/%s: %w", policy.GetNamespace(), policy.GetName(), err)
		}
	}
	return nil
}
