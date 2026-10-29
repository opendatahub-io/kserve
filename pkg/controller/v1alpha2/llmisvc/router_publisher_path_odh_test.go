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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"knative.dev/pkg/apis"
	duckv1 "knative.dev/pkg/apis/duck/v1"
	"knative.dev/pkg/kmeta"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
	"github.com/kserve/kserve/pkg/constants"
)

func TestNormalizePublisherPathMatches_ShippedRoute(t *testing.T) {
	for _, modelName := range []string{"app/root", "/app/root", "//app/root", "/app/root/", "app/root/"} {
		t.Run(modelName, func(t *testing.T) {
			llmSvc := &v1alpha2.LLMInferenceService{
				ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "ns"},
				Spec:       v1alpha2.LLMInferenceServiceSpec{Model: v1alpha2.LLMModelSpec{Name: ptr.To(modelName)}},
			}
			route := &gwapiv1.HTTPRoute{Spec: gwapiv1.HTTPRouteSpec{Rules: renderShippedRouterRoute(t, llmSvc)}}
			before := route.DeepCopy()

			normalizePublisherPathMatches(llmSvc, route)

			publisherPaths := map[string]string{
				"v1-completions-publisher-path":      "/v1/completions",
				"v1-chat-completions-publisher-path": "/v1/chat/completions",
				"v1-responses-publisher-path":        "/v1/responses",
				"v1-messages-publisher-path":         "/v1/messages",
				"v1-audio-publisher-path":            "/v1/audio",
				"v1-images-publisher-path":           "/v1/images",
				"v1-publisher-path-catch-all":        "",
			}
			headerMatches := 0
			for i, rule := range route.Spec.Rules {
				name := string(ptr.Deref(rule.Name, ""))
				if suffix, ok := publisherPaths[name]; ok {
					require.Len(t, rule.Matches, 1, "rule %s", name)
					require.NotNil(t, rule.Matches[0].Path, "rule %s", name)
					require.NotNil(t, rule.Matches[0].Path.Value, "rule %s", name)
					assert.Equal(t, "/publishers/ns/models/app/root"+suffix, *rule.Matches[0].Path.Value, "rule %s", name)
					assert.NotContains(t, *rule.Matches[0].Path.Value, "//", "rule %s", name)
					delete(publisherPaths, name)
				} else {
					assert.Equal(t, before.Spec.Rules[i], rule, "non-publisher rule %s must be unchanged", name)
				}
				for j, match := range rule.Matches {
					if len(match.Headers) > 0 {
						assert.Equal(t, before.Spec.Rules[i].Matches[j].Headers, match.Headers)
						assert.Contains(t, match.Headers[0].Value, modelName)
						headerMatches++
					}
				}
			}
			assert.Empty(t, publisherPaths, "every shipped publisher rule must be checked")
			assert.Positive(t, headerMatches, "the shipped model-routing headers must remain intact")
		})
	}
}

