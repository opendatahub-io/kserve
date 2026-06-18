package kservemodule

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8serr "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/opendatahub-io/odh-platform-utilities/api/common"

	platformv1alpha1 "github.com/opendatahub-io/kserve-module/pkg/apis/v1alpha1"
)

const (
	modelCacheLabelKey   = "kserve/localmodel"
	modelCacheLabelValue = "worker"

	modelCachePVName         = "kserve-localmodelnode-pv"
	modelCachePVCName        = "kserve-localmodelnode-pvc"
	modelCacheHostDir        = "/var/lib/kserve/models"
	modelCacheStorageClass   = "local-storage"

	localModelNodeGroupName = "workers"

	localModelConfigKeyName = "localModel"
	openshiftConfigKeyName  = "openshiftConfig"
	psaElevatedByAnnotation = "opendatahub.io/psa-elevated-by"
	psaElevatedByValue      = "kserve-modelcache"
	securityEnforceLabel    = "pod-security.kubernetes.io/enforce"

	openshiftSCCMCSAnnotation        = "openshift.io/sa.scc.mcs"
	localModelNodeAgentDaemonSetName = "kserve-localmodelnode-agent"

	modelCacheReasonNamespaceMCSMissing = "NamespaceMCSMissing"
	modelCacheReasonSELinuxMCSMismatch  = "SELinuxMCSMismatch"
	modelCacheReasonResourcesNotReady   = "ResourcesNotReady"
)

// validMCSLevel matches openshift.io/sa.scc.mcs values.
var validMCSLevel = regexp.MustCompile(`^s\d+(-s\d+)?(:c\d{1,4}([,.]c\d{1,4})*)?$`)

var localModelNodeGroupGVK = schema.GroupVersionKind{
	Group:   "serving.kserve.io",
	Version: "v1alpha1",
	Kind:    "LocalModelNodeGroup",
}

func isModelCacheEnabled(kserve *platformv1alpha1.Kserve) bool {
	return kserve.Spec.ModelCache != nil && kserve.Spec.ModelCache.ManagementState == common.Managed
}

func (r *KserveModuleReconciler) reconcileModelCacheResources(ctx context.Context, kserve *platformv1alpha1.Kserve) error {
	if err := r.updateNamespacePSA(ctx, "privileged"); err != nil {
		return err
	}

	if err := r.createOrUpdateModelCachePV(ctx, kserve); err != nil {
		return err
	}

	if err := r.createOrUpdateModelCachePVC(ctx, kserve); err != nil {
		return err
	}

	if err := r.createOrUpdateLocalModelNodeGroup(ctx, kserve); err != nil {
		return err
	}

	return r.labelModelCacheNodes(ctx, kserve)
}

func (r *KserveModuleReconciler) createOrUpdateModelCachePV(ctx context.Context, kserve *platformv1alpha1.Kserve) error {
	log := ctrl.LoggerFrom(ctx)

	if kserve.Spec.ModelCache.CacheSize == nil {
		return fmt.Errorf("cacheSize is required when ModelCache is Managed")
	}
	cacheSize := *kserve.Spec.ModelCache.CacheSize

	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: modelCachePVName},
	}
	result, err := controllerutil.CreateOrUpdate(ctx, r.Client, pv, func() error {
		pv.Spec.Capacity = corev1.ResourceList{corev1.ResourceStorage: cacheSize}
		pv.Spec.VolumeMode = ptr.To(corev1.PersistentVolumeFilesystem)
		pv.Spec.AccessModes = []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}
		pv.Spec.PersistentVolumeReclaimPolicy = corev1.PersistentVolumeReclaimRetain
		pv.Spec.StorageClassName = modelCacheStorageClass
		pv.Spec.PersistentVolumeSource = corev1.PersistentVolumeSource{
			HostPath: &corev1.HostPathVolumeSource{
				Path: modelCacheHostDir,
				Type: ptr.To(corev1.HostPathDirectoryOrCreate),
			},
		}
		pv.Spec.NodeAffinity = &corev1.VolumeNodeAffinity{
			Required: &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{{
					MatchExpressions: []corev1.NodeSelectorRequirement{{
						Key:      modelCacheLabelKey,
						Operator: corev1.NodeSelectorOpIn,
						Values:   []string{modelCacheLabelValue},
					}},
				}},
			},
		}
		return controllerutil.SetControllerReference(kserve, pv, r.Scheme)
	})
	if err != nil {
		return fmt.Errorf("failed to create/update model cache PV: %w", err)
	}
	log.Info("Reconciled model cache PV", "result", result)
	return nil
}

