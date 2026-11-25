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

package llmisvc

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	disaggregatedsetv1 "sigs.k8s.io/lws/api/disaggregatedset/v1"
	lwsapi "sigs.k8s.io/lws/api/leaderworkerset/v1"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
	"github.com/kserve/kserve/pkg/utils"
)

// The fixed component keys of the NetworkPolicy selector inventory. Policy
// names embed them (<service>-networkpolicy-<mode>-<component-key>), so they
// must stay stable across releases.
const (
	networkPolicyComponentMain      = "main"
	networkPolicyComponentPrefill   = "prefill"
	networkPolicyComponentScheduler = "scheduler"
	networkPolicyComponentTokenizer = "tokenizer"
	networkPolicyComponentMainMN    = "main-mn"
	networkPolicyComponentPrefillMN = "prefill-mn"
	networkPolicyComponentPD        = "pd"
)

// The policy-local classification of a component's Pods, deciding which
// traffic rules apply to the component's policies. These values live only in
// policy code; they are not Pod labels.
const (
	networkPolicyRoleWorkload  = "workload"
	networkPolicyRoleScheduler = "scheduler"
	networkPolicyRoleTokenizer = "tokenizer"
)

// networkPolicySelectorEntry is one stored, owned component of an
// LLMInferenceService: the fixed key naming it, the policy-local class deciding
// which traffic rules apply to its Pods, and the complete Pod selector its Pods
// carry.
type networkPolicySelectorEntry struct {
	// Component is the fixed component key (networkPolicyComponent*).
	Component string
	// Class is the policy-local classification of the component's Pods
	// (networkPolicyRole*): workload, scheduler, or tokenizer.
	Class string
	// Selector is the component's complete Pod selector: deep cloned from the
	// stored Deployment — matchLabels and matchExpressions, including
	// historical keys the current templates no longer set — or, for the
	// set-based workloads, the controller-supplied membership label of the set.
	Selector metav1.LabelSelector
}

// networkPolicySelectorInventory lists the stored, owned components of one
// LLMInferenceService in the fixed order main, prefill, scheduler, tokenizer,
// main-mn, prefill-mn, pd.
type networkPolicySelectorInventory []networkPolicySelectorEntry

// entry returns the inventory entry of one component key.
func (inv networkPolicySelectorInventory) entry(component string) (networkPolicySelectorEntry, bool) {
	for i := range inv {
		if inv[i].Component == component {
			return inv[i], true
		}
	}
	return networkPolicySelectorEntry{}, false
}

// ofClass returns the entries whose Pods belong to one policy-local class, in
// inventory order.
func (inv networkPolicySelectorInventory) ofClass(class string) networkPolicySelectorInventory {
	matched := make(networkPolicySelectorInventory, 0, len(inv))
	for i := range inv {
		if inv[i].Class == class {
			matched = append(matched, inv[i])
		}
	}
	return matched
}

// networkPolicySelectors reads the fixed set of component resources the
// controller owns for llmSvc and returns their complete Pod selectors.
//
// A component that is not stored produces no entry: the optional workload APIs
// (LeaderWorkerSet, DisaggregatedSet) are equally absent when their CRDs are
// not installed. Every stored, valid component is included regardless of the
// service's current desired topology or replica count — including components at
// zero replicas and components left over from a topology change — so coverage
// is never dropped while the change converges.
//
// Any other outcome is an error, so the caller retries without broadening
// selectors or pruning existing coverage: a read failure, a resource at the
// expected name that is not controlled by llmSvc, or a stored Deployment
// selector that is empty or syntactically invalid (an empty selector would
// select every Pod in the namespace). Pods are never listed; the selectors come
// from the stored component resources alone.
func (r *LLMISVCReconciler) networkPolicySelectors(ctx context.Context, llmSvc *v1alpha2.LLMInferenceService) (networkPolicySelectorInventory, error) {
	inventory := make(networkPolicySelectorInventory, 0, 7)
	for _, d := range []struct{ component, class, name string }{
		{networkPolicyComponentMain, networkPolicyRoleWorkload, mainDeploymentName(llmSvc)},
		{networkPolicyComponentPrefill, networkPolicyRoleWorkload, prefillDeploymentName(llmSvc)},
		{networkPolicyComponentScheduler, networkPolicyRoleScheduler, schedulerDeploymentName(llmSvc)},
		{networkPolicyComponentTokenizer, networkPolicyRoleTokenizer, tokenizerDeploymentName(llmSvc)},
	} {
		entry, err := r.networkPolicyDeploymentEntry(ctx, llmSvc, d.component, d.class, d.name)
		if err != nil {
			return nil, err
		}
		if entry != nil {
			inventory = append(inventory, *entry)
		}
	}
	for _, s := range []struct{ component, name string }{
		{networkPolicyComponentMainMN, mainLWSName(llmSvc)},
		{networkPolicyComponentPrefillMN, prefillLWSName(llmSvc)},
	} {
		entry, err := r.networkPolicyLeaderWorkerSetEntry(ctx, llmSvc, s.component, s.name)
		if err != nil {
			return nil, err
		}
		if entry != nil {
			inventory = append(inventory, *entry)
		}
	}
	entry, err := r.networkPolicyDisaggregatedSetEntry(ctx, llmSvc)
	if err != nil {
		return nil, err
	}
	if entry != nil {
		inventory = append(inventory, *entry)
	}
	return inventory, nil
}

