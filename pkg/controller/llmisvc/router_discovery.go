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
	"net"
	"slices"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"knative.dev/pkg/apis"

	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayapi "sigs.k8s.io/gateway-api/apis/v1"
)

func DiscoverURLs(ctx context.Context, c client.Client, route *gatewayapi.HTTPRoute) ([]*apis.URL, error) {
	var urls []*apis.URL

	for _, parentRef := range route.Spec.ParentRefs {
		ns := ptr.Deref((&parentRef).Namespace, gatewayapi.Namespace(route.Namespace))
		gwNS, gwName := string(ns), string((&parentRef).Name)

		gateway := &gatewayapi.Gateway{}
		if err := c.Get(ctx, types.NamespacedName{Namespace: gwNS, Name: gwName}, gateway); err != nil {
			return nil, fmt.Errorf("fetch Gateway %s/%s: %w", gwNS, gwName, err)
		}

		listener := selectListener(gateway, parentRef.SectionName)
		scheme := extractSchemeFromListener(listener)
		port := listener.Port

		addresses := gateway.Status.Addresses
		if len(addresses) == 0 {
			return nil, &ExternalAddressNotFoundError{
				GatewayNamespace: gateway.Namespace,
				GatewayName:      gateway.Name,
			}
		}

		hostnames := extractRouteHostnames(route)
		if len(hostnames) == 0 {
			hostnames = extractAddressValues(addresses)
		}

		path := extractRoutePath(route)

		gatewayURLs, err := combineIntoURLs(hostnames, scheme, port, path)
		if err != nil {
			return nil, fmt.Errorf("failed to combine URLs for Gateway %s/%s: %w", gwNS, gwName, err)
		}

		urls = append(urls, gatewayURLs...)
	}

	return urls, nil
}

func extractRoutePath(route *gatewayapi.HTTPRoute) string {
	if len(route.Spec.Rules) > 0 && len(route.Spec.Rules[0].Matches) > 0 {
		// TODO how do we deal with regexp
		return ptr.Deref(route.Spec.Rules[0].Matches[0].Path.Value, "/")
	}
	return "/"
}

func selectListener(gateway *gatewayapi.Gateway, sectionName *gatewayapi.SectionName) *gatewayapi.Listener {
	if sectionName != nil {
		for _, listener := range gateway.Spec.Listeners {
			if listener.Name == *sectionName {
				return &listener
			}
		}
	}

	return &gateway.Spec.Listeners[0]
}

func extractSchemeFromListener(listener *gatewayapi.Listener) string {
	if listener.Protocol == gatewayapi.HTTPSProtocolType {
		return "https"
	}
	return "http"
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

type ExternalAddressNotFoundError struct {
	GatewayNamespace string
	GatewayName      string
}

func (e *ExternalAddressNotFoundError) Error() string {
	return fmt.Sprintf("Gateway %s/%s has no external address found", e.GatewayNamespace, e.GatewayName)
}

func IgnoreExternalAddressNotFound(err error) error {
	if IsExternalAddressNotFound(err) {
		return nil
	}
	return err
}

func IsExternalAddressNotFound(err error) bool {
	var externalAddrNotFoundErr *ExternalAddressNotFoundError
	return errors.As(err, &externalAddrNotFoundErr)
}

func filter[T any](s []T, predicateFn func(T) bool) []T {
	out := make([]T, 0, len(s))
	for _, x := range s {
		if predicateFn(x) {
			out = append(out, x)
		}
	}
	return out
}
