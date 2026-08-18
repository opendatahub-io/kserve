package kservemodule

import (
	"fmt"
	"testing"

	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/opendatahub-io/odh-platform-utilities/api/common"
	"github.com/opendatahub-io/odh-platform-utilities/pkg/controller/conditions"

	platformv1alpha1 "github.com/opendatahub-io/kserve-module/pkg/apis/v1alpha1"
)

func TestNewConditionManager_InitializesConditions(t *testing.T) {
	g := NewWithT(t)

	kserve := &platformv1alpha1.Kserve{}
	condMgr := newConditionManager(kserve)

	g.Expect(condMgr.GetCondition(string(common.ConditionTypeReady))).ShouldNot(BeNil())
	g.Expect(condMgr.GetCondition(string(common.ConditionTypeProvisioningSucceeded))).ShouldNot(BeNil())
	g.Expect(condMgr.GetCondition(ConditionKServeReady)).ShouldNot(BeNil())
	g.Expect(condMgr.GetCondition(ConditionModelControllerReady)).ShouldNot(BeNil())
}

func TestApplyProvisioningCondition_Success(t *testing.T) {
	g := NewWithT(t)

	kserve := &platformv1alpha1.Kserve{}
	condMgr := newConditionManager(kserve)

	applyProvisioningCondition(condMgr, nil)

	cond := condMgr.GetCondition(string(common.ConditionTypeProvisioningSucceeded))
	g.Expect(cond).ShouldNot(BeNil())
	g.Expect(cond.Status).Should(Equal(metav1.ConditionTrue))
	g.Expect(cond.Reason).Should(Equal("AllResourcesApplied"))
}

func TestApplyProvisioningCondition_Failure(t *testing.T) {
	g := NewWithT(t)

	kserve := &platformv1alpha1.Kserve{}
	condMgr := newConditionManager(kserve)

	errs := map[string]error{
		"kserve": fmt.Errorf("render failed"),
	}
	applyProvisioningCondition(condMgr, errs)

	cond := condMgr.GetCondition(string(common.ConditionTypeProvisioningSucceeded))
	g.Expect(cond).ShouldNot(BeNil())
	g.Expect(cond.Status).Should(Equal(metav1.ConditionFalse))
	g.Expect(cond.Reason).Should(Equal("DeployFailed"))
}

func TestApplyDependencyConditions_NoDegradation(t *testing.T) {
	g := NewWithT(t)

	kserve := &platformv1alpha1.Kserve{}
	condMgr := newConditionManager(kserve)

	applyDependencyConditions(condMgr, dependencyResult{})

	cond := condMgr.GetCondition(string(common.ConditionTypeDegraded))
	g.Expect(cond).ShouldNot(BeNil())
	g.Expect(cond.Status).Should(Equal(metav1.ConditionFalse))
}

func TestApplyDependencyConditions_Degraded(t *testing.T) {
	g := NewWithT(t)

	kserve := &platformv1alpha1.Kserve{}
	condMgr := newConditionManager(kserve)

	applyDependencyConditions(condMgr, dependencyResult{
		allReasons: []string{"Istio CRD not found"},
	})

	cond := condMgr.GetCondition(string(common.ConditionTypeDegraded))
	g.Expect(cond).ShouldNot(BeNil())
	g.Expect(cond.Status).Should(Equal(metav1.ConditionFalse))
	g.Expect(cond.Severity).Should(Equal(common.ConditionSeverityInfo))
}

func markAllHealthy(condMgr *conditions.Manager) {
	condMgr.MarkTrue(string(common.ConditionTypeProvisioningSucceeded),
		conditions.WithReason("AllResourcesApplied"))
	condMgr.MarkTrue(ConditionKServeReady,
		conditions.WithReason("AllDeploymentsAvailable"))
	condMgr.MarkTrue(ConditionModelControllerReady,
		conditions.WithReason("AllDeploymentsAvailable"))
	condMgr.MarkTrue(ConditionModelCacheReady,
		conditions.WithReason("Disabled"))
	condMgr.MarkTrue(ConditionDependenciesAvailable,
		conditions.WithReason("AllDependenciesMet"))
	condMgr.ClearCondition(ConditionWVAReady)
}

func TestHappyCondition_AllHealthy(t *testing.T) {
	g := NewWithT(t)

	kserve := &platformv1alpha1.Kserve{}
	condMgr := newConditionManager(kserve)
	markAllHealthy(condMgr)

	g.Expect(condMgr.IsHappy()).Should(BeTrue())
}

func TestHappyCondition_DeploymentNotReady(t *testing.T) {
	g := NewWithT(t)

	kserve := &platformv1alpha1.Kserve{}
	condMgr := newConditionManager(kserve)
	markAllHealthy(condMgr)

	condMgr.MarkFalse(ConditionKServeReady,
		conditions.WithReason("DeploymentNotReady"))

	g.Expect(condMgr.IsHappy()).Should(BeFalse())
}

func TestHappyCondition_DependenciesNotAvailable(t *testing.T) {
	g := NewWithT(t)

	kserve := &platformv1alpha1.Kserve{}
	condMgr := newConditionManager(kserve)
	markAllHealthy(condMgr)

	condMgr.MarkFalse(ConditionDependenciesAvailable,
		conditions.WithReason("DependencyDegraded"))

	g.Expect(condMgr.IsHappy()).Should(BeFalse())
}

func depResultWithAvail(severity common.ConditionSeverity, msg string) dependencyResult {
	return dependencyResult{
		availReasons: []availabilityReason{{severity: severity, message: msg}},
		allReasons:   []string{msg},
	}
}

