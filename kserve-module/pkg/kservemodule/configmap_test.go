package kservemodule

import (
	"encoding/json"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"

	"k8s.io/apimachinery/pkg/api/resource"

	. "github.com/onsi/gomega"

	platformv1alpha1 "github.com/opendatahub-io/kserve-module/pkg/apis/v1alpha1"
)

func TestCustomizeKserveConfigMap_Headless(t *testing.T) {
	g := NewWithT(t)

	resources := buildTestResources(t)

	result, err := customizeKserveConfigMap(resources, buildTestKserve(platformv1alpha1.KserveRawHeadless, nil, nil))
	g.Expect(err).ShouldNot(HaveOccurred())

	_, cm, err := getIndexedResource[corev1.ConfigMap](result, configMapGVK, kserveConfigMapName)
	g.Expect(err).ShouldNot(HaveOccurred())
	g.Expect(cm.Data[ingressConfigKeyName]).Should(ContainSubstring(`"disableIngressCreation": true`))
	g.Expect(cm.Data[serviceConfigKeyName]).Should(ContainSubstring(`"serviceClusterIPNone": true`))
}

func TestCustomizeKserveConfigMap_Headed(t *testing.T) {
	g := NewWithT(t)

	resources := buildTestResources(t)

	result, err := customizeKserveConfigMap(resources, buildTestKserve(platformv1alpha1.KserveRawHeaded, nil, nil))
	g.Expect(err).ShouldNot(HaveOccurred())

	_, cm, err := getIndexedResource[corev1.ConfigMap](result, configMapGVK, kserveConfigMapName)
	g.Expect(err).ShouldNot(HaveOccurred())
	g.Expect(cm.Data[serviceConfigKeyName]).Should(ContainSubstring(`"serviceClusterIPNone": false`))
}

func TestCustomizeKserveConfigMap_AddsHashToDeployment(t *testing.T) {
	g := NewWithT(t)

	resources := buildTestResources(t)

	result, err := customizeKserveConfigMap(resources, buildTestKserve(platformv1alpha1.KserveRawHeadless, nil, nil))
	g.Expect(err).ShouldNot(HaveOccurred())

	_, deploy, err := getIndexedResource[appsv1.Deployment](result, deploymentGVK, kserveControllerDeployment)
	g.Expect(err).ShouldNot(HaveOccurred())
	g.Expect(deploy.Spec.Template.Annotations).Should(HaveKey(configHashAnnotationKey))
	g.Expect(deploy.Spec.Template.Annotations[configHashAnnotationKey]).ShouldNot(BeEmpty())
}

func TestCustomizeKserveConfigMap_NoConfigMap(t *testing.T) {
	g := NewWithT(t)

	resources := []unstructured.Unstructured{}
	result, err := customizeKserveConfigMap(resources, buildTestKserve(platformv1alpha1.KserveRawHeadless, nil, nil))
	g.Expect(err).ShouldNot(HaveOccurred())
	g.Expect(result).Should(BeEmpty())
}

func TestCustomizeKserveConfigMap_EnableTLS_Nil(t *testing.T) {
	g := NewWithT(t)

	resources := buildTestResources(t)

	result, err := customizeKserveConfigMap(resources, buildTestKserve(platformv1alpha1.KserveRawHeadless, nil, nil))
	g.Expect(err).ShouldNot(HaveOccurred())

	_, cm, err := getIndexedResource[corev1.ConfigMap](result, configMapGVK, kserveConfigMapName)
	g.Expect(err).ShouldNot(HaveOccurred())
	g.Expect(cm.Data[ingressConfigKeyName]).ShouldNot(ContainSubstring("enableLLMInferenceServiceTLS"))
}

func TestCustomizeKserveConfigMap_EnableTLS_True(t *testing.T) {
	g := NewWithT(t)

	resources := buildTestResources(t)

	result, err := customizeKserveConfigMap(resources, buildTestKserve(platformv1alpha1.KserveRawHeadless, ptr.To(true), nil))
	g.Expect(err).ShouldNot(HaveOccurred())

	_, cm, err := getIndexedResource[corev1.ConfigMap](result, configMapGVK, kserveConfigMapName)
	g.Expect(err).ShouldNot(HaveOccurred())
	g.Expect(cm.Data[ingressConfigKeyName]).Should(ContainSubstring(`"enableLLMInferenceServiceTLS": true`))
}

