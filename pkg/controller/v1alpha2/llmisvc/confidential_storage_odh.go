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
	"path/filepath"
	"strings"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/utils/ptr"
	"knative.dev/pkg/kmeta"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
	"github.com/kserve/kserve/pkg/constants"
)

const confidentialServiceNameAnnotation = "serving.kserve.io/confidential-service-name"

func validateConfidentialStorageDefaults(cfg *confidentialDefaults) error {
	if cfg.StorageSize.Sign() < 0 {
		return errors.New("storageSize must not be negative")
	}
	if cfg.LUKSResourceID != "" {
		if err := validateConfidentialDiskKey(cfg.LUKSResourceID); err != nil {
			return err
		}
	}
	names, devices := map[string]bool{}, map[string]bool{}
	for _, disk := range cfg.LocalVolumes {
		if len(validation.IsDNS1123Subdomain(disk.Name)) != 0 || len(validation.IsDNS1123Label(disk.Namespace)) != 0 ||
			len(validation.IsDNS1123Subdomain(disk.NodeName)) != 0 || disk.Capacity.Sign() <= 0 ||
			!strings.HasPrefix(disk.DevicePath, "/dev/") || filepath.Clean(disk.DevicePath) != disk.DevicePath || disk.DevicePath == "/dev/" {
			return fmt.Errorf("localVolumes entry %q requires a valid name, namespace, nodeName, /dev/ devicePath, and positive capacity", disk.Name)
		}
		device := disk.NodeName + ":" + disk.DevicePath
		if names[disk.Name] || devices[device] {
			return fmt.Errorf("duplicate localVolumes name or device: %s", disk.Name)
		}
		names[disk.Name], devices[device] = true, true
		if disk.LUKSResourceID != "" {
			if err := validateConfidentialDiskKey(disk.LUKSResourceID); err != nil {
				return fmt.Errorf("localVolumes entry %s: %w", disk.Name, err)
			}
		}
	}
	return nil
}

func validateConfidentialDiskKey(key string) error {
	path, ok := strings.CutPrefix(key, "kbs:///")
	parts := strings.Split(path, "/")
	if !ok || len(parts) != 3 || strings.ContainsAny(path, " \t\r\n?#") {
		return errors.New("luksResourceId must identify the disk key as kbs:///repository/type/tag")
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return errors.New("luksResourceId must identify the disk key as kbs:///repository/type/tag")
		}
	}
	return nil
}

func confidentialDiskKey(cfg *confidentialDefaults, pv *corev1.PersistentVolume, namespace string) error {
	if pv != nil {
		for _, disk := range cfg.LocalVolumes {
			if disk.Name == pv.Name && disk.Namespace == namespace && disk.LUKSResourceID != "" {
				cfg.LUKSResourceID = disk.LUKSResourceID
				break
			}
		}
	}
	if cfg.LUKSResourceID == "" {
		return errors.New("configure luksResourceId for the disk in inferenceservice-config.data.confidentialBareMetal; the disk key cannot be inferred from its PV")
	}
	return validateConfidentialDiskKey(cfg.LUKSResourceID)
}

