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

package reconcilers

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	apierr "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
	"github.com/kserve/kserve/pkg/apis/serving/v1beta1"
	"github.com/kserve/kserve/pkg/constants"
)

// sharedPVCPlatformClient wraps a client to observe and fail the NetworkPolicy
// operations the shared-PVC platform hook issues.
type sharedPVCPlatformClient struct {
	client.Client
	getNotFoundOnce bool  // the first NetworkPolicy Get reports NotFound, forcing the create path
	getErr          error // every NetworkPolicy Get fails
	createErr       error // NetworkPolicy Create fails
	updateErr       error // NetworkPolicy Update fails
	writes          int   // NetworkPolicy Update calls issued
}

func (c *sharedPVCPlatformClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if _, ok := obj.(*netv1.NetworkPolicy); ok {
		if c.getErr != nil {
			return c.getErr
		}
		if c.getNotFoundOnce {
			c.getNotFoundOnce = false
			return apierr.NewNotFound(netv1.SchemeGroupVersion.WithResource("networkpolicies").GroupResource(), key.Name)
		}
	}
	return c.Client.Get(ctx, key, obj, opts...)
}

func (c *sharedPVCPlatformClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if _, ok := obj.(*netv1.NetworkPolicy); ok && c.createErr != nil {
		return c.createErr
	}
	return c.Client.Create(ctx, obj, opts...)
}

func (c *sharedPVCPlatformClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	if _, ok := obj.(*netv1.NetworkPolicy); ok {
		c.writes++
		if c.updateErr != nil {
			return c.updateErr
		}
	}
	return c.Client.Update(ctx, obj, opts...)
}

// stalePolicyGetClient serves NetworkPolicy Gets from a fixed stale copy, simulating a
// reconcile whose read predates a concurrent write to the live object.
type stalePolicyGetClient struct {
	client.Client
	stale *netv1.NetworkPolicy
}

func (c *stalePolicyGetClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if policy, ok := obj.(*netv1.NetworkPolicy); ok && c.stale != nil {
		*policy = *c.stale.DeepCopy()
		return nil
	}
	return c.Client.Get(ctx, key, obj, opts...)
}

// freshSharedCache returns a shared-PVC cache with the finalizer already set and no import
// record, so a reconcile proceeds to the import Job path.
func freshSharedCache() *v1alpha1.LocalModelNamespaceCache {
	cache := importedSharedCache("")
	cache.Status = v1alpha1.LocalModelCacheStatus{}
	cache.UID = "cache-uid"
	return cache
}

func importEgressPolicyKey(cache *v1alpha1.LocalModelNamespaceCache) types.NamespacedName {
	return types.NamespacedName{Name: sharedPVCImportEgressPolicyName(cache.Name), Namespace: cache.Namespace}
}

func TestSharedPVCImportJobTemplateLabels(t *testing.T) {
	cache := &v1alpha1.LocalModelNamespaceCache{ObjectMeta: metav1.ObjectMeta{Name: "cache", Namespace: "ns"}}
	job := &batchv1.Job{Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"model": "cache", "modelNamespace": "ns"}},
	}}}

	customizeSharedPVCImportJob(job, cache)
	// The literal label values are the selector of the cache-owned import egress policy
	// (sharedPVCImportEgressSelector): asserting them here keeps the Go constants equal
	// to the literals the ODH NetworkPolicies select on, which kserve-module's render
	// test pins independently (it cannot import this module's constants).
	wantLabels := map[string]string{
		"model":                                 "cache",
		"modelNamespace":                        "ns",
		"serving.kserve.io/localmodel-download": "shared-pvc",
		"internal.serving.kserve.io/localmodel": "cache",
	}
	if !reflect.DeepEqual(job.Spec.Template.Labels, wantLabels) {
		t.Fatalf("pod template labels = %#v, want the managed labels merged with the caller's", job.Spec.Template.Labels)
	}

	// A nil label map is initialized rather than skipped.
	nilLabels := &batchv1.Job{}
	customizeSharedPVCImportJob(nilLabels, cache)
	wantManaged := map[string]string{
		"serving.kserve.io/localmodel-download": "shared-pvc",
		"internal.serving.kserve.io/localmodel": "cache",
	}
	if !reflect.DeepEqual(nilLabels.Spec.Template.Labels, wantManaged) {
		t.Fatalf("pod template labels = %#v, want the two managed labels", nilLabels.Spec.Template.Labels)
	}
}

func TestSharedPVCImportEgressPolicyName(t *testing.T) {
	if got := sharedPVCImportEgressPolicyName("my-cache"); got != "my-cache-import-egress" {
		t.Fatalf("sharedPVCImportEgressPolicyName() = %q, want my-cache-import-egress", got)
	}
}

