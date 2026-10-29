//go:build distro

/*
Copyright 2026 The KServe Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package llmisvc_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	securityv1 "github.com/openshift/api/security/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/utils/ptr"
	"knative.dev/pkg/apis"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/yaml"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
	"github.com/kserve/kserve/pkg/constants"
	"github.com/kserve/kserve/pkg/controller/v1alpha2/llmisvc"
	"github.com/kserve/kserve/pkg/credentials"
	pkgtest "github.com/kserve/kserve/pkg/testing"
	kservetypes "github.com/kserve/kserve/pkg/types"
	"github.com/kserve/kserve/pkg/utils"
)

const bareMetalPreset = "kserve-config-llm-confidential-bare-metal"

type confidentialTestingT interface {
	require.TestingT
	Helper()
}

func readConfidentialPreset(t confidentialTestingT) *v1alpha2.LLMInferenceServiceConfig {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(pkgtest.ProjectRoot(), "config/overlays/odh/llmisvcconfig/config-llm-confidential-bare-metal.yaml"))
	require.NoError(t, err)
	preset := &v1alpha2.LLMInferenceServiceConfig{}
	require.NoError(t, yaml.Unmarshal(data, preset))
	preset.Namespace = constants.KServeNamespace
	return preset
}

func confidentialBundle(t confidentialTestingT) *corev1.ConfigMap {
	t.Helper()
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: constants.KServeNamespace, Name: "kserve-confidential-bare-metal-resources"}, Data: map[string]string{}}
	for _, file := range []string{"link.sh", "format-disk.sh", "service-account.yaml", "role-binding.yaml"} {
		data, err := os.ReadFile(filepath.Join(pkgtest.ProjectRoot(), "config/overlays/odh/confidential", file)) //nolint:gosec // G304: test-only path from t.TempDir()
		require.NoError(t, err)
		cm.Data[file] = string(data)
	}
	return cm
}

func confidentialTestReconciler(t *testing.T) (*llmisvc.LLMISVCReconciler, *llmisvc.Config) {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, appsv1.AddToScheme(scheme))
	require.NoError(t, rbacv1.AddToScheme(scheme))
	require.NoError(t, v1alpha2.AddToScheme(scheme))
	data, err := os.ReadFile(filepath.Join(pkgtest.ProjectRoot(), "config/llmisvcconfig/config-llm-template.yaml"))
	require.NoError(t, err)
	base := &v1alpha2.LLMInferenceServiceConfig{}
	require.NoError(t, yaml.Unmarshal(data, base))
	base.Namespace = constants.KServeNamespace
	defaults := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: constants.KServeNamespace, Name: constants.InferenceServiceConfigMapName}, Data: map[string]string{
		"confidentialBareMetal": `{"luksResourceId":"kbs:///default/disk/key","initData":"installation-initdata"}`,
	}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&v1alpha2.LLMInferenceService{}).
		WithObjects(base, readConfidentialPreset(t), confidentialBundle(t), defaults).Build()
	return &llmisvc.LLMISVCReconciler{Client: c, Clientset: kubefake.NewClientset()}, &llmisvc.Config{
		CredentialConfig: &credentials.CredentialConfig{StorageSecretNameAnnotation: "serving.kserve.io/storageSecretName"},
		StorageConfig:    &kservetypes.StorageInitializerConfig{},
	}
}

func confidentialService(t confidentialTestingT, name, ns string) *v1alpha2.LLMInferenceService {
	t.Helper()
	uri, err := apis.ParseURL("https://example.test/model-encrypted")
	require.NoError(t, err)
	return &v1alpha2.LLMInferenceService{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, UID: types.UID(ns + "-" + name)},
		Spec: v1alpha2.LLMInferenceServiceSpec{
			BaseRefs: []corev1.LocalObjectReference{{Name: bareMetalPreset}},
			WorkloadSpec: v1alpha2.WorkloadSpec{Template: &corev1.PodSpec{Volumes: []corev1.Volume{{Name: "luks-block", VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: name + "-model-disk"},
			}}}}},
			Model: v1alpha2.LLMModelSpec{URI: *uri, Name: ptr.To(name), Confidential: &v1alpha2.ConfidentialSpec{Enabled: true, ResourceId: ptr.To("kbs:///default/model/key")}},
		},
	}
}

func TestConfidentialNamespaceSharingAndStorage(t *testing.T) {
	r, cfg := confidentialTestReconciler(t)
	for _, pair := range [][2]string{{"first", "project-a"}, {"second", "project-a"}, {"third", "project-b"}} {
		svc := confidentialService(t, pair[0], pair[1])
		claim := confidentialClaim(svc)
		claim.Labels = map[string]string{"customer": "owned"}
		require.NoError(t, r.Create(t.Context(), claim))
		before := claim.DeepCopy()
		require.NoError(t, r.Create(t.Context(), svc))
		require.NoError(t, r.CombineConfidentialForTest(t.Context(), svc, cfg))
		require.NoError(t, r.PreparePlatformWorkloadForTest(t.Context(), svc, cfg))
		claims := &corev1.PersistentVolumeClaimList{}
		require.NoError(t, r.List(t.Context(), claims, client.InNamespace(svc.Namespace)))
		require.NoError(t, r.Get(t.Context(), client.ObjectKeyFromObject(claim), claim))
		assert.Equal(t, before, claim, "customer storage must not be modified")
		assert.Equal(t, []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOncePod}, claim.Spec.AccessModes)
		assert.Equal(t, corev1.PersistentVolumeBlock, *claim.Spec.VolumeMode)
		assert.Empty(t, claim.OwnerReferences)
		rv := claim.ResourceVersion
		require.NoError(t, r.PreparePlatformWorkloadForTest(t.Context(), svc, cfg))
		require.NoError(t, r.Get(t.Context(), client.ObjectKeyFromObject(claim), claim))
		assert.Equal(t, rv, claim.ResourceVersion, "reconciliation must not rewrite a compatible claim")
		assert.Equal(t, "installation-initdata", svc.Spec.Annotations["io.katacontainers.config.hypervisor.cc_init_data"])

		pod := svc.Spec.Template
		assert.True(t, ptr.Deref(pod.ShareProcessNamespace, false))
		assert.Equal(t, "coco-cc-storage", pod.ServiceAccountName)
		names := make([]string, 0, len(pod.InitContainers))
		for _, c := range pod.InitContainers {
			names = append(names, c.Name)
		}
		assert.Equal(t, []string{"format-disk", "check-ready", "storage-initializer"}, names)
		assert.Equal(t, corev1.ContainerRestartPolicyAlways, *pod.InitContainers[0].RestartPolicy)
		main := utils.GetContainerWithName(pod, "main")
		assert.True(t, ptr.Deref(main.SecurityContext.Privileged, false))
		assert.False(t, ptr.Deref(main.SecurityContext.RunAsNonRoot, true))
		assert.True(t, ptr.Deref(main.SecurityContext.AllowPrivilegeEscalation, false))
		initializer := utils.GetInitContainerWithName(pod, "storage-initializer")
		assert.Equal(t, []string{svc.Spec.Model.URI.String(), "/mnt/models"}, initializer.Args)
		assert.Contains(t, initializer.Command[2], ". /opt/cc-storage/link.sh")
		for _, vol := range pod.Volumes {
			if slices.Contains([]string{"home", "tmp-dir", "model-cache", "dshm"}, vol.Name) {
				require.NotNil(t, vol.EmptyDir)
				assert.Equal(t, corev1.StorageMediumMemory, vol.EmptyDir.Medium)
			}
		}
		// The actual Deployment retains the wrapper and stops the old pod before replacement.
		deployment, err := r.ConfidentialDeploymentForTest(t.Context(), svc, cfg)
		require.NoError(t, err)
		assert.Equal(t, appsv1.RecreateDeploymentStrategyType, deployment.Spec.Strategy.Type)
		assert.Nil(t, deployment.Spec.Strategy.RollingUpdate)
		assert.Contains(t, deployment.Spec.Template.Spec.Volumes, corev1.Volume{
			Name: "luks-block", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: claim.Name}},
		})
		assert.Contains(t, utils.GetInitContainerWithName(pod, "format-disk").Env,
			corev1.EnvVar{Name: "LUKS_RESOURCE_ID", Value: "kbs:///default/disk/key"})
	}
	for _, ns := range []string{"project-a", "project-b"} {
		accounts := &corev1.ServiceAccountList{}
		require.NoError(t, r.List(t.Context(), accounts, client.InNamespace(ns)))
		require.Len(t, accounts.Items, 1)
		assert.Empty(t, accounts.Items[0].OwnerReferences)
		binding := &rbacv1.RoleBinding{}
		require.NoError(t, r.Get(t.Context(), client.ObjectKey{Namespace: ns, Name: "coco-cc-storage"}, binding))
		assert.Equal(t, ns, binding.Subjects[0].Namespace)
		assert.Empty(t, binding.OwnerReferences)
	}
}

func TestConfidentialRepairAndCredentials(t *testing.T) {
	r, cfg := confidentialTestReconciler(t)
	svc := confidentialService(t, "model", "project")
	require.NoError(t, r.Create(t.Context(), confidentialClaim(svc)))
	svc.Annotations = map[string]string{"serving.kserve.io/storageSecretName": "connection"}
	svc.Spec.Annotations = map[string]string{"io.katacontainers.config.hypervisor.cc_init_data": "service-initdata"}
	svc.Spec.Template = &corev1.PodSpec{Volumes: svc.Spec.Template.Volumes, RuntimeClassName: ptr.To("kata-cc-nvidia-gpu"), InitContainers: []corev1.Container{{Name: "additional-init", Image: "example.test/init:1"}}}
	require.NoError(t, r.Create(t.Context(), svc))
	require.NoError(t, r.CombineConfidentialForTest(t.Context(), svc, cfg))
	require.NoError(t, r.PreparePlatformWorkloadForTest(t.Context(), svc, cfg))
	assert.Equal(t, "kata-cc-nvidia-gpu", *svc.Spec.Template.RuntimeClassName)
	assert.Equal(t, "service-initdata", svc.Spec.Annotations["io.katacontainers.config.hypervisor.cc_init_data"])
	cm := &corev1.ConfigMap{}
	key := client.ObjectKey{Namespace: svc.Namespace, Name: "cc-storage-link"}
	require.NoError(t, r.Get(t.Context(), key, cm))
	cm.Data["link.sh"] = "broken"
	cm.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(svc, v1alpha2.LLMInferenceServiceGVK)}
	require.NoError(t, r.Update(t.Context(), cm))
	require.NoError(t, r.PreparePlatformWorkloadForTest(t.Context(), svc, cfg))
	require.NoError(t, r.Get(t.Context(), key, cm))
	assert.NotEqual(t, "broken", cm.Data["link.sh"])
	assert.Empty(t, cm.OwnerReferences, "shared resources must never remain owned by one model service")
	_, err := r.Clientset.CoreV1().Secrets(svc.Namespace).Create(t.Context(), &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "connection"}, Data: map[string][]byte{"HF_TOKEN": []byte("example-token")}}, metav1.CreateOptions{})
	require.NoError(t, err)
	d, err := r.ConfidentialDeploymentForTest(t.Context(), svc, cfg)
	require.NoError(t, err)
	init := utils.GetInitContainerWithName(&d.Spec.Template.Spec, "storage-initializer")
	assert.Contains(t, init.Command[2], ". /opt/cc-storage/link.sh")
	assert.Contains(t, init.Env, corev1.EnvVar{Name: "HF_TOKEN", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "connection"}, Key: "HF_TOKEN"}}})
}

func TestConfidentialSupportedStorageLayouts(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*v1alpha2.LLMInferenceService)
		want   string
	}{
		{"replicas", func(s *v1alpha2.LLMInferenceService) { s.Spec.Replicas = ptr.To(int32(2)) }, "one model pod"},
		{"autoscaling", func(s *v1alpha2.LLMInferenceService) { s.Spec.Scaling = &v1alpha2.ScalingSpec{MaxReplicas: 2} }, "one model pod"},
		{"workers", func(s *v1alpha2.LLMInferenceService) { s.Spec.Worker = &corev1.PodSpec{} }, "one model pod"},
		{"prefill", func(s *v1alpha2.LLMInferenceService) { s.Spec.Prefill = &v1alpha2.WorkloadSpec{} }, "one model pod"},
		{"adapters", func(s *v1alpha2.LLMInferenceService) { s.Spec.Model.LoRA = &v1alpha2.LoRASpec{} }, "adapter loading"},
		{"cache", func(s *v1alpha2.LLMInferenceService) { s.Spec.KVCacheOffloading = &v1alpha2.KVCacheOffloadingSpec{} }, "cache offloading"},
		{"missing key", func(s *v1alpha2.LLMInferenceService) { s.Spec.Model.Confidential.ResourceId = nil }, "model.confidential.resourceId"},
		{"missing runtime class", func(s *v1alpha2.LLMInferenceService) { s.Spec.Template.RuntimeClassName = nil }, "template.runtimeClassName: kata-cc-nvidia-gpu"},
		{"removed formatter", func(s *v1alpha2.LLMInferenceService) {
			s.Spec.Template.InitContainers = slices.DeleteFunc(s.Spec.Template.InitContainers, func(c corev1.Container) bool { return c.Name == "format-disk" })
		}, "format-disk"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, cfg := confidentialTestReconciler(t)
			svc := confidentialService(t, "model", "project")
			require.NoError(t, r.CombineConfidentialForTest(t.Context(), svc, cfg))
			tc.change(svc)
			require.ErrorContains(t, r.PreparePlatformWorkloadForTest(t.Context(), svc, cfg), tc.want)
		})
	}
}

func TestConfidentialRejectsUnapprovedRuntimeBeforeGrantingPermissions(t *testing.T) {
	for _, runtimeClass := range []string{"", "runc", "kata", "another-confidential-runtime", "kata-cc-nvidia-gpu-unapproved"} {
		t.Run(runtimeClass, func(t *testing.T) {
			r, cfg := confidentialTestReconciler(t)
			svc := confidentialService(t, "model", "project")
			svc.Spec.Template.RuntimeClassName = ptr.To(runtimeClass)
			require.NoError(t, r.Create(t.Context(), confidentialClaim(svc)))
			require.NoError(t, r.CombineConfidentialForTest(t.Context(), svc, cfg))
			require.ErrorContains(t, r.PreparePlatformWorkloadForTest(t.Context(), svc, cfg), "template.runtimeClassName: kata-cc-nvidia-gpu")
			err := r.Get(t.Context(), client.ObjectKey{Namespace: svc.Namespace, Name: "coco-cc-storage"}, &rbacv1.RoleBinding{})
			require.True(t, apierrors.IsNotFound(err), "rejected services must not receive SCC access: %v", err)
		})
	}
}

func TestConfidentialSecurityConstraints(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(pkgtest.ProjectRoot(), "config/overlays/odh/confidential/scc.yaml"))
	require.NoError(t, err)
	scc := &securityv1.SecurityContextConstraints{}
	require.NoError(t, yaml.Unmarshal(data, scc))
	assert.Empty(t, scc.AllowedCapabilities)
	assert.Equal(t, []string{"runtime/default"}, scc.SeccompProfiles)
}

func TestConfidentialOrdinaryServiceDoesNotProvision(t *testing.T) {
	r, cfg := confidentialTestReconciler(t)
	svc := confidentialService(t, "ordinary", "project")
	svc.Spec.BaseRefs = nil
	cm := &corev1.ConfigMap{}
	require.NoError(t, r.Get(t.Context(), client.ObjectKey{Namespace: constants.KServeNamespace, Name: constants.InferenceServiceConfigMapName}, cm))
	cm.Data["confidentialBareMetal"] = "malformed installation defaults"
	require.NoError(t, r.Update(t.Context(), cm))
	require.NoError(t, r.CombineConfidentialForTest(t.Context(), svc, cfg))
	require.NoError(t, r.PreparePlatformWorkloadForTest(t.Context(), svc, cfg))
	claims := &corev1.PersistentVolumeClaimList{}
	require.NoError(t, r.List(t.Context(), claims))
	assert.Empty(t, claims.Items)
}

func TestConfidentialPresetUsesInstalledVersion(t *testing.T) {
	r, cfg := confidentialTestReconciler(t)
	svc := confidentialService(t, "model", "project")
	// A tenant's same-name config cannot grant permissions or replace the product preset.
	local := readConfidentialPreset(t)
	local.Namespace = svc.Namespace
	local.Spec.Template.RuntimeClassName = ptr.To("tenant-runtime")
	require.NoError(t, r.Create(t.Context(), local))
	require.NoError(t, r.CombineConfidentialForTest(t.Context(), svc, cfg))
	assert.Equal(t, "kata-cc-nvidia-gpu", *svc.Spec.Template.RuntimeClassName)
	pinned := readConfidentialPreset(t)
	pinned.Name = "v3-4-0-" + bareMetalPreset
	require.NoError(t, r.Create(t.Context(), pinned))
	svc = confidentialService(t, "pinned", "project")
	svc.Status.Annotations = map[string]string{"serving.kserve.io/config-llm-confidential-bare-metal": pinned.Name}
	require.NoError(t, r.CombineConfidentialForTest(t.Context(), svc, cfg))
	assert.Equal(t, "kata-cc-nvidia-gpu", *svc.Spec.Template.RuntimeClassName)
	assert.True(t, svc.IsUsingLLMInferenceServiceConfigInNamespace(pinned.Name, constants.KServeNamespace))
	require.NoError(t, r.Create(t.Context(), confidentialClaim(svc)))
	require.NoError(t, r.PreparePlatformWorkloadForTest(t.Context(), svc, cfg))
}

// Customer claims may remain pending until a model Pod is scheduled.
func confidentialClaim(svc *v1alpha2.LLMInferenceService) *corev1.PersistentVolumeClaim {
	var name string
	for _, volume := range svc.Spec.Template.Volumes {
		if volume.Name == "luks-block" && volume.PersistentVolumeClaim != nil {
			name = volume.PersistentVolumeClaim.ClaimName
		}
	}
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: svc.Namespace},
		Spec: corev1.PersistentVolumeClaimSpec{
			VolumeMode:  ptr.To(corev1.PersistentVolumeBlock),
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOncePod},
			Resources:   corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("100Gi")}},
		},
	}
}

func TestConfidentialPresetRequiresCustomerVolume(t *testing.T) {
	preset := readConfidentialPreset(t)
	for _, volume := range preset.Spec.Template.Volumes {
		assert.NotEqual(t, "luks-block", volume.Name, "the installed preset must not select a claim")
	}
	r, cfg := confidentialTestReconciler(t)
	svc := confidentialService(t, "model", "project")
	svc.Spec.Template = nil
	require.NoError(t, r.CombineConfidentialForTest(t.Context(), svc, cfg))
	require.ErrorContains(t, r.PreparePlatformWorkloadForTest(t.Context(), svc, cfg), "persistentVolumeClaim.claimName")
	claims := &corev1.PersistentVolumeClaimList{}
	require.NoError(t, r.List(t.Context(), claims))
	assert.Empty(t, claims.Items)
}

func TestConfidentialCustomerPVCValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*v1alpha2.LLMInferenceService, *corev1.PersistentVolumeClaim)
		want   string
	}{
		{"missing volume", func(s *v1alpha2.LLMInferenceService, _ *corev1.PersistentVolumeClaim) {
			s.Spec.Template.Volumes = nil
		}, "persistentVolumeClaim.claimName"},
		{"empty claim name", func(s *v1alpha2.LLMInferenceService, _ *corev1.PersistentVolumeClaim) {
			s.Spec.Template.Volumes[0].PersistentVolumeClaim.ClaimName = ""
		}, "persistentVolumeClaim.claimName"},
		{"missing claim", func(s *v1alpha2.LLMInferenceService, _ *corev1.PersistentVolumeClaim) {
			s.Spec.Template.Volumes[0].PersistentVolumeClaim.ClaimName = "absent"
		}, "create the storage before deploying"},
		{"read-only", func(s *v1alpha2.LLMInferenceService, _ *corev1.PersistentVolumeClaim) {
			s.Spec.Template.Volumes[0].PersistentVolumeClaim.ReadOnly = true
		}, "must allow writes"},
		{"another namespace", func(_ *v1alpha2.LLMInferenceService, c *corev1.PersistentVolumeClaim) {
			c.Namespace = "other-project"
		}, "does not exist"},
		{"filesystem mode", func(_ *v1alpha2.LLMInferenceService, c *corev1.PersistentVolumeClaim) {
			c.Spec.VolumeMode = ptr.To(corev1.PersistentVolumeFilesystem)
		}, "volumeMode: Block"},
		{"default filesystem mode", func(_ *v1alpha2.LLMInferenceService, c *corev1.PersistentVolumeClaim) {
			c.Spec.VolumeMode = nil
		}, "volumeMode: Block"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, cfg := confidentialTestReconciler(t)
			svc := confidentialService(t, "model", "project")
			claim := confidentialClaim(svc)
			tc.change(svc, claim)
			require.NoError(t, r.Create(t.Context(), claim))
			require.NoError(t, r.CombineConfidentialForTest(t.Context(), svc, cfg))
			require.ErrorContains(t, r.PreparePlatformWorkloadForTest(t.Context(), svc, cfg), tc.want)
		})
	}
}

func TestConfidentialCustomerStorageSettings(t *testing.T) {
	for _, tc := range []struct{ name, raw, want string }{
		{"key only", `{"luksResourceId":"kbs:///default/disk/key"}`, ""},
		{"legacy allocation settings ignored", `{"storageClassName":"absent","storageSize":"-1Gi","localVolumes":[{}],"luksResourceId":"kbs:///default/disk/key"}`, ""},
		{"no defaults", "", "configure spec.confidentialContainers.luksResourceId"},
		{"missing key", `{}`, "configure spec.confidentialContainers.luksResourceId"},
		{"malformed JSON", "invalid-json", "invalid character"},
		{"invalid disk key", `{"luksResourceId":"not-a-kbs-reference"}`, "luksResourceId must identify"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, cfg := confidentialTestReconciler(t)
			svc := confidentialService(t, "model", "project")
			require.NoError(t, r.Create(t.Context(), confidentialClaim(svc)))
			cm := &corev1.ConfigMap{}
			require.NoError(t, r.Get(t.Context(), client.ObjectKey{Namespace: constants.KServeNamespace, Name: constants.InferenceServiceConfigMapName}, cm))
			cm.Data["confidentialBareMetal"] = tc.raw
			require.NoError(t, r.Update(t.Context(), cm))
			require.NoError(t, r.CombineConfidentialForTest(t.Context(), svc, cfg))
			err := r.PreparePlatformWorkloadForTest(t.Context(), svc, cfg)
			if tc.want == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tc.want)
			}
		})
	}
}

func TestConfidentialServiceDiskKeyOverride(t *testing.T) {
	modelKey := corev1.EnvVar{Name: "LUKS_RESOURCE_ID", Value: "kbs:///customer/model-disk/key"}
	for _, tc := range []struct {
		name     string
		fallback string
		override corev1.EnvVar
		wantErr  string
	}{
		{name: "service key takes precedence", fallback: "kbs:///default/disk/key", override: modelKey},
		{name: "service key without a default", override: modelKey},
		{name: "unused invalid default", fallback: "invalid-default", override: modelKey},
		{
			name: "preserves valueFrom",
			override: corev1.EnvVar{Name: "LUKS_RESOURCE_ID", ValueFrom: &corev1.EnvVarSource{
				ConfigMapKeyRef: &corev1.ConfigMapKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "model-disk"}, Key: "resourceId"},
			}},
		},
		{
			name: "invalid service key does not fall back", fallback: "kbs:///default/disk/key",
			override: corev1.EnvVar{Name: "LUKS_RESOURCE_ID", Value: "invalid-service-key"}, wantErr: "luksResourceId must identify",
		},
		{
			name: "empty service key does not fall back", fallback: "kbs:///default/disk/key",
			override: corev1.EnvVar{Name: "LUKS_RESOURCE_ID"}, wantErr: "set LUKS_RESOURCE_ID",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, cfg := confidentialTestReconciler(t)
			svc := confidentialService(t, "model", "project")
			svc.Spec.Template.InitContainers = []corev1.Container{{Name: "format-disk", Env: []corev1.EnvVar{tc.override}}}
			require.NoError(t, r.Create(t.Context(), confidentialClaim(svc)))
			cm := &corev1.ConfigMap{}
			require.NoError(t, r.Get(t.Context(), client.ObjectKey{Namespace: constants.KServeNamespace, Name: constants.InferenceServiceConfigMapName}, cm))
			raw, err := json.Marshal(map[string]string{"luksResourceId": tc.fallback, "initData": "installation-initdata"})
			require.NoError(t, err)
			cm.Data["confidentialBareMetal"] = string(raw)
			require.NoError(t, r.Update(t.Context(), cm))
			require.NoError(t, r.CombineConfidentialForTest(t.Context(), svc, cfg))
			err = r.PreparePlatformWorkloadForTest(t.Context(), svc, cfg)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				require.ErrorContains(t, err, "spec.template.initContainers[name=format-disk].env[name=LUKS_RESOURCE_ID]")
				binding := &rbacv1.RoleBinding{}
				assert.True(t, apierrors.IsNotFound(r.Get(t.Context(), client.ObjectKey{Namespace: svc.Namespace, Name: "coco-cc-storage"}, binding)))
				return
			}
			require.NoError(t, err)
			require.NoError(t, r.PreparePlatformWorkloadForTest(t.Context(), svc, cfg))
			deployment, err := r.ConfidentialDeploymentForTest(t.Context(), svc, cfg)
			require.NoError(t, err)
			pod := &deployment.Spec.Template.Spec
			formatter := utils.GetInitContainerWithName(pod, "format-disk")
			require.NotNil(t, formatter)
			var diskKeys []corev1.EnvVar
			for _, env := range formatter.Env {
				if env.Name == "LUKS_RESOURCE_ID" {
					diskKeys = append(diskKeys, env)
				}
			}
			assert.Equal(t, []corev1.EnvVar{tc.override}, diskKeys)
			assert.Equal(t, "installation-initdata", deployment.Spec.Template.Annotations["io.katacontainers.config.hypervisor.cc_init_data"])
			initializer := utils.GetInitContainerWithName(pod, "storage-initializer")
			require.NotNil(t, initializer)
			assert.Contains(t, initializer.Env, corev1.EnvVar{Name: constants.ConfidentialResourceIdEnvVar, Value: "kbs:///default/model/key"})
		})
	}
}
