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
	"errors"
	"fmt"
	"maps"
	"strings"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"knative.dev/pkg/kmeta"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/yaml"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
	"github.com/kserve/kserve/pkg/constants"
)

const (
	confidentialManagedLabel         = "serving.kserve.io/confidential-bare-metal"
	confidentialServiceUIDAnnotation = "serving.kserve.io/confidential-service-uid"
)

func (r *LLMISVCReconciler) reconcileConfidentialHelpers(ctx context.Context, namespace string) error {
	bundle := &corev1.ConfigMap{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: constants.KServeNamespace, Name: confidentialHelpersName}, bundle); err != nil {
		return fmt.Errorf("load confidential helpers: %w", err)
	}
	sa := &corev1.ServiceAccount{}
	binding := &rbacv1.RoleBinding{}
	if err := yaml.Unmarshal([]byte(bundle.Data["service-account.yaml"]), sa); err != nil {
		return err
	}
	if err := yaml.Unmarshal([]byte(bundle.Data["role-binding.yaml"]), binding); err != nil {
		return err
	}
	if sa.Name == "" || binding.Name == "" || bundle.Data["link.sh"] == "" || bundle.Data["format-disk.sh"] == "" {
		return errors.New("installed confidential helper definitions are incomplete")
	}
	for i := range binding.Subjects {
		binding.Subjects[i].Namespace = namespace
	}
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "cc-storage-link"}, Data: map[string]string{
		"link.sh": bundle.Data["link.sh"], "format-disk.sh": bundle.Data["format-disk.sh"],
	}}
	for _, expected := range []client.Object{cm, sa, binding} {
		expected.SetNamespace(namespace)
		// Shared helpers have no service owner: one service cannot garbage-collect another's dependencies.
		current := expected.DeepCopyObject().(client.Object)
		_, err := controllerutil.CreateOrUpdate(ctx, r.Client, current, func() error {
			if current.GetResourceVersion() != "" && current.GetLabels()[confidentialManagedLabel] != "true" {
				return fmt.Errorf("confidential helper %s/%s already exists and is not managed by KServe", namespace, current.GetName())
			}
			labels := maps.Clone(current.GetLabels())
			if labels == nil {
				labels = map[string]string{}
			}
			labels[confidentialManagedLabel] = "true"
			labels[constants.KubernetesPartOfLabelKey] = constants.LLMInferenceServicePartOfValue
			current.SetLabels(labels)
			current.SetOwnerReferences(nil)
			switch obj := current.(type) {
			case *corev1.ConfigMap:
				obj.Data = cm.Data
			case *corev1.ServiceAccount:
				obj.AutomountServiceAccountToken = sa.AutomountServiceAccountToken
			case *rbacv1.RoleBinding:
				obj.RoleRef = binding.RoleRef
				obj.Subjects = binding.Subjects
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("reconcile confidential helper %s/%s: %w", namespace, expected.GetName(), err)
		}
	}
	return nil
}

func (r *LLMISVCReconciler) reconcileConfidentialPVC(ctx context.Context, svc *v1alpha2.LLMInferenceService, cfg *confidentialDefaults) error {
	volume := confidentialBlockVolume(svc.Spec.Template)
	if volume == nil {
		return errors.New("the confidential bare-metal preset requires the luks-block PVC volume")
	}
	generatedName := kmeta.ChildName(svc.Name, "-confidential-storage")
	automatic := volume.ClaimName == generatedName || volume.ClaimName == ""
	if automatic {
		volume.ClaimName = generatedName
	}
	claim := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Namespace: svc.Namespace, Name: volume.ClaimName}}
	err := r.Get(ctx, client.ObjectKeyFromObject(claim), claim)
	if apierrors.IsNotFound(err) && automatic {
		sc := &storagev1.StorageClass{}
		if err := r.Get(ctx, client.ObjectKey{Name: cfg.StorageClassName}, sc); err != nil {
			return fmt.Errorf("get confidential StorageClass: %w", err)
		}
		if strings.HasPrefix(sc.Provisioner, "kubernetes.io/") {
			return fmt.Errorf("confidential storage requires a CSI StorageClass supporting raw block and ReadWriteOncePod; %s uses %s", sc.Name, sc.Provisioner)
		}
		claim.Labels = map[string]string{confidentialManagedLabel: "true", constants.KubernetesPartOfLabelKey: constants.LLMInferenceServicePartOfValue}
		claim.Annotations = map[string]string{confidentialServiceUIDAnnotation: string(svc.UID)}
		claim.Spec = corev1.PersistentVolumeClaimSpec{
			StorageClassName: ptr.To(cfg.StorageClassName), VolumeMode: ptr.To(corev1.PersistentVolumeBlock),
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOncePod},
			Resources:   corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: cfg.StorageSize}},
		}
		// Deliberately no owner reference: retain encrypted data after service deletion.
		if err = r.Create(ctx, claim); apierrors.IsAlreadyExists(err) {
			err = r.Get(ctx, client.ObjectKeyFromObject(claim), claim)
		}
	}
	if err != nil {
		return fmt.Errorf("get or create encrypted PVC %s/%s: %w", svc.Namespace, volume.ClaimName, err)
	}
	if automatic && claim.Annotations[confidentialServiceUIDAnnotation] != string(svc.UID) {
		original := &v1alpha2.LLMInferenceService{}
		if err := r.Get(ctx, client.ObjectKeyFromObject(svc), original); err != nil {
			return err
		}
		var explicit *corev1.PersistentVolumeClaimVolumeSource
		if original.Spec.Template != nil {
			explicit = confidentialBlockVolume(original.Spec.Template)
		}
		if explicit == nil || explicit.ClaimName != claim.Name {
			return fmt.Errorf("retained PVC %s belongs to another service instance; explicitly reference it in the luks-block volume to reuse its data", claim.Name)
		}
	}
	size := claim.Spec.Resources.Requests[corev1.ResourceStorage]
	if claim.DeletionTimestamp != nil || ptr.Deref(claim.Spec.VolumeMode, corev1.PersistentVolumeFilesystem) != corev1.PersistentVolumeBlock ||
		len(claim.Spec.AccessModes) != 1 || claim.Spec.AccessModes[0] != corev1.ReadWriteOncePod || size.Cmp(cfg.StorageSize) < 0 ||
		(automatic && ptr.Deref(claim.Spec.StorageClassName, "") != cfg.StorageClassName) {
		return fmt.Errorf("PVC %s is incompatible: requires a non-terminating raw block ReadWriteOncePod claim with at least %s capacity and, for an automatically created claim, StorageClass %s; its data was left intact", claim.Name, cfg.StorageSize.String(), cfg.StorageClassName)
	}
	// Pending is allowed: WaitForFirstConsumer provisioning needs the Deployment first.
	return nil
}
