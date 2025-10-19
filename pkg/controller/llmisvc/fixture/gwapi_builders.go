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

package fixture

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	igwv1 "sigs.k8s.io/gateway-api-inference-extension/api/v1"
	gatewayapi "sigs.k8s.io/gateway-api/apis/v1"
)

type ObjectOption[T client.Object] func(T)

type GatewayOption ObjectOption[*gatewayapi.Gateway]

func Gateway(name string, opts ...GatewayOption) *gatewayapi.Gateway {
	gw := &gatewayapi.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: gatewayapi.GatewaySpec{
			GatewayClassName: defaultGatewayClass,
			Listeners:        []gatewayapi.Listener{},
			Infrastructure:   &gatewayapi.GatewayInfrastructure{},
		},
		Status: gatewayapi.GatewayStatus{
			Addresses: []gatewayapi.GatewayStatusAddress{},
		},
	}

	for _, opt := range opts {
		opt(gw)
	}

	return gw
}

func InNamespace[T metav1.Object](namespace string) func(T) {
	return func(t T) {
		t.SetNamespace(namespace)
	}
}

func WithClassName(className string) GatewayOption {
	return func(gw *gatewayapi.Gateway) {
		gw.Spec.GatewayClassName = gatewayapi.ObjectName(className)
	}
}

func WithInfrastructureLabels(key, value string) GatewayOption {
	return func(gw *gatewayapi.Gateway) {
		if gw.Spec.Infrastructure.Labels == nil {
			gw.Spec.Infrastructure.Labels = make(map[gatewayapi.LabelKey]gatewayapi.LabelValue)
		}
		gw.Spec.Infrastructure.Labels[gatewayapi.LabelKey(key)] = gatewayapi.LabelValue(value)
	}
}

func WithListener(protocol gatewayapi.ProtocolType) GatewayOption {
	return func(gw *gatewayapi.Gateway) {
		port := gatewayapi.PortNumber(80)
		if protocol == gatewayapi.HTTPSProtocolType {
			port = 443
		}
		listener := gatewayapi.Listener{
			Name:     gatewayapi.SectionName("listener"),
			Protocol: protocol,
			Port:     port,
		}
		gw.Spec.Listeners = append(gw.Spec.Listeners, listener)
	}
}

func WithListeners(listeners ...gatewayapi.Listener) GatewayOption {
	return func(gw *gatewayapi.Gateway) {
		gw.Spec.Listeners = append(gw.Spec.Listeners, listeners...)
	}
}

type (
	HTTPRouteOption ObjectOption[*gatewayapi.HTTPRoute]
	ParentRefOption func(*gatewayapi.ParentReference)
)

func HTTPRoute(name string, opts ...HTTPRouteOption) *gatewayapi.HTTPRoute {
	route := &gatewayapi.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: gatewayapi.HTTPRouteSpec{
			CommonRouteSpec: gatewayapi.CommonRouteSpec{
				ParentRefs: []gatewayapi.ParentReference{},
			},
			Hostnames: []gatewayapi.Hostname{},
			Rules:     []gatewayapi.HTTPRouteRule{},
		},
	}

	for _, opt := range opts {
		opt(route)
	}

	return route
}

func WithParentRef(ref gatewayapi.ParentReference) HTTPRouteOption {
	return func(route *gatewayapi.HTTPRoute) {
		route.Spec.CommonRouteSpec.ParentRefs = append(route.Spec.CommonRouteSpec.ParentRefs, ref)
	}
}

func WithParentRefs(refs ...gatewayapi.ParentReference) HTTPRouteOption {
	return func(route *gatewayapi.HTTPRoute) {
		route.Spec.CommonRouteSpec.ParentRefs = refs
	}
}

func WithHostnames(hostnames ...string) HTTPRouteOption {
	return func(route *gatewayapi.HTTPRoute) {
		route.Spec.Hostnames = make([]gatewayapi.Hostname, len(hostnames))
		for i, hostname := range hostnames {
			route.Spec.Hostnames[i] = gatewayapi.Hostname(hostname)
		}
	}
}

func WithAddresses(addresses ...string) GatewayOption {
	return func(gw *gatewayapi.Gateway) {
		gw.Status.Addresses = make([]gatewayapi.GatewayStatusAddress, len(addresses))
		for i, address := range addresses {
			gw.Status.Addresses[i] = gatewayapi.GatewayStatusAddress{
				Value: address,
				// Type is left as nil (defaults to IPAddressType behavior)
			}
		}
	}
}

