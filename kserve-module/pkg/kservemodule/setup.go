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
	groupKind schema.GroupKind
	gvk       schema.GroupVersionKind
	filterFn  func(*unstructured.Unstructured) bool
	// selfInstalled marks a watch whose CRD this module installs itself. Such a
	// CRD is guaranteed to exist after the reconcile that installs it, so its
	// watch registration is gated (reconcile requeues until registered) rather
	// than best-effort. External/optional CRDs (selfInstalled false) register
	// only if already present and never hold up the happy path.
	selfInstalled bool
	registered    bool
}

// buildDynamicWatches returns the watches attached lazily once their backing CRD
// exists. Kept separate from SetupWithManager so tests can assert the wiring
// (selfInstalled flags, CRD names) without a manager.
func (r *KserveModuleReconciler) buildDynamicWatches() []*dynamicWatch {
	return []*dynamicWatch{
		{
			groupKind: schema.GroupKind{Group: "operator.openshift.io", Kind: "LeaderWorkerSetOperator"},
			gvk:       schema.GroupVersionKind{Group: "operator.openshift.io", Version: "v1", Kind: "LeaderWorkerSetOperator"},
		},
		{
			groupKind:     schema.GroupKind{Group: "serving.kserve.io", Kind: "LocalModelNodeGroup"},
			gvk:           localModelNodeGroupGVK,
			selfInstalled: true,
		},
		{
			// Presets are deliberately out of the ownerRef chain (see
			// unownedGroupKinds), so nothing recreates them when they are deleted.
			// Watching them turns that into a normal reconcile. Scoped to the
			// applications namespace: a reconcile renders and applies everything,
			// so copies a user made elsewhere must not trigger one.
			groupKind:     llmISVCConfigGVK.GroupKind(),
			gvk:           llmISVCConfigGVK,
			selfInstalled: true,
			filterFn: func(u *unstructured.Unstructured) bool {
				return isShippedPreset(u, r.getApplicationsNamespace())
			},
		},
	}
}

func (r *KserveModuleReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Deployer == nil {
		return fmt.Errorf("deployer must not be nil")
	}

	r.cache = mgr.GetCache()
	// Uncached reader for CRD existence checks: a CRD this module installs itself
	// is not yet in the cached client's store during the reconcile that installs
	// it, so registerDynamicWatches must read through to the API server to see it.
	r.apiReader = mgr.GetAPIReader()

	// Register dynamic watches up front so their CRD names can be fed to the CRD
	// watch predicate below. When a CRD this module installs itself (e.g.
	// serving.kserve.io) is created, that enqueues a reconcile and lets
	// registerDynamicWatches attach the watch. Without it, watch registration
	// races startup and, once the reconciler reaches steady state (no requeue),
	// never retries -- leaving deleted presets unrecreated.
	r.dynamicWatches = r.buildDynamicWatches()

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
			builder.WithPredicates(r.crdWatchPredicate()),
		).
		Watches(&corev1.ConfigMap{},
			handler.EnqueueRequestsFromMapFunc(mapToKserve),
			builder.WithPredicates(predicate.NewPredicateFuncs(func(o client.Object) bool {
				return o.GetName() == platformVersionConfigMap &&
					o.GetNamespace() == r.getApplicationsNamespace()
			})),
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

// registerDynamicWatches attaches a watch for each dynamicWatch whose backing
// CRD exists and is Established. It returns true when a self-installed watch is
// still pending registration, so the caller can requeue and retry: without that
// the watch would never register once the happy path stops requeuing
// (RHOAIENG-88471). External/optional CRDs that are simply absent do not mark
// work pending.
func (r *KserveModuleReconciler) registerDynamicWatches(ctx context.Context) bool {
	r.dynamicWatchMu.Lock()
	defer r.dynamicWatchMu.Unlock()

	if r.controller == nil {
		return false
	}

	reader := r.apiReader
	if reader == nil {
		reader = r.Client
	}

	var pending bool
	for _, dw := range r.dynamicWatches {
		if dw.registered {
			continue
		}

		// Read through to the API server: a just-installed CRD is not yet in the
		// cached client's store, so a cached check would miss it and the watch
		// would never register.
		if err := cluster.CustomResourceDefinitionExists(ctx, reader, dw.groupKind); err != nil {
			// A self-installed CRD is guaranteed to exist once installed; if the
			// check does not see it yet (not Established, cache lag), keep the
			// caller requeuing until it does. An absent external CRD is expected.
			if dw.selfInstalled {
				pending = true
			}
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
			if dw.selfInstalled {
				pending = true
			}
			continue
		}

		dw.registered = true
		ctrl.LoggerFrom(ctx).Info("registered dynamic watch", "gvk", dw.gvk)
	}

	return pending
}

func mapToKserve(_ context.Context, _ client.Object) []ctrl.Request {
	return []ctrl.Request{{
		NamespacedName: client.ObjectKey{Name: platformv1alpha1.KserveInstanceName},
	}}
}

func crdNamePredicate(extraNames map[string]bool) predicate.Predicate {
	return predicate.NewPredicateFuncs(func(obj client.Object) bool {
		name := obj.GetName()
		if dependencyCRDNames[name] || extraNames[name] {
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

// crdWatchPredicate builds the predicate for the CustomResourceDefinition watch.
// It must match both this module's dependency CRDs and the CRDs backing its own
// dynamic watches, so that installing a serving.kserve.io CRD this module ships
// itself enqueues a reconcile (RHOAIENG-88471). Feeding it crdNamePredicate(nil)
// reintroduces that bug: self-installed CRDs would be filtered out and the
// preset self-heal watch would never register.
func (r *KserveModuleReconciler) crdWatchPredicate() predicate.Predicate {
	return crdNamePredicate(r.dynamicWatchCRDNames())
}

// dynamicWatchCRDNames returns the CRD resource names (e.g.
// "llminferenceserviceconfigs.serving.kserve.io") backing every dynamic watch,
// so that creation of a CRD this module installs itself enqueues a reconcile and
// registerDynamicWatches can attach the watch. The name derivation mirrors
// cluster.CustomResourceDefinitionExists so the predicate and the existence
// check always agree.
func (r *KserveModuleReconciler) dynamicWatchCRDNames() map[string]bool {
	names := make(map[string]bool, len(r.dynamicWatches))
	for _, dw := range r.dynamicWatches {
		names[crdResourceName(dw.groupKind)] = true
	}
	return names
}

func crdResourceName(gk schema.GroupKind) string {
	return strings.ToLower(fmt.Sprintf("%ss.%s", gk.Kind, gk.Group))
}
