package kservemodule

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8serr "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/opendatahub-io/odh-platform-utilities/api/common"
	"github.com/opendatahub-io/odh-platform-utilities/pkg/cluster"

	platformv1alpha1 "github.com/opendatahub-io/kserve-module/pkg/apis/v1alpha1"
)

const (
	odhDashboardConfigName         = "odh-dashboard-config"
	hwpNameAnnotation              = "opendatahub.io/hardware-profile-name"
	hwpNamespaceAnnotation         = "opendatahub.io/hardware-profile-namespace"
	acceleratorNameAnnotation      = "opendatahub.io/accelerator-name"
	acceleratorProfileNSAnnotation = "opendatahub.io/accelerator-profile-namespace"
	kserveDeploymentModeAnnotation = "serving.kserve.io/deploymentMode"
	kserveDeploymentModeServerless = "Serverless"
	containerSizeHWPPrefix         = "containersize-"
	customServingHWP               = "custom-serving"
	kueueQueueNameLabel            = "kueue.x-k8s.io/queue-name"
	kueueManagedLabel              = "kueue.openshift.io/managed"
	kueueLegacyManagedLabel        = "kueue-managed"
)

//nolint:gochecknoglobals // Immutable GVK constants used only within this file.
var (
	inferenceServiceGVK   = schema.GroupVersionKind{Group: "serving.kserve.io", Version: "v1beta1", Kind: "InferenceService"}
	servingRuntimeGVK     = schema.GroupVersionKind{Group: "serving.kserve.io", Version: "v1alpha1", Kind: "ServingRuntime"}
	hardwareProfileGVK    = schema.GroupVersionKind{Group: "infrastructure.opendatahub.io", Version: "v1", Kind: "HardwareProfile"}
	acceleratorProfileGVK = schema.GroupVersionKind{Group: "dashboard.opendatahub.io", Version: "v1", Kind: "AcceleratorProfile"}
	odhDashboardConfigGVK = schema.GroupVersionKind{Group: "opendatahub.io", Version: "v1alpha", Kind: "OdhDashboardConfig"}
)

// containerSize mirrors the ContainerSize struct from opendatahub-operator's upgrade_utils.go
// and represents a model-server container size entry from OdhDashboardConfig.
type containerSize struct {
	Name      string
	Resources struct {
		Requests struct{ Cpu, Memory string }
		Limits   struct{ Cpu, Memory string }
	}
}

// upgradeRunnable executes one-time upgrade tasks after the controller-manager wins leader election.
// It implements manager.Runnable so that the manager calls Start exactly once on startup.
type upgradeRunnable struct {
	client        client.Client
	applicationNS string
}

// Start runs the upgrade tasks once. Errors are logged but never returned so that
// upgrade failures never block manager startup.
func (u *upgradeRunnable) Start(ctx context.Context) error {
	if err := runUpgradeTasks(ctx, u.client, u.applicationNS); err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "upgrade tasks encountered errors (non-fatal)")
	}
	return nil
}

// NeedLeaderElection reports that upgrade tasks must run under leader election,
// matching the pattern used by opendatahub-operator's LeaderElectionRunnableFunc.
func (u *upgradeRunnable) NeedLeaderElection() bool { return true }

// legacySelectorWorkload defines a Deployment or DaemonSet that may carry legacy selector
// labels injected by the in-tree ODH operator via kustomize.WithLabel.
type legacySelectorWorkload struct {
	name           string
	legacyLabelKey string
}

// legacySelectorDeployments lists Deployments whose spec.selector.matchLabels may contain
// labels injected by the in-tree ODH operator (app.opendatahub.io/* and app.kubernetes.io/part-of).
// Since spec.selector is immutable, these must be deleted so the reconciler can recreate them.
var legacySelectorDeployments = []legacySelectorWorkload{
	{name: "kserve-controller-manager", legacyLabelKey: "app.opendatahub.io/kserve"},
	{name: "llmisvc-controller-manager", legacyLabelKey: "app.opendatahub.io/kserve"},
	{name: "kserve-localmodel-controller-manager", legacyLabelKey: "app.opendatahub.io/kserve"},
	{name: "odh-model-controller", legacyLabelKey: "app.opendatahub.io/odh-model-controller"},
	{name: "model-serving-api", legacyLabelKey: "app.opendatahub.io/odh-model-controller"},
}

