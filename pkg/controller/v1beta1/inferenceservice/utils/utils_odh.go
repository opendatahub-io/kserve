//go:build distro

/*
Copyright 2021 The KServe Authors.

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

package utils

import (
	"context"
	"fmt"

	routev1 "github.com/openshift/api/route/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"knative.dev/pkg/apis"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// GetRouteURLIfExists checks for an OpenShift Route created by odh-model-controller.
// If the route is found and admitted, it returns the route URL.
func GetRouteURLIfExists(ctx context.Context, cli client.Client, metadata metav1.ObjectMeta, isvcName string) (*apis.URL, error) {
	foundRoute := false
	routeReady := false
	route := &routev1.Route{}
	err := cli.Get(ctx, types.NamespacedName{Name: isvcName, Namespace: metadata.Namespace}, route)
	if err != nil {
		return nil, err
	}

	// Check if the route is owned by the InferenceService
	for _, ownerRef := range route.OwnerReferences {
		if ownerRef.UID == metadata.UID {
			foundRoute = true
		}
	}

	// Check if the route is admitted
	for _, ingress := range route.Status.Ingress {
		for _, condition := range ingress.Conditions {
			if condition.Type == routev1.RouteAdmitted && condition.Status == corev1.ConditionTrue {
				routeReady = true
			}
		}
	}

	if !foundRoute || !routeReady {
		return nil, fmt.Errorf("route %s/%s not found or not ready", metadata.Namespace, isvcName)
	}

	// Construct the URL
	host := route.Spec.Host
	scheme := "http"
	if route.Spec.TLS != nil && route.Spec.TLS.Termination != "" {
		scheme = "https"
	}

	// Create the URL as an apis.URL object
	routeURL := &apis.URL{
		Scheme: scheme,
		Host:   host,
	}

	return routeURL, nil
}
