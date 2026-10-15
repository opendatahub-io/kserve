package kservemodule

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// These installation defaults are administrator-owned. Preserve the raw value,
// including invalid JSON, so reconciliation does not conceal or discard an edit.
func (r *KserveModuleReconciler) preserveConfidentialDefaults(ctx context.Context, resources []unstructured.Unstructured) ([]unstructured.Unstructured, error) {
	const key = "confidentialBareMetal"
	for i := range resources {
		obj := &resources[i]
		if obj.GroupVersionKind() != configMapGVK || obj.GetName() != kserveConfigMapName {
			continue
		}
		live := &corev1.ConfigMap{}
		if err := r.Get(ctx, client.ObjectKeyFromObject(obj), live); err != nil {
			if apierrors.IsNotFound(err) {
				return resources, nil
			}
			return nil, fmt.Errorf("read confidential installation defaults: %w", err)
		}
		if raw, exists := live.Data[key]; exists {
			if err := unstructured.SetNestedField(obj.Object, raw, "data", key); err != nil {
				return nil, err
			}
		}
		break
	}
	return resources, nil
}
