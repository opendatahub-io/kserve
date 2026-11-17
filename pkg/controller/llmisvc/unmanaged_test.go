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
	"testing"

	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/record"
	"knative.dev/pkg/apis"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
	"github.com/kserve/kserve/pkg/constants"
)

func unmanagedTestClient(t *testing.T, objects ...client.Object) client.WithWatch {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, install := range []func(*runtime.Scheme) error{
		v1alpha1.AddToScheme, appsv1.AddToScheme, corev1.AddToScheme, rbacv1.AddToScheme, monitoringv1.AddToScheme,
	} {
		require.NoError(t, install(scheme))
	}
	return fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&v1alpha1.LLMInferenceService{}).WithObjects(objects...).Build()
}

func TestUnmanagedLeavesWorkloadsUntouched(t *testing.T) {
	ctx := t.Context()
	service := &v1alpha1.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{
		Name: "existing", Namespace: "models", Generation: 3,
		Finalizers: []string{constants.KServeAPIGroupName + "/llmisvc-finalizer"},
	}}
	service.Status.ObservedGeneration = 2
	service.MarkMainWorkloadReady()
	service.MarkSchedulerWorkloadReady()
	service.MarkInferencePoolReady()
	service.GetConditionSet().Manage(service.GetStatus()).MarkTrue(apis.ConditionReady)
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "existing-workload", Namespace: service.Namespace}}
	deployment.Spec.Template.Spec.Containers = []corev1.Container{{Name: "main", Image: "existing@sha256:unchanged"}}
	c := unmanagedTestClient(t, service, deployment)
	r := &UnmanagedReconciler{LLMInferenceServiceReconciler: &LLMInferenceServiceReconciler{Client: c}}
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(service)}
	_, err := r.Reconcile(ctx, req)
	require.NoError(t, err) // No configs, kube clientset, GIE or Istio are installed.
	got := &v1alpha1.LLMInferenceService{}
	require.NoError(t, c.Get(ctx, req.NamespacedName, got))
	require.Equal(t, service.Spec, got.Spec)
	require.Equal(t, service.Finalizers, got.Finalizers)
	require.EqualValues(t, 2, got.Status.ObservedGeneration)
	for _, condition := range got.Status.Conditions {
		require.Equal(t, corev1.ConditionUnknown, condition.Status)
		require.Equal(t, "Unmanaged", condition.Reason)
	}
	version := got.ResourceVersion
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	require.NoError(t, c.Get(ctx, req.NamespacedName, got))
	require.Equal(t, version, got.ResourceVersion, "repeated reconciliation must not write unchanged status")
	gotDeployment := &appsv1.Deployment{}
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(deployment), gotDeployment))
	require.Equal(t, deployment.Spec, gotDeployment.Spec)
}

func TestUnmanagedDoesNotProvisionPendingService(t *testing.T) {
	ctx := t.Context()
	service := &v1alpha1.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{Name: "pending", Namespace: "models"}}
	c := unmanagedTestClient(t, service)
	r := &UnmanagedReconciler{LLMInferenceServiceReconciler: &LLMInferenceServiceReconciler{Client: c}}
	_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(service)})
	require.NoError(t, err)
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(service), service))
	require.Empty(t, service.Finalizers)
	deployments := &appsv1.DeploymentList{}
	require.NoError(t, c.List(ctx, deployments))
	require.Empty(t, deployments.Items)
	_, err = r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "missing", Namespace: "models"}})
	require.NoError(t, err)
}

func TestUnmanagedDeletion(t *testing.T) {
	for _, anotherService := range []bool{false, true} {
		t.Run(map[bool]string{false: "last-service", true: "monitoring-still-in-use"}[anotherService], func(t *testing.T) {
			ctx := t.Context()
			service := &v1alpha1.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{
				Name: "existing", Namespace: "models", DeletionTimestamp: &metav1.Time{Time: metav1.Now().Time},
				Finalizers: []string{constants.KServeAPIGroupName + "/llmisvc-finalizer", "customer.example/finalizer"},
			}}
			legacy := &LLMInferenceServiceReconciler{}
			binding := legacy.expectedSchedulerAuthDelegatorBinding(service, legacy.expectedSchedulerServiceAccount(service))
			metricsBinding := legacy.expectedMetricsReaderClusterRoleBinding(service)
			objects := []client.Object{service, binding, metricsBinding}
			if anotherService {
				objects = append(objects, &v1alpha1.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "models"}})
			}
			c := unmanagedTestClient(t, objects...)
			r := &UnmanagedReconciler{LLMInferenceServiceReconciler: &LLMInferenceServiceReconciler{Client: c, EventRecorder: record.NewFakeRecorder(10)}}
			_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(service)})
			require.NoError(t, err)
			require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(service), service))
			require.Equal(t, []string{"customer.example/finalizer"}, service.Finalizers)
			require.True(t, apierrors.IsNotFound(c.Get(ctx, client.ObjectKeyFromObject(binding), &rbacv1.ClusterRoleBinding{})))
			err = c.Get(ctx, client.ObjectKeyFromObject(metricsBinding), &rbacv1.ClusterRoleBinding{})
			if anotherService {
				require.NoError(t, err)
			} else {
				require.True(t, apierrors.IsNotFound(err))
			}
		})
	}
}

