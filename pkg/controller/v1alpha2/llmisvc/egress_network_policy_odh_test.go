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
	"reflect"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"knative.dev/pkg/kmeta"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	disaggregatedsetv1 "sigs.k8s.io/lws/api/disaggregatedset/v1"
	lwsapi "sigs.k8s.io/lws/api/leaderworkerset/v1"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
	"github.com/kserve/kserve/pkg/constants"
)

// This file holds the internal builder-contract fixtures and egress
// lifecycle tests of the LLMISVC egress NetworkPolicy lifecycle plan
// (plans/llmisvc-egress-networkpolicies.md). The inventory fixture covers
// every fixed component key with a distinct, complete stored selector, and
// the assertion helpers pin the contracts the ingress builders and the
// per-component allow-all egress builder share: exactly one policy per stored
// inventory entry, deterministic per-component names, the
// networkPolicyObjectMeta metadata and ownership contract, and a deep copy of
// each entry's complete stored Pod selector. The lifecycle tests pin the
// two-state egress orchestrator's replacement-before-pruning order and its
// integration with the ingress reconciliation.

// allComponentNetworkPolicySelectorInventory is the reusable inventory fixture
// covering every fixed component key of networkPolicyComponentKeys with a
// distinct, complete stored selector: matchLabels plus matchExpressions on the
// main entry and the set-membership labels of the multi-node and pd entries.
// Builder tests iterate it to prove full per-component coverage without
// guessing or collapsing selectors.
func allComponentNetworkPolicySelectorInventory(service *v1alpha2.LLMInferenceService) networkPolicySelectorInventory {
	name := service.GetName()
	return networkPolicySelectorInventory{
		{
			Component: networkPolicyComponentMain,
			Class:     networkPolicyRoleWorkload,
			Selector: metav1.LabelSelector{
				MatchLabels: map[string]string{
					constants.KubernetesAppNameLabelKey:   name,
					constants.KubernetesComponentLabelKey: constants.KServeComponentWorkload,
				},
				MatchExpressions: []metav1.LabelSelectorRequirement{
					{Key: "tier", Operator: metav1.LabelSelectorOpIn, Values: []string{"gold", "silver"}},
				},
			},
		},
		{
			Component: networkPolicyComponentPrefill,
			Class:     networkPolicyRoleWorkload,
			Selector: metav1.LabelSelector{MatchLabels: map[string]string{
				constants.KubernetesAppNameLabelKey: name,
				constants.LLMDRoleLabelKey:          constants.LLMDRolePrefill,
			}},
		},
		{
			Component: networkPolicyComponentScheduler,
			Class:     networkPolicyRoleScheduler,
			Selector: metav1.LabelSelector{MatchLabels: map[string]string{
				constants.KubernetesAppNameLabelKey:   name,
				constants.KubernetesComponentLabelKey: constants.LLMComponentRouterScheduler,
			}},
		},
		{
			Component: networkPolicyComponentTokenizer,
			Class:     networkPolicyRoleTokenizer,
			Selector: metav1.LabelSelector{MatchLabels: map[string]string{
				constants.KubernetesAppNameLabelKey:   name,
				constants.KubernetesComponentLabelKey: constants.LLMComponentTokenizer,
			}},
		},
		{
			Component: networkPolicyComponentMainMN,
			Class:     networkPolicyRoleWorkload,
			Selector: metav1.LabelSelector{MatchLabels: map[string]string{
				lwsapi.SetNameLabelKey: name + "-kserve-mn",
			}},
		},
		{
			Component: networkPolicyComponentPrefillMN,
			Class:     networkPolicyRoleWorkload,
			Selector: metav1.LabelSelector{MatchLabels: map[string]string{
				lwsapi.SetNameLabelKey: name + "-prefill-kserve-mn",
			}},
		},
		{
			Component: networkPolicyComponentPD,
			Class:     networkPolicyRoleWorkload,
			Selector: metav1.LabelSelector{MatchLabels: map[string]string{
				disaggregatedsetv1.SetNameLabelKey: name + "-kserve-pd",
			}},
		},
	}
}

// assertOwnedNetworkPolicyMetadata verifies the metadata contract of policies
// built through networkPolicyObjectMeta: the service namespace, the
// part-of/app-name/component labels, and exactly one controller owner
// reference to the service.
func assertOwnedNetworkPolicyMetadata(t *testing.T, service *v1alpha2.LLMInferenceService, policy *netv1.NetworkPolicy) {
	t.Helper()
	if policy.Namespace != service.GetNamespace() {
		t.Fatalf("policy %s namespace got %q, want %q", policy.Name, policy.Namespace, service.GetNamespace())
	}
	if !metav1.IsControlledBy(policy, service) {
		t.Fatalf("policy %s is not controlled by LLMInferenceService %s/%s", policy.Name, service.GetNamespace(), service.GetName())
	}
	// IsControlledBy alone would accept extra owner references; the metadata
	// contract promises exactly one, and that it is the LLMInferenceService
	// controller reference.
	if len(policy.OwnerReferences) != 1 {
		t.Fatalf("policy %s carries %d owner references, want exactly one", policy.Name, len(policy.OwnerReferences))
	}
	owner := policy.OwnerReferences[0]
	if owner.Kind != v1alpha2.LLMInferenceServiceGVK.Kind ||
		owner.APIVersion != v1alpha2.LLMInferenceServiceGVK.GroupVersion().String() ||
		owner.Name != service.GetName() || owner.UID != service.GetUID() ||
		owner.Controller == nil || !*owner.Controller {
		t.Fatalf("policy %s owner reference %#v is not the LLMInferenceService %s/%s controller reference",
			policy.Name, owner, service.GetNamespace(), service.GetName())
	}
	wantLabels := map[string]string{
		constants.KubernetesPartOfLabelKey:    constants.LLMInferenceServicePartOfValue,
		constants.KubernetesAppNameLabelKey:   service.GetName(),
		constants.KubernetesComponentLabelKey: networkPolicyComponentLabel,
	}
	if !reflect.DeepEqual(policy.Labels, wantLabels) {
		t.Fatalf("policy %s labels got %v, want %v", policy.Name, policy.Labels, wantLabels)
	}
}

// assertNetworkPolicySelectsStoredSelector verifies that a built policy
// selects exactly the complete stored selector of its inventory entry —
// matchLabels and matchExpressions — and that the builder deep-copied it, so
// probing the built policy never reaches the stored selector the inventory
// holds.
func assertNetworkPolicySelectsStoredSelector(t *testing.T, policy *netv1.NetworkPolicy, stored metav1.LabelSelector) {
	t.Helper()
	if !reflect.DeepEqual(policy.Spec.PodSelector, stored) {
		t.Fatalf("policy %s selector got %#v, want the complete stored selector %#v", policy.Name, policy.Spec.PodSelector, stored)
	}
	if len(policy.Spec.PodSelector.MatchLabels) == 0 && len(policy.Spec.PodSelector.MatchExpressions) == 0 {
		t.Fatalf("policy %s has an empty Pod selector, which would select every Pod in namespace %s: %#v",
			policy.Name, policy.Namespace, policy.Spec.PodSelector)
	}
	if policy.Spec.PodSelector.MatchLabels != nil {
		policy.Spec.PodSelector.MatchLabels["deep-copy-probe"] = "probe"
		if _, ok := stored.MatchLabels["deep-copy-probe"]; ok {
			t.Fatalf("policy %s aliases the stored selector's match labels", policy.Name)
		}
		delete(policy.Spec.PodSelector.MatchLabels, "deep-copy-probe")
	}
	if len(policy.Spec.PodSelector.MatchExpressions) > 0 && len(policy.Spec.PodSelector.MatchExpressions[0].Values) > 0 {
		original := policy.Spec.PodSelector.MatchExpressions[0].Values[0]
		policy.Spec.PodSelector.MatchExpressions[0].Values[0] = "deep-copy-probe"
		if stored.MatchExpressions[0].Values[0] != original {
			t.Fatalf("policy %s aliases the stored selector's match expressions", policy.Name)
		}
		policy.Spec.PodSelector.MatchExpressions[0].Values[0] = original
	}
}

// TestAllComponentNetworkPolicySelectorInventoryCoversFixedComponentKeys
// keeps the reusable inventory fixture honest: one distinct, complete selector
// per fixed component key, so builder tests that iterate it really exercise
// every key the bounded policy names and cleanup are built from.
func TestAllComponentNetworkPolicySelectorInventoryCoversFixedComponentKeys(t *testing.T) {
	service := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{Name: "model", Namespace: "team-a"}}
	inventory := allComponentNetworkPolicySelectorInventory(service)
	if len(inventory) != len(networkPolicyComponentKeys) {
		t.Fatalf("fixture has %d entries, want one per fixed component key (%d)", len(inventory), len(networkPolicyComponentKeys))
	}
	seen := make(map[string]int, len(inventory))
	for _, entry := range inventory {
		seen[entry.Component]++
		if seen[entry.Component] > 1 {
			t.Fatalf("duplicate component key %q in the fixture", entry.Component)
		}
		if len(entry.Selector.MatchLabels) == 0 && len(entry.Selector.MatchExpressions) == 0 {
			t.Fatalf("component %q has an empty selector", entry.Component)
		}
	}
	for _, key := range networkPolicyComponentKeys {
		if seen[key] != 1 {
			t.Fatalf("fixed component key %q is missing from the fixture", key)
		}
	}
	for i := range inventory {
		for j := i + 1; j < len(inventory); j++ {
			if reflect.DeepEqual(inventory[i].Selector, inventory[j].Selector) {
				t.Fatalf("components %q and %q share a selector", inventory[i].Component, inventory[j].Component)
			}
		}
	}
}

// TestDefaultIngressNetworkPolicyBuilderContractAcrossComponentInventory pins
// the default-mode builder contract across every fixed component key: exactly
// one ingress-only policy per inventory entry, named deterministically per
// component, owned by the service, selecting the entry's complete stored
// selector, with an explicit allow-all ingress rule and no egress rules.
func TestDefaultIngressNetworkPolicyBuilderContractAcrossComponentInventory(t *testing.T) {
	service := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{
		Name: "model", Namespace: "team-a", UID: "model-uid",
	}}
	inventory := allComponentNetworkPolicySelectorInventory(service)

	policies := expectedDefaultIngressNetworkPolicies(service, inventory)
	if len(policies) != len(inventory) {
		t.Fatalf("got %d policies, want one per inventory entry (%d)", len(policies), len(inventory))
	}
	seen := make(map[string]struct{}, len(policies))
	for i, entry := range inventory {
		policy := policies[i]
		if want := ingressNetworkPolicyName(service, networkPolicyModeDefault, entry.Component); policy.Name != want {
			t.Fatalf("policy %d name got %q, want %q", i, policy.Name, want)
		}
		if _, duplicate := seen[policy.Name]; duplicate {
			t.Fatalf("duplicate policy name %q", policy.Name)
		}
		seen[policy.Name] = struct{}{}
		assertOwnedNetworkPolicyMetadata(t, service, policy)
		assertNetworkPolicySelectsStoredSelector(t, policy, entry.Selector)
		if !reflect.DeepEqual(policy.Spec.PolicyTypes, []netv1.PolicyType{netv1.PolicyTypeIngress}) {
			t.Fatalf("policy %s types got %v, want ingress-only", policy.Name, policy.Spec.PolicyTypes)
		}
		if len(policy.Spec.Ingress) != 1 || len(policy.Spec.Ingress[0].From) != 0 || len(policy.Spec.Ingress[0].Ports) != 0 {
			t.Fatalf("policy %s ingress is not explicit allow-all: %#v", policy.Name, policy.Spec.Ingress)
		}
		if policy.Spec.Egress != nil {
			t.Fatalf("policy %s must not create egress rules: %#v", policy.Name, policy.Spec.Egress)
		}
	}
}

