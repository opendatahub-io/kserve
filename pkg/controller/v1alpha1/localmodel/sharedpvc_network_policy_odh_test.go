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

package localmodel

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
	"github.com/kserve/kserve/pkg/constants"
)

// Shared-PVC download NetworkPolicy covers the ODH platform grant for shared-PVC import
// pods: one cache-owned egress-only allow-all NetworkPolicy per LocalModelNamespaceCache,
// in that cache's namespace, ensured before the import Job and repaired through the
// ownership watch. envtest runs no Job or Pod controllers and no garbage collection, so
// Job status is simulated directly and ownership is verified rather than assumed to be
// enforced by the cluster.
var _ = Describe("Shared-PVC download NetworkPolicy", func() {
	const (
		timeout        = time.Second * 10
		duration       = time.Second * 3
		interval       = time.Millisecond * 250
		sourceModelUri = "s3://mybucket/mymodel"
	)

	// makeRWXPVC returns a filesystem RWX PVC with a request larger than the model size.
	makeRWXPVC := func(name, namespace string) *corev1.PersistentVolumeClaim {
		return &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
				VolumeMode:  ptr.To(corev1.PersistentVolumeFilesystem),
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("2Gi")},
				},
			},
		}
	}

	makeSharedCache := func(name, namespace, pvcRef string) *v1alpha1.LocalModelNamespaceCache {
		return &v1alpha1.LocalModelNamespaceCache{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: v1alpha1.LocalModelNamespaceCacheSpec{
				SourceModelUri: sourceModelUri,
				ModelSize:      resource.MustParse("1Gi"),
				PVCRef:         ptr.To(pvcRef),
			},
		}
	}

	// markImportJobComplete simulates the Job controller reporting a successful import.
	// The read-modify-status-update is contested (the cache controller also updates the
	// object's resourceVersion), so conflicts are retried.
	markImportJobComplete := func(ctx context.Context, key types.NamespacedName) {
		Eventually(func() error {
			return retry.RetryOnConflict(retry.DefaultRetry, func() error {
				job := &batchv1.Job{}
				if err := k8sClient.Get(ctx, key, job); err != nil {
					return err
				}
				now := metav1.Now()
				job.Status.StartTime = &now
				job.Status.CompletionTime = &now
				// Recent Kubernetes Job status validation requires the interim
				// SuccessCriteriaMet condition before the terminal one.
				job.Status.Conditions = []batchv1.JobCondition{
					{Type: batchv1.JobSuccessCriteriaMet, Status: corev1.ConditionTrue, LastTransitionTime: now},
					{Type: batchv1.JobComplete, Status: corev1.ConditionTrue, LastTransitionTime: now},
				}
				return k8sClient.Status().Update(ctx, job)
			})
		}, timeout, interval).Should(Succeed())
	}

	importEgressKey := func(cacheName, namespace string) types.NamespacedName {
		return types.NamespacedName{Name: cacheName + "-import-egress", Namespace: namespace}
	}

	importJobKey := func(cacheName, namespace string) types.NamespacedName {
		return types.NamespacedName{Name: cacheName + "-import", Namespace: namespace}
	}

	// grantIsIntact asserts the managed shape of the grant: the exact two-label selector,
	// the egress-only allow-all rule set, and controller ownership by the given cache.
	grantIsIntact := func(ctx context.Context, cache *v1alpha1.LocalModelNamespaceCache) {
		policy := &netv1.NetworkPolicy{}
		ExpectWithOffset(1, k8sClient.Get(ctx, importEgressKey(cache.Name, cache.Namespace), policy)).Should(Succeed())
		ExpectWithOffset(1, policy.Spec.PodSelector.MatchLabels).To(Equal(map[string]string{
			constants.DownloadJobLabelKey: constants.DownloadJobLabelValueSharedPVC,
			constants.LocalModelLabel:     cache.Name,
		}))
		ExpectWithOffset(1, policy.Spec.PolicyTypes).To(Equal([]netv1.PolicyType{netv1.PolicyTypeEgress}))
		ExpectWithOffset(1, policy.Spec.Egress).To(Equal([]netv1.NetworkPolicyEgressRule{{}}))
		ExpectWithOffset(1, policy.Spec.Ingress).To(BeEmpty())
		owner := metav1.GetControllerOf(policy)
		ExpectWithOffset(1, owner).NotTo(BeNil())
		ExpectWithOffset(1, owner.Kind).To(Equal("LocalModelNamespaceCache"))
		ExpectWithOffset(1, owner.UID).To(Equal(cache.UID))
		ExpectWithOffset(1, owner.BlockOwnerDeletion).NotTo(BeNil())
		ExpectWithOffset(1, *owner.BlockOwnerDeletion).To(BeFalse())
	}

	// countJobs returns the import Job count in a namespace; a List error is returned so
	// assertions never mistake a failed read for a zero count.
	countJobs := func(ctx context.Context, namespace string) (int, error) {
		jobs := &batchv1.JobList{}
		if err := k8sClient.List(ctx, jobs, client.InNamespace(namespace)); err != nil {
			return 0, err
		}
		return len(jobs.Items), nil
	}

	// countPolicies returns the NetworkPolicy count in a namespace; a List error is
	// returned so assertions never mistake a failed read for a zero count.
	countPolicies := func(ctx context.Context, namespace string) (int, error) {
		policies := &netv1.NetworkPolicyList{}
		if err := k8sClient.List(ctx, policies, client.InNamespace(namespace)); err != nil {
			return 0, err
		}
		return len(policies.Items), nil
	}

	It("Should grant the cache-owned egress policy before creating the import Job", func() {
		ctx, cancel := context.WithCancel(context.Background())
		DeferCleanup(cancel)

		ns := fmt.Sprintf("test-nwp-grant-%d", time.Now().UnixNano())
		defer k8sClient.Delete(ctx, createTestNamespace(ctx, ns))

		pvc := makeRWXPVC("grant-pvc", ns)
		Expect(k8sClient.Create(ctx, pvc)).Should(Succeed())
		defer k8sClient.Delete(ctx, pvc)

		cache := makeSharedCache("grant-cache", ns, pvc.Name)
		Expect(k8sClient.Create(ctx, cache)).Should(Succeed())
		defer k8sClient.Delete(ctx, cache)

		jobKey := importJobKey("grant-cache", ns)
		Eventually(func() error { return k8sClient.Get(ctx, jobKey, &batchv1.Job{}) }, timeout, interval).Should(Succeed())

		// Whenever the import Job exists the grant is already in place: the policy is
		// ensured before any Job creation, so an import pod is never scheduled into a
		// namespace whose default-deny egress has not been lifted for it yet.
		grantIsIntact(ctx, cache)

		job := &batchv1.Job{}
		Expect(k8sClient.Get(ctx, jobKey, job)).Should(Succeed())
		Expect(job.Spec.Template.Labels).To(HaveKeyWithValue(constants.DownloadJobLabelKey, constants.DownloadJobLabelValueSharedPVC))
		Expect(job.Spec.Template.Labels).To(HaveKeyWithValue(constants.LocalModelLabel, cache.Name))

		Consistently(func() (int, error) { return countPolicies(ctx, ns) }, duration, interval).
			Should(Equal(1), "exactly one grant per cache")
	})

	It("Should keep one grant per cache, including the same cache name in different namespaces", func() {
		ctx, cancel := context.WithCancel(context.Background())
		DeferCleanup(cancel)

		nsA := fmt.Sprintf("test-nwp-multi-a-%d", time.Now().UnixNano())
		defer k8sClient.Delete(ctx, createTestNamespace(ctx, nsA))
		nsB := fmt.Sprintf("test-nwp-multi-b-%d", time.Now().UnixNano())
		defer k8sClient.Delete(ctx, createTestNamespace(ctx, nsB))

		pvcA := makeRWXPVC("multi-pvc", nsA)
		Expect(k8sClient.Create(ctx, pvcA)).Should(Succeed())
		defer k8sClient.Delete(ctx, pvcA)
		pvcB := makeRWXPVC("multi-pvc", nsB)
		Expect(k8sClient.Create(ctx, pvcB)).Should(Succeed())
		defer k8sClient.Delete(ctx, pvcB)

		cacheA := makeSharedCache("multi-cache", nsA, pvcA.Name)
		Expect(k8sClient.Create(ctx, cacheA)).Should(Succeed())
		defer k8sClient.Delete(ctx, cacheA)
		cacheB := makeSharedCache("multi-cache", nsB, pvcB.Name)
		Expect(k8sClient.Create(ctx, cacheB)).Should(Succeed())
		defer k8sClient.Delete(ctx, cacheB)
		Expect(cacheA.UID).NotTo(Equal(cacheB.UID))

		for _, key := range []types.NamespacedName{importJobKey("multi-cache", nsA), importJobKey("multi-cache", nsB)} {
			Eventually(func() error { return k8sClient.Get(ctx, key, &batchv1.Job{}) }, timeout, interval).Should(Succeed())
		}

		// Each incarnation owns exactly its own namespace's grant.
		grantIsIntact(ctx, cacheA)
		grantIsIntact(ctx, cacheB)

		for _, key := range []types.NamespacedName{importJobKey("multi-cache", nsA), importJobKey("multi-cache", nsB)} {
			job := &batchv1.Job{}
			Expect(k8sClient.Get(ctx, key, job)).Should(Succeed())
			Expect(job.Spec.Template.Labels).To(HaveKeyWithValue(constants.DownloadJobLabelKey, constants.DownloadJobLabelValueSharedPVC))
			Expect(job.Spec.Template.Labels).To(HaveKeyWithValue(constants.LocalModelLabel, "multi-cache"))
		}

		grantsA, err := countPolicies(ctx, nsA)
		Expect(err).NotTo(HaveOccurred(), "listing policies in %s", nsA)
		Expect(grantsA).To(Equal(1))
		grantsB, err := countPolicies(ctx, nsB)
		Expect(err).NotTo(HaveOccurred(), "listing policies in %s", nsB)
		Expect(grantsB).To(Equal(1))
	})

	It("Should repair a drifted grant and recreate a deleted one without extra imports", func() {
		ctx, cancel := context.WithCancel(context.Background())
		DeferCleanup(cancel)

		ns := fmt.Sprintf("test-nwp-heal-%d", time.Now().UnixNano())
		defer k8sClient.Delete(ctx, createTestNamespace(ctx, ns))

		pvc := makeRWXPVC("heal-pvc", ns)
		Expect(k8sClient.Create(ctx, pvc)).Should(Succeed())
		defer k8sClient.Delete(ctx, pvc)

		cache := makeSharedCache("heal-cache", ns, pvc.Name)
		Expect(k8sClient.Create(ctx, cache)).Should(Succeed())
		defer k8sClient.Delete(ctx, cache)

		jobKey := importJobKey("heal-cache", ns)
		job := &batchv1.Job{}
		Eventually(func() error { return k8sClient.Get(ctx, jobKey, job) }, timeout, interval).Should(Succeed())
		jobUID := job.UID
		grantIsIntact(ctx, cache)

		// Drift: the selector loses the cache identity and the grant gains an ingress rule.
		policyKey := importEgressKey("heal-cache", ns)
		Expect(retry.RetryOnConflict(retry.DefaultRetry, func() error {
			current := &netv1.NetworkPolicy{}
			if err := k8sClient.Get(ctx, policyKey, current); err != nil {
				return err
			}
			current.Spec.PodSelector = metav1.LabelSelector{MatchLabels: map[string]string{
				constants.DownloadJobLabelKey: constants.DownloadJobLabelValueSharedPVC,
			}}
			current.Spec.PolicyTypes = []netv1.PolicyType{netv1.PolicyTypeIngress, netv1.PolicyTypeEgress}
			current.Spec.Ingress = []netv1.NetworkPolicyIngressRule{{}}
			return k8sClient.Update(ctx, current)
		})).Should(Succeed())

		// The ownership watch enqueues the cache and the next reconcile restores the grant.
		Eventually(func() bool {
			policy := &netv1.NetworkPolicy{}
			if err := k8sClient.Get(ctx, policyKey, policy); err != nil {
				return false
			}
			_, hasIdentity := policy.Spec.PodSelector.MatchLabels[constants.LocalModelLabel]
			return hasIdentity && len(policy.Spec.Ingress) == 0
		}, timeout, interval).Should(BeTrue(), "the drifted grant must be repaired")
		grantIsIntact(ctx, cache)

		// Deleting the grant heals the same way: the watch enqueues the cache and the
		// grant is recreated with the full shape.
		Expect(k8sClient.Delete(ctx, &netv1.NetworkPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: policyKey.Name, Namespace: policyKey.Namespace},
		})).Should(Succeed())
		Eventually(func() error {
			return k8sClient.Get(ctx, policyKey, &netv1.NetworkPolicy{})
		}, timeout, interval).Should(Succeed(), "the deleted grant must be recreated")
		grantIsIntact(ctx, cache)

		// Neither repair re-imports: the import Job is untouched throughout.
		Consistently(func() string {
			current := &batchv1.Job{}
			if err := k8sClient.Get(ctx, jobKey, current); err != nil {
				return ""
			}
			return string(current.UID)
		}, duration, interval).Should(Equal(string(jobUID)), "grant repairs must not re-import")
		Consistently(func() (int, error) { return countJobs(ctx, ns) }, duration, interval).Should(Equal(1))
	})

	It("Should refuse a grant left by a previous cache incarnation until it is removed", func() {
		ctx, cancel := context.WithCancel(context.Background())
		DeferCleanup(cancel)

		ns := fmt.Sprintf("test-nwp-incarnation-%d", time.Now().UnixNano())
		defer k8sClient.Delete(ctx, createTestNamespace(ctx, ns))

		pvc := makeRWXPVC("incarnation-pvc", ns)
		Expect(k8sClient.Create(ctx, pvc)).Should(Succeed())
		defer k8sClient.Delete(ctx, pvc)

		cache := makeSharedCache("incarnation-cache", ns, pvc.Name)
		Expect(k8sClient.Create(ctx, cache)).Should(Succeed())

		jobKey := importJobKey("incarnation-cache", ns)
		Eventually(func() error { return k8sClient.Get(ctx, jobKey, &batchv1.Job{}) }, timeout, interval).Should(Succeed())
		grantIsIntact(ctx, cache)

		// Delete the cache: the finalizer flow removes the import Job, while the grant is
		// left to owner-reference garbage collection (blockOwnerDeletion false), which
		// envtest does not run.
		Expect(k8sClient.Delete(ctx, cache)).Should(Succeed())
		Eventually(func() bool {
			deleting := &batchv1.Job{}
			if err := k8sClient.Get(ctx, jobKey, deleting); err != nil {
				return false
			}
			return deleting.DeletionTimestamp != nil
		}, timeout, interval).Should(BeTrue(), "the finalizer must foreground-delete the import Job")
		Expect(retry.RetryOnConflict(retry.DefaultRetry, func() error {
			current := &batchv1.Job{}
			if err := k8sClient.Get(ctx, jobKey, current); err != nil {
				return err
			}
			current.Finalizers = nil
			return k8sClient.Update(ctx, current)
		})).Should(Succeed())
		cacheKey := types.NamespacedName{Name: cache.Name, Namespace: ns}
		Eventually(func() bool {
			return apierrors.IsNotFound(k8sClient.Get(ctx, cacheKey, &v1alpha1.LocalModelNamespaceCache{}))
		}, timeout, interval).Should(BeTrue(), "the cache must finalize once its import Job is gone")

		// A new incarnation (same name, new UID) must not adopt the leftover grant: the
		// import stays blocked behind it and the old owner reference is never rewritten.
		reincarnated := makeSharedCache(cache.Name, ns, pvc.Name)
		Expect(k8sClient.Create(ctx, reincarnated)).Should(Succeed())
		defer k8sClient.Delete(ctx, reincarnated)

		Eventually(func() string {
			current := &v1alpha1.LocalModelNamespaceCache{}
			if err := k8sClient.Get(ctx, cacheKey, current); err != nil {
				return ""
			}
			condition := current.Status.GetCondition(v1alpha1.LocalModelCacheReady)
			if condition == nil {
				return ""
			}
			return condition.Reason
		}, timeout, interval).Should(Equal(v1alpha1.ReasonImportPending), "the blocked import must surface as import pending")
		Consistently(func() bool {
			return apierrors.IsNotFound(k8sClient.Get(ctx, jobKey, &batchv1.Job{}))
		}, duration, interval).Should(BeTrue(), "no import Job behind a grant the cache does not control")

		policyKey := importEgressKey(cache.Name, ns)
		leftover := &netv1.NetworkPolicy{}
		Expect(k8sClient.Get(ctx, policyKey, leftover)).Should(Succeed())
		leftoverOwner := metav1.GetControllerOf(leftover)
		Expect(leftoverOwner).NotTo(BeNil())
		Expect(leftoverOwner.UID).To(Equal(cache.UID), "the leftover grant must never be adopted")

		// Once the leftover is removed the new incarnation takes the name and imports.
		Expect(k8sClient.Delete(ctx, leftover)).Should(Succeed())
		Expect(retry.RetryOnConflict(retry.DefaultRetry, func() error {
			nudged := &v1alpha1.LocalModelNamespaceCache{}
			if err := k8sClient.Get(ctx, cacheKey, nudged); err != nil {
				return err
			}
			if nudged.Annotations == nil {
				nudged.Annotations = map[string]string{}
			}
			nudged.Annotations["test.kserve.io/nudge"] = "grant-free"
			return k8sClient.Update(ctx, nudged)
		})).Should(Succeed())
		Eventually(func() error { return k8sClient.Get(ctx, jobKey, &batchv1.Job{}) }, timeout, interval).
			Should(Succeed(), "the new incarnation must import once the name is free")
		grantIsIntact(ctx, reincarnated)
	})

	It("Should retain the grant across import Job completion, deletion, and replacement", func() {
		ctx, cancel := context.WithCancel(context.Background())
		DeferCleanup(cancel)

		ns := fmt.Sprintf("test-nwp-retain-%d", time.Now().UnixNano())
		defer k8sClient.Delete(ctx, createTestNamespace(ctx, ns))

		pvc := makeRWXPVC("retain-pvc", ns)
		Expect(k8sClient.Create(ctx, pvc)).Should(Succeed())
		defer k8sClient.Delete(ctx, pvc)

		cache := makeSharedCache("retain-cache", ns, pvc.Name)
		Expect(k8sClient.Create(ctx, cache)).Should(Succeed())
		defer k8sClient.Delete(ctx, cache)

		jobKey := importJobKey("retain-cache", ns)
		job := &batchv1.Job{}
		Eventually(func() error { return k8sClient.Get(ctx, jobKey, job) }, timeout, interval).Should(Succeed())
		jobUID := job.UID

		policyKey := importEgressKey("retain-cache", ns)
		policy := &netv1.NetworkPolicy{}
		Expect(k8sClient.Get(ctx, policyKey, policy)).Should(Succeed())
		policyUID := policy.UID

		// The import completes and the cache records it against the claim.
		markImportJobComplete(ctx, jobKey)
		cacheKey := types.NamespacedName{Name: cache.Name, Namespace: ns}
		Eventually(func() bool {
			current := &v1alpha1.LocalModelNamespaceCache{}
			if err := k8sClient.Get(ctx, cacheKey, current); err != nil {
				return false
			}
			return current.IsReady() && current.Status.SharedPVCImport != nil
		}, timeout, interval).Should(BeTrue(), "the completed import must be recorded")

		// The grant outlives the completed Job, unchanged.
		Expect(k8sClient.Get(ctx, policyKey, policy)).Should(Succeed())
		Expect(policy.UID).To(Equal(policyUID), "the grant must not be recreated on completion")

		// Deleting the completed Job (no consumers, no pods) allows exactly one
		// replacement import, and the same grant object covers it.
		current := &batchv1.Job{}
		Expect(k8sClient.Get(ctx, jobKey, current)).Should(Succeed())
		Expect(k8sClient.Delete(ctx, current, client.PropagationPolicy(metav1.DeletePropagationBackground))).Should(Succeed())
		Eventually(func() bool {
			replacement := &batchv1.Job{}
			if err := k8sClient.Get(ctx, jobKey, replacement); err != nil {
				return false
			}
			return replacement.UID != jobUID
		}, timeout, interval).Should(BeTrue(), "one replacement import after the completed Job is deleted")

		replacement := &batchv1.Job{}
		Expect(k8sClient.Get(ctx, jobKey, replacement)).Should(Succeed())
		Expect(replacement.Spec.Template.Labels).To(HaveKeyWithValue(constants.DownloadJobLabelKey, constants.DownloadJobLabelValueSharedPVC))
		Expect(replacement.Spec.Template.Labels).To(HaveKeyWithValue(constants.LocalModelLabel, cache.Name))

		Expect(k8sClient.Get(ctx, policyKey, policy)).Should(Succeed())
		Expect(policy.UID).To(Equal(policyUID), "the grant must be retained across the replacement")
		grantIsIntact(ctx, cache)

		Consistently(func() (int, error) { return countJobs(ctx, ns) }, duration, interval).
			Should(Equal(1), "never concurrent import Jobs")
	})
})