func WithHostnameAddresses(addresses ...string) GatewayOption {
	return func(gw *gatewayapi.Gateway) {
		gw.Status.Addresses = make([]gatewayapi.GatewayStatusAddress, len(addresses))
		for i, address := range addresses {
			gw.Status.Addresses[i] = gatewayapi.GatewayStatusAddress{
				Type:  ptr.To(gatewayapi.HostnameAddressType),
				Value: address,
			}
		}
	}
}

func WithMixedAddresses(addresses ...gatewayapi.GatewayStatusAddress) GatewayOption {
	return func(gw *gatewayapi.Gateway) {
		gw.Status.Addresses = addresses
	}
}

func IPAddress(value string) gatewayapi.GatewayStatusAddress {
	return gatewayapi.GatewayStatusAddress{
		Type:  ptr.To(gatewayapi.IPAddressType),
		Value: value,
	}
}

func HostnameAddress(value string) gatewayapi.GatewayStatusAddress {
	return gatewayapi.GatewayStatusAddress{
		Type:  ptr.To(gatewayapi.HostnameAddressType),
		Value: value,
	}
}

func WithPath(path string) HTTPRouteOption {
	return func(route *gatewayapi.HTTPRoute) {
		rule := gatewayapi.HTTPRouteRule{
			Matches: []gatewayapi.HTTPRouteMatch{
				{
					Path: &gatewayapi.HTTPPathMatch{
						Value: ptr.To(path),
					},
				},
			},
		}
		route.Spec.Rules = append(route.Spec.Rules, rule)
	}
}

func WithRules(rules ...gatewayapi.HTTPRouteRule) HTTPRouteOption {
	return func(route *gatewayapi.HTTPRoute) {
		route.Spec.Rules = rules
	}
}

func GatewayRef(name string, opts ...ParentRefOption) gatewayapi.ParentReference {
	ref := gatewayapi.ParentReference{
		Name:  gatewayapi.ObjectName(name),
		Group: ptr.To(gatewayapi.Group("gateway.networking.k8s.io")),
		Kind:  ptr.To(gatewayapi.Kind("Gateway")),
	}
	for _, opt := range opts {
		opt(&ref)
	}
	return ref
}

func RefInNamespace(namespace string) ParentRefOption {
	return func(ref *gatewayapi.ParentReference) {
		ref.Namespace = ptr.To(gatewayapi.Namespace(namespace))
	}
}

func GatewayRefWithoutNamespace(name string) gatewayapi.ParentReference {
	return gatewayapi.ParentReference{
		Name:  gatewayapi.ObjectName(name),
		Group: ptr.To(gatewayapi.Group("gateway.networking.k8s.io")),
		Kind:  ptr.To(gatewayapi.Kind("Gateway")),
		// Namespace intentionally omitted
	}
}

func HTTPSGateway(name, namespace string, addresses ...string) *gatewayapi.Gateway {
	return Gateway(name,
		InNamespace[*gatewayapi.Gateway](namespace),
		WithListener(gatewayapi.HTTPSProtocolType),
		WithAddresses(addresses...),
	)
}

func HTTPGateway(name, namespace string, addresses ...string) *gatewayapi.Gateway {
	return Gateway(name,
		InNamespace[*gatewayapi.Gateway](namespace),
		WithListener(gatewayapi.HTTPProtocolType),
		WithAddresses(addresses...),
	)
}

type (
	HTTPRouteRuleOption  func(*gatewayapi.HTTPRouteRule)
	HTTPBackendRefOption func(*gatewayapi.HTTPBackendRef)
)

func WithHTTPRouteRule(rule gatewayapi.HTTPRouteRule) HTTPRouteOption {
	return func(route *gatewayapi.HTTPRoute) {
		route.Spec.Rules = append(route.Spec.Rules, rule)
	}
}

func HTTPRouteRule(opts ...HTTPRouteRuleOption) gatewayapi.HTTPRouteRule {
	rule := gatewayapi.HTTPRouteRule{
		Matches:     []gatewayapi.HTTPRouteMatch{},
		BackendRefs: []gatewayapi.HTTPBackendRef{},
	}

	for _, opt := range opts {
		opt(&rule)
	}

	return rule
}

