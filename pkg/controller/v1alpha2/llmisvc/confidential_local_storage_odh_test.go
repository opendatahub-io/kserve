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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
	"github.com/kserve/kserve/pkg/constants"
	"github.com/kserve/kserve/pkg/controller/v1alpha2/llmisvc"
	"github.com/kserve/kserve/pkg/utils"
)

func setConfidentialSettings(t *testing.T, r *llmisvc.LLMISVCReconciler, settings map[string]any) {
	t.Helper()
	cm := &corev1.ConfigMap{}
	require.NoError(t, r.Get(t.Context(), client.ObjectKey{Namespace: constants.KServeNamespace, Name: constants.InferenceServiceConfigMapName}, cm))
	raw, err := json.Marshal(settings)
	require.NoError(t, err)
	cm.Data["confidentialBareMetal"] = string(raw)
	require.NoError(t, r.Update(t.Context(), cm))
}

func localBlockFixtures() (*corev1.PersistentVolume, *corev1.PersistentVolumeClaim) {
	claim := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "storage-encrypted", Namespace: "project", UID: "disk-claim"},
		Spec: corev1.PersistentVolumeClaimSpec{
			StorageClassName: ptr.To(""), VolumeName: "storage-encrypted", VolumeMode: ptr.To(corev1.PersistentVolumeBlock),
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}, Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("893Gi")}},
		},
		Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound},
	}
	pv := &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "storage-encrypted"}, Spec: corev1.PersistentVolumeSpec{
		StorageClassName: "", Capacity: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("893Gi")}, VolumeMode: ptr.To(corev1.PersistentVolumeBlock),
		AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}, PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
		PersistentVolumeSource: corev1.PersistentVolumeSource{Local: &corev1.LocalVolumeSource{Path: "/dev/sda"}},
		NodeAffinity: &corev1.VolumeNodeAffinity{Required: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{MatchExpressions: []corev1.NodeSelectorRequirement{{
			Key: corev1.LabelHostname, Operator: corev1.NodeSelectorOpIn, Values: []string{"virtlab2410"},
		}}}}}},
		ClaimRef: &corev1.ObjectReference{APIVersion: "v1", Kind: "PersistentVolumeClaim", Name: claim.Name, Namespace: claim.Namespace, UID: claim.UID},
	}, Status: corev1.PersistentVolumeStatus{Phase: corev1.VolumeBound}}
	return pv, claim
}

func localBlockService(t *testing.T, r *llmisvc.LLMISVCReconciler, cfg *llmisvc.Config, name string) *v1alpha2.LLMInferenceService {
	t.Helper()
	svc := confidentialService(t, name, "project")
	svc.Spec.Template = &corev1.PodSpec{Volumes: []corev1.Volume{{Name: "luks-block", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "storage-encrypted"}}}}}
	require.NoError(t, r.Create(t.Context(), svc))
	require.NoError(t, r.CombineConfidentialForTest(t.Context(), svc, cfg))
	return svc
}

func bindLocalClaim(t *testing.T, r *llmisvc.LLMISVCReconciler, name, pvName string) {
	t.Helper()
	claim := &corev1.PersistentVolumeClaim{}
	require.NoError(t, r.Get(t.Context(), client.ObjectKey{Namespace: "project", Name: name}, claim))
	if claim.UID == "" {
		claim.UID = types.UID(name)
		require.NoError(t, r.Update(t.Context(), claim))
	}
	pv := &corev1.PersistentVolume{}
	require.NoError(t, r.Get(t.Context(), client.ObjectKey{Name: pvName}, pv))
	pv.Spec.ClaimRef = &corev1.ObjectReference{APIVersion: "v1", Kind: "PersistentVolumeClaim", Namespace: claim.Namespace, Name: claim.Name, UID: claim.UID}
	require.NoError(t, r.Update(t.Context(), pv))
	pv.Status.Phase = corev1.VolumeBound
	require.NoError(t, r.Status().Update(t.Context(), pv))
	claim.Status.Phase = corev1.ClaimBound
	require.NoError(t, r.Status().Update(t.Context(), claim))
}

