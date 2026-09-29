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

package ingress

import (
	"cmp"
	"context"
	"fmt"
	"slices"
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
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/kserve/kserve/pkg/apis/serving/v1beta1"
	"github.com/kserve/kserve/pkg/constants"
	"github.com/kserve/kserve/pkg/utils"
)

const (
	// RouteNotAdmittedReason is the IngressReady reason while the OpenShift router has not
	// admitted the InferenceService Route.
	RouteNotAdmittedReason = "RouteNotAdmitted"
	// RouteNotOwnedReason is the IngressReady reason when a Route with the InferenceService name
	// exists but is not controlled by the InferenceService. Such a Route is never modified.
	RouteNotOwnedReason = "RouteNotOwned"
	// RouteReconcileFailedReason is the IngressReady reason when the Route could not be built or
	// written, e.g. while the target Services are missing.
	RouteReconcileFailedReason = "RouteReconcileFailed"

	routeTimeoutAnnotation = "haproxy.router.openshift.io/timeout"
	routeISVCNameLabel     = "inferenceservice-name"
	// defaultRouteTimeoutSeconds is counted for every component without TimeoutSeconds.
	defaultRouteTimeoutSeconds int64 = 30
	// routeAdmissionRequeueInterval backs up the Route watch while the Route awaits admission.
	routeAdmissionRequeueInterval = 30 * time.Second
	routeConflictRequeueInterval  = time.Second
)

// RawRouteReconciler exposes InferenceServices labelled networking.kserve.io/visibility=exposed
// through an OpenShift Route named after the InferenceService.
//
// The Route is the one odh-model-controller builds, field for field, so a Route it created is
// adopted without a write and both controllers can reconcile the same InferenceService.
//
// The wrapped RawIngressReconciler still handles the Kubernetes Ingress, the stopped state and the
// internal address; the status URL, and the address when auth is enabled, are then replaced with
// the endpoints OpenShift clients use.
type RawRouteReconciler struct {
	ingress *RawIngressReconciler
	client  client.Client
	scheme  *runtime.Scheme
}

func NewRawRouteReconciler(client client.Client,
	scheme *runtime.Scheme,
	ingressConfig *v1beta1.IngressConfig,
	isvcConfig *v1beta1.InferenceServicesConfig,
) (*RawRouteReconciler, error) {
	rawIngress, err := NewRawIngressReconciler(client, scheme, ingressConfig, isvcConfig)
	if err != nil {
		return nil, err
	}
	return &RawRouteReconciler{
		ingress: rawIngress,
		client:  client,
		scheme:  scheme,
	}, nil
}

func (r *RawRouteReconciler) Reconcile(ctx context.Context, isvc *v1beta1.InferenceService) (ctrl.Result, error) {
	// A stopped InferenceService keeps its Route, like the Services it points to.
	if utils.GetForceStopRuntime(isvc) {
		return r.ingress.Reconcile(ctx, isvc)
	}

	// The Route goes first, so a failure leaves the URL of the last successful reconcile in place
	// of the one RawIngressReconciler derives from the ingress domain.
	route, err := r.reconcileRoute(ctx, isvc)
	if err != nil {
		if apierr.IsConflict(err) || apierr.IsAlreadyExists(err) {
			// Another writer, such as odh-model-controller during the handoff, changed the Route
			// since it was read.
			log.V(1).Info("Route changed concurrently, requeueing", "namespace", isvc.Namespace, "name", isvc.Name)
			return ctrl.Result{RequeueAfter: routeConflictRequeueInterval}, nil
		}
		isvc.Status.URL = nil
		isvc.Status.SetCondition(v1beta1.IngressReady, &apis.Condition{
			Type:    v1beta1.IngressReady,
			Status:  corev1.ConditionFalse,
			Reason:  RouteReconcileFailedReason,
			Message: err.Error(),
		})
		return ctrl.Result{}, err
	}

	result, err := r.ingress.Reconcile(ctx, isvc)
	// RawIngressReconciler marks IngressReady True only once it computed the URL and address.
	// Until then its Ingress waits on a component, and the status it recorded stands.
	if err != nil || !isvc.Status.IsConditionReady(v1beta1.IngressReady) {
		return result, err
	}

	// With auth, clients reach the entry point over HTTPS: the auth proxy in front of the
	// predictor, or the transformer's own listener. Any casing of "true" enables it, as in the
	// Service and Deployment reconcilers that set those listeners up.
	authEnabled := strings.EqualFold(isvc.Annotations[constants.ODHKserveRawAuth], "true")
	authHost := getRawServiceHost(isvc) + ":" + strconv.Itoa(constants.OauthProxyPort)
	if isvc.Spec.Transformer != nil {
		authHost = getRawServiceHost(isvc) + ":" + strconv.Itoa(int(constants.TransformerHTTPSPort))
	}
	if authEnabled {
		isvc.Status.Address.URL.Scheme = "https"
		isvc.Status.Address.URL.Host = authHost
	}

	if isRouteExposed(isvc) {
		url, notReady := routeURL(isvc, route)
		if notReady != nil {
			isvc.Status.URL = nil
			isvc.Status.SetCondition(v1beta1.IngressReady, notReady)
			// Route events reach the InferenceService through the controller's Route watch; the
			// requeue only covers a missed admission.
			if notReady.Reason == RouteNotAdmittedReason {
				return ctrl.Result{RequeueAfter: routeAdmissionRequeueInterval}, nil
			}
			return ctrl.Result{}, nil
		}
		isvc.Status.URL = url
		return ctrl.Result{}, nil
	}

	isvc.Status.URL = &apis.URL{Scheme: "http", Host: getRawServiceHost(isvc)}
	if authEnabled {
		isvc.Status.URL = &apis.URL{Scheme: "https", Host: authHost}
	}
	return ctrl.Result{}, nil
}

