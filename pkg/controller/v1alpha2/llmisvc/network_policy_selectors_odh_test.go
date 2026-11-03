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
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"knative.dev/pkg/apis"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	disaggregatedsetv1 "sigs.k8s.io/lws/api/disaggregatedset/v1"
	lwsapi "sigs.k8s.io/lws/api/leaderworkerset/v1"

	"github.com/stretchr/testify/require"

	igwapi "sigs.k8s.io/gateway-api-inference-extension/api/v1"

	igwapiv1alpha2 "github.com/kserve/kserve/pkg/apis/gie/v1alpha2pool"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
	"github.com/kserve/kserve/pkg/constants"
)

// errDisaggregatedSetNoMatch simulates the error returned when the
// DisaggregatedSet CRD is not installed.
var errDisaggregatedSetNoMatch = &meta.NoKindMatchError{
	GroupKind: schema.GroupKind{
		Group: "disaggregatedset.x-k8s.io",
		Kind:  "DisaggregatedSet",
	},
}

// networkPolicySelectorTestClient returns a client with every scheme the
// selector inventory reads through (Deployments, the optional LWS and
// DisaggregatedSet APIs, and the schemes the Deployment builders use).
func networkPolicySelectorTestClient(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, appsv1.AddToScheme(scheme))
	require.NoError(t, v1alpha2.AddToScheme(scheme))
	require.NoError(t, lwsapi.AddToScheme(scheme))
	require.NoError(t, disaggregatedsetv1.AddToScheme(scheme))
	require.NoError(t, igwapi.Install(scheme))
	require.NoError(t, igwapiv1alpha2.AddToScheme(scheme))
	builder := fake.NewClientBuilder().WithScheme(scheme)
	if len(objects) > 0 {
		builder = builder.WithObjects(objects...)
	}
	return builder.Build()
}

func networkPolicySelectorTestClientWithInterceptors(t *testing.T, funcs interceptor.Funcs, objects ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, appsv1.AddToScheme(scheme))
	require.NoError(t, v1alpha2.AddToScheme(scheme))
	require.NoError(t, lwsapi.AddToScheme(scheme))
	require.NoError(t, disaggregatedsetv1.AddToScheme(scheme))
	require.NoError(t, igwapi.Install(scheme))
	require.NoError(t, igwapiv1alpha2.AddToScheme(scheme))
	builder := fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(funcs)
	if len(objects) > 0 {
		builder = builder.WithObjects(objects...)
	}
	return builder.Build()
}

func ownedByController(service *v1alpha2.LLMInferenceService) []metav1.OwnerReference {
	return []metav1.OwnerReference{*metav1.NewControllerRef(service, v1alpha2.LLMInferenceServiceGVK)}
}

func ownedDeployment(service *v1alpha2.LLMInferenceService, name string, selector *metav1.LabelSelector, templateLabels map[string]string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:            name,
			Namespace:       service.Namespace,
			OwnerReferences: ownedByController(service),
		},
		Spec: appsv1.DeploymentSpec{
			Selector: selector,
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: templateLabels}},
		},
	}
}

func ownedLeaderWorkerSet(service *v1alpha2.LLMInferenceService, name string) *lwsapi.LeaderWorkerSet {
	return &lwsapi.LeaderWorkerSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:            name,
			Namespace:       service.Namespace,
			OwnerReferences: ownedByController(service),
		},
	}
}

func ownedDisaggregatedSet(service *v1alpha2.LLMInferenceService, name string) *disaggregatedsetv1.DisaggregatedSet {
	return &disaggregatedsetv1.DisaggregatedSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:            name,
			Namespace:       service.Namespace,
			OwnerReferences: ownedByController(service),
		},
	}
}

