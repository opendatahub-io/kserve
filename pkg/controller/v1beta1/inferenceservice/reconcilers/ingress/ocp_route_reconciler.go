//go:build distro

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

package ingress

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	routev1 "github.com/openshift/api/route/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierr "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"knative.dev/pkg/apis"
	duckv1 "knative.dev/pkg/apis/duck/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/kserve/kserve/pkg/apis/serving/v1beta1"
	"github.com/kserve/kserve/pkg/constants"
	"github.com/kserve/kserve/pkg/utils"
)

const routeAdmissionRequeueDelay = 3 * time.Second

type RawOCPRouteReconciler struct {
	client        client.Client
	scheme        *runtime.Scheme
	ingressConfig *v1beta1.IngressConfig
	isvcConfig    *v1beta1.InferenceServicesConfig
}

func NewRawOCPRouteReconciler(client client.Client, scheme *runtime.Scheme,
	ingressConfig *v1beta1.IngressConfig, isvcConfig *v1beta1.InferenceServicesConfig,
) *RawOCPRouteReconciler {
	return &RawOCPRouteReconciler{
		client:        client,
		scheme:        scheme,
		ingressConfig: ingressConfig,
		isvcConfig:    isvcConfig,
	}
}

func (r *RawOCPRouteReconciler) Reconcile(ctx context.Context, isvc *v1beta1.InferenceService) (ctrl.Result, error) {
	authEnabled := false
	if val, ok := isvc.Annotations[constants.ODHKserveRawAuth]; ok && strings.EqualFold(val, "true") {
		authEnabled = true
	}

	isExposed := false
	if val, ok := isvc.Labels[constants.NetworkVisibility]; ok && val == constants.ODHRouteEnabled {
		isExposed = true
	}

	existingRoute := &routev1.Route{}
	getErr := r.client.Get(ctx, types.NamespacedName{Name: isvc.Name, Namespace: isvc.Namespace}, existingRoute)
	routeNotFound := apierr.IsNotFound(getErr)
	if getErr != nil && !routeNotFound {
		return ctrl.Result{}, fmt.Errorf("failed to get existing Route: %w", getErr)
	}

	forceStopRuntime := utils.GetForceStopRuntime(isvc)
	if forceStopRuntime {
		if !routeNotFound {
			if controller := metav1.GetControllerOf(existingRoute); controller != nil && controller.UID == isvc.UID {
				log.Info("ISVC is stopped — deleting its associated Route", "name", isvc.Name)
				if err := r.client.Delete(ctx, existingRoute); err != nil && !apierr.IsNotFound(err) {
					return ctrl.Result{}, fmt.Errorf("failed to delete Route %s/%s: %w", existingRoute.Namespace, existingRoute.Name, err)
				}
			}
		}
		isvc.Status.URL = nil
		isvc.Status.Address = nil
		isvc.Status.SetCondition(v1beta1.IngressReady, &apis.Condition{
			Type:   v1beta1.IngressReady,
			Status: corev1.ConditionFalse,
			Reason: v1beta1.StoppedISVCReason,
		})
		return ctrl.Result{}, nil
	}

	if isExposed {
		return r.reconcileExposed(ctx, isvc, existingRoute, routeNotFound, authEnabled)
	}
	return r.reconcileClusterLocal(ctx, isvc, existingRoute, routeNotFound, authEnabled)
}

func (r *RawOCPRouteReconciler) reconcileExposed(
	ctx context.Context, isvc *v1beta1.InferenceService,
	existingRoute *routev1.Route, routeNotFound, authEnabled bool,
) (ctrl.Result, error) {
	targetSvc, err := r.selectTargetService(ctx, isvc)
	if err != nil {
		return ctrl.Result{}, err
	}

	desiredRoute, err := r.buildDesiredRoute(isvc, targetSvc, authEnabled)
	if err != nil {
		return ctrl.Result{}, err
	}

	if routeNotFound {
		log.Info("Creating Route", "name", isvc.Name, "namespace", isvc.Namespace)
		if err := r.client.Create(ctx, desiredRoute); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to create Route: %w", err)
		}
		isvc.Status.SetCondition(v1beta1.IngressReady, &apis.Condition{
			Type:   v1beta1.IngressReady,
			Status: corev1.ConditionFalse,
			Reason: "RouteNotAdmitted",
		})
		return ctrl.Result{RequeueAfter: routeAdmissionRequeueDelay}, nil
	}

	if !semanticRouteEquals(desiredRoute, existingRoute) {
		existingRoute.Spec = desiredRoute.Spec
		existingRoute.Labels = desiredRoute.Labels
		if existingRoute.Annotations == nil {
			existingRoute.Annotations = map[string]string{}
		}
		for k, v := range desiredRoute.Annotations {
			existingRoute.Annotations[k] = v
		}
		log.Info("Updating Route", "name", isvc.Name, "namespace", isvc.Namespace)
		if err := r.client.Update(ctx, existingRoute); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to update Route: %w", err)
		}
	}

	routeHost, admitted := getAdmittedHost(existingRoute)
	if !admitted {
		isvc.Status.SetCondition(v1beta1.IngressReady, &apis.Condition{
			Type:   v1beta1.IngressReady,
			Status: corev1.ConditionFalse,
			Reason: "RouteNotAdmitted",
		})
		return ctrl.Result{RequeueAfter: routeAdmissionRequeueDelay}, nil
	}

	isvc.Status.URL = &apis.URL{Scheme: "https", Host: routeHost}

	if err := r.setAddress(ctx, isvc, authEnabled); err != nil {
		return ctrl.Result{}, err
	}

	isvc.Status.SetCondition(v1beta1.IngressReady, &apis.Condition{
		Type:   v1beta1.IngressReady,
		Status: corev1.ConditionTrue,
	})
	return ctrl.Result{}, nil
}