func (r *KserveModuleReconciler) createOrUpdateModelCachePVC(ctx context.Context, kserve *platformv1alpha1.Kserve) error {
	log := ctrl.LoggerFrom(ctx)

	if kserve.Spec.ModelCache.CacheSize == nil {
		return fmt.Errorf("cacheSize is required when ModelCache is Managed")
	}
	cacheSize := *kserve.Spec.ModelCache.CacheSize

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      modelCachePVCName,
			Namespace: r.getApplicationsNamespace(),
		},
	}
	result, err := controllerutil.CreateOrUpdate(ctx, r.Client, pvc, func() error {
		// Only set immutable fields on create (when VolumeName is empty).
		// On update, these fields are rejected by the API server.
		if pvc.Spec.VolumeName == "" {
			pvc.Spec.VolumeName = modelCachePVName
			pvc.Spec.AccessModes = []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}
			pvc.Spec.VolumeMode = ptr.To(corev1.PersistentVolumeFilesystem)
			pvc.Spec.StorageClassName = ptr.To(modelCacheStorageClass)
		}
		pvc.Spec.Resources = corev1.VolumeResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceStorage: cacheSize},
		}

		return controllerutil.SetControllerReference(kserve, pvc, r.Scheme)
	})
	if err != nil {
		return fmt.Errorf("failed to create/update model cache PVC: %w", err)
	}
	log.Info("Reconciled model cache PVC", "result", result)
	return nil
}

func (r *KserveModuleReconciler) createOrUpdateLocalModelNodeGroup(ctx context.Context, kserve *platformv1alpha1.Kserve) error {
	log := ctrl.LoggerFrom(ctx)

	if kserve.Spec.ModelCache.CacheSize == nil {
		return fmt.Errorf("cacheSize is required when ModelCache is Managed")
	}
	cacheSizeStr := kserve.Spec.ModelCache.CacheSize.String()

	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(localModelNodeGroupGVK)
	obj.SetName(localModelNodeGroupName)

	result, err := controllerutil.CreateOrUpdate(ctx, r.Client, obj, func() error {
		obj.Object["spec"] = map[string]interface{}{
			"storageLimit": cacheSizeStr,
			"persistentVolumeSpec": map[string]interface{}{
				"capacity": map[string]interface{}{
					"storage": cacheSizeStr,
				},
				"volumeMode":                    "Filesystem",
				"accessModes":                   []interface{}{"ReadWriteOnce"},
				"persistentVolumeReclaimPolicy": "Delete",
				"storageClassName":              modelCacheStorageClass,
				"hostPath": map[string]interface{}{
					"path": modelCacheHostDir,
					"type": "DirectoryOrCreate",
				},
				"nodeAffinity": map[string]interface{}{
					"required": map[string]interface{}{
						"nodeSelectorTerms": []interface{}{
							map[string]interface{}{
								"matchExpressions": []interface{}{
									map[string]interface{}{
										"key":      modelCacheLabelKey,
										"operator": "In",
										"values":   []interface{}{modelCacheLabelValue},
									},
								},
							},
						},
					},
				},
			},
			"persistentVolumeClaimSpec": map[string]interface{}{
				"accessModes": []interface{}{"ReadWriteOnce"},
				"volumeMode":  "Filesystem",
				"resources": map[string]interface{}{
					"requests": map[string]interface{}{
						"storage": cacheSizeStr,
					},
				},
				"storageClassName": modelCacheStorageClass,
			},
		}
		return controllerutil.SetControllerReference(kserve, obj, r.Scheme)

	})
	if err != nil {
		return fmt.Errorf("failed to create/update LocalModelNodeGroup: %w", err)
	}
	log.Info("Reconciled LocalModelNodeGroup", "result", result)
	return nil
}

