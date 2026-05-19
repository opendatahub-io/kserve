package kservemodule

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/opendatahub-io/odh-platform-utilities/api/common"

	platformv1alpha1 "github.com/opendatahub-io/kserve-module/pkg/apis/v1alpha1"
)

const (
	modelCacheLabelKey   = "kserve/localmodel"
	modelCacheLabelValue = "worker"

	modelCachePVName  = "kserve-localmodelnode-pv"
	modelCachePVCName = "kserve-localmodelnode-pvc"
	modelCacheHostDir = "/var/lib/kserve/models"

	localModelNodeGroupName = "workers"

	localModelConfigKeyName  = "localModel"
	openshiftConfigKeyName   = "openshiftConfig"
	psaElevatedByAnnotation  = "opendatahub.io/psa-elevated-by"
	psaElevatedByValue       = "kserve-modelcache"
	securityEnforceLabel     = "pod-security.kubernetes.io/enforce"
)

var localModelNodeGroupGVK = schema.GroupVersionKind{
	Group:   "serving.kserve.io",
	Version: "v1alpha1",
	Kind:    "LocalModelNodeGroup",
}

func isModelCacheEnabled(kserve *platformv1alpha1.Kserve) bool {
	return kserve.Spec.ModelCache != nil && kserve.Spec.ModelCache.ManagementState == common.Managed
}

func (r *KserveModuleReconciler) reconcileModelCache(ctx context.Context, kserve *platformv1alpha1.Kserve) error {
	log := ctrl.LoggerFrom(ctx)

	if !isModelCacheEnabled(kserve) {
		log.Info("ModelCache not enabled, skipping reconciliation")
		// TODO(RHOAIENG-61201): call cleanupModelCache once apply lifecycle is implemented
		return nil
	}

	log.Info("Reconciling ModelCache resources")

	// TODO(RHOAIENG-61201): wire these into the apply lifecycle:
	// 1. updateNamespacePSA(ctx, "privileged")
	// 2. forceReconcileKserveAgentImage (handled via post-render in components pipeline)
	// 3. CreateOrUpdate PV (from buildModelCachePV)
	// 4. CreateOrUpdate PVC (from buildModelCachePVC)
	// 5. CreateOrUpdate LocalModelNodeGroup (from buildLocalModelNodeGroup)
	// 6. labelModelCacheNodes(ctx, kserve)

	return nil
}

func (r *KserveModuleReconciler) buildModelCachePV(kserve *platformv1alpha1.Kserve) (*corev1.PersistentVolume, error) {
	if kserve.Spec.ModelCache.CacheSize == nil {
		return nil, fmt.Errorf("cacheSize is required when ModelCache is Managed")
	}

	cacheSize := *kserve.Spec.ModelCache.CacheSize

	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: modelCachePVName,
		},
		Spec: corev1.PersistentVolumeSpec{
			Capacity:                      corev1.ResourceList{corev1.ResourceStorage: cacheSize},
			VolumeMode:                    ptr.To(corev1.PersistentVolumeFilesystem),
			AccessModes:                   []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
			StorageClassName:              "local-storage",
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				HostPath: &corev1.HostPathVolumeSource{
					Path: modelCacheHostDir,
					Type: ptr.To(corev1.HostPathDirectoryOrCreate),
				},
			},
			NodeAffinity: &corev1.VolumeNodeAffinity{
				Required: &corev1.NodeSelector{
					NodeSelectorTerms: []corev1.NodeSelectorTerm{{
						MatchExpressions: []corev1.NodeSelectorRequirement{{
							Key:      modelCacheLabelKey,
							Operator: corev1.NodeSelectorOpIn,
							Values:   []string{modelCacheLabelValue},
						}},
					}},
				},
			},
		},
	}

	return pv, nil
}

func (r *KserveModuleReconciler) buildModelCachePVC(kserve *platformv1alpha1.Kserve) (*corev1.PersistentVolumeClaim, error) {
	if kserve.Spec.ModelCache.CacheSize == nil {
		return nil, fmt.Errorf("cacheSize is required when ModelCache is Managed")
	}

	cacheSize := *kserve.Spec.ModelCache.CacheSize

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      modelCachePVCName,
			Namespace: r.getApplicationsNamespace(),
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			VolumeName:       modelCachePVName,
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			VolumeMode:       ptr.To(corev1.PersistentVolumeFilesystem),
			StorageClassName: ptr.To("local-storage"),
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: cacheSize},
			},
		},
	}

	return pvc, nil
}

func (r *KserveModuleReconciler) buildLocalModelNodeGroup(kserve *platformv1alpha1.Kserve) (*unstructured.Unstructured, error) {
	if kserve.Spec.ModelCache.CacheSize == nil {
		return nil, fmt.Errorf("cacheSize is required when ModelCache is Managed")
	}

	cacheSizeStr := kserve.Spec.ModelCache.CacheSize.String()

	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(localModelNodeGroupGVK)
	obj.SetName(localModelNodeGroupName)
	obj.Object["spec"] = map[string]any{
		"storageLimit": cacheSizeStr,
		"persistentVolumeSpec": map[string]any{
			"capacity": map[string]any{
				"storage": cacheSizeStr,
			},
			"volumeMode":                    "Filesystem",
			"accessModes":                   []any{"ReadWriteOnce"},
			"persistentVolumeReclaimPolicy": "Delete",
			"storageClassName":              "local-storage",
			"hostPath": map[string]any{
				"path": modelCacheHostDir,
				"type": "DirectoryOrCreate",
			},
			"nodeAffinity": map[string]any{
				"required": map[string]any{
					"nodeSelectorTerms": []any{
						map[string]any{
							"matchExpressions": []any{
								map[string]any{
									"key":      modelCacheLabelKey,
									"operator": "In",
									"values":   []any{modelCacheLabelValue},
								},
							},
						},
					},
				},
			},
		},
		"persistentVolumeClaimSpec": map[string]any{
			"accessModes": []any{"ReadWriteOnce"},
			"volumeMode":  "Filesystem",
			"resources": map[string]any{
				"requests": map[string]any{
					"storage": cacheSizeStr,
				},
			},
			"storageClassName": "local-storage",
		},
	}

	return obj, nil
}

