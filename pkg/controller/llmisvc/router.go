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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"knative.dev/pkg/apis"
	duckv1 "knative.dev/pkg/apis/duck/v1"
	"knative.dev/pkg/network"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"k8s.io/apimachinery/pkg/types"

	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"knative.dev/pkg/kmeta"
	igwapi "sigs.k8s.io/gateway-api-inference-extension/api/v1alpha2"
	gatewayapi "sigs.k8s.io/gateway-api/apis/v1"

	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
	"github.com/kserve/kserve/pkg/constants"
	"github.com/kserve/kserve/pkg/utils"
)

func (r *LLMInferenceServiceReconciler) reconcileRouter(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService) error {
	logger := log.FromContext(ctx).WithName("reconcileRouter")
	ctx = log.IntoContext(ctx, logger)

	logger.Info("Reconciling Router")

	defer llmSvc.DetermineRouterReadiness()

	if err := r.validateGatewayOCP(ctx, llmSvc); err != nil {
		err := fmt.Errorf("failed to validate Gateway on OpenShift: %w", err)
		llmSvc.MarkHTTPRoutesNotReady("HTTPRouteReconcileError", err.Error())
		return err
	}

	if err := r.validateRouterReferences(ctx, llmSvc); err != nil {
		return err
	}

	if err := r.reconcileScheduler(ctx, llmSvc); err != nil {
		llmSvc.MarkSchedulerWorkloadNotReady("SchedulerReconcileError", "Failed to reconcile scheduler: %v", err.Error())
		return fmt.Errorf("failed to reconcile scheduler: %w", err)
	}

	// We do not support Gateway's spec, when creating HTTPRoutes either the default gateway or those provided
	// as refs are attached to reconciled routes
	if err := r.reconcileHTTPRoutes(ctx, llmSvc); err != nil {
		llmSvc.MarkHTTPRoutesNotReady("HTTPRouteReconcileError", "Failed to reconcile HTTPRoute: %v", err.Error())
		return fmt.Errorf("failed to reconcile HTTP routes: %w", err)
	}

	if err := r.reconcileIstioDestinationRules(ctx, llmSvc); err != nil {
		llmSvc.MarkHTTPRoutesNotReady("IstioDestinationRuleReconcileError", "Failed to reconcile DestinationRule: %v", err.Error())
		return fmt.Errorf("failed to reconcile istio destination rules: %w", err)
	}

	// Evaluate the subconditions
	if err := r.EvaluateInferencePoolConditions(ctx, llmSvc); err != nil {
		return fmt.Errorf("failed to evaluate Inference Pool conditions: %w", err)
	}

	if err := r.EvaluateGatewayConditions(ctx, llmSvc); err != nil {
		return fmt.Errorf("failed to evaluate gateway conditions: %w", err)
	}

	if err := r.EvaluateHTTPRouteConditions(ctx, llmSvc); err != nil {
		return fmt.Errorf("failed to evaluate HTTPRoute conditions: %w", err)
	}

	return nil
}

func (r *LLMInferenceServiceReconciler) reconcileHTTPRoutes(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService) error {
	logger := log.FromContext(ctx)
	logger.Info("Reconciling HTTPRoute")

	// First, try to get the existing HTTPRoute to check migration state
	existingRoute := &gatewayapi.HTTPRoute{}
	routeName := kmeta.ChildName(llmSvc.GetName(), "-kserve-route")
	err := r.Client.Get(ctx, types.NamespacedName{Namespace: llmSvc.GetNamespace(), Name: routeName}, existingRoute)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to get existing HTTPRoute: %w", err)
		}
		existingRoute = nil // Route doesn't exist yet
	}

	expectedHTTPRoute := r.expectedHTTPRoute(ctx, llmSvc, existingRoute)

	if utils.GetForceStopRuntime(llmSvc) || llmSvc.Spec.Router == nil || llmSvc.Spec.Router.Route == nil {
		_ = r.updateRoutingStatus(ctx, llmSvc)
		return Delete(ctx, r, llmSvc, expectedHTTPRoute)
	}

	referencedRoutes, err := r.collectReferencedRoutes(ctx, llmSvc)
	if err != nil {
		return fmt.Errorf("failed to collect referenced routes: %w", err)
	}

	route := llmSvc.Spec.Router.Route

	if route.HTTP.HasRefs() {
		err = Delete(ctx, r, llmSvc, expectedHTTPRoute)
		if err != nil {
			return fmt.Errorf("failed to delete expected HTTPRoute %s/%s: %w", expectedHTTPRoute.GetNamespace(), expectedHTTPRoute.GetName(), err)
		}
	}

	if route.HTTP.HasSpec() {
		if err := Reconcile(ctx, r, llmSvc, &gatewayapi.HTTPRoute{}, expectedHTTPRoute, semanticHTTPRouteIsEqual); err != nil {
			return fmt.Errorf("failed to reconcile HTTPRoute %s/%s: %w", expectedHTTPRoute.GetNamespace(), expectedHTTPRoute.GetName(), err)
		}
		referencedRoutes = append(referencedRoutes, expectedHTTPRoute)
	}

	return r.updateRoutingStatus(ctx, llmSvc, referencedRoutes...)
}