// TestAllowAllEgressNetworkPolicyBuilderContractAcrossComponentInventory pins
// the per-component allow-all egress builder contract across every fixed
// component key: exactly one egress-only policy per inventory entry, named
// deterministically per component, owned by the service, selecting the entry's
// complete stored selector — matchLabels and matchExpressions, deep-copied —
// with an explicit allow-all egress rule and no ingress rules.
func TestAllowAllEgressNetworkPolicyBuilderContractAcrossComponentInventory(t *testing.T) {
	service := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{
		Name: "model", Namespace: "team-a", UID: "model-uid",
	}}
	inventory := allComponentNetworkPolicySelectorInventory(service)

	policies := expectedAllowAllEgressNetworkPolicies(service, inventory)
	if len(policies) != len(inventory) {
		t.Fatalf("got %d policies, want one per inventory entry (%d)", len(policies), len(inventory))
	}
	seen := make(map[string]struct{}, len(policies))
	for i, entry := range inventory {
		policy := policies[i]
		if want := allowAllEgressNetworkPolicyName(service, entry.Component); policy.Name != want {
			t.Fatalf("policy %d name got %q, want %q", i, policy.Name, want)
		}
		if _, duplicate := seen[policy.Name]; duplicate {
			t.Fatalf("duplicate policy name %q", policy.Name)
		}
		seen[policy.Name] = struct{}{}
		assertOwnedNetworkPolicyMetadata(t, service, policy)
		assertNetworkPolicySelectsStoredSelector(t, policy, entry.Selector)
		if !reflect.DeepEqual(policy.Spec.PolicyTypes, []netv1.PolicyType{netv1.PolicyTypeEgress}) {
			t.Fatalf("policy %s types got %v, want egress-only", policy.Name, policy.Spec.PolicyTypes)
		}
		if len(policy.Spec.Egress) != 1 || len(policy.Spec.Egress[0].To) != 0 || len(policy.Spec.Egress[0].Ports) != 0 {
			t.Fatalf("policy %s egress is not explicit allow-all: %#v", policy.Name, policy.Spec.Egress)
		}
		if policy.Spec.Ingress != nil {
			t.Fatalf("policy %s must not create ingress rules: %#v", policy.Name, policy.Spec.Ingress)
		}
	}
}

// TestAllowAllEgressNetworkPolicyNamesAreBoundedAndDeterministic pins the
// bounded per-component egress name list: exactly one kmeta.ChildName name per
// fixed component key — deterministic, also for service names long enough to
// push ChildName into its hash branch — and never the retired legacy general
// egress name or the tracing policy name.
func TestAllowAllEgressNetworkPolicyNamesAreBoundedAndDeterministic(t *testing.T) {
	service := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{Name: "model", Namespace: "team-a"}}

	names := allowAllEgressNetworkPolicyNames(service)
	if !reflect.DeepEqual(names, allowAllEgressNetworkPolicyNames(service)) {
		t.Fatalf("the bounded egress name list is not deterministic: %v", names)
	}
	seen := make(map[string]struct{}, len(networkPolicyComponentKeys))
	for _, component := range networkPolicyComponentKeys {
		want := kmeta.ChildName(service.GetName(), "-networkpolicy-egress-"+component)
		found := false
		for _, name := range names {
			if name == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("bounded egress name list %v is missing the %q policy name %q", names, component, want)
		}
		if _, duplicate := seen[want]; duplicate {
			t.Fatalf("duplicate per-component egress policy name %q", want)
		}
		seen[want] = struct{}{}
	}
	if len(names) != len(networkPolicyComponentKeys) {
		t.Fatalf("got %d egress policy names, want exactly one per fixed component key (%d)", len(names), len(networkPolicyComponentKeys))
	}
	for _, foreign := range []string{
		egressNetworkPolicyName(service),
		tracingNetworkPolicyName(service),
	} {
		if _, owned := seen[foreign]; owned {
			t.Fatalf("the per-component egress name set must not contain the retired or tracing policy name %q", foreign)
		}
	}

	// A 63-character service name forces kmeta.ChildName into its hash branch
	// for these suffixes; the names must stay unique, deterministic, and valid
	// object names there too.
	longService := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{
		Name: strings.Repeat("m", 63), Namespace: "team-a",
	}}
	longNames := allowAllEgressNetworkPolicyNames(longService)
	if !reflect.DeepEqual(longNames, allowAllEgressNetworkPolicyNames(longService)) {
		t.Fatalf("the hashed egress name list is not deterministic: %v", longNames)
	}
	longSeen := make(map[string]struct{}, len(longNames))
	for _, name := range longNames {
		if len(name) > 63 {
			t.Fatalf("hashed egress policy name %q exceeds the object name limit", name)
		}
		if _, duplicate := longSeen[name]; duplicate {
			t.Fatalf("hashed egress policy names collide on %q", name)
		}
		longSeen[name] = struct{}{}
	}
	if len(longSeen) != len(networkPolicyComponentKeys) {
		t.Fatalf("got %d hashed egress policy names, want one per fixed component key (%d)", len(longSeen), len(networkPolicyComponentKeys))
	}
}

// TestStaleAllowAllEgressNetworkPolicyNames pins the bounded prune helper of
// the per-component allow-all egress policies: the complete inventory desires
// every name, a topology change makes exactly the departed components' names
// stale, and no stored component makes the whole bounded set stale. The stale
// set contains per-component egress names only, so pruning can never sweep
// legacy, tracing, or ingress policies.
func TestStaleAllowAllEgressNetworkPolicyNames(t *testing.T) {
	service := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{Name: "model", Namespace: "team-a"}}
	full := allComponentNetworkPolicySelectorInventory(service)

	if stale := staleAllowAllEgressNetworkPolicyNames(service, full); len(stale) != 0 {
		t.Fatalf("the complete inventory must desire every egress policy; got stale names %v", stale)
	}

	retained := []string{networkPolicyComponentMain, networkPolicyComponentScheduler}
	retainedInventory := make(networkPolicySelectorInventory, 0, len(retained))
	for _, component := range retained {
		entry, ok := full.entry(component)
		if !ok {
			t.Fatalf("the fixture must carry component %q", component)
		}
		retainedInventory = append(retainedInventory, entry)
	}
	wantStale := make(map[string]struct{}, len(networkPolicyComponentKeys))
	for _, component := range networkPolicyComponentKeys {
		retainedComponent := false
		for _, keep := range retained {
			if component == keep {
				retainedComponent = true
				break
			}
		}
		if !retainedComponent {
			wantStale[allowAllEgressNetworkPolicyName(service, component)] = struct{}{}
		}
	}
	gotStale := make(map[string]struct{}, len(networkPolicyComponentKeys))
	for _, name := range staleAllowAllEgressNetworkPolicyNames(service, retainedInventory) {
		gotStale[name] = struct{}{}
	}
	if !reflect.DeepEqual(gotStale, wantStale) {
		t.Fatalf("stale egress names after the topology change got %v, want %v", gotStale, wantStale)
	}

	if stale := staleAllowAllEgressNetworkPolicyNames(service, networkPolicySelectorInventory{}); len(stale) != len(networkPolicyComponentKeys) {
		t.Fatalf("an empty inventory must make every bounded egress name stale; got %v", stale)
	}
}

// TestStrictIngressNetworkPolicyBuilderContractAcrossComponentInventory pins
// the strict-mode builder contract across every fixed component key: exactly
// one ingress-only policy per inventory entry — in the workload, scheduler,
// and tokenizer class order the builder emits — named deterministically per
// component, owned by the service, selecting the entry's complete stored
// selector, admitting no source-less ingress rule, and creating no egress
// rules.
func TestStrictIngressNetworkPolicyBuilderContractAcrossComponentInventory(t *testing.T) {
	service := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{
		Name: "model", Namespace: "team-a", UID: "model-uid",
	}}
	inventory := allComponentNetworkPolicySelectorInventory(service)
	schedulerEntry, ok := inventory.entry(networkPolicyComponentScheduler)
	if !ok {
		t.Fatal("fixture must carry a scheduler entry")
	}
	schedulerPeers := []networkPolicySchedulerPeer{{
		peer:  netv1.NetworkPolicyPeer{PodSelector: schedulerEntry.Selector.DeepCopy()},
		ports: []int32{defaultWorkloadPort, 8007},
	}}
	gatewayNamespaces := []string{"team-a", "shared-gateway"}

	policies := expectedStrictIngressNetworkPolicies(service, inventory,
		gatewayNamespaces, []int32{defaultWorkloadPort, 8007}, schedulerPeers, nil)
	if len(policies) != len(inventory) {
		t.Fatalf("got %d policies, want one per inventory entry (%d)", len(policies), len(inventory))
	}
	entryByName := make(map[string]networkPolicySelectorEntry, len(inventory))
	for _, entry := range inventory {
		entryByName[ingressNetworkPolicyName(service, networkPolicyModeStrict, entry.Component)] = entry
	}
	seen := make(map[string]struct{}, len(policies))
	for _, policy := range policies {
		entry, ok := entryByName[policy.Name]
		if !ok {
			t.Fatalf("unexpected policy name %q", policy.Name)
		}
		if _, duplicate := seen[policy.Name]; duplicate {
			t.Fatalf("duplicate policy name %q", policy.Name)
		}
		seen[policy.Name] = struct{}{}
		assertOwnedNetworkPolicyMetadata(t, service, policy)
		assertNetworkPolicySelectsStoredSelector(t, policy, entry.Selector)
		if !reflect.DeepEqual(policy.Spec.PolicyTypes, []netv1.PolicyType{netv1.PolicyTypeIngress}) {
			t.Fatalf("policy %s types got %v, want ingress-only", policy.Name, policy.Spec.PolicyTypes)
		}
		for _, rule := range policy.Spec.Ingress {
			if len(rule.From) == 0 {
				t.Fatalf("policy %s has an ingress rule that admits every source: %#v", policy.Name, rule)
			}
		}
		if policy.Spec.Egress != nil {
			t.Fatalf("policy %s must not create egress rules: %#v", policy.Name, policy.Spec.Egress)
		}
	}
}