func WithMatches(matches ...gatewayapi.HTTPRouteMatch) HTTPRouteRuleOption {
	return func(rule *gatewayapi.HTTPRouteRule) {
		rule.Matches = append(rule.Matches, matches...)
	}
}

func WithBackendRefs(refs ...gatewayapi.HTTPBackendRef) HTTPRouteRuleOption {
	return func(rule *gatewayapi.HTTPRouteRule) {
		rule.BackendRefs = append(rule.BackendRefs, refs...)
	}
}

func WithHTTPRouteGatewayRef(references ...gatewayapi.ParentReference) HTTPRouteOption {
	return func(route *gatewayapi.HTTPRoute) {
		route.Spec.ParentRefs = append(route.Spec.ParentRefs, references...)
	}
}

// BackendRefInferencePoolV1 creates a v1 InferencePool backend ref
func BackendRefInferencePoolV1(name string, weight int32) gatewayapi.HTTPBackendRef {
	return gatewayapi.HTTPBackendRef{
		BackendRef: gatewayapi.BackendRef{
			BackendObjectReference: gatewayapi.BackendObjectReference{
				Group: ptr.To(gatewayapi.Group("inference.networking.k8s.io")),
				Kind:  ptr.To(gatewayapi.Kind("InferencePool")),
				Name:  gatewayapi.ObjectName(name),
				Port:  ptr.To(gatewayapi.PortNumber(8000)),
			},
			Weight: ptr.To(weight),
		},
	}
}

// BackendRefInferencePoolV1Alpha2 creates a v1alpha2 InferencePool backend ref with configurable weight
func BackendRefInferencePoolV1Alpha2(name string, weight int32) gatewayapi.HTTPBackendRef {
	return gatewayapi.HTTPBackendRef{
		BackendRef: gatewayapi.BackendRef{
			BackendObjectReference: gatewayapi.BackendObjectReference{
				Group: ptr.To(gatewayapi.Group("inference.networking.x-k8s.io")),
				Kind:  ptr.To(gatewayapi.Kind("InferencePool")),
				Name:  gatewayapi.ObjectName(name),
				Port:  ptr.To(gatewayapi.PortNumber(8000)),
			},
			Weight: ptr.To(weight),
		},
	}
}

// BackendRefServiceWithWeight creates a Service backend ref with configurable weight
func BackendRefServiceWithWeight(name string, weight int32) gatewayapi.HTTPBackendRef {
	return gatewayapi.HTTPBackendRef{
		BackendRef: gatewayapi.BackendRef{
			BackendObjectReference: gatewayapi.BackendObjectReference{
				Group: ptr.To(gatewayapi.Group("")),
				Kind:  ptr.To(gatewayapi.Kind("Service")),
				Name:  gatewayapi.ObjectName(name),
				Port:  ptr.To(gatewayapi.PortNumber(8000)),
			},
			Weight: ptr.To(weight),
		},
	}
}

func WithTimeouts(backendTimeout, requestTimeout string) HTTPRouteRuleOption {
	return func(rule *gatewayapi.HTTPRouteRule) {
		rule.Timeouts = &gatewayapi.HTTPRouteTimeouts{
			BackendRequest: ptr.To(gatewayapi.Duration(backendTimeout)),
			Request:        ptr.To(gatewayapi.Duration(requestTimeout)),
		}
	}
}

func WithFilters(filters ...gatewayapi.HTTPRouteFilter) HTTPRouteRuleOption {
	return func(rule *gatewayapi.HTTPRouteRule) {
		rule.Filters = append(rule.Filters, filters...)
	}
}

func WithHTTPRule(ruleOpts ...HTTPRouteRuleOption) HTTPRouteOption {
	return WithHTTPRouteRule(HTTPRouteRule(ruleOpts...))
}

func Matches(matches ...gatewayapi.HTTPRouteMatch) HTTPRouteRuleOption {
	return WithMatches(matches...)
}

func Timeouts(backendTimeout, requestTimeout string) HTTPRouteRuleOption {
	return WithTimeouts(backendTimeout, requestTimeout)
}

func Filters(filters ...gatewayapi.HTTPRouteFilter) HTTPRouteRuleOption {
	return WithFilters(filters...)
}

func PathPrefixMatch(path string) gatewayapi.HTTPRouteMatch {
	return gatewayapi.HTTPRouteMatch{
		Path: &gatewayapi.HTTPPathMatch{
			Type:  ptr.To(gatewayapi.PathMatchPathPrefix),
			Value: ptr.To(path),
		},
	}
}