func TestConfidentialReuseLocalBlockWithoutCreationDefaults(t *testing.T) {
	r, cfg := confidentialTestReconciler(t)
	require.NoError(t, r.Delete(t.Context(), &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "test-csi"}}))
	setConfidentialSettings(t, r, map[string]any{"luksResourceId": "kbs:///default/existing-disk/key"})
	pv, claim := localBlockFixtures()
	require.NoError(t, r.Create(t.Context(), pv))
	require.NoError(t, r.Create(t.Context(), claim))
	beforePV, beforeClaim := pv.DeepCopy(), claim.DeepCopy()
	svc := localBlockService(t, r, cfg, "model")
	require.NoError(t, r.PreparePlatformWorkloadForTest(t.Context(), svc, cfg))
	require.NoError(t, r.Get(t.Context(), client.ObjectKeyFromObject(claim), claim))
	assert.Equal(t, beforeClaim.Spec, claim.Spec)
	assert.Equal(t, beforeClaim.OwnerReferences, claim.OwnerReferences)
	assert.Equal(t, string(svc.UID), claim.Annotations["serving.kserve.io/confidential-service-uid"])
	require.NoError(t, r.Get(t.Context(), client.ObjectKeyFromObject(pv), pv))
	assert.Equal(t, beforePV, pv)
	formatter := utils.GetInitContainerWithName(svc.Spec.Template, "format-disk")
	assert.Contains(t, formatter.Env, corev1.EnvVar{Name: "LUKS_RESOURCE_ID", Value: "kbs:///default/existing-disk/key"})
	rv := claim.ResourceVersion
	require.NoError(t, r.PreparePlatformWorkloadForTest(t.Context(), svc, cfg))
	require.NoError(t, r.Get(t.Context(), client.ObjectKeyFromObject(claim), claim))
	assert.Equal(t, rv, claim.ResourceVersion)
}

func TestConfidentialExplicitGeneratedClaimIgnoresCreationDefaults(t *testing.T) {
	r, cfg := confidentialTestReconciler(t)
	setConfidentialSettings(t, r, map[string]any{
		"storageClassName": "unavailable-creation-default", "storageSize": "1Ti", "luksResourceId": "kbs:///default/disk/key",
	})
	pv, claim := localBlockFixtures()
	claim.Name = "model-confidential-storage"
	claim.Annotations = map[string]string{"serving.kserve.io/confidential-service-uid": "previous-instance"}
	pv.Spec.ClaimRef.Name = claim.Name
	require.NoError(t, r.Create(t.Context(), pv))
	require.NoError(t, r.Create(t.Context(), claim))
	svc := confidentialService(t, "model", "project")
	svc.Spec.Template = &corev1.PodSpec{Volumes: []corev1.Volume{{Name: "luks-block", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: claim.Name}}}}}
	require.NoError(t, r.Create(t.Context(), svc))
	require.NoError(t, r.CombineConfidentialForTest(t.Context(), svc, cfg))
	require.NoError(t, r.PreparePlatformWorkloadForTest(t.Context(), svc, cfg))
	require.NoError(t, r.Get(t.Context(), client.ObjectKeyFromObject(claim), claim))
	assert.Equal(t, "893Gi", claim.Spec.Resources.Requests.Storage().String())
	assert.Equal(t, string(svc.UID), claim.Annotations["serving.kserve.io/confidential-service-uid"])
}

