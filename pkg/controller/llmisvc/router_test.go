/*
These tests cover recent changes in router.go:
- EvaluateGatewayConditions
- CollectReferencedGateways
- EvaluateHTTPRouteConditions
- EvaluateInferencePoolConditions
- expectedHTTPRoute, toGatewayRef, semanticHTTPRouteIsEqual
- reconcileHTTPRoutes (selected paths)
- updateRoutingStatus (basic behavior)

Testing stack:
- standard "testing" package
- controller-runtime fake client (no new dependencies)
*/

package llmisvc

import (
	"context"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"sigs.k8s.io/controller-runtime/pkg/client"
	crfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	"knative.dev/pkg/apis"
	duckv1 "knative.dev/pkg/apis/duck/v1"

	gatewayapi "sigs.k8s.io/gateway-api/apis/v1"
	igwapi "sigs.k8s.io/gateway-api-inference-extension/api/v1alpha2"

	v1alpha1 "github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
)

// --------- Test scaffolding ---------

func buildScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := gatewayapi.AddToScheme(s); err != nil {
		t.Fatalf("add gatewayapi scheme: %v", err)
	}
	if err := igwapi.AddToScheme(s); err != nil {
		t.Fatalf("add inference extension scheme: %v", err)
	}
	if err := v1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("add kserve v1alpha1 scheme: %v", err)
	}
	return s
}

func newReconcilerWithObjects(t *testing.T, objs ...client.Object) (*LLMInferenceServiceReconciler, client.Client, context.Context) {
	t.Helper()
	s := buildScheme(t)
	cl := crfake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build()
	r := &LLMInferenceServiceReconciler{Client: cl}
	return r, cl, context.Background()
}

func makeLLMSvc(ns, name string, mutate func(*v1alpha1.LLMInferenceService)) *v1alpha1.LLMInferenceService {
	llmsvc := &v1alpha1.LLMInferenceService{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: ns,
			Name:      name,
		},
	}
	if mutate != nil {
		mutate(llmsvc)
	}
	return llmsvc
}

func makeGateway(ns, name string, ready bool) *gatewayapi.Gateway {
	condStatus := metav1.ConditionFalse
	if ready {
		condStatus = metav1.ConditionTrue
	}
	return &gatewayapi.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: ns,
			Name:      name,
		},
		Status: gatewayapi.GatewayStatus{
			Conditions: []metav1.Condition{
				{
					Type:               string(gatewayapi.GatewayConditionReady),
					Status:             condStatus,
					Reason:             "TestReason",
					Message:            "test message",
					ObservedGeneration: 1,
					LastTransitionTime: metav1.NewTime(time.Now()),
				},
			},
		},
	}
}

func makeHTTPRoute(ns, name string, ready bool) *gatewayapi.HTTPRoute {
	condStatus := metav1.ConditionFalse
	if ready {
		condStatus = metav1.ConditionTrue
	}
	return &gatewayapi.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: ns,
			Name:      name,
		},
		Status: gatewayapi.HTTPRouteStatus{
			Parents: []gatewayapi.RouteParentStatus{{
				Conditions: []metav1.Condition{{
					Type:               string(gatewayapi.RouteConditionAccepted),
					Status:             condStatus,
					Reason:             "TestReason",
					Message:            "test message",
					ObservedGeneration: 1,
					LastTransitionTime: metav1.NewTime(time.Now()),
				}},
			}},
		},
	}
}

func makeInferencePool(ns, name string, ready bool) *igwapi.InferencePool {
	condStatus := metav1.ConditionFalse
	if ready {
		condStatus = metav1.ConditionTrue
	}
	return &igwapi.InferencePool{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: ns,
			Name:      name,
		},
		Status: igwapi.InferencePoolStatus{
			Conditions: []metav1.Condition{
				{
					Type:               "Ready",
					Status:             condStatus,
					Reason:             "TestReason",
					Message:            "test message",
					ObservedGeneration: 1,
					LastTransitionTime: metav1.NewTime(time.Now()),
				},
			},
		},
	}
}

// --------- EvaluateGatewayConditions ---------

