// [TEMPORARY] ConfigMap customization adapted from opendatahub-operator
// (internal/controller/components/kserve/kserve_support.go).
// Remove this comment once the code diverges from the original.
package kservemodule

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	ingressConfigKeyName     = "ingress"
	serviceConfigKeyName     = "service"
	kserveConfigMapName      = "inferenceservice-config"
	isvcControllerDeployment = "kserve-controller-manager"
	configHashAnnotationKey  = "kserve-module/config-hash"
)

var (
	configMapGVK  = schema.GroupVersionKind{Group: "", Version: "v1", Kind: "ConfigMap"}
	deploymentGVK = schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}
)

func customizeKserveConfigMap(resources []unstructured.Unstructured, headless bool) ([]unstructured.Unstructured, error) {
	cmIdx, cm, err := getIndexedResource[corev1.ConfigMap](resources, configMapGVK, kserveConfigMapName)
	if err != nil {
		return resources, nil
	}

	if err := updateInferenceCM(cm, headless); err != nil {
		return nil, err
	}

	resources, err = replaceResourceAtIndex(resources, cmIdx, cm)
	if err != nil {
		return nil, err
	}

	deployIdx, deploy, err := getIndexedResource[appsv1.Deployment](resources, deploymentGVK, isvcControllerDeployment)
	if err != nil {
		return resources, nil
	}

	h := hashConfigMap(cm)
	if deploy.Spec.Template.Annotations == nil {
		deploy.Spec.Template.Annotations = make(map[string]string)
	}
	deploy.Spec.Template.Annotations[configHashAnnotationKey] = h

	resources, err = replaceResourceAtIndex(resources, deployIdx, deploy)
	if err != nil {
		return nil, err
	}

	return resources, nil
}

func updateInferenceCM(cm *corev1.ConfigMap, headless bool) error {
	if err := updateCMJSONKey(cm, ingressConfigKeyName, func(data map[string]any) {
		data["disableIngressCreation"] = true
	}); err != nil {
		return err
	}

	return updateCMJSONKey(cm, serviceConfigKeyName, func(data map[string]any) {
		data["serviceClusterIPNone"] = headless
	})
}

func updateCMJSONKey(cm *corev1.ConfigMap, key string, mutate func(map[string]any)) error {
	raw, ok := cm.Data[key]
	if !ok {
		return nil
	}

	var data map[string]any
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		return fmt.Errorf("parsing configmap key %q: %w", key, err)
	}

	mutate(data)

	out, err := json.MarshalIndent(data, "", " ")
	if err != nil {
		return fmt.Errorf("serializing configmap key %q: %w", key, err)
	}
	cm.Data[key] = string(out)
	return nil
}

func hashConfigMap(cm *corev1.ConfigMap) string {
	raw, _ := json.Marshal(cm.Data)
	h := sha256.Sum256(raw)
	return base64.RawURLEncoding.EncodeToString(h[:])
}

func getIndexedResource[T any](resources []unstructured.Unstructured, gvk schema.GroupVersionKind, name string) (int, *T, error) {
	for i, r := range resources {
		if r.GroupVersionKind() == gvk && r.GetName() == name {
			obj := new(T)
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(r.Object, obj); err != nil {
				return -1, nil, fmt.Errorf("converting %s %q: %w", gvk.Kind, name, err)
			}
			return i, obj, nil
		}
	}
	return -1, nil, fmt.Errorf("%s %q not found", gvk.Kind, name)
}

func replaceResourceAtIndex(resources []unstructured.Unstructured, idx int, obj any) ([]unstructured.Unstructured, error) {
	raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return nil, fmt.Errorf("converting to unstructured: %w", err)
	}
	resources[idx] = unstructured.Unstructured{Object: raw}
	return resources, nil
}
