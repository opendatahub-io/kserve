package kservemodule

import (
	"context"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/opendatahub-io/kserve-module/pkg/apis/v1alpha1"
)

// +kubebuilder:rbac:groups=components.platform.opendatahub.io,resources=kserves,verbs=list;watch
// +kubebuilder:rbac:groups=components.platform.opendatahub.io,resources=kserves,resourceNames=default-kserve,verbs=get;update;patch
// +kubebuilder:rbac:groups=components.platform.opendatahub.io,resources=kserves/status,resourceNames=default-kserve,verbs=get;update;patch

type KserveModuleReconciler struct {
	client.Client
	Scheme        *runtime.Scheme
	ManifestsPath string
}

func (r *KserveModuleReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	kserve := &platformv1alpha1.Kserve{}
	if err := r.Get(ctx, req.NamespacedName, kserve); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	log.Info("reconciling Kserve CR", "name", kserve.Name)
	return ctrl.Result{}, nil
}
