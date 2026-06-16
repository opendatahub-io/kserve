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
	"cmp"
	"context"
	"fmt"
	"slices"

	routev1 "github.com/openshift/api/route/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/utils/ptr"
	"knative.dev/pkg/apis"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const gatewayNameLabel = "gateway.networking.k8s.io/gateway-name"

// gatewayWithPaths pairs a gateway reference with the URL paths discovered for it.
type gatewayWithPaths struct {
	ref   gwapiv1.ObjectReference
	paths []string
}

// discoverAdditionalURLs discovers OpenShift Routes that provide external
// access to Gateways lacking external URLs in their status addresses.
func (r *LLMISVCReconciler) discoverAdditionalURLs(ctx context.Context, discovered []DiscoveredURL) ([]DiscoveredURL, error) {
	gateways := gatewaysWithoutExternalURLs(discovered)
	if len(gateways) == 0 {
		return nil, nil
	}

	existingURLs := make(map[string]bool, len(discovered))
	for _, d := range discovered {
		existingURLs[d.URL.String()] = true
	}

	var additional []DiscoveredURL

	for _, gw := range gateways {
		routeURLs, err := discoverRouteURLs(ctx, r.Client, gw)
		if err != nil {
			return nil, fmt.Errorf("failed to discover Routes for gateway %s/%s: %w",
				ptr.Deref(gw.ref.Namespace, ""), gw.ref.Name, err)
		}

		for _, u := range routeURLs {
			if !existingURLs[u.URL.String()] {
				additional = append(additional, u)
				existingURLs[u.URL.String()] = true
			}
		}
	}

	return additional, nil
}

// discoverRouteURLs finds OpenShift Routes that front a gateway's services
// and returns DiscoveredURLs with Route origin - one per Route per path.
func discoverRouteURLs(ctx context.Context, c client.Client, gw gatewayWithPaths) ([]DiscoveredURL, error) {
	ns := string(ptr.Deref(gw.ref.Namespace, ""))
	gwName := string(gw.ref.Name)

	svcNames, err := gatewayServiceNames(ctx, c, ns, gwName)
	if err != nil {
		return nil, err
	}
	if len(svcNames) == 0 {
		return nil, nil
	}

	routeList := &routev1.RouteList{}
	if err := c.List(ctx, routeList, client.InNamespace(ns)); err != nil {
		if meta.IsNoMatchError(err) {
			log.FromContext(ctx).V(1).Info("OpenShift Route CRD (route.openshift.io/v1) not available, skipping Route discovery")

			return nil, nil
		}

		return nil, fmt.Errorf("failed to list Routes in %s: %w", ns, err)
	}

	var urls []DiscoveredURL
	for i := range routeList.Items {
		route := &routeList.Items[i]

		// Kind defaults to "Service" when empty per OpenShift API
		if (route.Spec.To.Kind != "" && route.Spec.To.Kind != "Service") || !svcNames[route.Spec.To.Name] {
			continue
		}

		host := admittedHost(route)
		if host == "" {
			continue
		}

		// Routes with path-based routing are incompatible with gateway URL
		// discovery. HAProxy uses different regex patterns for rewrite-target
		// "/" vs other values, making reliable reverse-computation of the
		// client-facing URL fragile. In practice, Routes fronting gateway
		// services are host-only; path-based variants are rare and likely
		// misconfigured for this use case.
		if route.Spec.Path != "" {
			log.FromContext(ctx).V(1).Info("skipping Route with path-based routing incompatible with gateway URL discovery",
				"route", route.Name, "namespace", route.Namespace, "path", route.Spec.Path)

			continue
		}

		origin := &gwapiv1.ObjectReference{
			Group:     gwapiv1.Group(routev1.GroupName),
			Kind:      "Route",
			Name:      gwapiv1.ObjectName(route.Name),
			Namespace: ptr.To(gwapiv1.Namespace(route.Namespace)),
		}

		for _, gwPath := range gw.paths {
			u := routeURL(route, host)
			u.Path = gwPath

			urls = append(urls, DiscoveredURL{URL: u, Origin: origin})
		}
	}

	return urls, nil
}

// gatewayServiceNames returns the names of all Services in the namespace whose
// pod selector routes traffic to the given gateway's pods.
// Uses Spec.Selector (not metadata labels) because operator-created services
// (e.g. <gw>-data-science-gateway-class) may lack the gateway label on their
// metadata but still select the same pods via the pod selector.
func gatewayServiceNames(ctx context.Context, c client.Client, ns, gwName string) (map[string]bool, error) {
	svcList := &corev1.ServiceList{}
	if err := c.List(ctx, svcList, client.InNamespace(ns)); err != nil {
		return nil, fmt.Errorf("failed to list Services in %s: %w", ns, err)
	}

	names := make(map[string]bool)
	for _, svc := range svcList.Items {
		if svc.Spec.Selector[gatewayNameLabel] == gwName {
			names[svc.Name] = true
		}
	}

	return names, nil
}

// routeURL builds a URL from a Route's host, deriving the scheme from its TLS config.
func routeURL(route *routev1.Route, host string) *apis.URL {
	if route.Spec.TLS == nil {
		return apis.HTTP(host)
	}

	return apis.HTTPS(host)
}

// admittedHost returns the host of an admitted Route, or empty string if not admitted.
func admittedHost(route *routev1.Route) string {
	for _, ingress := range route.Status.Ingress {
		for _, cond := range ingress.Conditions {
			if cond.Type == routev1.RouteAdmitted && cond.Status == corev1.ConditionTrue {
				return ingress.Host
			}
		}
	}

	return ""
}

// gatewaysWithoutExternalURLs returns gateways that have no external URLs
// in their discovered addresses, paired with the unique paths for each.
func gatewaysWithoutExternalURLs(discovered []DiscoveredURL) []gatewayWithPaths {
	type gwKey struct{ ns, name string }
	pathsByGW := make(map[gwKey]map[string]bool)
	hasExternal := make(map[gwKey]bool)
	orderSeen := make(map[gwKey]gwapiv1.ObjectReference)

	for _, d := range discovered {
		if d.Origin == nil || d.Origin.Kind != "Gateway" {
			continue
		}

		key := gwKey{
			ns:   string(ptr.Deref(d.Origin.Namespace, "")),
			name: string(d.Origin.Name),
		}

		if _, ok := orderSeen[key]; !ok {
			orderSeen[key] = *d.Origin
		}

		if IsExternalURL(d.URL) {
			hasExternal[key] = true
		}

		if pathsByGW[key] == nil {
			pathsByGW[key] = make(map[string]bool)
		}
		pathsByGW[key][d.URL.Path] = true
	}

	var result []gatewayWithPaths
	for key, ref := range orderSeen {
		if hasExternal[key] {
			continue
		}

		paths := make([]string, 0, len(pathsByGW[key]))
		for p := range pathsByGW[key] {
			paths = append(paths, p)
		}
		slices.Sort(paths)

		result = append(result, gatewayWithPaths{ref: ref, paths: paths})
	}

	slices.SortFunc(result, func(a, b gatewayWithPaths) int {
		if c := cmp.Compare(string(ptr.Deref(a.ref.Namespace, "")), string(ptr.Deref(b.ref.Namespace, ""))); c != 0 {
			return c
		}

		return cmp.Compare(string(a.ref.Name), string(b.ref.Name))
	})

	return result
}
