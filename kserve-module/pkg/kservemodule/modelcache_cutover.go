package kservemodule

import (
	"context"
	"fmt"
	"reflect"
	"sort"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	odhLabels "github.com/opendatahub-io/odh-platform-utilities/pkg/metadata/labels"
)

// prepareModelCacheCutover stops old namespace workloads before the shared
// ConfigMap switches jobNamespace and before a new controller can start. Each
// phase waits for deletion to be observed in a later reconciliation.
func (r *KserveModuleReconciler) prepareModelCacheCutover(ctx context.Context, rendered []unstructured.Unstructured) error {
	oldNamespace := r.getApplicationsNamespace()
	if oldNamespace == modelCacheNamespace {
		return nil
	}
	for _, workload := range []client.Object{
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: localmodelControllerDeployment, Namespace: oldNamespace}},
		&appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: localModelNodeAgentDaemonSetName, Namespace: oldNamespace}},
	} {
		pending, err := r.deleteOldModelCacheObject(ctx, workload, true)
		if err != nil {
			return fmt.Errorf("stopping old ModelCache workload: %w", err)
		}
		if pending {
			return fmt.Errorf("waiting for old ModelCache workload %s/%s to terminate", oldNamespace, workload.GetName())
		}
	}
	if err := r.removeOldModelCacheJobs(ctx, oldNamespace); err != nil {
		return err
	}
	if err := r.waitForOldModelCachePods(ctx, oldNamespace); err != nil {
		return err
	}
	if err := r.removeOldModelCacheVolumes(ctx, oldNamespace); err != nil {
		return err
	}
	for i := range rendered {
		res := rendered[i].DeepCopy()
		if !isOldNamespacedModelCacheManifest(res) {
			continue
		}
		res.SetNamespace(oldNamespace)
		pending, err := r.deleteOldModelCacheObject(ctx, res, false)
		if err != nil {
			return fmt.Errorf("cleaning old ModelCache manifest %s/%s: %w", oldNamespace, res.GetName(), err)
		}
		if pending {
			return fmt.Errorf("waiting for old ModelCache manifest %s/%s to be removed", oldNamespace, res.GetName())
		}
	}
	if err := r.updateNamespacePSA(ctx, oldNamespace, "baseline"); err != nil {
		return fmt.Errorf("restoring applications namespace PSA: %w", err)
	}
	return nil
}

// isOldNamespacedModelCacheManifest selects old module-owned namespaced render
// entries while excluding cluster-scoped and shared resources.
func isOldNamespacedModelCacheManifest(res *unstructured.Unstructured) bool {
	if res.GetName() == "kserve-modelcache-configmap-source-reader" {
		return false // This reader is intentionally installed in the applications namespace.
	}
	switch res.GetKind() {
	case "ServiceAccount", "Service", "Role", "RoleBinding", "Certificate", "Issuer":
		return true
	case "ConfigMap":
		return res.GetName() == "kserve-modelcache-parameters"
	default:
		return false
	}
}

func (r *KserveModuleReconciler) deleteOldModelCacheObject(ctx context.Context, obj client.Object, strict bool) (bool, error) {
	live := obj.DeepCopyObject().(client.Object)
	key := client.ObjectKeyFromObject(obj)
	if err := r.Get(ctx, key, live); err != nil {
		if meta.IsNoMatchError(err) {
			return false, nil
		}
		return false, client.IgnoreNotFound(err)
	}
	if live.GetLabels()[odhLabels.PlatformPartOf] != KserveComponentName {
		if strict {
			return false, fmt.Errorf("%s is not marked as a KServe module resource", key)
		}
		return false, nil
	}
	if live.GetDeletionTimestamp().IsZero() {
		if err := r.Delete(ctx, live, client.PropagationPolicy(metav1.DeletePropagationForeground)); err != nil && !apierrors.IsNotFound(err) {
			return false, err
		}
	}
	return true, nil
}

