package kservemodule

import (
	"context"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/opendatahub-io/odh-platform-utilities/api/common"
	odhLabels "github.com/opendatahub-io/odh-platform-utilities/pkg/metadata/labels"

	platformv1alpha1 "github.com/opendatahub-io/kserve-module/pkg/apis/v1alpha1"
)


type componentConfig struct {
	name          string
	sourcePath    string
	sourcePathXKS string
	imageMap      map[string]string
	extraParams   func(kserve *platformv1alpha1.Kserve) map[string]string
	postRender    func(ctx context.Context, r *KserveModuleReconciler,
		kserve *platformv1alpha1.Kserve,
		resources []unstructured.Unstructured) ([]unstructured.Unstructured, error)
	enabled      func(kserve *platformv1alpha1.Kserve) bool
	extraCleanup func(ctx context.Context, r *KserveModuleReconciler) error
}

var components = []componentConfig{
	{
		name:          kserveComponentName,
		sourcePath:    kserveManifestSourcePath,
		sourcePathXKS: kserveManifestSourcePathXKS,
		imageMap:      kserveImageParamMap,
		postRender:    kservePostRender,
	},
	{
		name:        odhModelControllerComponentName,
		sourcePath:  modelControllerSourcePath,
		imageMap:    modelControllerImageParamMap,
		extraParams: modelControllerExtraParams,
	},
	{
		name:       wvaComponentName,
		sourcePath: wvaManifestSourcePathOCP,
		imageMap:   wvaImageParamMap,
		enabled:    isWVAEnabled,
	},
}

func kservePostRender(ctx context.Context, r *KserveModuleReconciler,
	kserve *platformv1alpha1.Kserve,
	resources []unstructured.Unstructured) ([]unstructured.Unstructured, error) {

	isHeadless := kserve.Spec.RawDeploymentServiceConfig != platformv1alpha1.KserveRawHeaded
	resources, err := customizeKserveConfigMap(resources, isHeadless)
	if err != nil {
		return nil, fmt.Errorf("customizing configmap: %w", err)
	}

	versionPrefix := r.getVersionPrefix(kserve)
	resources, err = versionedWellKnownLLMInferenceServiceConfigs(resources, versionPrefix)
	if err != nil {
		return nil, fmt.Errorf("versioning LLMInferenceServiceConfigs: %w", err)
	}

	return resources, nil
}

func isWVAEnabled(kserve *platformv1alpha1.Kserve) bool {
	return kserve.Spec.WVA.ManagementState == common.Managed
}

func modelControllerExtraParams(kserve *platformv1alpha1.Kserve) map[string]string {
	nimState := string(common.Managed)
	if kserve.Spec.NIM.ManagementState == common.Removed {
		nimState = string(common.Removed)
	}
	return map[string]string{
		"nim-state":    strings.ToLower(nimState),
		"kserve-state": strings.ToLower(string(platformv1alpha1.GetManagementState(kserve))),
	}
}

func commonPostRender(resources []unstructured.Unstructured, componentName string) {
	applyManagedByLabel(resources, componentName)
}

func applyManagedByLabel(resources []unstructured.Unstructured, componentName string) {
	for i := range resources {
		labels := resources[i].GetLabels()
		if labels == nil {
			labels = make(map[string]string)
		}
		labels[odhLabels.PlatformPartOf] = componentName
		resources[i].SetLabels(labels)
	}
}