// legacySelectorDaemonSets lists DaemonSets with the same legacy selector problem.
var legacySelectorDaemonSets = []legacySelectorWorkload{
	{name: "kserve-localmodelnode-agent", legacyLabelKey: "app.opendatahub.io/kserve"},
}

// deleteLegacySelectorWorkloads checks each Deployment and DaemonSet for legacy selector
// labels and deletes those that carry them. The reconciler will recreate them with the correct selectors.
func deleteLegacySelectorWorkloads(ctx context.Context, cli client.Client, namespace string) error {
	log := ctrl.LoggerFrom(ctx)
	var errs []error

	for _, ld := range legacySelectorDeployments {
		dep := &appsv1.Deployment{}
		if err := cli.Get(ctx, types.NamespacedName{Name: ld.name, Namespace: namespace}, dep); err != nil {
			if k8serr.IsNotFound(err) {
				continue
			}
			errs = append(errs, fmt.Errorf("failed to get deployment %s: %w", ld.name, err))
			continue
		}

		if dep.Spec.Selector == nil || dep.Spec.Selector.MatchLabels == nil {
			continue
		}

		if _, hasLegacy := dep.Spec.Selector.MatchLabels[ld.legacyLabelKey]; !hasLegacy {
			continue
		}

		log.Info("Deleting deployment with legacy selector labels",
			"deployment", ld.name, "legacyLabel", ld.legacyLabelKey)

		if err := cli.Delete(ctx, dep); err != nil && !k8serr.IsNotFound(err) {
			errs = append(errs, fmt.Errorf("failed to delete deployment %s: %w", ld.name, err))
		}
	}

	for _, ld := range legacySelectorDaemonSets {
		ds := &appsv1.DaemonSet{}
		if err := cli.Get(ctx, types.NamespacedName{Name: ld.name, Namespace: namespace}, ds); err != nil {
			if k8serr.IsNotFound(err) {
				continue
			}
			errs = append(errs, fmt.Errorf("failed to get daemonset %s: %w", ld.name, err))
			continue
		}

		if ds.Spec.Selector == nil || ds.Spec.Selector.MatchLabels == nil {
			continue
		}

		if _, hasLegacy := ds.Spec.Selector.MatchLabels[ld.legacyLabelKey]; !hasLegacy {
			continue
		}

		log.Info("Deleting daemonset with legacy selector labels",
			"daemonset", ld.name, "legacyLabel", ld.legacyLabelKey)

		if err := cli.Delete(ctx, ds); err != nil && !k8serr.IsNotFound(err) {
			errs = append(errs, fmt.Errorf("failed to delete daemonset %s: %w", ld.name, err))
		}
	}

	return errors.Join(errs...)
}

// knownConditionTypes lists all condition types managed by kserve-module.
// Conditions not in this set (e.g. from the in-tree ODH operator) are removed on upgrade.
// TODO(3.6): remove removeStaleConditions and knownConditionTypes — only needed for 3.5 migration from in-tree operator.
var knownConditionTypes = map[string]bool{
	string(common.ConditionTypeReady):                true,
	string(common.ConditionTypeProvisioningSucceeded): true,
	string(common.ConditionTypeDegraded):              true,
	ConditionKServeReady:                              true,
	ConditionModelControllerReady:                     true,
	ConditionWVAReady:                                 true,
	ConditionModelCacheReady:                          true,
	ConditionDependenciesAvailable:                    true,
	conditionLLMISVCDeps:                              true,
	conditionLLMISVCWideEPDeps:                        true,
	conditionLLMDWVADeps:                              true,
}

func removeStaleConditions(ctx context.Context, cli client.Client, crName string) error {
	kserve := &platformv1alpha1.Kserve{}
	if err := cli.Get(ctx, types.NamespacedName{Name: crName}, kserve); err != nil {
		if k8serr.IsNotFound(err) {
			return nil
		}
		return err
	}

	existing := kserve.GetConditions()
	if len(existing) == 0 {
		return nil
	}

	clean := make([]common.Condition, 0, len(existing))
	for _, c := range existing {
		if knownConditionTypes[c.Type] {
			clean = append(clean, c)
		}
	}

	if len(clean) == len(existing) {
		return nil
	}

	kserve.SetConditions(clean)
	return cli.Status().Update(ctx, kserve)
}