func (r *KserveModuleReconciler) waitForOldModelCachePods(ctx context.Context, namespace string) error {
	pods := &corev1.PodList{}
	if err := r.List(ctx, pods, client.InNamespace(namespace)); err != nil {
		return fmt.Errorf("listing old ModelCache Pods: %w", err)
	}
	for i := range pods.Items {
		pod := &pods.Items[i]
		controlPlane := pod.Labels["control-plane"]
		if controlPlane == localmodelControllerDeployment || controlPlane == localModelNodeAgentDaemonSetName ||
			pod.Spec.ServiceAccountName == "kserve-localmodelnode-agent" || pod.Spec.ServiceAccountName == "kserve-localmodel-permfix" {
			return fmt.Errorf("waiting for old ModelCache Pod %s/%s to terminate", namespace, pod.Name)
		}
	}
	return nil
}

func (r *KserveModuleReconciler) removeOldModelCacheJobs(ctx context.Context, namespace string) error {
	jobs := &batchv1.JobList{}
	if err := r.List(ctx, jobs, client.InNamespace(namespace)); err != nil {
		return fmt.Errorf("listing old ModelCache Jobs: %w", err)
	}
	for i := range jobs.Items {
		job := &jobs.Items[i]
		if !hasControllerOwner(job.OwnerReferences, "serving.kserve.io/v1alpha1", "LocalModelNode", "", "") {
			continue
		}
		valid := job.Labels["node"] != "" &&
			((job.Labels["fix-permissions"] == "true" && job.Labels["model"] == "") ||
				(job.Labels["model"] != "" && job.Labels["fix-permissions"] == ""))
		if !valid {
			return fmt.Errorf("old LocalModelNode Job %s/%s has ambiguous ModelCache labels", namespace, job.Name)
		}
		if job.DeletionTimestamp.IsZero() {
			if err := r.Delete(ctx, job, client.PropagationPolicy(metav1.DeletePropagationForeground)); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("deleting old ModelCache Job %s/%s: %w", namespace, job.Name, err)
			}
		}
		return fmt.Errorf("waiting for old ModelCache Job %s/%s to terminate", namespace, job.Name)
	}
	return nil
}

type oldWorkingVolume struct {
	pvcName        string
	pvName         string
	nodeGroupName  string
	ownerKind      string
	ownerName      string
	ownerUID       types.UID
	cacheNamespace string
}

func (r *KserveModuleReconciler) removeOldModelCacheVolumes(ctx context.Context, namespace string) error {
	if err := r.removeOldStaticVolume(ctx, namespace); err != nil {
		return err
	}
	volumes, err := r.oldWorkingVolumes(ctx)
	if err != nil {
		return err
	}
	for _, volume := range volumes {
		if err := r.removeOldWorkingVolume(ctx, namespace, volume); err != nil {
			return err
		}
	}
	return nil
}