// A first import creates the cache-owned egress policy before the import Job, and the new
// Job's pod template carries exactly the labels the policy selects on.
func TestSharedPVCImportEgressPolicyCreatedWithImportJob(t *testing.T) {
	cache := freshSharedCache()
	pvc := boundSharedPVC("pvc-uid")
	cl, scheme := sharedPVCFixture(t, cache, pvc)
	reconciler := &LocalModelNamespaceCacheReconciler{Client: cl, Scheme: scheme}

	if _, err := reconciler.reconcileSharedPVC(context.Background(), cache, &corev1.ConfigMap{}, &v1beta1.LocalModelConfig{}, cacheConsumers{}); err != nil {
		t.Fatalf("reconcileSharedPVC() error = %v", err)
	}

	policy := &netv1.NetworkPolicy{}
	if err := cl.Get(context.Background(), importEgressPolicyKey(cache), policy); err != nil {
		t.Fatalf("import egress NetworkPolicy must exist before the import Job: %v", err)
	}
	if !reflect.DeepEqual(policy.Spec.PodSelector, sharedPVCImportEgressSelector(cache.Name)) {
		t.Fatalf("policy pod selector = %#v, want the download-role and cache-identity conjunction", policy.Spec.PodSelector)
	}
	if !reflect.DeepEqual(policy.Spec.PolicyTypes, []netv1.PolicyType{netv1.PolicyTypeEgress}) {
		t.Fatalf("policy types = %#v, want egress-only", policy.Spec.PolicyTypes)
	}
	if len(policy.Spec.Egress) != 1 || !reflect.DeepEqual(policy.Spec.Egress[0], netv1.NetworkPolicyEgressRule{}) {
		t.Fatalf("policy egress = %#v, want exactly one empty allow-all rule", policy.Spec.Egress)
	}
	if len(policy.Spec.Ingress) != 0 {
		t.Fatalf("policy ingress = %#v, want none: the grant is egress-only", policy.Spec.Ingress)
	}
	owner := metav1.GetControllerOf(policy)
	if owner == nil || owner.UID != cache.UID || owner.Kind != "LocalModelNamespaceCache" {
		t.Fatalf("policy controller owner = %#v, want LocalModelNamespaceCache %s (UID %s)", owner, cache.Name, cache.UID)
	}
	if owner.BlockOwnerDeletion == nil || *owner.BlockOwnerDeletion {
		t.Fatalf("policy owner blockOwnerDeletion = %v, want false: the finalizer flow must not block on policy GC", owner.BlockOwnerDeletion)
	}

	job := &batchv1.Job{}
	if err := cl.Get(context.Background(), types.NamespacedName{Name: importJobName(cache.Name), Namespace: cache.Namespace}, job); err != nil {
		t.Fatalf("import Job must be created: %v", err)
	}
	if job.Spec.Template.Labels[constants.DownloadJobLabelKey] != constants.DownloadJobLabelValueSharedPVC ||
		job.Spec.Template.Labels[constants.LocalModelLabel] != cache.Name {
		t.Fatalf("import Job pod template labels = %#v, want the labels the policy selects on", job.Spec.Template.Labels)
	}
}

// A policy API failure is returned through the existing import-pending handling and no new
// import Job is created until the grant exists.
func TestSharedPVCImportEgressPolicyErrorBlocksJobCreation(t *testing.T) {
	tests := []struct {
		name      string
		getErr    error
		createErr error
	}{
		{name: "get fails", getErr: errors.New("policy read failed")},
		{name: "create fails", createErr: errors.New("policy create failed")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cache := freshSharedCache()
			pvc := boundSharedPVC("pvc-uid")
			cl, scheme := sharedPVCFixture(t, cache, pvc)
			wrapped := &sharedPVCPlatformClient{Client: cl, getErr: tt.getErr, createErr: tt.createErr}
			reconciler := &LocalModelNamespaceCacheReconciler{Client: wrapped, Scheme: scheme}

			_, err := reconciler.reconcileSharedPVC(context.Background(), cache, &corev1.ConfigMap{}, &v1beta1.LocalModelConfig{}, cacheConsumers{})
			want := tt.getErr
			if want == nil {
				want = tt.createErr
			}
			if !errors.Is(err, want) {
				t.Fatalf("reconcileSharedPVC() error = %v, want the policy failure %v", err, want)
			}
			condition := cache.Status.GetCondition(v1alpha1.LocalModelCacheReady)
			if condition == nil || condition.Reason != v1alpha1.ReasonImportPending {
				t.Fatalf("cache Ready condition = %#v, want %s", condition, v1alpha1.ReasonImportPending)
			}
			if got := listImportJobs(t, cl); len(got) != 0 {
				t.Fatalf("no import Job may be created while the egress policy cannot be ensured, got %d", len(got))
			}
		})
	}
}

// Repeated reconciles of an already-correct policy issue no writes: the comparison covers
// the spec and the managed owner reference only.
func TestSharedPVCImportEgressPolicyReconcileIsIdempotent(t *testing.T) {
	cache := freshSharedCache()
	pvc := boundSharedPVC("pvc-uid")
	cl, scheme := sharedPVCFixture(t, cache, pvc)
	wrapped := &sharedPVCPlatformClient{Client: cl}
	reconciler := &LocalModelNamespaceCacheReconciler{Client: wrapped, Scheme: scheme}

	for i := range 2 {
		if _, err := reconciler.reconcileSharedPVC(context.Background(), cache, &corev1.ConfigMap{}, &v1beta1.LocalModelConfig{}, cacheConsumers{}); err != nil {
			t.Fatalf("reconcileSharedPVC() #%d error = %v", i+1, err)
		}
	}
	if wrapped.writes != 0 {
		t.Fatalf("an already-correct policy must not be rewritten, got %d write(s)", wrapped.writes)
	}
	policies := &netv1.NetworkPolicyList{}
	if err := cl.List(context.Background(), policies, client.InNamespace(cache.Namespace)); err != nil {
		t.Fatalf("list policies: %v", err)
	}
	if len(policies.Items) != 1 {
		t.Fatalf("expected exactly one import egress policy, got %d", len(policies.Items))
	}
	if got := listImportJobs(t, cl); len(got) != 1 {
		t.Fatalf("expected exactly one import Job, got %d", len(got))
	}
}