// TestReconcileEgressNetworkPoliciesUnrestrictedEstablishesAllowAllBeforeRemoval
// covers the unrestricted state's replacement-before-pruning order: while the
// per-component allow-all set cannot be established, every policy it would
// replace — the opted-out tracing policy, the stale per-component egress name
// of a component that left the topology, and the retired legacy general
// egress policy — is retained; once a retry establishes the complete set, all
// of them are removed.
func TestReconcileEgressNetworkPoliciesUnrestrictedEstablishesAllowAllBeforeRemoval(t *testing.T) {
	service := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{
		Name: "model", Namespace: "team-a", UID: "model-uid",
	}}
	mainSelector := metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "model"}}
	egressMainName := allowAllEgressNetworkPolicyName(service, networkPolicyComponentMain)
	staleEgressSchedulerName := allowAllEgressNetworkPolicyName(service, networkPolicyComponentScheduler)
	// Prior coverage of both egress states: the tracing policy of an earlier
	// opted-in reconcile, a stale per-component egress policy of a component
	// that left the topology, and the retired legacy general egress policy.
	seeded := []client.Object{
		ownedDeployment(service, mainDeploymentName(service), mainSelector.DeepCopy(), mainSelector.MatchLabels),
		expectedTracingNetworkPolicy(service, netv1.NetworkPolicyPeer{}, 0, false),
		&netv1.NetworkPolicy{
			ObjectMeta: metav1.ObjectMeta{
				Name:            staleEgressSchedulerName,
				Namespace:       service.Namespace,
				OwnerReferences: ownedByController(service),
			},
			Spec: netv1.NetworkPolicySpec{PodSelector: mainSelector},
		},
		legacyGeneralEgressNetworkPolicy(service),
	}
	createErr := errors.New("allow-all egress NetworkPolicy Create failed")
	failCreate := true
	cl := newNetworkPolicyTestClientWithInterceptor(t, interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if policy, ok := obj.(*netv1.NetworkPolicy); ok && failCreate && policy.Name == egressMainName {
				return createErr
			}
			return c.Create(ctx, obj, opts...)
		},
	}, seeded...)
	r := &LLMISVCReconciler{Client: cl, EventRecorder: record.NewFakeRecorder(16)}

	if err := r.reconcileEgressNetworkPolicies(t.Context(), service); !errors.Is(err, createErr) {
		t.Fatalf("reconcile error = %v, want the allow-all creation failure %v", err, createErr)
	}
	if err := cl.Get(t.Context(), types.NamespacedName{Name: egressMainName, Namespace: service.Namespace}, &netv1.NetworkPolicy{}); !apierrors.IsNotFound(err) {
		t.Fatalf("allow-all egress policy must not exist while its creation fails: %v", err)
	}
	for _, name := range []string{
		tracingNetworkPolicyName(service),
		staleEgressSchedulerName,
		egressNetworkPolicyName(service),
	} {
		if err := cl.Get(t.Context(), types.NamespacedName{Name: name, Namespace: service.Namespace}, &netv1.NetworkPolicy{}); err != nil {
			t.Fatalf("prior egress coverage %s must be retained until the allow-all set exists: %v", name, err)
		}
	}

	// The retry establishes the complete allow-all set and removes every
	// policy it replaces.
	failCreate = false
	if err := r.reconcileEgressNetworkPolicies(t.Context(), service); err != nil {
		t.Fatalf("retry failed after the creation recovered: %v", err)
	}
	egressMain := &netv1.NetworkPolicy{}
	if err := cl.Get(t.Context(), types.NamespacedName{Name: egressMainName, Namespace: service.Namespace}, egressMain); err != nil {
		t.Fatalf("allow-all egress policy was not created on the retry: %v", err)
	}
	if !reflect.DeepEqual(egressMain.Spec.PodSelector, mainSelector) {
		t.Fatalf("allow-all egress selector got %#v, want the stored Deployment selector", egressMain.Spec.PodSelector)
	}
	for _, name := range []string{
		tracingNetworkPolicyName(service),
		staleEgressSchedulerName,
		egressNetworkPolicyName(service),
	} {
		if err := cl.Get(t.Context(), types.NamespacedName{Name: name, Namespace: service.Namespace}, &netv1.NetworkPolicy{}); !apierrors.IsNotFound(err) {
			t.Fatalf("replaced policy %s remained after the unrestricted state converged: %v", name, err)
		}
	}
}

// TestReconcileEgressNetworkPoliciesUnrestrictedReportsEveryRemovalFailure
// covers the unrestricted state's post-establishment error aggregation: once
// the allow-all set exists, a tracing-policy cleanup failure is reported
// without blocking the stale-name prune — one pass surfaces every problem —
// and the retry converges.
func TestReconcileEgressNetworkPoliciesUnrestrictedReportsEveryRemovalFailure(t *testing.T) {
	service := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{
		Name: "model", Namespace: "team-a", UID: "model-uid",
	}}
	mainSelector := metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "model"}}
	staleEgressSchedulerName := allowAllEgressNetworkPolicyName(service, networkPolicyComponentScheduler)
	deleteErr := errors.New("tracing NetworkPolicy Delete failed")
	failDelete := true
	cl := newNetworkPolicyTestClientWithInterceptor(t, interceptor.Funcs{
		Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			if policy, ok := obj.(*netv1.NetworkPolicy); ok && failDelete && policy.Name == tracingNetworkPolicyName(service) {
				return deleteErr
			}
			return c.Delete(ctx, obj, opts...)
		},
	},
		ownedDeployment(service, mainDeploymentName(service), mainSelector.DeepCopy(), mainSelector.MatchLabels),
		expectedTracingNetworkPolicy(service, netv1.NetworkPolicyPeer{}, 0, false),
		&netv1.NetworkPolicy{
			ObjectMeta: metav1.ObjectMeta{
				Name:            staleEgressSchedulerName,
				Namespace:       service.Namespace,
				OwnerReferences: ownedByController(service),
			},
			Spec: netv1.NetworkPolicySpec{PodSelector: mainSelector},
		})
	r := &LLMISVCReconciler{Client: cl, EventRecorder: record.NewFakeRecorder(16)}

	err := r.reconcileEgressNetworkPolicies(t.Context(), service)
	if !errors.Is(err, deleteErr) {
		t.Fatalf("reconcile error = %v, want the tracing cleanup failure %v", err, deleteErr)
	}
	// The allow-all grant exists and the stale prune still ran: the tracing
	// cleanup failure must not block the other removals, and the failed
	// cleanup leaves the tracing policy for the retry.
	if err := cl.Get(t.Context(), types.NamespacedName{
		Name:      allowAllEgressNetworkPolicyName(service, networkPolicyComponentMain),
		Namespace: service.Namespace,
	}, &netv1.NetworkPolicy{}); err != nil {
		t.Fatalf("allow-all egress policy must exist despite the tracing cleanup failure: %v", err)
	}
	if err := cl.Get(t.Context(), types.NamespacedName{Name: staleEgressSchedulerName, Namespace: service.Namespace}, &netv1.NetworkPolicy{}); !apierrors.IsNotFound(err) {
		t.Fatalf("stale egress policy must be pruned despite the tracing cleanup failure: %v", err)
	}
	if err := cl.Get(t.Context(), types.NamespacedName{Name: tracingNetworkPolicyName(service), Namespace: service.Namespace}, &netv1.NetworkPolicy{}); err != nil {
		t.Fatalf("tracing policy must survive its failed cleanup for the retry: %v", err)
	}

	failDelete = false
	if err := r.reconcileEgressNetworkPolicies(t.Context(), service); err != nil {
		t.Fatalf("retry failed after the tracing cleanup recovered: %v", err)
	}
	if err := cl.Get(t.Context(), types.NamespacedName{Name: tracingNetworkPolicyName(service), Namespace: service.Namespace}, &netv1.NetworkPolicy{}); !apierrors.IsNotFound(err) {
		t.Fatalf("tracing policy remained after the successful retry: %v", err)
	}
}

// TestReconcileEgressNetworkPoliciesTracingOptInEstablishesTracingBeforeRemoval
// covers the tracing opt-in state's replacement-before-pruning order: while
// the tracing policy cannot be established, every owned general allow-all
// egress grant is retained — including per-component names outside today's
// topology, because the removal is bounded by the fixed component keys — and
// no allow-all or deny-all fallback is generated. Once a retry establishes
// the tracing policy, every bounded per-component name and the retired legacy
// general egress name are removed.
func TestReconcileEgressNetworkPoliciesTracingOptInEstablishesTracingBeforeRemoval(t *testing.T) {
	service := &v1alpha2.LLMInferenceService{
		ObjectMeta: metav1.ObjectMeta{
			Name: "model", Namespace: "team-a", UID: "model-uid",
			Annotations: map[string]string{constants.EnableTracingEgressNetworkPolicyAnnotationKey: "true"},
		},
		Spec: v1alpha2.LLMInferenceServiceSpec{Tracing: &v1alpha2.TracingSpec{}},
	}
	mainSelector := metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "model"}}
	// General allow-all grants of an earlier unrestricted reconcile: two
	// components of the current topology plus one name of a component that is
	// not stored, which the bounded removal must cover all the same.
	allowAllPolicy := func(component string) *netv1.NetworkPolicy {
		return &netv1.NetworkPolicy{
			ObjectMeta: metav1.ObjectMeta{
				Name:            allowAllEgressNetworkPolicyName(service, component),
				Namespace:       service.Namespace,
				OwnerReferences: ownedByController(service),
			},
			Spec: netv1.NetworkPolicySpec{PodSelector: mainSelector},
		}
	}
	seededComponents := []string{networkPolicyComponentMain, networkPolicyComponentScheduler, networkPolicyComponentTokenizer}
	seeded := make([]client.Object, 0, 2+len(seededComponents))
	seeded = append(seeded,
		ownedDeployment(service, mainDeploymentName(service), mainSelector.DeepCopy(), mainSelector.MatchLabels),
		legacyGeneralEgressNetworkPolicy(service),
	)
	for _, component := range seededComponents {
		seeded = append(seeded, allowAllPolicy(component))
	}
	createErr := errors.New("tracing NetworkPolicy Create failed")
	failCreate := true
	cl := newNetworkPolicyTestClientWithInterceptor(t, interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if policy, ok := obj.(*netv1.NetworkPolicy); ok && failCreate && policy.Name == tracingNetworkPolicyName(service) {
				return createErr
			}
			return c.Create(ctx, obj, opts...)
		},
	}, seeded...)
	r := &LLMISVCReconciler{Client: cl, EventRecorder: record.NewFakeRecorder(16)}

	if err := r.reconcileEgressNetworkPolicies(t.Context(), service); !errors.Is(err, createErr) {
		t.Fatalf("reconcile error = %v, want the tracing creation failure %v", err, createErr)
	}
	for _, component := range seededComponents {
		if err := cl.Get(t.Context(), types.NamespacedName{
			Name:      allowAllEgressNetworkPolicyName(service, component),
			Namespace: service.Namespace,
		}, &netv1.NetworkPolicy{}); err != nil {
			t.Fatalf("allow-all egress policy for %s must be retained until the tracing policy exists: %v", component, err)
		}
	}
	if err := cl.Get(t.Context(), types.NamespacedName{Name: egressNetworkPolicyName(service), Namespace: service.Namespace}, &netv1.NetworkPolicy{}); err != nil {
		t.Fatalf("legacy general egress policy must be retained until the tracing policy exists: %v", err)
	}

	failCreate = false
	if err := r.reconcileEgressNetworkPolicies(t.Context(), service); err != nil {
		t.Fatalf("retry failed after the tracing creation recovered: %v", err)
	}
	if err := cl.Get(t.Context(), types.NamespacedName{Name: tracingNetworkPolicyName(service), Namespace: service.Namespace}, &netv1.NetworkPolicy{}); err != nil {
		t.Fatalf("tracing policy was not created on the retry: %v", err)
	}
	for _, name := range append(allowAllEgressNetworkPolicyNames(service), egressNetworkPolicyName(service)) {
		if err := cl.Get(t.Context(), types.NamespacedName{Name: name, Namespace: service.Namespace}, &netv1.NetworkPolicy{}); !apierrors.IsNotFound(err) {
			t.Fatalf("general allow-all egress policy %s remained after the tracing opt-in converged: %v", name, err)
		}
	}
}

