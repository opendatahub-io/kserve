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
	"context"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/validation"
	clientretry "k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	"knative.dev/pkg/kmeta"
	"knative.dev/pkg/network"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
	"github.com/kserve/kserve/pkg/constants"
	"github.com/kserve/kserve/pkg/utils"
)

const (
	tracingNetworkPolicySuffix = "-otlp-egress"
	tracingNPComponentLabel    = "llm-tracing"
	defaultOTLPPort            = 4317
	tracingServiceIndex        = "kserve.io/tracing-service"
	tracingBaseRefsIndex       = "kserve.io/tracing-base-refs"
)

func tracingNetworkPolicyName(llmSvc *v1alpha2.LLMInferenceService) string {
	return kmeta.ChildName(llmSvc.GetName(), tracingNetworkPolicySuffix)
}

func (r *LLMISVCReconciler) reconcileTracingNetworkPolicy(ctx context.Context, llmSvc *v1alpha2.LLMInferenceService) error {
	if utils.GetForceStopRuntime(llmSvc) || llmSvc.Spec.Tracing == nil || llmSvc.GetAnnotations()[constants.EnableTracingEgressNetworkPolicyAnnotationKey] != "true" {
		return r.cleanupTracingNetworkPolicy(ctx, llmSvc)
	}

	tracingEndpoint := ptr.Deref(llmSvc.Spec.Tracing.ExporterEndpoint, "")
	otlpPeer, otlpPort, hasCrossNamespaceOTLP, supported, err := r.otlpPeerForEndpoint(ctx, llmSvc)
	if err != nil {
		return err
	}
	if tracingEndpoint != "" && !supported {
		log.FromContext(ctx).V(1).Info("OTLP endpoint is not an existing cluster-local Service with a pod selector; no OTLP egress rule will be added",
			"namespace", llmSvc.GetNamespace(), "name", llmSvc.GetName())
	}

	var apiRule *netv1.NetworkPolicyEgressRule
	if r.Config != nil {
		if rule, ok := apiServerEgressRule(r.Config.Host); ok {
			apiRule = &rule
		} else {
			log.FromContext(ctx).V(1).Info("Kubernetes API endpoint is not an IP address; API egress rule will be omitted",
				"namespace", llmSvc.GetNamespace(), "name", llmSvc.GetName())
		}
	}
	expected := expectedTracingNetworkPolicy(llmSvc, apiRule, otlpPeer, otlpPort, hasCrossNamespaceOTLP)
	if err := Reconcile(ctx, r, llmSvc, &netv1.NetworkPolicy{}, expected, semanticNetworkPolicyIsEqual); err != nil {
		return fmt.Errorf("failed to reconcile tracing network policy %s/%s: %w", expected.GetNamespace(), expected.GetName(), err)
	}
	return nil
}

func (r *LLMISVCReconciler) cleanupTracingNetworkPolicy(ctx context.Context, llmSvc *v1alpha2.LLMInferenceService) error {
	expected := expectedTracingNetworkPolicy(llmSvc, nil, netv1.NetworkPolicyPeer{}, 0, false)
	if err := Delete[*v1alpha2.LLMInferenceService](ctx, r, llmSvc, expected); err != nil {
		return fmt.Errorf("failed to delete tracing network policy: %w", err)
	}
	return nil
}

func setupTracingServiceIndexes(ctx context.Context, indexer client.FieldIndexer) error {
	if err := indexer.IndexField(ctx, &v1alpha2.LLMInferenceService{}, tracingServiceIndex, func(object client.Object) []string {
		llmSvc, ok := object.(*v1alpha2.LLMInferenceService)
		if !ok {
			return nil
		}
		return tracingServiceIndexValues(llmSvc)
	}); err != nil {
		return fmt.Errorf("failed to index tracing services: %w", err)
	}
	if err := indexer.IndexField(ctx, &v1alpha2.LLMInferenceService{}, tracingBaseRefsIndex, func(object client.Object) []string {
		llmSvc, ok := object.(*v1alpha2.LLMInferenceService)
		if !ok {
			return nil
		}
		return tracingBaseRefsIndexValues(llmSvc)
	}); err != nil {
		return fmt.Errorf("failed to index tracing baseRefs: %w", err)
	}
	return nil
}

