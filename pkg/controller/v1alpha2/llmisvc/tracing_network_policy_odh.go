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
)

func tracingNetworkPolicyName(llmSvc *v1alpha2.LLMInferenceService) string {
	return kmeta.ChildName(llmSvc.GetName(), tracingNetworkPolicySuffix)
}

func (r *LLMISVCReconciler) reconcileTracingNetworkPolicy(ctx context.Context, llmSvc *v1alpha2.LLMInferenceService) error {
	if utils.GetForceStopRuntime(llmSvc) || llmSvc.Spec.Tracing == nil || llmSvc.GetAnnotations()[constants.EnableTracingEgressNetworkPolicyAnnotationKey] != "true" {
		setTracingServiceStatusAnnotation(llmSvc, "")
		return r.cleanupTracingNetworkPolicy(ctx, llmSvc)
	}

	tracingEndpoint := ptr.Deref(llmSvc.Spec.Tracing.ExporterEndpoint, "")
	otlpPeer, otlpPort, hasCrossNamespaceOTLP, supported, err := r.otlpPeerForEndpoint(ctx, llmSvc)
	if err != nil {
		return err
	}
	serviceKey := ""
	if endpoint, valid := parseOTLPServiceEndpoint(tracingEndpoint, llmSvc.GetNamespace()); valid {
		serviceKey = tracingServiceKey(endpoint.namespace, endpoint.serviceName)
	}
	setTracingServiceStatusAnnotation(llmSvc, serviceKey)
	if tracingEndpoint != "" && !supported {
		log.FromContext(ctx).V(1).Info("OTLP endpoint is not an existing cluster-local Service with a pod selector; no OTLP egress rule will be added",
			"namespace", llmSvc.GetNamespace(), "name", llmSvc.GetName())
	}

	expected := expectedTracingNetworkPolicy(llmSvc, otlpPeer, otlpPort, hasCrossNamespaceOTLP)
	if err := Reconcile(ctx, r, llmSvc, &netv1.NetworkPolicy{}, expected, semanticNetworkPolicyIsEqual); err != nil {
		return fmt.Errorf("failed to reconcile tracing network policy %s/%s: %w", expected.GetNamespace(), expected.GetName(), err)
	}
	return nil
}

func (r *LLMISVCReconciler) cleanupTracingNetworkPolicy(ctx context.Context, llmSvc *v1alpha2.LLMInferenceService) error {
	expected := expectedTracingNetworkPolicy(llmSvc, netv1.NetworkPolicyPeer{}, 0, false)
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
	return nil
}

func tracingServiceIndexValues(llmSvc *v1alpha2.LLMInferenceService) []string {
	if llmSvc == nil || utils.GetForceStopRuntime(llmSvc) || llmSvc.GetAnnotations()[constants.EnableTracingEgressNetworkPolicyAnnotationKey] != "true" {
		return nil
	}

	// The spec covers direct endpoints before status is persisted; status covers
	// endpoints resolved from merged base references.
	var serviceKeys []string
	if llmSvc.Spec.Tracing != nil {
		if endpoint, valid := parseOTLPServiceEndpoint(ptr.Deref(llmSvc.Spec.Tracing.ExporterEndpoint, ""), llmSvc.GetNamespace()); valid {
			serviceKeys = append(serviceKeys, tracingServiceKey(endpoint.namespace, endpoint.serviceName))
		}
	}
	serviceKey := llmSvc.Status.Annotations[constants.LLMTracingServiceStatusAnnotationKey]
	if serviceKey != "" {
		for _, indexedKey := range serviceKeys {
			if indexedKey == serviceKey {
				return serviceKeys
			}
		}
		serviceKeys = append(serviceKeys, serviceKey)
	}
	return serviceKeys
}

func setTracingServiceStatusAnnotation(llmSvc *v1alpha2.LLMInferenceService, serviceKey string) {
	if serviceKey == "" {
		if llmSvc.Status.Annotations != nil {
			delete(llmSvc.Status.Annotations, constants.LLMTracingServiceStatusAnnotationKey)
		}
		return
	}
	if llmSvc.Status.Annotations == nil {
		llmSvc.Status.Annotations = make(map[string]string)
	}
	llmSvc.Status.Annotations[constants.LLMTracingServiceStatusAnnotationKey] = serviceKey
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
		indexed := &v1alpha2.LLMInferenceServiceList{}
		// A Service event can arrive while the informer cache is recovering.
		// Retry the indexed lookup briefly instead of dropping that event.
		if err := clientretry.OnError(clientretry.DefaultRetry, func(error) bool { return true }, func() error {
			return r.List(ctx, indexed, client.MatchingFields{tracingServiceIndex: serviceKey})
		}); err != nil {
			logger.Error(err, "failed to list indexed LLMInferenceServices for OTLP Service change",
				"service", serviceKey)
			return nil
		}

		requests := make([]reconcile.Request, 0, len(indexed.Items))
		for i := range indexed.Items {
			llmSvc := &indexed.Items[i]
			requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{
				Namespace: llmSvc.GetNamespace(),
				Name:      llmSvc.GetName(),
			}})
		}

		return requests
	})
}

func expectedTracingNetworkPolicy(llmSvc *v1alpha2.LLMInferenceService, otlpPeer netv1.NetworkPolicyPeer, otlpPort int32, hasCrossNamespaceOTLP bool) *netv1.NetworkPolicy {
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
				// Keep the original compatibility rule. NetworkPolicy handling of
				// Service-IP DNAT is implementation-dependent, and workloads also
				// need HTTPS for model downloads.
				{Ports: []netv1.NetworkPolicyPort{port(tcp, 443), port(tcp, 6443)}},
				{To: []netv1.NetworkPolicyPeer{{PodSelector: &metav1.LabelSelector{}}}},
			},
		},
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