func TestUnmanagedDeletionWithoutMonitoringAPIs(t *testing.T) {
	ctx := t.Context()
	service := &v1alpha1.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{
		Name: "existing", Namespace: "models", DeletionTimestamp: &metav1.Time{Time: metav1.Now().Time},
		Finalizers: []string{constants.KServeAPIGroupName + "/llmisvc-finalizer", "customer.example/finalizer"},
	}}
	c := unmanagedTestClient(t, service)
	wrapped := interceptor.NewClient(c, interceptor.Funcs{
		Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			switch obj.(type) {
			case *monitoringv1.PodMonitor, *monitoringv1.ServiceMonitor:
				return &meta.NoKindMatchError{GroupKind: schema.GroupKind{Group: "monitoring.coreos.com", Kind: "MissingMonitor"}}
			default:
				return c.Delete(ctx, obj, opts...)
			}
		},
	})
	r := &UnmanagedReconciler{LLMInferenceServiceReconciler: &LLMInferenceServiceReconciler{Client: wrapped, EventRecorder: record.NewFakeRecorder(10)}}
	_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(service)})
	require.NoError(t, err)
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(service), service))
	require.Equal(t, []string{"customer.example/finalizer"}, service.Finalizers)
}

func TestUnmanagedCleanupFailureRetainsFinalizer(t *testing.T) {
	ctx := t.Context()
	service := &v1alpha1.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{
		Name: "existing", Namespace: "models", DeletionTimestamp: &metav1.Time{Time: metav1.Now().Time},
		Finalizers: []string{constants.KServeAPIGroupName + "/llmisvc-finalizer"},
	}}
	c := unmanagedTestClient(t, service)
	wrapped := interceptor.NewClient(c, interceptor.Funcs{
		Delete: func(context.Context, client.WithWatch, client.Object, ...client.DeleteOption) error {
			return errors.New("cleanup unavailable")
		},
	})
	r := &UnmanagedReconciler{LLMInferenceServiceReconciler: &LLMInferenceServiceReconciler{Client: wrapped}}
	_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(service)})
	require.ErrorContains(t, err, "cleanup unavailable")
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(service), service))
	require.Contains(t, service.Finalizers, constants.KServeAPIGroupName+"/llmisvc-finalizer")
}

func TestUnmanagedDeletionUsesStableBindingName(t *testing.T) {
	for _, account := range []string{"", "different-from-applied-account"} {
		t.Run("spec-account="+account, func(t *testing.T) {
			ctx := t.Context()
			service := &v1alpha1.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{
				Name: "existing", Namespace: "models", DeletionTimestamp: &metav1.Time{Time: metav1.Now().Time},
				Finalizers: []string{constants.KServeAPIGroupName + "/llmisvc-finalizer", "customer.example/finalizer"},
			}}
			// The applied account can have come from a now-missing base config or
			// differ from the spec. Binding deletion depends only on the stable name.
			service.Spec.BaseRefs = []corev1.LocalObjectReference{{Name: "deleted-base-config"}}
			service.Spec.Router = &v1alpha1.RouterSpec{Scheduler: &v1alpha1.SchedulerSpec{
				Template: &corev1.PodSpec{ServiceAccountName: account},
			}}
			legacy := &LLMInferenceServiceReconciler{}
			sa := legacy.expectedSchedulerServiceAccount(service)
			sa.Name = "actually-deployed-account"
			binding := legacy.expectedSchedulerAuthDelegatorBinding(service, sa)
			c := unmanagedTestClient(t, service, binding, sa)
			r := &UnmanagedReconciler{LLMInferenceServiceReconciler: &LLMInferenceServiceReconciler{Client: c, EventRecorder: record.NewFakeRecorder(10)}}
			_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(service)})
			require.NoError(t, err)
			require.True(t, apierrors.IsNotFound(c.Get(ctx, client.ObjectKeyFromObject(binding), &rbacv1.ClusterRoleBinding{})))
			require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(service), service))
			require.Equal(t, []string{"customer.example/finalizer"}, service.Finalizers)
			require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(sa), &corev1.ServiceAccount{}), "owned accounts are left to garbage collection")
		})
	}
}
