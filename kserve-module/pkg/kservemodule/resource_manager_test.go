package kservemodule

import (
	"context"
	"testing"

	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestCheckDeploymentReadiness_OCP_AllReady(t *testing.T) {
	g := NewWithT(t)

	var deps []appsv1.Deployment
	for _, name := range kserveDeploymentsOCP {
		deps = append(deps, buildDeployment(name, 1))
	}

	objs := make([]client.Object, len(deps))
	for i := range deps {
		objs[i] = &deps[i]
	}

	cli := fake.NewClientBuilder().
		WithScheme(testScheme()).
		WithObjects(objs...).
		WithStatusSubresource(objs...).
		Build()

	err := checkDeploymentReadiness(context.Background(), cli, "opendatahub", false)
	g.Expect(err).ShouldNot(HaveOccurred())
}

func TestCheckDeploymentReadiness_OCP_NotReady(t *testing.T) {
	g := NewWithT(t)

	deps := []appsv1.Deployment{
		buildDeployment(kserveControllerDeployment, 1),
		buildDeployment(llmISVCControllerDeployment, 0),
		buildDeployment(odhModelControllerDeployment, 1),
	}

	objs := make([]client.Object, len(deps))
	for i := range deps {
		objs[i] = &deps[i]
	}

	cli := fake.NewClientBuilder().
		WithScheme(testScheme()).
		WithObjects(objs...).
		WithStatusSubresource(objs...).
		Build()

	err := checkDeploymentReadiness(context.Background(), cli, "opendatahub", false)
	g.Expect(err).Should(HaveOccurred())
	g.Expect(err.Error()).Should(ContainSubstring("llmisvc-controller-manager"))
}

func TestCheckDeploymentReadiness_XKS_AllReady(t *testing.T) {
	g := NewWithT(t)

	var deps []appsv1.Deployment
	for _, name := range kserveDeploymentsXKS {
		deps = append(deps, buildDeployment(name, 1))
	}

	objs := make([]client.Object, len(deps))
	for i := range deps {
		objs[i] = &deps[i]
	}

	cli := fake.NewClientBuilder().
		WithScheme(testScheme()).
		WithObjects(objs...).
		WithStatusSubresource(objs...).
		Build()

	err := checkDeploymentReadiness(context.Background(), cli, "opendatahub", true)
	g.Expect(err).ShouldNot(HaveOccurred())
}

func TestCheckDeploymentReadiness_Missing(t *testing.T) {
	g := NewWithT(t)

	cli := fake.NewClientBuilder().
		WithScheme(testScheme()).
		Build()

	err := checkDeploymentReadiness(context.Background(), cli, "opendatahub", false)
	g.Expect(err).Should(HaveOccurred())
}

func buildDeployment(name string, available int32) appsv1.Deployment {
	return appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "opendatahub"},
		Status:     appsv1.DeploymentStatus{AvailableReplicas: available},
	}
}

func testScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = appsv1.AddToScheme(s)
	return s
}

