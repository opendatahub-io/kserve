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

package inferencegraph

import (
	"context"
	"sync/atomic"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	osroutev1 "github.com/openshift/api/route/v1"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
	distrotls "github.com/kserve/kserve/pkg/tls/distro"
	"github.com/kserve/kserve/pkg/utils"
)

// extendControllerSetup watches the OpenShift Routes that expose raw deployment graphs,
// when the Route CRD is installed, and the cluster APIServer TLS profile so raw
// deployment routers that project the resolved profile are reconciled when the
// profile policy changes.
func (r *InferenceGraphReconciler) extendControllerSetup(mgr manager.Manager, b *builder.Builder) error {
	routeFound, err := utils.IsCrdAvailable(r.ClientConfig, osroutev1.GroupVersion.String(), "Route")
	if err != nil {
		return err
	}

	if routeFound {
		b.Owns(&osroutev1.Route{})
	} else {
		r.Log.Info("The InferenceGraph controller won't watch route.openshift.io/v1/Route resources because the CRD is not available.")
	}

	retryEvents := make(chan event.GenericEvent, 1)
	var retryScheduled atomic.Bool
	mapProfileEvent := func(ctx context.Context, obj client.Object) []reconcile.Request {
		requests, err := inferenceGraphRequestsForTLSProfile(ctx, mgr.GetClient(), obj)
		if err == nil {
			return requests
		}
		if distrotls.IsTransientTLSProfileListError(err) {
			distrotls.ScheduleTLSProfileRetry(retryEvents, &retryScheduled, 5*time.Second)
		}
		r.Log.Error(err, "failed to enqueue InferenceGraphs after APIServer TLS profile change")
		return nil
	}
	profileHandler := handler.EnqueueRequestsFromMapFunc(mapProfileEvent)
	b.Watches(&configv1.APIServer{}, profileHandler, builder.WithPredicates(distrotls.TLSProfileChangedPredicate()))
	b.WatchesRawSource(source.Channel(retryEvents, profileHandler))

	return nil
}

func inferenceGraphRequestsForTLSProfile(ctx context.Context, reader client.Reader, obj client.Object) ([]reconcile.Request, error) {
	if obj.GetName() != "cluster" {
		return nil, nil
	}
	var graphs v1alpha1.InferenceGraphList
	if err := distrotls.ListWithRetry(ctx, reader, &graphs); err != nil {
		return nil, err
	}
	requests := make([]reconcile.Request, 0, len(graphs.Items))
	for i := range graphs.Items {
		requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&graphs.Items[i])})
	}
	return requests, nil
}