func TestNetworkPolicySelectorInventoryClonesStoredDeploymentSelectors(t *testing.T) {
	service := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{
		Name: "clone-selector", Namespace: "team-a", UID: "clone-selector-uid",
	}}
	// A historical selector: the role key is no longer set the same way by
	// current templates, and the expression is user metadata the selector
	// picked up. Both must survive into the inventory verbatim.
	storedSelector := metav1.LabelSelector{
		MatchLabels: map[string]string{
			constants.KubernetesAppNameLabelKey:   service.Name,
			constants.KubernetesComponentLabelKey: constants.KServeComponentWorkload,
			constants.LLMDRoleLabelKey:            constants.LLMDRoleBoth,
		},
		MatchExpressions: []metav1.LabelSelectorRequirement{
			{Key: "tier", Operator: metav1.LabelSelectorOpIn, Values: []string{"gold", "silver"}},
		},
	}
	main := ownedDeployment(service, mainDeploymentName(service), storedSelector.DeepCopy(), map[string]string{
		// Incidental template labels are not part of the stored selector.
		"team.io/owner": "alice",
	})
	main.Spec.Replicas = ptr.To(int32(0))

	var readBack *appsv1.Deployment
	cl := networkPolicySelectorTestClientWithInterceptors(t, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			// Capture only the main Deployment read; the inventory also Gets the
			// other component names, which do not exist here.
			if deployment, ok := obj.(*appsv1.Deployment); ok && key.Name == mainDeploymentName(service) {
				readBack = deployment
			}
			return c.Get(ctx, key, obj, opts...)
		},
	}, main)
	r := &LLMISVCReconciler{Client: cl}

	inventory, err := r.networkPolicySelectors(t.Context(), service)
	require.NoError(t, err)
	entry, ok := inventory.entry(networkPolicyComponentMain)
	require.True(t, ok, "a stored Deployment at zero replicas must stay in the inventory")
	require.Equal(t, networkPolicyRoleWorkload, entry.Class)
	require.Equal(t, storedSelector, entry.Selector, "the complete stored selector, expressions included, must be cloned")
	require.NotContains(t, entry.Selector.MatchLabels, "team.io/owner", "incidental template labels must not leak into the selector")

	// The entry must not alias the object the client returned, so policy
	// builders can treat the selector as theirs without corrupting a cached
	// object behind the client.
	entry.Selector.MatchLabels["mutation-probe"] = "x"
	entry.Selector.MatchExpressions[0].Values[0] = "mutated"
	require.NotContains(t, readBack.Spec.Selector.MatchLabels, "mutation-probe")
	require.Equal(t, []string{"gold", "silver"}, readBack.Spec.Selector.MatchExpressions[0].Values)
}

