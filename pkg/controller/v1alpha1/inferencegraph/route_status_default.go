//go:build !distro

package inferencegraph

import (
	routev1 "github.com/openshift/api/route/v1"
	"knative.dev/pkg/apis"
)

func routeHostname(route *routev1.Route) string {
	for _, ingress := range route.Status.Ingress {
		return ingress.Host
	}
	return ""
}

func routeStatusURL(url *apis.URL, hostname string) *apis.URL {
	url.Host = hostname
	url.Scheme = "https"
	return url
}
