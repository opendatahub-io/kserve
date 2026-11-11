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
	"errors"
	"fmt"

	netv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"knative.dev/pkg/kmeta"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
)

// allowAllEgressNetworkPolicyName returns the deterministic per-component
// egress policy name <service>-networkpolicy-egress-<component-key>. The name
// carries no ingress mode — the allow-all egress policy set is
// mode-independent, so a mode toggle never churns egress policy names — and
// is deliberately distinct from the retired legacy general egress name
// (<service>-networkpolicy-egress), which is never reused for a component
// selector.
func allowAllEgressNetworkPolicyName(llmSvc *v1alpha2.LLMInferenceService, component string) string {
	return kmeta.ChildName(llmSvc.GetName(), "-networkpolicy-egress-"+component)
}

// allowAllEgressNetworkPolicyNames returns the bounded set of per-component
// allow-all egress policy names: exactly one name per fixed component key,
// independent of the service's current topology. Steps that must remove every
// owned allow-all egress policy delete exactly this set instead of listing or
// guessing policies, so cleanup never sweeps unrelated service-owned
// resources.
func allowAllEgressNetworkPolicyNames(llmSvc *v1alpha2.LLMInferenceService) []string {
	names := make([]string, 0, len(networkPolicyComponentKeys))
	for _, component := range networkPolicyComponentKeys {
		names = append(names, allowAllEgressNetworkPolicyName(llmSvc, component))
	}
	return names
}

// staleAllowAllEgressNetworkPolicyNames returns the bounded set of
// per-component allow-all egress policy names a reconcile no longer desires:
// every fixed component key's egress name except the names of the components
// still stored in the inventory. This covers components that left the
// selector inventory after a topology change while the retained entries keep
// their coverage; with an empty inventory it is the complete bounded set.
func staleAllowAllEgressNetworkPolicyNames(llmSvc *v1alpha2.LLMInferenceService, inventory networkPolicySelectorInventory) []string {
	desired := make(map[string]struct{}, len(inventory))
	for _, entry := range inventory {
		desired[allowAllEgressNetworkPolicyName(llmSvc, entry.Component)] = struct{}{}
	}
	names := make([]string, 0, len(networkPolicyComponentKeys))
	for _, component := range networkPolicyComponentKeys {
		name := allowAllEgressNetworkPolicyName(llmSvc, component)
		if _, keep := desired[name]; !keep {
			names = append(names, name)
		}
	}
	return names
}

// networkPolicyEgressObject builds an egress-only policy that selects the
// Pods of one inventory entry through the entry's complete stored selector,
// reusing the metadata and ownership contract of the ingress policies.
func networkPolicyEgressObject(llmSvc *v1alpha2.LLMInferenceService, name string, selector metav1.LabelSelector) *netv1.NetworkPolicy {
	return &netv1.NetworkPolicy{
		ObjectMeta: networkPolicyObjectMeta(llmSvc, name, networkPolicyComponentLabel),
		Spec: netv1.NetworkPolicySpec{
			PodSelector: *selector.DeepCopy(),
			PolicyTypes: []netv1.PolicyType{netv1.PolicyTypeEgress},
			// Exactly one empty egress rule: an explicit allow-all grant over
			// every destination, protocol, and port. A finite protocol and
			// port list would have to track every engine's egress needs and
			// would silently deny new ones, and an absent policy is not an
			// explicit grant in a namespace with default-deny egress.
			Egress: []netv1.NetworkPolicyEgressRule{{}},
		},
	}
}

// expectedAllowAllEgressNetworkPolicies returns the mode-independent egress
// policy set: one egress-only allow-all policy per inventory entry, selecting
// the entry's Pods through its complete stored selector. No ingress rules are
// created; ingress allowance stays governed by the ingress-mode policies
// alone.
func expectedAllowAllEgressNetworkPolicies(llmSvc *v1alpha2.LLMInferenceService, inventory networkPolicySelectorInventory) []*netv1.NetworkPolicy {
	policies := make([]*netv1.NetworkPolicy, 0, len(inventory))
	for _, entry := range inventory {
		policies = append(policies, networkPolicyEgressObject(llmSvc,
			allowAllEgressNetworkPolicyName(llmSvc, entry.Component), entry.Selector))
	}
	return policies
}