// reconcileRoute brings the Route in line with desiredRoute and returns the Route found in the
// cluster afterwards, or nil when there is none.
//
// Only a Route controlled by the InferenceService is updated or deleted.
func (r *RawRouteReconciler) reconcileRoute(ctx context.Context, isvc *v1beta1.InferenceService) (*routev1.Route, error) {
	desired, err := r.desiredRoute(ctx, isvc)
	if err != nil {
		return nil, err
	}

	existing := &routev1.Route{}
	err = r.client.Get(ctx, types.NamespacedName{Name: isvc.Name, Namespace: isvc.Namespace}, existing)
	switch {
	case apierr.IsNotFound(err):
		if desired == nil {
			return nil, nil
		}
		log.Info("creating Route", "namespace", desired.Namespace, "name", desired.Name)
		if err := r.client.Create(ctx, desired); err != nil {
			return nil, fmt.Errorf("failed to create Route %s/%s: %w", desired.Namespace, desired.Name, err)
		}
		return desired, nil
	case err != nil:
		return nil, fmt.Errorf("failed to get Route %s/%s: %w", isvc.Namespace, isvc.Name, err)
	}

	if !metav1.IsControlledBy(existing, isvc) {
		if desired != nil {
			log.Info("Route is not controlled by the InferenceService, leaving it untouched",
				"namespace", existing.Namespace, "name", existing.Name)
		}
		return existing, nil
	}

	if desired == nil {
		log.Info("deleting Route", "namespace", existing.Namespace, "name", existing.Name)
		if err := r.client.Delete(ctx, existing); client.IgnoreNotFound(err) != nil {
			return nil, fmt.Errorf("failed to delete Route %s/%s: %w", existing.Namespace, existing.Name, err)
		}
		return nil, nil
	}

	if semanticRouteEquals(desired, existing) {
		return existing, nil
	}

	updated := existing.DeepCopy()
	updated.Labels = desired.Labels
	updated.Annotations = desired.Annotations
	updated.Spec = desired.Spec
	// The host is allocated by OpenShift when the Route is created. The desired Route never sets
	// one, and the API server ignores attempts to clear it.
	updated.Spec.Host = existing.Spec.Host
	log.Info("updating Route", "namespace", updated.Namespace, "name", updated.Name)
	if err := r.client.Update(ctx, updated); err != nil {
		return nil, fmt.Errorf("failed to update Route %s/%s: %w", updated.Namespace, updated.Name, err)
	}
	return updated, nil
}

