//go:build !distro

/*
Copyright 2021 The KServe Authors.

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
	"reflect"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
	"github.com/kserve/kserve/pkg/apis/serving/v1beta1"
	"github.com/kserve/kserve/pkg/constants"
)

func (r *InferenceServiceReconciler) clusterServingRuntimeFunc(ctx context.Context, obj client.Object) []reconcile.Request {
	clusterServingRuntimeObj, ok := obj.(*v1alpha1.ClusterServingRuntime)

	if !ok || clusterServingRuntimeObj == nil {
		return nil
	}

	var isvcList v1beta1.InferenceServiceList
	if err := r.List(ctx, &isvcList, client.InNamespace(clusterServingRuntimeObj.Namespace)); err != nil {
		r.Log.Error(err, "unable to list InferenceServices", "clusterServingRuntime", clusterServingRuntimeObj.Name)
		return nil
	}

	requests := make([]reconcile.Request, 0, len(isvcList.Items))
	for _, isvc := range isvcList.Items {
		annotations := isvc.GetAnnotations()
		if annotations != nil {
			if disableAutoUpdate, found := annotations[constants.DisableAutoUpdateAnnotationKey]; found && disableAutoUpdate == "true" && isvc.Status.IsReady() {
				r.Log.Info("Auto-update is disabled for InferenceService", "InferenceService", isvc.Name)
				continue
			}
		}
		if isvc.Status.ClusterServingRuntimeName == clusterServingRuntimeObj.Name {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: isvc.Namespace,
					Name:      isvc.Name,
				},
			})
		}
	}
	return requests
}

// csrFieldIndexValues returns field index values for ClusterServingRuntime name.
func csrFieldIndexValues(isvc *v1beta1.InferenceService) []string {
	if isvc.Status.ClusterServingRuntimeName != "" {
		return []string{isvc.Status.ClusterServingRuntimeName}
	}
	return nil
}

// setupCSRWatch adds a watch for ClusterServingRuntime changes.
func setupCSRWatch(ctrlBuilder *builder.Builder, r *InferenceServiceReconciler) *builder.Builder {
	return ctrlBuilder.Watches(&v1alpha1.ClusterServingRuntime{}, handler.EnqueueRequestsFromMapFunc(r.clusterServingRuntimeFunc),
		builder.WithPredicates(predicate.Funcs{
			UpdateFunc: func(e event.UpdateEvent) bool {
				oldClusterServingRuntime := e.ObjectOld.(*v1alpha1.ClusterServingRuntime)
				newClusterServingRuntime := e.ObjectNew.(*v1alpha1.ClusterServingRuntime)
				return !reflect.DeepEqual(oldClusterServingRuntime.Spec, newClusterServingRuntime.Spec)
			},
			CreateFunc:  func(e event.CreateEvent) bool { return false },
			DeleteFunc:  func(e event.DeleteEvent) bool { return false },
			GenericFunc: func(e event.GenericEvent) bool { return false },
		}))
}