// reconcileEgressNetworkPolicies runs the egress lifecycle in exactly one of
// two states per pass, chosen by the shared tracing opt-in predicate: the
// unrestricted state establishes the per-component allow-all egress set, and
// the tracing opt-in state establishes the tracing policy. Both ingress modes
// run the same egress state, so a mode toggle never churns egress policy
// names, and an ingress failure never skips egress reconciliation. Each state
// is replacement-before-pruning: the chosen egress coverage is established
// first, and only its success removes the coverage it replaces, so an
// establishment failure retains the other state's policies and reports the
// error for a retry instead of falling back to a broad or deny-all grant.
func (r *LLMISVCReconciler) reconcileEgressNetworkPolicies(ctx context.Context, llmSvc *v1alpha2.LLMInferenceService) error {
	if tracingEgressOptedIn(llmSvc) {
		return r.reconcileTracingOptInEgressNetworkPolicies(ctx, llmSvc)
	}
	return r.reconcileUnrestrictedEgressNetworkPolicies(ctx, llmSvc)
}

// reconcileUnrestrictedEgressNetworkPolicies establishes the unrestricted
// egress state: one explicit allow-all egress policy per stored component.
// The entire desired set is established before anything it replaces is
// removed, so an inventory, read, or write failure retains the prior tracing
// policy and the prior per-component egress coverage for the retry. With no
// stored component the desired set is empty and the obsolete owned policies
// are pruned normally. Once the complete set exists, the tracing lifecycle's
// opt-out branch removes the tracing policy, the stale per-component egress
// names of components that left the selector inventory are pruned, and the
// retired legacy general egress name is deleted because its replacement is
// ready. Every removal failure is reported for the retry — the completed
// allow-all set temporarily overrides an opted-out tracing policy until its
// cleanup succeeds. The tracing cleanup and the stale-name prune are
// independent of each other, each reporting its own error, while the bounded
// delete list is deliberately fail-fast within itself: the first failed
// delete stops the remaining names of that one list.
func (r *LLMISVCReconciler) reconcileUnrestrictedEgressNetworkPolicies(ctx context.Context, llmSvc *v1alpha2.LLMInferenceService) error {
	inventory, err := r.networkPolicySelectors(ctx, llmSvc)
	if err != nil {
		return fmt.Errorf("failed to build the NetworkPolicy selector inventory: %w", err)
	}
	for _, policy := range expectedAllowAllEgressNetworkPolicies(llmSvc, inventory) {
		if err := reconcileOwnedNetworkPolicy(ctx, r, llmSvc, policy); err != nil {
			return err
		}
	}

	var errs error
	if err := r.reconcileTracingNetworkPolicy(ctx, llmSvc); err != nil {
		errs = errors.Join(errs, err)
	}
	removeNames := append(staleAllowAllEgressNetworkPolicyNames(llmSvc, inventory), egressNetworkPolicyName(llmSvc))
	if err := deleteOwnedNetworkPolicies(ctx, r, llmSvc, removeNames); err != nil {
		errs = errors.Join(errs, err)
	}
	return errs
}

// reconcileTracingOptInEgressNetworkPolicies establishes the tracing opt-in
// egress state: the existing tracing policy is reconciled first — unchanged
// in name, application-label selector, rules, OTLP resolution, watches, and
// status annotations — and only after it succeeds is every owned general
// allow-all egress grant removed: all seven fixed per-component names,
// bounded by the component keys rather than today's topology, plus the
// retired legacy general egress name. A lookup, write, or ownership error
// must not generate an allow-all or deny-all fallback or start that removal:
// the prior allow-all grants temporarily survive next to the tracing
// restrictions until a retry converges. Multi-policy transitions are not
// atomic.
func (r *LLMISVCReconciler) reconcileTracingOptInEgressNetworkPolicies(ctx context.Context, llmSvc *v1alpha2.LLMInferenceService) error {
	if err := r.reconcileTracingNetworkPolicy(ctx, llmSvc); err != nil {
		return err
	}
	return deleteOwnedNetworkPolicies(ctx, r, llmSvc,
		append(allowAllEgressNetworkPolicyNames(llmSvc), egressNetworkPolicyName(llmSvc)))
}