// Spec drift on the owned policy is repaired while unrelated metadata on the live object
// is retained.
func TestSharedPVCImportEgressPolicyDriftRepaired(t *testing.T) {
	cache := freshSharedCache()
	pvc := boundSharedPVC("pvc-uid")
	cl, scheme := sharedPVCFixture(t, cache, pvc)

	drifted := &netv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:        sharedPVCImportEgressPolicyName(cache.Name),
			Namespace:   cache.Namespace,
			Labels:      map[string]string{"team": "odh"},
			Annotations: map[string]string{"note": "keep"},
		},
		// Drifted shape: the cache-identity label is missing from the selector, egress is
		// restricted to one port, and an ingress rule is present.
		Spec: netv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{
				constants.DownloadJobLabelKey: constants.DownloadJobLabelValueSharedPVC,
			}},
			PolicyTypes: []netv1.PolicyType{netv1.PolicyTypeIngress, netv1.PolicyTypeEgress},
			Ingress:     []netv1.NetworkPolicyIngressRule{{}},
			Egress:      []netv1.NetworkPolicyEgressRule{{Ports: []netv1.NetworkPolicyPort{{Port: ptrTo(intstr.FromInt32(443))}}}},
		},
	}
	if err := controllerutil.SetControllerReference(cache, drifted, scheme); err != nil {
		t.Fatalf("set owner: %v", err)
	}
	if err := cl.Create(context.Background(), drifted); err != nil {
		t.Fatalf("create drifted policy: %v", err)
	}
	wrapped := &sharedPVCPlatformClient{Client: cl}
	reconciler := &LocalModelNamespaceCacheReconciler{Client: wrapped, Scheme: scheme}

	if _, err := reconciler.reconcileSharedPVC(context.Background(), cache, &corev1.ConfigMap{}, &v1beta1.LocalModelConfig{}, cacheConsumers{}); err != nil {
		t.Fatalf("reconcileSharedPVC() error = %v", err)
	}
	repaired := &netv1.NetworkPolicy{}
	if err := cl.Get(context.Background(), importEgressPolicyKey(cache), repaired); err != nil {
		t.Fatalf("get repaired policy: %v", err)
	}
	if !reflect.DeepEqual(repaired.Spec.PodSelector, sharedPVCImportEgressSelector(cache.Name)) {
		t.Fatalf("repaired pod selector = %#v, want the full conjunction", repaired.Spec.PodSelector)
	}
	if !reflect.DeepEqual(repaired.Spec.PolicyTypes, []netv1.PolicyType{netv1.PolicyTypeEgress}) || len(repaired.Spec.Ingress) != 0 ||
		len(repaired.Spec.Egress) != 1 || !reflect.DeepEqual(repaired.Spec.Egress[0], netv1.NetworkPolicyEgressRule{}) {
		t.Fatalf("repaired spec = %#v, want the egress-only allow-all shape", repaired.Spec)
	}
	if repaired.Labels["team"] != "odh" || repaired.Annotations["note"] != "keep" {
		t.Fatalf("unrelated metadata must be retained, got labels %#v annotations %#v", repaired.Labels, repaired.Annotations)
	}
	if wrapped.writes != 1 {
		t.Fatalf("drift repair must issue exactly one write, got %d", wrapped.writes)
	}
	if got := listImportJobs(t, cl); len(got) != 1 {
		t.Fatalf("expected the import Job after the policy is repaired, got %d", len(got))
	}
}

