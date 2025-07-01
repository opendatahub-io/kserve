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
	"strings"

	"k8s.io/client-go/kubernetes"

	"github.com/kserve/kserve/pkg/apis/serving/v1beta1"
	"github.com/kserve/kserve/pkg/constants"

	"k8s.io/apimachinery/pkg/api/equality"

	"knative.dev/pkg/reconciler"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"

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

type Config struct {
	SystemNamespace         string `json:"systemNamespace,omitempty"`
	IngressGatewayName      string `json:"ingressGatewayName,omitempty"`
	IngressGatewayNamespace string `json:"ingressGatewayNamespace,omitempty"`
}

// NewConfig creates instance of llm-specific config based on predifined values
// in IngressConfig struct
func NewConfig(ingressConfig *v1beta1.IngressConfig) Config {
	igwNs := constants.KServeNamespace
	igwName := ingressConfig.KserveIngressGateway
	igw := strings.Split(igwName, "/")
	if len(igw) == 2 {
		igwNs = igw[0]
		igwName = igw[1]
	}

	return Config{
		SystemNamespace:         constants.KServeNamespace,
		IngressGatewayNamespace: igwNs,
		IngressGatewayName:      igwName,
	}
}

// LLMInferenceServiceReconciler reconciles a LLMInferenceService object.
// It orchestrates the reconciliation of child resources based on the spec.
type LLMInferenceServiceReconciler struct {
	client.Client
	record.EventRecorder
	Clientset kubernetes.Interface
	Config    Config
}

//+kubebuilder:rbac:groups=serving.kserve.io,resources=llminferenceservices,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=serving.kserve.io,resources=llminferenceservices/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=serving.kserve.io,resources=llminferenceservices/finalizers,verbs=update
//+kubebuilder:rbac:groups=serving.kserve.io,resources=llminferenceserviceconfigs,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=serving.kserve.io,resources=llminferenceserviceconfigs/finalizers,verbs=update
//+kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=httproutes;gateways,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=inference.networking.x-k8s.io,resources=inferencepools;inferencemodels;,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch
//+kubebuilder:rbac:groups=core,resources=serviceaccounts,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles;rolebindings,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=discovery.k8s.io,resources=endpointslices,verbs=get;list;watch
//+kubebuilder:rbac:groups=authentication.k8s.io,resources=tokenreviews;subjectaccessreviews,verbs=create

// Reconcile is the main entry point for the reconciliation loop.
// It fetches the LLMInferenceService and delegates the reconciliation of its constituent parts.
func (r *LLMInferenceServiceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithName("LLMInferenceService")
	ctx = log.IntoContext(ctx, logger)

	logger.Info("Starting reconciliation")
	original := &v1alpha1.LLMInferenceService{}
	if err := r.Get(ctx, req.NamespacedName, original); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if original.DeletionTimestamp != nil {
		// TODO(reconcile): Handle finalization logic if needed.
		logger.Info("Mark for deletion, skipping reconciliation")
		return ctrl.Result{}, nil
	}

	// TODO(ingress) next: add watch on CfgMap with predicate and cache tuning to trigger reconcile when it changes
	r.loadIngressConfig(ctx, logger, original)

	resource := original.DeepCopy()

	reconciler.PreProcessReconcile(ctx, resource)
	reconcileErr := r.reconcile(ctx, resource)
	reconciler.PostProcessReconcile(ctx, resource, original)

	if reconcileErr != nil {
		logger.Error(reconcileErr, "Failed to reconcile LLMInferenceService")
		r.Eventf(original, corev1.EventTypeWarning, "Error", "Reconciliation failed: %v", reconcileErr.Error())
	}

	if err := r.updateStatus(ctx, resource); err != nil {
		logger.Error(err, "Failed to update status for LLMInferenceService")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, reconcileErr
}

func (r *LLMInferenceServiceReconciler) loadIngressConfig(ctx context.Context, logger logr.Logger, original *v1alpha1.LLMInferenceService) {
	isvcConfigMap, errCfgMap := v1beta1.GetInferenceServiceConfigMap(ctx, r.Clientset) // Fetch directly from API Server (like everywhere else)
	if errCfgMap != nil {
		logger.Error(errCfgMap, fmt.Sprintf("Failed to load InferenceServiceConfigMap. Using existing values %+v", r.Config))
		r.Eventf(original, corev1.EventTypeWarning, "Error", "Reconciliation failed: %v", errCfgMap.Error())
	} else {
		ingressConfig, errConvert := v1beta1.NewIngressConfig(isvcConfigMap)
		if errConvert != nil {
			logger.Error(errConvert, fmt.Sprintf("Failed to convert InferenceServiceConfigMap to IngressConfig. Using existing values %+v", r.Config))
			r.Eventf(original, corev1.EventTypeWarning, "Error", "Reconciliation failed: %v", errConvert.Error())
		}
		// TODO Guard Config with a mutex or atomic
		r.Config = NewConfig(ingressConfig)
	}
}

func (r *LLMInferenceServiceReconciler) updateStatus(ctx context.Context, desired *v1alpha1.LLMInferenceService) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest := &v1alpha1.LLMInferenceService{}
		if err := r.Client.Get(ctx, client.ObjectKeyFromObject(desired), latest); err != nil {
			return err
		}

		if equality.Semantic.DeepEqual(latest.Status, desired.Status) {
			return nil
		}

		latest.Status = desired.Status

		if err := r.Client.Status().Update(ctx, latest); err != nil {
			return fmt.Errorf("failed to update status for LLMInferenceService: %w", err)
		}

		return nil
	})
}