// networkPolicyDeploymentEntry reads the Deployment name and returns its
// inventory entry, or no entry when the Deployment is not stored. A read
// failure, foreign ownership, or an empty or invalid stored selector is an
// error: dropping the component would open a coverage gap, and reconstructing
// its selector could select Pods the stored Deployment does not.
func (r *LLMISVCReconciler) networkPolicyDeploymentEntry(ctx context.Context, llmSvc *v1alpha2.LLMInferenceService, component, class, name string) (*networkPolicySelectorEntry, error) {
	key := types.NamespacedName{Namespace: llmSvc.GetNamespace(), Name: name}
	deployment := &appsv1.Deployment{}
	if err := r.Get(ctx, key, deployment); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get Deployment %s for the NetworkPolicy selector inventory: %w", key, err)
	}
	if err := ownedByLLMInferenceService(deployment, llmSvc, "Deployment", key); err != nil {
		return nil, err
	}
	selector, err := storedDeploymentPodSelector(deployment.Spec.Selector, key)
	if err != nil {
		return nil, err
	}
	return &networkPolicySelectorEntry{Component: component, Class: class, Selector: selector}, nil
}

// networkPolicyLeaderWorkerSetEntry reads the LeaderWorkerSet name and returns
// its inventory entry, or no entry when the set is not stored or its CRD is not
// installed (LeaderWorkerSet is an optional API). The entry selects every Pod of
// the set — leaders and workers — through the membership label the LWS
// controller stamps on its Pods, so it does not depend on template labels or
// transient revision names. Foreign ownership is an error, like the Deployment
// components.
func (r *LLMISVCReconciler) networkPolicyLeaderWorkerSetEntry(ctx context.Context, llmSvc *v1alpha2.LLMInferenceService, component, name string) (*networkPolicySelectorEntry, error) {
	key := types.NamespacedName{Namespace: llmSvc.GetNamespace(), Name: name}
	set := &lwsapi.LeaderWorkerSet{}
	if err := r.Get(ctx, key, set); err != nil {
		if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get LeaderWorkerSet %s for the NetworkPolicy selector inventory: %w", key, err)
	}
	if err := ownedByLLMInferenceService(set, llmSvc, "LeaderWorkerSet", key); err != nil {
		return nil, err
	}
	return &networkPolicySelectorEntry{
		Component: component,
		Class:     networkPolicyRoleWorkload,
		Selector: metav1.LabelSelector{MatchLabels: map[string]string{
			lwsapi.SetNameLabelKey: set.GetName(),
		}},
	}, nil
}

// networkPolicyDisaggregatedSetEntry reads the owned DisaggregatedSet and
// returns its inventory entry, or no entry when the set is not stored or its
// CRD is not installed (DisaggregatedSet is an optional API). The entry selects
// every Pod of the set — all roles, slices, and revisions — through the
// membership label the DisaggregatedSet controller stamps on the Pod templates
// of the LeaderWorkerSets it generates. Role-specific rules add
// disaggregatedsetv1.RoleLabelKey to this selector where a rule needs it;
// generated revision names are never selected. Foreign ownership is an error,
// like the Deployment components.
func (r *LLMISVCReconciler) networkPolicyDisaggregatedSetEntry(ctx context.Context, llmSvc *v1alpha2.LLMInferenceService) (*networkPolicySelectorEntry, error) {
	set, err := r.currentDisaggregatedSet(ctx, llmSvc)
	if err != nil {
		return nil, err
	}
	if set == nil {
		return nil, nil
	}
	if err := ownedByLLMInferenceService(set, llmSvc, "DisaggregatedSet",
		types.NamespacedName{Namespace: set.GetNamespace(), Name: set.GetName()}); err != nil {
		return nil, err
	}
	return &networkPolicySelectorEntry{
		Component: networkPolicyComponentPD,
		Class:     networkPolicyRoleWorkload,
		Selector: metav1.LabelSelector{MatchLabels: map[string]string{
			disaggregatedsetv1.SetNameLabelKey: set.GetName(),
		}},
	}, nil
}