func (r *LLMInferenceServiceReconciler) collectReferencedRoutes(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService) ([]*gatewayapi.HTTPRoute, error) {
	if llmSvc.Spec.Router == nil || llmSvc.Spec.Router.Route == nil || !llmSvc.Spec.Router.Route.HTTP.HasRefs() {
		return nil, nil
	}

	referencedRoutes := make([]*gatewayapi.HTTPRoute, 0, len(llmSvc.Spec.Router.Route.HTTP.Refs))

	for _, routeRef := range llmSvc.Spec.Router.Route.HTTP.Refs {
		route := &gatewayapi.HTTPRoute{}
		if err := r.Client.Get(ctx, types.NamespacedName{Namespace: llmSvc.GetNamespace(), Name: routeRef.Name}, route); err != nil {
			if apierrors.IsNotFound(err) {
				// Skip missing routes - validation is handled separately
				continue
			}
			return referencedRoutes, fmt.Errorf("failed to get HTTPRoute %s/%s: %w", llmSvc.GetName(), routeRef.Name, err)
		}

		referencedRoutes = append(referencedRoutes, route)
	}

	return referencedRoutes, nil
}

func (r *LLMInferenceServiceReconciler) expectedHTTPRoute(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService, existingRoute *gatewayapi.HTTPRoute) *gatewayapi.HTTPRoute {
	logger := log.FromContext(ctx)

	httpRoute := &gatewayapi.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      kmeta.ChildName(llmSvc.GetName(), "-kserve-route"),
			Namespace: llmSvc.GetNamespace(),
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(llmSvc, v1alpha1.LLMInferenceServiceGVK),
			},
			Labels:      RouterLabels(llmSvc),
			Annotations: make(map[string]string),
		},
	}

	if llmSvc.Spec.Router != nil && llmSvc.Spec.Router.Route != nil && llmSvc.Spec.Router.Route.HTTP.Spec != nil {
		httpRoute.Spec = *llmSvc.Spec.Router.Route.HTTP.Spec.DeepCopy()
	}

	// Determine which InferencePool API group to use based on migration state
	useV1 := r.shouldUseInferencePoolV1(ctx, existingRoute)
	if useV1 {
		logger.Info("Using InferencePool v1 API for HTTPRoute", "route", httpRoute.Name)
		// Set migration annotation (one-way lock)
		httpRoute.Annotations[constants.InferencePoolMigratedAnnotation] = "v1"
		// Update backendRefs to use v1 API group
		migrateHTTPRouteToV1(llmSvc, httpRoute)
	} else {
		logger.Info("Using InferencePool v1alpha2 API for HTTPRoute", "route", httpRoute.Name)
	}

	// Preserve existing annotations if present
	if existingRoute != nil && existingRoute.Annotations != nil {
		for k, v := range existingRoute.Annotations {
			if _, exists := httpRoute.Annotations[k]; !exists {
				httpRoute.Annotations[k] = v
			}
		}
	}

	return httpRoute
}

