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
	"fmt"

	routev1 "github.com/openshift/api/route/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/kserve/kserve/pkg/utils"
)

// extendControllerSetup watches the OpenShift resources the distro build manages for
// InferenceServices: Routes, so the status URL follows admission, and auth-delegator
// ClusterRoleBindings, so a deleted or edited binding is restored.
func (r *InferenceServiceReconciler) extendControllerSetup(mgr manager.Manager, b *builder.Builder) error {
	b.Watches(&rbacv1.ClusterRoleBinding{},
		handler.EnqueueRequestsFromMapFunc(r.authDelegatorBindingUsers),
		builder.WithPredicates(authDelegatorBindingChanged()))

	if err := routev1.Install(mgr.GetScheme()); err != nil {
		return fmt.Errorf("failed to add Route v1 APIs to scheme: %w", err)
	}
	routeFound, err := utils.IsCrdAvailable(mgr.GetConfig(), routev1.GroupVersion.String(), "Route")
	if err != nil {
		return err
	}
	if !routeFound {
		r.Log.Info("The InferenceService controller won't watch route.openshift.io/v1/Route resources because the CRD is not available.")
		return nil
	}
	// A Route is mapped by name rather than owner, so a Route the InferenceService does not
	// control still re-reconciles it when it changes or goes away.
	b.Watches(&routev1.Route{}, handler.EnqueueRequestsFromMapFunc(sameNameInferenceService))
	return nil
}

func sameNameInferenceService(_ context.Context, obj client.Object) []reconcile.Request {
	return []reconcile.Request{{NamespacedName: client.ObjectKeyFromObject(obj)}}
}
