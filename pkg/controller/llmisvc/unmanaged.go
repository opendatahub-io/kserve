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

package llmisvc

import (
	"context"

	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"knative.dev/pkg/apis"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
	"github.com/kserve/kserve/pkg/utils"
)

const UnmanagedMessage = "llm-d Technology Preview has been removed. Existing resources are unmanaged; " +
	"spec changes are rejected, deleted children are not recreated, and readiness is no longer monitored."

// UnmanagedReconciler does not provision, update, or repair serving resources.
// It only retires stale status and permits deletion through the legacy finalizer.
// Kubernetes continues to manage existing Deployments and their pods.
type UnmanagedReconciler struct {
	*LLMInferenceServiceReconciler
}

func (r *UnmanagedReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	service := &v1alpha1.LLMInferenceService{}
	if err := r.Get(ctx, req.NamespacedName, service); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !service.DeletionTimestamp.IsZero() {
		// The legacy deletion branch only finalizes resources and removes its
		// finalizer; it does not resolve presets or reconcile serving workloads.
		return r.LLMInferenceServiceReconciler.Reconcile(ctx, req)
	}

	before := service.DeepCopy()
	conditions := service.GetConditionSet().Manage(service.GetStatus())
	for _, condition := range before.Status.Conditions {
		conditions.MarkUnknown(condition.Type, "Unmanaged", UnmanagedMessage)
	}
	conditions.MarkUnknown(apis.ConditionReady, "Unmanaged", UnmanagedMessage)
	// Do not advance observedGeneration: the desired spec has not been applied.
	if equality.Semantic.DeepEqual(before.Status, service.Status) {
		return ctrl.Result{}, nil
	}
	return ctrl.Result{}, r.Status().Patch(ctx, service, client.MergeFrom(before))
}

func (r *UnmanagedReconciler) SetupWithManager(mgr ctrl.Manager) error {
	available, err := utils.IsCrdAvailable(mgr.GetConfig(), v1alpha1.SchemeGroupVersion.String(), "LLMInferenceService")
	if err != nil || !available {
		return err
	}
	// Register monitoring types for legacy finalization even if their CRDs are
	// absent. The legacy deletion helper tolerates missing APIs.
	if err := monitoringv1.AddToScheme(mgr.GetScheme()); err != nil {
		return err
	}
	// Deliberately do not watch configs, workloads, GIE, Istio, or Gateway objects.
	return ctrl.NewControllerManagedBy(mgr).
		Named("unmanaged-llminferenceservice").
		For(&v1alpha1.LLMInferenceService{}).
		Complete(r)
}