func TestApplyDependencyConditions_AvailInfoKeepsReady(t *testing.T) {
	g := NewWithT(t)

	kserve := &platformv1alpha1.Kserve{}
	condMgr := newConditionManager(kserve)
	markAllHealthy(condMgr)

	applyDependencyConditions(condMgr, depResultWithAvail(common.ConditionSeverityInfo, "LWS operator degraded"))

	depCond := condMgr.GetCondition(ConditionDependenciesAvailable)
	g.Expect(depCond).ShouldNot(BeNil())
	g.Expect(depCond.Status).Should(Equal(metav1.ConditionFalse))
	g.Expect(depCond.Severity).Should(Equal(common.ConditionSeverityInfo))

	g.Expect(condMgr.IsHappy()).Should(BeTrue())
}

func TestApplyDependencyConditions_AvailErrorBreaksReady(t *testing.T) {
	g := NewWithT(t)

	kserve := &platformv1alpha1.Kserve{}
	condMgr := newConditionManager(kserve)
	markAllHealthy(condMgr)

	applyDependencyConditions(condMgr, depResultWithAvail(common.ConditionSeverityError, "cert-manager CRD not found"))

	depCond := condMgr.GetCondition(ConditionDependenciesAvailable)
	g.Expect(depCond).ShouldNot(BeNil())
	g.Expect(depCond.Status).Should(Equal(metav1.ConditionFalse))

	g.Expect(condMgr.IsHappy()).Should(BeFalse())
}

func TestApplyDependencyConditions_MixedSeverityUsesWorst(t *testing.T) {
	g := NewWithT(t)

	kserve := &platformv1alpha1.Kserve{}
	condMgr := newConditionManager(kserve)
	markAllHealthy(condMgr)

	applyDependencyConditions(condMgr, dependencyResult{
		availReasons: []availabilityReason{
			{severity: common.ConditionSeverityInfo, message: "LWS operator degraded"},
			{severity: common.ConditionSeverityError, message: "cert-manager CRD not found"},
		},
		allReasons: []string{"LWS operator degraded", "cert-manager CRD not found"},
	})

	depCond := condMgr.GetCondition(ConditionDependenciesAvailable)
	g.Expect(depCond).ShouldNot(BeNil())
	g.Expect(depCond.Status).Should(Equal(metav1.ConditionFalse))
	g.Expect(depCond.Severity).Should(Equal(common.ConditionSeverityError))

	degradedCond := condMgr.GetCondition(string(common.ConditionTypeDegraded))
	g.Expect(degradedCond).ShouldNot(BeNil())
	g.Expect(degradedCond.Status).Should(Equal(metav1.ConditionTrue))
	g.Expect(degradedCond.Message).Should(ContainSubstring("cert-manager"))
	g.Expect(degradedCond.Message).ShouldNot(ContainSubstring("LWS"))

	g.Expect(condMgr.IsHappy()).Should(BeFalse())
}

func TestDegradedDoesNotAffectReady(t *testing.T) {
	g := NewWithT(t)

	kserve := &platformv1alpha1.Kserve{}
	condMgr := newConditionManager(kserve)
	markAllHealthy(condMgr)

	applyDependencyConditions(condMgr, dependencyResult{
		allReasons: []string{"optional dep missing"},
	})

	g.Expect(condMgr.IsHappy()).Should(BeTrue())
}

func TestApplyDependencyConditions_GroupCondition(t *testing.T) {
	g := NewWithT(t)

	kserve := &platformv1alpha1.Kserve{}
	condMgr := newConditionManager(kserve)

	applyDependencyConditions(condMgr, dependencyResult{
		groupReasons: map[string][]string{
			conditionLLMISVCDeps: {"RHCL not installed", "cert-manager not installed"},
		},
	})

	cond := condMgr.GetCondition(conditionLLMISVCDeps)
	g.Expect(cond).ShouldNot(BeNil())
	g.Expect(cond.Status).Should(Equal(metav1.ConditionFalse))
	g.Expect(cond.Severity).Should(Equal(common.ConditionSeverityInfo))
	g.Expect(cond.Message).Should(ContainSubstring("RHCL"))
}

func TestGroupConditionDoesNotAffectReady(t *testing.T) {
	g := NewWithT(t)

	kserve := &platformv1alpha1.Kserve{}
	condMgr := newConditionManager(kserve)
	markAllHealthy(condMgr)

	applyDependencyConditions(condMgr, dependencyResult{
		groupReasons: map[string][]string{
			conditionLLMISVCDeps:       {"RHCL not installed"},
			conditionLLMISVCWideEPDeps: {"LWS not installed"},
		},
	})

	g.Expect(condMgr.IsHappy()).Should(BeTrue())
}

func TestGroupConditionsSetByApply(t *testing.T) {
	g := NewWithT(t)

	kserve := &platformv1alpha1.Kserve{}
	condMgr := newConditionManager(kserve)

	g.Expect(condMgr.GetCondition(conditionLLMISVCDeps)).Should(BeNil(),
		"group conditions should not be registered as dependents")

	applyDependencyConditions(condMgr, dependencyResult{
		groupReasons: map[string][]string{
			conditionLLMISVCDeps: {"RHCL not installed"},
		},
	})

	cond := condMgr.GetCondition(conditionLLMISVCDeps)
	g.Expect(cond).ShouldNot(BeNil(), "group condition should be set after apply")
	g.Expect(cond.Status).Should(Equal(metav1.ConditionFalse))
}
