//go:build distro

/*
Copyright 2023 The KServe Authors.

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

	rbacv1 "k8s.io/api/rbac/v1"
	apierr "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/kserve/kserve/pkg/apis/serving/v1beta1"
	"github.com/kserve/kserve/pkg/constants"
)

const defaultServiceAccountName = "default"

var permLog = logf.Log.WithName("PlatformPermissions")

func reconcilePlatformPermissions(ctx context.Context, cl client.Client, isvc *v1beta1.InferenceService) error {
	saName := defaultServiceAccountName
	if isvc.Spec.Predictor.ServiceAccountName != "" {
		saName = isvc.Spec.Predictor.ServiceAccountName
	}

	authEnabled := false
	if val, ok := isvc.Annotations[constants.ODHKserveRawAuth]; ok && strings.EqualFold(val, "true") {
		authEnabled = true
	}

	// TODO: check what odh-model-controller produces for CRB names — the "-" separator
	// can cause collisions (e.g. namespace "a" + SA "b-c" vs namespace "a-b" + SA "c").
	crbName := isvc.Namespace + "-" + saName + "-auth-delegator"

	if !authEnabled {
		othersNeedCRB, err := otherISVCsNeedAuthCRB(ctx, cl, isvc, saName)
		if err != nil {
			return err
		}
		if !othersNeedCRB {
			return deleteClusterRoleBindingIfExists(ctx, cl, crbName)
		}
		return nil
	}

	desired := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: crbName,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      saName,
				Namespace: isvc.Namespace,
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     "system:auth-delegator",
		},
	}

	existing := &rbacv1.ClusterRoleBinding{}
	err := cl.Get(ctx, types.NamespacedName{Name: crbName}, existing)
	if apierr.IsNotFound(err) {
		permLog.Info("Creating ClusterRoleBinding", "name", crbName)
		if err := cl.Create(ctx, desired); err != nil {
			return fmt.Errorf("failed to create ClusterRoleBinding %s: %w", crbName, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to get ClusterRoleBinding %s: %w", crbName, err)
	}

	if !equalCRB(desired, existing) {
		if existing.RoleRef != desired.RoleRef {
			permLog.Info("RoleRef changed — deleting and recreating ClusterRoleBinding", "name", crbName)
			if err := cl.Delete(ctx, existing); err != nil {
				return fmt.Errorf("failed to delete ClusterRoleBinding %s for recreation: %w", crbName, err)
			}
			if err := cl.Create(ctx, desired); err != nil {
				return fmt.Errorf("failed to recreate ClusterRoleBinding %s: %w", crbName, err)
			}
			return nil
		}
		existing.Subjects = desired.Subjects
		permLog.Info("Updating ClusterRoleBinding", "name", crbName)
		if err := cl.Update(ctx, existing); err != nil {
			return fmt.Errorf("failed to update ClusterRoleBinding %s: %w", crbName, err)
		}
	}
	return nil
}

func otherISVCsNeedAuthCRB(ctx context.Context, cl client.Client, currentISVC *v1beta1.InferenceService, saName string) (bool, error) {
	var isvcList v1beta1.InferenceServiceList
	if err := cl.List(ctx, &isvcList, client.InNamespace(currentISVC.Namespace)); err != nil {
		return false, fmt.Errorf("failed to list InferenceServices: %w", err)
	}
	for i := range isvcList.Items {
		isvc := &isvcList.Items[i]
		if isvc.Name == currentISVC.Name && isvc.Namespace == currentISVC.Namespace {
			continue
		}
		if isvc.DeletionTimestamp != nil {
			continue
		}
		if val, ok := isvc.Annotations[constants.ODHKserveRawAuth]; !ok || !strings.EqualFold(val, "true") {
			continue
		}
		isvcSA := defaultServiceAccountName
		if isvc.Spec.Predictor.ServiceAccountName != "" {
			isvcSA = isvc.Spec.Predictor.ServiceAccountName
		}
		if isvcSA == saName {
			return true, nil
		}
	}
	return false, nil
}

func deleteClusterRoleBindingIfExists(ctx context.Context, cl client.Client, name string) error {
	crb := &rbacv1.ClusterRoleBinding{}
	err := cl.Get(ctx, types.NamespacedName{Name: name}, crb)
	if apierr.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to get ClusterRoleBinding %s: %w", name, err)
	}
	permLog.Info("Deleting ClusterRoleBinding", "name", name)
	if err := cl.Delete(ctx, crb); err != nil {
		return fmt.Errorf("failed to delete ClusterRoleBinding %s: %w", name, err)
	}
	return nil
}

func equalCRB(a, b *rbacv1.ClusterRoleBinding) bool {
	if a.RoleRef != b.RoleRef {
		return false
	}
	if len(a.Subjects) != len(b.Subjects) {
		return false
	}
	for i := range a.Subjects {
		if a.Subjects[i] != b.Subjects[i] {
			return false
		}
	}
	return true
}
