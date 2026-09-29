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
	"testing"

	routev1 "github.com/openshift/api/route/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"knative.dev/pkg/apis"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
)

func TestRouteStatusURL(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		ingress  []routev1.RouteIngress
		expected string
	}{
		{name: "route not yet admitted"},
		{name: "host without admission condition", ingress: []routev1.RouteIngress{{Host: "pending.example"}}},
		{name: "empty host with admitted condition", ingress: []routev1.RouteIngress{{Conditions: []routev1.RouteIngressCondition{{Type: routev1.RouteAdmitted, Status: corev1.ConditionTrue}}}}},
		{name: "unadmitted host", ingress: []routev1.RouteIngress{{Host: "unavailable.example", Conditions: []routev1.RouteIngressCondition{{Type: routev1.RouteAdmitted, Status: corev1.ConditionFalse}}}}},
		{name: "pending host", ingress: []routev1.RouteIngress{{Host: "pending.example", Conditions: []routev1.RouteIngressCondition{{Type: routev1.RouteAdmitted, Status: corev1.ConditionUnknown}}}}},
		{name: "admitted host", ingress: []routev1.RouteIngress{{Host: "available.example", Conditions: []routev1.RouteIngressCondition{{Type: routev1.RouteAdmitted, Status: corev1.ConditionTrue}}}}, expected: "available.example"},
		{name: "skip unadmitted ingress", ingress: []routev1.RouteIngress{{Host: "unavailable.example"}, {Host: "available.example", Conditions: []routev1.RouteIngressCondition{{Type: routev1.RouteAdmitted, Status: corev1.ConditionTrue}}}}, expected: "available.example"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := routev1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			if err := v1alpha1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			graph := &v1alpha1.InferenceGraph{ObjectMeta: metav1.ObjectMeta{Name: "graph", Namespace: "test"}}
			route := &routev1.Route{ObjectMeta: metav1.ObjectMeta{Name: "graph-route", Namespace: "test"}, Status: routev1.RouteStatus{Ingress: testCase.ingress}}
			objects := []runtime.Object{route}
			if len(testCase.ingress) == 0 {
				objects = nil
			}
			reconciler := OpenShiftRouteReconciler{Scheme: scheme, Client: fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objects...).Build()}
			hostname, err := reconciler.Reconcile(context.Background(), graph)
			if err != nil {
				t.Fatal(err)
			}
			if hostname != testCase.expected {
				t.Fatalf("hostname = %q, want %q", hostname, testCase.expected)
			}
			url := routeStatusURL(&apis.URL{Host: "stale.example", Scheme: "http"}, hostname)
			if hostname == "" {
				if url != nil {
					t.Fatalf("URL = %v, want nil", url)
				}
			} else if url == nil || url.Host != hostname || url.Scheme != "https" {
				t.Fatalf("URL = %v, want https://%s", url, hostname)
			}
		})
	}
}