// runUpgradeTasks performs one-time migration tasks on startup:
//  1. Deletes Deployments and DaemonSets carrying legacy selector labels from the in-tree ODH operator.
//  2. Removes stale conditions left by the in-tree ODH operator.
//  3. Migrates HardwareProfile annotations on InferenceServices.
func runUpgradeTasks(ctx context.Context, cli client.Client, applicationNS string) error {
	log := ctrl.LoggerFrom(ctx)

	if err := deleteLegacySelectorWorkloads(ctx, cli, applicationNS); err != nil {
		log.Error(err, "legacy selector deployment cleanup encountered errors")
	}

	if err := removeStaleConditions(ctx, cli, platformv1alpha1.KserveInstanceName); err != nil {
		log.Error(err, "stale condition cleanup encountered errors")
	}

	if err := migrateHardwareProfileAnnotations(ctx, cli, applicationNS); err != nil {
		return err
	}

	return nil
}

// migrateHardwareProfileAnnotations checks whether both the HardwareProfile and AcceleratorProfile
// CRDs are present, fetches OdhDashboardConfig, and delegates to attachHardwareProfileToInferenceServices.
//
// Returns nil (with an informational log) when either CRD is absent or when OdhDashboardConfig
// is not found, since those conditions are expected on clusters that haven't deployed the relevant features.
func migrateHardwareProfileAnnotations(ctx context.Context, cli client.Client, applicationNS string) error {
	log := ctrl.LoggerFrom(ctx)

	hwpGK := schema.GroupKind{Group: hardwareProfileGVK.Group, Kind: hardwareProfileGVK.Kind}
	if err := cluster.CustomResourceDefinitionExists(ctx, cli, hwpGK); err != nil {
		log.Info("Skipping HWP migration: HardwareProfile CRD not found", "group", hwpGK.Group)
		return nil
	}

	apGK := schema.GroupKind{Group: acceleratorProfileGVK.Group, Kind: acceleratorProfileGVK.Kind}
	if err := cluster.CustomResourceDefinitionExists(ctx, cli, apGK); err != nil {
		log.Info("Skipping HWP migration: AcceleratorProfile CRD not found", "group", apGK.Group)
		return nil
	}

	odhConfig, found, err := getOdhDashboardConfig(ctx, cli, applicationNS)
	if err != nil {
		return fmt.Errorf("error fetching OdhDashboardConfig: %w", err)
	}
	if !found {
		log.Info("Skipping HWP migration: OdhDashboardConfig not found", "namespace", applicationNS)
		return nil
	}

	return attachHardwareProfileToInferenceServices(ctx, cli, applicationNS, odhConfig)
}

// getOdhDashboardConfig retrieves the OdhDashboardConfig singleton from the given namespace.
//
// Returns (obj, true, nil) when found, (nil, false, nil) when not present, and
// (nil, false, err) on API errors.
func getOdhDashboardConfig(ctx context.Context, cli client.Client, applicationNS string) (*unstructured.Unstructured, bool, error) {
	odhConfig := &unstructured.Unstructured{}
	odhConfig.SetGroupVersionKind(odhDashboardConfigGVK)

	err := cli.Get(ctx, client.ObjectKey{Name: odhDashboardConfigName, Namespace: applicationNS}, odhConfig)
	if err == nil {
		return odhConfig, true, nil
	}
	if k8serr.IsNotFound(err) || meta.IsNoMatchError(err) {
		return nil, false, nil
	}
	return nil, false, fmt.Errorf("unable to get OdhDashboardConfig: %w", err)
}