func tracingServiceIndexValues(llmSvc *v1alpha2.LLMInferenceService) []string {
	if llmSvc == nil || llmSvc.GetAnnotations()[constants.EnableTracingEgressNetworkPolicyAnnotationKey] != "true" || llmSvc.Spec.Tracing == nil {
		return nil
	}
	endpoint, valid := parseOTLPServiceEndpoint(ptr.Deref(llmSvc.Spec.Tracing.ExporterEndpoint, ""), llmSvc.GetNamespace())
	if !valid {
		return nil
	}
	return []string{tracingServiceKey(endpoint.namespace, endpoint.serviceName)}
}

func tracingBaseRefsIndexValues(llmSvc *v1alpha2.LLMInferenceService) []string {
	if llmSvc == nil || llmSvc.GetAnnotations()[constants.EnableTracingEgressNetworkPolicyAnnotationKey] != "true" || len(llmSvc.Spec.BaseRefs) == 0 {
		return nil
	}
	return []string{"true"}
}

func tracingServiceKey(namespace, name string) string {
	return namespace + "/" + name
}

func otlpServiceChangePredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(_ event.CreateEvent) bool {
			return true
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldService, oldOK := e.ObjectOld.(*corev1.Service)
			newService, newOK := e.ObjectNew.(*corev1.Service)
			return oldOK && newOK && !equality.Semantic.DeepEqual(oldService.Spec.Selector, newService.Spec.Selector)
		},
		DeleteFunc: func(_ event.DeleteEvent) bool {
			return true
		},
	}
}

func (r *LLMISVCReconciler) enqueueOnOTLPServiceChange(logger logr.Logger) handler.EventHandler {
	logger = logger.WithName("enqueueOnOTLPServiceChange")

	return handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, object client.Object) []reconcile.Request {
		service, ok := object.(*corev1.Service)
		if !ok {
			return nil
		}

		serviceKey := tracingServiceKey(service.Namespace, service.Name)
		candidates := make(map[types.NamespacedName]*v1alpha2.LLMInferenceService)
		indexed := &v1alpha2.LLMInferenceServiceList{}
		if err := clientretry.OnError(clientretry.DefaultRetry, func(error) bool { return true }, func() error {
			return r.List(ctx, indexed, client.MatchingFields{tracingServiceIndex: serviceKey})
		}); err != nil {
			logger.Error(err, "failed to list indexed LLMInferenceServices for OTLP Service change",
				"service", serviceKey)
			return nil
		}
		for i := range indexed.Items {
			llmSvc := &indexed.Items[i]
			candidates[types.NamespacedName{Namespace: llmSvc.Namespace, Name: llmSvc.Name}] = llmSvc
		}

		baseRefCandidates := &v1alpha2.LLMInferenceServiceList{}
		if err := clientretry.OnError(clientretry.DefaultRetry, func(error) bool { return true }, func() error {
			return r.List(ctx, baseRefCandidates, client.MatchingFields{tracingBaseRefsIndex: "true"})
		}); err != nil {
			logger.Error(err, "failed to list baseRef LLMInferenceServices for OTLP Service change",
				"service", serviceKey)
			return nil
		}
		for i := range baseRefCandidates.Items {
			llmSvc := &baseRefCandidates.Items[i]
			candidates[types.NamespacedName{Namespace: llmSvc.Namespace, Name: llmSvc.Name}] = llmSvc
		}

		requests := make([]reconcile.Request, 0, len(candidates))
		for _, llmSvc := range candidates {
			if utils.GetForceStopRuntime(llmSvc) || llmSvc.GetAnnotations()[constants.EnableTracingEgressNetworkPolicyAnnotationKey] != "true" {
				continue
			}
			if len(llmSvc.Spec.BaseRefs) > 0 {
				requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{
					Namespace: llmSvc.GetNamespace(),
					Name:      llmSvc.GetName(),
				}})
				continue
			}
			if llmSvc.Spec.Tracing == nil {
				continue
			}

			endpoint, valid := parseOTLPServiceEndpoint(ptr.Deref(llmSvc.Spec.Tracing.ExporterEndpoint, ""), llmSvc.GetNamespace())
			if valid && endpoint.serviceName == service.Name && endpoint.namespace == service.Namespace {
				requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{
					Namespace: llmSvc.GetNamespace(),
					Name:      llmSvc.GetName(),
				}})
			}
		}

		return requests
	})
}

