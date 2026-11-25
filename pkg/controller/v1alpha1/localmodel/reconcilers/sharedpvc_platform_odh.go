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
	"fmt"
	"reflect"

	batchv1 "k8s.io/api/batch/v1"
	netv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
	"github.com/kserve/kserve/pkg/constants"
)

// sharedPVCImportEgressPolicyNameSuffix forms the deterministic name of the cache-owned
// egress policy for shared-PVC import pods: <cache-name>-import-egress.
const sharedPVCImportEgressPolicyNameSuffix = "-import-egress"

// sharedPVCImportEgressPolicyName returns the deterministic import egress policy name for
// a shared-PVC cache. NetworkPolicy object names are not bound by the 63-character
// label-value limit, so the cache name is used literally.
func sharedPVCImportEgressPolicyName(cacheName string) string {
	return cacheName + sharedPVCImportEgressPolicyNameSuffix
}

// sharedPVCImportEgressSelector returns the conjunction selecting exactly the import pods
// of one cache: the ODH download-role label and the cache-identity label. The role label
// distinguishes import pods from serving pods that carry the same cache-name label
// (constants.LocalModelLabel), so neither identity nor role alone is sufficient.
func sharedPVCImportEgressSelector(cacheName string) metav1.LabelSelector {
	return metav1.LabelSelector{MatchLabels: map[string]string{
		constants.DownloadJobLabelKey: constants.DownloadJobLabelValueSharedPVC,
		constants.LocalModelLabel:     cacheName,
	}}
}

// customizeSharedPVCImportJob stamps the ODH download-role and cache-identity labels onto
// the import Job's pod template so the cache-owned import egress policy selects exactly
// this cache's import pods. The labels merge into the template: caller-set labels are
// preserved and the two managed keys win. Only newly built Jobs pass through here; an
// existing Job is never relabeled, so retries from an old unlabeled Job stay unlabeled
// until a normal Job replacement picks up the new template.
func customizeSharedPVCImportJob(job *batchv1.Job, localModel *v1alpha1.LocalModelNamespaceCache) {
	if job.Spec.Template.Labels == nil {
		job.Spec.Template.Labels = map[string]string{}
	}
	job.Spec.Template.Labels[constants.DownloadJobLabelKey] = constants.DownloadJobLabelValueSharedPVC
	job.Spec.Template.Labels[constants.LocalModelLabel] = localModel.Name
}

// extendNamespaceCacheControllerSetup adds the distro-specific owned-resource watch to
// the namespace cache controller: cache-owned NetworkPolicies (the import egress grant)
// are watched, so deleting or drifting one enqueues the owning cache and the next
// reconcile restores it through reconcileSharedPVCPlatformResources. The watch only
// heals; teardown stays with owner-reference garbage collection of the cache.
func (c *LocalModelNamespaceCacheReconciler) extendNamespaceCacheControllerSetup(b *builder.Builder) {
	b.Owns(&netv1.NetworkPolicy{})
}

