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

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
)

func confidentialLocalAffinity(node string) *corev1.VolumeNodeAffinity {
	return &corev1.VolumeNodeAffinity{Required: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{
		MatchExpressions: []corev1.NodeSelectorRequirement{{Key: corev1.LabelHostname, Operator: corev1.NodeSelectorOpIn, Values: []string{node}}},
	}}}}
}

func (r *LLMISVCReconciler) allocateConfidentialLocalVolume(ctx context.Context, svc *v1alpha2.LLMInferenceService, cfg *confidentialDefaults, claimName string) (*corev1.PersistentVolume, error) {
	eligible := false
	defaultKey := cfg.LUKSResourceID
	for _, disk := range cfg.LocalVolumes {
		if disk.Namespace != svc.Namespace {
			continue
		}
		eligible = true
		cfg.LUKSResourceID = defaultKey
		if cfg.StorageSize.Cmp(disk.Capacity) > 0 {
			continue
		}
		pv := &corev1.PersistentVolume{}
		err := r.Get(ctx, client.ObjectKey{Name: disk.Name}, pv)
		if apierrors.IsNotFound(err) {
			candidate := &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: disk.Name}, Spec: corev1.PersistentVolumeSpec{
				Capacity: corev1.ResourceList{corev1.ResourceStorage: disk.Capacity}, VolumeMode: ptr.To(corev1.PersistentVolumeBlock),
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}, PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
				PersistentVolumeSource: corev1.PersistentVolumeSource{Local: &corev1.LocalVolumeSource{Path: disk.DevicePath}},
				NodeAffinity:           confidentialLocalAffinity(disk.NodeName),
				ClaimRef:               &corev1.ObjectReference{APIVersion: "v1", Kind: "PersistentVolumeClaim", Namespace: svc.Namespace, Name: claimName},
			}}
			// Validate the selected disk key before publishing any new storage.
			if err := confidentialDiskKey(cfg, candidate, svc.Namespace); err != nil {
				return nil, err
			}
			if err := r.checkConfidentialDeviceDuplicate(ctx, disk); err != nil {
				return nil, err
			}
			candidate.Labels = map[string]string{confidentialManagedLabel: "true"}
			candidate.Annotations = map[string]string{confidentialServiceUIDAnnotation: string(svc.UID), confidentialServiceNameAnnotation: svc.Name}
			err = r.Create(ctx, candidate)
			if err == nil {
				return candidate, nil
			}
			if !apierrors.IsAlreadyExists(err) {
				return nil, err
			}
			// Another model may have reserved the same approved entry concurrently.
			err = r.Get(ctx, client.ObjectKey{Name: disk.Name}, pv)
		}
		if err != nil {
			return nil, err
		}
		if err := validateConfidentialLocalPV(pv); err != nil {
			return nil, err
		}
		capacity := pv.Spec.Capacity[corev1.ResourceStorage]
		if pv.Spec.Local.Path != disk.DevicePath || capacity.Cmp(disk.Capacity) != 0 ||
			!equality.Semantic.DeepEqual(pv.Spec.NodeAffinity, confidentialLocalAffinity(disk.NodeName)) ||
			pv.Spec.PersistentVolumeReclaimPolicy != corev1.PersistentVolumeReclaimRetain {
			return nil, fmt.Errorf("PV %s differs from its approved localVolumes configuration; it was left intact", pv.Name)
		}
		if pv.Status.Phase == corev1.VolumeReleased || pv.Status.Phase == corev1.VolumeFailed {
			continue
		}
		ref := pv.Spec.ClaimRef
		if ref != nil && (ref.Namespace != svc.Namespace || ref.Name != claimName || ref.UID != "") {
			continue
		}
		if owner := pv.Annotations[confidentialServiceUIDAnnotation]; owner != "" && owner != string(svc.UID) {
			if ref != nil && ref.Namespace == svc.Namespace && ref.Name == claimName {
				return nil, fmt.Errorf("PV %s retains a reservation for an earlier service instance; explicitly recover the storage before creating a replacement", pv.Name)
			}
			continue
		}
		if ref == nil {
			// Do not claim an unreserved administrator PV while competing allocators
			// can select it. An explicit confidential-pv selection can use it instead.
			return nil, fmt.Errorf("PV %s already exists without this model's reservation; select it explicitly using %s", pv.Name, confidentialPVAnnotation)
		}
		if err := confidentialDiskKey(cfg, pv, svc.Namespace); err != nil {
			return nil, err
		}
		return pv, nil
	}
	if eligible {
		return nil, errors.New("no approved local disk is available with sufficient capacity; existing reservations and retained data were left intact")
	}
	return nil, nil
}

