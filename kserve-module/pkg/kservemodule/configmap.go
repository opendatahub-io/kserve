package kservemodule

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	platformv1alpha1 "github.com/opendatahub-io/kserve-module/pkg/apis/v1alpha1"
)


var (
	errResourceNotFound = errors.New("resource not found")
	configMapGVK        = schema.GroupVersionKind{Group: "", Version: "v1", Kind: "ConfigMap"}
	deploymentGVK       = schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}
	daemonSetGVK        = schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "DaemonSet"}
)

func customizeKserveConfigMap(resources []unstructured.Unstructured, kserve *platformv1alpha1.Kserve, applicationsNamespace string) ([]unstructured.Unstructured, error) {
	cmIdx, cm, err := getIndexedResource[corev1.ConfigMap](resources, configMapGVK, kserveConfigMapName)
	if err != nil {
		if errors.Is(err, errResourceNotFound) {
			return resources, nil
		}
		return nil, err
	}

	if err := updateInferenceCM(cm, kserve, applicationsNamespace); err != nil {
		return nil, err
	}

	resources, err = replaceResourceAtIndex(resources, cmIdx, cm)
	if err != nil {
		return nil, err
	}

	deployIdx, deploy, err := getIndexedResource[appsv1.Deployment](resources, deploymentGVK, kserveControllerDeployment)
	if err != nil {
		if errors.Is(err, errResourceNotFound) {
			return resources, nil
		}
		return nil, err
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

func updateInferenceCM(cm *corev1.ConfigMap, kserve *platformv1alpha1.Kserve, applicationsNamespace string) error {
	headless := kserve.Spec.RawDeploymentServiceConfig != platformv1alpha1.KserveRawHeaded

	if err := updateCMJSONKey(cm, ingressConfigKeyName, func(data map[string]any) {
		data["disableIngressCreation"] = true
		if kserve.Spec.EnableLLMInferenceServiceTLS != nil {
			data["enableLLMInferenceServiceTLS"] = *kserve.Spec.EnableLLMInferenceServiceTLS
		}
	}); err != nil {
		return err
	}

	if err := updateCMJSONKey(cm, serviceConfigKeyName, func(data map[string]any) {
		data["serviceClusterIPNone"] = headless
	}); err != nil {
		return err
	}

	oauthProxy := kserve.Spec.OAuthProxy
	if oauthProxy != nil && oauthProxy.Resources != nil {
		if err := updateCMJSONKey(cm, oauthProxyConfigKeyName, func(data map[string]any) {
			if v, ok := oauthProxy.Resources.Requests[corev1.ResourceMemory]; ok {
				data["memoryRequest"] = v.String()
			}
			if v, ok := oauthProxy.Resources.Limits[corev1.ResourceMemory]; ok {
				data["memoryLimit"] = v.String()
			}
			if v, ok := oauthProxy.Resources.Requests[corev1.ResourceCPU]; ok {
				data["cpuRequest"] = v.String()
			}
			if v, ok := oauthProxy.Resources.Limits[corev1.ResourceCPU]; ok {
				data["cpuLimit"] = v.String()
			}
		}); err != nil {
			return err
		}
	}

	modelCacheEnabled := kserve.Spec.ModelCache != nil && kserve.Spec.ModelCache.ManagementState == "Managed"
	if err := updateCMJSONKey(cm, localModelConfigKeyName, func(data map[string]any) {
		data["enabled"] = modelCacheEnabled
		data["jobNamespace"] = applicationsNamespace
	}); err != nil {
		return err
	}

	if agentImage := os.Getenv(kserveImageParamMap["kserve-agent"]); agentImage != "" {
		if err := updateCMJSONKey(cm, openshiftConfigKeyName, func(data map[string]any) {
			data["modelcachePermissionFixImage"] = agentImage
		}); err != nil {
			return err
		}
	}

	return nil
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
	if data == nil {
		data = map[string]any{}
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
	return -1, nil, fmt.Errorf("%w: %s %q", errResourceNotFound, gvk.Kind, name)
}

func replaceResourceAtIndex(resources []unstructured.Unstructured, idx int, obj any) ([]unstructured.Unstructured, error) {
	raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return nil, fmt.Errorf("converting to unstructured: %w", err)
	}
	resources[idx] = unstructured.Unstructured{Object: raw}
	return resources, nil
}
