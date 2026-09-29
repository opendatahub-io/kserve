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

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/kserve/kserve/pkg/apis/serving/v1beta1"
	"github.com/kserve/kserve/pkg/constants"
)

const authDelegatorBindingSuffix = "-auth-delegator"

// authDelegatorRoleRef lets the auth proxy in front of a predictor review tokens and access.
var authDelegatorRoleRef = rbacv1.RoleRef{
	APIGroup: rbacv1.GroupName,
	Kind:     "ClusterRole",
	Name:     "system:auth-delegator",
}

// reconcileAuthDelegatorBinding keeps the system:auth-delegator ClusterRoleBinding of the
// predictor service account while an auth-enabled InferenceService in the namespace runs as it,
// and removes it once none does.
//
// The binding is named and shaped like the one odh-model-controller creates, and carries no
// owner reference: it is cluster-scoped and shared by every InferenceService using the account.
// Names can collide across namespaces (a-b/c and a/b-c), so a binding with the name is only
// changed when it grants system:auth-delegator to the service account.
func (r *InferenceServiceReconciler) reconcileAuthDelegatorBinding(ctx context.Context, isvc *v1beta1.InferenceService) error {
	serviceAccount := predictorServiceAccount(isvc)
	desired := desiredAuthDelegatorBinding(isvc.Namespace, serviceAccount)
	existing, err := r.getAuthDelegatorBinding(ctx, desired.Name)
	if err != nil {
		return err
	}

	if !authDelegatorRequested(isvc) {
		// A missing binding is left to the auth-enabled InferenceServices that need it.
		if existing == nil || !grantsAuthDelegator(existing, isvc.Namespace, serviceAccount) {
			return nil
		}
		inUse, err := r.authDelegatorInUse(ctx, isvc.Namespace, serviceAccount)
		if err != nil {
			return err
		}
		if !inUse {
			return r.deleteAuthDelegatorBinding(ctx, existing)
		}
	}

	switch {
	case existing == nil:
		log.FromContext(ctx).Info("Creating auth-delegator ClusterRoleBinding", "name", desired.Name)
		if err := r.Create(ctx, desired); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("failed to create ClusterRoleBinding %s: %w", desired.Name, err)
		}
	case !grantsAuthDelegator(existing, isvc.Namespace, serviceAccount):
		log.FromContext(ctx).Info("ClusterRoleBinding does not grant system:auth-delegator to the predictor service account, leaving it untouched",
			"name", existing.Name, "serviceAccount", serviceAccount)
	case !equality.Semantic.DeepEqual(desired.Subjects, existing.Subjects):
		log.FromContext(ctx).Info("Updating auth-delegator ClusterRoleBinding", "name", existing.Name)
		updated := existing.DeepCopy()
		updated.Subjects = desired.Subjects
		if err := r.Update(ctx, updated); err != nil {
			return fmt.Errorf("failed to update ClusterRoleBinding %s: %w", existing.Name, err)
		}
	}
	return nil
}

// releaseAuthDelegatorBinding deletes the binding of a deleted InferenceService's predictor
// service account unless another auth-enabled InferenceService in the namespace still uses it.
func (r *InferenceServiceReconciler) releaseAuthDelegatorBinding(ctx context.Context, isvc *v1beta1.InferenceService) error {
	serviceAccount := predictorServiceAccount(isvc)
	existing, err := r.getAuthDelegatorBinding(ctx, desiredAuthDelegatorBinding(isvc.Namespace, serviceAccount).Name)
	if err != nil || existing == nil || !grantsAuthDelegator(existing, isvc.Namespace, serviceAccount) {
		return err
	}
	inUse, err := r.authDelegatorInUse(ctx, isvc.Namespace, serviceAccount)
	if err != nil || inUse {
		return err
	}
	return r.deleteAuthDelegatorBinding(ctx, existing)
}

// getAuthDelegatorBinding returns the named ClusterRoleBinding, or nil when there is none.
func (r *InferenceServiceReconciler) getAuthDelegatorBinding(ctx context.Context, name string) (*rbacv1.ClusterRoleBinding, error) {
	binding := &rbacv1.ClusterRoleBinding{}
	if err := r.Get(ctx, client.ObjectKey{Name: name}, binding); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get ClusterRoleBinding %s: %w", name, err)
	}
	return binding, nil
}

// authDelegatorInUse reports whether an auth-enabled InferenceService that is not being deleted
// runs its predictor as serviceAccount in the namespace.
func (r *InferenceServiceReconciler) authDelegatorInUse(ctx context.Context, namespace, serviceAccount string) (bool, error) {
	isvcs := &v1beta1.InferenceServiceList{}
	if err := r.List(ctx, isvcs, client.InNamespace(namespace)); err != nil {
		return false, fmt.Errorf("failed to list InferenceServices in namespace %s: %w", namespace, err)
	}
	for i := range isvcs.Items {
		other := &isvcs.Items[i]
		if other.DeletionTimestamp == nil && authDelegatorRequested(other) && predictorServiceAccount(other) == serviceAccount {
			return true, nil
		}
	}
	return false, nil
}