func TestCustomizeKserveConfigMap_EnableTLS_False(t *testing.T) {
	g := NewWithT(t)

	resources := buildTestResources(t)

	result, err := customizeKserveConfigMap(resources, buildTestKserve(platformv1alpha1.KserveRawHeadless, ptr.To(false), nil))
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

	result, err := customizeKserveConfigMap(resources, buildTestKserve(platformv1alpha1.KserveRawHeadless, nil, nil))
	g.Expect(err).ShouldNot(HaveOccurred())

	_, resultCM, err := getIndexedResource[corev1.ConfigMap](result, configMapGVK, kserveConfigMapName)
	g.Expect(err).ShouldNot(HaveOccurred())
	g.Expect(resultCM.Data[ingressConfigKeyName]).Should(ContainSubstring(`"enableLLMInferenceServiceTLS": true`))
}

func TestCustomizeKserveConfigMap_AuditLoggingProfiles(t *testing.T) {
	tests := []struct {
		name    string
		profile platformv1alpha1.AuditProfile
		want    string
	}{
		{name: "blank is equivalent to Removed", profile: "", want: "none"},
		{name: "Removed maps to none", profile: platformv1alpha1.AuditProfileRemoved, want: "none"},
		{name: "Metadata maps to metadata", profile: platformv1alpha1.AuditProfileMetadata, want: "metadata"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resources := buildTestResourcesWithOpenshiftConfig(t)
			result, err := customizeKserveConfigMap(resources, buildTestKserveWithAuditProfile(
				platformv1alpha1.KserveRawHeadless, nil, nil, tt.profile,
			))
			if err != nil {
				t.Fatalf("customizeKserveConfigMap() error = %v", err)
			}
			_, cm, err := getIndexedResource[corev1.ConfigMap](result, configMapGVK, kserveConfigMapName)
			if err != nil {
				t.Fatalf("get ConfigMap: %v", err)
			}
			var config map[string]any
			if err := json.Unmarshal([]byte(cm.Data[openshiftConfigKeyName]), &config); err != nil {
				t.Fatalf("decode openshiftConfig: %v", err)
			}
			if got := config["auditLoggingProfile"]; got != tt.want {
				t.Fatalf("auditLoggingProfile = %#v, want %q", got, tt.want)
			}
			if _, found := config["enableAuditLogging"]; found {
				t.Fatal("stale enableAuditLogging key was not removed")
			}
		})
	}
}

func TestUpdateInferenceCM_AuditLoggingProfilesRejectInvalidValues(t *testing.T) {
	for _, profile := range []platformv1alpha1.AuditProfile{
		"request",
		"none",
		"metadata",
	} {
		t.Run(string(profile), func(t *testing.T) {
			cm := &corev1.ConfigMap{Data: map[string]string{
				openshiftConfigKeyName: `{}`,
			}}
			err := updateInferenceCM(cm, buildTestKserveWithAuditProfile(platformv1alpha1.KserveRawHeadless, nil, nil, profile))
			if err == nil || !strings.Contains(err.Error(), `audit logging profile must be one of "Removed" or "Metadata"`) {
				t.Fatalf("updateInferenceCM() error = %v, want invalid audit logging profile error", err)
			}
		})
	}
}

