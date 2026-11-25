package kservemodule

import (
	"context"
	"errors"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/opendatahub-io/kserve-module/pkg/apis/v1alpha1"
)

// prepareModelCacheResize removes only the shared agent binding. Normal deployment
// recreates it at the desired size once asynchronous teardown has finished.
func (r *KserveModuleReconciler) prepareModelCacheResize(ctx context.Context, kserve *platformv1alpha1.Kserve) error {
	if !isModelCacheEnabled(kserve) {
		return nil
	}
	pvc := &corev1.PersistentVolumeClaim{}
	pvcErr := r.Get(ctx, client.ObjectKey{Name: modelCachePVCName, Namespace: modelCacheNamespace}, pvc)
	if pvcErr != nil && !apierrors.IsNotFound(pvcErr) {
		return pvcErr
	}
	pv := &corev1.PersistentVolume{}
	pvErr := r.Get(ctx, client.ObjectKey{Name: modelCachePVName}, pv)
	if pvErr != nil && !apierrors.IsNotFound(pvErr) {
		return pvErr
	}
	size := kserve.Spec.ModelCache.CacheSize
	if size.Sign() <= 0 {
		return errors.New("modelCache.cacheSize must be greater than zero")
	}
	resize := pvcErr == nil && (pvc.Spec.Resources.Requests.Storage().Cmp(*size) != 0 || !pvc.DeletionTimestamp.IsZero())
	resize = resize || pvErr == nil && (pv.Spec.Capacity.Storage().Cmp(*size) != 0 || !pv.DeletionTimestamp.IsZero() ||
		(pvcErr != nil && pv.Spec.ClaimRef != nil))
	if !resize {
		return nil
	}
	if pvcErr == nil && pvc.Spec.VolumeName != modelCachePVName {
		return fmt.Errorf("shared ModelCache PVC %s/%s references unexpected PV %q", modelCacheNamespace, modelCachePVCName, pvc.Spec.VolumeName)
	}
	if pvErr == nil {
		if err := validateOldStaticPV(pv, modelCacheNamespace); err != nil {
			return err
		}
	}
	ds := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: localModelNodeAgentDaemonSetName, Namespace: modelCacheNamespace}}
	if pending, err := r.deleteOldModelCacheObject(ctx, ds, true); err != nil {
		return err
	} else if pending {
		return errors.New("waiting for ModelCache node agents to terminate before storage replacement")
	}
	jobs := &batchv1.JobList{}
	if err := r.List(ctx, jobs, client.InNamespace(modelCacheNamespace)); err != nil {
		return err
	}
	for i := range jobs.Items {
		job := &jobs.Items[i]
		if !usesModelCacheClaim(job.Spec.Template.Spec) {
			continue
		}
		if job.Labels["fix-permissions"] != "true" || !hasControllerOwner(job.OwnerReferences, "serving.kserve.io/v1alpha1", "LocalModelNode", "", "") {
			return fmt.Errorf("unexpected Job %s uses shared ModelCache claim", job.Name)
		}
		if job.DeletionTimestamp.IsZero() {
			if err := r.Delete(ctx, job, client.PropagationPolicy(metav1.DeletePropagationForeground)); err != nil && !apierrors.IsNotFound(err) {
				return err
			}
		}
		return fmt.Errorf("waiting for ModelCache permission Job %s to terminate", job.Name)
	}
	pods := &corev1.PodList{}
	if err := r.List(ctx, pods, client.InNamespace(modelCacheNamespace)); err != nil {
		return err
	}
	for _, pod := range pods.Items {
		if usesModelCacheClaim(pod.Spec) {
			return fmt.Errorf("waiting for Pod %s to release shared ModelCache claim", pod.Name)
		}
	}
	for _, obj := range []client.Object{
		&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: modelCachePVCName, Namespace: modelCacheNamespace}},
		&corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: modelCachePVName}},
	} {
		if pending, err := r.deleteOldModelCacheObject(ctx, obj, true); err != nil {
			return err
		} else if pending {
			return fmt.Errorf("waiting for shared ModelCache storage %s to be removed", obj.GetName())
		}
	}
	return nil
}

func usesModelCacheClaim(spec corev1.PodSpec) bool {
	for _, volume := range spec.Volumes {
		if volume.PersistentVolumeClaim != nil && volume.PersistentVolumeClaim.ClaimName == modelCachePVCName {
			return true
		}
	}
	return false
}
