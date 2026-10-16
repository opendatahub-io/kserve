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

package reconcilers

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
	"github.com/kserve/kserve/pkg/constants"
	controllerutils "github.com/kserve/kserve/pkg/controller/v1alpha1/utils"
)

func TestLocalModelConfigMapFunc(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add serving scheme: %v", err)
	}
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&v1alpha1.LocalModelCache{ObjectMeta: metav1.ObjectMeta{Name: "cluster-model"}},
	).Build()
	reconciler := &LocalModelReconciler{Client: reader, Log: logr.Discard()}

	requests := reconciler.configMapFunc(context.Background(), &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: constants.InferenceServiceConfigMapName, Namespace: controllerutils.InferenceServiceConfigNamespace()},
	})
	want := []reconcile.Request{{NamespacedName: client.ObjectKey{Name: "cluster-model"}}}
	if len(requests) != len(want) || requests[0] != want[0] {
		t.Fatalf("configMapFunc() = %#v, want %#v", requests, want)
	}

	if requests := reconciler.configMapFunc(context.Background(), &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: constants.InferenceServiceConfigMapName, Namespace: "other"},
	}); requests != nil {
		t.Fatalf("configMapFunc() for another namespace = %#v, want nil", requests)
	}
}

func TestLocalModelNamespaceCacheConfigMapFunc(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add serving scheme: %v", err)
	}
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&v1alpha1.LocalModelNamespaceCache{ObjectMeta: metav1.ObjectMeta{Name: "namespace-model", Namespace: "team-a"}},
	).Build()
	reconciler := &LocalModelNamespaceCacheReconciler{Client: reader, Log: logr.Discard()}

	requests := reconciler.configMapFuncNamespaceCache(context.Background(), &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: constants.InferenceServiceConfigMapName, Namespace: controllerutils.InferenceServiceConfigNamespace()},
	})
	want := []reconcile.Request{{NamespacedName: client.ObjectKey{Name: "namespace-model", Namespace: "team-a"}}}
	if len(requests) != len(want) || requests[0] != want[0] {
		t.Fatalf("configMapFuncNamespaceCache() = %#v, want %#v", requests, want)
	}
}
