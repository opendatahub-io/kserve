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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"knative.dev/pkg/kmeta"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
)

func (r *LLMInferenceServiceReconciler) reconcileDisaggregatedServing(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService) error {
	logger := log.FromContext(ctx).WithName("reconcileDisaggregatedServing")
	ctx = log.IntoContext(ctx, logger)

	logger.V(2).Info("Reconciling disaggregated serving workload")

	if err := r.reconcilePrefillMain(ctx, llmSvc); err != nil {
		llmSvc.MarkPrefillWorkloadNotReady("ReconcilePrefillMainWorkloadError", err.Error())
		return fmt.Errorf("failed to reconcile prefill main workload: %w", err)
	}
	if err := r.reconcilePrefillWorker(ctx, llmSvc); err != nil {
		llmSvc.MarkPrefillWorkloadNotReady("ReconcilePrefillWorkerWorkloadError", err.Error())
		return fmt.Errorf("failed to reconcile prefill worker workload: %w", err)
	}

	return nil
}

func (r *LLMInferenceServiceReconciler) reconcilePrefillMain(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService) error {
	prefill := r.expectedPrefillMainDeployment(ctx, llmSvc)
	if llmSvc.Spec.Prefill == nil {
		if err := r.deleteObject(ctx, llmSvc, prefill); err != nil {
			return fmt.Errorf("failed to delete prefill main deployment: %w", err)
		}
		return nil
	}
	if err := r.reconcileDeployment(ctx, llmSvc, prefill); err != nil {
		return fmt.Errorf("failed to reconcile prefill deployment %s/%s: %w", prefill.GetNamespace(), prefill.GetName(), err)
	}
	return r.propagateDeploymentStatus(ctx, prefill, llmSvc.MarkPrefillWorkloadReady, llmSvc.MarkPrefillWorkloadNotReady)
}

func (r *LLMInferenceServiceReconciler) reconcilePrefillWorker(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService) error {
	expected := r.expectedPrefillWorkerDeployment(ctx, llmSvc)
	if llmSvc.Spec.Prefill == nil || llmSvc.Spec.Prefill.Worker == nil {
		if err := r.deleteObject(ctx, llmSvc, expected); err != nil {
			return fmt.Errorf("failed to delete prefill worker: %w", err)
		}
		return nil
	}
	if err := r.reconcileDeployment(ctx, llmSvc, expected); err != nil {
		return err
	}
	return r.propagateDeploymentStatus(ctx, expected, llmSvc.MarkPrefillWorkerWorkloadReady, llmSvc.MarkPrefillWorkerWorkloadNotReady)
}

func (r *LLMInferenceServiceReconciler) expectedPrefillWorkerDeployment(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService) *appsv1.Deployment {
	labels := map[string]string{
		"app.kubernetes.io/component": "llminferenceservice-workload-prefill-worker",
		"app.kubernetes.io/name":      llmSvc.GetName(),
		"app.kubernetes.io/part-of":   "llminferenceservice",
	}

	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      kmeta.ChildName(llmSvc.GetName(), "-kserve-prefill-worker"),
			Namespace: llmSvc.GetNamespace(),
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(llmSvc, v1alpha1.LLMInferenceServiceGVK),
			},
			Labels: labels,
		},
	}

	if llmSvc.Spec.Prefill != nil {
		d.Spec = appsv1.DeploymentSpec{
			Replicas: llmSvc.Spec.Prefill.Replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
			},
		}
	}

	if llmSvc.Spec.Prefill != nil && llmSvc.Spec.Prefill.Worker != nil {
		d.Spec.Template.Spec = *llmSvc.Spec.Prefill.Worker.DeepCopy()
	}

	log.FromContext(ctx).V(2).Info("Expected prefill worker deployment", "deployment", d)

	return d
}

func (r *LLMInferenceServiceReconciler) expectedPrefillMainDeployment(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService) *appsv1.Deployment {
	labels := map[string]string{
		"app.kubernetes.io/component": "llminferenceservice-workload-prefill",
		"app.kubernetes.io/name":      llmSvc.GetName(),
		"app.kubernetes.io/part-of":   "llminferenceservice",
	}

	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      kmeta.ChildName(llmSvc.GetName(), "-kserve-prefill"),
			Namespace: llmSvc.GetNamespace(),
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(llmSvc, v1alpha1.LLMInferenceServiceGVK),
			},
			Labels: labels,
		},
	}

	if llmSvc.Spec.Prefill != nil {
		d.Spec = appsv1.DeploymentSpec{
			Replicas: llmSvc.Spec.Prefill.Replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
			},
		}
	}

	if llmSvc.Spec.Prefill != nil && llmSvc.Spec.Prefill.Template != nil {
		d.Spec.Template.Spec = *llmSvc.Spec.Prefill.Template.DeepCopy()
	}

	log.FromContext(ctx).V(2).Info("Expected prefill deployment", "deployment", d)

	return d
}