// ServiceRef creates a Service backend reference for HTTPRoute rules.
// Gateway API v1.4+: port can be passed directly as int32 because PortNumber is a type alias.
func ServiceRef(name string, port int32, weight int32) gatewayapi.HTTPBackendRef {
	return gatewayapi.HTTPBackendRef{
		BackendRef: gatewayapi.BackendRef{
			BackendObjectReference: gatewayapi.BackendObjectReference{
				Kind: ptr.To(gatewayapi.Kind("Service")),
				Name: gatewayapi.ObjectName(name),
				Port: ptr.To(port),
			},
			Weight: ptr.To(weight),
		},
	}
}

func HTTPRouteRuleWithBackendAndTimeouts(backendName string, backendPort int32, path string, backendTimeout, requestTimeout string) gatewayapi.HTTPRouteRule {
	return gatewayapi.HTTPRouteRule{
		BackendRefs: []gatewayapi.HTTPBackendRef{
			ServiceRef(backendName, backendPort, 1),
		},
		Matches: []gatewayapi.HTTPRouteMatch{
			PathPrefixMatch(path),
		},
		Timeouts: &gatewayapi.HTTPRouteTimeouts{
			BackendRequest: ptr.To(gatewayapi.Duration(backendTimeout)),
			Request:        ptr.To(gatewayapi.Duration(requestTimeout)),
		},
	}
}

func GatewayParentRef(name, namespace string) gatewayapi.ParentReference {
	return gatewayapi.ParentReference{
		Group:     ptr.To(gatewayapi.Group("gateway.networking.k8s.io")),
		Kind:      ptr.To(gatewayapi.Kind("Gateway")),
		Name:      gatewayapi.ObjectName(name),
		Namespace: ptr.To(gatewayapi.Namespace(namespace)),
	}
}

// WithGatewayCondition creates a GatewayOption that sets specific status conditions
func WithGatewayCondition(conditionType string, status metav1.ConditionStatus, reason, message string) GatewayOption {
	return func(gw *gatewayapi.Gateway) {
		condition := metav1.Condition{
			Type:    conditionType,
			Status:  status,
			Reason:  reason,
			Message: message,
		}
		gw.Status.Conditions = append(gw.Status.Conditions, condition)
	}
}

// WithProgrammedCondition is a convenience function for setting the Programmed condition
func WithProgrammedCondition(status metav1.ConditionStatus, reason, message string) GatewayOption {
	return WithGatewayCondition(string(gatewayapi.GatewayConditionProgrammed), status, reason, message)
}

// HTTPRoute Status Options

// WithHTTPRouteParentStatus adds parent status to the HTTPRoute
func WithHTTPRouteParentStatus(parentRef gatewayapi.ParentReference, controllerName string, conditions ...metav1.Condition) HTTPRouteOption {
	return func(route *gatewayapi.HTTPRoute) {
		parentStatus := gatewayapi.RouteParentStatus{
			ParentRef:      parentRef,
			ControllerName: gatewayapi.GatewayController(controllerName),
			Conditions:     conditions,
		}
		route.Status.RouteStatus.Parents = append(route.Status.RouteStatus.Parents, parentStatus)
	}
}

// WithHTTPRouteReadyStatus sets the HTTPRoute status to ready for all parent refs
func WithHTTPRouteReadyStatus(controllerName string) HTTPRouteOption {
	return func(route *gatewayapi.HTTPRoute) {
		if len(route.Spec.ParentRefs) > 0 {
			route.Status.RouteStatus.Parents = make([]gatewayapi.RouteParentStatus, len(route.Spec.ParentRefs)*2)
			for i, parentRef := range route.Spec.ParentRefs {
				route.Status.RouteStatus.Parents[i] = gatewayapi.RouteParentStatus{
					ParentRef:      parentRef,
					ControllerName: gatewayapi.GatewayController(controllerName),
					Conditions: []metav1.Condition{
						{
							Type:               string(gatewayapi.RouteConditionAccepted),
							Status:             metav1.ConditionTrue,
							Reason:             "Accepted",
							Message:            "HTTPRoute accepted",
							LastTransitionTime: metav1.Now(),
						},
						{
							Type:               string(gatewayapi.RouteConditionResolvedRefs),
							Status:             metav1.ConditionTrue,
							Reason:             "ResolvedRefs",
							Message:            "HTTPRoute references resolved",
							LastTransitionTime: metav1.Now(),
						},
					},
				}

				route.Status.RouteStatus.Parents[len(route.Spec.ParentRefs)+i] = gatewayapi.RouteParentStatus{
					ParentRef:      parentRef,
					ControllerName: "kuadrant.io/policy-controller",
					Conditions: []metav1.Condition{
						{
							Type:               "kuadrant.io/AuthPolicyAffected",
							Status:             metav1.ConditionTrue,
							Reason:             "Accepted",
							LastTransitionTime: metav1.Now(),
						},
					},
				}
			}
		}
	}
}