// managedSchedulerNetworkPolicySelector resolves the stored scheduler
// Deployment selector of a service's managed scheduler, for use as a
// NetworkPolicy source peer or target. It serves the service's own scheduler
// and managed schedulers of sibling services discovered through shared pools
// alike: pass the service whose scheduler is needed, with its base
// configurations already merged.
//
// It returns nil when the service does not run a managed scheduler, so callers
// can apply it to every candidate sibling. When the service does run one, the
// scheduler Deployment must already be stored, controlled by the service, and
// carry a nonempty valid selector — the same validation path as the inventory.
// An unresolved managed scheduler is an error so the caller retries, rather
// than emitting an empty peer that covers nothing or widening the source beyond
// the scheduler's Pods. This is deliberately stricter than networkPolicySelectors,
// where a component that is not stored yet simply has no entry.
func (r *LLMISVCReconciler) managedSchedulerNetworkPolicySelector(ctx context.Context, llmSvc *v1alpha2.LLMInferenceService) (*metav1.LabelSelector, error) {
	if !hasManagedScheduler(llmSvc) {
		return nil, nil
	}
	// A force-stopped service deliberately deletes its managed scheduler
	// Deployment, so its absence is the stopped state, not an unresolved
	// scheduler: no peer is returned (there are no scheduler Pods to admit),
	// which callers must skip rather than retry in a loop or widen the source.
	if utils.GetForceStopRuntime(llmSvc) {
		return nil, nil
	}
	name := schedulerDeploymentName(llmSvc)
	entry, err := r.networkPolicyDeploymentEntry(ctx, llmSvc, networkPolicyComponentScheduler, networkPolicyRoleScheduler, name)
	if err != nil {
		return nil, err
	}
	if entry == nil {
		return nil, fmt.Errorf("managed scheduler Deployment %s/%s is not stored yet; the NetworkPolicy scheduler selector cannot be resolved",
			llmSvc.GetNamespace(), name)
	}
	return &entry.Selector, nil
}

// ownedByLLMInferenceService verifies that the stored component at key is
// controller-owned by llmSvc. A resource the service does not control must be
// neither selected nor pruned by its policies, so it aborts the inventory
// instead of being skipped.
func ownedByLLMInferenceService(obj metav1.Object, llmSvc *v1alpha2.LLMInferenceService, kind string, key types.NamespacedName) error {
	if metav1.IsControlledBy(obj, llmSvc) {
		return nil
	}
	return fmt.Errorf("%w: %s %s is not controlled by LLMInferenceService %s/%s",
		errNetworkPolicyOwnershipConflict, kind, key, llmSvc.GetNamespace(), llmSvc.GetName())
}

// storedDeploymentPodSelector deep-clones the stored Pod selector of an owned
// Deployment and validates it. An empty selector would select every Pod in the
// namespace rather than the component's Pods, and a syntactically invalid
// selector cannot be expressed as a NetworkPolicy podSelector; both abort the
// inventory instead of narrowing or widening coverage.
func storedDeploymentPodSelector(selector *metav1.LabelSelector, key types.NamespacedName) (metav1.LabelSelector, error) {
	if selector == nil || (len(selector.MatchLabels) == 0 && len(selector.MatchExpressions) == 0) {
		return metav1.LabelSelector{}, fmt.Errorf("Deployment %s has an empty Pod selector, which would select every Pod in namespace %s", key, key.Namespace)
	}
	clone := selector.DeepCopy()
	if _, err := metav1.LabelSelectorAsSelector(clone); err != nil {
		return metav1.LabelSelector{}, fmt.Errorf("Deployment %s has an invalid Pod selector: %w", key, err)
	}
	return *clone, nil
}