// apiServerEgressRule narrows API access to the IP and port used by the
// controller. A hostname cannot be safely represented by an IPBlock, so it
// intentionally produces no rule rather than allowing all destinations.
func apiServerEgressRule(host string) (netv1.NetworkPolicyEgressRule, bool) {
	parsed, err := url.Parse(host)
	if err != nil || parsed.Hostname() == "" {
		return netv1.NetworkPolicyEgressRule{}, false
	}
	ip := net.ParseIP(parsed.Hostname())
	if ip == nil {
		return netv1.NetworkPolicyEgressRule{}, false
	}

	apiPort := 443
	if port := parsed.Port(); port != "" {
		apiPort, err = strconv.Atoi(port)
		if err != nil || apiPort < 1 || apiPort > 65535 {
			return netv1.NetworkPolicyEgressRule{}, false
		}
	}
	cidrSuffix := "/32"
	if ip.To4() == nil {
		cidrSuffix = "/128"
	}
	tcp := corev1.ProtocolTCP
	return netv1.NetworkPolicyEgressRule{
		Ports: []netv1.NetworkPolicyPort{{Protocol: &tcp, Port: ptr.To(intstr.FromInt(apiPort))}},
		To:    []netv1.NetworkPolicyPeer{{IPBlock: &netv1.IPBlock{CIDR: ip.String() + cidrSuffix}}},
	}, true
}

func expectedTracingNetworkPolicy(llmSvc *v1alpha2.LLMInferenceService, apiRule *netv1.NetworkPolicyEgressRule, otlpPeer netv1.NetworkPolicyPeer, otlpPort int32, hasCrossNamespaceOTLP bool) *netv1.NetworkPolicy {
	tcp := corev1.ProtocolTCP
	udp := corev1.ProtocolUDP
	port := func(protocol corev1.Protocol, value int32) netv1.NetworkPolicyPort {
		return netv1.NetworkPolicyPort{
			Protocol: &protocol,
			Port:     ptr.To(intstr.FromInt32(value)),
		}
	}

	policy := &netv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      tracingNetworkPolicyName(llmSvc),
			Namespace: llmSvc.GetNamespace(),
			Labels: map[string]string{
				constants.KubernetesPartOfLabelKey:    constants.LLMInferenceServicePartOfValue,
				constants.KubernetesAppNameLabelKey:   llmSvc.GetName(),
				constants.KubernetesComponentLabelKey: tracingNPComponentLabel,
			},
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(llmSvc, v1alpha2.LLMInferenceServiceGVK),
			},
		},
		Spec: netv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{
				constants.KubernetesPartOfLabelKey:  constants.LLMInferenceServicePartOfValue,
				constants.KubernetesAppNameLabelKey: llmSvc.GetName(),
			}},
			PolicyTypes: []netv1.PolicyType{netv1.PolicyTypeEgress},
			Egress: []netv1.NetworkPolicyEgressRule{
				{Ports: []netv1.NetworkPolicyPort{
					port(udp, 53), port(tcp, 53), port(udp, 5353), port(tcp, 5353),
				}},
				{To: []netv1.NetworkPolicyPeer{{PodSelector: &metav1.LabelSelector{}}}},
			},
		},
	}
	if apiRule != nil {
		policy.Spec.Egress = append([]netv1.NetworkPolicyEgressRule{policy.Spec.Egress[0], *apiRule}, policy.Spec.Egress[1:]...)
	}
	if hasCrossNamespaceOTLP {
		policy.Spec.Egress = append(policy.Spec.Egress, netv1.NetworkPolicyEgressRule{
			Ports: []netv1.NetworkPolicyPort{port(tcp, otlpPort)},
			To:    []netv1.NetworkPolicyPeer{otlpPeer},
		})
	}
	return policy
}

type otlpServiceEndpoint struct {
	serviceName string
	namespace   string
	port        int32
}

