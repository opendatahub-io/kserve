package kservemodule

import (
	"context"
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
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
	if err := r.deleteLeftoverWVAController(ctx); err != nil {
		return err
	}
	if err := r.deleteVariantAutoscalingCRs(ctx); err != nil {
		return err
	}
	if err := r.deleteVariantAutoscalingCRD(ctx); err != nil {
		return err
	}
	return r.stripWVAAutoscalingConfig(ctx)
}

func (r *KserveModuleReconciler) deleteLeftoverWVAController(ctx context.Context) error {
	// Force-delete: 3.5 leftovers were applied by the WVA overlay, so they lack
	// platform.opendatahub.io/part-of=kserve. After stop-clone, defaultCleanup
	// has no overlay to render and no-ops ("manifest directory not found").
	deploy := &appsv1.Deployment{}
	key := client.ObjectKey{Name: wvaControllerDeployment, Namespace: r.getApplicationsNamespace()}
	if err := r.Get(ctx, key, deploy); err != nil {
		if client.IgnoreNotFound(err) == nil {
			return nil
		}
		return fmt.Errorf("getting leftover WVA Deployment %s: %w", key, err)
	}
	if err := r.Delete(ctx, deploy); err != nil {
		if client.IgnoreNotFound(err) == nil {
			return nil
		}
		return fmt.Errorf("deleting leftover WVA Deployment %s: %w", key, err)
	}
	ctrl.LoggerFrom(ctx).Info("deleted leftover WVA controller Deployment", "namespace", key.Namespace, "name", key.Name)
	return nil
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
	// Force-delete: leftover 3.5 CRDs were applied by the WVA overlay, not by
	// this module, so they lack platform.opendatahub.io/part-of=kserve.
	// deleteResourceIfPresent would skip them.
	crd := &apiextensionsv1.CustomResourceDefinition{}
	key := client.ObjectKey{Name: variantAutoscalingCRDName}
	if err := r.Get(ctx, key, crd); err != nil {
		if client.IgnoreNotFound(err) == nil {
			return nil
		}
		return fmt.Errorf("getting %s: %w", variantAutoscalingCRDName, err)
	}
	if err := r.Delete(ctx, crd); err != nil {
		if client.IgnoreNotFound(err) == nil {
			return nil
		}
		return fmt.Errorf("deleting %s: %w", variantAutoscalingCRDName, err)
	}
	ctrl.LoggerFrom(ctx).Info("deleted leftover VariantAutoscaling CRD", "name", variantAutoscalingCRDName)
	return nil
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