func (r *KserveModuleReconciler) removeOldStaticVolume(ctx context.Context, namespace string) error {
	pvc := &corev1.PersistentVolumeClaim{}
	pvcErr := r.Get(ctx, client.ObjectKey{Name: modelCachePVCName, Namespace: namespace}, pvc)
	if pvcErr != nil && !apierrors.IsNotFound(pvcErr) {
		return fmt.Errorf("reading old static PVC: %w", pvcErr)
	}
	pv := &corev1.PersistentVolume{}
	pvErr := r.Get(ctx, client.ObjectKey{Name: modelCachePVName}, pv)
	if pvErr != nil && !apierrors.IsNotFound(pvErr) {
		return fmt.Errorf("reading static PV: %w", pvErr)
	}
	if pvcErr == nil {
		if pvc.Labels[odhLabels.PlatformPartOf] != KserveComponentName || pvc.Spec.VolumeName != modelCachePVName {
			return fmt.Errorf("old static PVC %s/%s has unexpected ownership or PV", namespace, modelCachePVCName)
		}
		if pvErr != nil {
			if pvc.DeletionTimestamp.IsZero() {
				if err := r.Delete(ctx, pvc, client.PropagationPolicy(metav1.DeletePropagationForeground)); err != nil && !apierrors.IsNotFound(err) {
					return fmt.Errorf("deleting old static PVC: %w", err)
				}
			}
			return fmt.Errorf("waiting for old static PVC %s/%s to be removed", namespace, modelCachePVCName)
		}
		if err := validateOldStaticPV(pv, namespace); err != nil {
			return err
		}
		if pv.Spec.ClaimRef == nil && pv.Annotations[modelCacheClaimNamespaceAnnotation] == modelCacheNamespace {
			if err := r.validateOptionalTargetPVC(ctx, modelCachePVCName, modelCachePVName, "", "", "", true, false); err != nil {
				return err
			}
		}
		if pv.Spec.ClaimRef != nil && pv.Spec.ClaimRef.Namespace == modelCacheNamespace {
			if err := r.validateTargetPVC(ctx, modelCachePVCName, modelCachePVName, pv.Spec.ClaimRef); err != nil {
				return err
			}
		}
		if pv.Spec.ClaimRef != nil && pv.Spec.ClaimRef.Namespace == namespace &&
			pv.Spec.ClaimRef.Name == modelCachePVCName && pv.Spec.ClaimRef.UID != "" && pvc.UID != "" &&
			pv.Spec.ClaimRef.UID != pvc.UID {
			return fmt.Errorf("static PV %s claim UID does not match old PVC", modelCachePVName)
		}
		if pvc.DeletionTimestamp.IsZero() {
			if err := r.Delete(ctx, pvc, client.PropagationPolicy(metav1.DeletePropagationForeground)); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("deleting old static PVC: %w", err)
			}
		}
		return fmt.Errorf("waiting for old static PVC %s/%s to be removed", namespace, modelCachePVCName)
	}
	if pvErr != nil {
		return nil
	}
	if err := validateOldStaticPV(pv, namespace); err != nil {
		return err
	}
	if pv.Annotations[modelCacheClaimNamespaceAnnotation] == modelCacheNamespace {
		if pv.Spec.ClaimRef != nil {
			if err := r.validateTargetPVC(ctx, modelCachePVCName, modelCachePVName, pv.Spec.ClaimRef); err != nil {
				return err
			}
		} else if err := r.validateOptionalTargetPVC(ctx, modelCachePVCName, modelCachePVName, "", "", "", true, false); err != nil {
			return err
		}
		return nil
	}
	if pv.Spec.ClaimRef != nil && pv.Spec.ClaimRef.Namespace == modelCacheNamespace {
		if err := r.validateTargetPVC(ctx, modelCachePVCName, modelCachePVName, pv.Spec.ClaimRef); err != nil {
			return err
		}
		return nil // an interrupted cutover already rebound the target claim
	}
	if pv.DeletionTimestamp.IsZero() {
		if err := r.Delete(ctx, pv, client.PropagationPolicy(metav1.DeletePropagationForeground)); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("deleting old static PV: %w", err)
		}
	}
	return fmt.Errorf("waiting for old static PV %s to be removed before rebinding", modelCachePVName)
}

func validateOldStaticPV(pv *corev1.PersistentVolume, oldNamespace string) error {
	if pv.Labels[odhLabels.PlatformPartOf] != KserveComponentName || pv.Spec.HostPath == nil ||
		pv.Spec.HostPath.Path != modelCacheHostDir || pv.Spec.StorageClassName != modelCacheStorageClass ||
		pv.Spec.PersistentVolumeReclaimPolicy != corev1.PersistentVolumeReclaimRetain {
		return fmt.Errorf("static PV %s has unexpected ownership or storage", modelCachePVName)
	}
	claimNamespace := pv.Annotations[modelCacheClaimNamespaceAnnotation]
	if claimNamespace != "" && claimNamespace != oldNamespace && claimNamespace != modelCacheNamespace {
		return fmt.Errorf("static PV %s has unexpected claim namespace annotation %q", modelCachePVName, claimNamespace)
	}
	if pv.Spec.ClaimRef == nil {
		return nil
	}
	if claimNamespace == modelCacheNamespace &&
		(pv.Spec.ClaimRef.Namespace != modelCacheNamespace || pv.Spec.ClaimRef.Name != modelCachePVCName) {
		return fmt.Errorf("static PV %s has unexpected target claim binding", modelCachePVName)
	}
	if pv.Spec.ClaimRef.Namespace == oldNamespace && pv.Spec.ClaimRef.Name == modelCachePVCName {
		return nil
	}
	if pv.Spec.ClaimRef.Namespace == modelCacheNamespace && pv.Spec.ClaimRef.Name == modelCachePVCName {
		return nil
	}
	return fmt.Errorf("static PV %s has unexpected claim binding", modelCachePVName)
}

