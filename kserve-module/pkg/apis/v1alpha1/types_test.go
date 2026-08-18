package v1alpha1

import (
	"testing"

	"github.com/opendatahub-io/odh-platform-utilities/api/common"

	. "github.com/onsi/gomega"
)

func TestGetManagementState_FromSpec(t *testing.T) {
	g := NewWithT(t)

	kserve := &Kserve{}
	kserve.Spec.ManagementState = common.Removed

	g.Expect(GetManagementState(kserve)).Should(Equal(common.Removed))
}

func TestGetManagementState_DefaultManaged(t *testing.T) {
	g := NewWithT(t)

	kserve := &Kserve{}

	g.Expect(GetManagementState(kserve)).Should(Equal(common.Managed))
}

func TestGetManagementState_Managed(t *testing.T) {
	g := NewWithT(t)

	kserve := &Kserve{}
	kserve.Spec.ManagementState = common.Managed

	g.Expect(GetManagementState(kserve)).Should(Equal(common.Managed))
}

func TestPlatformObject_GetStatus(t *testing.T) {
	g := NewWithT(t)

	kserve := &Kserve{}
	kserve.Status.Phase = common.PhaseReady

	g.Expect(kserve.GetStatus().Phase).Should(Equal(common.PhaseReady))
}

func TestPlatformObject_Conditions(t *testing.T) {
	g := NewWithT(t)

	kserve := &Kserve{}
	g.Expect(kserve.GetConditions()).Should(BeEmpty())

	conditions := []common.Condition{{Type: "Ready", Status: "True"}}
	kserve.SetConditions(conditions)
	g.Expect(kserve.GetConditions()).Should(HaveLen(1))
	g.Expect(kserve.GetConditions()[0].Type).Should(Equal("Ready"))
}

func TestPlatformObject_Releases(t *testing.T) {
	g := NewWithT(t)

	kserve := &Kserve{}
	releases := common.ComponentReleaseStatus{
		Releases: []common.ComponentRelease{{Name: "KServe", Version: "v0.17.0"}},
	}
	kserve.SetReleaseStatus(releases)

	g.Expect(kserve.GetReleaseStatus().Releases).Should(HaveLen(1))
	g.Expect(kserve.GetReleaseStatus().Releases[0].Name).Should(Equal("KServe"))
}
