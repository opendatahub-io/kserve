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
	ConditionDependenciesAvailable = "DependenciesAvailable"
)

func newConditionManager(kserve *platformv1alpha1.Kserve) *conditions.Manager {
	return conditions.NewManager(kserve,
		string(common.ConditionTypeReady),
		string(common.ConditionTypeProvisioningSucceeded),
		ConditionKServeReady,
		ConditionModelControllerReady,
		ConditionWVAReady,
		ConditionDependenciesAvailable,
	)
}

func applyDependencyConditions(condMgr *conditions.Manager, result dependencyResult) {
	slices.Sort(result.criticalErrors)
	slices.Sort(result.degradedReasons)

	if len(result.criticalErrors) > 0 {
		condMgr.MarkFalse(ConditionDependenciesAvailable,
			conditions.WithReason("DependencyDegraded"),
			conditions.WithMessage("%s", strings.Join(result.criticalErrors, "; ")))
		condMgr.MarkTrue(string(common.ConditionTypeDegraded),
			conditions.WithSeverity(common.ConditionSeverityError),
			conditions.WithReason("MissingCriticalDependency"),
			conditions.WithMessage("%s", strings.Join(result.criticalErrors, "; ")))
	} else if len(result.degradedReasons) > 0 {
		condMgr.MarkTrue(ConditionDependenciesAvailable,
			conditions.WithReason("AllCriticalDependenciesMet"))
		condMgr.MarkTrue(string(common.ConditionTypeDegraded),
			conditions.WithSeverity(common.ConditionSeverityInfo),
			conditions.WithReason("MissingOptionalDependency"),
			conditions.WithMessage("%s", strings.Join(result.degradedReasons, "; ")))
	} else {
		condMgr.MarkTrue(ConditionDependenciesAvailable,
			conditions.WithReason("AllDependenciesMet"))
		condMgr.MarkFalse(string(common.ConditionTypeDegraded),
			conditions.WithSeverity(common.ConditionSeverityInfo),
			conditions.WithReason("NoDegradation"))
	}

	for group, reasons := range result.groupReasons {
		slices.Sort(reasons)
		if len(reasons) > 0 {
			condMgr.MarkTrue(group,
				conditions.WithSeverity(common.ConditionSeverityInfo),
				conditions.WithReason("MissingDependency"),
				conditions.WithMessage("%s", strings.Join(reasons, "; ")))
		} else {
			condMgr.MarkFalse(group,
				conditions.WithSeverity(common.ConditionSeverityInfo),
				conditions.WithReason("AllDependenciesSatisfied"))
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
}

func (r *KserveModuleReconciler) updateStatus(ctx context.Context, kserve *platformv1alpha1.Kserve, condMgr *conditions.Manager) error {
	r.setReleaseStatus(kserve)
	condMgr.Sort()

	if condMgr.IsHappy() {
		kserve.Status.Phase = common.PhaseReady
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

func (r *KserveModuleReconciler) setReleaseStatus(kserve *platformv1alpha1.Kserve) {
	if len(kserve.Status.Releases) > 0 {
		return
	}

	releases, err := loadComponentReleases(r.ManifestsTemplatePath,
		[]string{kserveComponentName, odhModelControllerComponentName})
	if err != nil {
		ctrl.Log.Error(err, "failed to load component releases")
		return
	}

	kserve.SetReleaseStatus(common.ComponentReleaseStatus{Releases: releases})
}
