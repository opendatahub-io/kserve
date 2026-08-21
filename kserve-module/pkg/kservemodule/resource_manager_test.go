package kservemodule

import (
	"context"
	"testing"

	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestCheckKServeReadiness_OCP_AllReady(t *testing.T) {
	g := NewWithT(t)

	deps := []appsv1.Deployment{
		buildDeployment(kserveControllerDeployment, 1),
		buildDeployment(llmISVCControllerDeployment, 1),
	}

	cli := buildFakeClient(deps...)

	err := checkKServeReadiness(context.Background(), cli, "opendatahub", false)
	g.Expect(err).ShouldNot(HaveOccurred())
}

func TestCheckKServeReadiness_OCP_NotReady(t *testing.T) {
	g := NewWithT(t)

	deps := []appsv1.Deployment{
		buildDeployment(kserveControllerDeployment, 1),
		buildDeployment(llmISVCControllerDeployment, 0),
	}

	cli := buildFakeClient(deps...)

	err := checkKServeReadiness(context.Background(), cli, "opendatahub", false)
	g.Expect(err).Should(HaveOccurred())
	g.Expect(err.Error()).Should(ContainSubstring("llmisvc-controller-manager"))
}

func TestCheckModelControllerReadiness_OCP_Ready(t *testing.T) {
	g := NewWithT(t)

	deps := []appsv1.Deployment{
		buildDeployment(odhModelControllerDeployment, 1),
	}

	cli := buildFakeClient(deps...)

	err := checkModelControllerReadiness(context.Background(), cli, "opendatahub", false)
	g.Expect(err).ShouldNot(HaveOccurred())
}

func TestCheckModelControllerReadiness_XKS_Skipped(t *testing.T) {
	g := NewWithT(t)

	cli := fake.NewClientBuilder().WithScheme(testScheme()).Build()

	err := checkModelControllerReadiness(context.Background(), cli, "opendatahub", true)
	g.Expect(err).ShouldNot(HaveOccurred())
}

func TestCheckKServeReadiness_XKS_AllReady(t *testing.T) {
	g := NewWithT(t)

	deps := []appsv1.Deployment{
		buildDeployment(llmISVCControllerDeployment, 1),
	}

	cli := buildFakeClient(deps...)

	err := checkKServeReadiness(context.Background(), cli, "opendatahub", true)
	g.Expect(err).ShouldNot(HaveOccurred())
}

func TestCheckKServeReadiness_Missing(t *testing.T) {
	g := NewWithT(t)

	cli := fake.NewClientBuilder().WithScheme(testScheme()).Build()

	err := checkKServeReadiness(context.Background(), cli, "opendatahub", false)
	g.Expect(err).Should(HaveOccurred())
}

func TestDeleteResourceIfPresent_Deletes(t *testing.T) {
	g := NewWithT(t)

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "test-deploy", Namespace: "ns"},
	}
	cli := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(dep).Build()

	err := deleteResourceIfPresent(context.Background(), cli, dep)
	g.Expect(err).ShouldNot(HaveOccurred())

	got := &appsv1.Deployment{}
	err = cli.Get(context.Background(), client.ObjectKeyFromObject(dep), got)
	g.Expect(err).Should(HaveOccurred())
	g.Expect(client.IgnoreNotFound(err)).Should(BeNil())
}

func TestDeleteResourceIfPresent_SkipsWhenAbsent(t *testing.T) {
	g := NewWithT(t)

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "nonexistent", Namespace: "ns"},
	}
	cli := fake.NewClientBuilder().WithScheme(testScheme()).Build()

	err := deleteResourceIfPresent(context.Background(), cli, dep)
	g.Expect(err).ShouldNot(HaveOccurred())
}

func TestCheckPresetsPresent_AllPresent(t *testing.T) {
	g := NewWithT(t)

	cli := fake.NewClientBuilder().WithScheme(testScheme()).
		WithObjects(buildPreset("v1-2-3-kserve-config-llm-scheduler"), buildPreset("v1-2-3-kserve-config-llm-decode")).
		Build()

	err := checkPresetsPresent(context.Background(), cli, "opendatahub",
		[]string{"v1-2-3-kserve-config-llm-scheduler", "v1-2-3-kserve-config-llm-decode"})
	g.Expect(err).ShouldNot(HaveOccurred())
}

func TestCheckPresetsPresent_ReportsMissing(t *testing.T) {
	g := NewWithT(t)

	cli := fake.NewClientBuilder().WithScheme(testScheme()).
		WithObjects(buildPreset("v1-2-3-kserve-config-llm-decode")).
		Build()

	err := checkPresetsPresent(context.Background(), cli, "opendatahub",
		[]string{"v1-2-3-kserve-config-llm-scheduler", "v1-2-3-kserve-config-llm-decode", "v1-2-3-kserve-config-llm-prefill"})
	g.Expect(err).Should(HaveOccurred())
	g.Expect(err.Error()).Should(ContainSubstring("v1-2-3-kserve-config-llm-prefill, v1-2-3-kserve-config-llm-scheduler"))
	g.Expect(err.Error()).ShouldNot(ContainSubstring("decode"))
}

// A preset deleted in the applications namespace must not be satisfied by a
// same-named config a user created somewhere else.
func TestCheckPresetsPresent_IgnoresOtherNamespaces(t *testing.T) {
	g := NewWithT(t)

	elsewhere := buildPreset("v1-2-3-kserve-config-llm-scheduler")
	elsewhere.SetNamespace("some-user-ns")
	cli := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(elsewhere).Build()

	err := checkPresetsPresent(context.Background(), cli, "opendatahub",
		[]string{"v1-2-3-kserve-config-llm-scheduler"})
	g.Expect(err).Should(HaveOccurred())
}

func TestCheckPresetsPresent_NothingExpected(t *testing.T) {
	g := NewWithT(t)

	cli := fake.NewClientBuilder().WithScheme(testScheme()).Build()

	g.Expect(checkPresetsPresent(context.Background(), cli, "opendatahub", nil)).Should(Succeed())
}

func buildPreset(name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(llmISVCConfigGVK)
	u.SetName(name)
	u.SetNamespace("opendatahub")
	u.SetAnnotations(map[string]string{wellKnownAnnotationKey: wellKnownAnnotationValue})
	return u
}

func buildDeployment(name string, available int32) appsv1.Deployment {
	return appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "opendatahub"},
		Status:     appsv1.DeploymentStatus{AvailableReplicas: available},
	}
}

func buildFakeClient(deps ...appsv1.Deployment) client.Client {
	objs := make([]client.Object, len(deps))
	for i := range deps {
		objs[i] = &deps[i]
	}
	return fake.NewClientBuilder().
		WithScheme(testScheme()).
		WithObjects(objs...).
		WithStatusSubresource(objs...).
		Build()
}

func testScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = appsv1.AddToScheme(s)
	s.AddKnownTypeWithName(llmISVCConfigGVK, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(llmISVCConfigListGVK, &unstructured.UnstructuredList{})
	return s
}
