/*
Copyright 2024 The KServe Authors.

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

package openshift

import (
	"context"
	"fmt"
	"strings"

	routev1 "github.com/openshift/api/route/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierr "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"knative.dev/pkg/apis"
	knapis "knative.dev/pkg/apis"
	duckv1 "knative.dev/pkg/apis/duck/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/kserve/kserve/pkg/apis/serving/v1beta1"
	"github.com/kserve/kserve/pkg/constants"
	v1beta1utils "github.com/kserve/kserve/pkg/controller/v1beta1/inferenceservice/utils"
	"github.com/kserve/kserve/pkg/utils"
)

// RouteReconciler reconciles the OpenShift route
type RouteReconciler struct {
	client        client.Client
	scheme        *runtime.Scheme
	ingressConfig *v1beta1.IngressConfig
	isvcConfig    *v1beta1.InferenceServicesConfig
}

func NewRouteReconciler(client client.Client, scheme *runtime.Scheme, ingressConfig *v1beta1.IngressConfig,
	isvcConfig *v1beta1.InferenceServicesConfig) *RouteReconciler {
	return &RouteReconciler{
		client:        client,
		scheme:        scheme,
		ingressConfig: ingressConfig,
		isvcConfig:    isvcConfig,
	}
}

func getServicePort(isvc *v1beta1.InferenceService) int32 {
	// If service has ODHKserveRawAuth annotation, use HTTPS port
	if val, ok := isvc.Annotations[constants.ODHKserveRawAuth]; ok && strings.EqualFold(val, "true") {
		return constants.OauthProxyPort
	}

	// If service is part of an InferenceGraph, use HTTPS port
	if val, ok := isvc.Labels[constants.InferenceGraphLabel]; ok && val == "true" {
		return 443
	}

	// Default to HTTP port
	return constants.CommonDefaultHttpPort
}

func createRouteSpec(serviceName string, port int32) routev1.RouteSpec {
	return routev1.RouteSpec{
		To: routev1.RouteTargetReference{
			Kind: "Service",
			Name: serviceName,
		},
		Port: &routev1.RoutePort{
			TargetPort: intstr.FromInt(int(port)),
		},
	}
}

func getTargetServiceName(ctx context.Context, isvc *v1beta1.InferenceService, client client.Client) string {
	// If transformer exists, use transformer service
	// Otherwise use predictor service
	if isvc.Spec.Transformer != nil {
		return constants.TransformerServiceName(isvc.Name)
	}
	return constants.PredictorServiceName(isvc.Name)
}

func (r *RouteReconciler) Reconcile(ctx context.Context, isvc *v1beta1.InferenceService) error {
	// Get service name and port
	serviceName := getTargetServiceName(ctx, isvc, r.client)
	servicePort := getServicePort(isvc)

	// Set internal URL in status
	internalHost := fmt.Sprintf("%s.%s.svc.cluster.local", serviceName, isvc.Namespace)
	scheme := "http"
	if val, ok := isvc.Annotations[constants.ODHKserveRawAuth]; ok && strings.EqualFold(val, "true") {
		scheme = "https"
		internalHost = fmt.Sprintf("%s:%d", internalHost, constants.OauthProxyPort)
	}

	// Set internal URLs
	internalURL := &apis.URL{
		Host:   internalHost,
		Scheme: scheme,
	}
	isvc.Status.Address = &duckv1.Addressable{
		URL: internalURL,
	}

	// Check network visibility
	visibility, ok := isvc.Labels[constants.NetworkVisibility]
	if !ok || visibility != constants.ODHRouteEnabled {
		// If not exposed, set Status.URL to internal URL as well
		isvc.Status.URL = &knapis.URL{
			Host:   internalHost,
			Scheme: scheme,
		}

		// If not exposed, delete any existing route
		route := &routev1.Route{}
		err := r.client.Get(ctx, types.NamespacedName{Name: isvc.Name, Namespace: isvc.Namespace}, route)
		if err == nil {
			// Route exists, delete it
			if err := r.client.Delete(ctx, route); err != nil {
				return fmt.Errorf("failed to delete route: %w", err)
			}
		} else if !apierr.IsNotFound(err) {
			// Error other than NotFound
			return fmt.Errorf("failed to get route: %w", err)
		}

		// Mark ingress as ready and return
		isvc.Status.SetCondition(v1beta1.IngressReady, &apis.Condition{
			Type:   v1beta1.IngressReady,
			Status: corev1.ConditionTrue,
		})
		return nil
	}

	// Create route
	route := &routev1.Route{
		ObjectMeta: metav1.ObjectMeta{
			Name:      isvc.Name,
			Namespace: isvc.Namespace,
			Labels:    isvc.Labels,
			Annotations: utils.Filter(isvc.Annotations, func(key string) bool {
				return !utils.Includes(r.isvcConfig.ServiceAnnotationDisallowedList, key)
			}),
		},
		Spec: createRouteSpec(serviceName, servicePort),
	}

	// Set controller reference
	if err := controllerutil.SetControllerReference(isvc, route, r.scheme); err != nil {
		return err
	}

	// Create or update route
	existing := &routev1.Route{}
	if err := r.client.Get(ctx, types.NamespacedName{Name: route.Name, Namespace: route.Namespace}, existing); err != nil {
		if apierr.IsNotFound(err) {
			if err := r.client.Create(ctx, route); err != nil {
				return err
			}
		} else {
			return err
		}
	} else {
		if !equality.Semantic.DeepEqual(route.Spec, existing.Spec) {
			route.ResourceVersion = existing.ResourceVersion
			if err := r.client.Update(ctx, route); err != nil {
				return err
			}
		}
		// Use existing route to check admission status
		route = existing
	}

	// Get OpenShift cluster domain
	domain, err := v1beta1utils.GetOpenShiftDomain(ctx, r.client)
	if err != nil {
		return fmt.Errorf("failed to get OpenShift domain: %w", err)
	}

	// Construct the host using the OpenShift domain
	host := fmt.Sprintf("%s-%s.%s", isvc.Name, isvc.Namespace, domain)

	scheme = "http"
	if val, ok := isvc.Annotations[constants.ODHKserveRawAuth]; ok && strings.EqualFold(val, "true") {
		scheme = "https"
	}

	isvc.Status.URL = &knapis.URL{
		Scheme: scheme,
		Host:   host,
	}

	// Only mark IngressReady as true if the route is admitted
	admitted := false
	for _, ingress := range route.Status.Ingress {
		for _, condition := range ingress.Conditions {
			if condition.Type == routev1.RouteAdmitted && condition.Status == corev1.ConditionTrue {
				admitted = true
				break
			}
		}
		if admitted {
			break
		}
	}

	if admitted {
		isvc.Status.SetCondition(v1beta1.IngressReady, &apis.Condition{
			Type:   v1beta1.IngressReady,
			Status: corev1.ConditionTrue,
		})
	} else {
		isvc.Status.SetCondition(v1beta1.IngressReady, &apis.Condition{
			Type:    v1beta1.IngressReady,
			Status:  corev1.ConditionFalse,
			Reason:  "RouteNotAdmitted",
			Message: "Route has not been admitted by OpenShift yet",
		})
	}

	return nil
}