func (r *KserveModuleReconciler) validateTargetPVC(ctx context.Context, pvcName, pvName string, claimRef *corev1.ObjectReference) error {
	if claimRef.Namespace != modelCacheNamespace || claimRef.Name != pvcName {
		return fmt.Errorf("PV %s has unexpected target claim %s/%s", pvName, claimRef.Namespace, claimRef.Name)
	}
	targetPVC := &corev1.PersistentVolumeClaim{}
	if err := r.Get(ctx, client.ObjectKey{Name: pvcName, Namespace: modelCacheNamespace}, targetPVC); err != nil {
		return fmt.Errorf("reading target PVC %s/%s for PV %s: %w", modelCacheNamespace, pvcName, pvName, err)
	}
	if targetPVC.Spec.VolumeName != pvName {
		return fmt.Errorf("target PVC %s/%s references unexpected PV %q", modelCacheNamespace, pvcName, targetPVC.Spec.VolumeName)
	}
	if claimRef.UID != "" && claimRef.UID != targetPVC.UID {
		return fmt.Errorf("PV %s claim UID does not match target PVC %s/%s", pvName, modelCacheNamespace, pvcName)
	}
	return nil
}

// validateOptionalTargetPVC checks a target claim when a released PV may be
// reused by the new namespace. A missing target claim is expected while the
// target controllers have not reconciled yet; an existing claim must already
// identify the expected PV and ownership contract before an old claim is
// removed.
func (r *KserveModuleReconciler) validateOptionalTargetPVC(
	ctx context.Context,
	pvcName, pvName, ownerKind, ownerName string,
	ownerUID types.UID,
	requireModuleLabel, requireNoOwner bool,
) error {
	targetPVC := &corev1.PersistentVolumeClaim{}
	if err := r.Get(ctx, client.ObjectKey{Name: pvcName, Namespace: modelCacheNamespace}, targetPVC); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("reading optional target PVC %s/%s for PV %s: %w", modelCacheNamespace, pvcName, pvName, err)
	}
	if targetPVC.Spec.VolumeName != pvName {
		return fmt.Errorf("target PVC %s/%s references unexpected PV %q", modelCacheNamespace, pvcName, targetPVC.Spec.VolumeName)
	}
	if requireModuleLabel && targetPVC.Labels[odhLabels.PlatformPartOf] != KserveComponentName {
		return fmt.Errorf("target PVC %s/%s is not marked as a KServe module resource", modelCacheNamespace, pvcName)
	}
	if ownerKind != "" && !hasControllerOwner(targetPVC.OwnerReferences, "serving.kserve.io/v1alpha1", ownerKind, ownerName, ownerUID) {
		return fmt.Errorf("target PVC %s/%s has unexpected %s ownership", modelCacheNamespace, pvcName, ownerKind)
	}
	if requireNoOwner && len(targetPVC.OwnerReferences) != 0 {
		return fmt.Errorf("target PVC %s/%s has unexpected owner references", modelCacheNamespace, pvcName)
	}
	return nil
}

func (r *KserveModuleReconciler) oldWorkingVolumes(ctx context.Context) ([]oldWorkingVolume, error) {
	var result []oldWorkingVolume
	for _, kind := range []string{"LocalModelCache", "LocalModelNamespaceCache"} {
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(schema.GroupVersionKind{Group: "serving.kserve.io", Version: "v1alpha1", Kind: kind + "List"})
		if err := r.List(ctx, list); err != nil {
			if meta.IsNoMatchError(err) {
				continue
			}
			return nil, fmt.Errorf("listing %s resources for ModelCache cutover: %w", kind, err)
		}
		for i := range list.Items {
			cache := &list.Items[i]
			groups, _, err := unstructured.NestedStringSlice(cache.Object, "spec", "nodeGroups")
			if err != nil {
				return nil, fmt.Errorf("reading %s/%s nodeGroups: %w", kind, cache.GetName(), err)
			}
			for _, group := range groups {
				volume := oldWorkingVolume{nodeGroupName: group, ownerKind: kind, ownerName: cache.GetName(), ownerUID: cache.GetUID(), cacheNamespace: cache.GetNamespace()}
				if kind == "LocalModelCache" {
					volume.pvcName = cache.GetName() + "-" + group
					volume.pvName = volume.pvcName + "-download"
				} else {
					volume.pvcName = cache.GetName() + "-" + group + "-" + cache.GetNamespace() + "-download"
					volume.pvName = volume.pvcName
				}
				result = append(result, volume)
			}
		}
	}
	return result, nil
}

