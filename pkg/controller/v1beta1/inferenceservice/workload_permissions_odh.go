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

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/env"
	"knative.dev/pkg/kmeta"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/kserve/kserve/pkg/apis/serving/v1beta1"
	"github.com/kserve/kserve/pkg/constants"
	"github.com/kserve/kserve/pkg/runtime"
	"github.com/kserve/kserve/pkg/utils"
)

// sccDisabled indicates whether SCC role binding reconciliation is disabled at runtime.
// When set to "true", the controller will skip creating SCC RoleBinding resources,
// useful for environments where SecurityContextConstraints are not needed.
var sccDisabled, _ = env.GetBool("ISVC_SCC_DISABLED", false)

// reconcileWorkloadPlatformPermissions reconciles platform-specific permissions (e.g., SCC RoleBindings)
// for InferenceService workloads that require special volume types like image volumes.
func (r *InferenceServiceReconciler) reconcileWorkloadPlatformPermissions(ctx context.Context, isvc *v1beta1.InferenceService) error {
	if sccDisabled {
		log.FromContext(ctx).V(2).Info("SCC is disabled via ISVC_SCC_DISABLED, skipping SCC role binding reconciliation")
		return nil
	}

	// Collect all service accounts that need image volume SCC
	serviceAccounts := getServiceAccountsRequiringImageVolumeSCC(ctx, r.Client, isvc)

	// Delete RoleBinding if runtime is being stopped or no service accounts need the SCC
	if utils.GetForceStopRuntime(isvc) || len(serviceAccounts) == 0 {
		return r.deleteImageVolumeSCCRoleBinding(ctx, isvc)
	}

	return r.reconcileImageVolumeSCCRoleBinding(ctx, isvc, serviceAccounts)
}

// getServiceAccountsRequiringImageVolumeSCC returns a list of unique service account names
// that need the image volume SCC. This includes accounts for predictor and transformer
// components that use OCI storage with legacy storageUri + MLServer runtime.
// Note: Explainer is not included as image volumes are not injected into explainer containers
// (explainers use storage-initializer instead due to custom container layouts).
func getServiceAccountsRequiringImageVolumeSCC(ctx context.Context, cl client.Client, isvc *v1beta1.InferenceService) []string {
	// Only MLServer runtime uses image volumes
	serverType, err := runtime.GetServerTypeFromIsvc(ctx, cl, isvc)
	if err != nil {
		log.FromContext(ctx).Error(err, "Failed to fetch runtime for server-type check, skipping SCC RoleBinding creation", "isvc", isvc.Name)
		return nil
	}
	if serverType == "" {
		log.FromContext(ctx).Info("Runtime server-type not available, skipping SCC RoleBinding creation", "isvc", isvc.Name)
		return nil
	}
	if serverType != constants.ServerTypeMLServer {
		return nil
	}

	accountSet := make(map[string]bool)
	var accounts []string

	// Check predictor
	if componentRequiresImageVolumeSCC(isvc.Spec.Predictor.StorageUris, isvc.Spec.Predictor.GetImplementation().GetStorageUri()) {
		saName := isvc.Spec.Predictor.ServiceAccountName
		if saName == "" {
			saName = "default"
		}
		if !accountSet[saName] {
			accountSet[saName] = true
			accounts = append(accounts, saName)
		}
	}

	// Check transformer
	if isvc.Spec.Transformer != nil {
		if componentRequiresImageVolumeSCC(isvc.Spec.Transformer.StorageUris, isvc.Spec.Transformer.GetImplementation().GetStorageUri()) {
			saName := isvc.Spec.Transformer.ServiceAccountName
			if saName == "" {
				saName = "default"
			}
			if !accountSet[saName] {
				accountSet[saName] = true
				accounts = append(accounts, saName)
			}
		}
	}

	return accounts
}

// componentRequiresImageVolumeSCC checks if a component uses OCI storage with legacy storageUri.
func componentRequiresImageVolumeSCC(storageUris []v1beta1.StorageUri, legacyStorageUri *string) bool {
	// Non-legacy path uses storageUris array - webhook doesn't handle those
	if len(storageUris) > 0 {
		return false
	}

	// Check for OCI storage in legacy storageUri
	if legacyStorageUri == nil {
		return false
	}

	return strings.HasPrefix(*legacyStorageUri, constants.OciURIPrefix)
}