// TestReconcileLLMISVCNetworkPoliciesEgressIndependentOfIngressFailures
// covers the independence contract of the integrated lifecycles: a strict-mode
// Gateway peer resolution failure must not skip the egress state, so the
// allow-all egress grant exists alongside the reported ingress error while no
// strict ingress policy was created.
func TestReconcileLLMISVCNetworkPoliciesEgressIndependentOfIngressFailures(t *testing.T) {
	service := &v1alpha2.LLMInferenceService{
		ObjectMeta: metav1.ObjectMeta{Name: "model", Namespace: "team-a", UID: "model-uid"},
		Spec: v1alpha2.LLMInferenceServiceSpec{Router: &v1alpha2.RouterSpec{
			Scheduler: &v1alpha2.SchedulerSpec{Template: &corev1.PodSpec{}},
		}},
	}
	mainSelector := metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "model"}}
	// The managed scheduler is configured but its Deployment is not stored
	// yet, so the strict Gateway peer resolution fails and retries.
	cl := newNetworkPolicyTestClient(t,
		ownedDeployment(service, mainDeploymentName(service), mainSelector.DeepCopy(), mainSelector.MatchLabels))
	r := &LLMISVCReconciler{Client: cl, EventRecorder: record.NewFakeRecorder(16)}

	err := reconcileLLMISVCNetworkPolicies(t.Context(), r, service, &Config{FeatureGates: FeatureGates{StrictNetworkPolicy: true}})
	if err == nil || !strings.Contains(err.Error(), "is not stored yet") {
		t.Fatalf("reconcile error = %v, want the unresolved managed scheduler failure", err)
	}
	egressMain := &netv1.NetworkPolicy{}
	if err := cl.Get(t.Context(), types.NamespacedName{
		Name:      allowAllEgressNetworkPolicyName(service, networkPolicyComponentMain),
		Namespace: service.Namespace,
	}, egressMain); err != nil {
		t.Fatalf("allow-all egress policy must exist despite the ingress failure: %v", err)
	}
	if !reflect.DeepEqual(egressMain.Spec.PodSelector, mainSelector) {
		t.Fatalf("allow-all egress selector got %#v, want the stored Deployment selector", egressMain.Spec.PodSelector)
	}
	if err := cl.Get(t.Context(), types.NamespacedName{
		Name:      ingressNetworkPolicyName(service, networkPolicyModeStrict, networkPolicyComponentMain),
		Namespace: service.Namespace,
	}, &netv1.NetworkPolicy{}); !apierrors.IsNotFound(err) {
		t.Fatalf("no strict ingress policy may exist while its peer resolution fails: %v", err)
	}
}

// TestReconcileLLMISVCNetworkPoliciesRunsTracingLifecycleOncePerPass pins
// that integrating the egress lifecycle left exactly one tracing lifecycle
// call site: an opted-in pass with an already-established tracing policy reads
// that policy exactly once — the reconcile lookup — instead of running
// reconcileTracingNetworkPolicy from both the egress state and a leftover
// call site.
func TestReconcileLLMISVCNetworkPoliciesRunsTracingLifecycleOncePerPass(t *testing.T) {
	service := &v1alpha2.LLMInferenceService{
		ObjectMeta: metav1.ObjectMeta{
			Name: "model", Namespace: "team-a", UID: "model-uid",
			Annotations: map[string]string{constants.EnableTracingEgressNetworkPolicyAnnotationKey: "true"},
		},
		Spec: v1alpha2.LLMInferenceServiceSpec{Tracing: &v1alpha2.TracingSpec{}},
	}
	mainSelector := metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "model"}}
	tracingReads := 0
	cl := newNetworkPolicyTestClientWithInterceptor(t, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*netv1.NetworkPolicy); ok && key.Name == tracingNetworkPolicyName(service) {
				tracingReads++
			}
			return c.Get(ctx, key, obj, opts...)
		},
	},
		ownedDeployment(service, mainDeploymentName(service), mainSelector.DeepCopy(), mainSelector.MatchLabels),
		expectedTracingNetworkPolicy(service, netv1.NetworkPolicyPeer{}, 0, false))
	r := &LLMISVCReconciler{Client: cl, EventRecorder: record.NewFakeRecorder(16)}

	if err := reconcileLLMISVCNetworkPolicies(t.Context(), r, service, &Config{}); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	if tracingReads != 1 {
		t.Fatalf("one pass read the tracing policy %d times, want exactly one tracing lifecycle run", tracingReads)
	}
}

// TestReconcileLLMISVCNetworkPoliciesDefersCombinedLegacyUntilEgressPruningSucceeds
// pins the migration-order contract of the branch-created combined policy:
// its removal requires a successful ingress AND egress reconciliation, so a
// failed prune of a stale per-component egress name defers the combined and
// monitoring legacy removals while the ingress policies and the stored
// components' allow-all grants stay in place. The retry completes the
// migration.
func TestReconcileLLMISVCNetworkPoliciesDefersCombinedLegacyUntilEgressPruningSucceeds(t *testing.T) {
	service := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{
		Name: "model", Namespace: "team-a", UID: "model-uid",
	}}
	mainSelector := metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "model"}}
	// A stale egress policy of a component that left the topology, plus the
	// superseded combined and monitoring legacy coverage.
	staleEgressScheduler := &netv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:            allowAllEgressNetworkPolicyName(service, networkPolicyComponentScheduler),
			Namespace:       service.Namespace,
			OwnerReferences: ownedByController(service),
		},
		Spec: netv1.NetworkPolicySpec{PodSelector: mainSelector},
	}
	legacyMonitoring := &netv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{
		Name:            legacyMonitoringNetworkPolicyName(service),
		Namespace:       service.Namespace,
		OwnerReferences: ownedByController(service),
	}}
	deleteErr := errors.New("stale egress NetworkPolicy Delete failed")
	failDelete := true
	cl := newNetworkPolicyTestClientWithInterceptor(t, interceptor.Funcs{
		Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			if policy, ok := obj.(*netv1.NetworkPolicy); ok && failDelete && policy.Name == staleEgressScheduler.Name {
				return deleteErr
			}
			return c.Delete(ctx, obj, opts...)
		},
	},
		ownedDeployment(service, mainDeploymentName(service), mainSelector.DeepCopy(), mainSelector.MatchLabels),
		staleEgressScheduler,
		legacyAllowAllNetworkPolicy(service),
		legacyMonitoring)
	r := &LLMISVCReconciler{Client: cl, EventRecorder: record.NewFakeRecorder(16)}

	err := reconcileLLMISVCNetworkPolicies(t.Context(), r, service, &Config{})
	if !errors.Is(err, deleteErr) {
		t.Fatalf("reconcile error = %v, want the stale egress prune failure %v", err, deleteErr)
	}
	// Ingress and the stored components' allow-all grants reconcile
	// independently of the failed egress prune.
	for _, name := range []string{
		ingressNetworkPolicyName(service, networkPolicyModeDefault, networkPolicyComponentMain),
		allowAllEgressNetworkPolicyName(service, networkPolicyComponentMain),
	} {
		if err := cl.Get(t.Context(), types.NamespacedName{Name: name, Namespace: service.Namespace}, &netv1.NetworkPolicy{}); err != nil {
			t.Fatalf("policy %s must exist despite the failed egress prune: %v", name, err)
		}
	}
	// The combined and monitoring legacy policies are superseded coverage, so
	// their removal waits for the successful egress prune: they — and the
	// stale egress name itself — are retained for the retry.
	for _, name := range []string{
		allowAllNetworkPolicyName(service),
		legacyMonitoringNetworkPolicyName(service),
		staleEgressScheduler.Name,
	} {
		if err := cl.Get(t.Context(), types.NamespacedName{Name: name, Namespace: service.Namespace}, &netv1.NetworkPolicy{}); err != nil {
			t.Fatalf("policy %s must be retained while the egress prune fails: %v", name, err)
		}
	}

	failDelete = false
	if err := reconcileLLMISVCNetworkPolicies(t.Context(), r, service, &Config{}); err != nil {
		t.Fatalf("retry failed after the egress prune recovered: %v", err)
	}
	for _, name := range []string{
		allowAllNetworkPolicyName(service),
		legacyMonitoringNetworkPolicyName(service),
		staleEgressScheduler.Name,
		egressNetworkPolicyName(service),
	} {
		if err := cl.Get(t.Context(), types.NamespacedName{Name: name, Namespace: service.Namespace}, &netv1.NetworkPolicy{}); !apierrors.IsNotFound(err) {
			t.Fatalf("policy %s remained after the successful retry: %v", name, err)
		}
	}
}

// steadyStateComponent is one stored component of the steady-state matrix
// fixtures: its fixed component key and its complete stored Pod selector.
type steadyStateComponent struct {
	component string
	selector  metav1.LabelSelector
}

// steadyStateStoredComponents returns the stored component fixtures of the
// steady-state matrix: one component per policy class, each with a complete
// stored selector that deliberately differs from the tracing policy's
// application labels, so the matrix proves the two Pod scopes stay apart.
func steadyStateStoredComponents(service *v1alpha2.LLMInferenceService) []steadyStateComponent {
	name := service.GetName()
	return []steadyStateComponent{
		{networkPolicyComponentMain, metav1.LabelSelector{MatchLabels: map[string]string{
			constants.KubernetesAppNameLabelKey:   name,
			constants.KubernetesComponentLabelKey: constants.KServeComponentWorkload,
		}}},
		{networkPolicyComponentScheduler, metav1.LabelSelector{MatchLabels: map[string]string{
			constants.KubernetesAppNameLabelKey:   name,
			constants.KubernetesComponentLabelKey: constants.LLMComponentRouterScheduler,
		}}},
		{networkPolicyComponentTokenizer, metav1.LabelSelector{MatchLabels: map[string]string{
			constants.KubernetesAppNameLabelKey:   name,
			constants.KubernetesComponentLabelKey: constants.LLMComponentTokenizer,
		}}},
	}
}