func (r *LLMISVCReconciler) reconcileConfidentialPVC(ctx context.Context, svc *v1alpha2.LLMInferenceService, cfg *confidentialDefaults) error {
	volume := confidentialBlockVolume(svc.Spec.Template)
	if volume == nil {
		return errors.New("the confidential bare-metal preset requires the luks-block PVC volume")
	}
	if volume.ReadOnly {
		return errors.New("the luks-block PVC must allow writes")
	}
	generatedName := kmeta.ChildName(svc.Name, "-confidential-storage")
	automatic := volume.ClaimName == generatedName || volume.ClaimName == ""
	if automatic {
		volume.ClaimName = generatedName
	}
	selectedPV := svc.Annotations[confidentialPVAnnotation]
	if selectedPV != "" && !automatic {
		return errors.New("select either an existing luks-block claim or confidential-pv, not both")
	}
	claim := &corev1.PersistentVolumeClaim{}
	key := client.ObjectKey{Namespace: svc.Namespace, Name: volume.ClaimName}
	err := r.Get(ctx, key, claim)
	existing := err == nil
	if apierrors.IsNotFound(err) && automatic {
		claim, err = r.createConfidentialClaim(ctx, svc, cfg, generatedName, selectedPV)
	}
	if err != nil {
		return fmt.Errorf("get or create encrypted PVC %s: %w", key, err)
	}
	if selectedPV != "" && claim.Spec.VolumeName != selectedPV {
		return fmt.Errorf("PVC %s already selects PV %q, not %q; its binding was left intact", claim.Name, claim.Spec.VolumeName, selectedPV)
	}
	explicit := !automatic
	if automatic && (existing || claim.Annotations[confidentialServiceUIDAnnotation] != string(svc.UID)) {
		explicit, err = r.confidentialExplicitClaim(ctx, svc, claim.Name)
		if err != nil {
			return err
		}
		if claim.Annotations[confidentialServiceUIDAnnotation] != string(svc.UID) && !explicit {
			return fmt.Errorf("retained PVC %s belongs to another service instance; explicitly reference it in the luks-block volume to reuse its data", claim.Name)
		}
	}
	size := claim.Spec.Resources.Requests[corev1.ResourceStorage]
	minimum := cfg.MinimumSize
	if !explicit && minimum.IsZero() {
		minimum = cfg.StorageSize
	}
	if claim.DeletionTimestamp != nil || ptr.Deref(claim.Spec.VolumeMode, corev1.PersistentVolumeFilesystem) != corev1.PersistentVolumeBlock ||
		len(claim.Spec.AccessModes) != 1 || size.Sign() <= 0 || size.Cmp(minimum) < 0 {
		return fmt.Errorf("PVC %s is incompatible: requires a non-terminating raw block claim with at least %s capacity; its data was left intact", claim.Name, minimum.String())
	}
	local := claim.Spec.AccessModes[0] == corev1.ReadWriteOnce
	if !local && claim.Spec.AccessModes[0] != corev1.ReadWriteOncePod {
		return fmt.Errorf("PVC %s requires ReadWriteOncePod, or ReadWriteOnce backed by a local block PV", claim.Name)
	}
	var pv *corev1.PersistentVolume
	if claim.Spec.VolumeName != "" {
		pv = &corev1.PersistentVolume{}
		if err := r.Get(ctx, client.ObjectKey{Name: claim.Spec.VolumeName}, pv); err != nil {
			return fmt.Errorf("read PVC %s backing PV: %w", claim.Name, err)
		}
		if local && claim.Status.Phase != corev1.ClaimBound {
			if err := validateConfidentialLocalPV(pv); err != nil {
				return err
			}
			ref := pv.Spec.ClaimRef
			if ref != nil && (ref.Name != claim.Name || ref.Namespace != claim.Namespace || (ref.UID != "" && ref.UID != claim.UID)) {
				return fmt.Errorf("PV %s is reserved for another claim; existing storage was left intact", pv.Name)
			}
			return fmt.Errorf("waiting for PVC %s to bind to its local block PV", claim.Name)
		}
		if err := validateConfidentialBoundVolume(claim, pv, local); err != nil {
			return err
		}
	} else if local || claim.Status.Phase == corev1.ClaimBound {
		return fmt.Errorf("waiting for PVC %s to bind to its local block PV", claim.Name)
	}
	if local && claim.Status.Phase != corev1.ClaimBound {
		return fmt.Errorf("waiting for PVC %s to bind to its local block PV", claim.Name)
	}
	if !explicit && !local && cfg.StorageClassName != "" && ptr.Deref(claim.Spec.StorageClassName, "") != cfg.StorageClassName {
		return fmt.Errorf("PVC %s uses a different StorageClass; its data was left intact", claim.Name)
	}
	if err := confidentialDiskKey(cfg, pv, svc.Namespace); err != nil {
		return err
	}
	if local {
		return r.assignConfidentialLocalClaim(ctx, svc, claim)
	}
	// CSI WaitForFirstConsumer claims need the Deployment before they can bind.
	return nil
}

func (r *LLMISVCReconciler) confidentialExplicitClaim(ctx context.Context, svc *v1alpha2.LLMInferenceService, name string) (bool, error) {
	original := &v1alpha2.LLMInferenceService{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(svc), original); err != nil {
		return false, err
	}
	if original.Spec.Template == nil {
		return false, nil
	}
	selected := confidentialBlockVolume(original.Spec.Template)
	return selected != nil && selected.ClaimName == name, nil
}