func TestNormalizePublisherPathMatches_SlashOnlyNameDropsPublisherRules(t *testing.T) {
	publisherRules := map[string]struct{}{
		"v1-completions-publisher-path":      {},
		"v1-chat-completions-publisher-path": {},
		"v1-responses-publisher-path":        {},
		"v1-messages-publisher-path":         {},
		"v1-audio-publisher-path":            {},
		"v1-images-publisher-path":           {},
		"v1-publisher-path-catch-all":        {},
	}
	for _, modelName := range []string{"/", "//", "////"} {
		t.Run(modelName, func(t *testing.T) {
			llmSvc := &v1alpha2.LLMInferenceService{
				ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "ns"},
				Spec:       v1alpha2.LLMInferenceServiceSpec{Model: v1alpha2.LLMModelSpec{Name: ptr.To(modelName)}},
			}
			route := &gwapiv1.HTTPRoute{Spec: gwapiv1.HTTPRouteSpec{Rules: renderShippedRouterRoute(t, llmSvc)}}
			before := route.DeepCopy()

			normalizePublisherPathMatches(llmSvc, route)

			var expected []gwapiv1.HTTPRouteRule
			removed := 0
			for _, rule := range before.Spec.Rules {
				if _, ok := publisherRules[string(ptr.Deref(rule.Name, ""))]; ok {
					removed++
					continue
				}
				expected = append(expected, rule)
			}
			assert.Equal(t, len(publisherRules), removed, "every shipped publisher rule must be checked")
			assert.Equal(t, expected, route.Spec.Rules, "other paths and model-routing matches must remain")
			assert.False(t, hasPublisherPathMatch(route, "/publishers/ns/models"), "no namespace-wide publisher catch-all")
			r := &LLMISVCReconciler{}
			assert.Empty(t, r.findPublisherPathCollisions(t.Context(), llmSvc, route, []v1alpha2.LLMInferenceService{
				newLLMISVC("peer", withModelName("other")),
			}))
		})
	}
}

func TestNormalizePublisherPathMatches_LeavesOtherModelPaths(t *testing.T) {
	llmSvc := &v1alpha2.LLMInferenceService{
		ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "ns"},
		Spec:       v1alpha2.LLMInferenceServiceSpec{Model: v1alpha2.LLMModelSpec{Name: ptr.To("/app/root")}},
	}
	other := "/publishers/ns/models//app/root-extra/v1/completions"
	regex := "/publishers/ns/models//app/root/v1/.*"
	route := &gwapiv1.HTTPRoute{Spec: gwapiv1.HTTPRouteSpec{Rules: []gwapiv1.HTTPRouteRule{{
		Matches: []gwapiv1.HTTPRouteMatch{
			{Path: &gwapiv1.HTTPPathMatch{Value: ptr.To(other)}},
			{Path: &gwapiv1.HTTPPathMatch{Type: ptr.To(gwapiv1.PathMatchRegularExpression), Value: ptr.To(regex)}},
		},
	}}}}

	normalizePublisherPathMatches(llmSvc, route)

	assert.Equal(t, other, *route.Spec.Rules[0].Matches[0].Path.Value)
	assert.Equal(t, regex, *route.Spec.Rules[0].Matches[1].Path.Value)
}

func withManagedRoute(svc *v1alpha2.LLMInferenceService) {
	svc.Spec.Router = &v1alpha2.RouterSpec{Route: &v1alpha2.GatewayRoutesSpec{}}
}

