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

package distro

import (
	"context"
	"errors"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/kserve/kserve/pkg/constants"
)

const (
	testSourceNamespace = "applications"
	testTargetNamespace = "model-serving-cache"
)

func newCABundleTestReconciler(t *testing.T, objects ...client.Object) (*CABundleReconciler, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
		Name: localModelDeploymentName, Namespace: testTargetNamespace, UID: types.UID("deployment-uid"),
	}}
	objects = append(objects, deployment)
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	return &CABundleReconciler{
		Client: cli, Reader: cli, Scheme: scheme,
		SourceNamespace: testSourceNamespace, TargetNamespace: testTargetNamespace,
	}, cli
}

func reconcileCABundle(t *testing.T, r *CABundleReconciler) error {
	t.Helper()
	_, err := r.Reconcile(context.Background(), ctrl.Request{})
	return err
}

func TestCABundleReconcilerCreateUpdateAndSelfHeal(t *testing.T) {
	source := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: CABundleConfigMapName, Namespace: testSourceNamespace},
		Data:       map[string]string{constants.DefaultCaBundleFileName: "certificate-1", "other": "not copied"},
	}
	r, cli := newCABundleTestReconciler(t, source)
	if err := reconcileCABundle(t, r); err != nil {
		t.Fatal(err)
	}
	targetKey := client.ObjectKey{Name: CABundleConfigMapName, Namespace: testTargetNamespace}
	target := &corev1.ConfigMap{}
	if err := cli.Get(context.Background(), targetKey, target); err != nil {
		t.Fatal(err)
	}
	if len(target.Data) != 1 || target.Data[constants.DefaultCaBundleFileName] != "certificate-1" ||
		!isManagedBundle(target) || target.OwnerReferences[0].UID != types.UID("deployment-uid") ||
		target.OwnerReferences[0].BlockOwnerDeletion == nil || *target.OwnerReferences[0].BlockOwnerDeletion {
		t.Fatalf("unexpected managed CA bundle: %#v", target)
	}
	source.Data[constants.DefaultCaBundleFileName] = "certificate-2"
	if err := cli.Update(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	if err := reconcileCABundle(t, r); err != nil {
		t.Fatal(err)
	}
	if err := cli.Get(context.Background(), targetKey, target); err != nil {
		t.Fatal(err)
	}
	if target.Data[constants.DefaultCaBundleFileName] != "certificate-2" {
		t.Fatalf("stale CA bundle was not updated: %#v", target.Data)
	}
	if err := cli.Delete(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	if err := reconcileCABundle(t, r); err != nil {
		t.Fatal(err)
	}
	if err := cli.Get(context.Background(), targetKey, target); err != nil {
		t.Fatalf("deleted managed CA bundle was not recreated: %v", err)
	}
	if err := cli.Delete(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	if err := reconcileCABundle(t, r); err != nil {
		t.Fatal(err)
	}
	if err := cli.Get(context.Background(), targetKey, target); !apierrors.IsNotFound(err) {
		t.Fatalf("expected source deletion to remove owned target, got %v", err)
	}
}

func TestCABundleReconcilerEmptySourceRemovesManagedTarget(t *testing.T) {
	source := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: CABundleConfigMapName, Namespace: testSourceNamespace},
		Data:       map[string]string{constants.DefaultCaBundleFileName: "certificate"},
	}
	r, cli := newCABundleTestReconciler(t, source)
	if err := reconcileCABundle(t, r); err != nil {
		t.Fatal(err)
	}
	source.Data[constants.DefaultCaBundleFileName] = " \n"
	if err := cli.Update(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	if err := reconcileCABundle(t, r); err != nil {
		t.Fatal(err)
	}
	if err := cli.Get(context.Background(), client.ObjectKey{Name: CABundleConfigMapName, Namespace: testTargetNamespace}, &corev1.ConfigMap{}); !apierrors.IsNotFound(err) {
		t.Fatalf("expected empty source to remove target, got %v", err)
	}
}

func TestCABundleReconcilerRejectsUnrelatedTarget(t *testing.T) {
	source := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: CABundleConfigMapName, Namespace: testSourceNamespace},
		Data:       map[string]string{constants.DefaultCaBundleFileName: "certificate"},
	}
	target := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: CABundleConfigMapName, Namespace: testTargetNamespace},
		Data:       map[string]string{constants.DefaultCaBundleFileName: "unrelated"},
	}
	r, cli := newCABundleTestReconciler(t, source, target)
	if err := reconcileCABundle(t, r); err == nil {
		t.Fatal("expected conflict for an unrelated target ConfigMap")
	}
	actual := &corev1.ConfigMap{}
	if err := cli.Get(context.Background(), client.ObjectKeyFromObject(target), actual); err != nil {
		t.Fatal(err)
	}
	if actual.Data[constants.DefaultCaBundleFileName] != "unrelated" {
		t.Fatalf("unrelated target was modified: %#v", actual.Data)
	}
}