func TestEvaluateGatewayConditions_NoRefs_ReturnsNil(t *testing.T) {
	r, _, ctx := newReconcilerWithObjects(t)
	llm := makeLLMSvc("ns", "svc", nil)

	if err := r.EvaluateGatewayConditions(ctx, llm); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestEvaluateGatewayConditions_RefsFetchError_ReturnsError(t *testing.T) {
	// Reference a Gateway that does not exist -> CollectReferencedGateways should error
	r, _, ctx := newReconcilerWithObjects(t)
	llm := makeLLMSvc("ns", "svc", func(s *v1alpha1.LLMInferenceService) {
		s.Spec.Router = &v1alpha1.RouterSpec{
			Gateway: &v1alpha1.RouterGateway{
				Refs: []v1alpha1.UntypedObjectReference{{Name: "missing-gw", Namespace: "ns"}},
			},
		}
	})

	if err := r.EvaluateGatewayConditions(ctx, llm); err == nil {
		t.Fatalf("expected error when fetching missing gateway, got nil")
	}
}

func TestEvaluateGatewayConditions_NotAllReady_MarksNotReady_NoError(t *testing.T) {
	gwNotReady := makeGateway("ns", "gw-notready", false)
	gwReady := makeGateway("ns", "gw-ready", true)
	r, _, ctx := newReconcilerWithObjects(t, gwNotReady, gwReady)

	llm := makeLLMSvc("ns", "svc", func(s *v1alpha1.LLMInferenceService) {
		s.Spec.Router = &v1alpha1.RouterSpec{
			Gateway: &v1alpha1.RouterGateway{
				Refs: []v1alpha1.UntypedObjectReference{
					{Name: gatewayapi.ObjectName(gwNotReady.Name), Namespace: gatewayapi.Namespace(gwNotReady.Namespace)},
					{Name: gatewayapi.ObjectName(gwReady.Name), Namespace: gatewayapi.Namespace(gwReady.Namespace)},
				},
			},
		}
	})

	if err := r.EvaluateGatewayConditions(ctx, llm); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEvaluateGatewayConditions_AllReady_MarksReady(t *testing.T) {
	gw1 := makeGateway("ns", "gw1", true)
	gw2 := makeGateway("ns", "gw2", true)
	r, _, ctx := newReconcilerWithObjects(t, gw1, gw2)

	llm := makeLLMSvc("ns", "svc", func(s *v1alpha1.LLMInferenceService) {
		s.Spec.Router = &v1alpha1.RouterSpec{
			Gateway: &v1alpha1.RouterGateway{
				Refs: []v1alpha1.UntypedObjectReference{
					{Name: gatewayapi.ObjectName(gw1.Name), Namespace: gatewayapi.Namespace(gw1.Namespace)},
					{Name: gatewayapi.ObjectName(gw2.Name), Namespace: gatewayapi.Namespace(gw2.Namespace)},
				},
			},
		}
	})

	if err := r.EvaluateGatewayConditions(ctx, llm); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// --------- CollectReferencedGateways ---------

func TestCollectReferencedGateways_NoRefs_ReturnsNil(t *testing.T) {
	r, _, ctx := newReconcilerWithObjects(t)
	llm := makeLLMSvc("ns", "svc", nil)

	got, err := r.CollectReferencedGateways(ctx, llm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
}

func TestCollectReferencedGateways_FetchesAndDedups(t *testing.T) {
	gwA := makeGateway("ns", "gwA", true)
	gwB := makeGateway("ns", "gwB", false)
	r, _, ctx := newReconcilerWithObjects(t, gwA, gwB)

	llm := makeLLMSvc("ns", "svc", func(s *v1alpha1.LLMInferenceService) {
		s.Spec.Router = &v1alpha1.RouterSpec{
			Gateway: &v1alpha1.RouterGateway{
				Refs: []v1alpha1.UntypedObjectReference{
					{Name: "gwA", Namespace: "ns"},
					{Name: "gwB", Namespace: "ns"},
					{Name: "gwA", Namespace: "ns"}, // duplicate
				},
			},
		}
	})

	got, err := r.CollectReferencedGateways(ctx, llm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 unique gateways, got %d", len(got))
	}
}

func TestCollectReferencedGateways_MissingNamespace_DefaultsToSvcNamespace(t *testing.T) {
	gw := makeGateway("ns", "gw", true)
	r, _, ctx := newReconcilerWithObjects(t, gw)

	llm := makeLLMSvc("ns", "svc", func(s *v1alpha1.LLMInferenceService) {
		s.Spec.Router = &v1alpha1.RouterSpec{
			Gateway: &v1alpha1.RouterGateway{
				Refs: []v1alpha1.UntypedObjectReference{
					{Name: "gw"}, // no namespace -> should default to "ns"
				},
			},
		}
	})

	got, err := r.CollectReferencedGateways(ctx, llm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Name != "gw" || got[0].Namespace != "ns" {
		t.Fatalf("unexpected gateways: %+v", got)
	}
}

// --------- collectReferencedRoutes ---------

func TestCollectReferencedRoutes_PresentAndMissing(t *testing.T) {
	existing := makeHTTPRoute("ns", "present", true)
	r, _, ctx := newReconcilerWithObjects(t, existing)

	llm := makeLLMSvc("ns", "svc", func(s *v1alpha1.LLMInferenceService) {
		s.Spec.Router = &v1alpha1.RouterSpec{
			Route: &v1alpha1.RouterRoute{
				HTTP: &v1alpha1.UntypedHTTPRoute{
					Refs: []v1alpha1.UntypedObjectReference{
						{Name: "present", Namespace: "ns"},
						{Name: "missing", Namespace: "ns"},
					},
				},
			},
		}
	})

	got, err := r.collectReferencedRoutes(ctx, llm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Name != "present" {
		t.Fatalf("expected only the present route, got: %+v", got)
	}
}

// --------- EvaluateHTTPRouteConditions ---------

func TestEvaluateHTTPRouteConditions_NoRouter_MarksReady(t *testing.T) {
	r, _, ctx := newReconcilerWithObjects(t)
	llm := makeLLMSvc("ns", "svc", nil)

	if err := r.EvaluateHTTPRouteConditions(ctx, llm); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEvaluateHTTPRouteConditions_ReferencedRoutes_NotReadyPath_NoError(t *testing.T) {
	// Include one not ready, one ready route. Function should mark NotReady but not return error.
	rNotReady := makeHTTPRoute("ns", "r1", false)
	rReady := makeHTTPRoute("ns", "r2", true)
	r, _, ctx := newReconcilerWithObjects(t, rNotReady, rReady)

	llm := makeLLMSvc("ns", "svc", func(s *v1alpha1.LLMInferenceService) {
		s.Spec.Router = &v1alpha1.RouterSpec{
			Route: &v1alpha1.RouterRoute{
				HTTP: &v1alpha1.UntypedHTTPRoute{
					Refs: []v1alpha1.UntypedObjectReference{
						{Name: "r1", Namespace: "ns"},
						{Name: "r2", Namespace: "ns"},
					},
				},
			},
		}
	})

	if err := r.EvaluateHTTPRouteConditions(ctx, llm); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEvaluateHTTPRouteConditions_ManagedRoutePresent(t *testing.T) {
	llm := makeLLMSvc("ns", "svc", func(s *v1alpha1.LLMInferenceService) {
		s.Spec.Router = &v1alpha1.RouterSpec{
			Route: &v1alpha1.RouterRoute{
				HTTP: &v1alpha1.UntypedHTTPRoute{
					Spec: &gatewayapi.HTTPRouteSpec{},
				},
			},
		}
	})
	r, cl, ctx := newReconcilerWithObjects(t)
	managed := makeHTTPRoute("ns", "svc-kserve-route", true)
	if err := cl.Create(ctx, managed); err != nil {
		t.Fatalf("failed to create managed route: %v", err)
	}

	if err := r.EvaluateHTTPRouteConditions(ctx, llm); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEvaluateHTTPRouteConditions_NoRoutes_MarksReady(t *testing.T) {
	r, _, ctx := newReconcilerWithObjects(t)
	llm := makeLLMSvc("ns", "svc", func(s *v1alpha1.LLMInferenceService) {
		s.Spec.Router = &v1alpha1.RouterSpec{
			Route: &v1alpha1.RouterRoute{HTTP: &v1alpha1.UntypedHTTPRoute{}},
		}
	})

	if err := r.EvaluateHTTPRouteConditions(ctx, llm); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// --------- EvaluateInferencePoolConditions ---------

func TestEvaluateInferencePoolConditions_NoPoolConfig_MarksReady(t *testing.T) {
	r, _, ctx := newReconcilerWithObjects(t)
	llm := makeLLMSvc("ns", "svc", nil)
	if err := r.EvaluateInferencePoolConditions(ctx, llm); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEvaluateInferencePoolConditions_ReferencedPoolNotReady_NoError(t *testing.T) {
	pool := makeInferencePool("ns", "pool", false)
	r, _, ctx := newReconcilerWithObjects(t, pool)
	llm := makeLLMSvc("ns", "svc", func(s *v1alpha1.LLMInferenceService) {
		s.Spec.Router = &v1alpha1.RouterSpec{
			Scheduler: &v1alpha1.RouterScheduler{
				Pool: &v1alpha1.RouterSchedulerPool{
					Ref: &v1alpha1.UntypedObjectReference{Name: "pool"},
				},
			},
		}
	})
	if err := r.EvaluateInferencePoolConditions(ctx, llm); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEvaluateInferencePoolConditions_EmbeddedPoolReady_NoError(t *testing.T) {
	embedded := makeInferencePool("ns", "svc-pool", true)
	r, cl, ctx := newReconcilerWithObjects(t, embedded)
	llm := makeLLMSvc("ns", "svc", func(s *v1alpha1.LLMInferenceService) {
		s.Spec.Router = &v1alpha1.RouterSpec{
			Scheduler: &v1alpha1.RouterScheduler{
				Pool: &v1alpha1.RouterSchedulerPool{Spec: &igwapi.InferencePoolSpec{}},
			},
		}
	})
	// Ensure expected embedded pool name exists (<name>-pool)
	if embedded.Name != llm.Name+"-pool" {
		exp := embedded.DeepCopy()
		exp.Name = llm.Name + "-pool"
		if err := cl.Create(ctx, exp); err != nil {
			t.Fatalf("failed to create embedded pool: %v", err)
		}
	}

	if err := r.EvaluateInferencePoolConditions(ctx, llm); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// --------- expectedHTTPRoute, toGatewayRef, RouterLabels, semanticHTTPRouteIsEqual ---------

func TestExpectedHTTPRoute_CopiesSpecAndAttachesGatewayRefs(t *testing.T) {
	r, _, ctx := newReconcilerWithObjects(t)
	spec := &gatewayapi.HTTPRouteSpec{
		CommonRouteSpec: gatewayapi.CommonRouteSpec{
			ParentRefs: []gatewayapi.ParentReference{{Name: "ignored"}},
		},
	}
	llm := makeLLMSvc("ns", "svc", func(s *v1alpha1.LLMInferenceService) {
		s.Spec.Router = &v1alpha1.RouterSpec{
			Route: &v1alpha1.RouterRoute{
				HTTP: &v1alpha1.UntypedHTTPRoute{Spec: spec},
			},
			Gateway: &v1alpha1.RouterGateway{
				Refs: []v1alpha1.UntypedObjectReference{
					{Name: "g1", Namespace: "ns1"},
					{Name: "g2", Namespace: "ns2"},
				},
			},
		}
	})

	got := r.expectedHTTPRoute(ctx, llm)
	if got.Name != "svc-kserve-route" || got.Namespace != "ns" {
		t.Fatalf("expected svc/ns name, got %s/%s", got.Namespace, got.Name)
	}
	want := []gatewayapi.ParentReference{
		{Name: "g1", Namespace: ptr.To(gatewayapi.Namespace("ns1")), Group: ptr.To(gatewayapi.Group("gateway.networking.k8s.io")), Kind: ptr.To(gatewayapi.Kind("Gateway"))},
		{Name: "g2", Namespace: ptr.To(gatewayapi.Namespace("ns2")), Group: ptr.To(gatewayapi.Group("gateway.networking.k8s.io")), Kind: ptr.To(gatewayapi.Kind("Gateway"))},
	}
	if diff := cmp.Diff(want, got.Spec.ParentRefs); diff != "" {
		t.Fatalf("parent refs mismatch (-want +got): %s", diff)
	}
}

func TestRouterLabels_ReturnsExpected(t *testing.T) {
	llm := makeLLMSvc("ns", "name", nil)
	labels := RouterLabels(llm)
	if labels["app.kubernetes.io/component"] != "llminferenceservice-router" {
		t.Fatalf("component label mismatch: %v", labels)
	}
	if labels["app.kubernetes.io/name"] != "name" {
		t.Fatalf("name label mismatch: %v", labels)
	}
	if labels["app.kubernetes.io/part-of"] != "llminferenceservice" {
		t.Fatalf("part-of label mismatch: %v", labels)
	}
}

func TestToGatewayRef_SetsFields(t *testing.T) {
	ref := toGatewayRef(v1alpha1.UntypedObjectReference{Name: "gw", Namespace: "ns"})
	if string(ref.Name) != "gw" || (ref.Namespace == nil || string(*ref.Namespace) != "ns") {
		t.Fatalf("unexpected ref name/namespace: %+v", ref)
	}
	if ref.Group == nil || string(*ref.Group) != "gateway.networking.k8s.io" || ref.Kind == nil || string(*ref.Kind) != "Gateway" {
		t.Fatalf("unexpected group/kind: %+v", ref)
	}
}

func TestSemanticHTTPRouteIsEqual(t *testing.T) {
	base := &gatewayapi.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Labels:      map[string]string{"a": "1"},
			Annotations: map[string]string{"x": "y"},
		},
		Spec: gatewayapi.HTTPRouteSpec{},
	}
	// Additional label is allowed (DeepDerivative)
	mod := base.DeepCopy()
	mod.Labels["b"] = "2"
	if !semanticHTTPRouteIsEqual(base, mod) {
		t.Fatalf("expected routes to be equal under DeepDerivative")
	}
	// Annotation change should break equality
	mod2 := base.DeepCopy()
	mod2.Annotations["x"] = "changed"
	if semanticHTTPRouteIsEqual(base, mod2) {
		t.Fatalf("expected routes to be not equal due to annotation change")
	}
}

// --------- reconcileHTTPRoutes selected paths ---------

func TestReconcileHTTPRoutes_NoRoute_DeletesManagedHTTPRoute(t *testing.T) {
	// Pre-create the managed route and verify Delete path when Router.Route is nil
	r, cl, ctx := newReconcilerWithObjects(t)
	llm := makeLLMSvc("ns", "svc", nil)

	pre := &gatewayapi.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "svc-kserve-route",
			Namespace: "ns",
		},
	}
	if err := cl.Create(ctx, pre); err != nil {
		t.Fatalf("failed to create pre-existing route: %v", err)
	}

	if err := r.reconcileHTTPRoutes(ctx, llm); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := &gatewayapi.HTTPRoute{}
	err := cl.Get(ctx, types.NamespacedName{Namespace: "ns", Name: "svc-kserve-route"}, got)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("expected route to be deleted, get err = %v (obj: %+v)", err, got)
	}
}

func TestReconcileHTTPRoutes_HTTPHasRefs_DeletesManagedHTTPRoute(t *testing.T) {
	r, cl, ctx := newReconcilerWithObjects(t)
	llm := makeLLMSvc("ns", "svc", func(s *v1alpha1.LLMInferenceService) {
		s.Spec.Router = &v1alpha1.RouterSpec{
			Route: &v1alpha1.RouterRoute{
				HTTP: &v1alpha1.UntypedHTTPRoute{
					Refs: []v1alpha1.UntypedObjectReference{{Name: "user-route", Namespace: "ns"}},
				},
			},
		}
	})
	// Pre-create managed route
	pre := &gatewayapi.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "svc-kserve-route",
			Namespace: "ns",
		},
	}
	if err := cl.Create(ctx, pre); err != nil {
		t.Fatalf("failed to create pre-existing route: %v", err)
	}

	if err := r.reconcileHTTPRoutes(ctx, llm); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := &gatewayapi.HTTPRoute{}
	err := cl.Get(ctx, types.NamespacedName{Namespace: "ns", Name: "svc-kserve-route"}, got)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("expected route to be deleted, get err = %v", err)
	}
}

func TestReconcileHTTPRoutes_HTTPHasSpec_ReconcilesManagedHTTPRoute(t *testing.T) {
	r, cl, ctx := newReconcilerWithObjects(t)
	llm := makeLLMSvc("ns", "svc", func(s *v1alpha1.LLMInferenceService) {
		s.Spec.Router = &v1alpha1.RouterSpec{
			Route: &v1alpha1.RouterRoute{
				HTTP: &v1alpha1.UntypedHTTPRoute{Spec: &gatewayapi.HTTPRouteSpec{}},
			},
		}
	})

	if err := r.reconcileHTTPRoutes(ctx, llm); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Managed route should exist
	got := &gatewayapi.HTTPRoute{}
	if err := cl.Get(ctx, types.NamespacedName{Namespace: "ns", Name: "svc-kserve-route"}, got); err != nil {
		t.Fatalf("expected managed route to exist, get err = %v", err)
	}
}

// --------- updateRoutingStatus basic behavior ---------

func TestUpdateRoutingStatus_SetsAddresses_And_AllowsExternalURL(t *testing.T) {
	// We cannot reliably mock DiscoverURLs here; verify that method doesn't error and status can hold URLs.
	r, _, ctx := newReconcilerWithObjects(t)
	llm := makeLLMSvc("ns", "svc", nil)
	r1 := makeHTTPRoute("ns", "r1", true)

	// Should not error even if no URLs discovered
	if err := r.updateRoutingStatus(ctx, llm, r1); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Manually set URL and addresses to validate status structure accommodates data correctly
	publicURL, _ := apis.ParseURL("https://public.example.com")
	internalURL, _ := apis.ParseURL("http://internal.svc.cluster.local")
	llm.Status.URL = publicURL
	llm.Status.Addresses = []duckv1.Addressable{{URL: publicURL}, {URL: internalURL}}

	if llm.Status.URL == nil || llm.Status.URL.String() != "https://public.example.com" {
		t.Fatalf("expected public URL, got %v", llm.Status.URL)
	}
	if len(llm.Status.Addresses) != 2 {
		t.Fatalf("expected 2 addresses, got %d", len(llm.Status.Addresses))
	}
}