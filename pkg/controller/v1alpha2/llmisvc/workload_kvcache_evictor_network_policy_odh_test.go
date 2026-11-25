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
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
	"github.com/kserve/kserve/pkg/constants"
)

func pvcEvictorPolicyFixture(claim, path string) (*v1alpha2.LLMInferenceService, *appsv1.Deployment) {
	svc := &v1alpha2.LLMInferenceService{
		ObjectMeta: metav1.ObjectMeta{Name: "model", Namespace: "team-a"},
		Spec: v1alpha2.LLMInferenceServiceSpec{WorkloadSpec: v1alpha2.WorkloadSpec{
			Template: &corev1.PodSpec{Containers: []corev1.Container{{Name: pvcEvictorTemplateName, Image: "example.com/evictor:test"}}},
			KVCacheOffloading: &v1alpha2.KVCacheOffloadingSpec{Secondary: []v1alpha2.SecondaryTierSpec{{FileSystem: &v1alpha2.FileSystemTierSpec{
				PVC: &v1alpha2.PVCTierSpec{Ref: &v1alpha2.PVCRefTierSpec{Name: claim, Path: path}},
			}}}},
		}},
	}
	dep := expectedPVCEvictorDeployment(svc, collectPVCEvictorTargets(svc)[0])
	dep.UID = types.UID("uid-" + pvcEvictorKey(claim, normalizePVCEvictorPath(path)))
	return svc, dep
}

func TestPVCEvictorNetworkPolicySelectorAndIdentity(t *testing.T) {
	_, dep := pvcEvictorPolicyFixture("shared", "cache")
	dep.Spec.Selector = dep.Spec.Selector.DeepCopy()
	dep.Spec.Selector.MatchExpressions = []metav1.LabelSelectorRequirement{{Key: "historical", Operator: metav1.LabelSelectorOpDoesNotExist}}
	before := dep.DeepCopy()
	policy, err := expectedPVCEvictorNetworkPolicy(dep)
	require.NoError(t, err)
	require.Equal(t, *dep.Spec.Selector, policy.Spec.PodSelector)
	require.ElementsMatch(t, []netv1.PolicyType{netv1.PolicyTypeIngress, netv1.PolicyTypeEgress}, policy.Spec.PolicyTypes)
	require.Empty(t, policy.Spec.Ingress)
	require.Empty(t, policy.Spec.Egress)
	require.True(t, metav1.IsControlledBy(policy, dep))
	require.Equal(t, "apps/v1", policy.OwnerReferences[0].APIVersion)
	require.Equal(t, "Deployment", policy.OwnerReferences[0].Kind)
	require.Len(t, policy.Labels, 2)
	require.Equal(t, dep.Labels[pvcEvictorKeyLabel], policy.Labels[pvcEvictorKeyLabel])
	require.Equal(t, before, dep)
	policy.Spec.PodSelector.MatchLabels[pvcEvictorLabel] = "changed"
	require.Equal(t, before, dep, "selector must be a deep copy")

	tests := map[string]func(*appsv1.Deployment){
		"missing UID":           func(d *appsv1.Deployment) { d.UID = "" },
		"missing marker":        func(d *appsv1.Deployment) { delete(d.Labels, pvcEvictorLabel) },
		"wrong key":             func(d *appsv1.Deployment) { d.Labels[pvcEvictorKeyLabel] = "foreign" },
		"missing PVC":           func(d *appsv1.Deployment) { delete(d.Annotations, pvcEvictorPVCName) },
		"missing path":          func(d *appsv1.Deployment) { delete(d.Annotations, pvcEvictorPVCPath) },
		"wrong service account": func(d *appsv1.Deployment) { d.Spec.Template.Spec.ServiceAccountName = "default" },
		"nil selector":          func(d *appsv1.Deployment) { d.Spec.Selector = nil },
		"empty selector":        func(d *appsv1.Deployment) { d.Spec.Selector = &metav1.LabelSelector{} },
		"broad selector": func(d *appsv1.Deployment) {
			d.Spec.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{pvcEvictorLabel: "true"}}
		},
		"invalid selector": func(d *appsv1.Deployment) {
			d.Spec.Selector.MatchExpressions = []metav1.LabelSelectorRequirement{{Key: "bad", Operator: "Invalid"}}
		},
		"template mismatch": func(d *appsv1.Deployment) { d.Spec.Template.Labels = nil },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			d := before.DeepCopy()
			mutate(d)
			p, err := expectedPVCEvictorNetworkPolicy(d)
			require.Error(t, err)
			require.Nil(t, p)
		})
	}
	require.NotEqual(t, pvcEvictorNetworkPolicyName(strings.Repeat("a", 70)), pvcEvictorNetworkPolicyName(strings.Repeat("a", 69)+"b"))
	require.LessOrEqual(t, len(pvcEvictorNetworkPolicyName(strings.Repeat("a", 70))), 63)
}