func TestNetworkPolicySelectorInventoryIncludesAllStoredComponentsRegardlessOfTopology(t *testing.T) {
	// A single-node service with no prefill and no router, but with stored
	// components of every other topology still present: the inventory keeps
	// covering them until the reconciliation that deletes them completes.
	service := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{
		Name: "mixed-topology", Namespace: "team-a", UID: "mixed-topology-uid",
	}}
	selectors := map[string]*metav1.LabelSelector{
		networkPolicyComponentMain: {MatchLabels: map[string]string{
			constants.KubernetesAppNameLabelKey: service.Name, constants.KServeComponentLabelKey: constants.KServeComponentWorkload, constants.LLMDRoleLabelKey: constants.LLMDRoleDecode,
		}},
		networkPolicyComponentPrefill: {MatchLabels: map[string]string{
			constants.KubernetesAppNameLabelKey: service.Name, constants.KServeComponentLabelKey: constants.KServeComponentWorkload, constants.LLMDRoleLabelKey: constants.LLMDRolePrefill,
		}},
		networkPolicyComponentScheduler: {MatchLabels: map[string]string{
			constants.KubernetesAppNameLabelKey: service.Name, "kserve.io/component": "router-scheduler",
		}},
		networkPolicyComponentTokenizer: {MatchLabels: map[string]string{
			constants.KubernetesAppNameLabelKey: service.Name, "kserve.io/component": "tokenizer",
		}},
	}
	objects := []client.Object{
		ownedDeployment(service, mainDeploymentName(service), selectors[networkPolicyComponentMain], nil),
		ownedDeployment(service, prefillDeploymentName(service), selectors[networkPolicyComponentPrefill], nil),
		ownedDeployment(service, schedulerDeploymentName(service), selectors[networkPolicyComponentScheduler], nil),
		ownedDeployment(service, tokenizerDeploymentName(service), selectors[networkPolicyComponentTokenizer], nil),
		ownedLeaderWorkerSet(service, mainLWSName(service)),
		ownedLeaderWorkerSet(service, prefillLWSName(service)),
		ownedDisaggregatedSet(service, disaggregatedSetName(service)),
	}
	cl := networkPolicySelectorTestClient(t, objects...)
	r := &LLMISVCReconciler{Client: cl}

	inventory, err := r.networkPolicySelectors(t.Context(), service)
	require.NoError(t, err)

	wantOrder := []string{
		networkPolicyComponentMain, networkPolicyComponentPrefill,
		networkPolicyComponentScheduler, networkPolicyComponentTokenizer,
		networkPolicyComponentMainMN, networkPolicyComponentPrefillMN,
		networkPolicyComponentPD,
	}
	require.Len(t, inventory, len(wantOrder))
	for i, component := range wantOrder {
		require.Equal(t, component, inventory[i].Component, "inventory entry %d", i)
	}
	for component, selector := range selectors {
		entry, ok := inventory.entry(component)
		require.True(t, ok, "component %s", component)
		require.Equal(t, *selector, entry.Selector, "component %s", component)
	}
	for _, tt := range []struct {
		component string
		selector  metav1.LabelSelector
	}{
		{networkPolicyComponentMainMN, metav1.LabelSelector{MatchLabels: map[string]string{
			lwsapi.SetNameLabelKey: mainLWSName(service),
		}}},
		{networkPolicyComponentPrefillMN, metav1.LabelSelector{MatchLabels: map[string]string{
			lwsapi.SetNameLabelKey: prefillLWSName(service),
		}}},
		{networkPolicyComponentPD, metav1.LabelSelector{MatchLabels: map[string]string{
			disaggregatedsetv1.SetNameLabelKey: disaggregatedSetName(service),
		}}},
	} {
		entry, ok := inventory.entry(tt.component)
		require.True(t, ok, "component %s", tt.component)
		require.Equal(t, networkPolicyRoleWorkload, entry.Class, "component %s", tt.component)
		require.Equal(t, tt.selector, entry.Selector, "component %s", tt.component)
	}
	require.Len(t, inventory.ofClass(networkPolicyRoleWorkload), 5)
	require.Len(t, inventory.ofClass(networkPolicyRoleScheduler), 1)
	require.Len(t, inventory.ofClass(networkPolicyRoleTokenizer), 1)
}

func TestNetworkPolicySelectorInventoryMissingComponentsYieldNoEntry(t *testing.T) {
	service := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{
		Name: "sparse", Namespace: "team-a", UID: "sparse-uid",
	}}

	t.Run("nothing stored", func(t *testing.T) {
		r := &LLMISVCReconciler{Client: networkPolicySelectorTestClient(t)}
		inventory, err := r.networkPolicySelectors(t.Context(), service)
		require.NoError(t, err)
		require.Empty(t, inventory)
	})

	t.Run("only the scheduler is stored", func(t *testing.T) {
		scheduler := ownedDeployment(service, schedulerDeploymentName(service), &metav1.LabelSelector{
			MatchLabels: map[string]string{constants.KubernetesAppNameLabelKey: service.Name},
		}, nil)
		r := &LLMISVCReconciler{Client: networkPolicySelectorTestClient(t, scheduler)}
		inventory, err := r.networkPolicySelectors(t.Context(), service)
		require.NoError(t, err)
		require.Len(t, inventory, 1)
		require.Equal(t, networkPolicyComponentScheduler, inventory[0].Component)
	})
}