// steadyStateDeploymentName returns the stored Deployment name of a
// steady-state matrix component key.
func steadyStateDeploymentName(service *v1alpha2.LLMInferenceService, component string) string {
	switch component {
	case networkPolicyComponentScheduler:
		return schedulerDeploymentName(service)
	case networkPolicyComponentTokenizer:
		return tokenizerDeploymentName(service)
	default:
		return mainDeploymentName(service)
	}
}

// tracingApplicationSelector returns the pre-existing application-label
// selector of the tracing policy: part-of plus app-name, never a stored
// component selector.
func tracingApplicationSelector(service *v1alpha2.LLMInferenceService) metav1.LabelSelector {
	return metav1.LabelSelector{MatchLabels: map[string]string{
		constants.KubernetesPartOfLabelKey:  constants.LLMInferenceServicePartOfValue,
		constants.KubernetesAppNameLabelKey: service.GetName(),
	}}
}

// assertBroadLegacyNamesAbsent verifies that no retired legacy broad policy
// name exists: the combined allow-all policy, the general egress policy, and
// the legacy monitoring policy.
func assertBroadLegacyNamesAbsent(t *testing.T, cl client.Client, service *v1alpha2.LLMInferenceService) {
	t.Helper()
	for _, name := range []string{
		allowAllNetworkPolicyName(service),
		egressNetworkPolicyName(service),
		legacyMonitoringNetworkPolicyName(service),
	} {
		if err := cl.Get(t.Context(), types.NamespacedName{Name: name, Namespace: service.Namespace}, &netv1.NetworkPolicy{}); !apierrors.IsNotFound(err) {
			t.Fatalf("retired legacy broad policy %s must not exist: %v", name, err)
		}
	}
}

// assertIngressSteadyState verifies the ingress steady state of one mode: a
// per-component ingress-only policy per stored component selecting its stored
// selector, no egress rules on them, no policy of the other mode, and no
// pre-selector names.
func assertIngressSteadyState(t *testing.T, cl client.Client, service *v1alpha2.LLMInferenceService, components []steadyStateComponent, mode string) {
	t.Helper()
	for _, component := range components {
		policy := &netv1.NetworkPolicy{}
		if err := cl.Get(t.Context(), types.NamespacedName{
			Name:      ingressNetworkPolicyName(service, mode, component.component),
			Namespace: service.Namespace,
		}, policy); err != nil {
			t.Fatalf("%s-mode ingress policy for %s was not created: %v", mode, component.component, err)
		}
		assertOwnedNetworkPolicyMetadata(t, service, policy)
		assertNetworkPolicySelectsStoredSelector(t, policy, component.selector)
		if !reflect.DeepEqual(policy.Spec.PolicyTypes, []netv1.PolicyType{netv1.PolicyTypeIngress}) {
			t.Fatalf("policy %s types got %v, want ingress-only", policy.Name, policy.Spec.PolicyTypes)
		}
		if policy.Spec.Egress != nil {
			t.Fatalf("%s-mode policy %s must not create egress rules: %#v", mode, policy.Name, policy.Spec.Egress)
		}
		switch mode {
		case networkPolicyModeDefault:
			if len(policy.Spec.Ingress) != 1 || len(policy.Spec.Ingress[0].From) != 0 || len(policy.Spec.Ingress[0].Ports) != 0 {
				t.Fatalf("default-mode policy %s ingress is not explicit allow-all: %#v", policy.Name, policy.Spec.Ingress)
			}
		case networkPolicyModeStrict:
			if len(policy.Spec.Ingress) == 0 {
				t.Fatalf("strict-mode policy %s admits no ingress at all: %#v", policy.Name, policy.Spec.Ingress)
			}
			for _, rule := range policy.Spec.Ingress {
				if len(rule.From) == 0 {
					t.Fatalf("strict-mode policy %s has an ingress rule that admits every source: %#v", policy.Name, rule)
				}
			}
		}
	}
	otherMode := networkPolicyModeStrict
	if mode == networkPolicyModeStrict {
		otherMode = networkPolicyModeDefault
	}
	for _, name := range append(ingressNetworkPolicyNames(service, otherMode),
		workloadNetworkPolicyName(service),
		schedulerNetworkPolicyName(service),
		tokenizerNetworkPolicyName(service),
	) {
		if err := cl.Get(t.Context(), types.NamespacedName{Name: name, Namespace: service.Namespace}, &netv1.NetworkPolicy{}); !apierrors.IsNotFound(err) {
			t.Fatalf("policy of the other ingress state %s remained: %v", name, err)
		}
	}
}

// assertEgressSteadyState verifies the complete egress steady state: with the
// tracing opt-in, exactly the tracing policy governs egress — under its
// application-label selector — and every owned unrestricted grant is gone;
// without it, every stored component keeps an explicit per-component allow-all
// egress grant and no tracing policy exists.
func assertEgressSteadyState(t *testing.T, cl client.Client, service *v1alpha2.LLMInferenceService, components []steadyStateComponent, wantOptIn bool) {
	t.Helper()
	if wantOptIn {
		stored := &netv1.NetworkPolicy{}
		if err := cl.Get(t.Context(), types.NamespacedName{
			Name: tracingNetworkPolicyName(service), Namespace: service.Namespace,
		}, stored); err != nil {
			t.Fatalf("opted-in service must keep the tracing policy: %v", err)
		}
		if !metav1.IsControlledBy(stored, service) {
			t.Fatal("tracing policy is not controlled by the service")
		}
		want := expectedTracingNetworkPolicy(service, netv1.NetworkPolicyPeer{}, 0, false)
		if !reflect.DeepEqual(stored.Spec, want.Spec) {
			t.Fatalf("tracing policy spec got %#v, want the documented tracing permissions %#v", stored.Spec, want.Spec)
		}
		if !reflect.DeepEqual(stored.Spec.PodSelector, tracingApplicationSelector(service)) {
			t.Fatalf("tracing policy selector got %#v, want the application labels %#v",
				stored.Spec.PodSelector, tracingApplicationSelector(service))
		}
		// The opt-in removal is bounded by the fixed component keys, not the
		// current topology, so no owned unrestricted grant may remain at all.
		for _, name := range allowAllEgressNetworkPolicyNames(service) {
			if err := cl.Get(t.Context(), types.NamespacedName{Name: name, Namespace: service.Namespace}, &netv1.NetworkPolicy{}); !apierrors.IsNotFound(err) {
				t.Fatalf("opted-in service must not keep the unrestricted egress grant %s: %v", name, err)
			}
		}
	} else {
		if err := cl.Get(t.Context(), types.NamespacedName{
			Name: tracingNetworkPolicyName(service), Namespace: service.Namespace,
		}, &netv1.NetworkPolicy{}); !apierrors.IsNotFound(err) {
			t.Fatalf("opted-out service must not keep a tracing policy: %v", err)
		}
		for _, component := range components {
			grant := &netv1.NetworkPolicy{}
			if err := cl.Get(t.Context(), types.NamespacedName{
				Name:      allowAllEgressNetworkPolicyName(service, component.component),
				Namespace: service.Namespace,
			}, grant); err != nil {
				t.Fatalf("component %s must keep an explicit allow-all egress grant: %v", component.component, err)
			}
			assertOwnedNetworkPolicyMetadata(t, service, grant)
			assertNetworkPolicySelectsStoredSelector(t, grant, component.selector)
			if !reflect.DeepEqual(grant.Spec.PolicyTypes, []netv1.PolicyType{netv1.PolicyTypeEgress}) {
				t.Fatalf("egress grant %s types got %v, want egress-only", grant.Name, grant.Spec.PolicyTypes)
			}
			if len(grant.Spec.Egress) != 1 || len(grant.Spec.Egress[0].To) != 0 || len(grant.Spec.Egress[0].Ports) != 0 {
				t.Fatalf("egress grant %s is not an explicit allow-all rule: %#v", grant.Name, grant.Spec.Egress)
			}
			if grant.Spec.Ingress != nil {
				t.Fatalf("egress grant %s must not create ingress rules: %#v", grant.Name, grant.Spec.Ingress)
			}
		}
	}
	policies := &netv1.NetworkPolicyList{}
	if err := cl.List(t.Context(), policies, client.InNamespace(service.Namespace)); err != nil {
		t.Fatal(err)
	}
	wantCount := 2 * len(components)
	if wantOptIn {
		wantCount = len(components) + 1
	}
	if len(policies.Items) != wantCount {
		names := make([]string, 0, len(policies.Items))
		for i := range policies.Items {
			names = append(names, policies.Items[i].Name)
		}
		t.Fatalf("got %d policies (%v), want %d: the ingress policies of the mode plus %s",
			len(policies.Items), names, wantCount,
			map[bool]string{true: "the tracing policy", false: "one egress grant per stored component"}[wantOptIn])
	}
}

