package kservemodule

import (
	"context"
	"maps"
	"slices"
	"strings"

	k8serr "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/opendatahub-io/odh-platform-utilities/api/common"
	"github.com/opendatahub-io/odh-platform-utilities/pkg/controller/conditions"

	platformv1alpha1 "github.com/opendatahub-io/kserve-module/pkg/apis/v1alpha1"
)

const (
	ConditionKServeReady           = "KServeReady"
	ConditionModelControllerReady  = "ModelControllerReady"
	ConditionWVAReady              = "WVAReady"
	ConditionModelCacheReady       = "ModelCacheReady"
	ConditionDependenciesAvailable = "DependenciesAvailable"
)

func newConditionManager(kserve *platformv1alpha1.Kserve) *conditions.Manager {
	return conditions.NewManager(kserve,
		string(common.ConditionTypeReady),
		string(common.ConditionTypeProvisioningSucceeded),
		ConditionKServeReady,
		ConditionModelControllerReady,
		ConditionWVAReady,
		ConditionModelCacheReady,
		ConditionDependenciesAvailable,
	)
}

func applyDependencyConditions(condMgr *conditions.Manager, result dependencyResult) {
	slices.Sort(result.allReasons)

	// DependenciesAvailable: only deps with availabilitySeverity != availSeverityNone contribute.
	// Severity=Info keeps IsHappy()=True (findUnhappyDependent skips non-Error severities).
	if len(result.availReasons) > 0 {
		worstSeverity := common.ConditionSeverityInfo
		msgs := make([]string, 0, len(result.availReasons))
		for _, ar := range result.availReasons {
			msgs = append(msgs, ar.message)
			if ar.severity == common.ConditionSeverityError {
				worstSeverity = common.ConditionSeverityError
			}
		}
		slices.Sort(msgs)
		condMgr.MarkFalse(ConditionDependenciesAvailable,
			conditions.WithSeverity(worstSeverity),
			conditions.WithReason("DependencyDegraded"),
			conditions.WithMessage("%s", strings.Join(msgs, "; ")))
	} else {
		condMgr.MarkTrue(ConditionDependenciesAvailable,
			conditions.WithReason("AllDependenciesMet"))
	}

	// Degraded: any failure contributes, severity escalates if Error-level deps failed
	if hasCriticalFailure(result) {
		var criticalMsgs []string
		for _, ar := range result.availReasons {
			if ar.severity == common.ConditionSeverityError {
				criticalMsgs = append(criticalMsgs, ar.message)
			}
		}
		slices.Sort(criticalMsgs)
		condMgr.MarkTrue(string(common.ConditionTypeDegraded),
			conditions.WithSeverity(common.ConditionSeverityError),
			conditions.WithReason("MissingCriticalDependency"),
			conditions.WithMessage("%s", strings.Join(criticalMsgs, "; ")))
	} else if len(result.allReasons) > 0 {
		condMgr.MarkFalse(string(common.ConditionTypeDegraded),
			conditions.WithSeverity(common.ConditionSeverityInfo),
			conditions.WithReason("MissingOptionalDependency"),
			conditions.WithMessage("%s", strings.Join(result.allReasons, "; ")))
	} else {
		condMgr.MarkFalse(string(common.ConditionTypeDegraded),
			conditions.WithSeverity(common.ConditionSeverityInfo),
			conditions.WithReason("NoDegradation"))
	}

	for group, reasons := range result.groupReasons {
		slices.Sort(reasons)
		if len(reasons) > 0 {
			condMgr.MarkFalse(group,
				conditions.WithSeverity(common.ConditionSeverityInfo),
				conditions.WithReason("PreConditionFailed"),
				conditions.WithMessage("%s", strings.Join(reasons, "; ")))
		} else {
			condMgr.MarkTrue(group,
				conditions.WithSeverity(common.ConditionSeverityInfo))
		}
	}
}

