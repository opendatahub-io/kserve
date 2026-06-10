package kservemodule_test

import (
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	k8serr "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/opendatahub-io/odh-platform-utilities/api/common"
	"github.com/opendatahub-io/odh-platform-utilities/pkg/cluster"

	platformv1alpha1 "github.com/opendatahub-io/kserve-module/pkg/apis/v1alpha1"
	"github.com/opendatahub-io/kserve-module/pkg/kservemodule"
	"github.com/opendatahub-io/kserve-module/pkg/kservemodule/fixture"
)

var _ = Describe("KserveModule Reconciler", func() {

	It("rejects a Kserve CR with wrong name", func(ctx SpecContext) {
		cr := fixture.KserveCR(fixture.WithName("wrong-name"))
		err := testEnv.Client.Create(ctx, cr)
		Expect(err).To(HaveOccurred())
		Expect(k8serr.IsInvalid(err)).To(BeTrue())
	})

	It("sets error status when manifests are missing", func(ctx SpecContext) {
		savedWorkDir := testEnv.Reconciler.WorkDir()
		testEnv.Reconciler.SetWorkDir(GinkgoT().TempDir())
		DeferCleanup(func() {
			testEnv.Reconciler.SetWorkDir(savedWorkDir)
		})

		cr := fixture.KserveCR()
		Expect(testEnv.Client.Create(ctx, cr)).To(Succeed())
		DeferCleanup(func(ctx SpecContext) {
			Expect(client.IgnoreNotFound(testEnv.Client.Delete(ctx, cr))).To(Succeed())
		})

		Eventually(func(g Gomega) {
			g.Expect(testEnv.Client.Get(ctx, client.ObjectKeyFromObject(cr), cr)).To(Succeed())
			cond := fixture.FindCondition(cr, string(common.ConditionTypeProvisioningSucceeded))
			g.Expect(cond).NotTo(BeNil())
			g.Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			g.Expect(cr.Status.Phase).To(Equal(common.PhaseNotReady))
			g.Expect(cr.Status.ObservedGeneration).To(Equal(cr.Generation))
		}).WithContext(ctx).Should(Succeed())
	})

	Context("reconcile lifecycle", Ordered, func() {
		var cr *platformv1alpha1.Kserve

		BeforeAll(func(ctx SpecContext) {
			cr = fixture.KserveCR()
			Expect(testEnv.Client.Create(ctx, cr)).To(Succeed())

			DeferCleanup(func(ctx SpecContext) {
				Expect(client.IgnoreNotFound(testEnv.Client.Delete(ctx, cr))).To(Succeed())
			})
		})

		BeforeEach(func() {
			testEnv.Deployer = &fixture.MockDeployer{}
			testEnv.Reconciler.Deployer = testEnv.Deployer
		})

		It("sets provisioning succeeded after successful reconcile", func(ctx SpecContext) {
			Eventually(func(g Gomega) {
				g.Expect(testEnv.Client.Get(ctx, client.ObjectKeyFromObject(cr), cr)).To(Succeed())
				cond := fixture.FindCondition(cr, string(common.ConditionTypeProvisioningSucceeded))
				g.Expect(cond).NotTo(BeNil())
				g.Expect(cond.Status).To(Equal(metav1.ConditionTrue))
			}).WithContext(ctx).Should(Succeed())

			lastCall := testEnv.Deployer.LastCall()
			Expect(lastCall).NotTo(BeNil())
			Expect(lastCall.Resources).NotTo(BeEmpty())

			hasConfigMap := false
			for _, res := range lastCall.Resources {
				if res.GetKind() == "ConfigMap" && res.GetName() == "inferenceservice-config" {
					hasConfigMap = true
					break
				}
			}
			Expect(hasConfigMap).To(BeTrue())
		})

		It("reports ready with all OCP deployments", func(ctx SpecContext) {
			testEnv.Reconciler.SetClusterType(cluster.ClusterTypeOpenShift)

			deployments := []string{
				"kserve-controller-manager",
				"llmisvc-controller-manager",
				"odh-model-controller",
			}
			for _, name := range deployments {
				createReadyDeployment(ctx, name, "opendatahub")
			}

			triggerReconcile(ctx, cr, "readiness-ocp")

			Eventually(func(g Gomega) {
				g.Expect(testEnv.Client.Get(ctx, client.ObjectKeyFromObject(cr), cr)).To(Succeed())
				g.Expect(cr.Status.Phase).To(Equal(common.PhaseReady))

				ready := fixture.FindCondition(cr, string(common.ConditionTypeReady))
				g.Expect(ready).NotTo(BeNil())
				g.Expect(ready.Status).To(Equal(metav1.ConditionTrue))

				kserveReady := fixture.FindCondition(cr, kservemodule.ConditionKServeReady)
				g.Expect(kserveReady).NotTo(BeNil())
				g.Expect(kserveReady.Status).To(Equal(metav1.ConditionTrue))

				modelCtrlReady := fixture.FindCondition(cr, kservemodule.ConditionModelControllerReady)
				g.Expect(modelCtrlReady).NotTo(BeNil())
				g.Expect(modelCtrlReady.Status).To(Equal(metav1.ConditionTrue))
			}).WithContext(ctx).Should(Succeed())
		})

		It("reports ready with XKS deployments only", func(ctx SpecContext) {
			testEnv.Reconciler.SetClusterType(cluster.ClusterTypeKubernetes)
			DeferCleanup(func() {
				testEnv.Reconciler.SetClusterType(cluster.ClusterTypeOpenShift)
			})

			// XKS only requires llmisvc-controller-manager
			createReadyDeployment(ctx, "llmisvc-controller-manager", "opendatahub")

			triggerReconcile(ctx, cr, "readiness-xks")

			Eventually(func(g Gomega) {
				g.Expect(testEnv.Client.Get(ctx, client.ObjectKeyFromObject(cr), cr)).To(Succeed())
				g.Expect(cr.Status.Phase).To(Equal(common.PhaseReady))

				ready := fixture.FindCondition(cr, string(common.ConditionTypeReady))
				g.Expect(ready).NotTo(BeNil())
				g.Expect(ready.Status).To(Equal(metav1.ConditionTrue))

				kserveReady := fixture.FindCondition(cr, kservemodule.ConditionKServeReady)
				g.Expect(kserveReady).NotTo(BeNil())
				g.Expect(kserveReady.Status).To(Equal(metav1.ConditionTrue))

				// XKS does not deploy model controller, so readiness is always true
				modelCtrlReady := fixture.FindCondition(cr, kservemodule.ConditionModelControllerReady)
				g.Expect(modelCtrlReady).NotTo(BeNil())
				g.Expect(modelCtrlReady.Status).To(Equal(metav1.ConditionTrue))
			}).WithContext(ctx).Should(Succeed())
		})

		// deploy error test runs last to avoid polluting CR status for other tests
		It("sets provisioning failed when deployer returns error", func(ctx SpecContext) {
			testEnv.Deployer.DeployError = fmt.Errorf("simulated deploy failure")

			triggerReconcile(ctx, cr, "deploy-error")

			Eventually(func(g Gomega) {
				g.Expect(testEnv.Client.Get(ctx, client.ObjectKeyFromObject(cr), cr)).To(Succeed())
				cond := fixture.FindCondition(cr, string(common.ConditionTypeProvisioningSucceeded))
				g.Expect(cond).NotTo(BeNil())
				g.Expect(cond.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(cond.Reason).To(Equal("DeployFailed"))
			}).WithContext(ctx).Should(Succeed())
		})
	})

	Context("WVA ManagementState lifecycle", Ordered, func() {
		var cr *platformv1alpha1.Kserve

		BeforeAll(func(ctx SpecContext) {
			cr = fixture.KserveCR()
			Expect(testEnv.Client.Create(ctx, cr)).To(Succeed())

			DeferCleanup(func(ctx SpecContext) {
				Expect(client.IgnoreNotFound(testEnv.Client.Delete(ctx, cr))).To(Succeed())
			})
		})

		BeforeEach(func() {
			testEnv.Deployer = &fixture.MockDeployer{}
			testEnv.Reconciler.Deployer = testEnv.Deployer
		})

		It("does not include WVA resources when ManagementState is Removed (default)", func(ctx SpecContext) {
			triggerReconcile(ctx, cr, "wva-default-removed")

			Eventually(func(g Gomega) {
				g.Expect(testEnv.Client.Get(ctx, client.ObjectKeyFromObject(cr), cr)).To(Succeed())
				cond := fixture.FindCondition(cr, string(common.ConditionTypeProvisioningSucceeded))
				g.Expect(cond).NotTo(BeNil())
				g.Expect(cond.Status).To(Equal(metav1.ConditionTrue))
			}).WithContext(ctx).Should(Succeed())

			lastCall := testEnv.Deployer.LastCall()
			Expect(lastCall).NotTo(BeNil())
			for _, res := range lastCall.Resources {
				Expect(res.GetName()).NotTo(Equal("workload-variant-autoscaler-controller-manager"),
					"WVA resources should not be deployed when Removed")
			}
		})

		It("includes WVA resources when ManagementState is Managed", func(ctx SpecContext) {
			err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
				if err := testEnv.Client.Get(ctx, client.ObjectKeyFromObject(cr), cr); err != nil {
					return err
				}
				cr.Spec.WVA.ManagementState = common.Managed
				return testEnv.Client.Update(ctx, cr)
			})
			Expect(err).NotTo(HaveOccurred())

			Eventually(func(g Gomega) {
				lastCall := testEnv.Deployer.LastCall()
				g.Expect(lastCall).NotTo(BeNil())

				hasWVA := false
				for _, res := range lastCall.Resources {
					if res.GetKind() == "Deployment" && res.GetName() == "workload-variant-autoscaler-controller-manager" {
						hasWVA = true
						break
					}
				}
				g.Expect(hasWVA).To(BeTrue(), "WVA Deployment should be in allResources when Managed")
			}).WithContext(ctx).Should(Succeed())
		})

		It("removes WVA resources when ManagementState changes to Removed", func(ctx SpecContext) {
			err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
				if err := testEnv.Client.Get(ctx, client.ObjectKeyFromObject(cr), cr); err != nil {
					return err
				}
				cr.Spec.WVA.ManagementState = common.Removed
				return testEnv.Client.Update(ctx, cr)
			})
			Expect(err).NotTo(HaveOccurred())

			Eventually(func(g Gomega) {
				lastCall := testEnv.Deployer.LastCall()
				g.Expect(lastCall).NotTo(BeNil())

				for _, res := range lastCall.Resources {
					g.Expect(res.GetName()).NotTo(Equal("workload-variant-autoscaler-controller-manager"),
						"WVA resources should not be in allResources after Removed")
				}
			}).WithContext(ctx).Should(Succeed())
		})
	})

	Context("WVA readiness condition", Ordered, func() {
		var cr *platformv1alpha1.Kserve

		BeforeAll(func(ctx SpecContext) {
			cr = fixture.KserveCR(fixture.WithWVAManagementState(common.Managed))
			Expect(testEnv.Client.Create(ctx, cr)).To(Succeed())

			DeferCleanup(func(ctx SpecContext) {
				Expect(client.IgnoreNotFound(testEnv.Client.Delete(ctx, cr))).To(Succeed())
			})
		})

		BeforeEach(func() {
			testEnv.Deployer = &fixture.MockDeployer{}
			testEnv.Reconciler.Deployer = testEnv.Deployer
		})

		It("reports WVAReady=False when WVA deployment is not available", func(ctx SpecContext) {
			triggerReconcile(ctx, cr, "wva-readiness-false")

			Eventually(func(g Gomega) {
				g.Expect(testEnv.Client.Get(ctx, client.ObjectKeyFromObject(cr), cr)).To(Succeed())
				cond := fixture.FindCondition(cr, "WVAReady")
				g.Expect(cond).NotTo(BeNil())
				g.Expect(cond.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(cond.Reason).To(Equal("DeploymentNotReady"))
			}).WithContext(ctx).Should(Succeed())
		})

		It("reports WVAReady=True when WVA deployment is available", func(ctx SpecContext) {
			createReadyDeployment(ctx, "workload-variant-autoscaler-controller-manager", "opendatahub")

			triggerReconcile(ctx, cr, "wva-readiness-true")

			Eventually(func(g Gomega) {
				g.Expect(testEnv.Client.Get(ctx, client.ObjectKeyFromObject(cr), cr)).To(Succeed())
				cond := fixture.FindCondition(cr, "WVAReady")
				g.Expect(cond).NotTo(BeNil())
				g.Expect(cond.Status).To(Equal(metav1.ConditionTrue))
				g.Expect(cond.Reason).To(Equal("AllDeploymentsAvailable"))
			}).WithContext(ctx).Should(Succeed())
		})

		It("clears WVAReady condition when WVA is disabled", func(ctx SpecContext) {
			err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
				if err := testEnv.Client.Get(ctx, client.ObjectKeyFromObject(cr), cr); err != nil {
					return err
				}
				cr.Spec.WVA.ManagementState = common.Removed
				return testEnv.Client.Update(ctx, cr)
			})
			Expect(err).NotTo(HaveOccurred())

			Eventually(func(g Gomega) {
				g.Expect(testEnv.Client.Get(ctx, client.ObjectKeyFromObject(cr), cr)).To(Succeed())
				cond := fixture.FindCondition(cr, "WVAReady")
				g.Expect(cond).To(BeNil(), "WVAReady condition should be cleared when WVA is disabled")
			}).WithContext(ctx).Should(Succeed())
		})
	})
})

func createReadyDeployment(ctx SpecContext, name, namespace string) {
	dep := fixture.ReadyDeployment(name, namespace)
	Expect(client.IgnoreAlreadyExists(testEnv.Client.Create(ctx, dep))).To(Succeed())
	DeferCleanup(func(ctx SpecContext) {
		Expect(client.IgnoreNotFound(testEnv.Client.Delete(ctx, dep))).To(Succeed())
	})
	dep.Status.AvailableReplicas = 1
	dep.Status.Replicas = 1
	dep.Status.ReadyReplicas = 1
	Expect(testEnv.Client.Status().Update(ctx, dep)).To(Succeed())
}

func triggerReconcile(ctx SpecContext, cr *platformv1alpha1.Kserve, trigger string) {
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := testEnv.Client.Get(ctx, client.ObjectKeyFromObject(cr), cr); err != nil {
			return err
		}
		if cr.Annotations == nil {
			cr.Annotations = map[string]string{}
		}
		cr.Annotations["test/trigger"] = trigger
		return testEnv.Client.Update(ctx, cr)
	})
	Expect(err).NotTo(HaveOccurred())
}
