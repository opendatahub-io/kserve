package kservemodule

import (
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

// isWellKnownConfig reports whether the object is an LLMInferenceServiceConfig
// preset shipped by this module, as opposed to one authored by a user.
func isWellKnownConfig(obj *unstructured.Unstructured) bool {
	if obj.GroupVersionKind().GroupKind() != llmISVCConfigGVK.GroupKind() {
		return false
	}
	return obj.GetAnnotations()[wellKnownAnnotationKey] == wellKnownAnnotationValue
}

// isShippedPreset reports whether the object is a preset this module applies,
// as opposed to a copy a user made in their own namespace.
func isShippedPreset(obj *unstructured.Unstructured, applicationsNS string) bool {
	return obj.GetNamespace() == applicationsNS && isWellKnownConfig(obj)
}

// wellKnownPresetNames returns the names of the presets in a rendered resource
// set, after versionedWellKnownLLMInferenceServiceConfigs has prefixed them.
func wellKnownPresetNames(resources []unstructured.Unstructured) []string {
	var names []string
	for i := range resources {
		if isWellKnownConfig(&resources[i]) {
			names = append(names, resources[i].GetName())
		}
	}
	return names
}

func versionedWellKnownLLMInferenceServiceConfigs(resources []unstructured.Unstructured, versionPrefix string) ([]unstructured.Unstructured, error) {
	if versionPrefix == "" {
		return resources, nil
	}

	envValue := fmt.Sprintf("%s-kserve-", versionPrefix)

	for i := range resources {
		gvk := resources[i].GroupVersionKind()

		if isWellKnownConfig(&resources[i]) {
			resources[i].SetName(fmt.Sprintf("%s-%s", versionPrefix, resources[i].GetName()))
		}

		if gvk == deploymentGVK && resources[i].GetName() == llmISVCControllerDeployment {
			deploy := &appsv1.Deployment{}
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(resources[i].Object, deploy); err != nil {
				return nil, err
			}

			for j := range deploy.Spec.Template.Spec.Containers {
				c := &deploy.Spec.Template.Spec.Containers[j]
				found := false
				for k := range c.Env {
					if c.Env[k].Name == llmISVCConfigPrefixEnv {
						c.Env[k].Value = envValue
						found = true
						break
					}
				}
				if !found {
					c.Env = append(c.Env, corev1.EnvVar{
						Name:  llmISVCConfigPrefixEnv,
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