// shouldUseInferencePoolV1 determines if the HTTPRoute should use v1 InferencePool.
// Migration decision tree:
// 1. Has migration annotation? → Use v1 (locked)
// 2. Gateway accepted v1? → Switch to v1, set annotation
// 3. Gateway rejected v1alpha2? → Switch to v1, set annotation
// 4. Default: Stay on v1alpha2
func (r *LLMInferenceServiceReconciler) shouldUseInferencePoolV1(ctx context.Context, existingRoute *gatewayapi.HTTPRoute) bool {
	logger := log.FromContext(ctx)

	// Check for migration annotation (one-way lock)
	isMigrated := false
	if existingRoute != nil && existingRoute.Annotations != nil {
		if existingRoute.Annotations[constants.InferencePoolMigratedAnnotation] == "v1" {
			isMigrated = true
			logger.V(1).Info("HTTPRoute already migrated to v1 (annotation present)")
		}
	}

	// No existing route means we haven't tried v1alpha2 yet - start with v1alpha2
	if existingRoute == nil {
		return false
	}

	// Check support status for both API versions
	infPoolV1Alpha2Support := IsInferencePoolV1Alpha2Supported(existingRoute)
	infPoolV1Support := IsInferencePoolV1Supported(existingRoute)

	// Switch to v1 if:
	// - Already migrated (annotation exists - one-way lock), OR
	// - Gateway accepted v1 (detected from HTTPRoute status), OR
	// - Gateway rejected v1alpha2 (detected from HTTPRoute status)
	if isMigrated || infPoolV1Support == metav1.ConditionTrue || infPoolV1Alpha2Support == metav1.ConditionFalse {
		logger.Info("Using InferencePool v1 API for HTTPRoute",
			"isMigrated", isMigrated,
			"infPoolV1Support", infPoolV1Support,
			"infPoolV1Alpha2Support", infPoolV1Alpha2Support,
		)
		return true
	}

	logger.V(1).Info("Using InferencePool v1alpha2 API for HTTPRoute",
		"isMigrated", isMigrated,
		"infPoolV1Support", infPoolV1Support,
		"infPoolV1Alpha2Support", infPoolV1Alpha2Support,
	)

	// Default: stay on v1alpha2
	return false
}

// migrateHTTPRouteToV1 updates the default InferencePool backendRef in the HTTPRoute
// to use the v1 API group (inference.networking.k8s.io).
// Only updates backendRefs that match the expected pool name for this LLMInferenceService.
func migrateHTTPRouteToV1(llmSvc *v1alpha1.LLMInferenceService, route *gatewayapi.HTTPRoute) {
	v1Group := gatewayapi.Group(constants.InferencePoolV1Group)

	for i := range route.Spec.Rules {
		for j := range route.Spec.Rules[i].BackendRefs {
			backendRef := &route.Spec.Rules[i].BackendRefs[j]
			// Only update the default InferencePool backendRef for this LLMInferenceService
			if isDefaultInferencePoolBackendRef(llmSvc, backendRef.BackendRef) {
				backendRef.Group = &v1Group
			}
		}
	}
}

// isDefaultInferencePoolBackendRef checks if a backendRef is the default InferencePool
// for this LLMInferenceService. Matches both v1alpha2 and v1 groups.
func isDefaultInferencePoolBackendRef(llmSvc *v1alpha1.LLMInferenceService, ref gatewayapi.BackendRef) bool {
	defaultInfPoolName := (&v1alpha1.SchedulerSpec{}).InferencePoolName(llmSvc)
	group := ""
	if ref.Group != nil {
		group = string(*ref.Group)
	}
	kind := ""
	if ref.Kind != nil {
		kind = string(*ref.Kind)
	}

	// Check if it's an InferencePool with the expected name (either v1alpha2 or v1 group)
	isInferencePool := kind == "InferencePool" &&
		(group == constants.InferencePoolV1Alpha2Group || group == constants.InferencePoolV1Group)

	return isInferencePool && string(ref.Name) == defaultInfPoolName
}