func (r *KserveModuleReconciler) updateNamespacePSA(ctx context.Context, desiredLevel string) error {
	log := ctrl.LoggerFrom(ctx)

	ns := &corev1.Namespace{}
	if err := r.Get(ctx, client.ObjectKey{Name: r.getApplicationsNamespace()}, ns); err != nil {
		return fmt.Errorf("failed to get application namespace: %w", err)
	}

	current := ns.Labels[securityEnforceLabel]
	currentAnnotation := ns.Annotations[psaElevatedByAnnotation]
	needsUpdate := false

	if desiredLevel == "privileged" {
		if current != desiredLevel || currentAnnotation != psaElevatedByValue {
			needsUpdate = true
		}
	} else if currentAnnotation == psaElevatedByValue {
		needsUpdate = true
	}

	if !needsUpdate {
		return nil
	}

	original := ns.DeepCopy()

	if ns.Labels == nil {
		ns.Labels = make(map[string]string)
	}
	if ns.Annotations == nil {
		ns.Annotations = make(map[string]string)
	}

	if desiredLevel == "privileged" {
		ns.Labels[securityEnforceLabel] = desiredLevel
		ns.Annotations[psaElevatedByAnnotation] = psaElevatedByValue
	} else {
		ns.Labels[securityEnforceLabel] = desiredLevel
		delete(ns.Annotations, psaElevatedByAnnotation)
	}

	if err := r.Patch(ctx, ns, client.MergeFrom(original)); err != nil {
		return fmt.Errorf("failed to update namespace PSA label: %w", err)
	}

	log.Info("Updated namespace PSA enforcement level", "namespace", ns.Name, "from", current, "to", desiredLevel)
	return nil
}

func (r *KserveModuleReconciler) labelModelCacheNodes(ctx context.Context, kserve *platformv1alpha1.Kserve) error {
	log := ctrl.LoggerFrom(ctx)

	var nodes []corev1.Node

	if len(kserve.Spec.ModelCache.NodeNames) == 0 && kserve.Spec.ModelCache.NodeSelector == nil {
		return fmt.Errorf("no nodeNames or nodeSelector specified for model cache")
	}

	switch {
	case len(kserve.Spec.ModelCache.NodeNames) > 0:
		for _, name := range kserve.Spec.ModelCache.NodeNames {
			node := corev1.Node{}
			if err := r.Client.Get(ctx, client.ObjectKey{Name: name}, &node); err != nil {
				return fmt.Errorf("failed to get node %q: %w", name, err)
			}
			nodes = append(nodes, node)
		}
	case kserve.Spec.ModelCache.NodeSelector != nil:
		sel, err := metav1.LabelSelectorAsSelector(kserve.Spec.ModelCache.NodeSelector)
		if err != nil {
			return fmt.Errorf("failed to convert nodeSelector to selector: %w", err)
		}
		nodeList := &corev1.NodeList{}
		if err := r.Client.List(ctx, nodeList, client.MatchingLabelsSelector{Selector: sel}); err != nil {
			return fmt.Errorf("failed to list nodes matching selector: %w", err)
		}
		nodes = nodeList.Items
	}

	desiredNodes := make(map[string]struct{}, len(nodes))
	for i := range nodes {
		node := &nodes[i]
		desiredNodes[node.Name] = struct{}{}
		if node.Labels[modelCacheLabelKey] == modelCacheLabelValue {
			continue
		}
		original := node.DeepCopy()
		if node.Labels == nil {
			node.Labels = make(map[string]string)
		}
		node.Labels[modelCacheLabelKey] = modelCacheLabelValue
		if err := r.Client.Patch(ctx, node, client.MergeFrom(original)); err != nil {
			return fmt.Errorf("failed to label node %q: %w", node.Name, err)
		}
		log.Info("Labeled node for model cache", "node", node.Name)
	}
	allLabeled := &corev1.NodeList{}
	if err := r.Client.List(ctx, allLabeled, client.MatchingLabels{modelCacheLabelKey: modelCacheLabelValue}); err != nil {
		return fmt.Errorf("failed to list labeled nodes: %w", err)
	}
	for i := range allLabeled.Items {
		node := &allLabeled.Items[i]
		if _, desired := desiredNodes[node.Name]; !desired {
			original := node.DeepCopy()
			delete(node.Labels, modelCacheLabelKey)
			if err := r.Client.Patch(ctx, node, client.MergeFrom(original)); err != nil {
				return fmt.Errorf("failed to unlabel node %q: %w", node.Name, err)
			}
			log.Info("Removed stale model cache label from node", "node", node.Name)
		}
	}

	return nil
}

