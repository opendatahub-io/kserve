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

package distro

import (
	"context"
	"reflect"
	"sync/atomic"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// TLSProfileChangedPredicate passes events for the cluster APIServer object
// whose TLS policy may have changed: creation, deletion, generic synthetic
// events, and spec updates. Controllers use it to watch
// config.openshift.io/v1 APIServer and re-reconcile workloads that project
// the resolved profile.
func TLSProfileChangedPredicate() predicate.Funcs {
	return predicate.Funcs{
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
}

// ScheduleTLSProfileRetry enqueues a synthetic cluster APIServer event into
// the given channel after the delay, coalescing concurrent schedules. It is
// used to retry enqueuing workloads when listing them failed transiently.
func ScheduleTLSProfileRetry(events chan<- event.GenericEvent, scheduled *atomic.Bool, delay time.Duration) {
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

// IsTransientTLSProfileListError reports whether listing workloads failed
// with an error worth retrying.
func IsTransientTLSProfileListError(err error) bool {
	return apierrors.IsTimeout(err) || apierrors.IsServerTimeout(err) ||
		apierrors.IsTooManyRequests(err) || apierrors.IsServiceUnavailable(err)
}

// ListWithRetry lists objects with three attempts and exponential backoff,
// retrying only transient errors. It backs the enqueue-all mappers that react
// to cluster TLS profile changes.
func ListWithRetry(ctx context.Context, reader client.Reader, list client.ObjectList) error {
	delay := 100 * time.Millisecond
	var err error
	for attempt := range 3 {
		err = reader.List(ctx, list)
		if err == nil {
			return nil
		}
		if !IsTransientTLSProfileListError(err) || attempt == 2 {
			return err
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		delay *= 2
	}
	return err
}
