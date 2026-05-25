package kservemodule

import (
	"context"
	"fmt"
	"slices"

	k8serr "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/opendatahub-io/odh-platform-utilities/pkg/cluster"
	"github.com/opendatahub-io/odh-platform-utilities/pkg/cluster/olm"
)

type checkType string

const (
	checkCRD          checkType = "crd"
	checkSubscription checkType = "subscription"
	checkOperator     checkType = "operator"

	// Dependency group condition types
	conditionLLMISVCDeps       = "LLMInferenceServiceDependencies"
	conditionLLMISVCWideEPDeps = "LLMInferenceServiceWideEPDependencies"
	conditionLLMDWVADeps       = "LLMDWVADependencies"

	// OLM subscription names
	rhclSubscription        = "rhcl-operator"
	certManagerSubscription = "openshift-cert-manager-operator"
	lwsSubscription         = "leader-worker-set"
	cmaSubscription         = "openshift-custom-metrics-autoscaler-operator"
)

type conditionFilterFunc func(conditionType string, status string) bool

type dependencyCheck struct {
	name             string
	checkType        checkType
	groupKind        schema.GroupKind        // CRD check
	subscriptionName string                  // Subscription check
	operatorGVK      schema.GroupVersionKind // Operator CR GVK
	operatorCRName   string                  // Operator CR name (empty = list first)
	conditionFilter  conditionFilterFunc     // Operator condition filter
	critical         bool                    // true → Ready=False, false → Degraded
	platform         string                  // "ocp", "xks", "" (both)
	conditionGroup   string                  // group into same condition
}

type dependencyResult struct {
	degradedReasons []string
	criticalErrors  []string
	groupReasons    map[string][]string
}

func crdDep(name, group, kind, platform string, critical bool) dependencyCheck {
	return dependencyCheck{
		name:      name,
		checkType: checkCRD,
		groupKind: schema.GroupKind{Group: group, Kind: kind},
		platform:  platform,
		critical:  critical,
	}
}

func subscriptionDep(name, subName, condGroup, platform string, critical bool) dependencyCheck {
	return dependencyCheck{
		name:             name,
		checkType:        checkSubscription,
		subscriptionName: subName,
		conditionGroup:   condGroup,
		platform:         platform,
		critical:         critical,
	}
}

func operatorDep(name string, gvk schema.GroupVersionKind, crName, condGroup, platform string, critical bool, filter conditionFilterFunc) dependencyCheck {
	return dependencyCheck{
		name:            name,
		checkType:       checkOperator,
		operatorGVK:     gvk,
		operatorCRName:  crName,
		conditionGroup:  condGroup,
		platform:        platform,
		critical:        critical,
		conditionFilter: filter,
	}
}

var kserveDependencies = []dependencyCheck{
	// xks only checks
	// Istio CRDs
	crdDep("istio-destinationrule", "networking.istio.io", "DestinationRule", "xks", false),
	crdDep("istio-envoyfilter", "networking.istio.io", "EnvoyFilter", "xks", false),
	crdDep("istio-gateway", "networking.istio.io", "Gateway", "xks", false),
	crdDep("istio-proxyconfig", "networking.istio.io", "ProxyConfig", "xks", false),
	crdDep("istio-serviceentry", "networking.istio.io", "ServiceEntry", "xks", false),
	crdDep("istio-sidecar", "networking.istio.io", "Sidecar", "xks", false),
	crdDep("istio-workloadentry", "networking.istio.io", "WorkloadEntry", "xks", false),
	crdDep("istio-workloadgroup", "networking.istio.io", "WorkloadGroup", "xks", false),
	crdDep("istio-authorizationpolicy", "security.istio.io", "AuthorizationPolicy", "xks", false),
	crdDep("istio-peerauthentication", "security.istio.io", "PeerAuthentication", "xks", false),
	crdDep("istio-requestauthentication", "security.istio.io", "RequestAuthentication", "xks", false),
	crdDep("istio-telemetry", "telemetry.istio.io", "Telemetry", "xks", false),
	crdDep("istio-wasmplugin", "extensions.istio.io", "WasmPlugin", "xks", false),

	// cert-manager CRDs
	crdDep("cert-manager-certificate", "cert-manager.io", "Certificate", "xks", true),
	crdDep("cert-manager-certificaterequest", "cert-manager.io", "CertificateRequest", "xks", true),
	crdDep("cert-manager-issuer", "cert-manager.io", "Issuer", "xks", true),
	crdDep("cert-manager-clusterissuer", "cert-manager.io", "ClusterIssuer", "xks", true),

	// LeaderWorkerSet CRD
	crdDep("leaderworkerset", "leaderworkerset.x-k8s.io", "LeaderWorkerSet", "xks", false),

	// OCP Subscription checks
	subscriptionDep("Red Hat Connectivity Link", rhclSubscription, conditionLLMISVCDeps, "ocp", false),
	subscriptionDep("Red Hat Connectivity Link (Wide EP)", rhclSubscription, conditionLLMISVCWideEPDeps, "ocp", false),
	subscriptionDep("cert-manager operator", certManagerSubscription, conditionLLMISVCDeps, "ocp", false),
	subscriptionDep("cert-manager operator (Wide EP)", certManagerSubscription, conditionLLMISVCWideEPDeps, "ocp", false),
	subscriptionDep("LeaderWorkerSet", lwsSubscription, conditionLLMISVCWideEPDeps, "ocp", false),

	// OCP LWS Operator health
	operatorDep("leaderworkerset-operator",
		schema.GroupVersionKind{Group: "operator.openshift.io", Version: "v1", Kind: "LeaderWorkerSet"},
		"", conditionLLMISVCWideEPDeps, "ocp", false, lwsConditionFilter),
}