func TestNetworkPolicySelectorInventoryForeignComponentsError(t *testing.T) {
	service := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{
		Name: "owned", Namespace: "team-a", UID: "owned-uid",
	}}
	foreignService := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{
		Name: "squatter", Namespace: "team-a", UID: "squatter-uid",
	}}
	foreignRef := []metav1.OwnerReference{*metav1.NewControllerRef(foreignService, v1alpha2.LLMInferenceServiceGVK)}

	main := ownedDeployment(service, mainDeploymentName(service), &metav1.LabelSelector{
		MatchLabels: map[string]string{constants.KubernetesAppNameLabelKey: service.Name},
	}, nil)
	main.OwnerReferences = foreignRef
	mainLWS := ownedLeaderWorkerSet(service, mainLWSName(service))
	mainLWS.OwnerReferences = foreignRef
	ds := ownedDisaggregatedSet(service, disaggregatedSetName(service))
	ds.OwnerReferences = foreignRef

	for _, tt := range []struct {
		name    string
		foreign client.Object
	}{
		{name: "Deployment", foreign: main},
		{name: "LeaderWorkerSet", foreign: mainLWS},
		{name: "DisaggregatedSet", foreign: ds},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := &LLMISVCReconciler{Client: networkPolicySelectorTestClient(t, tt.foreign)}
			inventory, err := r.networkPolicySelectors(t.Context(), service)
			require.Error(t, err, "a foreign resource must not be silently dropped from the inventory")
			require.Empty(t, inventory)
			require.ErrorIs(t, err, errNetworkPolicyOwnershipConflict)
			require.Contains(t, err.Error(), "LLMInferenceService team-a/owned")
		})
	}
}

func TestNetworkPolicySelectorInventoryInvalidStoredSelectorsError(t *testing.T) {
	service := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{
		Name: "bad-selector", Namespace: "team-a", UID: "bad-selector-uid",
	}}
	for _, tt := range []struct {
		name     string
		selector *metav1.LabelSelector
		message  string
	}{
		{
			name:     "empty selector",
			selector: &metav1.LabelSelector{},
			message:  "empty Pod selector",
		},
		{
			name: "invalid expression operator",
			selector: &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{
				{Key: "tier", Operator: "Typo", Values: []string{"gold"}},
			}},
			message: "invalid Pod selector",
		},
		{
			name: "In expression without values",
			selector: &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{
				{Key: "tier", Operator: metav1.LabelSelectorOpIn},
			}},
			message: "invalid Pod selector",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			main := ownedDeployment(service, mainDeploymentName(service), tt.selector, nil)
			r := &LLMISVCReconciler{Client: networkPolicySelectorTestClient(t, main)}
			inventory, err := r.networkPolicySelectors(t.Context(), service)
			require.Error(t, err, "an invalid stored selector must abort the inventory")
			require.Empty(t, inventory)
			require.Contains(t, err.Error(), tt.message)
			require.Contains(t, err.Error(), mainDeploymentName(service))
		})
	}
}

func TestNetworkPolicySelectorInventoryWithOptionalAPIsAbsent(t *testing.T) {
	service := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{
		Name: "no-optional-crds", Namespace: "team-a", UID: "no-optional-crds-uid",
	}}
	main := ownedDeployment(service, mainDeploymentName(service), &metav1.LabelSelector{
		MatchLabels: map[string]string{constants.KubernetesAppNameLabelKey: service.Name},
	}, nil)
	cl := networkPolicySelectorTestClientWithInterceptors(t, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			switch obj.(type) {
			case *lwsapi.LeaderWorkerSet:
				return errLWSNoMatch
			case *disaggregatedsetv1.DisaggregatedSet:
				return errDisaggregatedSetNoMatch
			}
			return c.Get(ctx, key, obj, opts...)
		},
	}, main)
	r := &LLMISVCReconciler{Client: cl}

	inventory, err := r.networkPolicySelectors(t.Context(), service)
	require.NoError(t, err, "absent optional APIs must not become cluster prerequisites")
	require.Len(t, inventory, 1)
	require.Equal(t, networkPolicyComponentMain, inventory[0].Component)
}

func TestNetworkPolicySelectorInventoryReadFailuresPropagate(t *testing.T) {
	service := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{
		Name: "read-failure", Namespace: "team-a", UID: "read-failure-uid",
	}}
	for _, tt := range []struct {
		name  string
		match func(client.Object) bool
	}{
		{name: "Deployment", match: func(obj client.Object) bool {
			_, ok := obj.(*appsv1.Deployment)
			return ok
		}},
		{name: "LeaderWorkerSet", match: func(obj client.Object) bool {
			_, ok := obj.(*lwsapi.LeaderWorkerSet)
			return ok
		}},
		{name: "DisaggregatedSet", match: func(obj client.Object) bool {
			_, ok := obj.(*disaggregatedsetv1.DisaggregatedSet)
			return ok
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			readErr := errors.New("transient read failure")
			cl := networkPolicySelectorTestClientWithInterceptors(t, interceptor.Funcs{
				Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if tt.match(obj) {
						return readErr
					}
					return c.Get(ctx, key, obj, opts...)
				},
			})
			r := &LLMISVCReconciler{Client: cl}
			inventory, err := r.networkPolicySelectors(t.Context(), service)
			require.ErrorIs(t, err, readErr, "a read failure must abort the inventory, not masquerade as a missing component")
			require.Empty(t, inventory)
		})
	}
}