func newConfidentialClaim(svc *v1alpha2.LLMInferenceService, name string) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: svc.Namespace,
			Labels:      map[string]string{confidentialManagedLabel: "true", constants.KubernetesPartOfLabelKey: constants.LLMInferenceServicePartOfValue},
			Annotations: map[string]string{confidentialServiceUIDAnnotation: string(svc.UID), confidentialServiceNameAnnotation: svc.Name},
		},
		Spec: corev1.PersistentVolumeClaimSpec{VolumeMode: ptr.To(corev1.PersistentVolumeBlock)},
	}
}

func (r *LLMISVCReconciler) createConfidentialClaim(ctx context.Context, svc *v1alpha2.LLMInferenceService, cfg *confidentialDefaults, name, selectedPV string) (*corev1.PersistentVolumeClaim, error) {
	// A missing retained claim must not turn into an empty replacement disk,
	// including when the administrator has changed the allocation defaults.
	retained := &corev1.PersistentVolumeList{}
	if err := r.List(ctx, retained); err != nil {
		return nil, err
	}
	for _, volume := range retained.Items {
		ref := volume.Spec.ClaimRef
		if ref != nil && ref.Namespace == svc.Namespace && ref.Name == name && ref.UID != "" {
			return nil, fmt.Errorf("PV %s retains a binding to missing PVC %s/%s; restore or explicitly recover that storage before creating a replacement", volume.Name, svc.Namespace, name)
		}
	}
	var pv *corev1.PersistentVolume
	var err error
	switch {
	case selectedPV != "":
		pv = &corev1.PersistentVolume{}
		err = r.Get(ctx, client.ObjectKey{Name: selectedPV}, pv)
	case cfg.StorageClassName == "":
		pv, err = r.allocateConfidentialLocalVolume(ctx, svc, cfg, name)
	}
	if err != nil {
		return nil, err
	}
	claim := newConfidentialClaim(svc, name)
	if pv != nil {
		if err := validateConfidentialLocalPV(pv); err != nil {
			return nil, err
		}
		ref := pv.Spec.ClaimRef
		if ref != nil && (ref.Namespace != svc.Namespace || ref.Name != name || ref.UID != "") {
			return nil, fmt.Errorf("PV %s is already assigned to %s/%s; explicitly select that existing claim in its namespace", pv.Name, ref.Namespace, ref.Name)
		}
		if pv.Status.Phase == corev1.VolumeReleased || pv.Status.Phase == corev1.VolumeFailed {
			return nil, fmt.Errorf("PV %s requires administrator recovery before reuse", pv.Name)
		}
		if err := confidentialDiskKey(cfg, pv, svc.Namespace); err != nil {
			return nil, err
		}
		size := cfg.StorageSize
		capacity := pv.Spec.Capacity[corev1.ResourceStorage]
		if size.IsZero() {
			size = capacity
		}
		if size.Cmp(capacity) > 0 {
			return nil, fmt.Errorf("requested storageSize %s exceeds PV %s capacity %s", size.String(), pv.Name, capacity.String())
		}
		claim.Spec.StorageClassName = ptr.To("")
		claim.Spec.VolumeName = pv.Name
		claim.Spec.AccessModes = []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}
		claim.Spec.Resources.Requests = corev1.ResourceList{corev1.ResourceStorage: size}
	} else {
		sc, err := r.confidentialStorageClass(ctx, cfg.StorageClassName)
		if err != nil {
			return nil, err
		}
		if strings.HasPrefix(sc.Provisioner, "kubernetes.io/") {
			return nil, fmt.Errorf("confidential storage requires a CSI StorageClass supporting raw block and ReadWriteOncePod; %s uses %s", sc.Name, sc.Provisioner)
		}
		if err := confidentialDiskKey(cfg, nil, svc.Namespace); err != nil {
			return nil, err
		}
		if cfg.StorageSize.Sign() <= 0 {
			return nil, errors.New("configure positive storageSize to create a new CSI claim, or select an existing claim or approved local disk")
		}
		claim.Spec.StorageClassName = ptr.To(sc.Name)
		claim.Spec.AccessModes = []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOncePod}
		claim.Spec.Resources.Requests = corev1.ResourceList{corev1.ResourceStorage: cfg.StorageSize}
	}
	// No owner reference: service deletion must not delete the encrypted data.
	if err := r.Create(ctx, claim); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return nil, err
		}
		err = r.Get(ctx, client.ObjectKeyFromObject(claim), claim)
		if err != nil {
			return nil, err
		}
	}
	return claim, nil
}

