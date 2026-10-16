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
	"fmt"
	"reflect"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/kserve/kserve/pkg/constants"
)

const (
	CABundleConfigMapName    = "odh-kserve-custom-ca-bundle"
	localModelDeploymentName = "kserve-localmodel-controller-manager"
)

// CABundleReconciler maintains the Job-mountable copy of the applications
// namespace bundle. Kubernetes ConfigMap volumes cannot cross namespaces.
type CABundleReconciler struct {
	Client          client.Client
	Reader          client.Reader
	Scheme          *runtime.Scheme
	SourceNamespace string
	TargetNamespace string
}

func (r *CABundleReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.SourceNamespace == r.TargetNamespace {
		return nil
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.ConfigMap{}, builder.WithPredicates(predicate.NewPredicateFuncs(r.isBundle))).
		Complete(r)
}

func (r *CABundleReconciler) isBundle(obj client.Object) bool {
	return obj.GetName() == CABundleConfigMapName &&
		(obj.GetNamespace() == r.SourceNamespace || obj.GetNamespace() == r.TargetNamespace)
}

func (r *CABundleReconciler) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	if r.SourceNamespace == r.TargetNamespace {
		return ctrl.Result{}, nil
	}
	reader := r.Reader
	if reader == nil {
		reader = r.Client
	}
	source := &corev1.ConfigMap{}
	sourceKey := types.NamespacedName{Namespace: r.SourceNamespace, Name: CABundleConfigMapName}
	sourceErr := reader.Get(ctx, sourceKey, source)
	if sourceErr != nil && !apierrors.IsNotFound(sourceErr) {
		return ctrl.Result{}, fmt.Errorf("reading source CA bundle %s: %w", sourceKey, sourceErr)
	}
	bundle := ""
	if sourceErr == nil {
		bundle = source.Data[constants.DefaultCaBundleFileName]
	}
	if strings.TrimSpace(bundle) == "" {
		bundle = ""
	}

	target := &corev1.ConfigMap{}
	targetKey := types.NamespacedName{Namespace: r.TargetNamespace, Name: CABundleConfigMapName}
	targetErr := reader.Get(ctx, targetKey, target)
	if targetErr != nil && !apierrors.IsNotFound(targetErr) {
		return ctrl.Result{}, fmt.Errorf("reading target CA bundle %s: %w", targetKey, targetErr)
	}
	if targetErr == nil && !isManagedBundle(target) {
		return ctrl.Result{}, fmt.Errorf("CA bundle ConfigMap %s exists without LocalModel Deployment ownership", targetKey)
	}
	if bundle == "" {
		if targetErr == nil {
			if err := r.Client.Delete(ctx, target); client.IgnoreNotFound(err) != nil {
				return ctrl.Result{}, fmt.Errorf("deleting target CA bundle %s: %w", targetKey, err)
			}
		}
		return ctrl.Result{}, nil
	}

	deployment := &appsv1.Deployment{}
	deploymentKey := types.NamespacedName{Namespace: r.TargetNamespace, Name: localModelDeploymentName}
	if err := reader.Get(ctx, deploymentKey, deployment); err != nil {
		return ctrl.Result{}, fmt.Errorf("reading LocalModel Deployment %s: %w", deploymentKey, err)
	}
	if targetErr != nil {
		target = &corev1.ConfigMap{}
		target.Namespace = r.TargetNamespace
		target.Name = CABundleConfigMapName
		target.Data = map[string]string{constants.DefaultCaBundleFileName: bundle}
		if err := controllerutil.SetControllerReference(deployment, target, r.Scheme, controllerutil.WithBlockOwnerDeletion(false)); err != nil {
			return ctrl.Result{}, fmt.Errorf("setting target CA bundle owner: %w", err)
		}
		if err := r.Client.Create(ctx, target); err != nil {
			return ctrl.Result{}, fmt.Errorf("creating target CA bundle %s: %w", targetKey, err)
		}
		return ctrl.Result{}, nil
	}

	before := target.DeepCopy()
	target.Data = map[string]string{constants.DefaultCaBundleFileName: bundle}
	target.BinaryData = nil
	if err := controllerutil.SetControllerReference(deployment, target, r.Scheme, controllerutil.WithBlockOwnerDeletion(false)); err != nil {
		return ctrl.Result{}, fmt.Errorf("setting target CA bundle owner: %w", err)
	}
	if !bundleEqual(before, target) {
		if err := r.Client.Patch(ctx, target, client.MergeFrom(before)); err != nil {
			return ctrl.Result{}, fmt.Errorf("updating target CA bundle %s: %w", targetKey, err)
		}
	}
	return ctrl.Result{}, nil
}

func isManagedBundle(cm *corev1.ConfigMap) bool {
	for _, owner := range cm.OwnerReferences {
		if owner.APIVersion == appsv1.SchemeGroupVersion.String() && owner.Kind == "Deployment" &&
			owner.Name == localModelDeploymentName && owner.Controller != nil && *owner.Controller {
			return true
		}
	}
	return false
}

func bundleEqual(a, b *corev1.ConfigMap) bool {
	return reflect.DeepEqual(a.Data, b.Data) &&
		reflect.DeepEqual(a.BinaryData, b.BinaryData) &&
		reflect.DeepEqual(a.OwnerReferences, b.OwnerReferences)
}