func TestConfidentialLocalValidationPreservesStorage(t *testing.T) {
	for _, tc := range []struct {
		name     string
		change   func(*corev1.PersistentVolume, *corev1.PersistentVolumeClaim)
		settings map[string]any
		want     string
	}{
		{name: "missing disk key", settings: map[string]any{}, want: "configure luksResourceId"},
		{name: "wrong binding UID", change: func(pv *corev1.PersistentVolume, _ *corev1.PersistentVolumeClaim) {
			pv.Spec.ClaimRef.UID = "another-claim"
		}, want: "binding and capacity"},
		{name: "filesystem", change: func(_ *corev1.PersistentVolume, c *corev1.PersistentVolumeClaim) {
			c.Spec.VolumeMode = ptr.To(corev1.PersistentVolumeFilesystem)
		}, want: "raw block"},
		{name: "missing node affinity", change: func(pv *corev1.PersistentVolume, _ *corev1.PersistentVolumeClaim) { pv.Spec.NodeAffinity = nil }, want: "node affinity"},
		{name: "backing volume too small", change: func(pv *corev1.PersistentVolume, _ *corev1.PersistentVolumeClaim) {
			pv.Spec.Capacity[corev1.ResourceStorage] = resource.MustParse("1Gi")
		}, want: "binding and capacity"},
		{name: "RWO without a local disk", change: func(pv *corev1.PersistentVolume, _ *corev1.PersistentVolumeClaim) { pv.Spec.Local = nil }, want: "local storage requires"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, cfg := confidentialTestReconciler(t)
			settings := tc.settings
			if settings == nil {
				settings = map[string]any{"luksResourceId": "kbs:///default/disk/key"}
			}
			setConfidentialSettings(t, r, settings)
			pv, claim := localBlockFixtures()
			if tc.change != nil {
				tc.change(pv, claim)
			}
			require.NoError(t, r.Create(t.Context(), pv))
			require.NoError(t, r.Create(t.Context(), claim))
			beforePV, beforeClaim := pv.DeepCopy(), claim.DeepCopy()
			svc := localBlockService(t, r, cfg, "model")
			require.ErrorContains(t, r.PreparePlatformWorkloadForTest(t.Context(), svc, cfg), tc.want)
			require.NoError(t, r.Get(t.Context(), client.ObjectKeyFromObject(pv), pv))
			require.NoError(t, r.Get(t.Context(), client.ObjectKeyFromObject(claim), claim))
			assert.Equal(t, beforePV, pv)
			assert.Equal(t, beforeClaim, claim)
		})
	}
}

func TestConfidentialLocalDiskExclusiveAssignment(t *testing.T) {
	r, cfg := confidentialTestReconciler(t)
	setConfidentialSettings(t, r, map[string]any{"luksResourceId": "kbs:///default/disk/key"})
	pv, claim := localBlockFixtures()
	require.NoError(t, r.Create(t.Context(), pv))
	require.NoError(t, r.Create(t.Context(), claim))
	first := localBlockService(t, r, cfg, "first")
	second := localBlockService(t, r, cfg, "second")
	require.NoError(t, r.PreparePlatformWorkloadForTest(t.Context(), first, cfg))
	require.ErrorContains(t, r.PreparePlatformWorkloadForTest(t.Context(), second, cfg), "assigned to model service first")
	require.NoError(t, r.Get(t.Context(), client.ObjectKeyFromObject(claim), claim))
	assert.Equal(t, string(first.UID), claim.Annotations["serving.kserve.io/confidential-service-uid"])
	require.NoError(t, r.Delete(t.Context(), first))
	// A deliberate selection can reuse retained storage after its old service is gone.
	require.NoError(t, r.PreparePlatformWorkloadForTest(t.Context(), second, cfg))
}

