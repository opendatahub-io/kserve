package kservemodule_test

import (
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/opendatahub-io/odh-platform-utilities/api/common"
	"github.com/opendatahub-io/odh-platform-utilities/pkg/cluster"

	platformv1alpha1 "github.com/opendatahub-io/kserve-module/pkg/apis/v1alpha1"
	"github.com/opendatahub-io/kserve-module/pkg/kservemodule"
	"github.com/opendatahub-io/kserve-module/pkg/kservemodule/fixture"
)

// createMatchingCRD creates a CRD whose name matches the convention used by
// CustomResourceDefinitionExists: lowercase(kind+"s").group
func createMatchingCRD(ctx SpecContext, k8sClient client.Client, group, kind string) *apiextensionsv1.CustomResourceDefinition {
	plural := strings.ToLower(kind) + "s"
	crd := &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("%s.%s", plural, group),
		},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: group,
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{
				Name: "v1", Served: true, Storage: true,
				Schema: &apiextensionsv1.CustomResourceValidation{
					OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
						Type: "object",
					},
				},
			}},
			Scope: apiextensionsv1.NamespaceScoped,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Plural:   plural,
				Singular: strings.ToLower(kind),
				Kind:     kind,
			},
		},
	}

	ExpectWithOffset(1, client.IgnoreAlreadyExists(k8sClient.Create(ctx, crd))).To(Succeed())

	Eventually(func(g Gomega) {
		var updated apiextensionsv1.CustomResourceDefinition
		g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: crd.Name}, &updated)).To(Succeed())
		for _, c := range updated.Status.Conditions {
			if c.Type == apiextensionsv1.Established && c.Status == apiextensionsv1.ConditionTrue {
				return
			}
		}
		g.Expect(false).To(BeTrue(), "CRD %s not established", crd.Name)
	}).WithContext(ctx).WithTimeout(30 * time.Second).Should(Succeed())

	return crd
}

func refreshKserveStatus(ctx SpecContext, k8sClient client.Client, kserve *platformv1alpha1.Kserve) error {
	return k8sClient.Get(ctx, client.ObjectKeyFromObject(kserve), kserve)
}

var criticalCRDs = kservemodule.CriticalCRDDependenciesForTest()