func (r *LLMInferenceServiceReconciler) updateRoutingStatus(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService, routes ...*gatewayapi.HTTPRoute) error {
	logger := log.FromContext(ctx)

	if utils.GetForceStopRuntime(llmSvc) {
		llmSvc.Status.Addresses = nil
		llmSvc.Status.Address = nil
		llmSvc.MarkHTTPRoutesNotReady("Stopped", "Service is stopped")
		return nil
	}

	if llmSvc.Spec.Router == nil || llmSvc.Spec.Router.Route == nil {
		llmSvc.Status.Addresses = []duckv1.Addressable{{
			URL: apis.HTTPS(network.GetServiceHostname(
				kmeta.ChildName(llmSvc.GetName(), "-kserve-workload-svc"),
				llmSvc.GetNamespace(),
			)),
		}}
		return nil
	}

	cfg, err := LoadConfig(ctx, r.Clientset)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	var urls []*apis.URL
	for _, route := range routes {
		discoverURL, err := DiscoverURLs(ctx, r.Client, route, cfg.UrlScheme)
		if IgnoreNoURLsDiscovered(err) != nil {
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
		llmSvc.Status.URL = nil
	} else {
		llmSvc.Status.URL = externalURLs[0]
	}

	llmSvc.Status.Addresses = make([]duckv1.Addressable, 0, len(urls))
	for _, url := range urls {
		addressType := AddressTypeName(url)
		llmSvc.Status.Addresses = append(llmSvc.Status.Addresses, duckv1.Addressable{
			Name: &addressType,
			URL:  url,
		})
	}

	return nil
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

// EvaluateGatewayConditions evaluates the readiness of all Gateways referenced by the LLMInferenceService
// and updates the GatewaysReady condition accordingly
func (r *LLMInferenceServiceReconciler) EvaluateGatewayConditions(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService) error {
	logger := log.FromContext(ctx).WithName("evaluateGatewayConditions")

	if utils.GetForceStopRuntime(llmSvc) {
		llmSvc.MarkGatewaysNotReady("Stopped", "Service is stopped")
		return nil
	}

	// If no router or gateway configuration, mark as ready to clear any previous stopped state
	if llmSvc.Spec.Router == nil || !llmSvc.Spec.Router.Gateway.HasRefs() {
		logger.Info("No Gateway references found, skipping Gateway condition evaluation")
		llmSvc.MarkGatewaysReadyUnset()
		return nil
	}

	gateways, err := r.CollectReferencedGateways(ctx, llmSvc)
	if err != nil {
		llmSvc.MarkGatewaysNotReady("GatewayFetchError", "Failed to fetch referenced Gateways: %v", err.Error())
		return fmt.Errorf("failed to fetch referenced gateways: %w", err)
	}

	notReadyGateways := EvaluateGatewayReadiness(ctx, gateways)

	if len(notReadyGateways) > 0 {
		gatewayNames := make([]string, len(notReadyGateways))
		for i, gw := range notReadyGateways {
			gatewayNames[i] = fmt.Sprintf("%s/%s", gw.Namespace, gw.Name)
		}
		llmSvc.MarkGatewaysNotReady("GatewaysNotReady", "The following Gateways are not ready: %v", gatewayNames)
		logger.V(2).Info("Some referenced Gateways are not ready", "gateways", notReadyGateways)
		return nil
	}
	llmSvc.MarkGatewaysReady()
	logger.Info("All referenced Gateways are ready")
	return nil
}

// CollectReferencedGateways retrieves all Gateway objects referenced in the LLMInferenceService spec
func (r *LLMInferenceServiceReconciler) CollectReferencedGateways(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService) ([]*gatewayapi.Gateway, error) {
	if llmSvc.Spec.Router == nil || !llmSvc.Spec.Router.Gateway.HasRefs() {
		return nil, nil
	}

	// Use a map to ensure gateways are not repeated (keyed by namespace/name)
	gatewayMap := make(map[string]*gatewayapi.Gateway)
	routes, err := r.collectReferencedRoutes(ctx, llmSvc)
	if err != nil {
		return nil, fmt.Errorf("failed to collect referenced routes: %w", err)
	}

	if llmSvc.Spec.Router.Route != nil && llmSvc.Spec.Router.Route.HTTP.HasSpec() {
		expected := r.expectedHTTPRoute(ctx, llmSvc, nil)
		curr := &gatewayapi.HTTPRoute{}
		if err := r.Get(ctx, client.ObjectKeyFromObject(expected), curr); err != nil {
			return nil, fmt.Errorf("failed to fetch HTTPRoute %s/%s: %w", expected.Namespace, expected.Name, err)
		}
		routes = append(routes, curr)
	}

	for _, route := range routes {
		discoveredGateways, err := DiscoverGateways(ctx, r.Client, route)
		if err != nil {
			return nil, fmt.Errorf("failed to discover gateways: %w", err)
		}
		for _, gateway := range discoveredGateways {
			key := gateway.gateway.Namespace + "/" + gateway.gateway.Name
			gatewayMap[key] = gateway.gateway
		}
	}

	for _, ref := range llmSvc.Spec.Router.Gateway.Refs {
		gateway := &gatewayapi.Gateway{}
		gatewayKey := types.NamespacedName{
			Name:      string(ref.Name),
			Namespace: string(ref.Namespace),
		}

		// If namespace is not specified, use the same namespace as the LLMInferenceService
		if gatewayKey.Namespace == "" {
			gatewayKey.Namespace = llmSvc.GetNamespace()
		}

		err := r.Client.Get(ctx, gatewayKey, gateway)
		if err != nil {
			return nil, fmt.Errorf("failed to get Gateway %s: %w", gatewayKey, err)
		}

		key := gateway.Namespace + "/" + gateway.Name
		gatewayMap[key] = gateway
	}

	// Convert map values to slice
	gateways := make([]*gatewayapi.Gateway, 0, len(gatewayMap))
	for _, gw := range gatewayMap {
		gateways = append(gateways, gw)
	}

	return gateways, nil
}

// EvaluateHTTPRouteConditions evaluates the readiness of all HTTPRoutes referenced by the LLMInferenceService
// and updates the HTTPRoutesReady condition accordingly
func (r *LLMInferenceServiceReconciler) EvaluateHTTPRouteConditions(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService) error {
	logger := log.FromContext(ctx).WithName("evaluateHTTPRouteConditions")

	if utils.GetForceStopRuntime(llmSvc) {
		llmSvc.MarkHTTPRoutesNotReady("Stopped", "Service is stopped")
		return nil
	}

	// If no router or route configuration, mark HTTPRoutes as ready (no routes to evaluate)
	if llmSvc.Spec.Router == nil || llmSvc.Spec.Router.Route == nil || llmSvc.Spec.Router.Route.HTTP == nil {
		logger.Info("No HTTPRoute configuration found, clearing HTTPRoutesReady condition")
		llmSvc.MarkHTTPRoutesReadyUnset()
		return nil
	}

	// Collect all HTTPRoutes (both referenced and managed)
	var allRoutes []*gatewayapi.HTTPRoute

	// Get referenced routes
	referencedRoutes, err := r.collectReferencedRoutes(ctx, llmSvc)
	if err != nil {
		llmSvc.MarkHTTPRoutesNotReady("HTTPRouteFetchError", "Failed to fetch referenced HTTPRoutes: %v", err.Error())
		return fmt.Errorf("failed to fetch referenced HTTPRoutes: %w", err)
	}
	allRoutes = append(allRoutes, referencedRoutes...)

	// Get managed route if it exists
	if llmSvc.Spec.Router.Route.HTTP.HasSpec() {
		expectedHTTPRoute := r.expectedHTTPRoute(ctx, llmSvc, nil)
		// Try to get the actual managed route from the cluster
		managedRoute := &gatewayapi.HTTPRoute{}
		if err := r.Client.Get(ctx, types.NamespacedName{
			Namespace: expectedHTTPRoute.Namespace,
			Name:      expectedHTTPRoute.Name,
		}, managedRoute); err == nil {
			allRoutes = append(allRoutes, managedRoute)
		}
	}

	// If no routes found, mark as ready (nothing to evaluate)
	if len(allRoutes) == 0 {
		llmSvc.MarkHTTPRoutesReady()
		logger.Info("No HTTPRoutes found, marking HTTPRoutesReady as true")
		return nil
	}

	notReadyRoutes := EvaluateHTTPRouteReadiness(ctx, llmSvc, allRoutes)

	if len(notReadyRoutes) > 0 {
		nonReadyRouteMessages := make([]string, len(notReadyRoutes))
		for i, route := range notReadyRoutes {
			topLevelCondition, _ := nonReadyHTTPRouteTopLevelCondition(llmSvc, route)
			if topLevelCondition != nil {
				nonReadyRouteMessages[i] = fmt.Sprintf("%s/%s: %v=%#v (reason %q, message %q)", route.Namespace, route.Name, topLevelCondition.Type, topLevelCondition.Status, topLevelCondition.Reason, topLevelCondition.Message)
			} else {
				nonReadyRouteMessages[i] = fmt.Sprintf("%s/%s: %#v", route.Namespace, route.Name, route.Status)
			}
		}
		llmSvc.MarkHTTPRoutesNotReady("HTTPRoutesNotReady", "The following HTTPRoutes are not ready: %v", nonReadyRouteMessages)
		logger.V(2).Info("Some HTTPRoutes are not ready", "routes", notReadyRoutes)
		return nil
	}

	llmSvc.MarkHTTPRoutesReady()
	logger.V(2).Info("All HTTPRoutes are ready", "routes", allRoutes)
	return nil
}

// EvaluateInferencePoolConditions evaluates the readiness of all Inference Pools in the LLMInferenceService
// and updates the InferencePoolReady condition accordingly
func (r *LLMInferenceServiceReconciler) EvaluateInferencePoolConditions(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService) error {
	logger := log.FromContext(ctx).WithName("EvaluateInferencePoolConditions")

	if utils.GetForceStopRuntime(llmSvc) {
		llmSvc.MarkInferencePoolNotReady("Stopped", "Service is stopped")
		return nil
	}

	// If no router or scheduler configuration, mark Inference Pools as ready (no Inference Pools to evaluate)
	if llmSvc.Spec.Router == nil || llmSvc.Spec.Router.Scheduler == nil {
		logger.V(2).Info("Scheduler is disabled, clearing InferencePoolReady condition")
		llmSvc.MarkInferencePoolReadyUnset()
		return nil
	}

	curr := &igwapi.InferencePool{}

	if llmSvc.Spec.Router.Scheduler.Pool != nil && llmSvc.Spec.Router.Scheduler.Pool.Ref != nil && llmSvc.Spec.Router.Scheduler.Pool.Ref.Name != "" {
		poolRef := llmSvc.Spec.Router.Scheduler.Pool.Ref
		err := r.Client.Get(ctx, types.NamespacedName{Namespace: llmSvc.Namespace, Name: poolRef.Name}, curr)
		if err != nil {
			err := fmt.Errorf("failed to fetch referenced Inference Pool %s/%s: %w", llmSvc.Namespace, poolRef.Name, err)
			llmSvc.MarkInferencePoolNotReady("InferencePoolFetchError", err.Error())
			return err
		}
	} else {
		expected := r.expectedSchedulerInferencePool(ctx, llmSvc)
		err := r.Client.Get(ctx, types.NamespacedName{Namespace: expected.Namespace, Name: expected.Name}, curr)
		if err != nil {
			err := fmt.Errorf("failed to fetch embedded Inference Pool %s/%s: %w", llmSvc.Namespace, llmSvc.Name, err)
			llmSvc.MarkInferencePoolNotReady("InferencePoolFetchError", err.Error())
			return err
		}
	}

	if !IsInferencePoolReady(curr) {
		topLevelCondition, _ := nonReadyInferencePoolTopLevelCondition(curr)
		if topLevelCondition != nil {
			llmSvc.MarkInferencePoolNotReady("InferencePoolNotReady", fmt.Sprintf(
				"%s/%s: %v=%#v (reason %q, message %q)",
				curr.Namespace,
				curr.Name,
				topLevelCondition.Type,
				topLevelCondition.Status,
				topLevelCondition.Reason,
				topLevelCondition.Message,
			))
		} else {
			llmSvc.MarkInferencePoolNotReady("InferencePoolNotReady", fmt.Sprintf("The inference pool %s/%s is not ready", curr.Namespace, curr.Name))
		}
		return nil
	}

	llmSvc.MarkInferencePoolReady()
	logger.V(2).Info("Inference Pool is ready", "pool", curr)
	return nil
}
