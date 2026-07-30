package kservemodule

import (
	"runtime"
	"testing"

	. "github.com/onsi/gomega"
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

func assertDependencyValid(g Gomega, dep dependencyCheck) {
	g.Expect(dep.name).ShouldNot(BeEmpty(), "dependency must have a name")
	g.Expect(dep.checkType).ShouldNot(BeEmpty(), "dependency %s must have a checkType", dep.name)
	g.Expect(dep.platform).Should(BeElementOf("", "ocp", "xks"),
		"dependency %s has invalid platform %q", dep.name, dep.platform)

	for _, arch := range dep.supportedArchitectures {
		g.Expect(arch).ShouldNot(BeEmpty(),
			"dependency %s has empty supportedArchitectures entry", dep.name)
	}

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

func TestRHCLDependencies_RestrictedToAMD64(t *testing.T) {
	g := NewWithT(t)

	rhclDependencyCount := 0
	for _, dep := range kserveDependencies {
		if dep.subscriptionName != rhclSubscription {
			continue
		}
		rhclDependencyCount++
		g.Expect(dep.supportedArchitectures).Should(Equal([]string{"amd64"}),
			"RHCL dependency %q must be restricted to amd64 only", dep.name)
	}
	g.Expect(rhclDependencyCount).Should(Equal(2),
		"expected exactly 2 RHCL dependencies (standard and Wide EP), found %d", rhclDependencyCount)
}

func TestSupportedArchitectures_FilterSkipsUnsupported(t *testing.T) {
	g := NewWithT(t)

	dep := dependencyCheck{
		name:                   "test-dep",
		checkType:              checkSubscription,
		subscriptionName:       "test-sub",
		conditionGroup:         conditionLLMISVCDeps,
		supportedArchitectures: []string{"amd64"},
	}

	if runtime.GOARCH == "amd64" {
		g.Expect(dep.supportedArchitectures).Should(ContainElement(runtime.GOARCH),
			"on amd64, dep should match the current architecture")
	} else {
		g.Expect(dep.supportedArchitectures).ShouldNot(ContainElement(runtime.GOARCH),
			"on non-amd64, dep should not match the current architecture")
	}
}
