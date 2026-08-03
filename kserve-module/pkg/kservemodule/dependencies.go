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

	"github.com/opendatahub-io/odh-platform-utilities/api/common"

	platformv1alpha1 "github.com/opendatahub-io/kserve-module/pkg/apis/v1alpha1"
	"github.com/opendatahub-io/odh-platform-utilities/pkg/cluster/olm"
)

type checkType string

const (
	checkCRD          checkType = "crd"
	checkSubscription checkType = "subscription"
	checkOperator     checkType = "operator"

	// availSeverityNone means failures are not reported to DependenciesAvailable.
	// Must differ from common.ConditionSeverityError (which is "").
	availSeverityNone common.ConditionSeverity = "None"

	// Dependency group condition types
	conditionLLMISVCDeps = "KserveLLMInferenceServiceDependencies"
	conditionLLMISVCWideEPDeps = "KserveLLMInferenceServiceWideEPDependencies"
	conditionLLMDWVADeps       = "LLM-D-WVADependencies"

	// OLM subscription names
	rhclSubscription        = "rhcl-operator"
	certManagerSubscription = "openshift-cert-manager-operator"
	lwsSubscription         = "leader-worker-set"
	cmaSubscription         = "openshift-custom-metrics-autoscaler-operator"
)

type conditionFilterFunc func(conditionType string, status string) bool

type dependencyCheck struct {
	name                 string
	checkType            checkType
	crdName              string                                     // Full CRD name (e.g. "authorizationpolicies.security.istio.io")
	subscriptionName     string                                     // Subscription check
	operatorGVK          schema.GroupVersionKind                    // Operator CR GVK
	operatorCRName       string                                     // Operator CR name (empty = list first)
	conditionFilter      conditionFilterFunc                        // Operator condition filter
	availabilitySeverity common.ConditionSeverity                   // availSeverityNone = no report, Error = Ready=False, Info = Ready=True
	platform             string                                     // "ocp", "xks", "" (both)
	conditionGroup       string                                     // group into same condition
	skipFunc             func(kserve *platformv1alpha1.Kserve) bool // true → skip this check
}

type availabilityReason struct {
	severity common.ConditionSeverity
	message  string
}

type dependencyResult struct {
	availReasons []availabilityReason
	allReasons   []string
	groupReasons map[string][]string
}

func crdDep(name, crdName, platform string, availSeverity common.ConditionSeverity) dependencyCheck {
	return dependencyCheck{
		name:                 name,
		checkType:            checkCRD,
		crdName:              crdName,
		platform:             platform,
		availabilitySeverity: availSeverity,
	}
}

func subscriptionDep(name, subName, condGroup, platform string, availSeverity common.ConditionSeverity) dependencyCheck {
	return dependencyCheck{
		name:                 name,
		checkType:            checkSubscription,
		subscriptionName:     subName,
		conditionGroup:       condGroup,
		platform:             platform,
		availabilitySeverity: availSeverity,
	}
}

func operatorDep(name string, gvk schema.GroupVersionKind, crName, condGroup, platform string, availSeverity common.ConditionSeverity, filter conditionFilterFunc) dependencyCheck {
	return dependencyCheck{
		name:                 name,
		checkType:            checkOperator,
		operatorGVK:          gvk,
		operatorCRName:       crName,
		conditionGroup:       condGroup,
		platform:             platform,
		availabilitySeverity: availSeverity,
		conditionFilter:      filter,
	}
}

var kserveDependencies = []dependencyCheck{
	// xks only checks
	// Istio CRDs — optional, degraded-only reporting
	crdDep("istio-destinationrule", "destinationrules.networking.istio.io", "xks", availSeverityNone),
	crdDep("istio-envoyfilter", "envoyfilters.networking.istio.io", "xks", availSeverityNone),
	crdDep("istio-gateway", "gateways.networking.istio.io", "xks", availSeverityNone),
	crdDep("istio-proxyconfig", "proxyconfigs.networking.istio.io", "xks", availSeverityNone),
	crdDep("istio-serviceentry", "serviceentries.networking.istio.io", "xks", availSeverityNone),
	crdDep("istio-sidecar", "sidecars.networking.istio.io", "xks", availSeverityNone),
	crdDep("istio-workloadentry", "workloadentries.networking.istio.io", "xks", availSeverityNone),
	crdDep("istio-workloadgroup", "workloadgroups.networking.istio.io", "xks", availSeverityNone),
	crdDep("istio-authorizationpolicy", "authorizationpolicies.security.istio.io", "xks", availSeverityNone),
	crdDep("istio-peerauthentication", "peerauthentications.security.istio.io", "xks", availSeverityNone),
	crdDep("istio-requestauthentication", "requestauthentications.security.istio.io", "xks", availSeverityNone),
	crdDep("istio-telemetry", "telemetries.telemetry.istio.io", "xks", availSeverityNone),
	crdDep("istio-wasmplugin", "wasmplugins.extensions.istio.io", "xks", availSeverityNone),

	// cert-manager CRDs — critical, kserve cannot function without them
	crdDep("cert-manager-certificate", "certificates.cert-manager.io", "xks", common.ConditionSeverityError),
	crdDep("cert-manager-certificaterequest", "certificaterequests.cert-manager.io", "xks", common.ConditionSeverityError),
	crdDep("cert-manager-issuer", "issuers.cert-manager.io", "xks", common.ConditionSeverityError),
	crdDep("cert-manager-clusterissuer", "clusterissuers.cert-manager.io", "xks", common.ConditionSeverityError),

	// LeaderWorkerSet CRD — optional, group condition only
	{
		name:                 "leaderworkersets",
		checkType:            checkCRD,
		crdName:              "leaderworkersets.leaderworkerset.x-k8s.io",
		platform:             "xks",
		availabilitySeverity: availSeverityNone,
		conditionGroup:       conditionLLMISVCWideEPDeps,
	},

	// OCP Subscription checks — optional, group condition only
	subscriptionDep("Red Hat Connectivity Link", rhclSubscription, conditionLLMISVCDeps, "ocp", availSeverityNone),
	subscriptionDep("Red Hat Connectivity Link (Wide EP)", rhclSubscription, conditionLLMISVCWideEPDeps, "ocp", availSeverityNone),
	subscriptionDep("cert-manager operator", certManagerSubscription, conditionLLMISVCDeps, "ocp", availSeverityNone),
	subscriptionDep("cert-manager operator (Wide EP)", certManagerSubscription, conditionLLMISVCWideEPDeps, "ocp", availSeverityNone),
	subscriptionDep("LeaderWorkerSet", lwsSubscription, conditionLLMISVCWideEPDeps, "ocp", availSeverityNone),

	// OCP LWS Operator health — report to DependenciesAvailable with Info (Ready stays True)
	operatorDep("leaderworkerset-operator",
		schema.GroupVersionKind{Group: "operator.openshift.io", Version: "v1", Kind: "LeaderWorkerSetOperator"},
		"", conditionLLMISVCWideEPDeps, "ocp", common.ConditionSeverityInfo, lwsConditionFilter),
}

