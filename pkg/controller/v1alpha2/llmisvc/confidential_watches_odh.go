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

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
	"github.com/kserve/kserve/pkg/constants"
)

func (r *LLMISVCReconciler) watchConfidentialDependencies(b *builder.Builder, logger logr.Logger) {
	mapper := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, object client.Object) []reconcile.Request {
		global := object.GetNamespace() == constants.KServeNamespace && (object.GetName() == confidentialHelpersName || object.GetName() == constants.InferenceServiceConfigMapName)
		_, isClaim := object.(*corev1.PersistentVolumeClaim)
		if !global && !isClaim && object.GetLabels()[confidentialManagedLabel] != "true" {
			return nil
		}
		opts := []client.ListOption{}
		if !global {
			opts = append(opts, client.InNamespace(object.GetNamespace()))
		}
		services := &v1alpha2.LLMInferenceServiceList{}
		if err := r.List(ctx, services, opts...); err != nil {
			logger.Error(err, "list confidential services")
			return nil
		}
		requests := make([]reconcile.Request, 0, len(services.Items))
		for _, svc := range services.Items {
			// Include new services that failed before a merged configuration could be recorded.
			selected := usesConfidentialBareMetal(&svc)
			for _, ref := range svc.Spec.BaseRefs {
				selected = selected || ref.Name == confidentialConfigName || ref.Name == configPrefix+confidentialConfigSuffix || ref.Name == confidentialPresetName(&svc)
			}
			if selected {
				requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&svc)})
			}
		}
		return requests
	})
	changes := predicate.Funcs{UpdateFunc: func(e event.UpdateEvent) bool {
		if oldCM, ok := e.ObjectOld.(*corev1.ConfigMap); ok {
			newCM := e.ObjectNew.(*corev1.ConfigMap)
			if isInferenceServiceConfigMap(newCM) {
				return oldCM.Data[confidentialDefaultsKey] != newCM.Data[confidentialDefaultsKey]
			}
			return !equality.Semantic.DeepEqual(oldCM.Data, newCM.Data)
		}
		return !equality.Semantic.DeepEqual(e.ObjectOld, e.ObjectNew)
	}}
	b.Watches(&corev1.ConfigMap{}, mapper, builder.WithPredicates(changes))
	b.Watches(&corev1.ServiceAccount{}, mapper, builder.WithPredicates(changes))
	b.Watches(&rbacv1.RoleBinding{}, mapper, builder.WithPredicates(changes))
	b.Watches(&corev1.PersistentVolumeClaim{}, mapper, builder.WithPredicates(changes))
}