func TestPVCEvictorNetworkPolicyReconcileRepairAndSharing(t *testing.T) {
	svc, dep := pvcEvictorPolicyFixture("shared", "cache")
	peer := svc.DeepCopy()
	peer.Name = "peer"
	_, otherPath := pvcEvictorPolicyFixture("shared", "other")
	_, otherPVC := pvcEvictorPolicyFixture("another", "cache")
	original := dep.DeepCopy()
	c := newNetworkPolicyTestClient(t, dep, otherPath, otherPVC)
	r := &LLMISVCReconciler{Client: c, EventRecorder: record.NewFakeRecorder(100)}
	reconcile := func(s *v1alpha2.LLMInferenceService) {
		require.NoError(t, r.reconcilePVCEvictorPlatformResources(t.Context(), s))
	}
	reconcile(svc)
	reconcile(peer)
	policies := &netv1.NetworkPolicyList{}
	require.NoError(t, c.List(t.Context(), policies))
	require.Len(t, policies.Items, 3)
	key := types.NamespacedName{Namespace: dep.Namespace, Name: pvcEvictorNetworkPolicyName(dep.Name)}
	policy := &netv1.NetworkPolicy{}
	require.NoError(t, c.Get(t.Context(), key, policy))
	version := policy.ResourceVersion
	reconcile(svc)
	require.NoError(t, c.Get(t.Context(), key, policy))
	require.Equal(t, version, policy.ResourceVersion, "no-op reconciliation must not write")
	policy.Spec.Egress = []netv1.NetworkPolicyEgressRule{{}}
	delete(policy.Labels, pvcEvictorLabel)
	require.NoError(t, c.Update(t.Context(), policy))
	reconcile(peer)
	require.NoError(t, c.Get(t.Context(), key, policy))
	require.Empty(t, policy.Spec.Egress)
	require.Equal(t, "true", policy.Labels[pvcEvictorLabel])
	require.NoError(t, c.Delete(t.Context(), policy))
	reconcile(peer)
	require.NoError(t, c.Get(t.Context(), key, policy))
	require.True(t, metav1.IsControlledBy(policy, dep))
	// A stopped/removed consumer must not prune a policy for the stored shared Deployment.
	svc.Annotations = map[string]string{constants.StopAnnotationKey: "true"}
	svc.Spec.KVCacheOffloading = nil
	reconcile(svc)
	require.NoError(t, c.Get(t.Context(), key, policy))
	actual := &appsv1.Deployment{}
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(dep), actual))
	require.Equal(t, original.Spec, actual.Spec)
	require.Equal(t, original.Labels, actual.Labels)
}

func TestPVCEvictorNetworkPolicyOwnershipConflictAndRecovery(t *testing.T) {
	for _, owner := range []string{"ownerless", "foreign", "stale"} {
		t.Run(owner, func(t *testing.T) {
			svc, dep := pvcEvictorPolicyFixture("shared", "cache")
			policy, err := expectedPVCEvictorNetworkPolicy(dep)
			require.NoError(t, err)
			if owner == "ownerless" {
				policy.OwnerReferences = nil
			} else {
				policy.OwnerReferences[0].UID = types.UID(owner)
			}
			c := newNetworkPolicyTestClient(t, dep, policy)
			r := &LLMISVCReconciler{Client: c, EventRecorder: record.NewFakeRecorder(100)}
			require.Error(t, r.reconcilePVCEvictorPlatformResources(t.Context(), svc))
			got := &netv1.NetworkPolicy{}
			require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(policy), got))
			require.Equal(t, policy.OwnerReferences, got.OwnerReferences)
			// Simulate the external removal/GC of the conflicting old policy; never adopt it.
			require.NoError(t, c.Delete(t.Context(), got))
			require.NoError(t, r.reconcilePVCEvictorPlatformResources(t.Context(), svc))
			require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(policy), got))
			require.True(t, metav1.IsControlledBy(got, dep))
		})
	}
}