func TestNetworkPolicySelectorInventoryDoesNotListPods(t *testing.T) {
	service := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{
		Name: "no-pod-listing", Namespace: "team-a", UID: "no-pod-listing-uid",
	}}
	main := ownedDeployment(service, mainDeploymentName(service), &metav1.LabelSelector{
		MatchLabels: map[string]string{constants.KubernetesAppNameLabelKey: service.Name},
	}, nil)
	podListErr := errors.New("the selector inventory must not list Pods")
	cl := networkPolicySelectorTestClientWithInterceptors(t, interceptor.Funcs{
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if _, ok := list.(*corev1.PodList); ok {
				return podListErr
			}
			return c.List(ctx, list, opts...)
		},
	}, main)
	r := &LLMISVCReconciler{Client: cl}

	inventory, err := r.networkPolicySelectors(t.Context(), service)
	require.NoError(t, err)
	require.Len(t, inventory, 1)
}

func TestNetworkPolicySelectorInventoryKeepsDistinctServicesAndComponentsIsolated(t *testing.T) {
	modelURI, err := apis.ParseURL("hf://facebook/opt-125m")
	require.NoError(t, err)
	newService := func(name string) *v1alpha2.LLMInferenceService {
		return &v1alpha2.LLMInferenceService{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "isolation", UID: types.UID(name + "-uid")},
			Spec: v1alpha2.LLMInferenceServiceSpec{
				Model:              v1alpha2.LLMModelSpec{URI: *modelURI},
				WorkloadSpec:       v1alpha2.WorkloadSpec{Template: &corev1.PodSpec{}},
				StorageInitializer: &v1alpha2.StorageInitializerSpec{Enabled: ptr.To(false)},
				Prefill:            &v1alpha2.WorkloadSpec{},
				Router:             &v1alpha2.RouterSpec{Scheduler: &v1alpha2.SchedulerSpec{}},
			},
		}
	}
	first, second := newService("alpha"), newService("bravo")
	r := &LLMISVCReconciler{Client: networkPolicySelectorTestClient(t)}

	// Build the stored Deployments with the real builders, so the selectors are
	// the ones the controller actually stores.
	stored := map[string]map[string]*appsv1.Deployment{}
	for _, service := range []*v1alpha2.LLMInferenceService{first, second} {
		main, err := r.expectedSingleNodeMainDeployment(t.Context(), service, &Config{})
		require.NoError(t, err)
		prefill, err := r.expectedPrefillMainDeployment(t.Context(), service, &Config{})
		require.NoError(t, err)
		scheduler, err := r.expectedSchedulerDeployment(t.Context(), service)
		require.NoError(t, err)
		tokenizer, err := r.expectedTokenizerDeployment(t.Context(), service)
		require.NoError(t, err)
		stored[service.Name] = map[string]*appsv1.Deployment{
			networkPolicyComponentMain:      main,
			networkPolicyComponentPrefill:   prefill,
			networkPolicyComponentScheduler: scheduler,
			networkPolicyComponentTokenizer: tokenizer,
		}
		for _, deployment := range stored[service.Name] {
			require.NoError(t, r.Create(t.Context(), deployment))
		}
	}

	for _, service := range []*v1alpha2.LLMInferenceService{first, second} {
		inventory, err := r.networkPolicySelectors(t.Context(), service)
		require.NoError(t, err)
		require.Len(t, inventory, 4)
		for _, entry := range inventory {
			selector, err := metav1.LabelSelectorAsSelector(&entry.Selector)
			require.NoError(t, err)

			own := stored[service.Name][entry.Component]
			require.True(t, selector.Matches(labels.Set(own.Spec.Template.Labels)),
				"%s/%s selector does not match its own Pod template", service.Name, entry.Component)

			for otherName, components := range stored {
				if otherName == service.Name {
					continue
				}
				for component, deployment := range components {
					require.False(t, selector.Matches(labels.Set(deployment.Spec.Template.Labels)),
						"%s/%s selector matches %s/%s Pods", service.Name, entry.Component, otherName, component)
				}
			}
			for component, deployment := range stored[service.Name] {
				if component == entry.Component {
					continue
				}
				require.False(t, selector.Matches(labels.Set(deployment.Spec.Template.Labels)),
					"%s/%s selector matches the %s Pods of the same service", service.Name, entry.Component, component)
			}
		}
	}
}

