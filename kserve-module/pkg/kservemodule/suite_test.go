package kservemodule_test

import (
	"context"
	"os"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/opendatahub-io/kserve-module/pkg/kservemodule/fixture"
)

var testEnv *fixture.TestEnv

func TestControllersIntegration(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("skipping envtest: KUBEBUILDER_ASSETS not set")
	}
	RegisterFailHandler(Fail)
	RunSpecs(t, "KserveModule Controller Suite")
}

var _ = BeforeSuite(func() {
	testEnv = fixture.SetupTestEnv(context.Background())
})