func (r *KserveModuleReconciler) desiredModelCacheNodes(ctx context.Context, kserve *platformv1alpha1.Kserve) (map[string]struct{}, error) {
	desired := make(map[string]struct{})

	switch {
	case len(kserve.Spec.ModelCache.NodeNames) > 0:
		for _, name := range kserve.Spec.ModelCache.NodeNames {
			node := corev1.Node{}
			if err := r.Get(ctx, client.ObjectKey{Name: name}, &node); err != nil {
				return nil, fmt.Errorf("failed to get node %q: %w", name, err)
			}
			desired[node.Name] = struct{}{}
		}
	case kserve.Spec.ModelCache.NodeSelector != nil:
		sel, err := metav1.LabelSelectorAsSelector(kserve.Spec.ModelCache.NodeSelector)
		if err != nil {
			return nil, fmt.Errorf("failed to convert nodeSelector to selector: %w", err)
		}
		nodeList := &corev1.NodeList{}
		if err := r.List(ctx, nodeList, client.MatchingLabelsSelector{Selector: sel}); err != nil {
			return nil, fmt.Errorf("failed to list nodes matching selector: %w", err)
		}
		for _, node := range nodeList.Items {
			desired[node.Name] = struct{}{}
		}
	}

	return desired, nil
}

func buildNamespacePSAPatch(namespace string, desiredLevel string) (*corev1.Namespace, *corev1.Namespace) {
	original := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: namespace,
		},
	}

	patched := original.DeepCopy()
	if patched.Labels == nil {
		patched.Labels = make(map[string]string)
	}
	if patched.Annotations == nil {
		patched.Annotations = make(map[string]string)
	}

	patched.Labels[securityEnforceLabel] = desiredLevel

	if desiredLevel == "privileged" {
		patched.Annotations[psaElevatedByAnnotation] = psaElevatedByValue
	} else {
		delete(patched.Annotations, psaElevatedByAnnotation)
	}

	return original, patched
}

func modelCachePostRender(
	_ context.Context,
	r *KserveModuleReconciler,
	kserve *platformv1alpha1.Kserve,
	resources []unstructured.Unstructured,
) ([]unstructured.Unstructured, error) {
	enabled := isModelCacheEnabled(kserve)

	resources, err := updateLocalModelConfig(resources, enabled, r.getApplicationsNamespace())
	if err != nil {
		return nil, fmt.Errorf("updating localModel config: %w", err)
	}

	if enabled {
		resources, err = forceReconcileKserveAgentImage(resources)
		if err != nil {
			return nil, fmt.Errorf("reconciling agent image: %w", err)
		}
	}

	return resources, nil
}

func updateLocalModelConfig(resources []unstructured.Unstructured, enabled bool, namespace string) ([]unstructured.Unstructured, error) {
	cmIdx, cm, err := getIndexedResource[corev1.ConfigMap](resources, configMapGVK, kserveConfigMapName)
	if err != nil {
		return resources, nil
	}

	if err := updateCMJSONKey(cm, localModelConfigKeyName, func(data map[string]any) {
		data["enabled"] = enabled
		data["jobNamespace"] = namespace
	}); err != nil {
		return nil, err
	}

	return replaceResourceAtIndex(resources, cmIdx, cm)
}

func forceReconcileKserveAgentImage(resources []unstructured.Unstructured) ([]unstructured.Unstructured, error) {
	expectedImage := os.Getenv(kserveImageParamMap["kserve-agent"])
	if expectedImage == "" {
		return resources, nil
	}

	cmIdx, cm, err := getIndexedResource[corev1.ConfigMap](resources, configMapGVK, kserveConfigMapName)
	if err != nil {
		return resources, nil
	}

	raw, ok := cm.Data[openshiftConfigKeyName]
	if !ok {
		return resources, nil
	}

	var openshiftConfig map[string]any
	if err := json.Unmarshal([]byte(raw), &openshiftConfig); err != nil {
		return nil, fmt.Errorf("parsing %s in ConfigMap: %w", openshiftConfigKeyName, err)
	}

	currentImage, _ := openshiftConfig["modelcachePermissionFixImage"].(string)
	if currentImage == expectedImage {
		return resources, nil
	}

	openshiftConfig["modelcachePermissionFixImage"] = expectedImage
	updated, err := json.MarshalIndent(openshiftConfig, "", " ")
	if err != nil {
		return nil, fmt.Errorf("marshaling %s: %w", openshiftConfigKeyName, err)
	}
	cm.Data[openshiftConfigKeyName] = string(updated)

	return replaceResourceAtIndex(resources, cmIdx, cm)
}

func cleanupModelCacheResources(namespace string) []client.Object {
	lmng := &unstructured.Unstructured{}
	lmng.SetGroupVersionKind(localModelNodeGroupGVK)
	lmng.SetName(localModelNodeGroupName)

	return []client.Object{
		&corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:      modelCachePVCName,
				Namespace: namespace,
			},
		},
		&corev1.PersistentVolume{
			ObjectMeta: metav1.ObjectMeta{
				Name: modelCachePVName,
			},
		},
		lmng,
	}
}