// desiredRoute builds the Route for an exposed InferenceService, or returns nil when the
// InferenceService is not exposed. It returns an error while the target Services are missing.
func (r *RawRouteReconciler) desiredRoute(ctx context.Context, isvc *v1beta1.InferenceService) (*routev1.Route, error) {
	if !isRouteExposed(isvc) {
		return nil, nil
	}
	// Any value strconv.ParseBool does not accept disables TLS re-encryption.
	reencrypt, _ := strconv.ParseBool(isvc.Annotations[constants.ODHKserveRawAuth])

	services := &corev1.ServiceList{}
	if err := r.client.List(ctx, services, client.InNamespace(isvc.Namespace),
		client.MatchingLabels{constants.InferenceServicePodLabelKey: isvc.Name}); err != nil {
		return nil, fmt.Errorf("failed to list services for InferenceService %q: %w", isvc.Name, err)
	}
	if len(services.Items) == 0 {
		return nil, fmt.Errorf("no services found for InferenceService %q; the backing Service may not have been created yet", isvc.Name)
	}
	target := routeTargetService(isvc, services.Items)
	if target == nil {
		return nil, fmt.Errorf("no predictor or transformer Service found for InferenceService %q", isvc.Name)
	}
	targetPort, err := routeTargetPort(reencrypt, target)
	if err != nil {
		return nil, err
	}

	termination := routev1.TLSTerminationEdge
	if reencrypt {
		termination = routev1.TLSTerminationReencrypt
	}
	route := &routev1.Route{
		ObjectMeta: metav1.ObjectMeta{
			Name:        isvc.Name,
			Namespace:   isvc.Namespace,
			Labels:      map[string]string{routeISVCNameLabel: isvc.Name},
			Annotations: map[string]string{routeTimeoutAnnotation: routeTimeout(isvc)},
		},
		Spec: routev1.RouteSpec{
			To: routev1.RouteTargetReference{
				Kind:   "Service",
				Name:   target.Name,
				Weight: ptr.To(int32(100)),
			},
			Port: &routev1.RoutePort{TargetPort: targetPort},
			TLS: &routev1.TLSConfig{
				Termination:                   termination,
				InsecureEdgeTerminationPolicy: routev1.InsecureEdgeTerminationPolicyRedirect,
			},
			WildcardPolicy: routev1.WildcardPolicyNone,
		},
	}
	if err := controllerutil.SetControllerReference(isvc, route, r.scheme); err != nil {
		return nil, err
	}

	if len(isvc.Spec.Canary) > 0 {
		// The router applies spec.port to every backend, and each canary Service names its port
		// after itself, so a port name is replaced by the target port it maps to.
		if targetPort.Type == intstr.String {
			for _, port := range target.Spec.Ports {
				if port.Name == targetPort.StrVal {
					route.Spec.Port = &routev1.RoutePort{TargetPort: port.TargetPort}
					break
				}
			}
		}
		if err := r.addCanaryBackends(ctx, isvc, route); err != nil {
			return nil, err
		}
	}

	return route, nil
}

// addCanaryBackends splits the Route traffic between the stable target and one alternate backend
// per canary, weighted by the canary traffic percentages.
func (r *RawRouteReconciler) addCanaryBackends(ctx context.Context, isvc *v1beta1.InferenceService, route *routev1.Route) error {
	var canaryWeight int32
	for _, canary := range isvc.Spec.Canary {
		canaryWeight += canary.TrafficPercent
	}
	stableWeight := 100 - canaryWeight
	if stableWeight < 0 {
		return fmt.Errorf("canary traffic percents sum to %d which exceeds 100; stable backend weight would be %d",
			canaryWeight, stableWeight)
	}
	route.Spec.To.Weight = &stableWeight

	backends := make([]routev1.RouteTargetReference, 0, len(isvc.Spec.Canary))
	for _, canary := range isvc.Spec.Canary {
		name := constants.PredictorServiceName(isvc.Name, canary.Predictor.Name)
		if err := r.client.Get(ctx, types.NamespacedName{Name: name, Namespace: isvc.Namespace}, &corev1.Service{}); err != nil {
			return fmt.Errorf("canary service %q not found: %w", name, err)
		}
		backends = append(backends, routev1.RouteTargetReference{
			Kind:   "Service",
			Name:   name,
			Weight: ptr.To(canary.TrafficPercent),
		})
	}
	route.Spec.AlternateBackends = backends
	return nil
}

// routeTargetService picks the transformer Service, then <isvc>-predictor, then the stable
// predictor Service of a named predictor, then any other predictor Service. Services are
// considered in name order, so the choice does not depend on the list order.
func routeTargetService(isvc *v1beta1.InferenceService, services []corev1.Service) *corev1.Service {
	sorted := slices.Clone(services)
	slices.SortFunc(sorted, func(a, b corev1.Service) int { return cmp.Compare(a.Name, b.Name) })

	defaultPredictor := constants.PredictorServiceName(isvc.Name)
	stablePredictor := constants.PredictorServiceName(isvc.Name, isvc.Spec.Predictor.Name)
	var transformer, predictor, fallback *corev1.Service
	for i := range sorted {
		svc := &sorted[i]
		component := svc.Labels[constants.KServiceComponentLabel]
		switch {
		case component == string(v1beta1.TransformerComponent):
			if transformer == nil {
				transformer = svc
			}
		case svc.Name == defaultPredictor:
			predictor = svc
		case component == string(v1beta1.PredictorComponent):
			if fallback == nil || svc.Name == stablePredictor {
				fallback = svc
			}
		}
	}
	switch {
	case transformer != nil:
		return transformer
	case predictor != nil:
		return predictor
	default:
		return fallback
	}
}