type failingCABundleReader struct{ client.Reader }

func (failingCABundleReader) Get(context.Context, client.ObjectKey, client.Object, ...client.GetOption) error {
	return errors.New("read failed")
}

func TestCABundleReconcilerReadErrorAndPredicate(t *testing.T) {
	r, _ := newCABundleTestReconciler(t)
	r.Reader = failingCABundleReader{}
	if err := reconcileCABundle(t, r); err == nil {
		t.Fatal("expected API read error to trigger retry")
	}
	for _, tt := range []struct {
		name, namespace string
		want            bool
	}{
		{CABundleConfigMapName, testSourceNamespace, true},
		{CABundleConfigMapName, testTargetNamespace, true},
		{CABundleConfigMapName, "user-namespace", false},
		{"unrelated", testSourceNamespace, false},
	} {
		cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: tt.name, Namespace: tt.namespace}}
		if got := r.isBundle(cm); got != tt.want {
			t.Fatalf("predicate for %s/%s = %t, want %t", tt.namespace, tt.name, got, tt.want)
		}
	}
}

func TestCABundleReconcilerRepairsStaleDeploymentOwner(t *testing.T) {
	oldUID := types.UID("old-deployment-uid")
	newUID := types.UID("new-deployment-uid")
	controller := true
	blockDeletion := true
	target := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Name: CABundleConfigMapName, Namespace: testTargetNamespace,
		OwnerReferences: []metav1.OwnerReference{{
			APIVersion: appsv1.SchemeGroupVersion.String(), Kind: "Deployment",
			Name: localModelDeploymentName, UID: oldUID,
			Controller: &controller, BlockOwnerDeletion: &blockDeletion,
		}},
	}, Data: map[string]string{constants.DefaultCaBundleFileName: "old-certificate"}}
	source := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: CABundleConfigMapName, Namespace: testSourceNamespace},
		Data:       map[string]string{constants.DefaultCaBundleFileName: "new-certificate"},
	}
	r, cli := newCABundleTestReconciler(t, source, target)
	deployment := &appsv1.Deployment{}
	if err := cli.Get(context.Background(), client.ObjectKey{Name: localModelDeploymentName, Namespace: testTargetNamespace}, deployment); err != nil {
		t.Fatal(err)
	}
	deployment.UID = newUID
	if err := cli.Update(context.Background(), deployment); err != nil {
		t.Fatal(err)
	}
	if err := reconcileCABundle(t, r); err != nil {
		t.Fatal(err)
	}
	actual := &corev1.ConfigMap{}
	if err := cli.Get(context.Background(), client.ObjectKeyFromObject(target), actual); err != nil {
		t.Fatal(err)
	}
	if actual.Data[constants.DefaultCaBundleFileName] != "new-certificate" || len(actual.OwnerReferences) != 1 {
		t.Fatalf("target was not reconciled: %#v", actual)
	}
	owner := actual.OwnerReferences[0]
	if owner.UID != newUID || owner.BlockOwnerDeletion == nil || *owner.BlockOwnerDeletion {
		t.Fatalf("stale Deployment owner was not repaired: %#v", owner)
	}
}
