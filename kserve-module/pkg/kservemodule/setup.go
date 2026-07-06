package kservemodule

import (
	"context"
	"fmt"
	"strings"

	securityv1 "github.com/openshift/api/security/v1"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/opendatahub-io/odh-platform-utilities/pkg/cluster"

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
	"persesdashboards.perses.dev":            true,
}

var watchedSubscriptions = map[string]bool{
	rhclSubscription:        true,
	certManagerSubscription: true,
	lwsSubscription:         true,
	cmaSubscription:         true,
}

type dynamicWatch struct {
	groupKind  schema.GroupKind
	gvk        schema.GroupVersionKind
	filterFn   func(*unstructured.Unstructured) bool
	registered bool
}

func (r *KserveModuleReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Deployer == nil {
		return fmt.Errorf("deployer must not be nil")
	}

	r.cache = mgr.GetCache()

	b := ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.Kserve{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&corev1.Secret{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.ServiceAccount{}).
		Owns(&corev1.PersistentVolume{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		Owns(&appsv1.Deployment{}).
		Owns(&appsv1.DaemonSet{}).
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
		).
		// Watch Nodes so that newly added or relabeled nodes trigger
		// reconciliation of labelModelCacheNodes.
		Watches(&corev1.Node{}, handler.EnqueueRequestsFromMapFunc(mapToKserve),
			builder.WithPredicates(predicate.Or(
				predicate.GenerationChangedPredicate{},
				predicate.LabelChangedPredicate{},
			)),
		)

	// SecurityContextConstraints CRD is always present on OpenShift (OLM); never on XKS.
	sccGK := schema.GroupKind{Group: "security.openshift.io", Kind: "SecurityContextConstraints"}
	if err := cluster.CustomResourceDefinitionExists(context.Background(), mgr.GetAPIReader(), sccGK); err == nil {
		b.Owns(&securityv1.SecurityContextConstraints{})
	}

	// Subscription CRD is always present on OpenShift (OLM); never on XKS.
	// One-time conditional watch at startup — no dynamic retry needed.
	subGK := schema.GroupKind{Group: "operators.coreos.com", Kind: "Subscription"}
	if err := cluster.CustomResourceDefinitionExists(context.Background(), mgr.GetAPIReader(), subGK); err == nil {
		subObj := &unstructured.Unstructured{}
		subObj.SetGroupVersionKind(schema.GroupVersionKind{Group: "operators.coreos.com", Version: "v1alpha1", Kind: "Subscription"})
		b.Watches(subObj,
			handler.EnqueueRequestsFromMapFunc(mapToKserve),
			builder.WithPredicates(predicate.NewPredicateFuncs(func(o client.Object) bool {
				u, ok := o.(*unstructured.Unstructured)
				if !ok {
					return false
				}
				return watchedSubscriptions[u.GetName()]
			})),
		)
	}

	r.dynamicWatches = []*dynamicWatch{
		{
			groupKind: schema.GroupKind{Group: "operator.openshift.io", Kind: "LeaderWorkerSet"},
			gvk:       schema.GroupVersionKind{Group: "operator.openshift.io", Version: "v1", Kind: "LeaderWorkerSet"},
		},
		{
			groupKind: schema.GroupKind{Group: "serving.kserve.io", Kind: "LocalModelNodeGroup"},
			gvk:       localModelNodeGroupGVK,
		},
	}

	for _, dw := range r.dynamicWatches {
		if err := cluster.CustomResourceDefinitionExists(context.Background(), mgr.GetAPIReader(), dw.groupKind); err != nil {
			continue
		}
		obj := &unstructured.Unstructured{}
		obj.SetGroupVersionKind(dw.gvk)
		if dw.filterFn != nil {
			b.Watches(obj,
				handler.EnqueueRequestsFromMapFunc(mapToKserve),
				builder.WithPredicates(predicate.NewPredicateFuncs(func(o client.Object) bool {
					u, ok := o.(*unstructured.Unstructured)
					if !ok {
						return false
					}
					return dw.filterFn(u)
				})),
			)
		} else {
			b.Watches(obj, handler.EnqueueRequestsFromMapFunc(mapToKserve))
		}
		dw.registered = true
	}

	c, err := b.Named("kserve-module").
		WithOptions(controller.Options{MaxConcurrentReconciles: 1}).
		Build(r)
	if err != nil {
		return err
	}
	r.controller = c

	if err := mgr.Add(&upgradeRunnable{
		client:        mgr.GetClient(),
		applicationNS: r.getApplicationsNamespace(),
	}); err != nil {
		return fmt.Errorf("error registering upgrade runnable: %w", err)
	}

	return nil
}

func (r *KserveModuleReconciler) registerDynamicWatches(ctx context.Context) {
	r.dynamicWatchMu.Lock()
	defer r.dynamicWatchMu.Unlock()

	if r.controller == nil {
		return
	}

	for _, dw := range r.dynamicWatches {
		if dw.registered {
			continue
		}

		if err := cluster.CustomResourceDefinitionExists(ctx, r.Client, dw.groupKind); err != nil {
			continue
		}

		obj := &unstructured.Unstructured{}
		obj.SetGroupVersionKind(dw.gvk)

		var preds []predicate.Predicate
		if dw.filterFn != nil {
			preds = append(preds, predicate.NewPredicateFuncs(func(o client.Object) bool {
				u, ok := o.(*unstructured.Unstructured)
				if !ok {
					return false
				}
				return dw.filterFn(u)
			}))
		}

		if err := r.controller.Watch(source.Kind[client.Object](r.cache, obj, handler.EnqueueRequestsFromMapFunc(mapToKserve), preds...)); err != nil {
			ctrl.LoggerFrom(ctx).Error(err, "failed to register dynamic watch", "gvk", dw.gvk)
			continue
		}

		dw.registered = true
		ctrl.LoggerFrom(ctx).Info("registered dynamic watch", "gvk", dw.gvk)
	}
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
