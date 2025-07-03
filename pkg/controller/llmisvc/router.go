/*
Copyright 2025 The KServe Authors.

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
	"cmp"
	"context"
	"fmt"
	"slices"

	duckv1 "knative.dev/pkg/apis/duck/v1"

	"knative.dev/pkg/apis"

	"k8s.io/apimachinery/pkg/types"

	"k8s.io/utils/ptr"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"knative.dev/pkg/kmeta"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayapi "sigs.k8s.io/gateway-api/apis/v1"

	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
)

func (r *LLMInferenceServiceReconciler) reconcileRouter(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService) error {
	logger := log.FromContext(ctx).WithName("reconcileRouter")
	ctx = log.IntoContext(ctx, logger)

	logger.Info("Reconciling Router")

	if err := r.reconcileScheduler(ctx, llmSvc); err != nil {
		llmSvc.MarkRouterNotReady("SchedulerReconcileError", "Failed to reconcile scheduler: %v", err.Error())
		return fmt.Errorf("failed to reconcile scheduler: %w", err)
	}

	// We do not support Gateway's spec, when creating HTTPRoutes either the default gateway or those provided
	// as refs are attached to reconciled routes
	if err := r.reconcileHTTPRoutes(ctx, llmSvc); err != nil {
		llmSvc.MarkRouterNotReady("HTTPRouteReconcileError", "Failed to reconcile HTTPRoute: %v", err.Error())
		return fmt.Errorf("failed to reconcile HTTP routes: %w", err)
	}

	llmSvc.MarkRouterReady()

	return nil
}

func (r *LLMInferenceServiceReconciler) reconcileHTTPRoutes(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService) error {
	logger := log.FromContext(ctx)
	logger.Info("Reconciling HTTPRoute")

	expectedHTTPRoute := r.expectedHTTPRoute(ctx, llmSvc)

	if llmSvc.Spec.Router == nil || llmSvc.Spec.Router.Route == nil || llmSvc.Spec.Router.Route.HTTP == nil {
		return Delete(ctx, r, llmSvc, expectedHTTPRoute)
	}

	route := llmSvc.Spec.Router.Route

	var referencedRoutes []*gatewayapi.HTTPRoute
	if route.HTTP.HasRefs() {
		for _, routeRef := range route.HTTP.Refs {
			providedRoute := &gatewayapi.HTTPRoute{}
			errGet := r.Client.Get(ctx, types.NamespacedName{Namespace: routeRef.Name, Name: llmSvc.GetNamespace()}, providedRoute)

			if errGet != nil {
				if apierrors.IsNotFound(errGet) {
					// TODO(follow-up) mark condition if not found
					continue
				}
				return fmt.Errorf("failed to get HTTPRoute %s/%s: %w", routeRef.Name, llmSvc.GetName(), errGet)
			}

			referencedRoutes = append(referencedRoutes, providedRoute)
		}

		return Delete(ctx, r, llmSvc, expectedHTTPRoute)
	}

	// TODO(validation): referenced gateway exists
	if route.IsManaged() || route.HTTP.HasSpec() {
		updatedRoute, errReconcile := r.reconcileHTTPRoute(ctx, llmSvc, expectedHTTPRoute)
		if errReconcile != nil {
			return fmt.Errorf("failed to reconcile HTTPRoute %s/%s: %w", expectedHTTPRoute.GetNamespace(), expectedHTTPRoute.GetName(), errReconcile)
		}

		referencedRoutes = append(referencedRoutes, updatedRoute)
	}

	return r.updateRoutingStatus(ctx, llmSvc, referencedRoutes...)
}

func (r *LLMInferenceServiceReconciler) expectedHTTPRoute(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService) *gatewayapi.HTTPRoute {
	httpRoute := &gatewayapi.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      kmeta.ChildName(llmSvc.GetName(), "-kserve-route"),
			Namespace: llmSvc.GetNamespace(),
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(llmSvc, v1alpha1.LLMInferenceServiceGVK),
			},
			Labels: RouterLabels(llmSvc),
		},
	}

	if llmSvc.Spec.Router != nil && llmSvc.Spec.Router.Route != nil && llmSvc.Spec.Router.Route.HTTP.Spec != nil {
		httpRoute.Spec = *llmSvc.Spec.Router.Route.HTTP.Spec.DeepCopy()
	}

	if llmSvc.Spec.Router != nil && llmSvc.Spec.Router.Gateway != nil {
		log.FromContext(ctx).Info("Reconciling Gateway", "gateway", llmSvc.Spec.Router.Gateway)

		// If Gateway is not managed (has .refs), re-attach the expected route to the referenced gateways
		if llmSvc.Spec.Router.Gateway.HasRefs() {
			httpRoute.Spec.CommonRouteSpec.ParentRefs = make([]gatewayapi.ParentReference, 0, len(llmSvc.Spec.Router.Gateway.Refs))
			for _, ref := range llmSvc.Spec.Router.Gateway.Refs {
				httpRoute.Spec.CommonRouteSpec.ParentRefs = append(httpRoute.Spec.CommonRouteSpec.ParentRefs, toGatewayRef(ref))
			}
		}
	}

	return httpRoute
}

func (r *LLMInferenceServiceReconciler) reconcileHTTPRoute(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService, expected *gatewayapi.HTTPRoute) (*gatewayapi.HTTPRoute, error) {
	curr := &gatewayapi.HTTPRoute{}

	err := r.Client.Get(ctx, client.ObjectKeyFromObject(expected), curr)
	if err != nil && !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("failed to get HTTPRoute %s/%s: %w", expected.GetNamespace(), expected.GetName(), err)
	}
	if apierrors.IsNotFound(err) {
		return expected, Create(ctx, r, llmSvc, expected)
	}

	return expected, Update(ctx, r, llmSvc, curr, expected, semanticHTTPRouteIsEqual)
}

func (r *LLMInferenceServiceReconciler) updateRoutingStatus(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService, routes ...*gatewayapi.HTTPRoute) error {
	logger := log.FromContext(ctx)

	var urls []*apis.URL
	for _, route := range routes {
		discoverURL, err := DiscoverURLs(ctx, r.Client, route)
		if IgnoreExternalAddressNotFound(err) != nil {
			return fmt.Errorf("failed to discover URL for route %s/%s: %w", route.GetNamespace(), route.GetName(), err)
		}
		if discoverURL != nil {
			urls = append(urls, discoverURL...)
		}
	}

	slices.SortStableFunc(urls, func(a, b *apis.URL) int {
		return cmp.Compare(a.String(), b.String())
	})

	externalURLs := FilterExternalURLs(urls)
	if len(externalURLs) == 0 {
		logger.Info("no public URL discovered")
	} else {
		llmSvc.Status.URL = externalURLs[0]
	}

	llmSvc.Status.Addresses = make([]duckv1.Addressable, 0, len(urls))
	for _, url := range urls {
		llmSvc.Status.Addresses = append(llmSvc.Status.Addresses, duckv1.Addressable{
			URL: url,
		})
	}

	return nil
}

func toGatewayRef(ref v1alpha1.UntypedObjectReference) gatewayapi.ParentReference {
	return gatewayapi.ParentReference{
		// TODO(api): With this structure we are missing the ability to narrow a section of targeted gateway by the route we are creating
		// missing SectionName and Port will implicitly bind the route to the first listener in the parent
		Name:      ref.Name,
		Namespace: &ref.Namespace,
		Group:     ptr.To(gatewayapi.Group("gateway.networking.k8s.io")),
		Kind:      ptr.To(gatewayapi.Kind("Gateway")),
	}
}

func RouterLabels(llmSvc *v1alpha1.LLMInferenceService) map[string]string {
	return map[string]string{
		"app.kubernetes.io/component": "llminferenceservice-router",
		"app.kubernetes.io/name":      llmSvc.GetName(),
		"app.kubernetes.io/part-of":   "llminferenceservice",
	}
}

func semanticHTTPRouteIsEqual(e *gatewayapi.HTTPRoute, c *gatewayapi.HTTPRoute) bool {
	return equality.Semantic.DeepDerivative(e.Spec, c.Spec) &&
		equality.Semantic.DeepDerivative(e.Labels, c.Labels) &&
		equality.Semantic.DeepDerivative(e.Annotations, c.Annotations)
}
