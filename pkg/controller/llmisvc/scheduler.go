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

package llmisvc

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"knative.dev/pkg/kmeta"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	igwapi "sigs.k8s.io/gateway-api-inference-extension/api/v1alpha2"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
)

func (r *LLMInferenceServiceReconciler) reconcileScheduler(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService) error {
	serviceAccount, err := r.reconcileSchedulerServiceAccount(ctx, llmSvc)
	if err != nil {
		return err
	}
	if err := r.reconcileSchedulerRole(ctx, llmSvc); err != nil {
		return err
	}
	if err := r.reconcileSchedulerRoleBinding(ctx, llmSvc, serviceAccount); err != nil {
		return err
	}
	if err := r.reconcileSchedulerDeployment(ctx, llmSvc); err != nil {
		return err
	}
	if err := r.reconcileSchedulerInferencePool(ctx, llmSvc); err != nil {
		return err
	}
	return nil
}

func (r *LLMInferenceServiceReconciler) reconcileSchedulerRole(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService) error {
	expected := r.expectedSchedulerRole(llmSvc)
	if llmSvc.Spec.Router == nil || llmSvc.Spec.Router.Scheduler == nil {
		return r.deleteObject(ctx, llmSvc, expected)
	}
	curr := &rbacv1.Role{}
	err := r.Client.Get(ctx, client.ObjectKeyFromObject(expected), curr)
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to get role %s/%s: %w", expected.GetNamespace(), expected.GetName(), err)
	}
	if apierrors.IsNotFound(err) {
		return r.createObject(ctx, llmSvc, expected)
	}
	return r.updateObject(ctx, llmSvc, curr, expected, semanticServiceAccountIsEqual)
}

func (r *LLMInferenceServiceReconciler) reconcileSchedulerRoleBinding(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService, sa *corev1.ServiceAccount) error {
	expected := r.expectedSchedulerRoleBinding(llmSvc, sa)
	if llmSvc.Spec.Router == nil || llmSvc.Spec.Router.Scheduler == nil {
		return r.deleteObject(ctx, llmSvc, expected)
	}
	curr := &rbacv1.RoleBinding{}
	err := r.Client.Get(ctx, client.ObjectKeyFromObject(expected), curr)
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to get role %s/%s: %w", expected.GetNamespace(), expected.GetName(), err)
	}
	if apierrors.IsNotFound(err) {
		return r.createObject(ctx, llmSvc, expected)
	}
	return r.updateObject(ctx, llmSvc, curr, expected, semanticServiceAccountIsEqual)
}

func (r *LLMInferenceServiceReconciler) reconcileSchedulerServiceAccount(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService) (*corev1.ServiceAccount, error) {
	expected := r.expectedSchedulerServiceAccount(llmSvc)
	if llmSvc.Spec.Router == nil || llmSvc.Spec.Router.Scheduler == nil {
		return expected, r.deleteObject(ctx, llmSvc, expected)
	}
	curr := &corev1.ServiceAccount{}
	err := r.Client.Get(ctx, client.ObjectKeyFromObject(expected), curr)
	if err != nil && !apierrors.IsNotFound(err) {
		return expected, fmt.Errorf("failed to get service account %s/%s: %w", expected.GetNamespace(), expected.GetName(), err)
	}
	if apierrors.IsNotFound(err) {
		return expected, r.createObject(ctx, llmSvc, expected)
	}
	return expected, r.updateObject(ctx, llmSvc, curr, expected, semanticServiceAccountIsEqual)
}

func (r *LLMInferenceServiceReconciler) reconcileSchedulerDeployment(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService) error {
	scheduler := r.expectedSchedulerDeployment(ctx, llmSvc)
	if llmSvc.Spec.Router == nil || llmSvc.Spec.Router.Scheduler == nil {
		return r.deleteObject(ctx, llmSvc, scheduler)
	}
	if err := r.reconcileDeployment(ctx, llmSvc, scheduler); err != nil {
		return fmt.Errorf("failed to reconcile scheduler deployment %s/%s: %w", scheduler.GetNamespace(), scheduler.GetName(), err)
	}
	return r.propagateDeploymentStatus(ctx, scheduler, llmSvc.MarkSchedulerWorkloadReady, llmSvc.MarkSchedulerWorkloadNotReady)
}

func (r *LLMInferenceServiceReconciler) reconcileSchedulerInferencePool(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService) error {
	expected := r.expectedSchedulerInferencePool(ctx, llmSvc)
	if llmSvc.Spec.Router == nil || llmSvc.Spec.Router.Scheduler == nil || llmSvc.Spec.Router.Scheduler.Pool.HasRef() {
		return r.deleteObject(ctx, llmSvc, expected)
	}

	curr := &igwapi.InferencePool{}
	err := r.Client.Get(ctx, client.ObjectKeyFromObject(expected), curr)
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to get InferencePool %s/%s: %w", expected.GetNamespace(), expected.GetName(), err)
	}
	if apierrors.IsNotFound(err) {
		return r.createObject(ctx, llmSvc, expected)
	}
	return r.updateObject(ctx, llmSvc, curr, expected, semanticInferencePoolIsEqual)
}