// TestReconcileLLMISVCNetworkPoliciesSteadyStateMatrix walks the complete
// steady-state matrix of the egress lifecycle — both ingress modes, including
// a nil controller config (which selects default mode), against every
// tracing/annotation combination — and asserts the full steady state rather
// than policy counts: owned policy names, stored selectors, ownership,
// PolicyTypes, and exact unrestricted/restricted rules. The stored selectors
// deliberately differ from the tracing policy's application labels, pinning
// that each egress state keeps its own Pod scope.
func TestReconcileLLMISVCNetworkPoliciesSteadyStateMatrix(t *testing.T) {
	annotationKey := constants.EnableTracingEgressNetworkPolicyAnnotationKey
	crossNamespaceEndpoint := "http://collector.observability.svc:4317"
	cases := []struct {
		name       string
		tracing    *v1alpha2.TracingSpec
		annotation *string
		wantOptIn  bool
	}{
		{name: "tracing absent without the annotation"},
		{name: "tracing absent with the exact annotation still opts out", annotation: ptr.To("true")},
		{name: "empty-but-present tracing spec without the annotation opts out", tracing: &v1alpha2.TracingSpec{}},
		{name: "tracing with an empty annotation value opts out", tracing: &v1alpha2.TracingSpec{}, annotation: ptr.To("")},
		{name: "tracing with annotation false opts out", tracing: &v1alpha2.TracingSpec{}, annotation: ptr.To("false")},
		{name: "tracing with annotation True is not the exact literal", tracing: &v1alpha2.TracingSpec{}, annotation: ptr.To("True")},
		{name: "tracing with a padded annotation value opts out", tracing: &v1alpha2.TracingSpec{}, annotation: ptr.To(" true")},
		{name: "empty tracing spec with the exact annotation opts in", tracing: &v1alpha2.TracingSpec{}, annotation: ptr.To("true"), wantOptIn: true},
		{
			name:       "tracing with an exporter endpoint and the exact annotation opts in",
			tracing:    &v1alpha2.TracingSpec{ExporterEndpoint: ptr.To(crossNamespaceEndpoint)},
			annotation: ptr.To("true"),
			wantOptIn:  true,
		},
	}
	modes := []struct {
		name   string
		config *Config
	}{
		{name: "default mode", config: &Config{}},
		{name: "strict mode", config: &Config{FeatureGates: FeatureGates{StrictNetworkPolicy: true}}},
		{name: "nil config", config: nil},
	}
	for _, ingressMode := range modes {
		for _, tt := range cases {
			t.Run(ingressMode.name+" / "+tt.name, func(t *testing.T) {
				service := &v1alpha2.LLMInferenceService{
					ObjectMeta: metav1.ObjectMeta{Name: "model", Namespace: "team-a", UID: "model-uid"},
					Spec:       v1alpha2.LLMInferenceServiceSpec{Tracing: tt.tracing},
				}
				if tt.annotation != nil {
					service.Annotations = map[string]string{annotationKey: *tt.annotation}
				}
				components := steadyStateStoredComponents(service)
				seeded := make([]client.Object, 0, len(components))
				for _, component := range components {
					seeded = append(seeded, ownedDeployment(service,
						steadyStateDeploymentName(service, component.component),
						component.selector.DeepCopy(), component.selector.MatchLabels))
				}
				cl := newNetworkPolicyTestClient(t, seeded...)
				r := &LLMISVCReconciler{Client: cl, EventRecorder: record.NewFakeRecorder(32)}

				// Repeated reconciles converge to the same steady state.
				for range 2 {
					if err := reconcileLLMISVCNetworkPolicies(t.Context(), r, service, ingressMode.config); err != nil {
						t.Fatalf("reconcile failed: %v", err)
					}
				}

				mode := networkPolicyModeDefault
				if ingressMode.config != nil && ingressMode.config.FeatureGates.StrictNetworkPolicy {
					mode = networkPolicyModeStrict
				}
				assertIngressSteadyState(t, cl, service, components, mode)
				assertEgressSteadyState(t, cl, service, components, tt.wantOptIn)
				assertBroadLegacyNamesAbsent(t, cl, service)
			})
		}
	}
}

// TestReconcileLLMISVCNetworkPoliciesAnnotationAndSpecOnlyTransitions walks
// the spec-only and annotation-only transitions of the egress state in both
// ingress modes: adding or removing only the tracing spec, and adding,
// removing, and altering only the annotation value, switch the egress state
// exactly as the shared opt-in predicate decides, while the ingress policies
// of the mode stay in place and ingress-only throughout.
func TestReconcileLLMISVCNetworkPoliciesAnnotationAndSpecOnlyTransitions(t *testing.T) {
	annotationKey := constants.EnableTracingEgressNetworkPolicyAnnotationKey
	for _, ingressMode := range []struct {
		name   string
		config *Config
	}{
		{name: "default mode", config: &Config{}},
		{name: "strict mode", config: &Config{FeatureGates: FeatureGates{StrictNetworkPolicy: true}}},
	} {
		t.Run(ingressMode.name, func(t *testing.T) {
			service := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{
				Name: "model", Namespace: "team-a", UID: "model-uid",
			}}
			mainSelector := metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "model"}}
			cl := newNetworkPolicyTestClient(t,
				ownedDeployment(service, mainDeploymentName(service), mainSelector.DeepCopy(), mainSelector.MatchLabels))
			r := &LLMISVCReconciler{Client: cl, EventRecorder: record.NewFakeRecorder(32)}
			mode := networkPolicyModeDefault
			if ingressMode.config.FeatureGates.StrictNetworkPolicy {
				mode = networkPolicyModeStrict
			}
			egressMainName := allowAllEgressNetworkPolicyName(service, networkPolicyComponentMain)

			for _, state := range []struct {
				name       string
				tracing    *v1alpha2.TracingSpec
				annotation *string
				wantOptIn  bool
			}{
				{name: "no tracing and no annotation"},
				{name: "adding only the tracing spec keeps the unrestricted state", tracing: &v1alpha2.TracingSpec{}},
				{name: "adding only the annotation switches to tracing", tracing: &v1alpha2.TracingSpec{}, annotation: ptr.To("true"), wantOptIn: true},
				{name: "altering the annotation to false returns to unrestricted", tracing: &v1alpha2.TracingSpec{}, annotation: ptr.To("false")},
				{name: "altering the annotation to True keeps the unrestricted state", tracing: &v1alpha2.TracingSpec{}, annotation: ptr.To("True")},
				{name: "returning the annotation to true switches back to tracing", tracing: &v1alpha2.TracingSpec{}, annotation: ptr.To("true"), wantOptIn: true},
				{name: "removing only the annotation returns to unrestricted", tracing: &v1alpha2.TracingSpec{}, wantOptIn: false},
				{name: "re-adding the annotation restores tracing", tracing: &v1alpha2.TracingSpec{}, annotation: ptr.To("true"), wantOptIn: true},
				{name: "removing only the tracing spec returns to unrestricted", annotation: ptr.To("true")},
				{name: "re-adding the tracing spec restores tracing", tracing: &v1alpha2.TracingSpec{}, annotation: ptr.To("true"), wantOptIn: true},
			} {
				service.Spec.Tracing = state.tracing
				service.Annotations = nil
				if state.annotation != nil {
					service.Annotations = map[string]string{annotationKey: *state.annotation}
				}
				// Repeated reconciles at every state keep the same egress state.
				for range 2 {
					if err := reconcileLLMISVCNetworkPolicies(t.Context(), r, service, ingressMode.config); err != nil {
						t.Fatalf("%s: reconcile failed: %v", state.name, err)
					}
				}
				if state.wantOptIn {
					if err := cl.Get(t.Context(), types.NamespacedName{
						Name: tracingNetworkPolicyName(service), Namespace: service.Namespace,
					}, &netv1.NetworkPolicy{}); err != nil {
						t.Fatalf("%s: the tracing policy must exist: %v", state.name, err)
					}
					for _, name := range allowAllEgressNetworkPolicyNames(service) {
						if err := cl.Get(t.Context(), types.NamespacedName{Name: name, Namespace: service.Namespace}, &netv1.NetworkPolicy{}); !apierrors.IsNotFound(err) {
							t.Fatalf("%s: the unrestricted egress grant %s must be gone: %v", state.name, name, err)
						}
					}
				} else {
					grant := &netv1.NetworkPolicy{}
					if err := cl.Get(t.Context(), types.NamespacedName{Name: egressMainName, Namespace: service.Namespace}, grant); err != nil {
						t.Fatalf("%s: the explicit allow-all egress grant must exist: %v", state.name, err)
					}
					if !reflect.DeepEqual(grant.Spec.PodSelector, mainSelector) {
						t.Fatalf("%s: egress grant selector got %#v, want the stored Deployment selector", state.name, grant.Spec.PodSelector)
					}
					if err := cl.Get(t.Context(), types.NamespacedName{
						Name: tracingNetworkPolicyName(service), Namespace: service.Namespace,
					}, &netv1.NetworkPolicy{}); !apierrors.IsNotFound(err) {
						t.Fatalf("%s: the tracing policy must be gone: %v", state.name, err)
					}
				}
				// Ingress stays governed by the mode alone through every
				// egress-state change.
				ingress := &netv1.NetworkPolicy{}
				if err := cl.Get(t.Context(), types.NamespacedName{
					Name:      ingressNetworkPolicyName(service, mode, networkPolicyComponentMain),
					Namespace: service.Namespace,
				}, ingress); err != nil {
					t.Fatalf("%s: the ingress policy was lost: %v", state.name, err)
				}
				if !reflect.DeepEqual(ingress.Spec.PolicyTypes, []netv1.PolicyType{netv1.PolicyTypeIngress}) || ingress.Spec.Egress != nil {
					t.Fatalf("%s: the ingress policy must stay ingress-only: %#v", state.name, ingress.Spec)
				}
			}
		})
	}
}

