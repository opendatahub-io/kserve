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
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/kserve/kserve/pkg/apis/serving/v1beta1"
)

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
