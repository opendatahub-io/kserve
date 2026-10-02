package kservemodule

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var variantAutoscalingGVK = schema.GroupVersionKind{
	Group:   "llmd.ai",
	Version: "v1alpha1",
	Kind:    "VariantAutoscaling",
}

func cleanupWVAComponent(ctx context.Context, r *KserveModuleReconciler) error {
	if err := r.deleteVariantAutoscalingCRs(ctx); err != nil {
		return err
	}
	if err := r.deleteVariantAutoscalingCRD(ctx); err != nil {
		return err
	}
	return r.stripWVAAutoscalingConfig(ctx)
}

func (r *KserveModuleReconciler) deleteVariantAutoscalingCRs(ctx context.Context) error {
	log := ctrl.LoggerFrom(ctx)

	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(variantAutoscalingGVK.GroupVersion().WithKind(variantAutoscalingGVK.Kind + "List"))
	if err := r.List(ctx, list); err != nil {
		if meta.IsNoMatchError(err) || client.IgnoreNotFound(err) == nil {
			return nil
		}
		return fmt.Errorf("listing VariantAutoscaling: %w", err)
	}

	var errs []string
	for i := range list.Items {
		va := &list.Items[i]
		if err := r.Delete(ctx, va); err != nil {
			if client.IgnoreNotFound(err) == nil {
				continue
			}
			errs = append(errs, fmt.Sprintf("%s/%s: %v", va.GetNamespace(), va.GetName(), err))
			continue
		}
		log.Info("deleted leftover VariantAutoscaling", "namespace", va.GetNamespace(), "name", va.GetName())
	}
	if len(errs) > 0 {
		return fmt.Errorf("deleting VariantAutoscaling CRs: %s", strings.Join(errs, "; "))
	}
	return nil
}

func (r *KserveModuleReconciler) deleteVariantAutoscalingCRD(ctx context.Context) error {
	crd := &apiextensionsv1.CustomResourceDefinition{}
	crd.Name = variantAutoscalingCRDName
	return deleteResourceIfPresent(ctx, r.Client, crd)
}

func (r *KserveModuleReconciler) stripWVAAutoscalingConfig(ctx context.Context) error {
	ns := r.getApplicationsNamespace()
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		cm := &corev1.ConfigMap{}
		if err := r.Get(ctx, client.ObjectKey{Name: kserveConfigMapName, Namespace: ns}, cm); err != nil {
			if client.IgnoreNotFound(err) == nil {
				return nil
			}
			return fmt.Errorf("getting %s: %w", kserveConfigMapName, err)
		}
		if cm.Data == nil {
			return nil
		}
		if _, ok := cm.Data[autoscalingWVAControllerConfigKey]; !ok {
			return nil
		}
		orig := cm.DeepCopy()
		delete(cm.Data, autoscalingWVAControllerConfigKey)
		if err := r.Patch(ctx, cm, client.MergeFrom(orig)); err != nil {
			return fmt.Errorf("stripping %s from %s: %w", autoscalingWVAControllerConfigKey, kserveConfigMapName, err)
		}
		return nil
	})
}