func TestFindPublisherPathCollisions(t *testing.T) {
	statusAtPath := func(path string) func(*v1alpha2.LLMInferenceService) {
		return func(svc *v1alpha2.LLMInferenceService) {
			url, err := apis.ParseURL("http://example.com" + path)
			require.NoError(t, err)
			svc.Status.Addresses = []v1alpha2.SourcedAddress{{
				Addressable: duckv1.Addressable{URL: url},
				Models: []v1alpha2.ModelSourcedAddressStatus{{
					Name: fullyQualifiedModelName(svc.Namespace, resolveModelName(svc)),
				}},
			}}
		}
	}
	routeAtPath := func(path string) *gwapiv1.HTTPRoute {
		return &gwapiv1.HTTPRoute{Spec: gwapiv1.HTTPRouteSpec{Rules: []gwapiv1.HTTPRouteRule{{
			Matches: []gwapiv1.HTTPRouteMatch{{Path: &gwapiv1.HTTPPathMatch{
				Type: ptr.To(gwapiv1.PathMatchPathPrefix), Value: ptr.To(path),
			}}},
		}}}}
	}
	managedRoute := func(peerName, path string) *gwapiv1.HTTPRoute {
		peer := newLLMISVC(peerName)
		route := routeAtPath(path)
		route.Name = kmeta.ChildName(peer.Name, "-kserve-route")
		route.Namespace = peer.Namespace
		route.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(&peer, v1alpha2.LLMInferenceServiceGVK)}
		return route
	}
	unownedRoute := managedRoute("b", "/publishers/default/models/app/root")
	unownedRoute.OwnerReferences = nil
	withBaseRef := func(svc *v1alpha2.LLMInferenceService) {
		svc.Spec.BaseRefs = []corev1.LocalObjectReference{{Name: "route-preset"}}
	}

	tests := []struct {
		name   string
		self   v1alpha2.LLMInferenceService
		route  *gwapiv1.HTTPRoute
		others []v1alpha2.LLMInferenceService
		routes []client.Object
		want   []string
	}{
		{
			name:  "baseRef route collides before peer has addresses",
			self:  newLLMISVC("a", withModelName("/app/root")),
			route: routeAtPath("/publishers/default/models/app/root"),
			others: []v1alpha2.LLMInferenceService{
				newLLMISVC("b", withModelName("app/root"), withBaseRef),
			},
			routes: []client.Object{managedRoute("b", "/publishers/default/models/app/root")},
			want:   []string{"b"},
		},
		{
			name:  "unowned route at the managed name does not collide",
			self:  newLLMISVC("a", withModelName("/app/root")),
			route: routeAtPath("/publishers/default/models/app/root"),
			others: []v1alpha2.LLMInferenceService{
				newLLMISVC("b", withModelName("app/root"), withBaseRef),
			},
			routes: []client.Object{unownedRoute},
		},
		{
			name:  "baseRef route with a different publisher path does not collide",
			self:  newLLMISVC("a", withModelName("/app/root")),
			route: routeAtPath("/publishers/default/models/app/root"),
			others: []v1alpha2.LLMInferenceService{
				newLLMISVC("b", withModelName("app/root"), withBaseRef),
			},
			routes: []client.Object{managedRoute("b", "/publishers/default/models/other")},
		},
		{
			name:  "force-stopped baseRef peer with managed route does not collide",
			self:  newLLMISVC("a", withModelName("/app/root")),
			route: routeAtPath("/publishers/default/models/app/root"),
			others: []v1alpha2.LLMInferenceService{
				newLLMISVC("b", withModelName("app/root"), withBaseRef, func(svc *v1alpha2.LLMInferenceService) {
					svc.Annotations = map[string]string{constants.StopAnnotationKey: "true"}
				}),
			},
			routes: []client.Object{managedRoute("b", "/publishers/default/models/app/root")},
		},
		{
			name:  "slash-prefixed model collides with ordinary model before peer has status",
			self:  newLLMISVC("a", withModelName("/app/root")),
			route: routeAtPath("/publishers/default/models/app/root"),
			others: []v1alpha2.LLMInferenceService{
				newLLMISVC("b", withModelName("app/root"), withManagedRoute),
			},
			want: []string{"b"},
		},
		{
			name:  "ordinary model collides with slash-prefixed peers using advertised paths",
			self:  newLLMISVC("a", withModelName("app/root")),
			route: routeAtPath("/publishers/default/models/app/root"),
			others: []v1alpha2.LLMInferenceService{
				newLLMISVC("c", withModelName("/app/root"), statusAtPath("/publishers/default/models/app/root")),
				newLLMISVC("b", withModelName("//app/root"), statusAtPath("/publishers/default/models/app/root")),
			},
			want: []string{"b", "c"},
		},
		{
			name:  "peer status overrides spec fallback",
			self:  newLLMISVC("a", withModelName("/app/root")),
			route: routeAtPath("/publishers/default/models/app/root"),
			others: []v1alpha2.LLMInferenceService{
				newLLMISVC("b", withModelName("app/root"), statusAtPath("/publishers/default/models/other")),
			},
		},
		{
			name:  "raw model identity collision is handled by the existing warning",
			self:  newLLMISVC("a", withModelName("/app/root")),
			route: routeAtPath("/publishers/default/models/app/root"),
			others: []v1alpha2.LLMInferenceService{
				newLLMISVC("b", withModelName("/app/root"), withManagedRoute),
			},
		},
		{
			name:  "peer without a managed route or advertised path does not collide",
			self:  newLLMISVC("a", withModelName("/app/root")),
			route: routeAtPath("/publishers/default/models/app/root"),
			others: []v1alpha2.LLMInferenceService{
				newLLMISVC("b", withModelName("app/root")),
				newLLMISVC("c", withModelName("app/root"), func(svc *v1alpha2.LLMInferenceService) {
					svc.Spec.Router = &v1alpha2.RouterSpec{}
				}),
			},
		},
		{
			name:  "force-stopped peer with a managed route and no address does not collide",
			self:  newLLMISVC("a", withModelName("/app/root")),
			route: routeAtPath("/publishers/default/models/app/root"),
			others: []v1alpha2.LLMInferenceService{
				newLLMISVC("b", withModelName("app/root"), withManagedRoute, func(svc *v1alpha2.LLMInferenceService) {
					svc.Annotations = map[string]string{constants.StopAnnotationKey: "true"}
				}),
			},
		},
		{
			name:  "model-routing-only route has no publisher path to collide",
			self:  newLLMISVC("a", withModelName("/app/root")),
			route: &gwapiv1.HTTPRoute{},
			others: []v1alpha2.LLMInferenceService{
				newLLMISVC("b", withModelName("app/root"), withManagedRoute),
			},
		},
		{
			name:  "distinct model identities in the same routing group collide",
			self:  newLLMISVC("a", withModelName("/app/root"), withGroup("group")),
			route: routeAtPath("/publishers/default/models/app/root"),
			others: []v1alpha2.LLMInferenceService{
				newLLMISVC("b", withModelName("app/root"), withGroup("group")),
			},
			want: []string{"b"},
		},
		{
			name:  "shared model identity in the same routing group is excluded",
			self:  newLLMISVC("a", withModelName("/app/root"), withGroup("group")),
			route: routeAtPath("/publishers/default/models/app/root"),
			others: []v1alpha2.LLMInferenceService{
				newLLMISVC("b", withModelName("/app/root"), withGroup("group")),
			},
		},
		{
			name:  "deleting peer is excluded",
			self:  newLLMISVC("a", withModelName("/app/root")),
			route: routeAtPath("/publishers/default/models/app/root"),
			others: []v1alpha2.LLMInferenceService{
				newLLMISVC("b", withModelName("app/root"), withManagedRoute, withDeleting()),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, gwapiv1.Install(scheme))
			r := &LLMISVCReconciler{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(tt.routes...).Build()}
			got := r.findPublisherPathCollisions(t.Context(), &tt.self, tt.route, tt.others)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestWarnPublisherPathCollisions(t *testing.T) {
	self := newLLMISVC("a", withModelName("/app/root"))
	peer := newLLMISVC("b", withModelName("app/root"), withManagedRoute)
	route := &gwapiv1.HTTPRoute{Spec: gwapiv1.HTTPRouteSpec{Rules: []gwapiv1.HTTPRouteRule{{
		Matches: []gwapiv1.HTTPRouteMatch{{Path: &gwapiv1.HTTPPathMatch{
			Value: ptr.To("/publishers/default/models/app/root"),
		}}},
	}}}}
	recorder := record.NewFakeRecorder(1)
	r := &LLMISVCReconciler{EventRecorder: recorder}

	r.warnPublisherPathCollisions(t.Context(), &self, route, []v1alpha2.LLMInferenceService{peer})

	select {
	case event := <-recorder.Events:
		assert.Contains(t, event, "PublisherPathCollision")
		assert.Contains(t, event, "/publishers/default/models/app/root")
	default:
		t.Fatal("expected a publisher path collision warning")
	}
}