// A concurrent owner change between the controller's read and its repair write is
// rejected by the Update's resourceVersion precondition: the stale write fails as a
// conflict instead of crossing the ownership check made against the inspected copy, the
// changed owner survives, and the retry re-reads the live object and refuses it.
func TestSharedPVCImportEgressPolicyConcurrentOwnerChangeRejected(t *testing.T) {
	controller := true
	tests := []struct {
		name           string
		rewrittenOwner metav1.OwnerReference
	}{
		{
			name: "foreign owner",
			rewrittenOwner: metav1.OwnerReference{
				APIVersion: "apps/v1", Kind: "Deployment", Name: "other",
				UID: "other-uid", Controller: &controller,
			},
		},
		{
			name: "new-UID cache owner",
			rewrittenOwner: metav1.OwnerReference{
				APIVersion: v1alpha1.SchemeGroupVersion.String(), Kind: "LocalModelNamespaceCache", Name: "cache",
				UID: "new-cache-uid", Controller: &controller,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cache := freshSharedCache()
			pvc := boundSharedPVC("pvc-uid")
			cl, scheme := sharedPVCFixture(t, cache, pvc)

			// An owned policy with a drifted spec, so the reconcile must issue a repair write.
			drifted := &netv1.NetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      sharedPVCImportEgressPolicyName(cache.Name),
					Namespace: cache.Namespace,
				},
				Spec: netv1.NetworkPolicySpec{
					PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{
						constants.DownloadJobLabelKey: constants.DownloadJobLabelValueSharedPVC,
					}},
					PolicyTypes: []netv1.PolicyType{netv1.PolicyTypeEgress},
					Egress:      []netv1.NetworkPolicyEgressRule{{Ports: []netv1.NetworkPolicyPort{{Port: ptrTo(intstr.FromInt32(443))}}}},
				},
			}
			if err := controllerutil.SetControllerReference(cache, drifted, scheme, controllerutil.WithBlockOwnerDeletion(false)); err != nil {
				t.Fatalf("set owner: %v", err)
			}
			if err := cl.Create(context.Background(), drifted); err != nil {
				t.Fatalf("create drifted policy: %v", err)
			}
			// The controller reads this copy; a concurrent actor then rewrites the owner.
			stale := drifted.DeepCopy()

			current := &netv1.NetworkPolicy{}
			if err := cl.Get(context.Background(), importEgressPolicyKey(cache), current); err != nil {
				t.Fatalf("get current policy: %v", err)
			}
			current.OwnerReferences = []metav1.OwnerReference{tt.rewrittenOwner}
			if err := cl.Update(context.Background(), current); err != nil {
				t.Fatalf("rewrite owner: %v", err)
			}

			// The repair write built from the stale copy must be rejected as a conflict.
			staleClient := &stalePolicyGetClient{Client: cl, stale: stale}
			reconciler := &LocalModelNamespaceCacheReconciler{Client: staleClient, Scheme: scheme}
			_, err := reconciler.reconcileSharedPVC(context.Background(), cache, &corev1.ConfigMap{}, &v1beta1.LocalModelConfig{}, cacheConsumers{})
			if !apierr.IsConflict(err) {
				t.Fatalf("reconcileSharedPVC() error = %v, want a resourceVersion conflict rejecting the stale write", err)
			}
			if got := listImportJobs(t, cl); len(got) != 0 {
				t.Fatalf("no import Job may be created behind a rejected policy write, got %d", len(got))
			}
			live := &netv1.NetworkPolicy{}
			if err := cl.Get(context.Background(), importEgressPolicyKey(cache), live); err != nil {
				t.Fatalf("get live policy: %v", err)
			}
			if owner := metav1.GetControllerOf(live); owner == nil || owner.UID != tt.rewrittenOwner.UID {
				t.Fatalf("the concurrent owner rewrite must survive the rejected stale write, got %#v", owner)
			}
			if len(live.Spec.Egress) != 1 || len(live.Spec.Egress[0].Ports) != 1 {
				t.Fatalf("the drifted spec must be unrepaired while the write is rejected, got %#v", live.Spec.Egress)
			}

			// The retry re-reads the live object and refuses the changed owner.
			retry := &LocalModelNamespaceCacheReconciler{Client: cl, Scheme: scheme}
			_, err = retry.reconcileSharedPVC(context.Background(), cache, &corev1.ConfigMap{}, &v1beta1.LocalModelConfig{}, cacheConsumers{})
			if err == nil || !strings.Contains(err.Error(), "not controlled by") {
				t.Fatalf("reconcileSharedPVC() retry error = %v, want an ownership conflict", err)
			}
			if got := listImportJobs(t, cl); len(got) != 0 {
				t.Fatalf("no import Job may be created behind a changed owner, got %d", len(got))
			}
			if err := cl.Get(context.Background(), importEgressPolicyKey(cache), live); err != nil {
				t.Fatalf("get live policy after retry: %v", err)
			}
			if owner := metav1.GetControllerOf(live); owner == nil || owner.UID != tt.rewrittenOwner.UID {
				t.Fatalf("the changed owner must never be adopted or rewritten, got %#v", owner)
			}
		})
	}
}

// A foreign policy occupying the deterministic name is never adopted, its owner references
// are left untouched, and the import Job stays blocked.
func TestSharedPVCImportEgressPolicyForeignNotAdopted(t *testing.T) {
	cache := freshSharedCache()
	pvc := boundSharedPVC("pvc-uid")
	foreign := &netv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sharedPVCImportEgressPolicyName(cache.Name),
			Namespace: cache.Namespace,
		},
		Spec: netv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{},
			PolicyTypes: []netv1.PolicyType{netv1.PolicyTypeEgress},
			Egress:      []netv1.NetworkPolicyEgressRule{{}},
		},
	}
	cl, scheme := sharedPVCFixture(t, cache, pvc, foreign)
	reconciler := &LocalModelNamespaceCacheReconciler{Client: cl, Scheme: scheme}

	_, err := reconciler.reconcileSharedPVC(context.Background(), cache, &corev1.ConfigMap{}, &v1beta1.LocalModelConfig{}, cacheConsumers{})
	if err == nil || !strings.Contains(err.Error(), "not controlled by") {
		t.Fatalf("reconcileSharedPVC() error = %v, want an ownership conflict", err)
	}
	if got := listImportJobs(t, cl); len(got) != 0 {
		t.Fatalf("no import Job may be created while a foreign policy holds the name, got %d", len(got))
	}
	got := &netv1.NetworkPolicy{}
	if err := cl.Get(context.Background(), importEgressPolicyKey(cache), got); err != nil {
		t.Fatalf("get foreign policy: %v", err)
	}
	if len(got.OwnerReferences) != 0 {
		t.Fatalf("a foreign policy must never be adopted, got owner references %#v", got.OwnerReferences)
	}
}

