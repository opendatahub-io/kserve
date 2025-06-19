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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"knative.dev/pkg/kmeta"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
)

func (r *LLMInferenceServiceReconciler) reconcileDisaggregatedServing(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService) error {
	logger := log.FromContext(ctx).WithName("reconcileDisaggregatedServing")
	ctx = log.IntoContext(ctx, logger)

	logger.V(2).Info("Reconciling disaggregated serving workload")

	prefill := r.expectedPrefillMainDeployment(ctx, llmSvc)
	if llmSvc.Spec.Prefill == nil {
		if err := r.deleteDeployment(ctx, llmSvc, prefill); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to delete prefill deployment: %w", err)
		}
		return nil
	}
	return r.reconcileDeployment(ctx, llmSvc, prefill)
}

func (r *LLMInferenceServiceReconciler) expectedPrefillMainDeployment(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService) *appsv1.Deployment {
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      kmeta.ChildName(llmSvc.GetName(), "-kserve-prefill"),
			Namespace: llmSvc.GetNamespace(),
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(llmSvc, v1alpha1.LLMInferenceServiceGVK),
			},
			Labels: map[string]string{
				"app.kubernetes.io/component": "llminferenceservice-workload-prefill",
				"app.kubernetes.io/name":      llmSvc.GetName(),
				"app.kubernetes.io/part-of":   "llminferenceservice",
			},
		},
	}

	if llmSvc.Spec.Prefill != nil {
		d.Spec = appsv1.DeploymentSpec{
			Replicas: llmSvc.Spec.Prefill.Replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app.kubernetes.io/component": "llminferenceservice-workload-prefill",
					"app.kubernetes.io/name":      llmSvc.GetName(),
					"app.kubernetes.io/part-of":   "llminferenceservice",
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app.kubernetes.io/component": "llminferenceservice-workload-prefill",
						"app.kubernetes.io/name":      llmSvc.GetName(),
						"app.kubernetes.io/part-of":   "llminferenceservice",
					},
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