// TestNetworkPolicySelectorInventoryMembershipSurvivesRevisions covers the
// set-based membership selectors of the inventory: the LWS and DisaggregatedSet
// controllers stamp their membership label on every Pod they generate —
// across revisions, roles, and slices — so the entries built from the stored
// sets keep covering leaders, compute-only workers, and every role of a
// DisaggregatedSet without depending on revision hashes, an InferencePool
// serving marker, or llm-d role labels, and without matching another set's
// Pods.
func TestNetworkPolicySelectorInventoryMembershipSurvivesRevisions(t *testing.T) {
	service := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{
		Name: "membership", Namespace: "team-a", UID: "membership-uid",
	}}
	cl := networkPolicySelectorTestClient(t,
		ownedLeaderWorkerSet(service, mainLWSName(service)),
		ownedDisaggregatedSet(service, disaggregatedSetName(service)),
	)
	r := &LLMISVCReconciler{Client: cl}

	inventory, err := r.networkPolicySelectors(t.Context(), service)
	require.NoError(t, err)
	lwsEntry, ok := inventory.entry(networkPolicyComponentMainMN)
	require.True(t, ok, "the stored LeaderWorkerSet must produce an inventory entry")
	lwsSelector, err := metav1.LabelSelectorAsSelector(&lwsEntry.Selector)
	require.NoError(t, err)
	dsEntry, ok := inventory.entry(networkPolicyComponentPD)
	require.True(t, ok, "the stored DisaggregatedSet must produce an inventory entry")
	dsSelector, err := metav1.LabelSelectorAsSelector(&dsEntry.Selector)
	require.NoError(t, err)

	// Pod label sets as the LWS controller stamps them: the membership label
	// on every Pod, the template-revision hash that changes when a new
	// revision rolls out, and per-Pod index labels. Compute-only workers
	// deliberately carry no InferencePool serving marker and no llm-d role
	// label; old and new revisions stay online together during a rollout.
	lwsPod := func(revision string, workerIndex string) labels.Set {
		return labels.Set{
			lwsapi.SetNameLabelKey:     mainLWSName(service),
			lwsapi.RevisionKey:         revision,
			lwsapi.WorkerIndexLabelKey: workerIndex,
		}
	}
	for _, tt := range []struct {
		name string
		pod  labels.Set
	}{{
		name: "leader pod of revision 0", pod: lwsPod("hash-0", "0"),
	}, {
		name: "compute-only worker pod of revision 0", pod: lwsPod("hash-0", "1"),
	}, {
		name: "leader pod of revision 1", pod: lwsPod("hash-1", "0"),
	}, {
		name: "compute-only worker pod of revision 1", pod: lwsPod("hash-1", "1"),
	}} {
		t.Run("lws/"+tt.name, func(t *testing.T) {
			require.True(t, lwsSelector.Matches(tt.pod),
				"the main-mn selector must keep covering the Pod after the revision rolled: %v", tt.pod)
			require.False(t, dsSelector.Matches(tt.pod), "the pd selector must not match LWS Pods")
		})
	}

	// Pod label sets as the DisaggregatedSet controller stamps them on the
	// LeaderWorkerSets it generates per role: membership, role, subrole, and
	// the per-role revision hash.
	dsPod := func(role, revision string) labels.Set {
		return labels.Set{
			disaggregatedsetv1.SetNameLabelKey:  disaggregatedSetName(service),
			disaggregatedsetv1.RoleLabelKey:     role,
			disaggregatedsetv1.SubRoleLabelKey:  "default",
			disaggregatedsetv1.RevisionLabelKey: revision,
		}
	}
	for _, tt := range []struct {
		name string
		pod  labels.Set
	}{{
		name: "decode pod of revision a", pod: dsPod("decode", "aaa"),
	}, {
		name: "prefill pod of revision a", pod: dsPod("prefill", "aaa"),
	}, {
		name: "decode pod of revision b", pod: dsPod("decode", "bbb"),
	}, {
		name: "prefill pod of revision b", pod: dsPod("prefill", "bbb"),
	}} {
		t.Run("ds/"+tt.name, func(t *testing.T) {
			require.True(t, dsSelector.Matches(tt.pod),
				"the pd selector must keep covering the Pod across roles and revisions: %v", tt.pod)
			require.False(t, lwsSelector.Matches(tt.pod), "the main-mn selector must not match DisaggregatedSet Pods")
		})
	}

	// Neither membership selector may reach another set's Pods: an ordinary
	// distinct service keeps its own coverage.
	otherService := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{
		Name: "other", Namespace: "team-a", UID: "other-uid",
	}}
	for _, tt := range []struct {
		name     string
		pod      labels.Set
		selector func() labels.Selector
	}{{
		name: "another service's LWS pod",
		pod: labels.Set{
			lwsapi.SetNameLabelKey:     mainLWSName(otherService),
			lwsapi.RevisionKey:         "hash-0",
			lwsapi.WorkerIndexLabelKey: "0",
		},
		selector: func() labels.Selector { return lwsSelector },
	}, {
		name: "another service's DisaggregatedSet pod",
		pod: labels.Set{
			disaggregatedsetv1.SetNameLabelKey:  disaggregatedSetName(otherService),
			disaggregatedsetv1.RoleLabelKey:     "decode",
			disaggregatedsetv1.RevisionLabelKey: "aaa",
		},
		selector: func() labels.Selector { return dsSelector },
	}} {
		t.Run(tt.name, func(t *testing.T) {
			require.False(t, tt.selector().Matches(tt.pod),
				"the membership selector must not match another service's Pods: %v", tt.pod)
		})
	}
}