// WithGatewayReadyStatus sets the Gateway status to ready (Accepted and Programmed)
func WithGatewayReadyStatus() GatewayOption {
	return func(gw *gatewayapi.Gateway) {
		gw.Status.Conditions = []metav1.Condition{
			{
				Type:               string(gatewayapi.GatewayConditionAccepted),
				Status:             metav1.ConditionTrue,
				Reason:             "Accepted",
				Message:            "Gateway accepted",
				LastTransitionTime: metav1.Now(),
			},
			{
				Type:               string(gatewayapi.GatewayConditionProgrammed),
				Status:             metav1.ConditionTrue,
				Reason:             "Ready",
				Message:            "Gateway is ready",
				LastTransitionTime: metav1.Now(),
			},
		}
	}
}

// Advanced fixture patterns for custom conditions

// KuadrantControllerStatus creates a RouteParentStatus for Kuadrant policy controller
func KuadrantControllerStatus(parentRef gatewayapi.ParentReference, generation int64) gatewayapi.RouteParentStatus {
	return gatewayapi.RouteParentStatus{
		ParentRef:      parentRef,
		ControllerName: "kuadrant.io/policy-controller",
		Conditions: []metav1.Condition{
			{
				Type:               "kuadrant.io/AuthPolicyAffected",
				Status:             metav1.ConditionTrue,
				Reason:             "Accepted",
				Message:            "Object affected by AuthPolicy [openshift-ingress/gateway-auth-policy]",
				LastTransitionTime: metav1.Now(),
				ObservedGeneration: generation,
			},
			{
				Type:               "kuadrant.io/RateLimitPolicyAffected",
				Status:             metav1.ConditionTrue,
				Reason:             "Accepted",
				Message:            "Object affected by RateLimitPolicy [openshift-ingress/gateway-rate-limits]",
				LastTransitionTime: metav1.Now(),
				ObservedGeneration: generation,
			},
			{
				Type:               "kuadrant.io/TokenRateLimitPolicyAffected",
				Status:             metav1.ConditionTrue,
				Reason:             "Accepted",
				Message:            "Object affected by TokenRateLimitPolicy [openshift-ingress/gateway-token-rate-limits]",
				LastTransitionTime: metav1.Now(),
				ObservedGeneration: generation,
			},
		},
	}
}

// GatewayAPIControllerStatus creates a RouteParentStatus for Gateway API controller
func GatewayAPIControllerStatus(parentRef gatewayapi.ParentReference, generation int64) gatewayapi.RouteParentStatus {
	return gatewayapi.RouteParentStatus{
		ParentRef:      parentRef,
		ControllerName: "openshift.io/gateway-controller/v1",
		Conditions: []metav1.Condition{
			{
				Type:               string(gatewayapi.RouteConditionAccepted),
				Status:             metav1.ConditionTrue,
				Reason:             "Accepted",
				Message:            "Route was valid",
				LastTransitionTime: metav1.Now(),
				ObservedGeneration: generation,
			},
			{
				Type:               string(gatewayapi.RouteConditionResolvedRefs),
				Status:             metav1.ConditionTrue,
				Reason:             "ResolvedRefs",
				Message:            "All references resolved",
				LastTransitionTime: metav1.Now(),
				ObservedGeneration: generation,
			},
		},
	}
}

// StatusFunc is a function that creates a RouteParentStatus for a given parent ref and generation
type StatusFunc func(parentRef gatewayapi.ParentReference, generation int64) gatewayapi.RouteParentStatus

// WithHTTPRouteMultipleControllerStatus sets HTTPRoute status with multiple controllers
// This simulates real-world scenarios where policy controllers and gateway controllers
// both add status entries for the same parent ref
func WithHTTPRouteMultipleControllerStatus(parentRef gatewayapi.ParentReference, statusFuncs ...StatusFunc) HTTPRouteOption {
	return func(route *gatewayapi.HTTPRoute) {
		for _, statusFunc := range statusFuncs {
			status := statusFunc(parentRef, route.Generation)
			route.Status.RouteStatus.Parents = append(route.Status.RouteStatus.Parents, status)
		}
	}
}

