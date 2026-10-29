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

package llmisvc

import (
	"context"
	"errors"
	"strings"
	"testing"

	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
)

func TestSchedulerMonitorWaitsForKnownConfig(t *testing.T) {
	model := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{Name: "first", Namespace: "models", UID: "first"}}
	r := &LLMISVCReconciler{EventRecorder: record.NewFakeRecorder(20)}
	previous := r.expectedSchedulerMonitor(model)
	r.Client = monitoringTestClient(t, model, previous)
	if err := r.reconcileSchedulerMonitor(t.Context(), model, nil); err == nil || !strings.Contains(err.Error(), "configuration is unavailable") {
		t.Fatalf("missing configuration must defer reconciliation with an explicit error got %v", err)
	}
	actual := &monitoringv1.ServiceMonitor{}
	if err := r.Get(t.Context(), client.ObjectKeyFromObject(previous), actual); err != nil {
		t.Fatal(err)
	}
	if actual.Spec.Endpoints[0].Scheme == nil || *actual.Spec.Endpoints[0].Scheme != "http" {
		t.Fatal("unknown TLS configuration changed the existing HTTP monitor")
	}
}

func TestSchedulerMonitorMigrationWaitsForEveryService(t *testing.T) {
	for _, tt := range []struct {
		name         string
		replacements int
		foreignOwner bool
		retain       bool
	}{
		{name: "another service has not reconciled", replacements: 0, retain: true},
		{name: "only one replacement exists", replacements: 1, retain: true},
		{name: "replacement belongs to a previous instance", replacements: 2, foreignOwner: true, retain: true},
		{name: "every service has both replacements", replacements: 2, retain: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			first := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{Name: "first", Namespace: "models", UID: "first"}}
			second := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{Name: "second", Namespace: "models", UID: "second"}}
			objects := []client.Object{first, second}
			r := &LLMISVCReconciler{EventRecorder: record.NewFakeRecorder(20)}
			names := []string{"kserve-llm-isvc-scheduler-default", "kserve-llm-isvc-scheduler"}
			for i, name := range names {
				objects = append(objects, &monitoringv1.ServiceMonitor{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "models"}})
				if i < tt.replacements {
					replacement := r.expectedSchedulerMonitor(second)
					replacement.Name = "second-" + name
					if tt.foreignOwner {
						replacement.OwnerReferences[0].UID = "previous-instance"
					}
					objects = append(objects, replacement)
				}
			}
			r.Client = monitoringTestClient(t, objects...)
			if err := r.reconcileSchedulerMonitor(t.Context(), first, &Config{EnableTLS: true}); err != nil {
				t.Fatal(err)
			}
			for _, name := range names {
				monitor := &monitoringv1.ServiceMonitor{}
				err := r.Get(t.Context(), client.ObjectKey{Name: name, Namespace: "models"}, monitor)
				if tt.retain && err != nil {
					t.Errorf("shared monitor %s was removed before migration completed: %v", name, err)
				}
				if !tt.retain && !apierrors.IsNotFound(err) {
					t.Errorf("shared monitor %s should be removed after migration: %v", name, err)
				}
			}
		})
	}
}

func monitoringTestClient(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, v1alpha2.AddToScheme, monitoringv1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{monitoringv1.SchemeGroupVersion})
	mapper.Add(monitoringv1.SchemeGroupVersion.WithKind("ServiceMonitor"), meta.RESTScopeNamespace)
	return fake.NewClientBuilder().WithScheme(scheme).WithRESTMapper(mapper).WithObjects(objects...).Build()
}

func TestMonitoringFinalizationWithoutServiceMonitorAPI(t *testing.T) {
	for _, tt := range []struct {
		name      string
		listError error
		wantError bool
	}{
		{name: "removed CRD", listError: &meta.NoKindMatchError{GroupKind: schema.GroupKind{Group: "monitoring.coreos.com", Kind: "ServiceMonitor"}}},
		{name: "other list errors remain fatal", listError: errors.New("access denied"), wantError: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			model := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{Name: "last", Namespace: "models"}}
			r := &LLMISVCReconciler{EventRecorder: record.NewFakeRecorder(20)}
			account := r.expectedMetricsReaderServiceAccount(model)
			scheme := runtime.NewScheme()
			for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, netv1.AddToScheme, rbacv1.AddToScheme, v1alpha2.AddToScheme, monitoringv1.AddToScheme} {
				if err := add(scheme); err != nil {
					t.Fatal(err)
				}
			}
			base := fake.NewClientBuilder().WithScheme(scheme).WithObjects(account).Build()
			r.Client = interceptor.NewClient(base, interceptor.Funcs{List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				if _, ok := list.(*monitoringv1.ServiceMonitorList); ok {
					return tt.listError
				}
				return c.List(ctx, list, opts...)
			}})
			err := r.cleanupMonitoringResources(t.Context(), model)
			if tt.wantError {
				if !errors.Is(err, tt.listError) {
					t.Fatalf("expected list failure to block cleanup got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := r.Get(t.Context(), client.ObjectKeyFromObject(account), &corev1.ServiceAccount{}); !apierrors.IsNotFound(err) {
				t.Fatalf("missing monitoring CRD prevented shared account cleanup: %v", err)
			}
		})
	}
}