func TestConfidentialLocalDiskExistingConsumers(t *testing.T) {
	for _, tc := range []struct {
		phase       corev1.PodPhase
		terminating bool
	}{
		{corev1.PodRunning, false},
		{corev1.PodPending, false},
		{corev1.PodSucceeded, false},
		{corev1.PodSucceeded, true},
		{corev1.PodFailed, true},
	} {
		name := string(tc.phase)
		if tc.terminating {
			name += "-terminating"
		}
		t.Run(name, func(t *testing.T) {
			r, cfg := confidentialTestReconciler(t)
			setConfidentialSettings(t, r, map[string]any{"luksResourceId": "kbs:///default/disk/key"})
			pv, claim := localBlockFixtures()
			require.NoError(t, r.Create(t.Context(), pv))
			require.NoError(t, r.Create(t.Context(), claim))
			svc := localBlockService(t, r, cfg, "model")
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "manual-consumer", Namespace: svc.Namespace}, Spec: corev1.PodSpec{Volumes: svc.Spec.Template.Volumes}, Status: corev1.PodStatus{Phase: tc.phase}}
			if tc.terminating {
				pod.Finalizers = []string{"test.kserve.io/hold-consumer"}
			}
			require.NoError(t, r.Create(t.Context(), pod))
			if tc.terminating {
				require.NoError(t, r.Delete(t.Context(), pod))
				require.NoError(t, r.Get(t.Context(), client.ObjectKeyFromObject(pod), pod))
				require.NotNil(t, pod.DeletionTimestamp)
			}
			err := r.PreparePlatformWorkloadForTest(t.Context(), svc, cfg)
			if tc.phase == corev1.PodSucceeded && !tc.terminating {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, "still used by pod manual-consumer")
			}
		})
	}
}

func TestConfidentialLocalDiskOwnPodReplacement(t *testing.T) {
	r, cfg := confidentialTestReconciler(t)
	setConfidentialSettings(t, r, map[string]any{"luksResourceId": "kbs:///default/disk/key"})
	pv, claim := localBlockFixtures()
	require.NoError(t, r.Create(t.Context(), pv))
	require.NoError(t, r.Create(t.Context(), claim))
	svc := localBlockService(t, r, cfg, "model")
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "model-kserve", Namespace: svc.Namespace, UID: "deployment", OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(svc, v1alpha2.LLMInferenceServiceGVK)}}}
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "model-rs", Namespace: svc.Namespace, UID: "replica-set", OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(deployment, appsv1.SchemeGroupVersion.WithKind("Deployment"))}}}
	require.NoError(t, r.Create(t.Context(), deployment))
	require.NoError(t, r.Create(t.Context(), rs))
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "model-pod", Namespace: svc.Namespace, OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(rs, appsv1.SchemeGroupVersion.WithKind("ReplicaSet"))}}, Spec: corev1.PodSpec{Volumes: svc.Spec.Template.Volumes}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	require.NoError(t, r.Create(t.Context(), pod))
	require.NoError(t, r.PreparePlatformWorkloadForTest(t.Context(), svc, cfg))
	pod.ResourceVersion, pod.Name = "", "extra-model-pod"
	require.NoError(t, r.Create(t.Context(), pod))
	require.ErrorContains(t, r.PreparePlatformWorkloadForTest(t.Context(), svc, cfg), "multiple model pods")
}

func TestConfidentialNamedLocalPV(t *testing.T) {
	r, cfg := confidentialTestReconciler(t)
	setConfidentialSettings(t, r, map[string]any{"luksResourceId": "kbs:///default/disk/key"})
	pv, _ := localBlockFixtures()
	pv.Spec.ClaimRef, pv.Status.Phase = nil, corev1.VolumeAvailable
	require.NoError(t, r.Create(t.Context(), pv))
	svc := confidentialService(t, "model", "project")
	svc.Annotations = map[string]string{"serving.kserve.io/confidential-pv": pv.Name}
	require.NoError(t, r.Create(t.Context(), svc))
	require.NoError(t, r.CombineConfidentialForTest(t.Context(), svc, cfg))
	require.ErrorContains(t, r.PreparePlatformWorkloadForTest(t.Context(), svc, cfg), "waiting for PVC")
	claim := &corev1.PersistentVolumeClaim{}
	require.NoError(t, r.Get(t.Context(), client.ObjectKey{Namespace: svc.Namespace, Name: "model-confidential-storage"}, claim))
	assert.Equal(t, "", *claim.Spec.StorageClassName)
	assert.Equal(t, pv.Name, claim.Spec.VolumeName)
	assert.Equal(t, "893Gi", claim.Spec.Resources.Requests.Storage().String())
	bindLocalClaim(t, r, claim.Name, pv.Name)
	require.NoError(t, r.PreparePlatformWorkloadForTest(t.Context(), svc, cfg))
}

