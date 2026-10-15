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
	"time"

	configv1 "github.com/openshift/api/config/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/kserve/kserve/pkg/apis/serving/v1beta1"
)

// extendControllerSetup watches the cluster APIServer TLS profile so all
// generated InferenceService proxy sidecars are reconciled when profile policy
// changes.
func (r *InferenceServiceReconciler) extendControllerSetup(mgr manager.Manager, ctrlBuilder *builder.Builder) error {
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
	ctrlBuilder.Watches(&configv1.APIServer{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		requests, err := inferenceServiceRequestsForTLSProfile(ctx, mgr.GetClient(), obj)
		if err != nil {
			r.Log.Error(err, "failed to enqueue InferenceServices after APIServer TLS profile change")
			return nil
		}
		return requests
	}), builder.WithPredicates(profileChanged))
	return nil
}

func inferenceServiceRequestsForTLSProfile(ctx context.Context, reader client.Reader, obj client.Object) ([]reconcile.Request, error) {
	if obj.GetName() != "cluster" {
		return nil, nil
	}
	var isvcs v1beta1.InferenceServiceList
	backoff := wait.Backoff{Duration: 100 * time.Millisecond, Factor: 2, Steps: 3, Jitter: 0.1}
	if err := wait.ExponentialBackoffWithContext(ctx, backoff, func(ctx context.Context) (bool, error) {
		err := reader.List(ctx, &isvcs)
		if err == nil {
			return true, nil
		}
		if !isTransientTLSProfileListError(err) {
			return false, err
		}
		return false, nil
	}); err != nil {
		return nil, err
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