// WithHTTPRouteNotReadyStatus sets the HTTPRoute status to not ready (for negative testing)
func WithHTTPRouteNotReadyStatus(controllerName, reason, message string) HTTPRouteOption {
	return func(route *gatewayapi.HTTPRoute) {
		if len(route.Spec.ParentRefs) > 0 {
			route.Status.RouteStatus.Parents = make([]gatewayapi.RouteParentStatus, len(route.Spec.ParentRefs))
			for i, parentRef := range route.Spec.ParentRefs {
				route.Status.RouteStatus.Parents[i] = gatewayapi.RouteParentStatus{
					ParentRef:      parentRef,
					ControllerName: gatewayapi.GatewayController(controllerName),
					Conditions: []metav1.Condition{
						{
							Type:               string(gatewayapi.RouteConditionAccepted),
							Status:             metav1.ConditionFalse,
							Reason:             reason,
							Message:            message,
							LastTransitionTime: metav1.Now(),
						},
					},
				}
			}
		}
	}
}

type InferencePoolOption ObjectOption[*igwv1.InferencePool]

func InferencePool(name string, opts ...InferencePoolOption) *igwv1.InferencePool {
	pool := &igwv1.InferencePool{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: igwv1.InferencePoolSpec{
			Selector: igwv1.LabelSelector{
				MatchLabels: make(map[igwv1.LabelKey]igwv1.LabelValue),
			},
			TargetPorts: []igwv1.Port{
				{Number: igwv1.PortNumber(8000)},
			},
			EndpointPickerRef: igwv1.EndpointPickerRef{},
		},
		Status: igwv1.InferencePoolStatus{
			Parents: []igwv1.ParentStatus{},
		},
	}

	for _, opt := range opts {
		opt(pool)
	}

	return pool
}

func WithSelector(key, value string) InferencePoolOption {
	return func(pool *igwv1.InferencePool) {
		if pool.Spec.Selector.MatchLabels == nil {
			pool.Spec.Selector.MatchLabels = make(map[igwv1.LabelKey]igwv1.LabelValue)
		}
		pool.Spec.Selector.MatchLabels[igwv1.LabelKey(key)] = igwv1.LabelValue(value)
	}
}

func WithTargetPort(port int32) InferencePoolOption {
	return func(pool *igwv1.InferencePool) {
		pool.Spec.TargetPorts = []igwv1.Port{
			{Number: igwv1.PortNumber(port)},
		}
	}
}

func WithExtensionRef(group, kind, name string) InferencePoolOption {
	return func(pool *igwv1.InferencePool) {
		pool.Spec.EndpointPickerRef = igwv1.EndpointPickerRef{
			Group: ptr.To(igwv1.Group(group)),
			Kind:  igwv1.Kind(kind),
			Name:  igwv1.ObjectName(name),
			Port:  ptr.To(igwv1.Port{Number: igwv1.PortNumber(8000)}), // GIE v1 requires port when kind is Service
		}
	}
}

func WithInferencePoolReadyStatus() InferencePoolOption {
	return func(pool *igwv1.InferencePool) {
		pool.Status.Parents = []igwv1.ParentStatus{
			{
				ParentRef: igwv1.ParentReference{
					Group:     ptr.To(igwv1.Group("gateway.networking.k8s.io")),
					Kind:      igwv1.Kind("Gateway"),
					Name:      igwv1.ObjectName("kserve-ingress-gateway"),
					Namespace: igwv1.Namespace("kserve"),
				},
				Conditions: []metav1.Condition{
					{
						Type:               string(igwv1.InferencePoolConditionAccepted),
						Status:             metav1.ConditionTrue,
						Reason:             string(igwv1.InferencePoolReasonAccepted),
						Message:            "InferencePool accepted",
						LastTransitionTime: metav1.Now(),
					},
					{
						Type:               string(igwv1.InferencePoolConditionResolvedRefs),
						Status:             metav1.ConditionTrue,
						Reason:             string(igwv1.InferencePoolReasonResolvedRefs),
						Message:            "All references resolved",
						LastTransitionTime: metav1.Now(),
					},
				},
			},
		}
	}
}
