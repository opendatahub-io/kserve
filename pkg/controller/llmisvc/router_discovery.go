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
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"knative.dev/pkg/apis"
	"knative.dev/pkg/network"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	igwapi "sigs.k8s.io/gateway-api-inference-extension/api/v1alpha2"
	gatewayapi "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
	"github.com/kserve/kserve/pkg/constants"
)

var wildcardHostname = constants.GetEnvOrDefault("GATEWAY_API_WILDCARD_HOSTNAME", "inference")

type resolvedGateway struct {
	gateway      *gatewayapi.Gateway
	gatewayClass *gatewayapi.GatewayClass
	parentRef    gatewayapi.ParentReference
}

func DiscoverGateways(ctx context.Context, c client.Client, route *gatewayapi.HTTPRoute) ([]resolvedGateway, error) {
	gateways := make([]resolvedGateway, 0)
	for _, parentRef := range route.Spec.ParentRefs {
		ns := ptr.Deref((&parentRef).Namespace, gatewayapi.Namespace(route.Namespace))
		gwNS, gwName := string(ns), string((&parentRef).Name)

		gateway := &gatewayapi.Gateway{}
		if err := c.Get(ctx, types.NamespacedName{Namespace: gwNS, Name: gwName}, gateway); err != nil {
			return nil, fmt.Errorf("failed to get Gateway %s/%s for route %s/%s: %w", gwNS, gwName, route.Namespace, route.Name, err)
		}

		gatewayClass := &gatewayapi.GatewayClass{}
		if err := c.Get(ctx, types.NamespacedName{Name: string(gateway.Spec.GatewayClassName)}, gatewayClass); err != nil {
			return nil, fmt.Errorf("failed to get GatewayClass %q for gateway %s/%s: %w", string(gateway.Spec.GatewayClassName), gwNS, gwName, err)
		}
		gateways = append(gateways, resolvedGateway{
			gateway:      gateway,
			gatewayClass: gatewayClass,
			parentRef:    parentRef,
		})
	}
	return gateways, nil
}

// DiscoverGatewayServiceHost attempts to find the cluster-local hostname
// for the Service backing a Gateway.
// Returns empty string if no backing service is found (not an error).
func DiscoverGatewayServiceHost(ctx context.Context, c client.Client, gateway *gatewayapi.Gateway) (string, error) {
	logger := log.FromContext(ctx)

	// Look for Service with known gateway label first
	svcList := &corev1.ServiceList{}
	if err := c.List(ctx, svcList,
		client.InNamespace(gateway.Namespace),
		client.MatchingLabels{
			"gateway.networking.k8s.io/gateway-name": gateway.Name,
		},
	); err != nil {
		return "", fmt.Errorf("failed to list services for gateway %s/%s: %w", gateway.Namespace, gateway.Name, err)
	}
	if len(svcList.Items) > 0 {
		svc := &svcList.Items[0]
		host := network.GetServiceHostname(svc.Name, svc.Namespace)
		logger.V(1).Info("Discovered gateway service via label", "gateway", gateway.Name, "service", svc.Name, "host", host)
		return host, nil
	}

	// Fallback: Look for Service with the same name as Gateway
	svc := &corev1.Service{}
	err := c.Get(ctx, types.NamespacedName{
		Namespace: gateway.Namespace,
		Name:      gateway.Name,
	}, svc)
	if err == nil {
		host := network.GetServiceHostname(svc.Name, svc.Namespace)
		logger.V(1).Info("Discovered gateway service via name match", "gateway", gateway.Name, "service", svc.Name, "host", host)
		return host, nil
	}
	if !apierrors.IsNotFound(err) {
		return "", fmt.Errorf("failed to get service %s/%s: %w", gateway.Namespace, gateway.Name, err)
	}

	// No backing service found - not an error - we are guessing here
	logger.V(1).Info("No backing service found for gateway", "gateway", fmt.Sprintf("%s/%s", gateway.Namespace, gateway.Name))
	return "", nil
}