func (r *LLMInferenceServiceReconciler) reconcile(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService) error {
	logger := log.FromContext(ctx).WithName("reconcile")
	ctx = log.IntoContext(ctx, logger)

	baseCfg, err := r.combineBaseRefsConfig(ctx, llmSvc, &r.Config)
	if err != nil {
		llmSvc.MarkPresetsCombinedNotReady("CombineBaseError", err.Error())
		return fmt.Errorf("failed to combine base-configurations: %w", err)
	}
	llmSvc.MarkPresetsCombinedReady()
	// We are only writing to status, so we can safely use the original object.
	llmSvc.Spec = baseCfg.Spec

	logger.Info("Reconciling with combined base configurations", "spec", llmSvc.Spec)

	if err := r.reconcileWorkload(ctx, llmSvc); err != nil {
		return fmt.Errorf("failed to reconcile workload: %w", err)
	}

	if err := r.reconcileRouter(ctx, llmSvc); err != nil {
		return fmt.Errorf("failed to reconcile networking: %w", err)
	}

	return nil
}

// SetupWithManager sets up the controller with the Manager.
// It configures watches for the LLMInferenceService and its owned resources.
func (r *LLMInferenceServiceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	logger := mgr.GetLogger().WithName("LLMInferenceService.SetupWithManager")

	b := ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.LLMInferenceService{}).
		Watches(&v1alpha1.LLMInferenceServiceConfig{}, r.enqueueOnLLMInferenceServiceConfigChange(logger)).
		Owns(&netv1.Ingress{}, builder.WithPredicates(childResourcesPredicate)).
		Owns(&appsv1.Deployment{}, builder.WithPredicates(childResourcesPredicate)).
		Owns(&corev1.Service{}, builder.WithPredicates(childResourcesPredicate))

	if ok, err := utils.IsCrdAvailable(mgr.GetConfig(), gatewayapi.GroupVersion.String(), "HTTPRoute"); ok && err == nil {
		if err := gatewayapi.Install(mgr.GetScheme()); err != nil {
			return fmt.Errorf("failed to add GIE APIs to scheme: %w", err)
		}
		b = b.Owns(&gatewayapi.HTTPRoute{}, builder.WithPredicates(childResourcesPredicate))
	}
	if ok, err := utils.IsCrdAvailable(mgr.GetConfig(), igwapi.GroupVersion.String(), "InferencePool"); ok && err == nil {
		if err := igwapi.Install(mgr.GetScheme()); err != nil {
			return fmt.Errorf("failed to add GIE APIs to scheme: %w", err)
		}
		b = b.Owns(&igwapi.InferencePool{}, builder.WithPredicates(childResourcesPredicate))
	}
	if ok, err := utils.IsCrdAvailable(mgr.GetConfig(), igwapi.GroupVersion.String(), "InferenceModel"); ok && err == nil {
		if err := igwapi.Install(mgr.GetScheme()); err != nil {
			return fmt.Errorf("failed to add GIE APIs to scheme: %w", err)
		}
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