var modelControllerDependencies = []dependencyCheck{
	{
		name:                 "Custom Metrics Autoscaler",
		checkType:            checkSubscription,
		subscriptionName:     cmaSubscription,
		conditionGroup:       conditionLLMDWVADeps,
		platform:             "ocp",
		availabilitySeverity: availSeverityNone,
		skipFunc: func(k *platformv1alpha1.Kserve) bool {
			return !isWVAEnabled(k)
		},
	},
}

var allDependencies = slices.Concat(kserveDependencies, modelControllerDependencies)

type checkResultItem struct {
	dep     dependencyCheck
	reasons []string
}

func (r *KserveModuleReconciler) checkDependencies(ctx context.Context, kserve *platformv1alpha1.Kserve) dependencyResult {
	log := ctrl.LoggerFrom(ctx)
	isXKS := r.isKubernetes(ctx)

	result := dependencyResult{
		groupReasons: map[string][]string{
			conditionLLMISVCDeps:       {},
			conditionLLMISVCWideEPDeps: {},
			conditionLLMDWVADeps:       {},
		},
	}

	ch := make(chan checkResultItem, len(allDependencies))
	active := 0

	for _, dep := range allDependencies {
		if dep.platform == "ocp" && isXKS {
			continue
		}
		if dep.platform == "xks" && !isXKS {
			continue
		}
		if dep.skipFunc != nil && dep.skipFunc(kserve) {
			continue
		}

		active++
		go func(d dependencyCheck) {
			var reasons []string
			switch d.checkType {
			case checkCRD:
				reasons = r.checkCRD(ctx, d)
			case checkSubscription:
				reasons = r.checkSubscription(ctx, d)
			case checkOperator:
				reasons = r.checkOperatorHealth(ctx, d)
			}
			ch <- checkResultItem{dep: d, reasons: reasons}
		}(dep)
	}

	for i := 0; i < active; i++ {
		item := <-ch
		if len(item.reasons) == 0 {
			continue
		}
		for _, msg := range item.reasons {
			log.Info("dependency not satisfied", "dependency", item.dep.name,
				"type", item.dep.checkType, "availabilitySeverity", item.dep.availabilitySeverity, "message", msg)

			result.allReasons = append(result.allReasons, msg)

			if item.dep.availabilitySeverity != availSeverityNone {
				result.availReasons = append(result.availReasons, availabilityReason{
					severity: item.dep.availabilitySeverity,
					message:  msg,
				})
			}
			if item.dep.conditionGroup != "" {
				result.groupReasons[item.dep.conditionGroup] = append(
					result.groupReasons[item.dep.conditionGroup], msg)
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
	crd := &unstructured.Unstructured{}
	crd.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "apiextensions.k8s.io",
		Version: "v1",
		Kind:    "CustomResourceDefinition",
	})
	if err := r.Client.Get(ctx, client.ObjectKey{Name: dep.crdName}, crd); err != nil {
		if k8serr.IsNotFound(err) {
			return []string{fmt.Sprintf("%s CRD not found (%s)", dep.name, dep.crdName)}
		}
		return []string{fmt.Sprintf("%s CRD lookup failed (%s): %v", dep.name, dep.crdName, err)}
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
	// Skip checks when context is cancelled to avoid false-positive dependency errors.
	if ctx.Err() != nil {
		return nil
	}
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

func hasCriticalFailure(result dependencyResult) bool {
	for _, ar := range result.availReasons {
		if ar.severity == common.ConditionSeverityError {
			return true
		}
	}
	return false
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