func DiscoverURLs(ctx context.Context, c client.Client, route *gatewayapi.HTTPRoute, preferredUrlScheme string) ([]*apis.URL, error) {
	var urls []*apis.URL

	gateways, err := DiscoverGateways(ctx, c, route)
	if err != nil {
		return nil, fmt.Errorf("failed to discover gateways: %w", err)
	}

	for _, g := range gateways {
		listeners, err := selectListeners(g.gateway, g.parentRef.SectionName, preferredUrlScheme)
		if err != nil {
			return nil, fmt.Errorf("failed to select listeners for gateway %s/%s: %w", g.gateway.Namespace, g.gateway.Name, err)
		}

		path := extractRoutePath(route)
		addresses := g.gateway.Status.Addresses

		// Discover external URLs from Gateway status addresses (if available)
		if len(addresses) > 0 {
			for _, listener := range listeners {
				scheme, err := resolveScheme(listener)
				if err != nil {
					return nil, fmt.Errorf("failed to resolve scheme for gateway %s/%s listener %s: %w",
						g.gateway.Namespace, g.gateway.Name, listener.Name, err)
				}

				hostnames := extractHostnamesForListener(route, listener, addresses)
				gatewayURLs, err := combineIntoURLs(hostnames, scheme, listener.Port, path)
				if err != nil {
					return nil, fmt.Errorf("failed to combine URLs for Gateway %s/%s: %w", g.gateway.Namespace, g.gateway.Name, err)
				}
				urls = append(urls, gatewayURLs...)
			}
		}

		// Discover internal URL from Gateway backing service
		internalHost, err := DiscoverGatewayServiceHost(ctx, c, g.gateway)
		if err != nil {
			return nil, fmt.Errorf("failed to discover gateway service host for %s/%s: %w", g.gateway.Namespace, g.gateway.Name, err)
		}
		if internalHost != "" {
			// Use first listener's scheme and port - internal service matches Gateway listener
			listener := listeners[0]
			internalURLs, err := combineIntoURLs([]string{internalHost}, schemeForProtocol(listener.Protocol), listener.Port, path)
			if err != nil {
				return nil, fmt.Errorf("failed to build internal URL for Gateway %s/%s: %w", g.gateway.Namespace, g.gateway.Name, err)
			}
			urls = append(urls, internalURLs...)
		}
	}

	// Error only if no URLs discovered at all (neither external nor internal)
	if len(urls) == 0 && len(gateways) > 0 {
		g := gateways[0]
		return nil, &NoURLsDiscoveredError{
			GatewayNamespace: g.gateway.Namespace,
			GatewayName:      g.gateway.Name,
		}
	}

	return urls, nil
}

// extractHostnamesForListener determines the hostnames to use for URL generation
func extractHostnamesForListener(route *gatewayapi.HTTPRoute, listener *gatewayapi.Listener, addresses []gatewayapi.GatewayStatusAddress) []string {
	hostnames := extractRouteHostnames(route)
	// If Hostname is set in the spec, use the Hostname specified.
	// Using the LoadBalancer addresses in `Gateway.Status.Addresses` will return 404 in those cases.
	if len(hostnames) == 0 && listener.Hostname != nil && *listener.Hostname != "" {
		if host, isWildcard := strings.CutPrefix(string(*listener.Hostname), "*."); isWildcard {
			// Hostnames that are prefixed with a wildcard label (`*.`) are interpreted
			// as a suffix match. That means that a match for `*.example.com` would match
			// both `test.example.com`, and `foo.test.example.com`, but not `example.com`.
			hostnames = append(hostnames, fmt.Sprintf("%s.%s", wildcardHostname, host))
		} else {
			hostnames = []string{host}
		}
	}
	if len(hostnames) == 0 {
		hostnames = extractAddressValues(addresses)
	}
	return hostnames
}