func (r *RawOCPRouteReconciler) reconcileClusterLocal(
	ctx context.Context, isvc *v1beta1.InferenceService,
	existingRoute *routev1.Route, routeNotFound, authEnabled bool,
) (ctrl.Result, error) {
	if !routeNotFound {
		if controller := metav1.GetControllerOf(existingRoute); controller != nil && controller.UID == isvc.UID {
			log.Info("Visibility changed to cluster-local — deleting Route", "name", isvc.Name)
			if err := r.client.Delete(ctx, existingRoute); err != nil && !apierr.IsNotFound(err) {
				return ctrl.Result{}, fmt.Errorf("failed to delete Route %s/%s: %w", existingRoute.Namespace, existingRoute.Name, err)
			}
		}
	}

	host := getRawServiceHost(isvc)
	urlScheme := "http"
	if authEnabled {
		host += ":" + strconv.Itoa(constants.OauthProxyPort)
		urlScheme = "https"
	}
	isvc.Status.URL = &apis.URL{Scheme: urlScheme, Host: host}

	if err := r.setAddress(ctx, isvc, authEnabled); err != nil {
		return ctrl.Result{}, err
	}

	isvc.Status.SetCondition(v1beta1.IngressReady, &apis.Condition{
		Type:   v1beta1.IngressReady,
		Status: corev1.ConditionTrue,
	})
	return ctrl.Result{}, nil
}

func (r *RawOCPRouteReconciler) setAddress(ctx context.Context, isvc *v1beta1.InferenceService, authEnabled bool) error {
	host := getRawServiceHost(isvc)

	entryPointSvcName := constants.PredictorServiceName(isvc.Name)
	if isvc.Spec.Transformer != nil {
		entryPointSvcName = constants.TransformerServiceName(isvc.Name)
	}
	entryPointSvc := &corev1.Service{}
	if err := r.client.Get(ctx, types.NamespacedName{
		Namespace: isvc.Namespace,
		Name:      entryPointSvcName,
	}, entryPointSvc); err != nil {
		return fmt.Errorf("failed to get entry point service %s: %w", entryPointSvcName, err)
	}
	if entryPointSvc.Spec.ClusterIP == corev1.ClusterIPNone {
		host = host + ":" + constants.InferenceServiceDefaultHttpPort
	}

	addrScheme := "http"
	if authEnabled {
		host = getRawServiceHost(isvc) + ":" + strconv.Itoa(constants.OauthProxyPort)
		addrScheme = "https"
	}
	isvc.Status.Address = &duckv1.Addressable{
		URL: &apis.URL{Scheme: addrScheme, Host: host},
	}
	return nil
}

func (r *RawOCPRouteReconciler) selectTargetService(ctx context.Context, isvc *v1beta1.InferenceService) (*corev1.Service, error) {
	var serviceList corev1.ServiceList
	if err := r.client.List(ctx, &serviceList,
		client.InNamespace(isvc.Namespace),
		client.MatchingLabels{constants.InferenceServicePodLabelKey: isvc.Name},
	); err != nil {
		return nil, fmt.Errorf("failed to list services for ISVC %s: %w", isvc.Name, err)
	}

	var transformerSvc, predictorSvc *corev1.Service
	exactPredictorName := constants.PredictorServiceName(isvc.Name)

	for i := range serviceList.Items {
		svc := &serviceList.Items[i]
		component := svc.Labels[constants.KServiceComponentLabel]
		switch {
		case component == string(constants.Transformer):
			transformerSvc = svc
		case svc.Name == exactPredictorName:
			predictorSvc = svc
		case component == string(constants.Predictor) && predictorSvc == nil:
			predictorSvc = svc
		}
	}

	if transformerSvc != nil {
		return transformerSvc, nil
	}
	if predictorSvc != nil {
		return predictorSvc, nil
	}
	return nil, fmt.Errorf("no suitable target service found for ISVC %s/%s", isvc.Namespace, isvc.Name)
}