var modelControllerDependencies = []dependencyCheck{
	subscriptionDep("Custom Metrics Autoscaler", cmaSubscription, conditionLLMDWVADeps, "ocp", false),
}

var allDependencies = slices.Concat(kserveDependencies, modelControllerDependencies)

func (r *KserveModuleReconciler) checkDependencies(ctx context.Context) dependencyResult {
	log := ctrl.LoggerFrom(ctx)
	isXKS := r.isKubernetes(ctx)

	result := dependencyResult{
		groupReasons: map[string][]string{
			conditionLLMISVCDeps:       {},
			conditionLLMISVCWideEPDeps: {},
			conditionLLMDWVADeps:       {},
		},
	}

	for _, dep := range allDependencies {
		if dep.platform == "ocp" && isXKS {
			continue
		}
		if dep.platform == "xks" && !isXKS {
			continue
		}

		var reasons []string
		switch dep.checkType {
		case checkCRD:
			reasons = r.checkCRD(ctx, dep)
		case checkSubscription:
			reasons = r.checkSubscription(ctx, dep)
		case checkOperator:
			reasons = r.checkOperatorHealth(ctx, dep)
		}

		for _, msg := range reasons {
			log.Info("dependency not satisfied", "dependency", dep.name,
				"type", dep.checkType, "critical", dep.critical, "message", msg)

			if dep.critical {
				result.criticalErrors = append(result.criticalErrors, msg)
			} else if dep.conditionGroup != "" {
				result.groupReasons[dep.conditionGroup] = append(
					result.groupReasons[dep.conditionGroup], msg)
			} else {
				result.degradedReasons = append(result.degradedReasons, msg)
			}
		}
	}

	return result
}

func (r *KserveModuleReconciler) checkCRD(ctx context.Context, dep dependencyCheck) []string {
  	// Skip checks when context is cancelled to avoid false-positive dependency errors.
	if ctx.Err() != nil {
		return nil
	}
	if err := cluster.CustomResourceDefinitionExists(ctx, r.Client, dep.groupKind); err != nil {
		return []string{fmt.Sprintf("%s CRD check failed (%s): %v", dep.name, dep.groupKind, err)}
	}
	return nil
}

func (r *KserveModuleReconciler) checkSubscription(ctx context.Context, dep dependencyCheck) []string {
	// Skip checks when context is cancelled to avoid false-positive dependency errors.
	if ctx.Err() != nil {
		return nil
	}
	found, err := olm.SubscriptionExists(ctx, r.Client, dep.subscriptionName)
	if err != nil {
		if meta.IsNoMatchError(err) {
			return nil
		}
		return []string{fmt.Sprintf("%s subscription check failed: %v", dep.name, err)}
	}
	if !found {
		return []string{fmt.Sprintf("%s not installed", dep.name)}
	}
	return nil
}

func (r *KserveModuleReconciler) checkOperatorHealth(ctx context.Context, dep dependencyCheck) []string {
	if dep.conditionFilter == nil {
		return nil
	}

	cr, err := r.fetchOperatorCR(ctx, dep)
	if err != nil {
		if meta.IsNoMatchError(err) || k8serr.IsNotFound(err) {
			return nil
		}
		return []string{fmt.Sprintf("%s: failed to get operator CR: %v", dep.operatorGVK.Kind, err)}
	}

	return collectDegradedConditions(cr, dep)
}

func (r *KserveModuleReconciler) fetchOperatorCR(ctx context.Context, dep dependencyCheck) (*unstructured.Unstructured, error) {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(dep.operatorGVK)

	if dep.operatorCRName != "" {
		err := r.Client.Get(ctx, client.ObjectKey{Name: dep.operatorCRName}, obj)
		return obj, err
	}

	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(dep.operatorGVK)
	if err := r.Client.List(ctx, list, client.Limit(1)); err != nil {
		return nil, err
	}
	if len(list.Items) == 0 {
		return nil, k8serr.NewNotFound(
			schema.GroupResource{Group: dep.operatorGVK.Group, Resource: dep.operatorGVK.Kind}, "")
	}
	return &list.Items[0], nil
}

func collectDegradedConditions(cr *unstructured.Unstructured, dep dependencyCheck) []string {
	conditions, found, err := unstructured.NestedSlice(cr.Object, "status", "conditions")
	if err != nil || !found {
		return nil
	}

	var degraded []string
	for _, c := range conditions {
		condMap, ok := c.(map[string]any)
		if !ok {
			continue
		}

		condType, found, _ := unstructured.NestedString(condMap, "type")
		if !found {
			continue
		}
		condStatus, found, _ := unstructured.NestedString(condMap, "status")
		if !found {
			continue
		}

		if !dep.conditionFilter(condType, condStatus) {
			continue
		}

		reason, _, _ := unstructured.NestedString(condMap, "reason")
		message, _, _ := unstructured.NestedString(condMap, "message")

		detail := fmt.Sprintf("%s %s: %s=%s", dep.operatorGVK.Kind, cr.GetName(), condType, condStatus)
		if reason != "" {
			detail += fmt.Sprintf(" (%s)", reason)
		}
		if message != "" {
			detail += fmt.Sprintf(": %s", message)
		}
		degraded = append(degraded, detail)
	}

	return degraded
}

// lwsConditionFilter returns true when the given condition indicates an unhealthy state.
func lwsConditionFilter(condType, condStatus string) bool {
	switch condType {
	case "Degraded", "TargetConfigControllerDegraded":
		return condStatus == "True"
	case "Available":
		return condStatus == "False"
	default:
		return false
	}
}