// attachHardwareProfileToInferenceServices iterates over all cluster-wide InferenceServices and
// sets the opendatahub.io/hardware-profile-name annotation where it is absent, deriving the
// HardwareProfile name from the ServingRuntime's AcceleratorProfile annotation, a matching
// container size, or the "custom-serving" fallback.
//
// Serverless ISVCs and ISVCs in Kueue-managed namespaces without the queue label are skipped.
// Per-ISVC errors are accumulated and returned together; processing continues for remaining ISVCs.
func attachHardwareProfileToInferenceServices(ctx context.Context, cli client.Client, applicationNS string, odhConfig *unstructured.Unstructured) error {
	log := ctrl.LoggerFrom(ctx)

	isvcs, err := getInferenceServices(ctx, cli)
	if err != nil {
		return fmt.Errorf("failed to list InferenceServices: %w", err)
	}
	if len(isvcs) == 0 {
		log.Info("No InferenceServices found, skipping HWP annotation migration")
		return nil
	}

	containerSizes, err := getContainerSizes(ctx, odhConfig, "modelServerSizes")
	if err != nil {
		return fmt.Errorf("failed to get modelServerSizes from OdhDashboardConfig: %w", err)
	}

	var errs []error
	for _, isvc := range isvcs {
		annotations := isvc.GetAnnotations()
		if annotations == nil {
			annotations = map[string]string{}
		}

		if annotations[hwpNameAnnotation] != "" {
			continue
		}

		if isISVCServerless(isvc) {
			log.Info("Skipping Serverless InferenceService during HWP migration",
				"isvc", isvc.GetName(), "namespace", isvc.GetNamespace())
			msg := fmt.Sprintf("Skipping HardwareProfile migration for Serverless InferenceService %s", isvc.GetName())
			if eventErr := recordWarningEvent(ctx, cli, isvc, "ServerlessMigrationSkipped", msg); eventErr != nil {
				log.Error(eventErr, "Failed to record warning event", "isvc", isvc.GetName())
			}
			continue
		}

		kueueManaged, nsErr := isNamespaceManagedByKueue(ctx, cli, isvc.GetNamespace())
		if nsErr != nil {
			log.Error(nsErr, "Failed to check Kueue namespace management; proceeding with HardwareProfile annotation",
				"namespace", isvc.GetNamespace())
		} else if kueueManaged && isvc.GetLabels()[kueueQueueNameLabel] == "" {
			msg := fmt.Sprintf("Skipping HardwareProfile migration for InferenceService %s: namespace is Kueue-managed but missing required label %q",
				isvc.GetName(), kueueQueueNameLabel)
			log.Info("Skipping HardwareProfile migration: namespace is Kueue-managed but missing required label",
				"isvc", isvc.GetName(), "namespace", isvc.GetNamespace(), "label", kueueQueueNameLabel)
			if eventErr := recordWarningEvent(ctx, cli, isvc, "HardwareProfileMigrationSkipped", msg); eventErr != nil {
				log.Error(eventErr, "Failed to record warning event", "isvc", isvc.GetName())
			}
			continue
		}

		// Prefer AcceleratorProfile annotation from the associated ServingRuntime.
		servingRuntime, srErr := getSRFromISVC(ctx, cli, isvc)
		if srErr != nil {
			log.V(1).Info("Could not get ServingRuntime for InferenceService; falling back to container-size matching",
				"isvc", isvc.GetName(), "namespace", isvc.GetNamespace(), "error", srErr)
		} else {
			runtimeAnnotations := servingRuntime.GetAnnotations()
			if runtimeAnnotations == nil {
				runtimeAnnotations = map[string]string{}
			}
			if apName := runtimeAnnotations[acceleratorNameAnnotation]; apName != "" {
				hwpName := fmt.Sprintf("%s-serving", strings.ReplaceAll(strings.ToLower(apName), " ", "-"))
				if errsMsg := validation.IsDNS1123Label(hwpName); len(errsMsg) > 0 {
					log.Info("Skipping HardwareProfile migration: derived HWP name is not a valid DNS label",
						"isvc", isvc.GetName(), "hwpName", hwpName, "reasons", errsMsg)
					continue
				}
				hwpNS := runtimeAnnotations[acceleratorProfileNSAnnotation]
				if annotErr := setHWPAnnotation(ctx, cli, isvc, hwpName, hwpNS, applicationNS); annotErr != nil {
					if !handleISVCSetHWPAnnotationError(ctx, cli, isvc, annotErr) {
						errs = append(errs, fmt.Errorf("failed to set HWP annotation on InferenceService %s/%s: %w", isvc.GetNamespace(), isvc.GetName(), annotErr))
					}
				} else {
					log.Info("Migrated ServingRuntime AcceleratorProfile annotation to HardwareProfile annotation for InferenceService",
						"isvc", isvc.GetName(), "runtime", servingRuntime.GetName(), "hwp", hwpName)
				}
				continue
			}
		}

		// Fall back to container-size matching, then to custom-serving.
		hwpName := customServingHWP
		var matchedSize string
		resources, resErr := getInferenceServiceResources(isvc)
		if resErr == nil {
			matchedSize = findContainerSizeByResources(containerSizes, resources)
			if matchedSize != "" {
				hwpName = fmt.Sprintf("%s%s-serving", containerSizeHWPPrefix, strings.ReplaceAll(strings.ToLower(matchedSize), " ", "-"))
			}
		}

		if annotErr := setHWPAnnotation(ctx, cli, isvc, hwpName, "", applicationNS); annotErr != nil {
			if !handleISVCSetHWPAnnotationError(ctx, cli, isvc, annotErr) {
				errs = append(errs, fmt.Errorf("failed to set HWP annotation on InferenceService %s/%s: %w", isvc.GetNamespace(), isvc.GetName(), annotErr))
			}
		} else if matchedSize != "" {
			log.Info("Set HardwareProfile annotation for InferenceService based on container size match",
				"isvc", isvc.GetName(), "size", matchedSize, "hardwareProfile", hwpName)
		} else {
			log.Info("Set HardwareProfile annotation for InferenceService with custom-serving HardwareProfile",
				"isvc", isvc.GetName(), "hardwareProfile", hwpName)
		}
	}

	return errors.Join(errs...)
}