func (r *KserveModuleReconciler) removeOldWorkingVolume(ctx context.Context, namespace string, volume oldWorkingVolume) error {
	pvc := &corev1.PersistentVolumeClaim{}
	pvcErr := r.Get(ctx, client.ObjectKey{Name: volume.pvcName, Namespace: namespace}, pvc)
	if pvcErr != nil && !apierrors.IsNotFound(pvcErr) {
		return fmt.Errorf("reading old download PVC %s: %w", volume.pvcName, pvcErr)
	}
	pv := &corev1.PersistentVolume{}
	pvErr := r.Get(ctx, client.ObjectKey{Name: volume.pvName}, pv)
	if pvErr != nil && !apierrors.IsNotFound(pvErr) {
		return fmt.Errorf("reading old download PV %s: %w", volume.pvName, pvErr)
	}
	if pvcErr != nil && pvErr != nil {
		return nil
	}
	if pvcErr == nil && pvc.Spec.VolumeName != volume.pvName {
		return fmt.Errorf("old download PVC %s/%s references unexpected PV %q", namespace, volume.pvcName, pvc.Spec.VolumeName)
	}
	if volume.ownerKind == "LocalModelCache" {
		if (pvcErr == nil && !hasControllerOwner(pvc.OwnerReferences, "serving.kserve.io/v1alpha1", volume.ownerKind, volume.ownerName, volume.ownerUID)) ||
			(pvErr == nil && !hasControllerOwner(pv.OwnerReferences, "serving.kserve.io/v1alpha1", volume.ownerKind, volume.ownerName, volume.ownerUID)) {
			return fmt.Errorf("download volume %s has unexpected LocalModelCache ownership", volume.pvName)
		}
	} else {
		if pvErr == nil && len(pv.OwnerReferences) != 0 {
			return fmt.Errorf("download PV %s has unexpected LocalModelNamespaceCache ownership", volume.pvName)
		}
		if pvcErr == nil && len(pvc.OwnerReferences) != 0 &&
			!hasControllerOwner(pvc.OwnerReferences, "serving.kserve.io/v1alpha1", volume.ownerKind, volume.ownerName, volume.ownerUID) {
			return fmt.Errorf("download PVC %s/%s has unexpected ownership", namespace, volume.pvcName)
		}
	}
	if pvErr == nil {
		if pv.Spec.ClaimRef != nil && pv.Spec.ClaimRef.Namespace == modelCacheNamespace {
			if err := r.validateTargetPVC(ctx, volume.pvcName, volume.pvName, pv.Spec.ClaimRef); err != nil {
				return err
			}
			// A target PVC may have claimed this PV before the old PVC was
			// garbage-collected. The old claim is still safe to remove after
			// its ownership and storage have been checked.
			if pvcErr == nil {
				if err := r.validateOldDownloadStorage(ctx, volume.nodeGroupName, pv); err != nil {
					return err
				}
				if pvc.DeletionTimestamp.IsZero() {
					if err := r.Delete(ctx, pvc, client.PropagationPolicy(metav1.DeletePropagationForeground)); err != nil && !apierrors.IsNotFound(err) {
						return fmt.Errorf("deleting old download PVC %s: %w", volume.pvcName, err)
					}
				}
				return fmt.Errorf("waiting for old download PVC %s/%s to be removed", namespace, volume.pvcName)
			}
			return nil // a new download PVC already owns this PV
		}
		if pv.Spec.ClaimRef == nil {
			// A released PV is reusable by the target PVC.  Validate its
			// storage before retaining it so an unrelated PV with the same
			// generated name cannot silently be adopted.
			if volume.ownerKind == "LocalModelCache" {
				if err := r.validateOptionalTargetPVC(ctx, volume.pvcName, volume.pvName, volume.ownerKind, volume.ownerName, volume.ownerUID, false, false); err != nil {
					return err
				}
			} else {
				if err := r.validateOptionalTargetPVC(ctx, volume.pvcName, volume.pvName, "", "", "", false, true); err != nil {
					return err
				}
			}
			if pvcErr == nil {
				if err := r.validateOldDownloadStorage(ctx, volume.nodeGroupName, pv); err != nil {
					return err
				}
			}
		} else if pv.Spec.ClaimRef.Namespace != namespace || pv.Spec.ClaimRef.Name != volume.pvcName {
			return fmt.Errorf("download PV %s has unexpected claim binding", volume.pvName)
		} else if pvcErr == nil && pv.Spec.ClaimRef.UID != "" && pvc.UID != "" && pv.Spec.ClaimRef.UID != pvc.UID {
			return fmt.Errorf("download PV %s claim UID does not match PVC", volume.pvName)
		}
		if pv.Spec.ClaimRef != nil {
			if err := r.validateOldDownloadStorage(ctx, volume.nodeGroupName, pv); err != nil {
				return err
			}
		}
	}
	if pvcErr == nil {
		if pvc.DeletionTimestamp.IsZero() {
			if err := r.Delete(ctx, pvc, client.PropagationPolicy(metav1.DeletePropagationForeground)); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("deleting old download PVC %s: %w", volume.pvcName, err)
			}
		}
		return fmt.Errorf("waiting for old download PVC %s/%s to be removed", namespace, volume.pvcName)
	}
	if pvErr == nil && pv.Spec.ClaimRef != nil {
		if pv.DeletionTimestamp.IsZero() {
			if err := r.Delete(ctx, pv, client.PropagationPolicy(metav1.DeletePropagationForeground)); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("deleting old download PV %s: %w", volume.pvName, err)
			}
		}
		return fmt.Errorf("waiting for old download PV %s to be removed", volume.pvName)
	}
	return nil
}

