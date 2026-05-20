package kservemodule

import (
	"context"
	"slices"
	"strings"

	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/opendatahub-io/odh-platform-utilities/api/common"
	"github.com/opendatahub-io/odh-platform-utilities/pkg/controller/conditions"

	platformv1alpha1 "github.com/opendatahub-io/kserve-module/pkg/apis/v1alpha1"
)

const (
	ConditionKServeReady           = "KServeReady"
	ConditionModelControllerReady  = "ModelControllerReady"
	ConditionDependenciesAvailable = "DependenciesAvailable"
)

func newConditionManager(kserve *platformv1alpha1.Kserve) *conditions.Manager {
	return conditions.NewManager(kserve,
		string(common.ConditionTypeReady),
		string(common.ConditionTypeProvisioningSucceeded),
		ConditionKServeReady,
		ConditionModelControllerReady,
		ConditionDependenciesAvailable,
	)
}

func applyDependencyConditions(condMgr *conditions.Manager, result dependencyResult) {
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

	names := make([]string, 0, len(componentErrors))
	for name := range componentErrors {
		names = append(names, name)
	}
	slices.Sort(names)

	msgs := make([]string, 0, len(names))
	for _, name := range names {
		msgs = append(msgs, name+": "+componentErrors[name].Error())
	}
	condMgr.MarkFalse(string(common.ConditionTypeProvisioningSucceeded),
		conditions.WithReason("DeployFailed"),
		conditions.WithMessage("%s", strings.Join(msgs, "; ")))
}

func (r *KserveModuleReconciler) updateComponentReadiness(ctx context.Context, condMgr *conditions.Manager) {
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
}

func (r *KserveModuleReconciler) updateStatus(ctx context.Context, kserve *platformv1alpha1.Kserve, condMgr *conditions.Manager) error {
	log := ctrl.LoggerFrom(ctx)

	r.setReleaseStatus(kserve)

	condMgr.Sort()
	kserve.Status.ObservedGeneration = kserve.Generation

	if condMgr.IsHappy() {
		kserve.Status.Phase = common.PhaseReady
	} else {
		kserve.Status.Phase = common.PhaseNotReady
	}

	if err := r.Status().Update(ctx, kserve); err != nil {
		log.Error(err, "failed to update status")
		return err
	}
	return nil
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