// extractRoutePath extracts the path from the HTTPRoute rules.
// Returns the shortest path (by slash count) from rules referencing a Service backend.
func extractRoutePath(route *gatewayapi.HTTPRoute) string {
	serviceKind := gatewayapi.Kind("Service")
	servicePaths := []string{}
	paths := []string{}
	for _, rule := range route.Spec.Rules {
		serviceFound := false
		for _, backendRef := range rule.BackendRefs {
			if backendRef.Kind == &serviceKind {
				serviceFound = true
				break
			}
		}
		for _, match := range rule.Matches {
			if serviceFound {
				servicePaths = append(servicePaths, ptr.Deref(match.Path.Value, "/"))
			} else {
				paths = append(paths, ptr.Deref(match.Path.Value, "/"))
			}
		}
	}

	// Paths set in rules referencing a Service as the backend will take priority
	if len(servicePaths) > 0 {
		paths = servicePaths
	}

	// If any paths are set in rules for the route, return the highest level path with the shortest length
	// TODO how do we deal with regexp
	// TODO how do we intelligently handle multiple rules
	shortestPath := "/"
	minSlashes := math.MaxInt
	for _, path := range paths {
		pathSlashes := strings.Count(path, "/")
		if pathSlashes < minSlashes || (pathSlashes == minSlashes && len(path) < len(shortestPath)) {
			shortestPath = path
			minSlashes = pathSlashes
		}
	}

	return shortestPath
}

// schemeForProtocol returns the URL scheme for a Gateway API protocol.
// Returns empty string for protocols that don't support HTTP routing.
func schemeForProtocol(protocol gatewayapi.ProtocolType) string {
	switch protocol {
	case gatewayapi.HTTPProtocolType, gatewayapi.HTTPSProtocolType:
		return strings.ToLower(string(protocol))
	case gatewayapi.TLSProtocolType:
		return "https"
	default:
		return ""
	}
}

// selectListeners returns the applicable listeners for URL generation.
// - If sectionName is provided, returns only that specific listener
// - Otherwise, returns ALL HTTP-capable listeners sorted by: preferredScheme first, then HTTPS, then HTTP
func selectListeners(gateway *gatewayapi.Gateway, sectionName *gatewayapi.SectionName, preferredScheme string) ([]*gatewayapi.Listener, error) {
	// If sectionName provided, find exact match (single listener)
	if sectionName != nil {
		for i := range gateway.Spec.Listeners {
			if gateway.Spec.Listeners[i].Name == *sectionName {
				return []*gatewayapi.Listener{&gateway.Spec.Listeners[i]}, nil
			}
		}
		return nil, fmt.Errorf("listener %q not found in gateway %s/%s", *sectionName, gateway.Namespace, gateway.Name)
	}

	// Collect all HTTP-capable listeners
	var listeners []*gatewayapi.Listener
	for i := range gateway.Spec.Listeners {
		if scheme := schemeForProtocol(gateway.Spec.Listeners[i].Protocol); scheme != "" {
			listeners = append(listeners, &gateway.Spec.Listeners[i])
		}
	}
	if len(listeners) == 0 {
		return nil, fmt.Errorf("no HTTP-capable listener found in gateway %s/%s", gateway.Namespace, gateway.Name)
	}

	// Sort: preferredScheme first, then HTTPS before HTTP
	precedence := func(l *gatewayapi.Listener) int {
		scheme := schemeForProtocol(l.Protocol)
		if scheme == preferredScheme {
			return 0
		}
		if scheme == "https" {
			return 1
		}
		return 2
	}
	slices.SortFunc(listeners, func(a, b *gatewayapi.Listener) int {
		return precedence(a) - precedence(b)
	})

	return listeners, nil
}