// A policy left by a previous incarnation of the cache (same name, different UID, awaiting
// garbage collection) is not adopted either: the stale owner reference is never rewritten
// to the new UID.
func TestSharedPVCImportEgressPolicyStaleOwnerNotAdopted(t *testing.T) {
	cache := freshSharedCache()
	cache.UID = "new-cache-uid"
	pvc := boundSharedPVC("pvc-uid")
	stale := &netv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sharedPVCImportEgressPolicyName(cache.Name),
			Namespace: cache.Namespace,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: v1alpha1.SchemeGroupVersion.String(),
				Kind:       "LocalModelNamespaceCache",
				Name:       cache.Name,
				UID:        "old-cache-uid",
				Controller: ptrTo(true),
			}},
		},
		Spec: netv1.NetworkPolicySpec{
			PodSelector: sharedPVCImportEgressSelector(cache.Name),
			PolicyTypes: []netv1.PolicyType{netv1.PolicyTypeEgress},
			Egress:      []netv1.NetworkPolicyEgressRule{{}},
		},
	}
	cl, scheme := sharedPVCFixture(t, cache, pvc, stale)
	reconciler := &LocalModelNamespaceCacheReconciler{Client: cl, Scheme: scheme}

	_, err := reconciler.reconcileSharedPVC(context.Background(), cache, &corev1.ConfigMap{}, &v1beta1.LocalModelConfig{}, cacheConsumers{})
	if err == nil || !strings.Contains(err.Error(), "not controlled by") {
		t.Fatalf("reconcileSharedPVC() error = %v, want an ownership conflict", err)
	}
	if got := listImportJobs(t, cl); len(got) != 0 {
		t.Fatalf("no import Job may be created while the stale policy holds the name, got %d", len(got))
	}
	got := &netv1.NetworkPolicy{}
	if err := cl.Get(context.Background(), importEgressPolicyKey(cache), got); err != nil {
		t.Fatalf("get stale policy: %v", err)
	}
	if owner := metav1.GetControllerOf(got); owner == nil || owner.UID != "old-cache-uid" {
		t.Fatalf("the stale owner reference must never be rewritten to the new cache UID, got %#v", owner)
	}
}

// Losing a create race re-reads the winning policy and validates its ownership: an owned
// winner converges without a write, a foreign winner blocks the import Job.
func TestSharedPVCImportEgressPolicyCreateRaceRechecksOwnership(t *testing.T) {
	tests := []struct {
		name        string
		winnerOwner bool
		wantErr     bool
	}{
		{name: "owned winner converges", winnerOwner: true},
		{name: "foreign winner blocks", winnerOwner: false, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cache := freshSharedCache()
			pvc := boundSharedPVC("pvc-uid")
			cl, scheme := sharedPVCFixture(t, cache, pvc)

			winner := &netv1.NetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      sharedPVCImportEgressPolicyName(cache.Name),
					Namespace: cache.Namespace,
				},
				Spec: netv1.NetworkPolicySpec{
					PodSelector: sharedPVCImportEgressSelector(cache.Name),
					PolicyTypes: []netv1.PolicyType{netv1.PolicyTypeEgress},
					Egress:      []netv1.NetworkPolicyEgressRule{{}},
				},
			}
			if tt.winnerOwner {
				// The exact owner-reference shape the hook itself stamps, so the race winner
				// is already correct and the re-read converges without a write.
				if err := controllerutil.SetControllerReference(cache, winner, scheme, controllerutil.WithBlockOwnerDeletion(false)); err != nil {
					t.Fatalf("set owner: %v", err)
				}
			}
			if err := cl.Create(context.Background(), winner); err != nil {
				t.Fatalf("create winning policy: %v", err)
			}

			// The first Get misses the winner, so the hook attempts to create and must
			// handle AlreadyExists by re-reading and checking ownership.
			wrapped := &sharedPVCPlatformClient{Client: cl, getNotFoundOnce: true}
			reconciler := &LocalModelNamespaceCacheReconciler{Client: wrapped, Scheme: scheme}

			_, err := reconciler.reconcileSharedPVC(context.Background(), cache, &corev1.ConfigMap{}, &v1beta1.LocalModelConfig{}, cacheConsumers{})
			if tt.wantErr {
				if err == nil || !strings.Contains(err.Error(), "not controlled by") {
					t.Fatalf("reconcileSharedPVC() error = %v, want an ownership conflict", err)
				}
				if got := listImportJobs(t, cl); len(got) != 0 {
					t.Fatalf("no import Job may be created behind a foreign winner, got %d", len(got))
				}
				return
			}
			if err != nil {
				t.Fatalf("reconcileSharedPVC() error = %v", err)
			}
			if wrapped.writes != 0 {
				t.Fatalf("an already-correct race winner must not be rewritten, got %d write(s)", wrapped.writes)
			}
			if got := listImportJobs(t, cl); len(got) != 1 {
				t.Fatalf("expected the import Job after the race resolves, got %d", len(got))
			}
		})
	}
}