// handleISVCSetHWPAnnotationError inspects annotation update errors for known webhook rejections
// (Serverless mode incompatibility and Kueue label validation). When recognised, it logs the
// situation, emits a warning Event, and returns true so the caller can skip to the next ISVC
// rather than treating it as a hard failure. Returns false for any other error.
//
// This is a precautionary check: Serverless and Kueue ISVCs are already filtered out before
// calling setHWPAnnotation, but the webhook may still reject the update in edge cases.
func handleISVCSetHWPAnnotationError(ctx context.Context, cli client.Client, isvc *unstructured.Unstructured, err error) bool {
	log := ctrl.LoggerFrom(ctx)
	errStr := err.Error()

	if strings.Contains(errStr, "deploymentMode cannot be changed") || strings.Contains(errStr, "Serverless") {
		log.Info("Skipping HardwareProfile migration: Serverless webhook rejection",
			"isvc", isvc.GetName(), "namespace", isvc.GetNamespace(), "error", errStr)
		msg := fmt.Sprintf("Skipping HardwareProfile migration due to Serverless mode incompatibility: %s", errStr)
		if eventErr := recordWarningEvent(ctx, cli, isvc, "ServerlessMigrationSkipped", msg); eventErr != nil {
			log.Error(eventErr, "Failed to record warning event", "isvc", isvc.GetName())
		}
		return true
	}

	if strings.Contains(errStr, "Kueue label validation failed") ||
		(strings.Contains(errStr, "missing required label") && strings.Contains(errStr, "kueue")) {
		log.Info("Skipping HardwareProfile migration: Kueue webhook rejection (RHOAIENG-50667)",
			"isvc", isvc.GetName(), "namespace", isvc.GetNamespace(), "error", errStr)
		msg := fmt.Sprintf("Skipping HardwareProfile migration for InferenceService %s: namespace is Kueue-managed but missing required label %q on the InferenceService",
			isvc.GetName(), kueueQueueNameLabel)
		if eventErr := recordWarningEvent(ctx, cli, isvc, "HardwareProfileMigrationSkipped", msg); eventErr != nil {
			log.Error(eventErr, "Failed to record warning event", "isvc", isvc.GetName())
		}
		return true
	}

	return false
}

