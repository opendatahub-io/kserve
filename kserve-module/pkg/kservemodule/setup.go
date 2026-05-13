package kservemodule

import (
	ctrl "sigs.k8s.io/controller-runtime"

	platformv1alpha1 "github.com/opendatahub-io/kserve-module/pkg/apis/v1alpha1"
)

func (r *KserveModuleReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.Kserve{}).
		Named("kserve-module").
		Complete(r)
}
