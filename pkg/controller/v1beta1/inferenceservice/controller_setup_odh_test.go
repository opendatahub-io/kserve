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
	"testing"

	"github.com/go-logr/logr"
	configv1 "github.com/openshift/api/config/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"

	"github.com/kserve/kserve/pkg/apis/serving/v1beta1"
)

func TestMapTLSProfileToInferenceServices(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := v1beta1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	objects := []runtime.Object{
		&v1beta1.InferenceService{ObjectMeta: metav1.ObjectMeta{Name: "first", Namespace: "ns-a"}},
		&v1beta1.InferenceService{ObjectMeta: metav1.ObjectMeta{Name: "second", Namespace: "ns-b"}},
	}
	r := &InferenceServiceReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objects...).Build(),
		Log:    logr.Discard(),
	}

	requests := r.mapTLSProfileToInferenceServices(context.Background(), nil)
	want := map[types.NamespacedName]bool{
		{Namespace: "ns-a", Name: "first"}:  true,
		{Namespace: "ns-b", Name: "second"}: true,
	}
	if len(requests) != len(want) {
		t.Fatalf("got %d requests, want %d", len(requests), len(want))
	}
	for _, request := range requests {
		if !want[request.NamespacedName] {
			t.Errorf("unexpected request %s", request.NamespacedName)
		}
	}
}

func TestTLSSecurityProfileChangedPredicate(t *testing.T) {
	predicate := tlsSecurityProfileChangedPredicate()
	oldAPIServer := &configv1.APIServer{ObjectMeta: metav1.ObjectMeta{Name: clusterAPIServerName}}
	newAPIServer := oldAPIServer.DeepCopy()
	newAPIServer.Spec.TLSSecurityProfile = &configv1.TLSSecurityProfile{Type: configv1.TLSProfileModernType}

	if !predicate.Update(event.UpdateEvent{ObjectOld: oldAPIServer, ObjectNew: newAPIServer}) {
		t.Fatal("expected TLS profile change to trigger reconciliation")
	}

	unchanged := newAPIServer.DeepCopy()
	unchanged.Labels = map[string]string{"unrelated": "change"}
	if predicate.Update(event.UpdateEvent{ObjectOld: newAPIServer, ObjectNew: unchanged}) {
		t.Fatal("unrelated APIServer change triggered reconciliation")
	}
}