func (r *KserveModuleReconciler) cleanupModelCache(ctx context.Context) error {
	log := ctrl.LoggerFrom(ctx)

	if err := r.updateNamespacePSA(ctx, "baseline"); err != nil {
		return err
	}

	for _, obj := range modelCacheResources(r.getApplicationsNamespace()) {
		if err := deleteResourceIfPresent(ctx, r.Client, obj); err != nil {
			return err
		}
	}

	nodeList := &corev1.NodeList{}
	if err := r.List(ctx, nodeList, client.MatchingLabels{modelCacheLabelKey: modelCacheLabelValue}); err != nil {
		return fmt.Errorf("failed to list model cache nodes: %w", err)
	}
	for i := range nodeList.Items {
		node := &nodeList.Items[i]
		original := node.DeepCopy()
		delete(node.Labels, modelCacheLabelKey)
		if err := r.Patch(ctx, node, client.MergeFrom(original)); err != nil {
			return fmt.Errorf("failed to unlabel node %q: %w", node.Name, err)
		}
		log.Info("Removed model cache label from node", "node", node.Name)
	}

	return nil
}

func modelCacheComponentPostRender(
	ctx context.Context,
	r *KserveModuleReconciler,
	kserve *platformv1alpha1.Kserve,
	resources []unstructured.Unstructured,
) ([]unstructured.Unstructured, error) {
	// kserve is nil when called from defaultCleanup; skip imperative creation.
	if kserve == nil {
		return resources, nil
	}
	if err := r.reconcileModelCacheResources(ctx, kserve); err != nil {
		return nil, fmt.Errorf("reconciling modelcache resources: %w", err)
	}

	if !r.isKubernetes(ctx) {
		mcsLevel, err := r.resolveNamespaceMCSLevel(ctx, r.getApplicationsNamespace())
		if err != nil {
			return nil, fmt.Errorf("resolving namespace MCS level: %w", err)
		}
		resources, err = patchLocalModelNodeAgentMCSLevel(resources, mcsLevel)
		if err != nil {
			return nil, fmt.Errorf("patching localmodelnode-agent MCS level: %w", err)
		}
	}

	return resources, nil
}

