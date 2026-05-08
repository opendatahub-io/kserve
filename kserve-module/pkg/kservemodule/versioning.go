// [TEMPORARY] LLMInferenceServiceConfig versioning logic adapted from rhods-operator
// (internal/controller/components/kserve/kserve_controller_actions.go).
// Prefixes well-known config names with release version to allow multiple
// versions to coexist during rolling upgrades.
// Remove this comment once the code diverges from the original.
package kservemodule

import (
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

const (
	wellKnownAnnotationKey   = "serving.kserve.io/well-known-config"
	wellKnownAnnotationValue = "true"
	llmISVCConfigPrefix      = "LLM_INFERENCE_SERVICE_CONFIG_PREFIX"
	llmISVCControllerName    = "llmisvc-controller-manager"
	llmISVCConfigGroup       = "serving.kserve.io"
	llmISVCConfigKind        = "LLMInferenceServiceConfig"
)

func versionedWellKnownLLMInferenceServiceConfigs(resources []unstructured.Unstructured, versionPrefix string) ([]unstructured.Unstructured, error) {
	if versionPrefix == "" {
		return resources, nil
	}

	envValue := fmt.Sprintf("%s-kserve-", versionPrefix)

	for i := range resources {
		gvk := resources[i].GroupVersionKind()

		if gvk.Group == llmISVCConfigGroup && gvk.Kind == llmISVCConfigKind {
			ann := resources[i].GetAnnotations()
			if v, ok := ann[wellKnownAnnotationKey]; ok && v == wellKnownAnnotationValue {
				resources[i].SetName(fmt.Sprintf("%s-%s", versionPrefix, resources[i].GetName()))
			}
		}

		if gvk == deploymentGVK && resources[i].GetName() == llmISVCControllerName {
			deploy := &appsv1.Deployment{}
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(resources[i].Object, deploy); err != nil {
				return nil, err
			}

			for j := range deploy.Spec.Template.Spec.Containers {
				c := &deploy.Spec.Template.Spec.Containers[j]
				found := false
				for k := range c.Env {
					if c.Env[k].Name == llmISVCConfigPrefix {
						c.Env[k].Value = envValue
						found = true
						break
					}
				}
				if !found {
					c.Env = append(c.Env, corev1.EnvVar{
						Name:  llmISVCConfigPrefix,
						Value: envValue,
					})
				}
			}

			raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(deploy)
			if err != nil {
				return nil, err
			}
			resources[i] = unstructured.Unstructured{Object: raw}
		}
	}

	return resources, nil
}
