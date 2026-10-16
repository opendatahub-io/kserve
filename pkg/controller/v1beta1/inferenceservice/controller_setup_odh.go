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
	"reflect"
	"sync/atomic"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/kserve/kserve/pkg/apis/serving/v1beta1"
)

// extendControllerSetup watches the cluster APIServer TLS profile so all
// generated InferenceService proxy sidecars are reconciled when profile policy
// changes.
func (r *InferenceServiceReconciler) extendControllerSetup(mgr manager.Manager, ctrlBuilder *builder.Builder) error {
	retryEvents := make(chan event.GenericEvent, 1)
	var retryScheduled atomic.Bool
	mapProfileEvent := func(ctx context.Context, obj client.Object) []reconcile.Request {
		requests, err := inferenceServiceRequestsForTLSProfile(ctx, mgr.GetClient(), obj)
		if err == nil {
			return requests
		}
		if isTransientTLSProfileListError(err) {
			scheduleTLSProfileRetry(retryEvents, &retryScheduled, 5*time.Second)
		}
		r.Log.Error(err, "failed to enqueue InferenceServices after APIServer TLS profile change")
		return nil
	}
	profileChanged := predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return e.Object.GetName() == "cluster"
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldAPIServer, oldOK := e.ObjectOld.(*configv1.APIServer)
			newAPIServer, newOK := e.ObjectNew.(*configv1.APIServer)
			return oldOK && newOK && newAPIServer.Name == "cluster" && !reflect.DeepEqual(oldAPIServer.Spec, newAPIServer.Spec)
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return e.Object.GetName() == "cluster"
		},
		GenericFunc: func(e event.GenericEvent) bool {
			return e.Object.GetName() == "cluster"
		},
	}
	profileHandler := handler.EnqueueRequestsFromMapFunc(mapProfileEvent)
	ctrlBuilder.Watches(&configv1.APIServer{}, profileHandler, builder.WithPredicates(profileChanged))
	ctrlBuilder.WatchesRawSource(source.Channel(retryEvents, profileHandler))
	return nil
}

func scheduleTLSProfileRetry(events chan<- event.GenericEvent, scheduled *atomic.Bool, delay time.Duration) {
	if !scheduled.CompareAndSwap(false, true) {
		return
	}
	time.AfterFunc(delay, func() {
		scheduled.Store(false)
		retry := event.GenericEvent{Object: &configv1.APIServer{ObjectMeta: metav1.ObjectMeta{Name: "cluster"}}}
		select {
		case events <- retry:
		default: // A retry is already queued; the cluster-wide events are coalesced.
		}
	})
}

func inferenceServiceRequestsForTLSProfile(ctx context.Context, reader client.Reader, obj client.Object) ([]reconcile.Request, error) {
	if obj.GetName() != "cluster" {
		return nil, nil
	}
	var isvcs v1beta1.InferenceServiceList
	delay := 100 * time.Millisecond
	for attempt := range 3 {
		isvcs = v1beta1.InferenceServiceList{}
		err := reader.List(ctx, &isvcs)
		if err == nil {
			break
		}
		if !isTransientTLSProfileListError(err) || attempt == 2 {
			return nil, err
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
		delay *= 2
	}
	requests := make([]reconcile.Request, 0, len(isvcs.Items))
	for i := range isvcs.Items {
		requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&isvcs.Items[i])})
	}
	return requests, nil
}

func isTransientTLSProfileListError(err error) bool {
	return apierrors.IsTimeout(err) || apierrors.IsServerTimeout(err) ||
		apierrors.IsTooManyRequests(err) || apierrors.IsServiceUnavailable(err)
}