// getInferenceServices lists all InferenceService resources cluster-wide.
//
// Returns a nil slice (not an error) when the InferenceService GVK is not registered
// in the cluster's API server (meta.IsNoMatchError).
func getInferenceServices(ctx context.Context, cli client.Client) ([]*unstructured.Unstructured, error) {
	isvcList := &unstructured.UnstructuredList{}
	isvcList.SetGroupVersionKind(inferenceServiceGVK)

	if err := cli.List(ctx, isvcList); err != nil {
		if meta.IsNoMatchError(err) {
			return nil, nil
		}
		return nil, err
	}

	isvcs := make([]*unstructured.Unstructured, len(isvcList.Items))
	for i := range isvcList.Items {
		isvcs[i] = &isvcList.Items[i]
	}
	return isvcs, nil
}

// getSRFromISVC looks up the ServingRuntime referenced by an InferenceService's
// spec.predictor.model.runtime field within the same namespace.
func getSRFromISVC(ctx context.Context, cli client.Client, isvc *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	runtimeName, found, err := unstructured.NestedString(isvc.Object, "spec", "predictor", "model", "runtime")
	if err != nil || !found {
		return nil, errors.New("runtime not found in InferenceService spec")
	}

	sr := &unstructured.Unstructured{}
	sr.SetGroupVersionKind(servingRuntimeGVK)
	err = cli.Get(ctx, client.ObjectKey{Name: runtimeName, Namespace: isvc.GetNamespace()}, sr)
	return sr, err
}

// getInferenceServiceResources extracts spec.predictor.model.resources from an InferenceService.
func getInferenceServiceResources(isvc *unstructured.Unstructured) (map[string]any, error) {
	resources, found, err := unstructured.NestedMap(isvc.Object, "spec", "predictor", "model", "resources")
	if err != nil || !found {
		return nil, errors.New("resources not found in InferenceService spec")
	}
	return resources, nil
}

// findContainerSizeByResources matches the given resource map against the known container sizes
// and returns the name of the first match, or an empty string when no size matches.
//
// Comparison uses resource.Quantity semantics so that equivalent values like "1" and "1000m"
// are treated as equal, matching Kubernetes resource handling.
func findContainerSizeByResources(sizes []containerSize, resources map[string]any) string {
	if resources == nil {
		return ""
	}

	requests, reqOk := resources["requests"].(map[string]any)
	limits, limOk := resources["limits"].(map[string]any)
	if !reqOk || !limOk {
		return ""
	}

	parseQ := func(m map[string]any, key string) (resource.Quantity, bool) {
		s, ok := m[key].(string)
		if !ok || s == "" {
			return resource.Quantity{}, false
		}
		q, err := resource.ParseQuantity(s)
		return q, err == nil
	}

	isvcReqCPU, ok1 := parseQ(requests, "cpu")
	isvcReqMem, ok2 := parseQ(requests, "memory")
	isvcLimCPU, ok3 := parseQ(limits, "cpu")
	isvcLimMem, ok4 := parseQ(limits, "memory")
	if !ok1 || !ok2 || !ok3 || !ok4 {
		// ISVC resource values are admission-validated by Kubernetes using the same parser, so
		// parse failures are impossible in practice. custom-serving is the safe fallback either way.
		return ""
	}

	for _, size := range sizes {
		szReqCPU, e1 := resource.ParseQuantity(size.Resources.Requests.Cpu)
		szReqMem, e2 := resource.ParseQuantity(size.Resources.Requests.Memory)
		szLimCPU, e3 := resource.ParseQuantity(size.Resources.Limits.Cpu)
		szLimMem, e4 := resource.ParseQuantity(size.Resources.Limits.Memory)
		if e1 != nil || e2 != nil || e3 != nil || e4 != nil {
			// Sizes are pre-validated by getContainerSizes; ParseQuantity will not fail in practice.
			continue
		}

		if isvcReqCPU.Cmp(szReqCPU) == 0 &&
			isvcReqMem.Cmp(szReqMem) == 0 &&
			isvcLimCPU.Cmp(szLimCPU) == 0 &&
			isvcLimMem.Cmp(szLimMem) == 0 {
			return size.Name
		}
	}
	return ""
}

