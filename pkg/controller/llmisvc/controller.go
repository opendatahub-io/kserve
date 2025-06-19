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
	"fmt"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"knative.dev/pkg/reconciler"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	igwapi "sigs.k8s.io/gateway-api-inference-extension/api/v1alpha2"
	gatewayapi "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/kserve/kserve/pkg/utils"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
)

var childResourcesPredicate, _ = predicate.LabelSelectorPredicate(metav1.LabelSelector{
	MatchLabels: map[string]string{
		"app.kubernetes.io/part-of": "llminferenceservice",
	},
})

type ReconcilerConfig struct {
	SystemNamespace string `json:"systemNamespace,omitempty"`
}

// LLMInferenceServiceReconciler reconciles a LLMInferenceService object.
// It orchestrates the reconciliation of child resources based on the spec.
type LLMInferenceServiceReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder

	Config ReconcilerConfig
}

//+kubebuilder:rbac:groups=llm.services.kserve.io,resources=llminferenceservices,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=llm.services.kserve.io,resources=llminferenceservices/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=llm.services.kserve.io,resources=llminferenceservices/finalizers,verbs=update
//+kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch
//+kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=httproutes;gateways,verbs=get;list;watch;create;update;patch;delete

// Reconcile is the main entry point for the reconciliation loop.
// It fetches the LLMInferenceService and delegates the reconciliation of its constituent parts.
func (r *LLMInferenceServiceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithName("LLMInferenceService")
	logger.Info("Starting reconciliation")

	// 1. Fetch the LLMInferenceService instance
	original := &v1alpha1.LLMInferenceService{}
	if err := r.Get(ctx, req.NamespacedName, original); err != nil {
		if errors.IsNotFound(err) {
			// Object not found, return. Created objects are automatically garbage collected.
			// For additional cleanup logic use finalizers.
			logger.V(2).Info("LLMInferenceService resource not found. Ignoring since object must be deleted.")
			return ctrl.Result{}, nil
		}
		// Error reading the object - requeue the request.
		logger.Error(err, "Failed to get LLMInferenceService")
		return ctrl.Result{
			Requeue: true,
		}, err
	}
	resource := original.DeepCopy()

	reconciler.PreProcessReconcile(ctx, resource)
	reconcileErr := r.ReconcileKind(ctx, resource)
	reconciler.PostProcessReconcile(ctx, resource, original)

	if err := r.updateStatus(ctx, original, resource); err != nil {
		return ctrl.Result{Requeue: true}, err
	}

	if reconcileErr != nil {
		logger.Error(reconcileErr, "Failed to reconcile LLMInferenceService")
		r.Recorder.Eventf(original, corev1.EventTypeWarning, "Error", reconcileErr.Error())
		return ctrl.Result{Requeue: true}, reconcileErr
	}

	logger.Info("Reconciliation completed successfully")
	return ctrl.Result{}, nil
}

func (r *LLMInferenceServiceReconciler) updateStatus(ctx context.Context, existing *v1alpha1.LLMInferenceService, desired *v1alpha1.LLMInferenceService) error {
	// If there's nothing to update, just return.
	if equality.Semantic.DeepEqual(existing.Status, desired.Status) {
		return nil
	}
	if err := r.Client.Status().Update(ctx, desired); err != nil {
		return fmt.Errorf("failed to update status for LLMInferenceService: %w", err)
	}
	return nil
}

func (r *LLMInferenceServiceReconciler) ReconcileKind(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService) error {
	logger := log.FromContext(ctx).WithName("ReconcileKind")
	ctx = log.IntoContext(ctx, logger)

	baseCfg, err := r.combineBaseRefsConfig(ctx, llmSvc)
	if err != nil {
		return fmt.Errorf("failed to combine base-configurations: %w", err)
	}
	llmSvc = llmSvc.DeepCopy()
	llmSvc.Spec = baseCfg.Spec

	logger.Info("Reconciling with combined base configurations", "spec", llmSvc.Spec)

	if err := r.reconcileWorkload(ctx, llmSvc); err != nil {
		return fmt.Errorf("failed to reconcile workload: %w", err)
	}

	// if err := r.reconcileRouter(ctx, llmSvc); err != nil {
	//	return fmt.Errorf("failed to reconcile networking: %w", err)
	//}

	return nil
}

// SetupWithManager sets up the controller with the Manager.
// It configures watches for the LLMInferenceService and its owned resources.
func (r *LLMInferenceServiceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	logger := mgr.GetLogger().WithName("LLMInferenceService.SetupWithManager")

	b := ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.LLMInferenceService{}).
		Watches(&v1alpha1.LLMInferenceServiceConfig{}, r.enqueueOnLLMInferenceServiceConfigChange(logger)).
		// FIXME(envtest): complains about missing Kind
		// Owns(&netv1.Ingress{}, builder.WithPredicates(childResourcesPredicate)).
		Owns(&appsv1.Deployment{}, builder.WithPredicates(childResourcesPredicate)).
		Owns(&corev1.Service{}, builder.WithPredicates(childResourcesPredicate))

	if ok, err := utils.IsCrdAvailable(mgr.GetConfig(), gatewayapi.GroupVersion.String(), "HTTPRoute"); ok && err == nil {
		b = b.Owns(&gatewayapi.HTTPRoute{}, builder.WithPredicates(childResourcesPredicate))
	}
	if ok, err := utils.IsCrdAvailable(mgr.GetConfig(), igwapi.GroupVersion.String(), "InferencePool"); ok && err == nil {
		b = b.Owns(&igwapi.InferencePool{}, builder.WithPredicates(childResourcesPredicate))
	}
	if ok, err := utils.IsCrdAvailable(mgr.GetConfig(), igwapi.GroupVersion.String(), "InferenceModel"); ok && err == nil {
		b = b.Owns(&igwapi.InferenceModel{}, builder.WithPredicates(childResourcesPredicate))
	}

	return b.Complete(r)
}

func (r *LLMInferenceServiceReconciler) enqueueOnLLMInferenceServiceConfigChange(logger logr.Logger) handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, object client.Object) []reconcile.Request {
		sub := object.(*v1alpha1.LLMInferenceServiceConfig)
		reqs := make([]reconcile.Request, 0, 2)

		listNamespace := sub.GetNamespace()

		// LLMInferenceServiceConfig in the system namespace can be used by any LLMInferenceService.
		if sub.Namespace == r.Config.SystemNamespace {
			listNamespace = corev1.NamespaceAll
		}

		continueToken := ""
		for {
			llmSvcList := &v1alpha1.LLMInferenceServiceList{}
			if err := r.Client.List(ctx, llmSvcList, &client.ListOptions{Namespace: listNamespace, Continue: continueToken}); err != nil {
				logger.Error(err, "Failed to list LLMInferenceService")
				return reqs
			}
			for _, llmSvc := range llmSvcList.Items {
				for _, ref := range llmSvc.Spec.BaseRefs {
					if ref.Name == sub.Name || wellKnownDefaultConfigs.Has(ref.Name) {
						reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{
							Namespace: llmSvc.Namespace,
							Name:      llmSvc.Name,
						}})
					}
				}
			}

			if llmSvcList.Continue == "" {
				break
			}
			continueToken = llmSvcList.Continue
		}

		return reqs
	})
}