func (r *LLMISVCReconciler) confidentialStorageClass(ctx context.Context, name string) (*storagev1.StorageClass, error) {
	sc := &storagev1.StorageClass{}
	if name != "" {
		if err := r.Get(ctx, client.ObjectKey{Name: name}, sc); err != nil {
			return nil, fmt.Errorf("get confidential StorageClass: %w", err)
		}
		return sc, nil
	}
	classes := &storagev1.StorageClassList{}
	if err := r.List(ctx, classes); err != nil {
		return nil, err
	}
	for i := range classes.Items {
		candidate := &classes.Items[i]
		if candidate.Annotations["storageclass.kubernetes.io/is-default-class"] == "true" || candidate.Annotations["storageclass.beta.kubernetes.io/is-default-class"] == "true" {
			if sc.Name == "" || candidate.CreationTimestamp.After(sc.CreationTimestamp.Time) {
				sc = candidate
			}
		}
	}
	if sc.Name == "" {
		return nil, errors.New("no StorageClass or approved local disk is configured; select an existing luks-block claim or confidential-pv, or configure localVolumes")
	}
	return sc, nil
}

func validateConfidentialLocalPV(pv *corev1.PersistentVolume) error {
	capacity := pv.Spec.Capacity[corev1.ResourceStorage]
	if pv.DeletionTimestamp != nil || pv.Spec.Local == nil || !strings.HasPrefix(pv.Spec.Local.Path, "/dev/") ||
		pv.Spec.NodeAffinity == nil || pv.Spec.NodeAffinity.Required == nil || len(pv.Spec.NodeAffinity.Required.NodeSelectorTerms) == 0 ||
		ptr.Deref(pv.Spec.VolumeMode, corev1.PersistentVolumeFilesystem) != corev1.PersistentVolumeBlock ||
		len(pv.Spec.AccessModes) != 1 || pv.Spec.AccessModes[0] != corev1.ReadWriteOnce || capacity.Sign() <= 0 || pv.Spec.StorageClassName != "" {
		return fmt.Errorf("PV %s is incompatible: local storage requires a live classless raw block device, node affinity, positive capacity, and ReadWriteOnce", pv.Name)
	}
	return nil
}

func validateConfidentialBoundVolume(claim *corev1.PersistentVolumeClaim, pv *corev1.PersistentVolume, local bool) error {
	if local {
		if err := validateConfidentialLocalPV(pv); err != nil {
			return err
		}
	} else if pv.Spec.CSI == nil {
		return fmt.Errorf("PVC %s requires a CSI PV for ReadWriteOncePod", claim.Name)
	}
	capacity, requested := pv.Spec.Capacity[corev1.ResourceStorage], claim.Spec.Resources.Requests[corev1.ResourceStorage]
	ref := pv.Spec.ClaimRef
	if pv.DeletionTimestamp != nil || ptr.Deref(pv.Spec.VolumeMode, corev1.PersistentVolumeFilesystem) != corev1.PersistentVolumeBlock ||
		capacity.Cmp(requested) < 0 || pv.Spec.StorageClassName != ptr.Deref(claim.Spec.StorageClassName, "") ||
		ref == nil || ref.Name != claim.Name || ref.Namespace != claim.Namespace || claim.UID == "" || ref.UID != claim.UID {
		return fmt.Errorf("PV %s does not match the raw block PVC %s binding and capacity; existing storage was left intact", pv.Name, claim.Name)
	}
	return nil
}

// Update only metadata, with a resourceVersion precondition for competing models.
func (r *LLMISVCReconciler) reserveConfidentialClaim(ctx context.Context, svc *v1alpha2.LLMInferenceService, claim *corev1.PersistentVolumeClaim) error {
	if claim.Annotations[confidentialServiceUIDAnnotation] == string(svc.UID) && claim.Annotations[confidentialServiceNameAnnotation] == svc.Name {
		return nil
	}
	before := claim.DeepCopy()
	claim.Annotations = maps.Clone(claim.Annotations)
	if claim.Annotations == nil {
		claim.Annotations = map[string]string{}
	}
	claim.Annotations[confidentialServiceUIDAnnotation] = string(svc.UID)
	claim.Annotations[confidentialServiceNameAnnotation] = svc.Name
	return r.Patch(ctx, claim, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}))
}