// reconcileImageVolumeSCCRoleBinding creates or updates the RoleBinding for image volume SCC access.
func (r *InferenceServiceReconciler) reconcileImageVolumeSCCRoleBinding(ctx context.Context, isvc *v1beta1.InferenceService, serviceAccounts []string) error {
	expected := expectedImageVolumeSCCRoleBinding(isvc, serviceAccounts)

	existing := &rbacv1.RoleBinding{}
	err := r.Get(ctx, client.ObjectKeyFromObject(expected), existing)
	if err != nil {
		if client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("failed to get RoleBinding: %w", err)
		}
		// RoleBinding doesn't exist, create it
		log.FromContext(ctx).Info("Creating image volume SCC RoleBinding", "name", expected.Name, "serviceAccounts", serviceAccounts)
		if err := r.Create(ctx, expected); err != nil {
			return fmt.Errorf("failed to create RoleBinding %s/%s: %w", expected.Namespace, expected.Name, err)
		}
		r.Recorder.Eventf(isvc, corev1.EventTypeNormal, "Created", "Created RoleBinding %s for image volume SCC", expected.Name)
		return nil
	}

	// RoleBinding exists, update if needed
	if !semanticRoleBindingEquals(expected, existing) {
		log.FromContext(ctx).Info("Updating image volume SCC RoleBinding", "name", expected.Name, "serviceAccounts", serviceAccounts)
		existing.RoleRef = expected.RoleRef
		existing.Subjects = expected.Subjects
		existing.Labels = expected.Labels
		existing.OwnerReferences = expected.OwnerReferences
		if err := r.Update(ctx, existing); err != nil {
			return fmt.Errorf("failed to update RoleBinding %s/%s: %w", existing.Namespace, existing.Name, err)
		}
		r.Recorder.Eventf(isvc, corev1.EventTypeNormal, "Updated", "Updated RoleBinding %s for image volume SCC", expected.Name)
	}

	return nil
}

// deleteImageVolumeSCCRoleBinding deletes the RoleBinding if it exists.
func (r *InferenceServiceReconciler) deleteImageVolumeSCCRoleBinding(ctx context.Context, isvc *v1beta1.InferenceService) error {
	rb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      kmeta.ChildName(isvc.Name, "-image-volume-scc"),
			Namespace: isvc.Namespace,
		},
	}

	err := r.Delete(ctx, rb)
	if err != nil && client.IgnoreNotFound(err) != nil {
		return fmt.Errorf("failed to delete RoleBinding: %w", err)
	}
	return nil
}

// expectedImageVolumeSCCRoleBinding creates the expected RoleBinding spec.
func expectedImageVolumeSCCRoleBinding(isvc *v1beta1.InferenceService, serviceAccounts []string) *rbacv1.RoleBinding {
	// Build subjects list from service accounts
	subjects := make([]rbacv1.Subject, 0, len(serviceAccounts))
	for _, saName := range serviceAccounts {
		subjects = append(subjects, rbacv1.Subject{
			Kind:      "ServiceAccount",
			Name:      saName,
			Namespace: isvc.Namespace,
		})
	}

	return &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      kmeta.ChildName(isvc.Name, "-image-volume-scc"),
			Namespace: isvc.Namespace,
			Labels: map[string]string{
				constants.InferenceServiceLabel: isvc.Name,
			},
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(isvc, v1beta1.SchemeGroupVersion.WithKind("InferenceService")),
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "ClusterRole",
			Name:     "openshift-ai-inferenceservice-image-volume-scc",
		},
		Subjects: subjects,
	}
}

// semanticRoleBindingEquals checks if two RoleBindings are semantically equal.
// Uses Kubernetes API equality package for robust comparison.
func semanticRoleBindingEquals(desired, existing *rbacv1.RoleBinding) bool {
	return equality.Semantic.DeepDerivative(desired.Subjects, existing.Subjects) &&
		equality.Semantic.DeepDerivative(desired.RoleRef, existing.RoleRef) &&
		equality.Semantic.DeepDerivative(desired.Labels, existing.Labels) &&
		equality.Semantic.DeepDerivative(desired.OwnerReferences, existing.OwnerReferences)
}