// The distro setup hook registers an ownership watch on NetworkPolicies, so deleting or
// drifting a cache-owned import egress policy re-enqueues the owning cache and the next
// reconcile heals it. The builder offers no public introspection of its watches, so the
// registered owned type is read from the builder input; end-to-end watch recovery is
// covered by the envtest suite.
func TestSharedPVCPlatformSetupOwnsNetworkPolicies(t *testing.T) {
	b := ctrl.NewControllerManagedBy(nil)
	(&LocalModelNamespaceCacheReconciler{}).extendNamespaceCacheControllerSetup(b)

	owns := reflect.ValueOf(b).Elem().FieldByName("ownsInput")
	if owns.Kind() != reflect.Slice {
		t.Fatalf("builder ownsInput not readable (kind %s): controller-runtime internals changed", owns.Kind())
	}
	want := reflect.TypeOf(&netv1.NetworkPolicy{})
	for i := range owns.Len() {
		if obj := owns.Index(i).FieldByName("object"); !obj.IsNil() && obj.Elem().Type() == want {
			return
		}
	}
	t.Fatalf("extendNamespaceCacheControllerSetup() must own NetworkPolicies for import egress self-healing")
}

// The import egress selector is the exact conjunction of the download role and the cache
// identity: it matches this cache's import pods and nothing else. Serving pods carry the
// cache-identity label without the download role, node-mode download pods carry the role
// key with the node value, another cache's import pods carry a different identity, and an
// import pod missing the cache identity is not matched either.
func TestSharedPVCImportEgressSelectorMatchesOnlyThisCachesImportPods(t *testing.T) {
	selector := sharedPVCImportEgressSelector("cache")
	parsed, err := metav1.LabelSelectorAsSelector(&selector)
	if err != nil {
		t.Fatalf("LabelSelectorAsSelector() error = %v", err)
	}
	tests := []struct {
		name   string
		labels map[string]string
		match  bool
	}{
		{
			name: "import pod of this cache",
			labels: map[string]string{
				constants.DownloadJobLabelKey: constants.DownloadJobLabelValueSharedPVC,
				constants.LocalModelLabel:     "cache",
			},
			match: true,
		},
		{
			name:   "serving pod of this cache: identity without the download role",
			labels: map[string]string{constants.LocalModelLabel: "cache"},
		},
		{
			name: "node download pod of this cache: role key with the node value",
			labels: map[string]string{
				constants.DownloadJobLabelKey: constants.DownloadJobLabelValueNode,
				constants.LocalModelLabel:     "cache",
			},
		},
		{
			name: "import pod of another cache",
			labels: map[string]string{
				constants.DownloadJobLabelKey: constants.DownloadJobLabelValueSharedPVC,
				constants.LocalModelLabel:     "other-cache",
			},
		},
		{
			name:   "import pod without the cache identity",
			labels: map[string]string{constants.DownloadJobLabelKey: constants.DownloadJobLabelValueSharedPVC},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parsed.Matches(labels.Set(tt.labels)); got != tt.match {
				t.Fatalf("selector.Matches(%#v) = %v, want %v", tt.labels, got, tt.match)
			}
		})
	}
}

// The grant is in place before the import Job is created: no import pod can be scheduled
// into a namespace whose default-deny egress has not been lifted for it yet.
func TestSharedPVCImportEgressPolicyCreatedBeforeImportJob(t *testing.T) {
	cache := freshSharedCache()
	pvc := boundSharedPVC("pvc-uid")
	base, scheme := sharedPVCFixture(t, cache, pvc)

	var created []string
	cl := interceptor.NewClient(base, interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			switch obj.(type) {
			case *netv1.NetworkPolicy:
				created = append(created, "NetworkPolicy")
			case *batchv1.Job:
				created = append(created, "Job")
			default:
				created = append(created, fmt.Sprintf("%T", obj))
			}
			return c.Create(ctx, obj, opts...)
		},
	})
	reconciler := &LocalModelNamespaceCacheReconciler{Client: cl, Scheme: scheme}

	if _, err := reconciler.reconcileSharedPVC(context.Background(), cache, &corev1.ConfigMap{}, &v1beta1.LocalModelConfig{}, cacheConsumers{}); err != nil {
		t.Fatalf("reconcileSharedPVC() error = %v", err)
	}
	want := []string{"NetworkPolicy", "Job"}
	if !reflect.DeepEqual(created, want) {
		t.Fatalf("objects created in order %v, want %v: the egress grant must precede the import Job", created, want)
	}
}

// sharedCacheFor builds a fresh (no import record, finalizer set) shared-PVC cache for an
// arbitrary name, namespace, and claim, with a stable per-cache UID, for fixtures outside
// the fixed "cache"/"ns"/"pvc" shape of importedSharedCache.
func sharedCacheFor(name, namespace, pvcName string) *v1alpha1.LocalModelNamespaceCache {
	return &v1alpha1.LocalModelNamespaceCache{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Namespace:  namespace,
			UID:        types.UID(name + "-" + namespace + "-uid"),
			Finalizers: []string{NamespaceCacheFinalizerName},
		},
		Spec: v1alpha1.LocalModelNamespaceCacheSpec{
			SourceModelUri: "s3://bucket/model",
			ModelSize:      resource.MustParse("1Gi"),
			PVCRef:         ptrTo(pvcName),
		},
	}
}

// boundPVCFor builds a bound RWX claim for an arbitrary name and namespace.
func boundPVCFor(name, namespace string, uid types.UID) *corev1.PersistentVolumeClaim {
	pvc := pvcWith(fsMode(), []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany}, "2Gi", corev1.ClaimBound, "2Gi")
	pvc.Name = name
	pvc.Namespace = namespace
	pvc.UID = uid
	return pvc
}

