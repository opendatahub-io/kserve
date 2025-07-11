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
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"knative.dev/pkg/kmeta"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
	"github.com/kserve/kserve/pkg/constants"
	"github.com/kserve/kserve/pkg/utils"
)

func (r *LLMInferenceServiceReconciler) reconcileSingleNodeWorkload(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService) error {
	logger := log.FromContext(ctx).WithName("single-node-workload")
	ctx = log.IntoContext(ctx, logger)

	if err := r.reconcileSingleNodeMainWorkload(ctx, llmSvc); err != nil {
		return fmt.Errorf("failed to reconcile main workload: %w", err)
	}

	if err := r.reconcileSingleNodePrefill(ctx, llmSvc); err != nil {
		return fmt.Errorf("failed to reconcile prefill workload: %w", err)
	}
	return nil
}

func (r *LLMInferenceServiceReconciler) reconcileSingleNodeMainWorkload(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService) error {
	expected, err := r.expectedSingleNodeMainDeployment(ctx, llmSvc)
	if err != nil {
		return fmt.Errorf("failed to get expected main deployment: %w", err)
	}
	if llmSvc.Spec.Worker != nil {
		return Delete(ctx, r, llmSvc, expected)
	}
	if err := Reconcile(ctx, r, llmSvc, &appsv1.Deployment{}, expected, semanticDeploymentIsEqual); err != nil {
		return err
	}
	return r.propagateDeploymentStatus(ctx, expected, llmSvc.MarkMainWorkloadReady, llmSvc.MarkMainWorkloadNotReady)
}

func (r *LLMInferenceServiceReconciler) expectedSingleNodeMainDeployment(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService) (*appsv1.Deployment, error) {
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
			},
		},
	}

	if llmSvc.Spec.Template != nil {
		d.Spec.Template.Spec = *llmSvc.Spec.Template.DeepCopy()
	}

	if err := r.attachModelArtifacts(llmSvc, &d.Spec.Template.Spec); err != nil {
		return nil, fmt.Errorf("failed to attach model artifacts to main deployment: %w", err)
	}

	log.FromContext(ctx).V(2).Info("Expected main deployment", "deployment", d)

	return d, nil
}

func (r *LLMInferenceServiceReconciler) reconcileSingleNodePrefill(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService) error {
	prefill := r.expectedPrefillMainDeployment(ctx, llmSvc)
	if llmSvc.Spec.Prefill == nil {
		if err := Delete(ctx, r, llmSvc, prefill); err != nil {
			return fmt.Errorf("failed to delete prefill main deployment: %w", err)
		}
		return nil
	}
	if err := Reconcile(ctx, r, llmSvc, &appsv1.Deployment{}, prefill, semanticDeploymentIsEqual); err != nil {
		return fmt.Errorf("failed to reconcile prefill deployment %s/%s: %w", prefill.GetNamespace(), prefill.GetName(), err)
	}
	return r.propagateDeploymentStatus(ctx, prefill, llmSvc.MarkPrefillWorkloadReady, llmSvc.MarkPrefillWorkloadNotReady)
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

func (r *LLMInferenceServiceReconciler) attachModelArtifacts(llmSvc *v1alpha1.LLMInferenceService, podSpec *corev1.PodSpec) error {
	modelUri := llmSvc.Spec.Model.URI.String()

	// For PVC source URIs we need to mount the source to be able to access it
	// See design and discussion here: https://github.com/kserve/kserve/issues/148
	if strings.HasPrefix(modelUri, constants.PvcURIPrefix) {
		return r.attachPVCModelArtifact(modelUri, podSpec)
	} else {
		// Backwards compatibility
		// TODO: Evaluate if this is needed, because it essentially ignores the model URI.
		for idx := range podSpec.Containers {
			if podSpec.Containers[idx].Name == "main" {
				podSpec.Containers[idx].Args = append(podSpec.Containers[idx].Args, *llmSvc.Spec.Model.Name)
			}
		}
	}

	return nil
}

// attachPVCModelArtifact mounts a model artifact from a PersistentVolumeClaim (PVC) to the specified PodSpec.
// It adds the PVC as a volume and mounts it to the `main` container. The mount path is added to the arguments of the
// `main` container, assuming the model server expects a positional argument indicating the location of the model (which is the case of vLLM)
//
// Parameters:
//   - modelUri: The URI of the model, expected to have a PVC prefix.
//   - podSpec: The PodSpec to which the PVC volume and mount should be attached.
//
// Returns:
//
//	An error if attaching the PVC model artifact fails, otherwise nil.
//
// TODO: For now, this supports only direct mount. Copying from PVC would come later (if it makes sense at all).
func (r *LLMInferenceServiceReconciler) attachPVCModelArtifact(modelUri string, podSpec *corev1.PodSpec) error {
	if err := utils.AddModelPvcMount(modelUri, "main", true, podSpec); err != nil {
		return err
	}
	for idx := range podSpec.Containers {
		if podSpec.Containers[idx].Name == "main" {
			podSpec.Containers[idx].Args = append(podSpec.Containers[idx].Args, constants.DefaultModelLocalMountPath)
		}
	}

	return nil
}

func (r *LLMInferenceServiceReconciler) propagateDeploymentStatus(ctx context.Context, expected *appsv1.Deployment, ready func(), notReady func(reason, messageFormat string, messageA ...interface{})) error {
	logger := log.FromContext(ctx)

	curr := &appsv1.Deployment{}
	err := retry.OnError(retry.DefaultRetry, apierrors.IsNotFound, func() error {
		return r.Client.Get(ctx, client.ObjectKeyFromObject(expected), curr)
	})
	if err != nil {
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
	logger.Info("Deployment processed")
	notReady(string(appsv1.DeploymentProgressing), "")
	return nil
}

func semanticDeploymentIsEqual(expected *appsv1.Deployment, curr *appsv1.Deployment) bool {
	return equality.Semantic.DeepDerivative(expected.Spec, curr.Spec) &&
		equality.Semantic.DeepDerivative(expected.Labels, curr.Labels) &&
		equality.Semantic.DeepDerivative(expected.Annotations, curr.Annotations)
}
