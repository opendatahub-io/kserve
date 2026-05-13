package kservemodule

import (
	"testing"

	"github.com/opendatahub-io/odh-platform-utilities/api/common"
	platformv1alpha1 "github.com/opendatahub-io/kserve-module/pkg/apis/v1alpha1"

	. "github.com/onsi/gomega"
)

func TestGetManagementState_FromSpec(t *testing.T) {
	g := NewWithT(t)

	kserve := &platformv1alpha1.Kserve{}
	kserve.Spec.ManagementState = common.Removed

	g.Expect(platformv1alpha1.GetManagementState(kserve)).Should(Equal(common.Removed))
}

func TestGetManagementState_DefaultManaged(t *testing.T) {
	g := NewWithT(t)

	kserve := &platformv1alpha1.Kserve{}

	g.Expect(platformv1alpha1.GetManagementState(kserve)).Should(Equal(common.Managed))
}

func TestGetManagementState_Managed(t *testing.T) {
	g := NewWithT(t)

	kserve := &platformv1alpha1.Kserve{}
	kserve.Spec.ManagementState = common.Managed

	g.Expect(platformv1alpha1.GetManagementState(kserve)).Should(Equal(common.Managed))
}