// Each cache owns exactly one egress policy in its own namespace: the same cache name in
// two namespaces yields two independent grants, and a second cache in one of those
// namespaces yields a second grant there, each selecting only its own cache's import pods.
func TestSharedPVCImportEgressPolicyPerCacheAndNamespace(t *testing.T) {
	sameNameA := sharedCacheFor("multi", "ns-a", "pvc-a")
	sameNameB := sharedCacheFor("multi", "ns-b", "pvc-b")
	other := sharedCacheFor("other", "ns-a", "pvc-c")
	cl, scheme := sharedPVCFixture(t,
		sameNameA, boundPVCFor("pvc-a", "ns-a", "pvc-a-uid"),
		sameNameB, boundPVCFor("pvc-b", "ns-b", "pvc-b-uid"),
		other, boundPVCFor("pvc-c", "ns-a", "pvc-c-uid"))
	reconciler := &LocalModelNamespaceCacheReconciler{Client: cl, Scheme: scheme}

	caches := []*v1alpha1.LocalModelNamespaceCache{sameNameA, sameNameB, other}
	for _, cache := range caches {
		if _, err := reconciler.reconcileSharedPVC(context.Background(), cache, &corev1.ConfigMap{}, &v1beta1.LocalModelConfig{}, cacheConsumers{}); err != nil {
			t.Fatalf("reconcileSharedPVC(%s/%s) error = %v", cache.Namespace, cache.Name, err)
		}
	}

	for _, cache := range caches {
		policy := &netv1.NetworkPolicy{}
		if err := cl.Get(context.Background(), importEgressPolicyKey(cache), policy); err != nil {
			t.Fatalf("policy for cache %s/%s: %v", cache.Namespace, cache.Name, err)
		}
		if !metav1.IsControlledBy(policy, cache) {
			t.Fatalf("policy %s/%s must be controlled by its own cache, got owner references %#v", policy.Namespace, policy.Name, policy.OwnerReferences)
		}
		if !reflect.DeepEqual(policy.Spec.PodSelector, sharedPVCImportEgressSelector(cache.Name)) {
			t.Fatalf("policy %s/%s selector = %#v, want the cache-identity conjunction", policy.Namespace, policy.Name, policy.Spec.PodSelector)
		}
		job := &batchv1.Job{}
		if err := cl.Get(context.Background(), types.NamespacedName{Name: importJobName(cache.Name), Namespace: cache.Namespace}, job); err != nil {
			t.Fatalf("import Job for cache %s/%s: %v", cache.Namespace, cache.Name, err)
		}
		if job.Spec.Template.Labels[constants.DownloadJobLabelKey] != constants.DownloadJobLabelValueSharedPVC ||
			job.Spec.Template.Labels[constants.LocalModelLabel] != cache.Name {
			t.Fatalf("import Job %s/%s template labels = %#v, want this cache's role and identity", job.Namespace, job.Name, job.Spec.Template.Labels)
		}
	}

	countPolicies := func(namespace string) int {
		policies := &netv1.NetworkPolicyList{}
		if err := cl.List(context.Background(), policies, client.InNamespace(namespace)); err != nil {
			t.Fatalf("list policies in %s: %v", namespace, err)
		}
		return len(policies.Items)
	}
	if got := countPolicies("ns-a"); got != 2 {
		t.Fatalf("ns-a must hold one grant per cache (2), got %d", got)
	}
	if got := countPolicies("ns-b"); got != 1 {
		t.Fatalf("ns-b must hold exactly its own cache's grant (1), got %d", got)
	}
	countJobs := func(namespace string) int {
		jobs := &batchv1.JobList{}
		if err := cl.List(context.Background(), jobs, client.InNamespace(namespace)); err != nil {
			t.Fatalf("list jobs in %s: %v", namespace, err)
		}
		return len(jobs.Items)
	}
	if got := countJobs("ns-a"); got != 2 {
		t.Fatalf("ns-a must hold one import Job per cache (2), got %d", got)
	}
	if got := countJobs("ns-b"); got != 1 {
		t.Fatalf("ns-b must hold exactly its own cache's import Job (1), got %d", got)
	}
}