func (r *LLMInferenceServiceReconciler) expectedSchedulerInferencePool(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService) *igwapi.InferencePool {
	labels := map[string]string{
		"app.kubernetes.io/component": "llminferenceservice-router-scheduler",
		"app.kubernetes.io/name":      llmSvc.GetName(),
		"app.kubernetes.io/part-of":   "llminferenceservice",
	}

	ip := &igwapi.InferencePool{
		ObjectMeta: metav1.ObjectMeta{
			Name:      kmeta.ChildName(llmSvc.GetName(), "-inference-pool"),
			Namespace: llmSvc.GetNamespace(),
			Labels:    labels,
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(llmSvc, v1alpha1.LLMInferenceServiceGVK),
			},
		},
	}
	if llmSvc.Spec.Router != nil || llmSvc.Spec.Router.Scheduler != nil && llmSvc.Spec.Router.Scheduler.Pool != nil && llmSvc.Spec.Router.Scheduler.Pool.Spec != nil {
		ip.Spec = *llmSvc.Spec.Router.Scheduler.Pool.Spec.DeepCopy()
	}

	log.FromContext(ctx).V(2).Info("Expected router InferencePool", "inferencepool", ip)

	return ip
}

func (r *LLMInferenceServiceReconciler) expectedSchedulerDeployment(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService) *appsv1.Deployment {
	labels := r.schedulerLabels(llmSvc)
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      kmeta.ChildName(llmSvc.GetName(), "-kserve-router-scheduler"),
			Namespace: llmSvc.GetNamespace(),
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(llmSvc, v1alpha1.LLMInferenceServiceGVK),
			},
			Labels: labels,
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
			},
		},
	}

	if llmSvc.Spec.Router != nil && llmSvc.Spec.Router.Scheduler != nil && llmSvc.Spec.Router.Scheduler.Template != nil {
		d.Spec.Template.Spec = *llmSvc.Spec.Router.Scheduler.Template.DeepCopy()
	}

	log.FromContext(ctx).V(2).Info("Expected router scheduler deployment", "deployment", d)

	return d
}

func (r *LLMInferenceServiceReconciler) expectedSchedulerServiceAccount(llmSvc *v1alpha1.LLMInferenceService) *corev1.ServiceAccount {
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      kmeta.ChildName(llmSvc.GetName(), "-epp-sa"),
			Namespace: llmSvc.GetNamespace(),
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(llmSvc, v1alpha1.LLMInferenceServiceGVK),
			},
			Labels: r.schedulerLabels(llmSvc),
		},
	}

	if llmSvc.Spec.Router != nil &&
		llmSvc.Spec.Router.Scheduler != nil &&
		llmSvc.Spec.Router.Scheduler.Template != nil &&
		llmSvc.Spec.Router.Scheduler.Template.ServiceAccountName != "" {
		sa.Name = llmSvc.Spec.Router.Scheduler.Template.ServiceAccountName
	}

	return sa
}

func (r *LLMInferenceServiceReconciler) expectedSchedulerRole(llmSvc *v1alpha1.LLMInferenceService) *rbacv1.Role {
	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      kmeta.ChildName(llmSvc.GetName(), "-epp-role"),
			Namespace: llmSvc.GetNamespace(),
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(llmSvc, v1alpha1.LLMInferenceServiceGVK),
			},
			Labels: r.schedulerLabels(llmSvc),
		},
		Rules: []rbacv1.PolicyRule{
			{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"get", "list", "watch"}},
			{APIGroups: []string{"inference.networking.x-k8s.io"}, Resources: []string{"inferencepools", "inferencemodels"}, Verbs: []string{"get", "list", "watch"}},
			{APIGroups: []string{"discovery.k8s.io"}, Resources: []string{"endpointslices"}, Verbs: []string{"get", "list", "watch"}},
			{APIGroups: []string{"authentication.k8s.io"}, Resources: []string{"tokenreviews", "subjectaccessreviews"}, Verbs: []string{"create"}},
		},
	}
	return role
}

func (r *LLMInferenceServiceReconciler) expectedSchedulerRoleBinding(llmSvc *v1alpha1.LLMInferenceService, sa *corev1.ServiceAccount) *rbacv1.RoleBinding {
	rb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      kmeta.ChildName(llmSvc.GetName(), "-epp-rb"),
			Namespace: llmSvc.GetNamespace(),
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(llmSvc, v1alpha1.LLMInferenceServiceGVK),
			},
			Labels: r.schedulerLabels(llmSvc),
		},
		Subjects: []rbacv1.Subject{{
			Kind:      "ServiceAccount",
			Name:      sa.GetName(),
			Namespace: sa.GetNamespace(),
		}},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     kmeta.ChildName(llmSvc.GetName(), "-epp-role"),
		},
	}
	return rb
}

func semanticInferencePoolIsEqual(expected client.Object, curr client.Object) bool {
	e := expected.(*igwapi.InferencePool)
	c := curr.(*igwapi.InferencePool)
	return equality.Semantic.DeepDerivative(e.Spec, c.Spec) &&
		equality.Semantic.DeepDerivative(e.Labels, c.Labels) &&
		equality.Semantic.DeepDerivative(e.Annotations, c.Annotations)
}

func semanticServiceAccountIsEqual(expected client.Object, curr client.Object) bool {
	e := expected.(*corev1.ServiceAccount)
	c := curr.(*corev1.ServiceAccount)
	return equality.Semantic.DeepDerivative(e.Secrets, c.Secrets) &&
		equality.Semantic.DeepDerivative(e.ImagePullSecrets, c.ImagePullSecrets) &&
		equality.Semantic.DeepDerivative(e.Labels, c.Labels) &&
		equality.Semantic.DeepDerivative(e.Annotations, c.Annotations)
}

func (r *LLMInferenceServiceReconciler) schedulerLabels(llmSvc *v1alpha1.LLMInferenceService) map[string]string {
	return map[string]string{
		"app.kubernetes.io/component": "llminferenceservice-router-scheduler",
		"app.kubernetes.io/name":      llmSvc.GetName(),
		"app.kubernetes.io/part-of":   "llminferenceservice",
	}
}