func approvedDisk(name, node, device string) map[string]any {
	return map[string]any{"name": name, "namespace": "project", "nodeName": node, "devicePath": device, "capacity": "300Gi", "luksResourceId": "kbs:///default/approved-disk/key"}
}

func TestConfidentialApprovedLocalAllocation(t *testing.T) {
	r, cfg := confidentialTestReconciler(t)
	setConfidentialSettings(t, r, map[string]any{"localVolumes": []any{approvedDisk("approved-disk", "worker", "/dev/disk/by-id/approved")}})
	svc := confidentialService(t, "model", "project")
	require.NoError(t, r.Create(t.Context(), svc))
	require.NoError(t, r.CombineConfidentialForTest(t.Context(), svc, cfg))
	require.ErrorContains(t, r.PreparePlatformWorkloadForTest(t.Context(), svc, cfg), "waiting for PVC")
	pv := &corev1.PersistentVolume{}
	require.NoError(t, r.Get(t.Context(), client.ObjectKey{Name: "approved-disk"}, pv))
	assert.Equal(t, corev1.PersistentVolumeReclaimRetain, pv.Spec.PersistentVolumeReclaimPolicy)
	assert.Empty(t, pv.OwnerReferences)
	assert.Equal(t, "model-confidential-storage", pv.Spec.ClaimRef.Name)
	assert.Equal(t, "project", pv.Spec.ClaimRef.Namespace)
	assert.Equal(t, "/dev/disk/by-id/approved", pv.Spec.Local.Path)
	before := pv.DeepCopy()
	require.ErrorContains(t, r.PreparePlatformWorkloadForTest(t.Context(), svc, cfg), "waiting for PVC")
	require.NoError(t, r.Get(t.Context(), client.ObjectKeyFromObject(pv), pv))
	assert.Equal(t, before, pv)
	second := confidentialService(t, "second", "project")
	require.NoError(t, r.CombineConfidentialForTest(t.Context(), second, cfg))
	require.ErrorContains(t, r.PreparePlatformWorkloadForTest(t.Context(), second, cfg), "no approved local disk is available")
	bindLocalClaim(t, r, "model-confidential-storage", pv.Name)
	require.NoError(t, r.PreparePlatformWorkloadForTest(t.Context(), svc, cfg))
	assert.Contains(t, utils.GetInitContainerWithName(svc.Spec.Template, "format-disk").Env, corev1.EnvVar{Name: "LUKS_RESOURCE_ID", Value: "kbs:///default/approved-disk/key"})
	require.NoError(t, r.Delete(t.Context(), svc))
	require.NoError(t, r.Get(t.Context(), client.ObjectKeyFromObject(pv), pv))
	claim := &corev1.PersistentVolumeClaim{}
	require.NoError(t, r.Get(t.Context(), client.ObjectKey{Namespace: "project", Name: "model-confidential-storage"}, claim))
	assert.Empty(t, claim.OwnerReferences)
}

