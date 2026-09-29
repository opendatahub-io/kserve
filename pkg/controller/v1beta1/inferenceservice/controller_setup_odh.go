//go:build distro

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

package inferenceservice

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	routev1 "github.com/openshift/api/route/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/kserve/kserve/pkg/apis/serving/v1beta1"
	distrotls "github.com/kserve/kserve/pkg/tls/distro"
	"github.com/kserve/kserve/pkg/utils"
)

// extendControllerSetup watches the OpenShift resources the distro build manages for
// InferenceServices: the cluster APIServer TLS profile, so generated proxy sidecars follow
// profile policy; auth-delegator ClusterRoleBindings, so a deleted or edited binding is
// restored; and Routes, so the status URL follows admission.
func (r *InferenceServiceReconciler) extendControllerSetup(mgr manager.Manager, ctrlBuilder *builder.Builder) error {
	retryEvents := make(chan event.GenericEvent, 1)
	var retryScheduled atomic.Bool
	mapProfileEvent := func(ctx context.Context, obj client.Object) []reconcile.Request {
		requests, err := inferenceServiceRequestsForTLSProfile(ctx, mgr.GetClient(), obj)
		if err == nil {
			return requests
		}
		if distrotls.IsTransientTLSProfileListError(err) {
			distrotls.ScheduleTLSProfileRetry(retryEvents, &retryScheduled, 5*time.Second)
		}
		r.Log.Error(err, "failed to enqueue InferenceServices after APIServer TLS profile change")
		return nil
	}
	profileChanged := distrotls.TLSProfileChangedPredicate()
	profileHandler := handler.EnqueueRequestsFromMapFunc(mapProfileEvent)
	ctrlBuilder.Watches(&configv1.APIServer{}, profileHandler, builder.WithPredicates(profileChanged))
	ctrlBuilder.WatchesRawSource(source.Channel(retryEvents, profileHandler))

	ctrlBuilder.Watches(&rbacv1.ClusterRoleBinding{},
		handler.EnqueueRequestsFromMapFunc(r.authDelegatorBindingUsers),
		builder.WithPredicates(authDelegatorBindingChanged()))

	if err := routev1.Install(mgr.GetScheme()); err != nil {
		return fmt.Errorf("failed to add Route v1 APIs to scheme: %w", err)
	}
	routeFound, err := utils.IsCrdAvailable(mgr.GetConfig(), routev1.GroupVersion.String(), "Route")
	if err != nil {
		return err
	}
	if !routeFound {
		r.Log.Info("The InferenceService controller won't watch route.openshift.io/v1/Route resources because the CRD is not available.")
		return nil
	}
	// A Route is mapped by name rather than owner, so a Route the InferenceService does not
	// control still re-reconciles it when it changes or goes away.
	ctrlBuilder.Watches(&routev1.Route{}, handler.EnqueueRequestsFromMapFunc(sameNameInferenceService))
	return nil
}

func inferenceServiceRequestsForTLSProfile(ctx context.Context, reader client.Reader, obj client.Object) ([]reconcile.Request, error) {
	if obj.GetName() != "cluster" {
		return nil, nil
	}
	var isvcs v1beta1.InferenceServiceList
	if err := distrotls.ListWithRetry(ctx, reader, &isvcs); err != nil {
		return nil, err
	}
	requests := make([]reconcile.Request, 0, len(isvcs.Items))
	for i := range isvcs.Items {
		requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&isvcs.Items[i])})
	}
	return requests, nil
}

func sameNameInferenceService(_ context.Context, obj client.Object) []reconcile.Request {
	return []reconcile.Request{{NamespacedName: client.ObjectKeyFromObject(obj)}}
}