// isISVCServerless reports whether an InferenceService is in Serverless deployment mode,
// checking both the serving.kserve.io/deploymentMode annotation and the status.deploymentMode field.
func isISVCServerless(isvc *unstructured.Unstructured) bool {
	annotations := isvc.GetAnnotations()
	if annotations != nil && annotations[kserveDeploymentModeAnnotation] == kserveDeploymentModeServerless {
		return true
	}
	status, found, _ := unstructured.NestedString(isvc.Object, "status", "deploymentMode")
	return found && status == kserveDeploymentModeServerless
}

// isNamespaceManagedByKueue returns true when the namespace carries either the
// kueue.openshift.io/managed or the legacy kueue-managed label set to "true".
func isNamespaceManagedByKueue(ctx context.Context, cli client.Client, namespaceName string) (bool, error) {
	if namespaceName == "" {
		return false, nil
	}
	ns := &corev1.Namespace{}
	if err := cli.Get(ctx, client.ObjectKey{Name: namespaceName}, ns); err != nil {
		return false, err
	}
	return ns.Labels[kueueManagedLabel] == "true" || ns.Labels[kueueLegacyManagedLabel] == "true", nil
}

// getContainerSizes extracts the container size entries from spec.<sizeType> in OdhDashboardConfig
// and maps each entry to a containerSize struct.
//
// Entries with missing or unparseable resource quantity strings are skipped with a warning log,
// since a silently-skipped size would cause ISVCs to fall through to the custom-serving fallback
// with no indication of why. Returns an empty slice (not an error) when the sizeType key is absent.
func getContainerSizes(ctx context.Context, odhConfig *unstructured.Unstructured, sizeType string) ([]containerSize, error) {
	log := ctrl.LoggerFrom(ctx)

	sizes, found, err := unstructured.NestedSlice(odhConfig.Object, "spec", sizeType)
	if err != nil {
		return nil, fmt.Errorf("failed to read spec.%s from OdhDashboardConfig: %w", sizeType, err)
	}
	if !found {
		return []containerSize{}, nil
	}

	result := make([]containerSize, 0, len(sizes))
	for _, item := range sizes {
		sizeMap, ok := item.(map[string]any)
		if !ok {
			continue
		}

		cs := containerSize{}
		if name, ok := sizeMap["name"].(string); ok {
			cs.Name = name
		}

		if resources, ok := sizeMap["resources"].(map[string]any); ok {
			if requests, ok := resources["requests"].(map[string]any); ok {
				if cpu, ok := requests["cpu"].(string); ok {
					cs.Resources.Requests.Cpu = cpu
				}
				if memory, ok := requests["memory"].(string); ok {
					cs.Resources.Requests.Memory = memory
				}
			}
			if limits, ok := resources["limits"].(map[string]any); ok {
				if cpu, ok := limits["cpu"].(string); ok {
					cs.Resources.Limits.Cpu = cpu
				}
				if memory, ok := limits["memory"].(string); ok {
					cs.Resources.Limits.Memory = memory
				}
			}
		}

		if err := validateContainerSizeQuantities(cs); err != nil {
			log.Info("Skipping OdhDashboardConfig container size entry: invalid resource quantity",
				"size", cs.Name, "sizeType", sizeType, "error", err)
			continue
		}

		result = append(result, cs)
	}
	return result, nil
}

// validateContainerSizeQuantities checks that all four resource quantity strings in a containerSize
// are parseable as Kubernetes resource.Quantity values.
func validateContainerSizeQuantities(cs containerSize) error {
	fields := []struct {
		label string
		value string
	}{
		{"requests.cpu", cs.Resources.Requests.Cpu},
		{"requests.memory", cs.Resources.Requests.Memory},
		{"limits.cpu", cs.Resources.Limits.Cpu},
		{"limits.memory", cs.Resources.Limits.Memory},
	}
	for _, f := range fields {
		if _, err := resource.ParseQuantity(f.value); err != nil {
			return fmt.Errorf("invalid %s %q: %w", f.label, f.value, err)
		}
	}
	return nil
}