// reconcileSharedPVCPlatformResources ensures the ODH platform resource a shared-PVC
// import depends on: one egress-only allow-all NetworkPolicy per cache, in the cache
// namespace, owned by the cache. The grant is kept across Job retries, replacement,
// completion, and failure, and is garbage-collected with the cache — the existing
// finalizer does not delete it explicitly. It is ensured before any import Job is
// created, so a real API failure is returned for a retry while no new Job starts without
// the grant. Existing owned policies are reconciled idempotently, which recovers
// deletion and spec drift on the reconcile path.
func (c *LocalModelNamespaceCacheReconciler) reconcileSharedPVCPlatformResources(ctx context.Context, localModel *v1alpha1.LocalModelNamespaceCache) error {
	desired := &netv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sharedPVCImportEgressPolicyName(localModel.Name),
			Namespace: localModel.Namespace,
		},
		Spec: netv1.NetworkPolicySpec{
			PodSelector: sharedPVCImportEgressSelector(localModel.Name),
			PolicyTypes: []netv1.PolicyType{netv1.PolicyTypeEgress},
			// Exactly one empty egress rule: an explicit allow-all grant over every
			// destination, protocol, and port. Download destinations (model storage,
			// registries, identity providers, DNS) are deployment-specific, and in a
			// namespace with default-deny egress an absent policy is not a grant.
			// Egress-only by design: no ingress rules are added, so the import pods keep
			// the namespace's default ingress isolation.
			Egress: []netv1.NetworkPolicyEgressRule{{}},
		},
	}
	if err := controllerutil.SetControllerReference(localModel, desired, c.Scheme, controllerutil.WithBlockOwnerDeletion(false)); err != nil {
		return fmt.Errorf("setting owner reference on import egress NetworkPolicy %s/%s: %w", desired.Namespace, desired.Name, err)
	}

	existing := &netv1.NetworkPolicy{}
	err := c.Get(ctx, client.ObjectKeyFromObject(desired), existing)
	if apierrors.IsNotFound(err) {
		createErr := c.Create(ctx, desired)
		if createErr == nil {
			c.Log.Info("Created shared-PVC import egress NetworkPolicy",
				"name", desired.Name, "namespace", desired.Namespace)
			return nil
		}
		if !apierrors.IsAlreadyExists(createErr) {
			return fmt.Errorf("creating import egress NetworkPolicy %s/%s: %w", desired.Namespace, desired.Name, createErr)
		}
		// Lost a create race: re-read the winning object and validate its ownership below.
		if err := c.Get(ctx, client.ObjectKeyFromObject(desired), existing); err != nil {
			return fmt.Errorf("reading import egress NetworkPolicy %s/%s: %w", desired.Namespace, desired.Name, err)
		}
	} else if err != nil {
		return fmt.Errorf("reading import egress NetworkPolicy %s/%s: %w", desired.Namespace, desired.Name, err)
	}

	// Only a policy controlled by this exact cache object is reconciled. A foreign policy,
	// or one left by a previous incarnation of the cache (same name, different UID, still
	// awaiting garbage collection), is never adopted and its owner reference is never
	// rewritten: the error blocks the import Job until the name is free.
	if !metav1.IsControlledBy(existing, localModel) {
		return fmt.Errorf("import egress NetworkPolicy %s/%s exists and is not controlled by LocalModelNamespaceCache %s/%s",
			existing.Namespace, existing.Name, localModel.Namespace, localModel.Name)
	}

	// Repair drift on the owned policy. The owner reference is normalized through
	// SetControllerReference (the UID already matches; only its remaining fields can
	// differ) and the spec is replaced with a deep copy, so the live and desired objects
	// never share label maps. Everything else on the live object — labels, annotations,
	// unrelated owner references — is retained; only spec and owner reference are
	// compared, and only a difference issues a write. The write is an Update carrying the
	// resourceVersion of the inspected object, so a concurrent owner change or a
	// delete/recreate between the read and the write is rejected by the apiserver as a
	// conflict instead of crossing the ownership check made against that inspected copy.
	before := existing.DeepCopy()
	if err := controllerutil.SetControllerReference(localModel, existing, c.Scheme, controllerutil.WithBlockOwnerDeletion(false)); err != nil {
		return fmt.Errorf("setting owner reference on import egress NetworkPolicy %s/%s: %w", existing.Namespace, existing.Name, err)
	}
	existing.Spec = *desired.Spec.DeepCopy()
	if sharedPVCImportEgressPolicyEqual(before, existing) {
		return nil
	}
	if err := c.Update(ctx, existing); err != nil {
		return fmt.Errorf("updating import egress NetworkPolicy %s/%s: %w", existing.Namespace, existing.Name, err)
	}
	c.Log.Info("Repaired shared-PVC import egress NetworkPolicy",
		"name", existing.Name, "namespace", existing.Namespace)
	return nil
}

// sharedPVCImportEgressPolicyEqual reports whether two versions of the import egress
// policy agree on everything the controller manages: the spec and the owner references.
// Any other metadata on the live object is unrelated and never compared, so it neither
// triggers writes nor is overwritten by them.
func sharedPVCImportEgressPolicyEqual(a, b *netv1.NetworkPolicy) bool {
	return equality.Semantic.DeepEqual(a.Spec, b.Spec) &&
		reflect.DeepEqual(a.OwnerReferences, b.OwnerReferences)
}