func TestPVCEvictorNetworkPolicyFailuresDoNotSkipOtherTargets(t *testing.T) {
	injected := errors.New("injected policy API failure")
	for _, verb := range []string{"list", "get", "create", "update"} {
		t.Run(verb, func(t *testing.T) {
			svc, dep := pvcEvictorPolicyFixture("shared", "cache")
			_, other := pvcEvictorPolicyFixture("other", "cache")
			policy, err := expectedPVCEvictorNetworkPolicy(dep)
			require.NoError(t, err)
			policy.Spec.Ingress = []netv1.NetworkPolicyIngressRule{{}}
			funcs := interceptor.Funcs{}
			switch verb {
			case "list":
				funcs.List = func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
					return injected
				}
			case "get":
				funcs.Get = func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if key.Name == policy.Name {
						return injected
					}
					return c.Get(ctx, key, obj, opts...)
				}
			case "create":
				funcs.Create = func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
					if obj.GetName() == policy.Name {
						return injected
					}
					return c.Create(ctx, obj, opts...)
				}
			case "update":
				funcs.Update = func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
					if obj.GetName() == policy.Name {
						return apierrors.NewConflict(netv1.Resource("networkpolicies"), obj.GetName(), injected)
					}
					return c.Update(ctx, obj, opts...)
				}
			}
			objects := []client.Object{dep, other}
			if verb == "update" {
				objects = append(objects, policy)
			}
			c := newNetworkPolicyTestClientWithInterceptor(t, funcs, objects...)
			r := &LLMISVCReconciler{Client: c, EventRecorder: record.NewFakeRecorder(100)}
			require.ErrorContains(t, r.reconcilePVCEvictorPlatformResources(t.Context(), svc), injected.Error())
			condition := svc.Status.GetCondition(v1alpha2.PVCEvictorReady)
			require.NotNil(t, condition)
			require.Equal(t, corev1.ConditionFalse, condition.Status)
			if verb != "list" {
				require.NoError(t, c.Get(t.Context(), types.NamespacedName{Namespace: other.Namespace, Name: pvcEvictorNetworkPolicyName(other.Name)}, &netv1.NetworkPolicy{}))
			}
			peer := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{Name: "unrelated", Namespace: svc.Namespace}}
			require.Error(t, r.reconcilePVCEvictorPlatformResources(t.Context(), peer))
			require.Nil(t, peer.Status.GetCondition(v1alpha2.PVCEvictorReady))
		})
	}
}

func TestPVCEvictorNetworkPolicyDisabledAndTerminating(t *testing.T) {
	svc, dep := pvcEvictorPolicyFixture("shared", "cache")
	existing, err := expectedPVCEvictorNetworkPolicy(dep)
	require.NoError(t, err)
	existing.Spec.Egress = []netv1.NetworkPolicyEgressRule{{}}
	c := newNetworkPolicyTestClient(t, dep, existing)
	r := &LLMISVCReconciler{Client: c, EventRecorder: record.NewFakeRecorder(100)}
	previous := networkPolicyDisabled
	networkPolicyDisabled = true
	t.Cleanup(func() { networkPolicyDisabled = previous })
	require.NoError(t, r.reconcilePVCEvictorPlatformResources(t.Context(), svc))
	got := &netv1.NetworkPolicy{}
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(existing), got))
	require.Len(t, got.Spec.Egress, 1, "disable is not repair or purge")
	require.NoError(t, c.Delete(t.Context(), got))
	require.NoError(t, r.reconcilePVCEvictorPlatformResources(t.Context(), svc))
	require.True(t, apierrors.IsNotFound(c.Get(t.Context(), client.ObjectKeyFromObject(existing), got)))
	networkPolicyDisabled = false
	dep.DeletionTimestamp = ptr.To(metav1.Now())
	dep.Finalizers = []string{metav1.FinalizerDeleteDependents}
	c = newNetworkPolicyTestClient(t, dep)
	r.Client = c
	require.NoError(t, r.reconcilePVCEvictorPlatformResources(t.Context(), svc))
	require.True(t, apierrors.IsNotFound(c.Get(t.Context(), client.ObjectKeyFromObject(existing), got)))
}