func (r *KserveModuleReconciler) validateOldDownloadStorage(ctx context.Context, groupName string, pv *corev1.PersistentVolume) error {
	group := &unstructured.Unstructured{}
	group.SetGroupVersionKind(localModelNodeGroupGVK)
	if err := r.Get(ctx, client.ObjectKey{Name: groupName}, group); err != nil {
		return fmt.Errorf("reading NodeGroup %s for download PV %s: %w", groupName, pv.Name, err)
	}
	specMap, found, err := unstructured.NestedMap(group.Object, "spec", "persistentVolumeSpec")
	if err != nil || !found {
		return fmt.Errorf("NodeGroup %s has no usable persistentVolumeSpec: %v", groupName, err)
	}
	var spec corev1.PersistentVolumeSpec
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(specMap, &spec); err != nil {
		return fmt.Errorf("reading NodeGroup %s PV spec: %w", groupName, err)
	}
	if !persistentVolumeSpecsMatch(spec, pv.Spec) {
		return fmt.Errorf("download PV %s storage differs from NodeGroup %s", pv.Name, groupName)
	}
	return nil
}

func persistentVolumeSpecsMatch(expected, actual corev1.PersistentVolumeSpec) bool {
	expected = *expected.DeepCopy()
	actual = *actual.DeepCopy()

	if !resourceListsEqual(expected.Capacity, actual.Capacity) {
		return false
	}

	expected.Capacity = nil
	actual.Capacity = nil
	expected.ClaimRef = nil
	actual.ClaimRef = nil
	expected.AccessModes = normalizedAccessModes(expected.AccessModes)
	actual.AccessModes = normalizedAccessModes(actual.AccessModes)
	expected.MountOptions = normalizedStrings(expected.MountOptions)
	actual.MountOptions = normalizedStrings(actual.MountOptions)
	if expected.VolumeMode == nil {
		expected.VolumeMode = ptr.To(corev1.PersistentVolumeFilesystem)
	}
	if actual.VolumeMode == nil {
		actual.VolumeMode = ptr.To(corev1.PersistentVolumeFilesystem)
	}
	if expected.PersistentVolumeReclaimPolicy == "" {
		expected.PersistentVolumeReclaimPolicy = corev1.PersistentVolumeReclaimRetain
	}
	if actual.PersistentVolumeReclaimPolicy == "" {
		actual.PersistentVolumeReclaimPolicy = corev1.PersistentVolumeReclaimRetain
	}
	expected.NodeAffinity = normalizedVolumeNodeAffinity(expected.NodeAffinity)
	actual.NodeAffinity = normalizedVolumeNodeAffinity(actual.NodeAffinity)
	return reflect.DeepEqual(expected, actual)
}

