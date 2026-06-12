package kservemodule

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"

	. "github.com/onsi/gomega"
)

func TestCustomizeKserveConfigMap_Headless(t *testing.T) {
	g := NewWithT(t)

	resources := buildTestResources(t)

	result, err := customizeKserveConfigMap(resources, true, nil)
	g.Expect(err).ShouldNot(HaveOccurred())

	_, cm, err := getIndexedResource[corev1.ConfigMap](result, configMapGVK, kserveConfigMapName)
	g.Expect(err).ShouldNot(HaveOccurred())
	g.Expect(cm.Data[ingressConfigKeyName]).Should(ContainSubstring(`"disableIngressCreation": true`))
	g.Expect(cm.Data[serviceConfigKeyName]).Should(ContainSubstring(`"serviceClusterIPNone": true`))
}

func TestCustomizeKserveConfigMap_Headed(t *testing.T) {
	g := NewWithT(t)

	resources := buildTestResources(t)

	result, err := customizeKserveConfigMap(resources, false, nil)
	g.Expect(err).ShouldNot(HaveOccurred())

	_, cm, err := getIndexedResource[corev1.ConfigMap](result, configMapGVK, kserveConfigMapName)
	g.Expect(err).ShouldNot(HaveOccurred())
	g.Expect(cm.Data[serviceConfigKeyName]).Should(ContainSubstring(`"serviceClusterIPNone": false`))
}

func TestCustomizeKserveConfigMap_AddsHashToDeployment(t *testing.T) {
	g := NewWithT(t)

	resources := buildTestResources(t)

	result, err := customizeKserveConfigMap(resources, true, nil)
	g.Expect(err).ShouldNot(HaveOccurred())

	_, deploy, err := getIndexedResource[appsv1.Deployment](result, deploymentGVK, kserveControllerDeployment)
	g.Expect(err).ShouldNot(HaveOccurred())
	g.Expect(deploy.Spec.Template.Annotations).Should(HaveKey(configHashAnnotationKey))
	g.Expect(deploy.Spec.Template.Annotations[configHashAnnotationKey]).ShouldNot(BeEmpty())
}

func TestCustomizeKserveConfigMap_NoConfigMap(t *testing.T) {
	g := NewWithT(t)

	resources := []unstructured.Unstructured{}
	result, err := customizeKserveConfigMap(resources, true, nil)
	g.Expect(err).ShouldNot(HaveOccurred())
	g.Expect(result).Should(BeEmpty())
}

func TestCustomizeKserveConfigMap_EnableTLS_Nil(t *testing.T) {
	g := NewWithT(t)

	resources := buildTestResources(t)

	result, err := customizeKserveConfigMap(resources, true, nil)
	g.Expect(err).ShouldNot(HaveOccurred())

	_, cm, err := getIndexedResource[corev1.ConfigMap](result, configMapGVK, kserveConfigMapName)
	g.Expect(err).ShouldNot(HaveOccurred())
	g.Expect(cm.Data[ingressConfigKeyName]).ShouldNot(ContainSubstring("enableLLMInferenceServiceTLS"))
}

func TestCustomizeKserveConfigMap_EnableTLS_True(t *testing.T) {
	g := NewWithT(t)

	resources := buildTestResources(t)

	result, err := customizeKserveConfigMap(resources, true, ptr.To(true))
	g.Expect(err).ShouldNot(HaveOccurred())

	_, cm, err := getIndexedResource[corev1.ConfigMap](result, configMapGVK, kserveConfigMapName)
	g.Expect(err).ShouldNot(HaveOccurred())
	g.Expect(cm.Data[ingressConfigKeyName]).Should(ContainSubstring(`"enableLLMInferenceServiceTLS": true`))
}

func TestCustomizeKserveConfigMap_EnableTLS_False(t *testing.T) {
	g := NewWithT(t)

	resources := buildTestResources(t)

	result, err := customizeKserveConfigMap(resources, true, ptr.To(false))
	g.Expect(err).ShouldNot(HaveOccurred())

	_, cm, err := getIndexedResource[corev1.ConfigMap](result, configMapGVK, kserveConfigMapName)
	g.Expect(err).ShouldNot(HaveOccurred())
	g.Expect(cm.Data[ingressConfigKeyName]).Should(ContainSubstring(`"enableLLMInferenceServiceTLS": false`))
}

func TestCustomizeKserveConfigMap_EnableTLS_NilPreservesExisting(t *testing.T) {
	g := NewWithT(t)

	cm := &corev1.ConfigMap{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{Name: kserveConfigMapName, Namespace: "opendatahub"},
		Data: map[string]string{
			ingressConfigKeyName: `{"ingressDomain": "example.com", "enableLLMInferenceServiceTLS": true}`,
			serviceConfigKeyName: `{"serviceType": "ClusterIP"}`,
		},
	}
	cmU, err := runtime.DefaultUnstructuredConverter.ToUnstructured(cm)
	g.Expect(err).ShouldNot(HaveOccurred())

	deploy := &appsv1.Deployment{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
		ObjectMeta: metav1.ObjectMeta{Name: kserveControllerDeployment, Namespace: "opendatahub"},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{}},
			},
		},
	}
	deployU, err := runtime.DefaultUnstructuredConverter.ToUnstructured(deploy)
	g.Expect(err).ShouldNot(HaveOccurred())

	resources := []unstructured.Unstructured{{Object: cmU}, {Object: deployU}}

	result, err := customizeKserveConfigMap(resources, true, nil)
	g.Expect(err).ShouldNot(HaveOccurred())

	_, resultCM, err := getIndexedResource[corev1.ConfigMap](result, configMapGVK, kserveConfigMapName)
	g.Expect(err).ShouldNot(HaveOccurred())
	g.Expect(resultCM.Data[ingressConfigKeyName]).Should(ContainSubstring(`"enableLLMInferenceServiceTLS": true`))
}

func buildTestResources(t *testing.T) []unstructured.Unstructured {
	t.Helper()
	g := NewWithT(t)

	cm := &corev1.ConfigMap{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{Name: kserveConfigMapName, Namespace: "opendatahub"},
		Data: map[string]string{
			ingressConfigKeyName: `{"ingressDomain": "example.com"}`,
			serviceConfigKeyName: `{"serviceType": "ClusterIP"}`,
		},
	}
	cmU, err := runtime.DefaultUnstructuredConverter.ToUnstructured(cm)
	g.Expect(err).ShouldNot(HaveOccurred())

	deploy := &appsv1.Deployment{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
		ObjectMeta: metav1.ObjectMeta{Name: kserveControllerDeployment, Namespace: "opendatahub"},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{},
				},
			},
		},
	}
	deployU, err := runtime.DefaultUnstructuredConverter.ToUnstructured(deploy)
	g.Expect(err).ShouldNot(HaveOccurred())

	return []unstructured.Unstructured{
		{Object: cmU},
		{Object: deployU},
	}
}