// resolveScheme returns the URL scheme derived from the listener's protocol.
func resolveScheme(listener *gatewayapi.Listener) (string, error) {
	scheme := schemeForProtocol(listener.Protocol)
	if scheme == "" {
		return "", fmt.Errorf("listener %q uses unsupported protocol %s for HTTP routing", listener.Name, listener.Protocol)
	}
	return scheme, nil
}

func extractRouteHostnames(route *gatewayapi.HTTPRoute) []string {
	var hostnames []string
	for _, h := range route.Spec.Hostnames {
		host := string(h)
		if host != "" && host != "*" {
			hostnames = append(hostnames, host)
		}
	}
	return hostnames
}

func extractAddressValues(addresses []gatewayapi.GatewayStatusAddress) []string {
	var values []string
	for _, addr := range addresses {
		if addr.Value != "" {
			values = append(values, addr.Value)
		}
	}
	return values
}

func combineIntoURLs(hostnames []string, scheme string, port gatewayapi.PortNumber, path string) ([]*apis.URL, error) {
	urls := make([]*apis.URL, 0, len(hostnames))

	sortedHostnames := make([]string, len(hostnames))
	copy(sortedHostnames, hostnames)
	slices.Sort(sortedHostnames)

	for _, hostname := range sortedHostnames {
		var urlStr string
		if (scheme == "http" && port != 80) || (scheme == "https" && port != 443) {
			urlStr = fmt.Sprintf("%s://%s%s", scheme, joinHostPort(hostname, &port), path)
		} else {
			urlStr = fmt.Sprintf("%s://%s%s", scheme, hostname, path)
		}

		url, err := apis.ParseURL(urlStr)
		if err != nil {
			return nil, fmt.Errorf("failed to parse URL %s: %w", urlStr, err)
		}

		urls = append(urls, url)
	}

	return urls, nil
}

func joinHostPort(host string, port *gatewayapi.PortNumber) string {
	if port != nil && *port != 0 {
		return net.JoinHostPort(host, fmt.Sprint(*port))
	}
	return host
}

type NoURLsDiscoveredError struct {
	GatewayNamespace string
	GatewayName      string
}

func (e *NoURLsDiscoveredError) Error() string {
	return fmt.Sprintf("no URLs discovered for Gateway %s/%s (no external addresses and no backing service found)", e.GatewayNamespace, e.GatewayName)
}

func IgnoreNoURLsDiscovered(err error) error {
	if IsNoURLsDiscovered(err) {
		return nil
	}
	return err
}

func IsNoURLsDiscovered(err error) bool {
	var noURLsErr *NoURLsDiscoveredError
	return errors.As(err, &noURLsErr)
}

// EvaluateGatewayReadiness checks the readiness status of Gateways and returns those that are not ready
func EvaluateGatewayReadiness(ctx context.Context, gateways []*gatewayapi.Gateway) []*gatewayapi.Gateway {
	logger := log.FromContext(ctx)
	notReadyGateways := make([]*gatewayapi.Gateway, 0)

	for _, gateway := range gateways {
		ready := IsGatewayReady(gateway)
		logger.Info("Gateway readiness evaluated", "gateway", fmt.Sprintf("%s/%s", gateway.Namespace, gateway.Name), "ready", ready)

		if !ready {
			notReadyGateways = append(notReadyGateways, gateway)
		}
	}

	return notReadyGateways
}

// IsGatewayReady determines if a Gateway is ready based on its status conditions
func IsGatewayReady(gateway *gatewayapi.Gateway) bool {
	// Check for the standard Gateway API "Programmed" condition
	for _, condition := range gateway.Status.Conditions {
		if condition.Type == string(gatewayapi.GatewayConditionProgrammed) {
			return condition.Status == metav1.ConditionTrue
		}
	}

	// If no Programmed condition is found, Gateway is considered not ready
	return false
}