func (r *RawOCPRouteReconciler) buildDesiredRoute(
	isvc *v1beta1.InferenceService, targetSvc *corev1.Service, authEnabled bool,
) (*routev1.Route, error) {
	targetPort, err := setRouteTargetPort(authEnabled, targetSvc)
	if err != nil {
		return nil, err
	}

	tlsConfig := &routev1.TLSConfig{
		Termination:                   routev1.TLSTerminationEdge,
		InsecureEdgeTerminationPolicy: routev1.InsecureEdgeTerminationPolicyRedirect,
	}
	if authEnabled {
		tlsConfig.Termination = routev1.TLSTerminationReencrypt
	}

	route := &routev1.Route{
		ObjectMeta: metav1.ObjectMeta{
			Name:      isvc.Name,
			Namespace: isvc.Namespace,
			Labels:    isvc.Labels,
		},
		Spec: routev1.RouteSpec{
			To: routev1.RouteTargetReference{
				Kind:   "Service",
				Name:   targetSvc.Name,
				Weight: ptr.To(int32(100)),
			},
			Port: &routev1.RoutePort{TargetPort: targetPort},
			TLS:  tlsConfig,
		},
	}

	setRouteTimeout(route, isvc)

	if err := controllerutil.SetControllerReference(isvc, route, r.scheme); err != nil {
		return nil, fmt.Errorf("failed to set controller reference on Route: %w", err)
	}
	return route, nil
}

func getAdmittedHost(route *routev1.Route) (string, bool) {
	for _, ingress := range route.Status.Ingress {
		for _, condition := range ingress.Conditions {
			if condition.Type == routev1.RouteAdmitted && condition.Status == corev1.ConditionTrue {
				return ingress.Host, true
			}
		}
	}
	return "", false
}

func setRouteTargetPort(authEnabled bool, svc *corev1.Service) (intstr.IntOrString, error) {
	desiredName := "http"
	if authEnabled {
		desiredName = "https"
	}
	for _, port := range svc.Spec.Ports {
		if port.Name == desiredName {
			return intstr.FromString(desiredName), nil
		}
	}
	if len(svc.Spec.Ports) > 0 {
		log.Info("Desired port not found, falling back to first port",
			"service", svc.Name, "desiredPort", desiredName, "fallbackPort", svc.Spec.Ports[0].Name)
		if svc.Spec.Ports[0].Name != "" {
			return intstr.FromString(svc.Spec.Ports[0].Name), nil
		}
		return intstr.FromInt32(svc.Spec.Ports[0].Port), nil
	}
	return intstr.IntOrString{}, fmt.Errorf("service %s/%s has no ports", svc.Namespace, svc.Name)
}

func setRouteTimeout(route *routev1.Route, isvc *v1beta1.InferenceService) {
	if val, ok := isvc.Annotations["haproxy.router.openshift.io/timeout"]; ok {
		if route.Annotations == nil {
			route.Annotations = map[string]string{}
		}
		route.Annotations["haproxy.router.openshift.io/timeout"] = val
		return
	}

	maxTimeout := int64(0)
	if isvc.Spec.Predictor.TimeoutSeconds != nil {
		maxTimeout = *isvc.Spec.Predictor.TimeoutSeconds
	}
	if isvc.Spec.Transformer != nil && isvc.Spec.Transformer.TimeoutSeconds != nil {
		if *isvc.Spec.Transformer.TimeoutSeconds > maxTimeout {
			maxTimeout = *isvc.Spec.Transformer.TimeoutSeconds
		}
	}
	if maxTimeout > 0 {
		if route.Annotations == nil {
			route.Annotations = map[string]string{}
		}
		route.Annotations["haproxy.router.openshift.io/timeout"] = strconv.FormatInt(maxTimeout, 10) + "s"
	}
}

func semanticRouteEquals(desired, existing *routev1.Route) bool {
	if !equality.Semantic.DeepEqual(desired.Spec, existing.Spec) {
		return false
	}
	if !equality.Semantic.DeepEqual(desired.Labels, existing.Labels) {
		return false
	}
	for key, val := range desired.Annotations {
		if existing.Annotations[key] != val {
			return false
		}
	}
	return true
}