func (r *InferenceServiceReconciler) deleteAuthDelegatorBinding(ctx context.Context, binding *rbacv1.ClusterRoleBinding) error {
	log.FromContext(ctx).Info("Deleting auth-delegator ClusterRoleBinding", "name", binding.Name)
	if err := r.Delete(ctx, binding); client.IgnoreNotFound(err) != nil {
		return fmt.Errorf("failed to delete ClusterRoleBinding %s: %w", binding.Name, err)
	}
	return nil
}

// authDelegatorRequested matches the annotation value exactly, as odh-model-controller did for
// the binding; the Route and the auth proxy accept other spellings of true.
func authDelegatorRequested(isvc *v1beta1.InferenceService) bool {
	return isvc.Annotations[constants.ODHKserveRawAuth] == "true"
}

func predictorServiceAccount(isvc *v1beta1.InferenceService) string {
	if isvc.Spec.Predictor.ServiceAccountName != "" {
		return isvc.Spec.Predictor.ServiceAccountName
	}
	return constants.DefaultServiceAccount
}

// grantsAuthDelegator reports whether binding binds system:auth-delegator to the service account.
func grantsAuthDelegator(binding *rbacv1.ClusterRoleBinding, namespace, serviceAccount string) bool {
	if binding.RoleRef != authDelegatorRoleRef {
		return false
	}
	for _, subject := range binding.Subjects {
		if subject.Kind == rbacv1.ServiceAccountKind && subject.Namespace == namespace && subject.Name == serviceAccount {
			return true
		}
	}
	return false
}

// authDelegatorBindingUsers maps an auth-delegator ClusterRoleBinding to the auth-enabled
// InferenceServices running as its service accounts, so a deleted or edited binding is restored.
func (r *InferenceServiceReconciler) authDelegatorBindingUsers(ctx context.Context, obj client.Object) []reconcile.Request {
	binding, ok := obj.(*rbacv1.ClusterRoleBinding)
	if !ok {
		return nil
	}
	var requests []reconcile.Request
	for _, subject := range binding.Subjects {
		if subject.Kind != rbacv1.ServiceAccountKind ||
			desiredAuthDelegatorBinding(subject.Namespace, subject.Name).Name != binding.Name {
			continue
		}
		isvcs := &v1beta1.InferenceServiceList{}
		if err := r.List(ctx, isvcs, client.InNamespace(subject.Namespace)); err != nil {
			log.FromContext(ctx).Error(err, "Failed to list InferenceServices for auth-delegator ClusterRoleBinding",
				"name", binding.Name, "namespace", subject.Namespace)
			continue
		}
		for i := range isvcs.Items {
			isvc := &isvcs.Items[i]
			if authDelegatorRequested(isvc) && predictorServiceAccount(isvc) == subject.Name {
				requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(isvc)})
			}
		}
	}
	return requests
}

// authDelegatorBindingChanged selects deletions of auth-delegator ClusterRoleBindings and edits of
// their subjects. roleRef is immutable.
func authDelegatorBindingChanged() predicate.Funcs {
	isAuthDelegator := func(obj client.Object) bool {
		binding, ok := obj.(*rbacv1.ClusterRoleBinding)
		return ok && binding.RoleRef == authDelegatorRoleRef && strings.HasSuffix(binding.Name, authDelegatorBindingSuffix)
	}
	return predicate.Funcs{
		CreateFunc: func(event.CreateEvent) bool { return false },
		DeleteFunc: func(e event.DeleteEvent) bool { return isAuthDelegator(e.Object) },
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldBinding, okOld := e.ObjectOld.(*rbacv1.ClusterRoleBinding)
			newBinding, okNew := e.ObjectNew.(*rbacv1.ClusterRoleBinding)
			return okOld && okNew && isAuthDelegator(newBinding) &&
				!equality.Semantic.DeepEqual(oldBinding.Subjects, newBinding.Subjects)
		},
		GenericFunc: func(event.GenericEvent) bool { return false },
	}
}

func desiredAuthDelegatorBinding(namespace, serviceAccount string) *rbacv1.ClusterRoleBinding {
	return &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: namespace + "-" + serviceAccount + authDelegatorBindingSuffix,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      rbacv1.ServiceAccountKind,
				Namespace: namespace,
				Name:      serviceAccount,
			},
		},
		RoleRef: authDelegatorRoleRef,
	}
}