// parseOTLPServiceEndpoint parses a cluster-local Service endpoint. The
// endpoint must resolve to an existing Service before it can be used to build
// an egress peer because DNS shape alone cannot distinguish Service names from
// external hosts.
func parseOTLPServiceEndpoint(endpoint, serviceNamespace string) (otlpServiceEndpoint, bool) {
	parsed, err := url.Parse(endpoint)
	if err != nil || (!strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https")) || parsed.Hostname() == "" {
		return otlpServiceEndpoint{}, false
	}

	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if host == "localhost" || net.ParseIP(host) != nil {
		return otlpServiceEndpoint{}, false
	}
	hostParts := strings.Split(host, ".")
	clusterDomain := strings.TrimSuffix(strings.ToLower(network.GetClusterDomainName()), ".")
	var namespace string
	switch {
	case len(hostParts) == 1:
		namespace = serviceNamespace
	case len(hostParts) == 2:
		// Kubernetes search paths resolve service.namespace from a pod.
		namespace = hostParts[1]
	case len(hostParts) == 3 && hostParts[2] == "svc":
		namespace = hostParts[1]
	case len(hostParts) > 3 && hostParts[2] == "svc" && strings.Join(hostParts[3:], ".") == clusterDomain:
		namespace = hostParts[1]
	default:
		return otlpServiceEndpoint{}, false
	}
	if namespace == "" || len(validation.IsDNS1123Label(hostParts[0])) > 0 || len(validation.IsDNS1123Label(namespace)) > 0 {
		return otlpServiceEndpoint{}, false
	}

	port := int32(defaultOTLPPort)
	if portString := parsed.Port(); portString != "" {
		value, err := strconv.ParseInt(portString, 10, 32)
		if err != nil || value < 1 || value > 65535 {
			return otlpServiceEndpoint{}, false
		}
		port = int32(value)
	}
	return otlpServiceEndpoint{serviceName: hostParts[0], namespace: namespace, port: port}, true
}

func (r *LLMISVCReconciler) otlpPeerForEndpoint(ctx context.Context, llmSvc *v1alpha2.LLMInferenceService) (netv1.NetworkPolicyPeer, int32, bool, bool, error) {
	endpoint := ptr.Deref(llmSvc.Spec.Tracing.ExporterEndpoint, "")
	parsed, valid := parseOTLPServiceEndpoint(endpoint, llmSvc.GetNamespace())
	if !valid {
		return netv1.NetworkPolicyPeer{}, 0, false, false, nil
	}

	service := &corev1.Service{}
	serviceKey := types.NamespacedName{Name: parsed.serviceName, Namespace: parsed.namespace}
	if err := r.Get(ctx, serviceKey, service); err != nil {
		if apierrors.IsNotFound(err) {
			return netv1.NetworkPolicyPeer{}, 0, false, false, nil
		}
		return netv1.NetworkPolicyPeer{}, 0, false, false, fmt.Errorf("failed to get OTLP Service %s/%s: %w", parsed.namespace, parsed.serviceName, err)
	}

	peer, port, hasPeer, supported := otlpPeerForService(parsed, llmSvc.GetNamespace(), service)
	return peer, port, hasPeer, supported, nil
}

// otlpPeerForService scopes a cross-namespace peer to the Service's selected
// pods. Same-namespace endpoints are already covered by the namespace rule.
func otlpPeerForService(endpoint otlpServiceEndpoint, serviceNamespace string, service *corev1.Service) (netv1.NetworkPolicyPeer, int32, bool, bool) {
	if service == nil || service.GetName() != endpoint.serviceName || service.GetNamespace() != endpoint.namespace {
		return netv1.NetworkPolicyPeer{}, 0, false, false
	}
	if endpoint.namespace == serviceNamespace {
		return netv1.NetworkPolicyPeer{}, endpoint.port, false, true
	}
	if len(service.Spec.Selector) == 0 {
		return netv1.NetworkPolicyPeer{}, 0, false, false
	}

	peer := namespaceSelectorPeer(endpoint.namespace)
	peer.PodSelector = &metav1.LabelSelector{MatchLabels: service.Spec.Selector}
	return peer, endpoint.port, true, true
}
