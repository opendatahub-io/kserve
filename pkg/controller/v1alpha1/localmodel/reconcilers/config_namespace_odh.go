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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
	"github.com/kserve/kserve/pkg/apis/serving/v1beta1"
	"github.com/kserve/kserve/pkg/constants"
	controllerutils "github.com/kserve/kserve/pkg/controller/v1alpha1/utils"
)

func inferenceServiceConfigNamespace() string {
	return controllerutils.InferenceServiceConfigNamespace()
}

func getInferenceServiceConfigMap(ctx context.Context, clientset kubernetes.Interface) (*corev1.ConfigMap, error) {
	return v1beta1.GetInferenceServiceConfigMapInNamespace(ctx, clientset, inferenceServiceConfigNamespace())
}

func (c *LocalModelReconciler) configMapFunc(ctx context.Context, obj client.Object) []reconcile.Request {
	if obj.GetName() != constants.InferenceServiceConfigMapName || obj.GetNamespace() != inferenceServiceConfigNamespace() {
		return nil
	}
	localModels := &v1alpha1.LocalModelCacheList{}
	if err := c.List(ctx, localModels); err != nil {
		c.Log.Error(err, "unable to list LocalModelCache resources for config update")
		return nil
	}
	requests := make([]reconcile.Request, 0, len(localModels.Items))
	for index := range localModels.Items {
		requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&localModels.Items[index])})
	}
	return requests
}

func (c *LocalModelNamespaceCacheReconciler) configMapFuncNamespaceCache(ctx context.Context, obj client.Object) []reconcile.Request {
	if obj.GetName() != constants.InferenceServiceConfigMapName || obj.GetNamespace() != inferenceServiceConfigNamespace() {
		return nil
	}
	localModels := &v1alpha1.LocalModelNamespaceCacheList{}
	if err := c.List(ctx, localModels); err != nil {
		c.Log.Error(err, "unable to list LocalModelNamespaceCache resources for config update")
		return nil
	}
	requests := make([]reconcile.Request, 0, len(localModels.Items))
	for index := range localModels.Items {
		requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&localModels.Items[index])})
	}
	return requests
}

func (c *LocalModelReconciler) setupConfigMapWatch(b *builder.Builder) {
	b.Watches(&corev1.ConfigMap{}, handler.EnqueueRequestsFromMapFunc(c.configMapFunc), builder.WithPredicates(predicate.NewPredicateFuncs(func(obj client.Object) bool {
		return obj.GetName() == constants.InferenceServiceConfigMapName && obj.GetNamespace() == inferenceServiceConfigNamespace()
	})))
}

func (c *LocalModelNamespaceCacheReconciler) setupNamespaceCacheConfigMapWatch(b *builder.Builder) {
	b.Watches(&corev1.ConfigMap{}, handler.EnqueueRequestsFromMapFunc(c.configMapFuncNamespaceCache), builder.WithPredicates(predicate.NewPredicateFuncs(func(obj client.Object) bool {
		return obj.GetName() == constants.InferenceServiceConfigMapName && obj.GetNamespace() == inferenceServiceConfigNamespace()
	})))
}
