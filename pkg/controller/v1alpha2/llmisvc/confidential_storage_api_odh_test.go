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
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
	"github.com/kserve/kserve/pkg/constants"
	"github.com/kserve/kserve/pkg/controller/v1alpha2/llmisvc"
	pkgtest "github.com/kserve/kserve/pkg/testing"
)

// Exercise optimistic locking against an actual API server rather than relying
// on the fake client's patch and resourceVersion behavior. No storage is mounted.
func TestConfidentialLocalStorageAPI(t *testing.T) {
	env := pkgtest.NewEnvTest(pkgtest.WithCRDs(filepath.Join(pkgtest.ProjectRoot(), "config/crd/full/llmisvc/serving.kserve.io_llminferenceservices.yaml"))).BuildEnvironment()
	config, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, env.Stop()) })
	c, err := client.NewWithWatch(config, client.Options{Scheme: env.Scheme})
	require.NoError(t, err)
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "confidential-storage-"}}
	require.NoError(t, c.Create(t.Context(), ns))
	t.Cleanup(func() { require.NoError(t, c.Delete(context.Background(), ns)) })
	require.NoError(t, c.Create(t.Context(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: constants.KServeNamespace}}))
	require.NoError(t, c.Create(t.Context(), confidentialBundle(t)))
	require.NoError(t, c.Create(t.Context(), &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: constants.KServeNamespace, Name: constants.InferenceServiceConfigMapName},
		Data:       map[string]string{"confidentialBareMetal": `{"luksResourceId":"kbs:///default/disk/key"}`},
	}))
	pv, claim := localBlockFixtures()
	pv.Name, pv.Spec.ClaimRef = ns.Name+"-disk", nil
	claim.Namespace, claim.UID, claim.Spec.VolumeName = ns.Name, "", pv.Name
	// Create validates the static local block specs with no StorageClass installed.
	require.NoError(t, c.Create(t.Context(), pv))
	require.NoError(t, c.Create(t.Context(), claim))
	pv.Spec.ClaimRef = &corev1.ObjectReference{APIVersion: "v1", Kind: "PersistentVolumeClaim", Namespace: ns.Name, Name: claim.Name, UID: claim.UID}
	require.NoError(t, c.Update(t.Context(), pv))
	pv.Status.Phase = corev1.VolumeBound
	require.NoError(t, c.Status().Update(t.Context(), pv))
	claim.Status.Phase = corev1.ClaimBound
	require.NoError(t, c.Status().Update(t.Context(), claim))
	beforeSpec := claim.Spec.DeepCopy()
	fakeR, cfg := confidentialTestReconciler(t)
	first, second := confidentialService(t, "first", ns.Name), confidentialService(t, "second", ns.Name)
	for _, svc := range []*v1alpha2.LLMInferenceService{first, second} {
		svc.UID = ""
		svc.Spec.Template = &corev1.PodSpec{Volumes: []corev1.Volume{{Name: "luks-block", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: claim.Name}}}}}
		require.NoError(t, c.Create(t.Context(), svc))
		require.NoError(t, fakeR.CombineConfidentialForTest(t.Context(), svc, cfg))
	}
	r := &llmisvc.LLMISVCReconciler{Client: c}
	// Warm shared helpers, then remove the assignment to force both contenders
	// to read the same unassigned resourceVersion.
	require.NoError(t, r.PreparePlatformWorkloadForTest(t.Context(), first, cfg))
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(claim), claim))
	claim.Annotations = nil
	require.NoError(t, c.Update(t.Context(), claim))
	arrived := make(chan struct{}, 2)
	release := make(chan struct{})
	raceContext, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	wrapped := interceptor.NewClient(c, interceptor.Funcs{Patch: func(ctx context.Context, underlying client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
		if _, ok := obj.(*corev1.PersistentVolumeClaim); ok {
			arrived <- struct{}{}
			select {
			case <-release:
			case <-raceContext.Done():
				return raceContext.Err()
			}
		}
		return underlying.Patch(ctx, obj, patch, opts...)
	}})
	r.Client = wrapped
	outcomes := make(chan error, 2)
	go func() { outcomes <- r.PreparePlatformWorkloadForTest(raceContext, first, cfg) }()
	go func() { outcomes <- r.PreparePlatformWorkloadForTest(raceContext, second, cfg) }()
	for range 2 {
		select {
		case <-arrived:
		case <-raceContext.Done():
			t.Fatal("both contenders did not reach the storage assignment")
		}
	}
	close(release)
	results := []error{<-outcomes, <-outcomes}
	successes, conflicts := 0, 0
	for _, result := range results {
		switch {
		case result == nil:
			successes++
		case apierrors.IsConflict(result):
			conflicts++
		default:
			t.Fatalf("unexpected assignment error: %v", result)
		}
	}
	assert.Equal(t, 1, successes)
	assert.Equal(t, 1, conflicts)
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(claim), claim))
	assert.Equal(t, *beforeSpec, claim.Spec)
	assert.Contains(t, []string{string(first.UID), string(second.UID)}, claim.Annotations["serving.kserve.io/confidential-service-uid"])
	// Competing models must not publish two PVs for one approved pool entry.
	r.Client = c
	disk := approvedDisk(ns.Name+"-approved", "worker", "/dev/disk/by-id/approved")
	disk["namespace"] = ns.Name
	setConfidentialSettings(t, r, map[string]any{"localVolumes": []any{disk}})
	third, fourth := confidentialService(t, "third", ns.Name), confidentialService(t, "fourth", ns.Name)
	for _, svc := range []*v1alpha2.LLMInferenceService{third, fourth} {
		svc.UID = ""
		require.NoError(t, c.Create(t.Context(), svc))
		require.NoError(t, fakeR.CombineConfidentialForTest(t.Context(), svc, cfg))
	}
	allocated := make(chan error, 2)
	go func() { allocated <- r.PreparePlatformWorkloadForTest(raceContext, third, cfg) }()
	go func() { allocated <- r.PreparePlatformWorkloadForTest(raceContext, fourth, cfg) }()
	pending, unavailable := 0, 0
	for range 2 {
		select {
		case result := <-allocated:
			require.Error(t, result)
			switch {
			case strings.Contains(result.Error(), "waiting for PVC"):
				pending++
			case strings.Contains(result.Error(), "no approved local disk is available"):
				unavailable++
			default:
				t.Fatalf("unexpected allocation error: %v", result)
			}
		case <-raceContext.Done():
			t.Fatal("local disk allocation did not finish")
		}
	}
	assert.Equal(t, 1, pending)
	assert.Equal(t, 1, unavailable)
	published := &corev1.PersistentVolume{}
	require.NoError(t, c.Get(t.Context(), client.ObjectKey{Name: ns.Name + "-approved"}, published))
	assert.Contains(t, []string{"third-confidential-storage", "fourth-confidential-storage"}, published.Spec.ClaimRef.Name)
	created := &corev1.PersistentVolumeClaimList{}
	require.NoError(t, c.List(t.Context(), created, client.InNamespace(ns.Name)))
	assert.Len(t, created.Items, 2, "one existing claim and exactly one newly allocated claim")
}
