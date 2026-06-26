package kservemodule

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8serr "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
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

	modelCachePVName         = "kserve-localmodelnode-pv"
	modelCachePVCName        = "kserve-localmodelnode-pvc"
	modelCacheHostDir        = "/var/lib/kserve/models"
	modelCacheStorageClass   = "local-storage"

	localModelNodeGroupName = "workers"

	localModelConfigKeyName = "localModel"
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

func buildModelCachePV(cacheSize resource.Quantity) (*corev1.PersistentVolume, error) {
	pv := &corev1.PersistentVolume{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "PersistentVolume"},
		ObjectMeta: metav1.ObjectMeta{Name: modelCachePVName},
		Spec: corev1.PersistentVolumeSpec{
			Capacity:                      corev1.ResourceList{corev1.ResourceStorage: cacheSize},
			VolumeMode:                    ptr.To(corev1.PersistentVolumeFilesystem),
			AccessModes:                   []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
			StorageClassName:              modelCacheStorageClass,
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

func buildModelCachePVC(cacheSize resource.Quantity, namespace string) (*corev1.PersistentVolumeClaim, error) {
	pvc := &corev1.PersistentVolumeClaim{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "PersistentVolumeClaim"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      modelCachePVCName,
			Namespace: namespace,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			VolumeName:       modelCachePVName,
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			VolumeMode:       ptr.To(corev1.PersistentVolumeFilesystem),
			StorageClassName: ptr.To(modelCacheStorageClass),
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: cacheSize},
			},
		},
	}
	return pvc, nil
}

func buildLocalModelNodeGroup(cacheSizeStr string) unstructured.Unstructured {
	obj := unstructured.Unstructured{Object: map[string]any{
		"apiVersion": localModelNodeGroupGVK.Group + "/" + localModelNodeGroupGVK.Version,
		"kind":       localModelNodeGroupGVK.Kind,
		"metadata": map[string]any{
			"name": localModelNodeGroupName,
		},
		"spec": map[string]any{
			"storageLimit": cacheSizeStr,
			"persistentVolumeSpec": map[string]any{
				"capacity": map[string]any{
					"storage": cacheSizeStr,
				},
				"volumeMode":                    "Filesystem",
				"accessModes":                   []any{"ReadWriteOnce"},
				"persistentVolumeReclaimPolicy": "Delete",
				"storageClassName":              modelCacheStorageClass,
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
				"storageClassName": modelCacheStorageClass,
			},
		},
	}}
	return obj
}

func buildModelCacheResources(kserve *platformv1alpha1.Kserve, namespace string) ([]unstructured.Unstructured, error) {
	if kserve.Spec.ModelCache.CacheSize == nil {
		return nil, fmt.Errorf("cacheSize is required when ModelCache is Managed")
	}
	cacheSize := *kserve.Spec.ModelCache.CacheSize

	pv, err := buildModelCachePV(cacheSize)
	if err != nil {
		return nil, err
	}
	pvU, err := runtime.DefaultUnstructuredConverter.ToUnstructured(pv)
	if err != nil {
		return nil, fmt.Errorf("converting PV to unstructured: %w", err)
	}

	pvc, err := buildModelCachePVC(cacheSize, namespace)
	if err != nil {
		return nil, err
	}
	pvcU, err := runtime.DefaultUnstructuredConverter.ToUnstructured(pvc)
	if err != nil {
		return nil, fmt.Errorf("converting PVC to unstructured: %w", err)
	}

	lmng := buildLocalModelNodeGroup(cacheSize.String())

	return []unstructured.Unstructured{
		{Object: pvU},
		{Object: pvcU},
		lmng,
	}, nil
}

func (r *KserveModuleReconciler) ensureModelCacheNamespace(ctx context.Context) error {
	log := ctrl.LoggerFrom(ctx)

	ns := &corev1.Namespace{}
	err := r.Get(ctx, client.ObjectKey{Name: modelCacheNamespace}, ns)
	if err != nil && !k8serr.IsNotFound(err) {
		return fmt.Errorf("getting model cache namespace %q: %w", modelCacheNamespace, err)
	}

	if k8serr.IsNotFound(err) {
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: modelCacheNamespace,
				Labels: map[string]string{
					securityEnforceLabel: "privileged",
				},
			},
		}
		if createErr := r.Create(ctx, ns); createErr != nil {
			return fmt.Errorf("creating model cache namespace %q: %w", modelCacheNamespace, createErr)
		}
		log.Info("Created model cache namespace", "namespace", modelCacheNamespace)
		return nil
	}

	if ns.Labels == nil {
		ns.Labels = make(map[string]string)
	}
	if ns.Labels[securityEnforceLabel] == "privileged" {
		return nil
	}

	original := ns.DeepCopy()
	ns.Labels[securityEnforceLabel] = "privileged"
	if patchErr := r.Patch(ctx, ns, client.MergeFrom(original)); patchErr != nil {
		return fmt.Errorf("patching model cache namespace PSA label: %w", patchErr)
	}
	log.Info("Updated model cache namespace PSA enforcement level", "namespace", modelCacheNamespace, "level", "privileged")
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
	for _, obj := range modelCacheResources(modelCacheNamespace) {
		if err := deleteResourceIfPresent(ctx, r.Client, obj); err != nil {
			return err
		}
	}

	return r.unlabelAllModelCacheNodes(ctx)
}