// Ordered keeps the manager alive across specs so deferred status updates
// succeed, and lets tests incrementally add/remove CRDs to verify state transitions.
var _ = Describe("Dependency Integration", Ordered, func() {
	var (
		testCRDs map[string]*apiextensionsv1.CustomResourceDefinition
		kserve   *platformv1alpha1.Kserve
	)

	BeforeAll(func(ctx SpecContext) {
		testCRDs = make(map[string]*apiextensionsv1.CustomResourceDefinition)

		testEnv.Reconciler.Deployer = &fixture.MockDeployer{}

		kserve = fixture.KserveCR(fixture.WithName("default-kserve"))
		Expect(testEnv.Client.Create(ctx, kserve)).To(Succeed())

		Eventually(func(g Gomega) {
			g.Expect(refreshKserveStatus(ctx, testEnv.Client, kserve)).To(Succeed())
			g.Expect(kserve.Status.ObservedGeneration).To(Equal(kserve.Generation))
		}).WithContext(ctx).WithTimeout(30 * time.Second).Should(Succeed())

		testEnv.Reconciler.SetClusterType(cluster.ClusterTypeKubernetes)
	})

	AfterAll(func(ctx SpecContext) {
		if kserve != nil {
			client.IgnoreNotFound(testEnv.Client.Delete(ctx, kserve))
		}
		for _, crd := range testCRDs {
			client.IgnoreNotFound(testEnv.Client.Delete(ctx, crd))
		}
		testEnv.Reconciler.SetClusterType(cluster.ClusterTypeOpenShift)
	})

	It("sets DependenciesAvailable=False and Degraded=True with Error severity when critical CRDs are missing", func(ctx SpecContext) {
		triggerReconcile(ctx, kserve, "dep-critical-failed")

		Eventually(func(g Gomega) {
			g.Expect(refreshKserveStatus(ctx, testEnv.Client, kserve)).To(Succeed())


			depCond := fixture.FindCondition(kserve, kservemodule.ConditionDependenciesAvailable)
			g.Expect(depCond).NotTo(BeNil(), "DependenciesAvailable condition should exist")
			g.Expect(depCond.Status).To(Equal(metav1.ConditionFalse))

			degradedCond := fixture.FindCondition(kserve, string(common.ConditionTypeDegraded))
			g.Expect(degradedCond).NotTo(BeNil(), "Degraded condition should exist")
			g.Expect(degradedCond.Status).To(Equal(metav1.ConditionTrue))
			g.Expect(degradedCond.Severity).To(Equal(common.ConditionSeverityError))
		}).WithContext(ctx).WithTimeout(30 * time.Second).WithPolling(2 * time.Second).Should(Succeed())
	})

	It("reports critical failure with Error severity even when some optional CRDs are present", func(ctx SpecContext) {
		for _, dep := range []struct{ group, kind string }{
			{"networking.istio.io", "DestinationRule"},
			{"networking.istio.io", "Gateway"},
			{"security.istio.io", "AuthorizationPolicy"},
		} {
			crd := createMatchingCRD(ctx, testEnv.Client, dep.group, dep.kind)
			testCRDs[crd.Name] = crd
		}

		triggerReconcile(ctx, kserve, "dep-optional-ok-critical-failed")

		Eventually(func(g Gomega) {
			g.Expect(refreshKserveStatus(ctx, testEnv.Client, kserve)).To(Succeed())


			depCond := fixture.FindCondition(kserve, kservemodule.ConditionDependenciesAvailable)
			g.Expect(depCond).NotTo(BeNil())
			g.Expect(depCond.Status).To(Equal(metav1.ConditionFalse))

			degradedCond := fixture.FindCondition(kserve, string(common.ConditionTypeDegraded))
			g.Expect(degradedCond).NotTo(BeNil())
			g.Expect(degradedCond.Status).To(Equal(metav1.ConditionTrue))
			g.Expect(degradedCond.Severity).To(Equal(common.ConditionSeverityError))
		}).WithContext(ctx).WithTimeout(30 * time.Second).WithPolling(2 * time.Second).Should(Succeed())
	})

	It("sets Degraded=True with Info severity when critical CRDs exist but some optional CRDs are missing", func(ctx SpecContext) {
		for _, gk := range criticalCRDs {
			crd := createMatchingCRD(ctx, testEnv.Client, gk.Group, gk.Kind)
			testCRDs[crd.Name] = crd
		}

		triggerReconcile(ctx, kserve, "dep-critical-ok-optional-partial")

		Eventually(func(g Gomega) {
			g.Expect(refreshKserveStatus(ctx, testEnv.Client, kserve)).To(Succeed())


			depCond := fixture.FindCondition(kserve, kservemodule.ConditionDependenciesAvailable)
			g.Expect(depCond).NotTo(BeNil())
			g.Expect(depCond.Status).To(Equal(metav1.ConditionTrue))

			degradedCond := fixture.FindCondition(kserve, string(common.ConditionTypeDegraded))
			g.Expect(degradedCond).NotTo(BeNil())
			g.Expect(degradedCond.Status).To(Equal(metav1.ConditionTrue))
			g.Expect(degradedCond.Severity).To(Equal(common.ConditionSeverityInfo))
		}).WithContext(ctx).WithTimeout(30 * time.Second).WithPolling(2 * time.Second).Should(Succeed())
	})

	It("clears Degraded when all CRDs are present", func(ctx SpecContext) {
		for _, gk := range kservemodule.XKSCRDDependenciesForTest() {
			crd := createMatchingCRD(ctx, testEnv.Client, gk.Group, gk.Kind)
			testCRDs[crd.Name] = crd
		}

		triggerReconcile(ctx, kserve, "dep-all-met")

		Eventually(func(g Gomega) {
			g.Expect(refreshKserveStatus(ctx, testEnv.Client, kserve)).To(Succeed())


			depCond := fixture.FindCondition(kserve, kservemodule.ConditionDependenciesAvailable)
			g.Expect(depCond).NotTo(BeNil())
			g.Expect(depCond.Status).To(Equal(metav1.ConditionTrue))
			g.Expect(depCond.Reason).To(Equal("AllDependenciesMet"))

			degradedCond := fixture.FindCondition(kserve, string(common.ConditionTypeDegraded))
			g.Expect(degradedCond).NotTo(BeNil())
			g.Expect(degradedCond.Status).To(Equal(metav1.ConditionFalse))
		}).WithContext(ctx).WithTimeout(30 * time.Second).WithPolling(2 * time.Second).Should(Succeed())
	})
})