// A repair write that fails is returned through the import-pending handling and blocks
// the import Job: the grant must exist in its managed shape before any import starts.
func TestSharedPVCImportEgressPolicyUpdateErrorBlocksJobCreation(t *testing.T) {
	cache := freshSharedCache()
	pvc := boundSharedPVC("pvc-uid")
	cl, scheme := sharedPVCFixture(t, cache, pvc)

	// An owned policy with a drifted selector, so the reconcile must repair it via Update.
	drifted := &netv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sharedPVCImportEgressPolicyName(cache.Name),
			Namespace: cache.Namespace,
		},
		Spec: netv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{
				constants.DownloadJobLabelKey: constants.DownloadJobLabelValueSharedPVC,
			}},
			PolicyTypes: []netv1.PolicyType{netv1.PolicyTypeEgress},
			Egress:      []netv1.NetworkPolicyEgressRule{{}},
		},
	}
	if err := controllerutil.SetControllerReference(cache, drifted, scheme); err != nil {
		t.Fatalf("set owner: %v", err)
	}
	if err := cl.Create(context.Background(), drifted); err != nil {
		t.Fatalf("create drifted policy: %v", err)
	}
	updateErr := errors.New("policy update failed")
	wrapped := &sharedPVCPlatformClient{Client: cl, updateErr: updateErr}
	reconciler := &LocalModelNamespaceCacheReconciler{Client: wrapped, Scheme: scheme}

	_, err := reconciler.reconcileSharedPVC(context.Background(), cache, &corev1.ConfigMap{}, &v1beta1.LocalModelConfig{}, cacheConsumers{})
	if !errors.Is(err, updateErr) {
		t.Fatalf("reconcileSharedPVC() error = %v, want the policy update failure %v", err, updateErr)
	}
	if wrapped.writes != 1 {
		t.Fatalf("drift repair must issue exactly one write, got %d", wrapped.writes)
	}
	if condition := cache.Status.GetCondition(v1alpha1.LocalModelCacheReady); condition == nil || condition.Reason != v1alpha1.ReasonImportPending {
		t.Fatalf("cache Ready condition = %#v, want %s", condition, v1alpha1.ReasonImportPending)
	}
	if got := listImportJobs(t, cl); len(got) != 0 {
		t.Fatalf("no import Job may be created while the policy repair fails, got %d", len(got))
	}
	// The failed write converged nothing: the drifted selector is left as it was.
	live := &netv1.NetworkPolicy{}
	if err := cl.Get(context.Background(), importEgressPolicyKey(cache), live); err != nil {
		t.Fatalf("get policy: %v", err)
	}
	if _, ok := live.Spec.PodSelector.MatchLabels[constants.LocalModelLabel]; ok {
		t.Fatalf("the failed repair must leave the drifted selector in place, got %#v", live.Spec.PodSelector)
	}
}

// The grant outlives the import Job's lifecycle: it is kept unchanged across Job
// completion, deletion of the completed Job, and the replacement import, and the
// replacement Job's template carries the labels the grant selects on.
func TestSharedPVCImportEgressPolicyRetainedAcrossJobCompletionAndReplacement(t *testing.T) {
	cache := freshSharedCache()
	pvc := boundSharedPVC("pvc-uid")
	cl, scheme := sharedPVCFixture(t, cache, pvc)
	wrapped := &sharedPVCPlatformClient{Client: cl}
	reconciler := &LocalModelNamespaceCacheReconciler{Client: wrapped, Scheme: scheme}
	reconcile := func() {
		t.Helper()
		if _, err := reconciler.reconcileSharedPVC(context.Background(), cache, &corev1.ConfigMap{}, &v1beta1.LocalModelConfig{}, cacheConsumers{}); err != nil {
			t.Fatalf("reconcileSharedPVC() error = %v", err)
		}
	}

	reconcile()
	jobKey := types.NamespacedName{Name: importJobName(cache.Name), Namespace: cache.Namespace}
	job := &batchv1.Job{}
	if err := cl.Get(context.Background(), jobKey, job); err != nil {
		t.Fatalf("get import job: %v", err)
	}

	// The import completes and the cache records it. Jobs carry a status subresource in
	// the fake client, so the completion is written through the status client the way the
	// Job controller would.
	job.Status.CompletionTime = ptrTo(metav1.Now())
	job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}
	if err := cl.Status().Update(context.Background(), job); err != nil {
		t.Fatalf("complete import job: %v", err)
	}
	reconcile()
	if !cache.IsReady() {
		t.Fatalf("cache should be Ready after the import completes, got %#v", cache.Status.GetCondition(v1alpha1.LocalModelCacheReady))
	}
	if cache.Status.SharedPVCImport == nil {
		t.Fatal("the completed import must be recorded")
	}

	// The completed Job is deleted; with no consumers or pods reading the claim one
	// replacement import is allowed.
	if err := cl.Delete(context.Background(), job); err != nil {
		t.Fatalf("delete completed job: %v", err)
	}
	reconcile()

	replacement := &batchv1.Job{}
	if err := cl.Get(context.Background(), jobKey, replacement); err != nil {
		t.Fatalf("get replacement job: %v", err)
	}
	if replacement.Spec.Template.Labels[constants.DownloadJobLabelKey] != constants.DownloadJobLabelValueSharedPVC ||
		replacement.Spec.Template.Labels[constants.LocalModelLabel] != cache.Name {
		t.Fatalf("replacement Job template labels = %#v, want the labels the grant selects on", replacement.Spec.Template.Labels)
	}
	if got := listImportJobs(t, cl); len(got) != 1 {
		t.Fatalf("expected exactly one import Job after replacement, got %d", len(got))
	}

	// The grant was never rewritten across completion, deletion, and replacement.
	if wrapped.writes != 0 {
		t.Fatalf("the grant must be retained without rewrites, got %d write(s)", wrapped.writes)
	}
	policy := &netv1.NetworkPolicy{}
	if err := cl.Get(context.Background(), importEgressPolicyKey(cache), policy); err != nil {
		t.Fatalf("get policy: %v", err)
	}
	if !metav1.IsControlledBy(policy, cache) {
		t.Fatalf("the retained policy must stay owned by the cache, got owner references %#v", policy.OwnerReferences)
	}
	if !reflect.DeepEqual(policy.Spec.PodSelector, sharedPVCImportEgressSelector(cache.Name)) {
		t.Fatalf("the retained policy selector = %#v, want the cache-identity conjunction", policy.Spec.PodSelector)
	}
	if !reflect.DeepEqual(policy.Spec.Egress, []netv1.NetworkPolicyEgressRule{{}}) {
		t.Fatalf("the retained policy egress = %#v, want the allow-all rule", policy.Spec.Egress)
	}
}