func TestConfidentialLocalCreationRejectsBadInputs(t *testing.T) {
	for _, tc := range []struct {
		name     string
		settings map[string]any
		want     string
	}{
		{"no storage configured", map[string]any{"luksResourceId": "kbs:///default/disk/key", "storageSize": "1Gi"}, "no StorageClass or approved local disk"},
		{"negative size", map[string]any{"storageSize": "-1Gi"}, "must not be negative"},
		{"invalid disk key", map[string]any{"luksResourceId": "not-a-kbs-key"}, "luksResourceId must identify"},
		{"no key for approved disk", map[string]any{"localVolumes": []any{map[string]any{"name": "disk", "namespace": "project", "nodeName": "worker", "devicePath": "/dev/sdb", "capacity": "1Gi"}}}, "configure luksResourceId"},
		{"disk too small", map[string]any{"storageSize": "500Gi", "localVolumes": []any{approvedDisk("disk", "worker", "/dev/sdb")}}, "sufficient capacity"},
		{"duplicate disks", map[string]any{"localVolumes": []any{approvedDisk("one", "worker", "/dev/sdb"), approvedDisk("two", "worker", "/dev/sdb")}}, "duplicate localVolumes"},
		{"unauthorized namespace", map[string]any{"localVolumes": []any{map[string]any{"name": "disk", "namespace": "another-project", "nodeName": "worker", "devicePath": "/dev/sdb", "capacity": "1Gi"}}, "storageSize": "1Gi", "luksResourceId": "kbs:///default/disk/key"}, "no StorageClass or approved local disk"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, cfg := confidentialTestReconciler(t)
			setConfidentialSettings(t, r, tc.settings)
			svc := confidentialService(t, "model", "project")
			require.NoError(t, r.CombineConfidentialForTest(t.Context(), svc, cfg))
			require.ErrorContains(t, r.PreparePlatformWorkloadForTest(t.Context(), svc, cfg), tc.want)
			claims, volumes := &corev1.PersistentVolumeClaimList{}, &corev1.PersistentVolumeList{}
			require.NoError(t, r.List(t.Context(), claims))
			require.NoError(t, r.List(t.Context(), volumes))
			assert.Empty(t, claims.Items)
			assert.Empty(t, volumes.Items)
		})
	}
}

func TestConfidentialDoesNotPublishExistingDeviceAgain(t *testing.T) {
	r, cfg := confidentialTestReconciler(t)
	pv, claim := localBlockFixtures()
	require.NoError(t, r.Create(t.Context(), pv))
	require.NoError(t, r.Create(t.Context(), claim))
	setConfidentialSettings(t, r, map[string]any{"localVolumes": []any{approvedDisk("duplicate-device", "virtlab2410", "/dev/sda")}})
	svc := confidentialService(t, "model", "project")
	require.NoError(t, r.CombineConfidentialForTest(t.Context(), svc, cfg))
	require.ErrorContains(t, r.PreparePlatformWorkloadForTest(t.Context(), svc, cfg), "already represented by PV storage-encrypted")
	volumes := &corev1.PersistentVolumeList{}
	require.NoError(t, r.List(t.Context(), volumes))
	assert.Len(t, volumes.Items, 1)
}

func TestConfidentialDefaultCSIStorageClass(t *testing.T) {
	r, cfg := confidentialTestReconciler(t)
	sc := &storagev1.StorageClass{}
	require.NoError(t, r.Get(t.Context(), client.ObjectKey{Name: "test-csi"}, sc))
	sc.Annotations = map[string]string{"storageclass.kubernetes.io/is-default-class": "true"}
	require.NoError(t, r.Update(t.Context(), sc))
	setConfidentialSettings(t, r, map[string]any{"storageSize": "50Gi", "luksResourceId": "kbs:///default/disk/key"})
	svc := confidentialService(t, "model", "project")
	require.NoError(t, r.CombineConfidentialForTest(t.Context(), svc, cfg))
	require.NoError(t, r.PreparePlatformWorkloadForTest(t.Context(), svc, cfg))
	claim := &corev1.PersistentVolumeClaim{}
	require.NoError(t, r.Get(t.Context(), client.ObjectKey{Namespace: svc.Namespace, Name: "model-confidential-storage"}, claim))
	assert.Equal(t, "test-csi", *claim.Spec.StorageClassName)
	assert.Equal(t, []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOncePod}, claim.Spec.AccessModes)
}