func normalizedVolumeNodeAffinity(affinity *corev1.VolumeNodeAffinity) *corev1.VolumeNodeAffinity {
	if affinity == nil || affinity.Required == nil {
		return affinity
	}

	normalized := affinity.DeepCopy()
	terms := normalized.Required.NodeSelectorTerms
	for i := range terms {
		terms[i].MatchExpressions = normalizeNodeSelectorRequirements(terms[i].MatchExpressions)
		terms[i].MatchFields = normalizeNodeSelectorRequirements(terms[i].MatchFields)
	}
	sort.Slice(terms, func(i, j int) bool {
		return compareNodeSelectorTerms(terms[i], terms[j]) < 0
	})
	return normalized
}

func normalizeNodeSelectorRequirements(requirements []corev1.NodeSelectorRequirement) []corev1.NodeSelectorRequirement {
	if len(requirements) == 0 {
		return nil
	}
	for i := range requirements {
		if len(requirements[i].Values) == 0 {
			requirements[i].Values = nil
			continue
		}
		sort.Strings(requirements[i].Values)
	}
	sort.Slice(requirements, func(i, j int) bool {
		return compareNodeSelectorRequirements(requirements[i], requirements[j]) < 0
	})
	return requirements
}

func compareNodeSelectorTerms(a, b corev1.NodeSelectorTerm) int {
	if comparison := compareNodeSelectorRequirementSlices(a.MatchExpressions, b.MatchExpressions); comparison != 0 {
		return comparison
	}
	return compareNodeSelectorRequirementSlices(a.MatchFields, b.MatchFields)
}

func compareNodeSelectorRequirementSlices(a, b []corev1.NodeSelectorRequirement) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if comparison := compareNodeSelectorRequirements(a[i], b[i]); comparison != 0 {
			return comparison
		}
	}
	switch {
	case len(a) < len(b):
		return -1
	case len(a) > len(b):
		return 1
	default:
		return 0
	}
}

func compareNodeSelectorRequirements(a, b corev1.NodeSelectorRequirement) int {
	if a.Key < b.Key {
		return -1
	}
	if a.Key > b.Key {
		return 1
	}
	if a.Operator < b.Operator {
		return -1
	}
	if a.Operator > b.Operator {
		return 1
	}
	return compareStrings(a.Values, b.Values)
}

func compareStrings(a, b []string) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	switch {
	case len(a) < len(b):
		return -1
	case len(a) > len(b):
		return 1
	default:
		return 0
	}
}

func resourceListsEqual(expected, actual corev1.ResourceList) bool {
	if len(expected) != len(actual) {
		return false
	}
	for name, expectedQuantity := range expected {
		actualQuantity, ok := actual[name]
		if !ok || expectedQuantity.Cmp(actualQuantity) != 0 {
			return false
		}
	}
	return true
}

func normalizedAccessModes(modes []corev1.PersistentVolumeAccessMode) []corev1.PersistentVolumeAccessMode {
	if len(modes) == 0 {
		return nil
	}
	result := append([]corev1.PersistentVolumeAccessMode(nil), modes...)
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func normalizedStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}

func hasControllerOwner(refs []metav1.OwnerReference, apiVersion, kind, name string, uid types.UID) bool {
	for _, ref := range refs {
		if ref.APIVersion == apiVersion && ref.Kind == kind &&
			(name == "" || ref.Name == name) && (uid == "" || ref.UID == uid) && ptr.Deref(ref.Controller, false) {
			return true
		}
	}
	return false
}