func applyProvisioningCondition(condMgr *conditions.Manager, componentErrors map[string]error) {
	if len(componentErrors) == 0 {
		condMgr.MarkTrue(string(common.ConditionTypeProvisioningSucceeded),
			conditions.WithReason("AllResourcesApplied"))
		return
	}

	msgs := make([]string, 0, len(componentErrors))
	for _, name := range slices.Sorted(maps.Keys(componentErrors)) {
		msgs = append(msgs, name+": "+componentErrors[name].Error())
	}
	condMgr.MarkFalse(string(common.ConditionTypeProvisioningSucceeded),
		conditions.WithReason("DeployFailed"),
		conditions.WithMessage("%s", strings.Join(msgs, "; ")))
}

func (r *KserveModuleReconciler) updateComponentReadiness(ctx context.Context, kserve *platformv1alpha1.Kserve, condMgr *conditions.Manager) {
	ns := r.getApplicationsNamespace()
	isXKS := r.isKubernetes(ctx)

	if err := checkKServeReadiness(ctx, r.Client, ns, isXKS); err != nil {
		condMgr.MarkFalse(ConditionKServeReady,
			conditions.WithReason("DeploymentNotReady"),
			conditions.WithMessage("%s", err.Error()))
	} else {
		condMgr.MarkTrue(ConditionKServeReady,
			conditions.WithReason("AllDeploymentsAvailable"))
	}

	if err := checkModelControllerReadiness(ctx, r.Client, ns, isXKS); err != nil {
		condMgr.MarkFalse(ConditionModelControllerReady,
			conditions.WithReason("DeploymentNotReady"),
			conditions.WithMessage("%s", err.Error()))
	} else {
		condMgr.MarkTrue(ConditionModelControllerReady,
			conditions.WithReason("AllDeploymentsAvailable"))
	}

	if isWVAEnabled(kserve) {
		if err := checkWVAReadiness(ctx, r.Client, ns); err != nil {
			condMgr.MarkFalse(ConditionWVAReady,
				conditions.WithReason("DeploymentNotReady"),
				conditions.WithMessage("%s", err.Error()))
		} else {
			condMgr.MarkTrue(ConditionWVAReady,
				conditions.WithReason("AllDeploymentsAvailable"))
		}
	} else {
		condMgr.ClearCondition(ConditionWVAReady)
	}

	if !isModelCacheEnabled(kserve) {
		condMgr.ClearCondition(ConditionModelCacheReady)
	} else if err := r.checkModelCacheReadiness(ctx); err != nil {
		condMgr.MarkFalse(ConditionModelCacheReady,
			conditions.WithReason(modelCacheReadinessReason(err)),
			conditions.WithMessage("%s", err.Error()))
	} else {
		condMgr.MarkTrue(ConditionModelCacheReady,
			conditions.WithReason("ResourcesReady"))
	}
}

func (r *KserveModuleReconciler) updateStatus(ctx context.Context, kserve *platformv1alpha1.Kserve, condMgr *conditions.Manager) error {
	r.setReleaseStatus(ctx, kserve)
	condMgr.Sort()

	if condMgr.IsHappy() {
		kserve.Status.Phase = common.PhaseReady
		for i := range kserve.Status.Conditions {
			if kserve.Status.Conditions[i].Type == string(common.ConditionTypeReady) {
				kserve.Status.Conditions[i].Reason = ""
				break
			}
		}
	} else {
		kserve.Status.Phase = common.PhaseNotReady
	}

	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest := &platformv1alpha1.Kserve{}
		if err := r.Get(ctx, types.NamespacedName{Name: kserve.Name}, latest); err != nil {
			if k8serr.IsNotFound(err) {
				ctrl.LoggerFrom(ctx).Info("CR deleted, skipping status update")
				return nil
			}
			return err
		}
		latest.Status = kserve.Status
		latest.Status.ObservedGeneration = kserve.Generation
		return r.Status().Update(ctx, latest)
	})
}

func (r *KserveModuleReconciler) setReleaseStatus(ctx context.Context, kserve *platformv1alpha1.Kserve) {
	releases, err := loadComponentReleases(r.ManifestsTemplatePath,
		[]string{KserveComponentName, OdhModelControllerComponentName})
	if err != nil {
		ctrl.Log.Error(err, "failed to load component releases")
		return
	}

	if v := r.getPlatformVersion(ctx); v != "" {
		releases = append(releases, common.ComponentRelease{
			Name:    "platform",
			Version: v,
		})
	}

	kserve.SetReleaseStatus(common.ComponentReleaseStatus{Releases: releases})
}