func buildTestResourcesWithOpenshiftConfig(t *testing.T) []unstructured.Unstructured {
	t.Helper()
	g := NewWithT(t)

	cm := &corev1.ConfigMap{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{Name: kserveConfigMapName, Namespace: "opendatahub"},
		Data: map[string]string{
			ingressConfigKeyName:   `{"ingressDomain": "example.com"}`,
			serviceConfigKeyName:   `{"serviceType": "ClusterIP"}`,
			openshiftConfigKeyName: `{"enableAuditLogging":true}`,
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

	return []unstructured.Unstructured{{Object: cmU}, {Object: deployU}}
}

func TestCustomizeKserveConfigMap_OAuthProxy_FullOverride(t *testing.T) {
	g := NewWithT(t)

	resources := buildTestResourcesWithOAuthProxy(t)
	oauthProxy := &platformv1alpha1.OAuthProxyConfig{
		Resources: &platformv1alpha1.OAuthProxyResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceMemory: resource.MustParse("256Mi"),
				corev1.ResourceCPU:    resource.MustParse("200m"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceMemory: resource.MustParse("512Mi"),
				corev1.ResourceCPU:    resource.MustParse("500m"),
			},
		},
	}

	result, err := customizeKserveConfigMap(resources, buildTestKserve(platformv1alpha1.KserveRawHeadless, nil, oauthProxy))
	g.Expect(err).ShouldNot(HaveOccurred())

	_, cm, err := getIndexedResource[corev1.ConfigMap](result, configMapGVK, kserveConfigMapName)
	g.Expect(err).ShouldNot(HaveOccurred())
	g.Expect(cm.Data[oauthProxyConfigKeyName]).Should(ContainSubstring(`"memoryRequest": "256Mi"`))
	g.Expect(cm.Data[oauthProxyConfigKeyName]).Should(ContainSubstring(`"memoryLimit": "512Mi"`))
	g.Expect(cm.Data[oauthProxyConfigKeyName]).Should(ContainSubstring(`"cpuRequest": "200m"`))
	g.Expect(cm.Data[oauthProxyConfigKeyName]).Should(ContainSubstring(`"cpuLimit": "500m"`))
}

func TestCustomizeKserveConfigMap_OAuthProxy_PartialOverride(t *testing.T) {
	g := NewWithT(t)

	resources := buildTestResourcesWithOAuthProxy(t)
	oauthProxy := &platformv1alpha1.OAuthProxyConfig{
		Resources: &platformv1alpha1.OAuthProxyResourceRequirements{
			Limits: corev1.ResourceList{
				corev1.ResourceMemory: resource.MustParse("512Mi"),
			},
		},
	}

	result, err := customizeKserveConfigMap(resources, buildTestKserve(platformv1alpha1.KserveRawHeadless, nil, oauthProxy))
	g.Expect(err).ShouldNot(HaveOccurred())

	_, cm, err := getIndexedResource[corev1.ConfigMap](result, configMapGVK, kserveConfigMapName)
	g.Expect(err).ShouldNot(HaveOccurred())
	g.Expect(cm.Data[oauthProxyConfigKeyName]).Should(ContainSubstring(`"memoryLimit": "512Mi"`))
	g.Expect(cm.Data[oauthProxyConfigKeyName]).Should(ContainSubstring(`"memoryRequest": "64Mi"`))
	g.Expect(cm.Data[oauthProxyConfigKeyName]).Should(ContainSubstring(`"cpuRequest": "100m"`))
}

func TestCustomizeKserveConfigMap_OAuthProxy_NilConfig(t *testing.T) {
	g := NewWithT(t)

	resources := buildTestResourcesWithOAuthProxy(t)

	result, err := customizeKserveConfigMap(resources, buildTestKserve(platformv1alpha1.KserveRawHeadless, nil, nil))
	g.Expect(err).ShouldNot(HaveOccurred())

	_, cm, err := getIndexedResource[corev1.ConfigMap](result, configMapGVK, kserveConfigMapName)
	g.Expect(err).ShouldNot(HaveOccurred())
	g.Expect(cm.Data[oauthProxyConfigKeyName]).Should(ContainSubstring(`"memoryRequest": "64Mi"`))
	g.Expect(cm.Data[oauthProxyConfigKeyName]).Should(ContainSubstring(`"memoryLimit": "128Mi"`))
}

func TestCustomizeKserveConfigMap_OAuthProxy_MissingKey(t *testing.T) {
	g := NewWithT(t)

	resources := buildTestResources(t)
	oauthProxy := &platformv1alpha1.OAuthProxyConfig{
		Resources: &platformv1alpha1.OAuthProxyResourceRequirements{
			Limits: corev1.ResourceList{
				corev1.ResourceMemory: resource.MustParse("512Mi"),
			},
		},
	}

	result, err := customizeKserveConfigMap(resources, buildTestKserve(platformv1alpha1.KserveRawHeadless, nil, oauthProxy))
	g.Expect(err).ShouldNot(HaveOccurred())

	_, cm, err := getIndexedResource[corev1.ConfigMap](result, configMapGVK, kserveConfigMapName)
	g.Expect(err).ShouldNot(HaveOccurred())
	g.Expect(cm.Data).ShouldNot(HaveKey(oauthProxyConfigKeyName))
}

func TestCustomizeKserveConfigMap_OpenshiftConfig_SetsAgentImage(t *testing.T) {
	g := NewWithT(t)

	t.Setenv("RELATED_IMAGE_ODH_KSERVE_AGENT_IMAGE", "quay.io/test/agent:v2")

	cm := &corev1.ConfigMap{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{Name: kserveConfigMapName, Namespace: "opendatahub"},
		Data: map[string]string{
			ingressConfigKeyName:   `{"ingressDomain": "example.com"}`,
			serviceConfigKeyName:   `{"serviceType": "ClusterIP"}`,
			openshiftConfigKeyName: `{"modelcachePermissionFixImage": "REPLACE_IMAGE"}`,
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
	result, err := customizeKserveConfigMap(resources, buildTestKserve(platformv1alpha1.KserveRawHeadless, nil, nil))
	g.Expect(err).ShouldNot(HaveOccurred())

	_, resultCM, err := getIndexedResource[corev1.ConfigMap](result, configMapGVK, kserveConfigMapName)
	g.Expect(err).ShouldNot(HaveOccurred())
	g.Expect(resultCM.Data[openshiftConfigKeyName]).Should(ContainSubstring(`"modelcachePermissionFixImage": "quay.io/test/agent:v2"`))
}

func TestCustomizeKserveConfigMap_OpenshiftConfig_NoOpWithoutEnvVar(t *testing.T) {
	g := NewWithT(t)

	cm := &corev1.ConfigMap{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{Name: kserveConfigMapName, Namespace: "opendatahub"},
		Data: map[string]string{
			ingressConfigKeyName:   `{"ingressDomain": "example.com"}`,
			serviceConfigKeyName:   `{"serviceType": "ClusterIP"}`,
			openshiftConfigKeyName: `{"modelcachePermissionFixImage": "REPLACE_IMAGE"}`,
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
	result, err := customizeKserveConfigMap(resources, buildTestKserve(platformv1alpha1.KserveRawHeadless, nil, nil))
	g.Expect(err).ShouldNot(HaveOccurred())

	_, resultCM, err := getIndexedResource[corev1.ConfigMap](result, configMapGVK, kserveConfigMapName)
	g.Expect(err).ShouldNot(HaveOccurred())
	g.Expect(resultCM.Data[openshiftConfigKeyName]).Should(ContainSubstring(`"modelcachePermissionFixImage": "REPLACE_IMAGE"`))
}

func buildTestResourcesWithOAuthProxy(t *testing.T) []unstructured.Unstructured {
	t.Helper()
	g := NewWithT(t)

	cm := &corev1.ConfigMap{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{Name: kserveConfigMapName, Namespace: "opendatahub"},
		Data: map[string]string{
			ingressConfigKeyName:    `{"ingressDomain": "example.com"}`,
			serviceConfigKeyName:    `{"serviceType": "ClusterIP"}`,
			oauthProxyConfigKeyName: `{"image": "registry.example.com/oauth-proxy:latest", "memoryRequest": "64Mi", "memoryLimit": "128Mi", "cpuRequest": "100m", "cpuLimit": "200m"}`,
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

	return []unstructured.Unstructured{{Object: cmU}, {Object: deployU}}
}

func buildTestKserve(rawSvc platformv1alpha1.RawServiceConfig, enableTLS *bool, oauthProxy *platformv1alpha1.OAuthProxyConfig) *platformv1alpha1.Kserve {
	return buildTestKserveWithAuditProfile(rawSvc, enableTLS, oauthProxy, "")
}

func buildTestKserveWithAuditProfile(rawSvc platformv1alpha1.RawServiceConfig, enableTLS *bool, oauthProxy *platformv1alpha1.OAuthProxyConfig, auditLoggingProfile platformv1alpha1.AuditProfile) *platformv1alpha1.Kserve {
	return &platformv1alpha1.Kserve{
		Spec: platformv1alpha1.KserveSpec{
			RawDeploymentServiceConfig:   rawSvc,
			EnableLLMInferenceServiceTLS: enableTLS,
			OAuthProxy:                   oauthProxy,
			AuditLoggingProfile:          auditLoggingProfile,
		},
	}
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
