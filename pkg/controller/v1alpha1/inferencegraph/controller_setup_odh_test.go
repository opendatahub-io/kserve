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

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
	distrotls "github.com/kserve/kserve/pkg/tls/distro"
)

type igListRetryReader struct {
	client.Reader
	remainingFailures int
	err               error
	listCalls         int
}

func (r *igListRetryReader) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	r.listCalls++
	if r.remainingFailures > 0 {
		r.remainingFailures--
		return r.err
	}
	return r.Reader.List(ctx, list, opts...)
}

func TestInferenceGraphRequestsForTLSProfile(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	reader := fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(
		&v1alpha1.InferenceGraph{ObjectMeta: metav1.ObjectMeta{Name: "one", Namespace: "team-a"}},
		&v1alpha1.InferenceGraph{ObjectMeta: metav1.ObjectMeta{Name: "two", Namespace: "team-b"}},
	).Build()

	requests, err := inferenceGraphRequestsForTLSProfile(t.Context(), reader, &configv1.APIServer{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 {
		t.Fatalf("got %d requests, want 2", len(requests))
	}
	want := map[string]bool{"team-a/one": false, "team-b/two": false}
	for _, request := range requests {
		if _, ok := want[request.String()]; !ok {
			t.Fatalf("unexpected request %s", request.String())
		}
		want[request.String()] = true
	}
	for name, seen := range want {
		if !seen {
			t.Fatalf("missing request for %s", name)
		}
	}

	// Only the cluster object maps to requests.
	requests, err = inferenceGraphRequestsForTLSProfile(t.Context(), reader, &configv1.APIServer{
		ObjectMeta: metav1.ObjectMeta{Name: "other"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 0 {
		t.Fatalf("got %d requests for a non-cluster object, want 0", len(requests))
	}
}

func TestInferenceGraphRequestsForTLSProfileRetriesTransientErrors(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	base := fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(
		&v1alpha1.InferenceGraph{ObjectMeta: metav1.ObjectMeta{Name: "one", Namespace: "team-a"}},
	).Build()
	reader := &igListRetryReader{Reader: base, remainingFailures: 2, err: apierrors.NewServiceUnavailable("apiserver unavailable")}

	requests, err := inferenceGraphRequestsForTLSProfile(t.Context(), reader, &configv1.APIServer{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 1 {
		t.Fatalf("got %d requests, want 1", len(requests))
	}
	if reader.listCalls != 3 {
		t.Fatalf("got %d list calls, want 3 (two transient failures retried)", reader.listCalls)
	}

	// Non-transient errors surface without retries.
	reader = &igListRetryReader{Reader: base, remainingFailures: 1, err: apierrors.NewForbidden(schema.GroupResource{}, "cluster", nil)}
	if _, err := inferenceGraphRequestsForTLSProfile(t.Context(), reader, &configv1.APIServer{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
	}); err == nil {
		t.Fatal("want error for a non-transient list failure")
	}
}

func TestInferenceGraphTLSProfileRetryScheduling(t *testing.T) {
	retryEvents := make(chan event.GenericEvent, 1)
	var scheduled atomic.Bool

	distrotls.ScheduleTLSProfileRetry(retryEvents, &scheduled, time.Millisecond)
	distrotls.ScheduleTLSProfileRetry(retryEvents, &scheduled, time.Millisecond)

	select {
	case evt := <-retryEvents:
		if evt.Object.GetName() != "cluster" {
			t.Fatalf("retry event object = %s, want cluster", evt.Object.GetName())
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the retry event")
	}
	select {
	case <-retryEvents:
		t.Fatal("retry events must coalesce")
	default:
	}
}