func cleanupModelCacheComponent(ctx context.Context, r *KserveModuleReconciler) error {
	return r.cleanupModelCache(ctx)
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

func (r *KserveModuleReconciler) checkModelCacheReadiness(ctx context.Context) error {
	if err := checkDeploymentsReady(ctx, r.Client, r.getApplicationsNamespace(), []string{localmodelControllerDeployment}); err != nil {
		return err
	}

	pv := &corev1.PersistentVolume{}
	if err := r.Get(ctx, client.ObjectKey{Name: modelCachePVName}, pv); err != nil {
		return fmt.Errorf("PV %s: %w", modelCachePVName, err)
	}

	pvc := &corev1.PersistentVolumeClaim{}
	if err := r.Get(ctx, client.ObjectKey{Name: modelCachePVCName, Namespace: r.getApplicationsNamespace()}, pvc); err != nil {
		return fmt.Errorf("PVC %s: %w", modelCachePVCName, err)
	}

	lmng := &unstructured.Unstructured{}
	lmng.SetGroupVersionKind(localModelNodeGroupGVK)
	if err := r.Get(ctx, client.ObjectKey{Name: localModelNodeGroupName}, lmng); err != nil {
		if meta.IsNoMatchError(err) {
			return fmt.Errorf("LocalModelNodeGroup CRD not installed")
		}
		return fmt.Errorf("LocalModelNodeGroup %s: %w", localModelNodeGroupName, err)
	}

	if !r.isKubernetes(ctx) {
		if err := r.checkModelCacheDaemonSetMCS(ctx); err != nil {
			return err
		}
	}

	return nil
}

func modelCacheResources(namespace string) []client.Object {
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

type modelCacheReadinessError struct {
	reason string
	msg    string
}

func (e *modelCacheReadinessError) Error() string {
	return e.msg
}

func newModelCacheReadinessError(reason, msg string) error {
	return &modelCacheReadinessError{reason: reason, msg: msg}
}

func modelCacheReadinessReason(err error) string {
	var readinessErr *modelCacheReadinessError
	if errors.As(err, &readinessErr) {
		return readinessErr.reason
	}
	return modelCacheReasonResourcesNotReady
}

func (r *KserveModuleReconciler) resolveNamespaceMCSLevel(ctx context.Context, namespace string) (string, error) {
	ns := &corev1.Namespace{}
	if err := r.Get(ctx, client.ObjectKey{Name: namespace}, ns); err != nil {
		return "", fmt.Errorf("getting namespace %q: %w", namespace, err)
	}

	mcs, ok := ns.Annotations[openshiftSCCMCSAnnotation]
	if !ok {
		return "", newModelCacheReadinessError(
			modelCacheReasonNamespaceMCSMissing,
			fmt.Sprintf("namespace %q is missing annotation %q", namespace, openshiftSCCMCSAnnotation),
		)
	}

	mcs = strings.TrimSpace(mcs)
	if mcs == "" {
		return "", newModelCacheReadinessError(
			modelCacheReasonNamespaceMCSMissing,
			fmt.Sprintf("namespace %q has empty annotation %q", namespace, openshiftSCCMCSAnnotation),
		)
	}

	if !validMCSLevel.MatchString(mcs) {
		return "", fmt.Errorf("invalid MCS level from namespace annotation: %q", mcs)
	}

	return mcs, nil
}

func patchLocalModelNodeAgentMCSLevel(resources []unstructured.Unstructured, mcsLevel string) ([]unstructured.Unstructured, error) {
	log := ctrl.Log.WithName("modelcache")

	dsIdx, ds, err := getIndexedResource[appsv1.DaemonSet](resources, daemonSetGVK, localModelNodeAgentDaemonSetName)
	if err != nil {
		if errors.Is(err, errResourceNotFound) {
			log.Info("DaemonSet not found in rendered resources, skipping MCS patch", "name", localModelNodeAgentDaemonSetName)
			return resources, nil
		}
		return nil, err
	}

	if ds.Spec.Template.Spec.SecurityContext == nil {
		ds.Spec.Template.Spec.SecurityContext = &corev1.PodSecurityContext{}
	}
	ds.Spec.Template.Spec.SecurityContext.SELinuxOptions = &corev1.SELinuxOptions{
		Level: mcsLevel,
	}

	return replaceResourceAtIndex(resources, dsIdx, ds)
}

func (r *KserveModuleReconciler) checkModelCacheDaemonSetMCS(ctx context.Context) error {
	ds := &appsv1.DaemonSet{}
	key := client.ObjectKey{
		Name:      localModelNodeAgentDaemonSetName,
		Namespace: r.getApplicationsNamespace(),
	}
	if err := r.Get(ctx, key, ds); err != nil {
		if k8serr.IsNotFound(err) {
			return newModelCacheReadinessError(
				modelCacheReasonResourcesNotReady,
				fmt.Sprintf("DaemonSet %s not found", localModelNodeAgentDaemonSetName),
			)
		}
		return fmt.Errorf("DaemonSet %s: %w", localModelNodeAgentDaemonSetName, err)
	}

	expectedMCS, err := r.resolveNamespaceMCSLevel(ctx, r.getApplicationsNamespace())
	if err != nil {
		return err
	}

	actualMCS := ""
	if ds.Spec.Template.Spec.SecurityContext != nil && ds.Spec.Template.Spec.SecurityContext.SELinuxOptions != nil {
		actualMCS = ds.Spec.Template.Spec.SecurityContext.SELinuxOptions.Level
	}

	if actualMCS != expectedMCS {
		return newModelCacheReadinessError(
			modelCacheReasonSELinuxMCSMismatch,
			fmt.Sprintf(
				`DaemonSet MCS level %q does not match namespace MCS %q`,
				actualMCS,
				expectedMCS,
			),
		)
	}

	return nil
}