// routeTargetPort picks the Service port named https when TLS is re-encrypted and http
// otherwise, then any named port, then the number of the first port.
func routeTargetPort(reencrypt bool, svc *corev1.Service) (intstr.IntOrString, error) {
	preferred := "http"
	if reencrypt {
		preferred = "https"
	}
	for _, port := range svc.Spec.Ports {
		if port.Name == preferred {
			return intstr.FromString(port.Name), nil
		}
	}
	for _, port := range svc.Spec.Ports {
		if port.Name != "" {
			return intstr.FromString(port.Name), nil
		}
	}
	if len(svc.Spec.Ports) > 0 {
		return intstr.FromInt32(svc.Spec.Ports[0].Port), nil
	}
	return intstr.IntOrString{}, fmt.Errorf("service %q has no ports defined", svc.Name)
}

// routeTimeout returns the router timeout: the InferenceService annotation when present, even
// if empty, otherwise the sum of the component timeouts.
func routeTimeout(isvc *v1beta1.InferenceService) string {
	if timeout, ok := isvc.Annotations[routeTimeoutAnnotation]; ok {
		return timeout
	}
	timeout := componentRouteTimeout(isvc.Spec.Predictor.TimeoutSeconds)
	if isvc.Spec.Transformer != nil {
		timeout += componentRouteTimeout(isvc.Spec.Transformer.TimeoutSeconds)
	}
	if isvc.Spec.Explainer != nil {
		timeout += componentRouteTimeout(isvc.Spec.Explainer.TimeoutSeconds)
	}
	return fmt.Sprintf("%ds", timeout)
}

func componentRouteTimeout(timeoutSeconds *int64) int64 {
	if timeoutSeconds != nil {
		return *timeoutSeconds
	}
	return defaultRouteTimeoutSeconds
}

// semanticRouteEquals compares the fields the Route reconciler owns. The host is left out: it is
// allocated by OpenShift and never set on the desired Route.
func semanticRouteEquals(desired, existing *routev1.Route) bool {
	return equality.Semantic.DeepEqual(desired.Spec.To, existing.Spec.To) &&
		equality.Semantic.DeepEqual(desired.Spec.AlternateBackends, existing.Spec.AlternateBackends) &&
		equality.Semantic.DeepEqual(desired.Spec.Port, existing.Spec.Port) &&
		equality.Semantic.DeepEqual(desired.Spec.TLS, existing.Spec.TLS) &&
		desired.Spec.WildcardPolicy == existing.Spec.WildcardPolicy &&
		equality.Semantic.DeepEqual(desired.Labels, existing.Labels) &&
		equality.Semantic.DeepEqual(desired.Annotations, existing.Annotations)
}

// routeURL returns the external URL served by an admitted Route the InferenceService controls,
// or the IngressReady condition explaining why there is none.
func routeURL(isvc *v1beta1.InferenceService, route *routev1.Route) (*apis.URL, *apis.Condition) {
	if route == nil || !metav1.IsControlledBy(route, isvc) {
		return nil, &apis.Condition{
			Type:    v1beta1.IngressReady,
			Status:  corev1.ConditionFalse,
			Reason:  RouteNotOwnedReason,
			Message: fmt.Sprintf("Route %s/%s is not controlled by this InferenceService", isvc.Namespace, isvc.Name),
		}
	}
	if admitted, rejection := routeAdmission(route); !admitted {
		message := fmt.Sprintf("Route %s/%s has not been admitted by a router", route.Namespace, route.Name)
		if rejection != "" {
			message = fmt.Sprintf("Route %s/%s was not admitted: %s", route.Namespace, route.Name, rejection)
		}
		return nil, &apis.Condition{
			Type:    v1beta1.IngressReady,
			Status:  corev1.ConditionFalse,
			Reason:  RouteNotAdmittedReason,
			Message: message,
		}
	}
	scheme := "http"
	if route.Spec.TLS != nil && route.Spec.TLS.Termination != "" {
		scheme = "https"
	}
	return &apis.URL{Scheme: scheme, Host: route.Spec.Host}, nil
}

// routeAdmission reports whether any router admitted the Route and, if none did, why a router
// rejected it.
func routeAdmission(route *routev1.Route) (bool, string) {
	var rejection string
	for _, ingress := range route.Status.Ingress {
		for _, condition := range ingress.Conditions {
			if condition.Type != routev1.RouteAdmitted {
				continue
			}
			if condition.Status == corev1.ConditionTrue {
				return true, ""
			}
			if condition.Status == corev1.ConditionFalse && rejection == "" {
				rejection = strings.TrimSpace(condition.Reason + " " + condition.Message)
			}
		}
	}
	return false, rejection
}

func isRouteExposed(isvc *v1beta1.InferenceService) bool {
	return isvc.Labels[constants.NetworkVisibility] == constants.ODHRouteEnabled
}