func (r *KserveModuleReconciler) unlabelAllModelCacheNodes(ctx context.Context) error {
	log := ctrl.LoggerFrom(ctx)

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
	if kserve == nil {
		return resources, nil
	}

	if err := r.ensureModelCacheNamespace(ctx); err != nil {
		return nil, fmt.Errorf("ensuring model cache namespace: %w", err)
	}
	if err := r.labelModelCacheNodes(ctx, kserve); err != nil {
		return nil, fmt.Errorf("labeling model cache nodes: %w", err)
	}

	extra, err := buildModelCacheResources(kserve, modelCacheNamespace)
	if err != nil {
		return nil, fmt.Errorf("building modelcache resources: %w", err)
	}
	resources = append(resources, extra...)

	resources, err = patchModelCacheConfigNamespaceEnv(resources, r.getApplicationsNamespace())
	if err != nil {
		return nil, fmt.Errorf("patching modelcache config namespace env: %w", err)
	}

	if !r.isKubernetes(ctx) {
		mcsLevel, err := r.resolveNamespaceMCSLevel(ctx, modelCacheNamespace)
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

func (r *KserveModuleReconciler) checkModelCacheReadiness(ctx context.Context) error {
	if err := checkDeploymentsReady(ctx, r.Client, modelCacheNamespace, []string{localmodelControllerDeployment}); err != nil {
		return err
	}

	pv := &corev1.PersistentVolume{}
	if err := r.Get(ctx, client.ObjectKey{Name: modelCachePVName}, pv); err != nil {
		return fmt.Errorf("PV %s: %w", modelCachePVName, err)
	}

	pvc := &corev1.PersistentVolumeClaim{}
	if err := r.Get(ctx, client.ObjectKey{Name: modelCachePVCName, Namespace: modelCacheNamespace}, pvc); err != nil {
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

func patchModelCacheConfigNamespaceEnv(resources []unstructured.Unstructured, configNamespace string) ([]unstructured.Unstructured, error) {
	var err error

	deployIdx, deploy, deployErr := getIndexedResource[appsv1.Deployment](resources, deploymentGVK, localmodelControllerDeployment)
	if deployErr == nil {
		if len(deploy.Spec.Template.Spec.Containers) == 0 {
			return nil, fmt.Errorf("deployment %q has no containers", localmodelControllerDeployment)
		}
		deploy.Spec.Template.Spec.Containers[0].Env = upsertContainerEnv(
			deploy.Spec.Template.Spec.Containers[0].Env,
			kserveConfigNamespaceEnv,
			configNamespace,
		)
		resources, err = replaceResourceAtIndex(resources, deployIdx, deploy)
		if err != nil {
			return nil, err
		}
	} else if !errors.Is(deployErr, errResourceNotFound) {
		return nil, deployErr
	}

	dsIdx, ds, dsErr := getIndexedResource[appsv1.DaemonSet](resources, daemonSetGVK, localModelNodeAgentDaemonSetName)
	if dsErr == nil {
		if len(ds.Spec.Template.Spec.Containers) == 0 {
			return nil, fmt.Errorf("daemonset %q has no containers", localModelNodeAgentDaemonSetName)
		}
		ds.Spec.Template.Spec.Containers[0].Env = upsertContainerEnv(
			ds.Spec.Template.Spec.Containers[0].Env,
			kserveConfigNamespaceEnv,
			configNamespace,
		)
		resources, err = replaceResourceAtIndex(resources, dsIdx, ds)
		if err != nil {
			return nil, err
		}
	} else if !errors.Is(dsErr, errResourceNotFound) {
		return nil, dsErr
	}

	return resources, nil
}

func upsertContainerEnv(envs []corev1.EnvVar, name, value string) []corev1.EnvVar {
	for i := range envs {
		if envs[i].Name == name {
			envs[i].Value = value
			return envs
		}
	}
	return append(envs, corev1.EnvVar{Name: name, Value: value})
}

func (r *KserveModuleReconciler) checkModelCacheDaemonSetMCS(ctx context.Context) error {
	ds := &appsv1.DaemonSet{}
	key := client.ObjectKey{
		Name:      localModelNodeAgentDaemonSetName,
		Namespace: modelCacheNamespace,
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

	expectedMCS, err := r.resolveNamespaceMCSLevel(ctx, modelCacheNamespace)
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
