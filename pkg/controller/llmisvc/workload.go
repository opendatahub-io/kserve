/*
Copyright 2025 The KServe Authors.

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
	"errors"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"knative.dev/pkg/kmeta"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
)

// reconcileWorkload manages the Deployments and Services for the LLM.
// It handles standard, multi-node, and disaggregated (prefill/decode) deployment patterns.
func (r *LLMInferenceServiceReconciler) reconcileWorkload(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService) error {
	logger := log.FromContext(ctx).WithName("reconcileWorkload")
	ctx = log.IntoContext(ctx, logger)

	defer llmSvc.DetermineWorkloadReadiness()

	// We need to always reconcile every type of workload to handle transitions from P/D to another topology (meaning
	// finalizing superfluous workloads).

	// TODO Properly handle Replicas > 1 (scaling) for multi node (Worker) in both main and prefill WorkloadSpec.

	if err := r.reconcileMainWorkload(ctx, llmSvc); err != nil {
		llmSvc.MarkWorkloadNotReady("ReconcileMainWorkloadError", err.Error())
		return fmt.Errorf("failed to reconcile main workload: %w", err)
	}

	if err := r.reconcileMainWorker(ctx, llmSvc); err != nil {
		llmSvc.MarkWorkloadNotReady("ReconcileMainWorkerError", err.Error())
		return fmt.Errorf("failed to reconcile main worker: %w", err)
	}

	if err := r.reconcileDisaggregatedServing(ctx, llmSvc); err != nil {
		llmSvc.MarkWorkloadNotReady("ReconcileDisaggregatedServingError", err.Error())
		return fmt.Errorf("failed to reconcile disaggregated serving workload: %w", err)
	}

	return nil
}

func (r *LLMInferenceServiceReconciler) reconcileMainWorkload(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService) error {
	expected, err := r.expectedMainDeployment(ctx, llmSvc)
	if err != nil {
		return fmt.Errorf("failed to get expected main deployment: %w", err)
	}
	if err := r.reconcileDeployment(ctx, llmSvc, expected); err != nil {
		return err
	}
	return r.propagateDeploymentStatus(ctx, expected, llmSvc.MarkMainWorkloadReady, llmSvc.MarkMainWorkloadNotReady)
}

func (r *LLMInferenceServiceReconciler) reconcileMainWorker(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService) error {
	expected := r.expectedMainWorker(ctx, llmSvc)
	if llmSvc.Spec.Worker == nil {
		if err := r.deleteDeployment(ctx, llmSvc, expected); err != nil {
			return fmt.Errorf("failed to delete worker: %w", err)
		}
		return nil
	}
	if err := r.reconcileDeployment(ctx, llmSvc, expected); err != nil {
		return err
	}
	return r.propagateDeploymentStatus(ctx, expected, llmSvc.MarkWorkerWorkloadReady, llmSvc.MarkWorkerWorkloadNotReady)
}

func (r *LLMInferenceServiceReconciler) expectedMainWorker(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService) *appsv1.Deployment {
	labels := map[string]string{
		"app.kubernetes.io/component": "llminferenceservice-workload-worker",
		"app.kubernetes.io/name":      llmSvc.GetName(),
		"app.kubernetes.io/part-of":   "llminferenceservice",
	}

	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      kmeta.ChildName(llmSvc.GetName(), "-kserve-worker"),
			Namespace: llmSvc.GetNamespace(),
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(llmSvc, v1alpha1.LLMInferenceServiceGVK),
			},
			Labels: labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: llmSvc.Spec.Replicas,
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

	if llmSvc.Spec.Worker != nil {
		d.Spec.Template.Spec = *llmSvc.Spec.Worker.DeepCopy()
	}

	log.FromContext(ctx).V(2).Info("Expected worker deployment", "deployment", d)

	return d
}

func (r *LLMInferenceServiceReconciler) expectedMainDeployment(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService) (*appsv1.Deployment, error) {
	if llmSvc.Spec.Template == nil {
		return nil, errors.New("llmSvc.Spec.Template must not be nil")
	}

	labels := map[string]string{
		"app.kubernetes.io/component": "llminferenceservice-workload",
		"app.kubernetes.io/name":      llmSvc.GetName(),
		"app.kubernetes.io/part-of":   "llminferenceservice",
	}

	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      kmeta.ChildName(llmSvc.GetName(), "-kserve"),
			Namespace: llmSvc.GetNamespace(),
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(llmSvc, v1alpha1.LLMInferenceServiceGVK),
			},
			Labels: labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: llmSvc.Spec.Replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: *llmSvc.Spec.Template.DeepCopy(),
			},
		},
	}

	log.FromContext(ctx).V(2).Info("Expected main deployment", "deployment", d)

	return d, nil
}

func (r *LLMInferenceServiceReconciler) reconcileDeployment(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService, expected *appsv1.Deployment) error {
	curr := &appsv1.Deployment{}

	err := r.Client.Get(ctx, client.ObjectKeyFromObject(expected), curr)
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to get deployment %s/%s: %w", expected.GetNamespace(), expected.GetName(), err)
	}
	if apierrors.IsNotFound(err) {
		return r.createDeployment(ctx, llmSvc, expected)
	}

	if equality.Semantic.DeepDerivative(expected.Spec, curr.Spec) &&
		equality.Semantic.DeepEqual(expected.Labels, curr.Labels) &&
		equality.Semantic.DeepEqual(expected.Annotations, expected.Annotations) &&
		metav1.IsControlledBy(curr, llmSvc) {
		return nil
	}
	return r.updateDeployment(ctx, llmSvc, curr, expected)
}

func (r *LLMInferenceServiceReconciler) createDeployment(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService, expected *appsv1.Deployment) error {
	if err := r.Client.Create(ctx, expected); err != nil {
		return fmt.Errorf("failed to create deployment %s/%s: %w", expected.GetNamespace(), expected.GetName(), err)
	}
	r.Recorder.Eventf(llmSvc, corev1.EventTypeNormal, "Created", "Created deployment %s/%s", expected.GetNamespace(), expected.GetName())

	return nil
}

func (r *LLMInferenceServiceReconciler) updateDeployment(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService, curr, expected *appsv1.Deployment) error {
	if !metav1.IsControlledBy(curr, llmSvc) {
		return fmt.Errorf("failed to update deployment %s/%s: it is not controlled by LLMInferenceService %s/%s",
			expected.GetNamespace(), expected.GetName(),
			llmSvc.GetNamespace(), llmSvc.GetName(),
		)
	}
	expected.ResourceVersion = curr.ResourceVersion

	if err := r.Client.Update(ctx, expected); err != nil {
		return fmt.Errorf("failed to update deployment %s/%s: %w", expected.GetNamespace(), expected.GetName(), err)
	}
	r.Recorder.Eventf(llmSvc, corev1.EventTypeNormal, "Updated", "Updated deployment %s/%s", expected.GetNamespace(), expected.GetName())

	return nil
}

func (r *LLMInferenceServiceReconciler) deleteDeployment(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService, expected *appsv1.Deployment) error {
	if err := r.Client.Delete(ctx, expected); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to delete deployment %s/%s: %w", expected.GetNamespace(), expected.GetName(), err)
		}
		return nil
	}
	r.Recorder.Eventf(llmSvc, corev1.EventTypeNormal, "Deleted", "Deleted deployment %s/%s", expected.GetNamespace(), expected.GetName())
	return nil
}

func (r *LLMInferenceServiceReconciler) propagateDeploymentStatus(ctx context.Context, expected *appsv1.Deployment, ready func(), notReady func(reason, messageFormat string, messageA ...interface{})) error {
	curr := &appsv1.Deployment{}
	if err := r.Client.Get(ctx, client.ObjectKeyFromObject(expected), curr); err != nil {
		return fmt.Errorf("failed to get current deployment %s/%s: %w", expected.GetNamespace(), expected.GetName(), err)
	}

	for _, cond := range curr.Status.Conditions {
		if cond.Type == appsv1.DeploymentAvailable {
			if cond.Status == corev1.ConditionTrue {
				ready()
			} else {
				notReady(cond.Reason, cond.Message)
			}
			return nil
		}
	}
	notReady(string(appsv1.DeploymentProgressing), "")
	return nil
}
