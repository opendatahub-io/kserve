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

	netv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/env"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
	"github.com/kserve/kserve/pkg/constants"
)

const (
	// monitoringNamespaceEnvVar, if set on the controller, is an extra Prometheus
	// peer for a non-default monitoring namespace. RHOAI's default is already
	// listed as defaultRHOAIMonitoringNamespace.
	monitoringNamespaceEnvVar = "MONITORING_NAMESPACE"

	// defaultMonitoringNamespace is where Platform Prometheus runs on OpenShift.
	defaultMonitoringNamespace = "openshift-monitoring"

	// defaultUserWorkloadMonitoringNamespace is where User Workload Monitoring Prometheus runs.
	defaultUserWorkloadMonitoringNamespace = "openshift-user-workload-monitoring"

	// defaultRHOAIMonitoringNamespace is the RHOAI DSCI default monitoring namespace.
	defaultRHOAIMonitoringNamespace = "redhat-ods-monitoring"

	monitoringNetworkPolicyNotOwnedReason = "MonitoringNetworkPolicyNotOwned"
)

// rhoaiWorkloadMonitoringNamespace returns the RHOAI / DSCI monitoring namespace
// from config, the controller env var, or the historical default.
func rhoaiWorkloadMonitoringNamespace(config *Config) string {
	if config != nil && config.MonitoringNamespace != "" {
		return config.MonitoringNamespace
	}
	if ns := env.GetString(monitoringNamespaceEnvVar, ""); ns != "" {
		return ns
	}
	return defaultRHOAIMonitoringNamespace
}

// prometheusPeerNamespaces is the union of OpenShift Platform Monitoring, User
// Workload Monitoring, and the RHOAI/DSCI workload monitoring namespace.
func prometheusPeerNamespaces(config *Config) []string {
	return uniqueNamespaces(
		defaultMonitoringNamespace,
		defaultUserWorkloadMonitoringNamespace,
		rhoaiWorkloadMonitoringNamespace(config),
	)
}

func uniqueNamespaces(nss ...string) []string {
	seen := make(map[string]struct{}, len(nss))
	out := make([]string, 0, len(nss))
	for _, ns := range nss {
		if ns == "" {
			continue
		}
		if _, ok := seen[ns]; ok {
			continue
		}
		seen[ns] = struct{}{}
		out = append(out, ns)
	}
	return out
}

// ingressNamespace is the namespace of the cluster default Gateway.
// Distro builds run on RHOAI (OCP) and RHAII (xKS/OCP), so this must come from
// Config (kserveIngressGateway), not an OpenShift-only default.
// If config is missing, use POD_NAMESPACE like NewConfig, never openshift-ingress.
func ingressNamespace(config *Config) string {
	if config != nil && config.IngressGatewayNamespace != "" {
		return config.IngressGatewayNamespace
	}
	return constants.KServeNamespace
}

// gatewayPeerNamespaces returns the namespaces that can contain the Gateway
// serving this LLMISVC. Gateway API references do not carry a portable source
// Pod selector, so these namespace-only peers are a temporary, documented
// exception. Keep the list limited to configured Gateway namespaces; policy
// builders decide which destination ports are allowed.
func gatewayPeerNamespaces(llmSvc *v1alpha2.LLMInferenceService, ingressNs string) []string {
	if ingressNs == "" {
		ingressNs = constants.KServeNamespace
	}
	if llmSvc == nil || llmSvc.Spec.Router == nil {
		// A service without router configuration has no Gateway route. Do not
		// admit the cluster Gateway namespace just because the controller has a
		// configured default Gateway.
		return nil
	}
	// Inspect route parents before falling back to the managed Gateway. A
	// non-nil but empty Gateway stanza is commonly used with an inline or
	// referenced HTTPRoute and must not silently reintroduce the default
	// namespace when the route names a different parent.
	if llmSvc.Spec.Router.Route != nil && llmSvc.Spec.Router.Route.HTTP != nil {
		if parents := routeParentNamespaces(llmSvc); len(parents) > 0 {
			return parents
		}
		if llmSvc.Spec.Router.Route.HTTP.HasRefs() {
			// Referenced HTTPRoutes are resolved by the reconciler, which can
			// inspect their parentRefs. Do not guess the managed namespace.
			return nil
		}
	}
	if llmSvc.Spec.Router.Gateway == nil {
		return nil
	}
	// An absent gateway ref means the managed global Gateway is the route
	// parent. Once refs are present, only those referenced namespaces are
	// callers; admitting the managed namespace as well would grant access to a
	// Gateway that the service does not use.
	if !llmSvc.Spec.Router.Gateway.HasRefs() {
		return []string{ingressNs}
	}
	seen := map[string]struct{}{}
	out := []string{}
	for _, ref := range llmSvc.Spec.Router.Gateway.Refs {
		ns := string(ref.Namespace)
		if ns == "" {
			ns = llmSvc.GetNamespace()
		}
		if ns == "" {
			continue
		}
		if _, ok := seen[ns]; ok {
			continue
		}
		seen[ns] = struct{}{}
		out = append(out, ns)
	}
	return out
}

// routeParentNamespaces returns explicit parent namespaces from an inline
// HTTPRoute specification. An omitted namespace means the LLMISVC namespace
// according to Gateway API parentRef semantics.
func routeParentNamespaces(llmSvc *v1alpha2.LLMInferenceService) []string {
	if llmSvc == nil || llmSvc.Spec.Router == nil || llmSvc.Spec.Router.Route == nil ||
		llmSvc.Spec.Router.Route.HTTP == nil || !llmSvc.Spec.Router.Route.HTTP.HasSpec() {
		return nil
	}
	namespaces := make([]string, 0, len(llmSvc.Spec.Router.Route.HTTP.Spec.ParentRefs))
	for _, ref := range llmSvc.Spec.Router.Route.HTTP.Spec.ParentRefs {
		if ref.Kind != nil && string(*ref.Kind) != "Gateway" {
			continue
		}
		ns := llmSvc.GetNamespace()
		if ref.Namespace != nil && string(*ref.Namespace) != "" {
			ns = string(*ref.Namespace)
		}
		if ns != "" {
			namespaces = append(namespaces, ns)
		}
	}
	return uniqueNamespaces(namespaces...)
}

func namespaceSelectorPeer(ns string) netv1.NetworkPolicyPeer {
	return netv1.NetworkPolicyPeer{
		NamespaceSelector: &metav1.LabelSelector{
			MatchLabels: map[string]string{
				"kubernetes.io/metadata.name": ns,
			},
		},
	}
}

func (r *LLMISVCReconciler) reconcileNetworkPolicies(ctx context.Context, llmSvc *v1alpha2.LLMInferenceService, config *Config) error {
	// Baseline policies are independent of Prometheus resource reconciliation,
	// so LLMISVC_MONITORING_DISABLED only removes monitoring peers; it must not
	// disable workload policy coverage.
	return reconcileLLMISVCNetworkPolicies(ctx, r, llmSvc, config)
}

func semanticNetworkPolicyIsEqual(expected *netv1.NetworkPolicy, current *netv1.NetworkPolicy) bool {
	return equality.Semantic.DeepEqual(expected.Spec, current.Spec) &&
		equality.Semantic.DeepDerivative(expected.Labels, current.Labels) &&
		equality.Semantic.DeepDerivative(expected.Annotations, current.Annotations)
}