// TestReconcileLLMISVCNetworkPoliciesModeToggleKeepsEgressPoliciesUnchanged
// pins that ingress-mode toggles never churn the egress policies: with every
// fixed component key stored — including the set-based workloads — the
// per-component allow-all egress grants keep their identity (resource version,
// spec, and labels) across default→strict→default toggles, while the ingress
// policies switch names with the mode.
func TestReconcileLLMISVCNetworkPoliciesModeToggleKeepsEgressPoliciesUnchanged(t *testing.T) {
	service := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{
		Name: "model", Namespace: "team-a", UID: "model-uid",
	}}
	name := service.GetName()
	mainSelector := metav1.LabelSelector{MatchLabels: map[string]string{
		constants.KubernetesAppNameLabelKey: name, constants.KubernetesComponentLabelKey: constants.KServeComponentWorkload,
	}}
	prefillSelector := metav1.LabelSelector{MatchLabels: map[string]string{
		constants.KubernetesAppNameLabelKey: name, constants.LLMDRoleLabelKey: constants.LLMDRolePrefill,
	}}
	schedulerSelector := metav1.LabelSelector{MatchLabels: map[string]string{
		constants.KubernetesAppNameLabelKey: name, constants.KubernetesComponentLabelKey: constants.LLMComponentRouterScheduler,
	}}
	tokenizerSelector := metav1.LabelSelector{MatchLabels: map[string]string{
		constants.KubernetesAppNameLabelKey: name, constants.KubernetesComponentLabelKey: constants.LLMComponentTokenizer,
	}}
	seeded := []client.Object{
		ownedDeployment(service, mainDeploymentName(service), mainSelector.DeepCopy(), mainSelector.MatchLabels),
		ownedDeployment(service, prefillDeploymentName(service), prefillSelector.DeepCopy(), prefillSelector.MatchLabels),
		ownedDeployment(service, schedulerDeploymentName(service), schedulerSelector.DeepCopy(), schedulerSelector.MatchLabels),
		ownedDeployment(service, tokenizerDeploymentName(service), tokenizerSelector.DeepCopy(), tokenizerSelector.MatchLabels),
		ownedLeaderWorkerSet(service, mainLWSName(service)),
		ownedLeaderWorkerSet(service, prefillLWSName(service)),
		ownedDisaggregatedSet(service, disaggregatedSetName(service)),
	}
	cl := newNetworkPolicyTestClient(t, seeded...)
	r := &LLMISVCReconciler{Client: cl, EventRecorder: record.NewFakeRecorder(64)}
	defaultConfig := &Config{}
	strictConfig := &Config{FeatureGates: FeatureGates{StrictNetworkPolicy: true}}
	componentSelectors := map[string]metav1.LabelSelector{
		networkPolicyComponentMain:      mainSelector,
		networkPolicyComponentPrefill:   prefillSelector,
		networkPolicyComponentScheduler: schedulerSelector,
		networkPolicyComponentTokenizer: tokenizerSelector,
		networkPolicyComponentMainMN:    {MatchLabels: map[string]string{lwsapi.SetNameLabelKey: mainLWSName(service)}},
		networkPolicyComponentPrefillMN: {MatchLabels: map[string]string{lwsapi.SetNameLabelKey: prefillLWSName(service)}},
		networkPolicyComponentPD:        {MatchLabels: map[string]string{disaggregatedsetv1.SetNameLabelKey: disaggregatedSetName(service)}},
	}

	if err := reconcileLLMISVCNetworkPolicies(t.Context(), r, service, defaultConfig); err != nil {
		t.Fatalf("default-mode reconcile failed: %v", err)
	}
	captured := make(map[string]*netv1.NetworkPolicy, len(networkPolicyComponentKeys))
	for _, component := range networkPolicyComponentKeys {
		policy := &netv1.NetworkPolicy{}
		if err := cl.Get(t.Context(), types.NamespacedName{
			Name:      allowAllEgressNetworkPolicyName(service, component),
			Namespace: service.Namespace,
		}, policy); err != nil {
			t.Fatalf("egress grant for %s was not created: %v", component, err)
		}
		if want := componentSelectors[component]; !reflect.DeepEqual(policy.Spec.PodSelector, want) {
			t.Fatalf("egress grant for %s selector got %#v, want the stored selector %#v", component, policy.Spec.PodSelector, want)
		}
		if !reflect.DeepEqual(policy.Spec.PolicyTypes, []netv1.PolicyType{netv1.PolicyTypeEgress}) {
			t.Fatalf("egress grant for %s types got %v, want egress-only", component, policy.Spec.PolicyTypes)
		}
		captured[component] = policy.DeepCopy()
	}

	for _, toggle := range []struct {
		name   string
		config *Config
		mode   string
	}{
		{name: "strict", config: strictConfig, mode: networkPolicyModeStrict},
		{name: "default", config: defaultConfig, mode: networkPolicyModeDefault},
	} {
		if err := reconcileLLMISVCNetworkPolicies(t.Context(), r, service, toggle.config); err != nil {
			t.Fatalf("%s-mode reconcile failed: %v", toggle.name, err)
		}
		for _, component := range networkPolicyComponentKeys {
			policy := &netv1.NetworkPolicy{}
			if err := cl.Get(t.Context(), types.NamespacedName{
				Name:      allowAllEgressNetworkPolicyName(service, component),
				Namespace: service.Namespace,
			}, policy); err != nil {
				t.Fatalf("the %s toggle lost the egress grant for %s: %v", toggle.name, component, err)
			}
			before := captured[component]
			if policy.ResourceVersion != before.ResourceVersion {
				t.Fatalf("the %s toggle rewrote the egress grant for %s: resource version %s, want %s",
					toggle.name, component, policy.ResourceVersion, before.ResourceVersion)
			}
			if !reflect.DeepEqual(policy, before) {
				t.Fatalf("the %s toggle churned the egress grant for %s: got %#v, want %#v", toggle.name, component, policy, before)
			}
			ingress := &netv1.NetworkPolicy{}
			if err := cl.Get(t.Context(), types.NamespacedName{
				Name:      ingressNetworkPolicyName(service, toggle.mode, component),
				Namespace: service.Namespace,
			}, ingress); err != nil {
				t.Fatalf("%s-mode ingress policy for %s was not created: %v", toggle.name, component, err)
			}
			if ingress.Spec.Egress != nil {
				t.Fatalf("%s-mode ingress policy for %s must not create egress rules: %#v", toggle.name, component, ingress.Spec.Egress)
			}
		}
		otherMode := networkPolicyModeDefault
		if toggle.mode == networkPolicyModeDefault {
			otherMode = networkPolicyModeStrict
		}
		for _, name := range ingressNetworkPolicyNames(service, otherMode) {
			if err := cl.Get(t.Context(), types.NamespacedName{Name: name, Namespace: service.Namespace}, &netv1.NetworkPolicy{}); !apierrors.IsNotFound(err) {
				t.Fatalf("policy of the other ingress state %s remained after the %s toggle: %v", name, toggle.name, err)
			}
		}
	}
}

// TestReconcileEgressNetworkPoliciesCoverInventoryEdgeCases covers the
// inventory edge cases of the egress lifecycle: a component scaled to zero
// replicas keeps its explicit allow-all grant — coverage is never dropped
// while the component stays stored — and absent optional workload APIs (the
// LeaderWorkerSet and DisaggregatedSet CRDs) neither block the lifecycle nor
// produce egress policies for components that cannot be stored.
func TestReconcileEgressNetworkPoliciesCoverInventoryEdgeCases(t *testing.T) {
	t.Run("zero-replica components keep their egress coverage", func(t *testing.T) {
		service := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{
			Name: "model", Namespace: "team-a", UID: "model-uid",
		}}
		mainSelector := metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "model"}}
		deployment := ownedDeployment(service, mainDeploymentName(service), mainSelector.DeepCopy(), mainSelector.MatchLabels)
		deployment.Spec.Replicas = ptr.To(int32(0))
		cl := newNetworkPolicyTestClient(t, deployment)
		r := &LLMISVCReconciler{Client: cl, EventRecorder: record.NewFakeRecorder(16)}
		egressMainName := allowAllEgressNetworkPolicyName(service, networkPolicyComponentMain)

		for range 2 {
			if err := reconcileLLMISVCNetworkPolicies(t.Context(), r, service, &Config{}); err != nil {
				t.Fatalf("reconcile with a zero-replica component failed: %v", err)
			}
			if err := cl.Get(t.Context(), types.NamespacedName{Name: egressMainName, Namespace: service.Namespace}, &netv1.NetworkPolicy{}); err != nil {
				t.Fatalf("a zero-replica component must keep its egress grant: %v", err)
			}
		}
		captured := &netv1.NetworkPolicy{}
		if err := cl.Get(t.Context(), types.NamespacedName{Name: egressMainName, Namespace: service.Namespace}, captured); err != nil {
			t.Fatal(err)
		}

		// Scaling the component back up must not churn the grant either.
		stored := &appsv1.Deployment{}
		if err := cl.Get(t.Context(), types.NamespacedName{
			Name: mainDeploymentName(service), Namespace: service.Namespace,
		}, stored); err != nil {
			t.Fatal(err)
		}
		stored.Spec.Replicas = ptr.To(int32(3))
		if err := cl.Update(t.Context(), stored); err != nil {
			t.Fatal(err)
		}
		if err := reconcileLLMISVCNetworkPolicies(t.Context(), r, service, &Config{}); err != nil {
			t.Fatalf("reconcile after scaling up failed: %v", err)
		}
		after := &netv1.NetworkPolicy{}
		if err := cl.Get(t.Context(), types.NamespacedName{Name: egressMainName, Namespace: service.Namespace}, after); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(after, captured) {
			t.Fatalf("scaling the component churned its egress grant: got %#v, want %#v", after, captured)
		}
	})

	t.Run("absent optional APIs do not block the egress lifecycle", func(t *testing.T) {
		service := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{
			Name: "model", Namespace: "team-a", UID: "model-uid",
		}}
		mainSelector := metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "model"}}
		cl := newNetworkPolicyTestClientWithInterceptor(t, interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				switch obj.(type) {
				case *lwsapi.LeaderWorkerSet:
					return errLWSNoMatch
				case *disaggregatedsetv1.DisaggregatedSet:
					return errDisaggregatedSetNoMatch
				}
				return c.Get(ctx, key, obj, opts...)
			},
		}, ownedDeployment(service, mainDeploymentName(service), mainSelector.DeepCopy(), mainSelector.MatchLabels))
		r := &LLMISVCReconciler{Client: cl, EventRecorder: record.NewFakeRecorder(16)}

		for _, config := range []*Config{
			{},
			{FeatureGates: FeatureGates{StrictNetworkPolicy: true}},
		} {
			if err := reconcileLLMISVCNetworkPolicies(t.Context(), r, service, config); err != nil {
				t.Fatalf("reconcile with absent optional APIs failed: %v", err)
			}
		}
		if err := cl.Get(t.Context(), types.NamespacedName{
			Name:      allowAllEgressNetworkPolicyName(service, networkPolicyComponentMain),
			Namespace: service.Namespace,
		}, &netv1.NetworkPolicy{}); err != nil {
			t.Fatalf("the stored component must keep its egress grant: %v", err)
		}
		for _, component := range []string{networkPolicyComponentMainMN, networkPolicyComponentPrefillMN, networkPolicyComponentPD} {
			if err := cl.Get(t.Context(), types.NamespacedName{
				Name:      allowAllEgressNetworkPolicyName(service, component),
				Namespace: service.Namespace,
			}, &netv1.NetworkPolicy{}); !apierrors.IsNotFound(err) {
				t.Fatalf("a component that cannot be stored must not gain an egress grant: %v", err)
			}
		}
	})
}

