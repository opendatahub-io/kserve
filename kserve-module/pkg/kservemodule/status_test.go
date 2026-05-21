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

func markAllHealthy(condMgr *conditions.Manager) {
	condMgr.MarkTrue(string(common.ConditionTypeProvisioningSucceeded),
		conditions.WithReason("AllResourcesApplied"))
	condMgr.MarkTrue(ConditionKServeReady,
		conditions.WithReason("AllDeploymentsAvailable"))
	condMgr.MarkTrue(ConditionModelControllerReady,
		conditions.WithReason("AllDeploymentsAvailable"))
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