// EvaluateHTTPRouteReadiness checks the readiness status of HTTPRoutes and returns those that are not ready
func EvaluateHTTPRouteReadiness(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService, routes []*gatewayapi.HTTPRoute) []*gatewayapi.HTTPRoute {
	logger := log.FromContext(ctx)
	notReadyRoutes := make([]*gatewayapi.HTTPRoute, 0)

	for _, route := range routes {
		ready := IsHTTPRouteReady(llmSvc, route)
		logger.Info("HTTPRoute readiness evaluated", "route", fmt.Sprintf("%s/%s", route.Namespace, route.Name), "ready", ready)

		if !ready {
			notReadyRoutes = append(notReadyRoutes, route)
		}
	}

	return notReadyRoutes
}

// IsHTTPRouteReady determines if an HTTPRoute is ready based on its status conditions.
func IsHTTPRouteReady(llmSvc *v1alpha1.LLMInferenceService, route *gatewayapi.HTTPRoute) bool {
	if route == nil || len(route.Spec.ParentRefs) == 0 {
		return false
	}

	if cond, missing := nonReadyHTTPRouteTopLevelCondition(llmSvc, route); cond != nil || missing {
		return false
	}

	return true
}

func nonReadyHTTPRouteTopLevelCondition(llmSvc *v1alpha1.LLMInferenceService, route *gatewayapi.HTTPRoute) (*metav1.Condition, bool) {
	if route == nil {
		return nil, true
	}

	routeConditionAcceptedMissing := true
	routeAuthEnforced := false

	for _, parent := range route.Status.RouteStatus.Parents {
		if parent.ControllerName == "kuadrant.io/policy-controller" && llmSvc.IsAuthEnabled() {
			cond := meta.FindStatusCondition(parent.Conditions, "kuadrant.io/AuthPolicyAffected")
			if cond == nil {
				continue
			}
			if cond.Status != metav1.ConditionTrue {
				return cond, false
			}
			routeAuthEnforced = true
			continue
		}

		cond := meta.FindStatusCondition(parent.Conditions, string(gatewayapi.RouteConditionAccepted))
		if cond == nil {
			// This can happen when multiple controllers write to the status, e.g., besides the gateway controller, there
			// are conditions reported from the policy controller.
			// See example https://gist.github.com/bartoszmajsak/4329206afe107357afdcb9b92ed778bd
			continue
		}
		routeConditionAcceptedMissing = false
		staleCondition := cond.ObservedGeneration > 0 && cond.ObservedGeneration < route.Generation
		if cond.Status != metav1.ConditionTrue || staleCondition {
			return cond, false
		}
	}

	if llmSvc.IsAuthEnabled() && !routeAuthEnforced {
		return &metav1.Condition{
			Type:    "kuadrant.io/AuthPolicyAffected",
			Status:  metav1.ConditionFalse,
			Reason:  "Authentication is not enforced",
			Message: "Either disable authentication with security.opendatahub.io/enable-auth=false annotation or install Red Hat Connectivity Link",
		}, false
	}

	return nil, routeConditionAcceptedMissing
}

// IsInferencePoolReady checks if an InferencePool has been accepted by all parents.
func IsInferencePoolReady(pool *igwapi.InferencePool) bool {
	if pool == nil || len(pool.Status.Parents) == 0 {
		return false
	}

	if cond, missing := nonReadyInferencePoolTopLevelCondition(pool); cond != nil || missing {
		return false
	}

	return true
}

func nonReadyInferencePoolTopLevelCondition(pool *igwapi.InferencePool) (*metav1.Condition, bool) {
	if pool == nil {
		return nil, true
	}

	for _, parent := range pool.Status.Parents {
		cond := meta.FindStatusCondition(parent.Conditions, string(igwapi.InferencePoolConditionAccepted))
		if cond == nil {
			return nil, true
		}
		staleCondition := cond.ObservedGeneration > 0 && cond.ObservedGeneration < pool.Generation
		if cond.Status != metav1.ConditionTrue || staleCondition {
			return cond, false
		}
	}

	return nil, false
}