// TestReconcileLLMISVCNetworkPoliciesPreservesForeignDefaultDenyEgressPolicy
// covers the additive-policy boundary of the plan: a namespace carrying a
// foreign default-deny egress policy. Without the opt-in the controller must
// establish its own explicit unrestricted grants — under a default-deny, an
// absent egress policy is not an allow-all grant — and with the opt-in no
// owned unrestricted egress rule may remain. The foreign policy is never
// adopted, mutated, or deleted. The fake client cannot prove packet
// enforcement; this pins only the policy objects the controller owns.
func TestReconcileLLMISVCNetworkPoliciesPreservesForeignDefaultDenyEgressPolicy(t *testing.T) {
	service := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{
		Name: "model", Namespace: "team-a", UID: "model-uid",
	}}
	mainSelector := metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "model"}}
	foreignDefaultDeny := &netv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "namespace-default-deny-egress", Namespace: service.Namespace},
		Spec: netv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{},
			PolicyTypes: []netv1.PolicyType{netv1.PolicyTypeEgress},
		},
	}
	cl := newNetworkPolicyTestClient(t, foreignDefaultDeny,
		ownedDeployment(service, mainDeploymentName(service), mainSelector.DeepCopy(), mainSelector.MatchLabels))
	r := &LLMISVCReconciler{Client: cl, EventRecorder: record.NewFakeRecorder(32)}
	before := foreignDefaultDeny.DeepCopy()
	assertForeignUnchanged := func(stage string) {
		t.Helper()
		after := &netv1.NetworkPolicy{}
		if err := cl.Get(t.Context(), client.ObjectKeyFromObject(foreignDefaultDeny), after); err != nil {
			t.Fatalf("%s: the foreign default-deny policy was deleted: %v", stage, err)
		}
		if !reflect.DeepEqual(after, before) {
			t.Fatalf("%s: the foreign default-deny policy was mutated: before=%#v after=%#v", stage, before, after)
		}
	}

	// Without the opt-in the explicit unrestricted grant exists next to the
	// foreign default-deny.
	if err := reconcileLLMISVCNetworkPolicies(t.Context(), r, service, &Config{}); err != nil {
		t.Fatalf("unrestricted reconcile failed: %v", err)
	}
	grant := &netv1.NetworkPolicy{}
	if err := cl.Get(t.Context(), types.NamespacedName{
		Name:      allowAllEgressNetworkPolicyName(service, networkPolicyComponentMain),
		Namespace: service.Namespace,
	}, grant); err != nil {
		t.Fatalf("an explicit unrestricted grant must exist under a foreign default-deny: %v", err)
	}
	if len(grant.Spec.Egress) != 1 || len(grant.Spec.Egress[0].To) != 0 || len(grant.Spec.Egress[0].Ports) != 0 {
		t.Fatalf("the unrestricted grant is not an explicit allow-all rule: %#v", grant.Spec.Egress)
	}
	assertForeignUnchanged("after the unrestricted reconcile")

	// Opting in removes every owned unrestricted grant while the foreign
	// default-deny stays untouched.
	service.Spec.Tracing = &v1alpha2.TracingSpec{}
	service.Annotations = map[string]string{constants.EnableTracingEgressNetworkPolicyAnnotationKey: "true"}
	if err := reconcileLLMISVCNetworkPolicies(t.Context(), r, service, &Config{}); err != nil {
		t.Fatalf("opt-in reconcile failed: %v", err)
	}
	if err := cl.Get(t.Context(), types.NamespacedName{
		Name: tracingNetworkPolicyName(service), Namespace: service.Namespace,
	}, &netv1.NetworkPolicy{}); err != nil {
		t.Fatalf("the tracing policy must exist while opted in: %v", err)
	}
	for _, name := range allowAllEgressNetworkPolicyNames(service) {
		if err := cl.Get(t.Context(), types.NamespacedName{Name: name, Namespace: service.Namespace}, &netv1.NetworkPolicy{}); !apierrors.IsNotFound(err) {
			t.Fatalf("no owned unrestricted egress rule may remain while opted in: %s: %v", name, err)
		}
	}
	assertForeignUnchanged("after the opt-in reconcile")
}

// TestReconcileEgressNetworkPoliciesRetainsCoverageOnReadAndUpdateFailures
// covers the read and update failure ordering of the unrestricted egress
// state: while an existing allow-all grant cannot be read or repaired, every
// policy it would replace — the opted-out tracing policy, the stale
// per-component name, and the legacy broad policies — is retained for the
// retry, which converges once the write recovers.
func TestReconcileEgressNetworkPoliciesRetainsCoverageOnReadAndUpdateFailures(t *testing.T) {
	newService := func() *v1alpha2.LLMInferenceService {
		return &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{
			Name: "model", Namespace: "team-a", UID: "model-uid",
		}}
	}
	mainSelector := metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "model"}}
	staleSchedulerEgressName := func(service *v1alpha2.LLMInferenceService) string {
		return allowAllEgressNetworkPolicyName(service, networkPolicyComponentScheduler)
	}

	t.Run("update failure on an existing grant", func(t *testing.T) {
		service := newService()
		egressMainName := allowAllEgressNetworkPolicyName(service, networkPolicyComponentMain)
		// A tampered owned grant: the reconcile must repair it in place, so a
		// failing Update blocks the establishment of the desired set.
		tamperedGrant := &netv1.NetworkPolicy{
			ObjectMeta: metav1.ObjectMeta{
				Name:            egressMainName,
				Namespace:       service.Namespace,
				OwnerReferences: ownedByController(service),
			},
			Spec: netv1.NetworkPolicySpec{
				PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"tampered": "true"}},
				PolicyTypes: []netv1.PolicyType{netv1.PolicyTypeIngress, netv1.PolicyTypeEgress},
				Ingress:     []netv1.NetworkPolicyIngressRule{{}},
				Egress:      []netv1.NetworkPolicyEgressRule{{Ports: []netv1.NetworkPolicyPort{networkPolicyPort(corev1.ProtocolTCP, 443)}}},
			},
		}
		updateErr := errors.New("allow-all egress NetworkPolicy Update failed")
		failUpdate := true
		cl := newNetworkPolicyTestClientWithInterceptor(t, interceptor.Funcs{
			Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				if policy, ok := obj.(*netv1.NetworkPolicy); ok && failUpdate && policy.Name == egressMainName {
					return updateErr
				}
				return c.Update(ctx, obj, opts...)
			},
		},
			ownedDeployment(service, mainDeploymentName(service), mainSelector.DeepCopy(), mainSelector.MatchLabels),
			tamperedGrant,
			// Prior coverage of the states and legacy schemes the unrestricted
			// state would replace.
			expectedTracingNetworkPolicy(service, netv1.NetworkPolicyPeer{}, 0, false),
			&netv1.NetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:            staleSchedulerEgressName(service),
					Namespace:       service.Namespace,
					OwnerReferences: ownedByController(service),
				},
				Spec: netv1.NetworkPolicySpec{PodSelector: mainSelector},
			},
			legacyGeneralEgressNetworkPolicy(service),
			legacyAllowAllNetworkPolicy(service),
		)
		r := &LLMISVCReconciler{Client: cl, EventRecorder: record.NewFakeRecorder(32)}

		err := reconcileLLMISVCNetworkPolicies(t.Context(), r, service, &Config{})
		if !errors.Is(err, updateErr) {
			t.Fatalf("reconcile error = %v, want the grant repair failure %v", err, updateErr)
		}
		if errors.Is(err, errNetworkPolicyOwnershipConflict) {
			t.Fatalf("a real API error was misclassified as an ownership conflict: %v", err)
		}
		// Ingress reconciles independently of the failed egress repair.
		if err := cl.Get(t.Context(), types.NamespacedName{
			Name:      ingressNetworkPolicyName(service, networkPolicyModeDefault, networkPolicyComponentMain),
			Namespace: service.Namespace,
		}, &netv1.NetworkPolicy{}); err != nil {
			t.Fatalf("ingress policy was not reconciled despite the egress repair failure: %v", err)
		}
		// The desired egress set was not established, so every policy it would
		// replace is retained for the retry.
		retained := &netv1.NetworkPolicy{}
		if err := cl.Get(t.Context(), types.NamespacedName{Name: egressMainName, Namespace: service.Namespace}, retained); err != nil {
			t.Fatalf("the tampered grant must survive its failed repair: %v", err)
		}
		if !reflect.DeepEqual(retained.Spec, tamperedGrant.Spec) {
			t.Fatalf("the tampered grant was mutated despite the failed repair: got %#v want %#v", retained.Spec, tamperedGrant.Spec)
		}
		for _, name := range []string{
			tracingNetworkPolicyName(service),
			staleSchedulerEgressName(service),
			egressNetworkPolicyName(service),
			allowAllNetworkPolicyName(service),
		} {
			if err := cl.Get(t.Context(), types.NamespacedName{Name: name, Namespace: service.Namespace}, &netv1.NetworkPolicy{}); err != nil {
				t.Fatalf("policy %s must be retained while the egress repair fails: %v", name, err)
			}
		}

		failUpdate = false
		if err := reconcileLLMISVCNetworkPolicies(t.Context(), r, service, &Config{}); err != nil {
			t.Fatalf("retry failed after the repair recovered: %v", err)
		}
		repaired := &netv1.NetworkPolicy{}
		if err := cl.Get(t.Context(), types.NamespacedName{Name: egressMainName, Namespace: service.Namespace}, repaired); err != nil {
			t.Fatalf("the grant was not repaired on the retry: %v", err)
		}
		if !reflect.DeepEqual(repaired.Spec.PodSelector, mainSelector) {
			t.Fatalf("repaired grant selector got %#v, want the stored Deployment selector", repaired.Spec.PodSelector)
		}
		if !reflect.DeepEqual(repaired.Spec.PolicyTypes, []netv1.PolicyType{netv1.PolicyTypeEgress}) {
			t.Fatalf("repaired grant types got %v, want egress-only", repaired.Spec.PolicyTypes)
		}
		if len(repaired.Spec.Egress) != 1 || len(repaired.Spec.Egress[0].To) != 0 || len(repaired.Spec.Egress[0].Ports) != 0 {
			t.Fatalf("repaired grant is not an explicit allow-all rule: %#v", repaired.Spec.Egress)
		}
		for _, name := range []string{
			tracingNetworkPolicyName(service),
			staleSchedulerEgressName(service),
			egressNetworkPolicyName(service),
			allowAllNetworkPolicyName(service),
		} {
			if err := cl.Get(t.Context(), types.NamespacedName{Name: name, Namespace: service.Namespace}, &netv1.NetworkPolicy{}); !apierrors.IsNotFound(err) {
				t.Fatalf("policy %s remained after the successful retry: %v", name, err)
			}
		}
	})

	t.Run("get failure on an existing grant", func(t *testing.T) {
		service := newService()
		egressMainName := allowAllEgressNetworkPolicyName(service, networkPolicyComponentMain)
		getErr := errors.New("allow-all egress NetworkPolicy Get failed")
		failGet := true
		cl := newNetworkPolicyTestClientWithInterceptor(t, interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*netv1.NetworkPolicy); ok && failGet && key.Name == egressMainName {
					return getErr
				}
				return c.Get(ctx, key, obj, opts...)
			},
		},
			ownedDeployment(service, mainDeploymentName(service), mainSelector.DeepCopy(), mainSelector.MatchLabels),
			expectedAllowAllEgressNetworkPolicies(service, networkPolicySelectorInventory{
				{Component: networkPolicyComponentMain, Class: networkPolicyRoleWorkload, Selector: mainSelector},
			})[0],
			expectedTracingNetworkPolicy(service, netv1.NetworkPolicyPeer{}, 0, false))
		r := &LLMISVCReconciler{Client: cl, EventRecorder: record.NewFakeRecorder(32)}

		if err := reconcileLLMISVCNetworkPolicies(t.Context(), r, service, &Config{}); !errors.Is(err, getErr) {
			t.Fatalf("reconcile error = %v, want the grant read failure %v", err, getErr)
		}
		// The read failure retains the prior tracing coverage for the retry.
		if err := cl.Get(t.Context(), types.NamespacedName{
			Name: tracingNetworkPolicyName(service), Namespace: service.Namespace,
		}, &netv1.NetworkPolicy{}); err != nil {
			t.Fatalf("the tracing policy must be retained while the grant cannot be read: %v", err)
		}

		failGet = false
		if err := reconcileLLMISVCNetworkPolicies(t.Context(), r, service, &Config{}); err != nil {
			t.Fatalf("retry failed after the read recovered: %v", err)
		}
		if err := cl.Get(t.Context(), types.NamespacedName{
			Name: tracingNetworkPolicyName(service), Namespace: service.Namespace,
		}, &netv1.NetworkPolicy{}); !apierrors.IsNotFound(err) {
			t.Fatalf("the tracing policy remained after the successful retry: %v", err)
		}
	})
}
