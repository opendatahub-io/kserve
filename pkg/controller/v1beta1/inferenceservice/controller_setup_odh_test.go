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
	"errors"
	"sync/atomic"
	"testing"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"

	"github.com/kserve/kserve/pkg/apis/serving/v1beta1"
)

type listRetryReader struct {
	client.Reader
	remainingFailures int
	err               error
	listCalls         int
}

func (r *listRetryReader) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	r.listCalls++
	if r.remainingFailures > 0 {
		r.remainingFailures--
		return r.err
	}
	return r.Reader.List(ctx, list, opts...)
}

func TestInferenceServiceRequestsForTLSProfile(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := v1beta1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	reader := fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(
		&v1beta1.InferenceService{ObjectMeta: metav1.ObjectMeta{Name: "one", Namespace: "team-a"}},
		&v1beta1.InferenceService{ObjectMeta: metav1.ObjectMeta{Name: "two", Namespace: "team-b"}},
	).Build()

	requests, err := inferenceServiceRequestsForTLSProfile(t.Context(), reader, &configv1.APIServer{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 {
		t.Fatalf("got %d reconcile requests, want 2", len(requests))
	}
	got := map[string]bool{}
	for _, request := range requests {
		got[request.Namespace+"/"+request.Name] = true
	}
	if !got["team-a/one"] || !got["team-b/two"] {
		t.Fatalf("got reconcile requests %v, want both InferenceServices", got)
	}

	requests, err = inferenceServiceRequestsForTLSProfile(t.Context(), reader, &configv1.APIServer{
		ObjectMeta: metav1.ObjectMeta{Name: "other"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 0 {
		t.Fatalf("non-cluster APIServer produced %d requests, want none", len(requests))
	}
}

func TestInferenceServiceRequestsForTLSProfileRetriesTransientListFailures(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := v1beta1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	reader := &listRetryReader{
		Reader: fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(
			&v1beta1.InferenceService{ObjectMeta: metav1.ObjectMeta{Name: "one", Namespace: "team-a"}},
		).Build(),
		remainingFailures: 1,
		err:               apierrors.NewTimeoutError("temporary list failure", 1),
	}

	requests, err := inferenceServiceRequestsForTLSProfile(t.Context(), reader, &configv1.APIServer{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if reader.listCalls != 2 || len(requests) != 1 || requests[0].NamespacedName != (client.ObjectKey{Namespace: "team-a", Name: "one"}) {
		t.Fatalf("got calls=%d requests=%v, want a retry and the InferenceService request", reader.listCalls, requests)
	}
}

func TestInferenceServiceRequestsForTLSProfileReturnsTransientErrorAfterBoundedRetries(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := v1beta1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	reader := &listRetryReader{
		Reader: fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(
			&v1beta1.InferenceService{ObjectMeta: metav1.ObjectMeta{Name: "one", Namespace: "team-a"}},
		).Build(),
		remainingFailures: 3,
		err:               apierrors.NewServiceUnavailable("temporary list failure"),
	}
	requests, err := inferenceServiceRequestsForTLSProfile(t.Context(), reader, &configv1.APIServer{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
	})
	if !isTransientTLSProfileListError(err) || len(requests) != 0 {
		t.Fatalf("got requests=%v, err=%v; want transient error for asynchronous retry", requests, err)
	}
	if reader.listCalls != 3 {
		t.Fatalf("list called %d times, want 3 bounded attempts", reader.listCalls)
	}
}

func TestInferenceServiceRequestsForTLSProfileDoesNotRetryPermanentErrors(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := v1beta1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	baseReader := fakeclient.NewClientBuilder().WithScheme(scheme).Build()
	for name, listErr := range map[string]error{
		"forbidden":  apierrors.NewForbidden(schema.GroupResource{Group: "serving.kserve.io", Resource: "inferenceservices"}, "", nil),
		"unexpected": errors.New("unexpected list failure"),
	} {
		t.Run(name, func(t *testing.T) {
			reader := &listRetryReader{Reader: baseReader, err: listErr, remainingFailures: 1}
			requests, err := inferenceServiceRequestsForTLSProfile(t.Context(), reader, &configv1.APIServer{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
			})
			if err == nil || isTransientTLSProfileListError(err) || len(requests) != 0 {
				t.Fatalf("got requests=%v, err=%v; want permanent fail-closed error", requests, err)
			}
			if reader.listCalls != 1 {
				t.Fatalf("list called %d times, want one attempt for permanent %s error", reader.listCalls, name)
			}
		})
	}
}

func TestScheduleTLSProfileRetryQueuesClusterEventAndCoalesces(t *testing.T) {
	retryEvents := make(chan event.GenericEvent, 1)
	var scheduled atomic.Bool
	scheme := runtime.NewScheme()
	if err := v1beta1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	reader := &listRetryReader{
		Reader: fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(
			&v1beta1.InferenceService{ObjectMeta: metav1.ObjectMeta{Name: "one", Namespace: "team-a"}},
		).Build(),
		remainingFailures: 3,
		err:               apierrors.NewServiceUnavailable("temporary list failure"),
	}
	apiServer := &configv1.APIServer{ObjectMeta: metav1.ObjectMeta{Name: "cluster"}}
	if _, err := inferenceServiceRequestsForTLSProfile(t.Context(), reader, apiServer); !isTransientTLSProfileListError(err) {
		t.Fatalf("initial profile mapping error = %v, want transient failure", err)
	}
	scheduleTLSProfileRetry(retryEvents, &scheduled, time.Millisecond)
	scheduleTLSProfileRetry(retryEvents, &scheduled, time.Millisecond)

	select {
	case retry := <-retryEvents:
		if retry.Object.GetName() != "cluster" {
			t.Fatalf("retry event object name = %q, want cluster", retry.Object.GetName())
		}
		requests, err := inferenceServiceRequestsForTLSProfile(t.Context(), reader, retry.Object)
		if err != nil || len(requests) != 1 || requests[0].NamespacedName != (client.ObjectKey{Namespace: "team-a", Name: "one"}) {
			t.Fatalf("retry mapping got requests=%v, err=%v; want recovered InferenceService request", requests, err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the retry APIServer event")
	}
}
