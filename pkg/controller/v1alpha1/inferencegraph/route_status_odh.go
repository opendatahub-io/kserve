//go:build distro

package inferencegraph

import (
	routev1 "github.com/openshift/api/route/v1"
	corev1 "k8s.io/api/core/v1"
	"knative.dev/pkg/apis"
)

func routeHostname(route *routev1.Route) string {
	for _, ingress := range route.Status.Ingress {
		if ingress.Host == "" {
			continue
		}
		for _, condition := range ingress.Conditions {
			if condition.Type == routev1.RouteAdmitted && condition.Status == corev1.ConditionTrue {
				return ingress.Host
			}
		}
	}
	return ""
}

func routeStatusURL(url *apis.URL, hostname string) *apis.URL {
	if hostname == "" {
		return nil
	}
	url.Host = hostname
	url.Scheme = "https"
	return url
}
