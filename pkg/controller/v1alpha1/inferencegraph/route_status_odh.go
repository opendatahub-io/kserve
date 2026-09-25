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