func (r *LLMISVCReconciler) checkConfidentialDeviceDuplicate(ctx context.Context, disk confidentialLocalVolume) error {
	volumes := &corev1.PersistentVolumeList{}
	if err := r.List(ctx, volumes); err != nil {
		return err
	}
	for _, pv := range volumes.Items {
		if pv.Spec.Local != nil && pv.Spec.Local.Path == disk.DevicePath && confidentialPVMayUseNode(&pv, disk.NodeName) {
			return fmt.Errorf("device %s on %s is already represented by PV %s; reuse that PV instead of publishing the disk again", disk.DevicePath, disk.NodeName, pv.Name)
		}
	}
	return nil
}

// Unknown affinity is treated conservatively: only a hostname constraint that
// excludes this node proves that the same device path describes a different disk.
func confidentialPVMayUseNode(pv *corev1.PersistentVolume, node string) bool {
	if pv.Spec.NodeAffinity == nil || pv.Spec.NodeAffinity.Required == nil {
		return true
	}
	for _, term := range pv.Spec.NodeAffinity.Required.NodeSelectorTerms {
		excluded := false
		for _, match := range term.MatchExpressions {
			if match.Key != corev1.LabelHostname {
				continue
			}
			contains := false
			for _, value := range match.Values {
				contains = contains || value == node
			}
			excluded = excluded || (match.Operator == corev1.NodeSelectorOpIn && !contains) ||
				(match.Operator == corev1.NodeSelectorOpNotIn && contains) || match.Operator == corev1.NodeSelectorOpDoesNotExist
		}
		if !excluded {
			return true
		}
	}
	return len(pv.Spec.NodeAffinity.Required.NodeSelectorTerms) == 0
}

func (r *LLMISVCReconciler) assignConfidentialLocalClaim(ctx context.Context, svc *v1alpha2.LLMInferenceService, claim *corev1.PersistentVolumeClaim) error {
	if svc.UID == "" {
		return errors.New("cannot assign a local disk before the model service has a UID")
	}
	previous := claim.Annotations[confidentialServiceUIDAnnotation]
	if previous != "" && previous != string(svc.UID) {
		services := &v1alpha2.LLMInferenceServiceList{}
		if err := r.List(ctx, services, client.InNamespace(svc.Namespace)); err != nil {
			return err
		}
		for _, other := range services.Items {
			if string(other.UID) == previous {
				return fmt.Errorf("PVC %s is assigned to model service %s; it cannot be shared", claim.Name, other.Name)
			}
		}
	}
	pods := &corev1.PodList{}
	if err := r.List(ctx, pods, client.InNamespace(svc.Namespace)); err != nil {
		return err
	}
	ownPods := 0
	for _, pod := range pods.Items {
		if pod.DeletionTimestamp == nil && (pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed) {
			continue
		}
		usesClaim := false
		for _, volume := range pod.Spec.Volumes {
			usesClaim = usesClaim || (volume.PersistentVolumeClaim != nil && volume.PersistentVolumeClaim.ClaimName == claim.Name)
		}
		if !usesClaim {
			continue
		}
		owned, err := r.confidentialPodOwnedByService(ctx, &pod, svc)
		if err != nil {
			return err
		}
		if !owned {
			return fmt.Errorf("PVC %s is still used by pod %s; stop that consumer before assigning the disk", claim.Name, pod.Name)
		}
		ownPods++
	}
	if ownPods > 1 {
		return fmt.Errorf("PVC %s is used by multiple model pods; local encrypted storage requires a single writer", claim.Name)
	}
	// Optimistic locking on the PVC prevents two model reconcilers acquiring it.
	// This is not device fencing against independent pods or a failed node.
	return r.reserveConfidentialClaim(ctx, svc, claim)
}

func (r *LLMISVCReconciler) confidentialPodOwnedByService(ctx context.Context, pod *corev1.Pod, svc *v1alpha2.LLMInferenceService) (bool, error) {
	ref := metav1.GetControllerOf(pod)
	if ref == nil || ref.APIVersion != appsv1.SchemeGroupVersion.String() || ref.Kind != "ReplicaSet" {
		return false, nil
	}
	rs := &appsv1.ReplicaSet{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: pod.Namespace, Name: ref.Name}, rs); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	if rs.UID != ref.UID {
		return false, nil
	}
	ref = metav1.GetControllerOf(rs)
	if ref == nil || ref.APIVersion != appsv1.SchemeGroupVersion.String() || ref.Kind != "Deployment" {
		return false, nil
	}
	deployment := &appsv1.Deployment{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: pod.Namespace, Name: ref.Name}, deployment); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	if deployment.UID != ref.UID {
		return false, nil
	}
	ref = metav1.GetControllerOf(deployment)
	return ref != nil && ref.APIVersion == v1alpha2.LLMInferenceServiceGVK.GroupVersion().String() &&
		ref.Kind == v1alpha2.LLMInferenceServiceGVK.Kind && ref.Name == svc.Name && ref.UID == svc.UID, nil
}
