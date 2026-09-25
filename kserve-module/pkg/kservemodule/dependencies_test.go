package kservemodule

import (
	"testing"

	. "github.com/onsi/gomega"

	"github.com/opendatahub-io/odh-platform-utilities/api/common"

	platformv1alpha1 "github.com/opendatahub-io/kserve-module/pkg/apis/v1alpha1"
)

func TestKserveDependencies_Defined(t *testing.T) {
	g := NewWithT(t)

	g.Expect(kserveDependencies).ShouldNot(BeEmpty())

	for _, dep := range kserveDependencies {
		assertDependencyValid(g, dep)
	}
}

func TestModelControllerDependencies_Defined(t *testing.T) {
	g := NewWithT(t)

	g.Expect(modelControllerDependencies).ShouldNot(BeEmpty())

	for _, dep := range modelControllerDependencies {
		assertDependencyValid(g, dep)
	}
}

func TestModelControllerDependencies_WVAAlwaysSkipped(t *testing.T) {
	g := NewWithT(t)

	var cma *dependencyCheck
	for i := range modelControllerDependencies {
		if modelControllerDependencies[i].conditionGroup == conditionLLMDWVADeps {
			cma = &modelControllerDependencies[i]
			break
		}
	}
	g.Expect(cma).ShouldNot(BeNil(), "CMA/WVA dependency must remain registered")
	g.Expect(cma.skipFunc).ShouldNot(BeNil(), "CMA/WVA dependency must have skipFunc")

	for _, state := range []common.ManagementState{common.Managed, common.Removed, ""} {
		kserve := &platformv1alpha1.Kserve{
			Spec: platformv1alpha1.KserveSpec{
				WVA: platformv1alpha1.WVASpec{ManagementState: state},
			},
		}
		g.Expect(cma.skipFunc(kserve)).To(BeTrue(),
			"CMA/WVA dependency must be skipped even when spec.wva.managementState=%q", state)
	}
}

func assertDependencyValid(g Gomega, dep dependencyCheck) {
	g.Expect(dep.name).ShouldNot(BeEmpty(), "dependency must have a name")
	g.Expect(dep.checkType).ShouldNot(BeEmpty(), "dependency %s must have a checkType", dep.name)
	g.Expect(dep.platform).Should(BeElementOf("", "ocp", "xks"),
		"dependency %s has invalid platform %q", dep.name, dep.platform)

	switch dep.checkType {
	case checkCRD:
		g.Expect(dep.crdName).ShouldNot(BeEmpty(),
			"CRD dependency %s must have crdName", dep.name)
	case checkSubscription:
		g.Expect(dep.subscriptionName).ShouldNot(BeEmpty(),
			"subscription dependency %s must have subscriptionName", dep.name)
		g.Expect(dep.conditionGroup).ShouldNot(BeEmpty(),
			"subscription dependency %s must have conditionGroup", dep.name)
	case checkOperator:
		g.Expect(dep.operatorGVK.Kind).ShouldNot(BeEmpty(),
			"operator dependency %s must have operatorGVK.Kind", dep.name)
		g.Expect(dep.conditionFilter).ShouldNot(BeNil(),
			"operator dependency %s must have conditionFilter", dep.name)
	}
}

func TestLwsConditionFilter_Healthy(t *testing.T) {
	g := NewWithT(t)

	g.Expect(lwsConditionFilter("Available", "True")).Should(BeFalse())
	g.Expect(lwsConditionFilter("Degraded", "False")).Should(BeFalse())
}

func TestLwsConditionFilter_Degraded(t *testing.T) {
	g := NewWithT(t)

	g.Expect(lwsConditionFilter("Degraded", "True")).Should(BeTrue())
	g.Expect(lwsConditionFilter("Available", "False")).Should(BeTrue())
}

func TestLwsConditionFilter_TargetConfigDegraded(t *testing.T) {
	g := NewWithT(t)

	g.Expect(lwsConditionFilter("TargetConfigControllerDegraded", "True")).Should(BeTrue())
}

func TestLwsConditionFilter_Unknown(t *testing.T) {
	g := NewWithT(t)

	g.Expect(lwsConditionFilter("SomeOther", "True")).Should(BeFalse())
	g.Expect(lwsConditionFilter("", "")).Should(BeFalse())
}