func TestConfidentialLocalSelectionChecks(t *testing.T) {
	for _, tc := range []struct {
		name      string
		configure func(*llmisvc.LLMISVCReconciler, *v1alpha2.LLMInferenceService)
		want      string
	}{
		{"read-only claim", func(_ *llmisvc.LLMISVCReconciler, svc *v1alpha2.LLMInferenceService) {
			for i := range svc.Spec.Template.Volumes {
				if svc.Spec.Template.Volumes[i].Name == "luks-block" {
					svc.Spec.Template.Volumes[i].PersistentVolumeClaim.ReadOnly = true
				}
			}
		}, "must allow writes"},
		{"explicit minimum", func(_ *llmisvc.LLMISVCReconciler, svc *v1alpha2.LLMInferenceService) {
			svc.Annotations = map[string]string{"serving.kserve.io/confidential-storage-size": "1Ti"}
		}, "at least 1Ti"},
		{"malformed defaults", func(r *llmisvc.LLMISVCReconciler, _ *v1alpha2.LLMInferenceService) {
			cm := &corev1.ConfigMap{}
			require.NoError(t, r.Get(t.Context(), client.ObjectKey{Namespace: constants.KServeNamespace, Name: constants.InferenceServiceConfigMapName}, cm))
			cm.Data["confidentialBareMetal"] = "invalid-json"
			require.NoError(t, r.Update(t.Context(), cm))
		}, "invalid character"},
		{"absent defaults", func(r *llmisvc.LLMISVCReconciler, _ *v1alpha2.LLMInferenceService) {
			cm := &corev1.ConfigMap{}
			require.NoError(t, r.Get(t.Context(), client.ObjectKey{Namespace: constants.KServeNamespace, Name: constants.InferenceServiceConfigMapName}, cm))
			delete(cm.Data, "confidentialBareMetal")
			require.NoError(t, r.Update(t.Context(), cm))
		}, "configure luksResourceId"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, cfg := confidentialTestReconciler(t)
			setConfidentialSettings(t, r, map[string]any{"luksResourceId": "kbs:///default/disk/key"})
			pv, claim := localBlockFixtures()
			require.NoError(t, r.Create(t.Context(), pv))
			require.NoError(t, r.Create(t.Context(), claim))
			svc := localBlockService(t, r, cfg, "model")
			tc.configure(r, svc)
			require.ErrorContains(t, r.PreparePlatformWorkloadForTest(t.Context(), svc, cfg), tc.want)
			require.NoError(t, r.Get(t.Context(), client.ObjectKeyFromObject(claim), claim))
			assert.Empty(t, claim.Annotations)
		})
	}
}

func TestConfidentialMissingRetainedClaimDoesNotAllocateReplacement(t *testing.T) {
	r, cfg := confidentialTestReconciler(t)
	setConfidentialSettings(t, r, map[string]any{"localVolumes": []any{approvedDisk("empty-replacement", "worker", "/dev/sdb")}})
	pv, claim := localBlockFixtures()
	claim.Name = "model-confidential-storage"
	pv.Spec.ClaimRef.Name = claim.Name
	// The PVC was deleted, but its data and original binding remain on the PV.
	require.NoError(t, r.Create(t.Context(), pv))
	svc := confidentialService(t, "model", "project")
	require.NoError(t, r.CombineConfidentialForTest(t.Context(), svc, cfg))
	require.ErrorContains(t, r.PreparePlatformWorkloadForTest(t.Context(), svc, cfg), "retains a binding to missing PVC")
	claims, volumes := &corev1.PersistentVolumeClaimList{}, &corev1.PersistentVolumeList{}
	require.NoError(t, r.List(t.Context(), claims))
	require.NoError(t, r.List(t.Context(), volumes))
	assert.Empty(t, claims.Items)
	assert.Len(t, volumes.Items, 1)
}