func TestPVCEvictorNetworkPolicyIndependentOfTracingAndMonitoring(t *testing.T) {
	t.Setenv("LLMISVC_MONITORING_DISABLED", "true")
	for _, tracing := range []bool{false, true} {
		svc, dep := pvcEvictorPolicyFixture("shared", "cache")
		if tracing {
			svc.Spec.Tracing = &v1alpha2.TracingSpec{ExporterEndpoint: ptr.To("http://missing.team-a.svc.cluster.local:4317")}
			svc.Annotations = map[string]string{"serving.kserve.io/enable-tracing-egress-network-policy": "true"}
		}
		c := newNetworkPolicyTestClient(t, dep)
		r := &LLMISVCReconciler{Client: c, EventRecorder: record.NewFakeRecorder(100)}
		require.NoError(t, r.postReconcilePVCEvictors(t.Context(), svc, nil))
		policies := &netv1.NetworkPolicyList{}
		require.NoError(t, c.List(t.Context(), policies))
		require.Len(t, policies.Items, 1)
		require.Empty(t, policies.Items[0].Spec.Ingress)
		require.Empty(t, policies.Items[0].Spec.Egress)
	}
}

func TestPVCEvictorNetworkPolicyFailureRetriesDespiteTerminalEvictorError(t *testing.T) {
	svc, dep := pvcEvictorPolicyFixture("shared", "cache")
	terminal := reconcile.TerminalError(errors.New("unsupported PVC access mode"))
	transient := errors.New("temporary policy API failure")
	c := newNetworkPolicyTestClientWithInterceptor(t, interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			return transient
		},
	}, dep)
	r := &LLMISVCReconciler{Client: c, EventRecorder: record.NewFakeRecorder(100)}
	err := r.postReconcilePVCEvictors(t.Context(), svc, terminal)
	require.ErrorIs(t, err, transient)
	require.ErrorContains(t, err, terminal.Error())
	require.False(t, errors.Is(err, reconcile.TerminalError(nil)), "policy failures must requeue even after terminal evictor failures")

	r.Client = newNetworkPolicyTestClient(t, dep)
	require.Same(t, terminal, r.postReconcilePVCEvictors(t.Context(), svc, terminal), "a terminal-only failure must remain terminal")
	require.NoError(t, r.postReconcilePVCEvictors(t.Context(), svc, nil))
}

func TestPVCEvictorNetworkPolicyWatch(t *testing.T) {
	svc, dep := pvcEvictorPolicyFixture("shared", "cache")
	p, err := expectedPVCEvictorNetworkPolicy(dep)
	require.NoError(t, err)
	pred := pvcEvictorNetworkPolicyPredicate()
	require.True(t, pred.Create(event.CreateEvent{Object: p}))
	require.True(t, pred.Delete(event.DeleteEvent{Object: p}))
	require.False(t, pred.Create(event.CreateEvent{Object: &netv1.NetworkPolicy{}}))
	require.False(t, pred.Update(event.UpdateEvent{ObjectOld: p, ObjectNew: p.DeepCopy()}))
	changed := p.DeepCopy()
	changed.Labels = nil
	require.True(t, pred.Update(event.UpdateEvent{ObjectOld: p, ObjectNew: changed}))
	require.True(t, pred.Delete(event.DeleteEvent{Object: changed}), "owner/name must recognize missing labels")
	changed.OwnerReferences = nil
	require.True(t, pred.Update(event.UpdateEvent{ObjectOld: p, ObjectNew: changed}))
	peer := svc.DeepCopy()
	peer.Name = "peer"
	elsewhere := svc.DeepCopy()
	elsewhere.Namespace = "elsewhere"
	c := newNetworkPolicyTestClient(t, svc, peer, elsewhere)
	r := &LLMISVCReconciler{Client: c}
	require.Len(t, r.pvcEvictorNamespaceRequests(t.Context(), p), 2)
}
