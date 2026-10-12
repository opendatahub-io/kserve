package kservemodule

import (
	"context"
	"fmt"
	"slices"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const modelExpressAuthDelegatorFinalizer = "modelexpress.opendatahub.io/auth-delegator"

var modelExpressServerListGVK = schema.GroupVersionKind{
	Group:   "modelexpress.opendatahub.io",
	Version: "v1alpha1",
	Kind:    "ModelExpressServerList",
}

// modelExpressRemovalBlockers returns namespace/name of every ModelExpressServer
// that still holds the operator's auth-delegator finalizer.
func modelExpressRemovalBlockers(ctx context.Context, r *KserveModuleReconciler) ([]string, error) {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(modelExpressServerListGVK)
	if err := r.List(ctx, list); err != nil {
		if meta.IsNoMatchError(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("listing ModelExpressServers: %w", err)
	}

	var blockers []string
	for i := range list.Items {
		if slices.Contains(list.Items[i].GetFinalizers(), modelExpressAuthDelegatorFinalizer) {
			blockers = append(blockers, list.Items[i].GetNamespace()+"/"+list.Items[i].GetName())
		}
	}
	slices.Sort(blockers)
	return blockers, nil
}