func TestManagedSchedulerNetworkPolicySelectorDistinguishesRequiredFromMissing(t *testing.T) {
	newManagedSchedulerService := func(name string) *v1alpha2.LLMInferenceService {
		return &v1alpha2.LLMInferenceService{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "team-a", UID: types.UID(name + "-uid")},
			Spec: v1alpha2.LLMInferenceServiceSpec{
				Router: &v1alpha2.RouterSpec{Scheduler: &v1alpha2.SchedulerSpec{
					Template: &corev1.PodSpec{},
				}},
			},
		}
	}
	schedulerSelector := func(service *v1alpha2.LLMInferenceService) *metav1.LabelSelector {
		return &metav1.LabelSelector{MatchLabels: map[string]string{
			constants.KubernetesAppNameLabelKey:   service.Name,
			constants.KubernetesComponentLabelKey: constants.LLMComponentRouterScheduler,
		}}
	}

	t.Run("no managed scheduler resolves to nil", func(t *testing.T) {
		externalPool := &v1alpha2.LLMInferenceService{
			ObjectMeta: metav1.ObjectMeta{Name: "external-pool", Namespace: "team-a", UID: "external-pool-uid"},
			Spec: v1alpha2.LLMInferenceServiceSpec{Router: &v1alpha2.RouterSpec{Scheduler: &v1alpha2.SchedulerSpec{
				Pool: &v1alpha2.InferencePoolSpec{Ref: &corev1.LocalObjectReference{Name: "external-pool"}},
			}}},
		}
		noRouter := &v1alpha2.LLMInferenceService{
			ObjectMeta: metav1.ObjectMeta{Name: "no-router", Namespace: "team-a", UID: "no-router-uid"},
		}
		r := &LLMISVCReconciler{Client: networkPolicySelectorTestClient(t)}
		for _, service := range []*v1alpha2.LLMInferenceService{externalPool, noRouter} {
			selector, err := r.managedSchedulerNetworkPolicySelector(t.Context(), service)
			require.NoError(t, err)
			require.Nil(t, selector, "%s must resolve to no scheduler peer", service.Name)
		}
	})

	t.Run("stopped service resolves to no peer without error", func(t *testing.T) {
		// A force-stopped service keeps its managed scheduler configured while
		// the workload reconcilers delete the scheduler Deployment: the absence
		// is the stopped state, not an unresolved scheduler, so it must yield no
		// peer instead of a retryable error (an error loop) or a widened source.
		service := newManagedSchedulerService("stopped")
		service.Annotations = map[string]string{constants.StopAnnotationKey: "true"}
		r := &LLMISVCReconciler{Client: networkPolicySelectorTestClient(t)}

		selector, err := r.managedSchedulerNetworkPolicySelector(t.Context(), service)
		require.NoError(t, err, "a force-stopped managed scheduler must not fail the reconcile")
		require.Nil(t, selector, "a stopped scheduler has no Pods to admit and must yield no peer")
	})

	t.Run("stored scheduler selector is returned in full", func(t *testing.T) {
		service := newManagedSchedulerService("resolved")
		scheduler := ownedDeployment(service, schedulerDeploymentName(service), schedulerSelector(service), nil)
		r := &LLMISVCReconciler{Client: networkPolicySelectorTestClient(t, scheduler)}
		selector, err := r.managedSchedulerNetworkPolicySelector(t.Context(), service)
		require.NoError(t, err)
		require.NotNil(t, selector)
		require.Equal(t, *schedulerSelector(service), *selector)
	})

	t.Run("required scheduler that is not stored errors while the inventory stays silent", func(t *testing.T) {
		service := newManagedSchedulerService("unresolved")
		r := &LLMISVCReconciler{Client: networkPolicySelectorTestClient(t)}

		_, err := r.managedSchedulerNetworkPolicySelector(t.Context(), service)
		require.Error(t, err, "a required managed scheduler must trigger a retry, not an empty peer")
		require.Contains(t, err.Error(), schedulerDeploymentName(service))
		require.NotErrorIs(t, err, errNetworkPolicyOwnershipConflict)

		// The same missing component is a non-event for the inventory: the
		// scheduler entry simply does not exist there.
		inventory, invErr := r.networkPolicySelectors(t.Context(), service)
		require.NoError(t, invErr)
		_, ok := inventory.entry(networkPolicyComponentScheduler)
		require.False(t, ok)
	})

	t.Run("required scheduler that is foreign errors", func(t *testing.T) {
		service := newManagedSchedulerService("foreign-scheduler")
		foreignService := &v1alpha2.LLMInferenceService{
			ObjectMeta: metav1.ObjectMeta{Name: "squatter", Namespace: "team-a", UID: "squatter-uid"},
		}
		scheduler := ownedDeployment(service, schedulerDeploymentName(service), schedulerSelector(service), nil)
		scheduler.OwnerReferences = []metav1.OwnerReference{
			*metav1.NewControllerRef(foreignService, v1alpha2.LLMInferenceServiceGVK),
		}
		r := &LLMISVCReconciler{Client: networkPolicySelectorTestClient(t, scheduler)}
		_, err := r.managedSchedulerNetworkPolicySelector(t.Context(), service)
		require.ErrorIs(t, err, errNetworkPolicyOwnershipConflict)
	})

	t.Run("sibling scheduler resolves through the same path", func(t *testing.T) {
		caller := newManagedSchedulerService("caller-svc")
		sibling := newManagedSchedulerService("sibling-svc")
		// Both services have their scheduler stored; the helper must return the
		// selector of the service it is asked about, not the caller's.
		objects := []client.Object{
			ownedDeployment(caller, schedulerDeploymentName(caller), schedulerSelector(caller), nil),
			ownedDeployment(sibling, schedulerDeploymentName(sibling), schedulerSelector(sibling), nil),
		}
		r := &LLMISVCReconciler{Client: networkPolicySelectorTestClient(t, objects...)}
		selector, err := r.managedSchedulerNetworkPolicySelector(t.Context(), sibling)
		require.NoError(t, err)
		require.NotNil(t, selector)
		require.Equal(t, *schedulerSelector(sibling), *selector)
		require.Equal(t, sibling.Name, selector.MatchLabels[constants.KubernetesAppNameLabelKey],
			"the sibling's scheduler selector must be resolved, not the caller's")
	})
}