// setHWPAnnotation locates the HardwareProfile named hwpName by searching namespaces in
// priority order (apNamespace → isvc.Namespace → applicationNS), sets
// opendatahub.io/hardware-profile-name and opendatahub.io/hardware-profile-namespace
// annotations on the ISVC, then calls cli.Update.
//
// When the HardwareProfile is not found in any candidate namespace, only the name annotation
// is set and a warning Kubernetes Event is emitted.
func setHWPAnnotation(ctx context.Context, cli client.Client, isvc *unstructured.Unstructured, hwpName, apNamespace, applicationNS string) error {
	log := ctrl.LoggerFrom(ctx)

	// Capture the base before any mutation so Patch sends only the annotation diff,
	// avoiding lost-update conflicts from concurrent writes to other ISVC fields.
	base := isvc.DeepCopy()

	annotations := isvc.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}
	annotations[hwpNameAnnotation] = hwpName

	isvcNS := isvc.GetNamespace()
	var namespacesToCheck []string
	if apNamespace != "" {
		namespacesToCheck = append(namespacesToCheck, apNamespace)
	}
	if isvcNS != "" && isvcNS != apNamespace {
		namespacesToCheck = append(namespacesToCheck, isvcNS)
	}
	if applicationNS != apNamespace && applicationNS != isvcNS {
		namespacesToCheck = append(namespacesToCheck, applicationNS)
	}

	hwpFound := false
	for _, ns := range namespacesToCheck {
		_, err := getHardwareProfile(ctx, cli, hwpName, ns)
		if err == nil {
			annotations[hwpNamespaceAnnotation] = ns
			hwpFound = true
			log.Info("Found HardwareProfile for migration",
				"hwpName", hwpName, "hwpNamespace", ns, "isvc", isvc.GetName(), "isvcNamespace", isvc.GetNamespace())
			break
		} else if !k8serr.IsNotFound(err) {
			return fmt.Errorf("failed to check HardwareProfile %q in namespace %s: %w", hwpName, ns, err)
		}
	}

	if !hwpFound {
		log.Info("HardwareProfile not found in any candidate namespace; setting name annotation only",
			"hwpName", hwpName, "isvc", isvc.GetName(), "searched", namespacesToCheck)
		msg := fmt.Sprintf("Skipping HardwareProfile namespace annotation: %q not found in namespaces %v", hwpName, namespacesToCheck)
		if eventErr := recordWarningEvent(ctx, cli, isvc, "HardwareProfileMigrationSkipped", msg); eventErr != nil {
			log.Error(eventErr, "Failed to record warning event", "isvc", isvc.GetName())
		}
	}

	isvc.SetAnnotations(annotations)
	return cli.Patch(ctx, isvc, client.MergeFrom(base))
}

// getHardwareProfile fetches the HardwareProfile with the given name from the given namespace
// using an unstructured client, avoiding a typed dependency on the HardwareProfile API package.
func getHardwareProfile(ctx context.Context, cli client.Client, name, namespace string) (*unstructured.Unstructured, error) {
	hwp := &unstructured.Unstructured{}
	hwp.SetGroupVersionKind(hardwareProfileGVK)
	err := cli.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, hwp)
	return hwp, err
}

// recordWarningEvent creates a Kubernetes Warning Event for the given unstructured object.
func recordWarningEvent(ctx context.Context, cli client.Client, obj *unstructured.Unstructured, reason, message string) error {
	now := metav1.NewTime(time.Now())
	event := &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: obj.GetName() + "-",
			Namespace:    obj.GetNamespace(),
		},
		InvolvedObject: corev1.ObjectReference{
			APIVersion: obj.GetAPIVersion(),
			Kind:       obj.GetKind(),
			Name:       obj.GetName(),
			Namespace:  obj.GetNamespace(),
			UID:        obj.GetUID(),
		},
		Reason:              reason,
		Message:             message,
		Type:                corev1.EventTypeWarning,
		FirstTimestamp:      now,
		LastTimestamp:       now,
		Count:               1,
		Source:              corev1.EventSource{Component: "kserve-module"},
		ReportingController: "kserve-module",
		ReportingInstance:   "kserve-module",
	}
	return cli.Create(ctx, event)
}
