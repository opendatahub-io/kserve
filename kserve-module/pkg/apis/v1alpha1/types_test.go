package v1alpha1

import (
	"testing"

	"github.com/opendatahub-io/odh-platform-utilities/api/common"
	"github.com/opendatahub-io/odh-platform-utilities/api/common/validation"

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

// TestPlatformObject_Contract verifies that Kserve satisfies the
// PlatformObject behavioral contract using the shared validation suite
// from odh-platform-utilities, instead of hand-rolled accessor checks.
// It covers non-nil status, conditions round-trip, mandatory condition
// types (Ready + ProvisioningSucceeded), release status round-trip, and
// that Phase is writable through the status pointer.
func TestPlatformObject_Contract(t *testing.T) {
	validation.ValidatePlatformObject(t, &Kserve{})
}
