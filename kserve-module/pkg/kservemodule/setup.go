package kservemodule

import (
	"context"
	"fmt"
	"strings"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	platformv1alpha1 "github.com/opendatahub-io/kserve-module/pkg/apis/v1alpha1"
)

var dependencyCRDSuffixes = []string{
	".networking.istio.io",
	".security.istio.io",
	".telemetry.istio.io",
	".extensions.istio.io",
	".cert-manager.io",
	".leaderworkerset.x-k8s.io",
}

var dependencyCRDNames = map[string]bool{
	"leaderworkersets.operator.openshift.io": true,
	"subscriptions.operators.coreos.com":     true,
}

func (r *KserveModuleReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Deployer == nil {
		return fmt.Errorf("deployer must not be nil")
	}

	b := ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.Kserve{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&corev1.Secret{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.ServiceAccount{}).
		Owns(&appsv1.Deployment{}).
		Owns(&networkingv1.NetworkPolicy{}).
		Owns(&rbacv1.Role{}).
		Owns(&rbacv1.RoleBinding{}).
		Owns(&rbacv1.ClusterRole{}).
		Owns(&rbacv1.ClusterRoleBinding{}).
		Owns(&admissionregistrationv1.MutatingWebhookConfiguration{}).
		Owns(&admissionregistrationv1.ValidatingWebhookConfiguration{}).
		WatchesMetadata(
			&apiextensionsv1.CustomResourceDefinition{},
			handler.EnqueueRequestsFromMapFunc(mapToKserve),
			builder.WithPredicates(crdNamePredicate()),
		)

	return b.Named("kserve-module").
		WithOptions(controller.Options{MaxConcurrentReconciles: 1}).
		Complete(r)
}

func mapToKserve(_ context.Context, _ client.Object) []ctrl.Request {
	return []ctrl.Request{{
		NamespacedName: client.ObjectKey{Name: platformv1alpha1.KserveInstanceName},
	}}
}

func crdNamePredicate() predicate.Predicate {
	return predicate.NewPredicateFuncs(func(obj client.Object) bool {
		name := obj.GetName()
		if dependencyCRDNames[name] {
			return true
		}
		for _, suffix := range dependencyCRDSuffixes {
			if strings.HasSuffix(name, suffix) {
				return true
			}
		}
		return false
	})
}
