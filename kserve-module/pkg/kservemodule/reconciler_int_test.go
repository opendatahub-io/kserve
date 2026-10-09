package kservemodule_test

import (
	"encoding/json"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	k8serr "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/opendatahub-io/odh-platform-utilities/api/common"
	"github.com/opendatahub-io/odh-platform-utilities/pkg/cluster"
	odhLabels "github.com/opendatahub-io/odh-platform-utilities/pkg/metadata/labels"

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
			deleteAndWaitGone(ctx, cr)
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
			// Real: assert operands actually land in the cluster. Set before Create so
			// the create-time reconcile uses it; Ordered keeps it for all specs.
			testEnv.Reconciler.Deployer = kservemodule.NewDeployer()

			cr = fixture.KserveCR()
			Expect(testEnv.Client.Create(ctx, cr)).To(Succeed())

			DeferCleanup(func(ctx SpecContext) {
				deleteAndWaitGone(ctx, cr)
			})
		})

		It("sets provisioning succeeded and applies the config to the cluster", func(ctx SpecContext) {
			Eventually(func(g Gomega) {
				g.Expect(testEnv.Client.Get(ctx, client.ObjectKeyFromObject(cr), cr)).To(Succeed())
				cond := fixture.FindCondition(cr, string(common.ConditionTypeProvisioningSucceeded))
				g.Expect(cond).NotTo(BeNil())
				g.Expect(cond.Status).To(Equal(metav1.ConditionTrue))
			}).WithContext(ctx).Should(Succeed())

			// The inferenceservice-config ConfigMap is really created (not just intended).
			cm := &corev1.ConfigMap{}
			Expect(testEnv.Client.Get(ctx,
				client.ObjectKey{Name: "inferenceservice-config", Namespace: "opendatahub"}, cm)).To(Succeed())
		})

		It("reports ready with all OCP deployments", func(ctx SpecContext) {
			testEnv.Reconciler.SetClusterType(cluster.ClusterTypeOpenShift)

			deployments := []string{
				"kserve-controller-manager",
				"llmisvc-controller-manager",
				"odh-model-controller",
				"model-serving-api",
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

			deployments := []string{
				"llmisvc-controller-manager",
				"odh-model-controller",
			}
			for _, name := range deployments {
				createReadyDeployment(ctx, name, "opendatahub")
			}

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

				modelCtrlReady := fixture.FindCondition(cr, kservemodule.ConditionModelControllerReady)
				g.Expect(modelCtrlReady).NotTo(BeNil())
				g.Expect(modelCtrlReady.Status).To(Equal(metav1.ConditionTrue))
			}).WithContext(ctx).Should(Succeed())
		})

	})

	Context("deploy failure", func() {
		It("sets provisioning failed when deployer returns error", func(ctx SpecContext) {
			// Mock: fault injection (DeployError); the real deployer can't be told to fail.
			testEnv.Reconciler.Deployer = &fixture.MockDeployer{DeployError: fmt.Errorf("simulated deploy failure")}

			cr := fixture.KserveCR()
			Expect(testEnv.Client.Create(ctx, cr)).To(Succeed())
			DeferCleanup(func(ctx SpecContext) {
				deleteAndWaitGone(ctx, cr)
			})

			Eventually(func(g Gomega) {
				g.Expect(testEnv.Client.Get(ctx, client.ObjectKeyFromObject(cr), cr)).To(Succeed())
				cond := fixture.FindCondition(cr, string(common.ConditionTypeProvisioningSucceeded))
				g.Expect(cond).NotTo(BeNil())
				g.Expect(cond.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(cond.Reason).To(Equal("DeployFailed"))
			}).WithContext(ctx).Should(Succeed())
		})
	})

	// WVA is removed from the product in RHOAI 3.6 (RHAISTRAT-2756).
	// isWVAEnabled always returns false. The WVA componentConfig is still
	// registered so defaultCleanup tears down leftovers on 3.5→3.6 upgrade.
	// These tests verify that Managed is ignored and leftovers are deleted.
	Context("WVA always disabled (RHOAI 3.6 removal)", Ordered, func() {
		var cr *platformv1alpha1.Kserve
		wvaKey := client.ObjectKey{Name: "workload-variant-autoscaler-controller-manager", Namespace: "opendatahub"}
		// Applied from the WVA rendered set (see fixture.WriteMinimalManifests).
		wvaCRDKey := client.ObjectKey{Name: "wvatestresources.test.kserve.io"}

		BeforeAll(func(ctx SpecContext) {
			// Real deployer so assertions check actual cluster state.
			testEnv.Reconciler.Deployer = kservemodule.NewDeployer()

			cr = fixture.KserveCR()
			Expect(testEnv.Client.Create(ctx, cr)).To(Succeed())

			DeferCleanup(func(ctx SpecContext) {
				deleteAndWaitGone(ctx, cr)
			})
		})

		It("does not create the WVA Deployment when ManagementState is Removed (default)", func(ctx SpecContext) {
			triggerReconcile(ctx, cr, "wva-default-removed")

			Eventually(func(g Gomega) {
				g.Expect(testEnv.Client.Get(ctx, client.ObjectKeyFromObject(cr), cr)).To(Succeed())
				cond := fixture.FindCondition(cr, string(common.ConditionTypeProvisioningSucceeded))
				g.Expect(cond).NotTo(BeNil())
				g.Expect(cond.Status).To(Equal(metav1.ConditionTrue))
			}).WithContext(ctx).Should(Succeed())

			err := testEnv.Client.Get(ctx, wvaKey, &appsv1.Deployment{})
			Expect(k8serr.IsNotFound(err)).To(BeTrue(), "WVA Deployment should not exist when Removed")
		})

		It("does not create the WVA Deployment even when ManagementState is Managed", func(ctx SpecContext) {
			err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
				if err := testEnv.Client.Get(ctx, client.ObjectKeyFromObject(cr), cr); err != nil {
					return err
				}
				cr.Spec.WVA.ManagementState = common.Managed
				return testEnv.Client.Update(ctx, cr)
			})
			Expect(err).NotTo(HaveOccurred())

			triggerReconcile(ctx, cr, "wva-managed-ignored")

			Eventually(func(g Gomega) {
				g.Expect(testEnv.Client.Get(ctx, client.ObjectKeyFromObject(cr), cr)).To(Succeed())
				cond := fixture.FindCondition(cr, string(common.ConditionTypeProvisioningSucceeded))
				g.Expect(cond).NotTo(BeNil())
				g.Expect(cond.Status).To(Equal(metav1.ConditionTrue))
			}).WithContext(ctx).Should(Succeed())

			err = testEnv.Client.Get(ctx, wvaKey, &appsv1.Deployment{})
			Expect(k8serr.IsNotFound(err)).To(BeTrue(),
				"WVA Deployment must not be created even when ManagementState is Managed (WVA removed in 3.6)")
		})

		It("deletes a leftover WVA Deployment via defaultCleanup on upgrade", func(ctx SpecContext) {
			// Simulate a 3.5 leftover: manually create the WVA Deployment and the
			// overlay CRD. defaultCleanup must delete the Deployment and skip the CRD.
			leftover := &appsv1.Deployment{}
			leftover.Name = wvaKey.Name
			leftover.Namespace = wvaKey.Namespace
			leftover.Labels = map[string]string{odhLabels.PlatformPartOf: kservemodule.KserveComponentName}
			leftover.Spec.Selector = &metav1.LabelSelector{
				MatchLabels: map[string]string{"control-plane": wvaKey.Name},
			}
			leftover.Spec.Template.Labels = map[string]string{"control-plane": wvaKey.Name}
			leftover.Spec.Template.Spec.Containers = []corev1.Container{
				{Name: "manager", Image: "ghcr.io/llm-d/llm-d-workload-variant-autoscaler:latest"},
			}
			Expect(testEnv.Client.Create(ctx, leftover)).To(Succeed())

			crd := fixture.CreateCRDByName(ctx, testEnv.Client, wvaCRDKey.Name, "test.kserve.io", "v1",
				apiextensionsv1.NamespaceScoped)
			DeferCleanup(func(ctx SpecContext) {
				Expect(client.IgnoreNotFound(testEnv.Client.Delete(ctx, crd))).To(Succeed())
			})

			triggerReconcile(ctx, cr, "wva-leftover-cleanup")

			Eventually(func(g Gomega) {
				err := testEnv.Client.Get(ctx, wvaKey, &appsv1.Deployment{})
				g.Expect(k8serr.IsNotFound(err)).To(BeTrue(),
					"Leftover WVA Deployment should be deleted by defaultCleanup on 3.5→3.6 upgrade")
			}).WithContext(ctx).Should(Succeed())

			Expect(testEnv.Client.Get(ctx, wvaCRDKey, &apiextensionsv1.CustomResourceDefinition{})).To(Succeed(),
				"defaultCleanup must skip CRDs so leftover WVA CRDs survive")
		})
	})

	Context("WVA readiness condition always cleared (WVA removed in 3.6)", Ordered, func() {
		var cr *platformv1alpha1.Kserve

		BeforeAll(func(ctx SpecContext) {
			testEnv.Reconciler.Deployer = &fixture.MockDeployer{}

			// Even with Managed, isWVAEnabled returns false in 3.6.
			cr = fixture.KserveCR(fixture.WithWVAManagementState(common.Managed))
			Expect(testEnv.Client.Create(ctx, cr)).To(Succeed())

			DeferCleanup(func(ctx SpecContext) {
				deleteAndWaitGone(ctx, cr)
			})
		})

		It("clears WVAReady condition even when ManagementState is Managed", func(ctx SpecContext) {
			Eventually(func(g Gomega) {
				g.Expect(testEnv.Client.Get(ctx, client.ObjectKeyFromObject(cr), cr)).To(Succeed())
				cond := fixture.FindCondition(cr, string(common.ConditionTypeProvisioningSucceeded))
				g.Expect(cond).NotTo(BeNil())
				g.Expect(cond.Status).To(Equal(metav1.ConditionTrue))
			}).WithContext(ctx).Should(Succeed())

			err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
				if err := testEnv.Client.Get(ctx, client.ObjectKeyFromObject(cr), cr); err != nil {
					return err
				}
				if fixture.FindCondition(cr, kservemodule.ConditionWVAReady) != nil {
					return nil
				}
				cr.Status.Conditions = append(cr.Status.Conditions, common.Condition{
					Type:               kservemodule.ConditionWVAReady,
					Status:             metav1.ConditionTrue,
					Reason:             "AllDeploymentsAvailable",
					LastTransitionTime: metav1.Now(),
				})
				return testEnv.Client.Status().Update(ctx, cr)
			})
			Expect(err).NotTo(HaveOccurred())

			Eventually(func(g Gomega) {
				g.Expect(testEnv.Client.Get(ctx, client.ObjectKeyFromObject(cr), cr)).To(Succeed())
				g.Expect(fixture.FindCondition(cr, kservemodule.ConditionWVAReady)).NotTo(BeNil(),
					"seeded WVAReady condition must be present before reconcile")
			}).WithContext(ctx).Should(Succeed())

			triggerReconcile(ctx, cr, "wva-readiness-always-cleared")

			Eventually(func(g Gomega) {
				g.Expect(testEnv.Client.Get(ctx, client.ObjectKeyFromObject(cr), cr)).To(Succeed())
				cond := fixture.FindCondition(cr, kservemodule.ConditionWVAReady)
				g.Expect(cond).To(BeNil(), "WVAReady condition should be cleared because WVA is always disabled in 3.6")
			}).WithContext(ctx).Should(Succeed())
		})
	})

	Context("ModelExpress ManagementState lifecycle", Ordered, func() {
		var cr *platformv1alpha1.Kserve
		mxKey := client.ObjectKey{Name: "modelexpress-operator", Namespace: "opendatahub"}
		mxCRDKey := client.ObjectKey{Name: "modelexpressservers.modelexpress.opendatahub.io"}

		BeforeAll(func(ctx SpecContext) {
			testEnv.Reconciler.Deployer = kservemodule.NewDeployer()

			cr = fixture.KserveCR()
			Expect(testEnv.Client.Create(ctx, cr)).To(Succeed())

			DeferCleanup(func(ctx SpecContext) {
				deleteAndWaitGone(ctx, cr)
			})
		})

		It("does not create the ModelExpress Deployment when ManagementState is Removed (default)", func(ctx SpecContext) {
			triggerReconcile(ctx, cr, "modelexpress-default-removed")

			Eventually(func(g Gomega) {
				g.Expect(testEnv.Client.Get(ctx, client.ObjectKeyFromObject(cr), cr)).To(Succeed())
				cond := fixture.FindCondition(cr, string(common.ConditionTypeProvisioningSucceeded))
				g.Expect(cond).NotTo(BeNil())
				g.Expect(cond.Status).To(Equal(metav1.ConditionTrue))
			}).WithContext(ctx).Should(Succeed())

			err := testEnv.Client.Get(ctx, mxKey, &appsv1.Deployment{})
			Expect(k8serr.IsNotFound(err)).To(BeTrue(), "ModelExpress Deployment should not exist when Removed")
		})

		It("creates the ModelExpress Deployment when ManagementState is Managed", func(ctx SpecContext) {
			err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
				if err := testEnv.Client.Get(ctx, client.ObjectKeyFromObject(cr), cr); err != nil {
					return err
				}
				cr.Spec.ModelExpress.ManagementState = common.Managed
				return testEnv.Client.Update(ctx, cr)
			})
			Expect(err).NotTo(HaveOccurred())

			Eventually(func(g Gomega) {
				g.Expect(testEnv.Client.Get(ctx, mxKey, &appsv1.Deployment{})).To(Succeed(),
					"ModelExpress Deployment should be applied to the cluster when Managed")
			}).WithContext(ctx).Should(Succeed())

			Eventually(func(g Gomega) {
				crd := &apiextensionsv1.CustomResourceDefinition{}
				g.Expect(testEnv.Client.Get(ctx, mxCRDKey, crd)).To(Succeed(),
					"ModelExpress CRD should be applied to the cluster when Managed")
				for _, ref := range crd.GetOwnerReferences() {
					g.Expect(ref.Kind).NotTo(Equal("Kserve"),
						"ModelExpress CRD must not be owned by the Kserve CR (would cause GC cascade-delete on CR removal)")
				}
			}).WithContext(ctx).Should(Succeed())
		})

		It("deletes the ModelExpress Deployment but preserves the CRD when ManagementState changes to Removed", func(ctx SpecContext) {
			Expect(testEnv.Client.Get(ctx, mxKey, &appsv1.Deployment{})).To(Succeed())
			Expect(testEnv.Client.Get(ctx, mxCRDKey, &apiextensionsv1.CustomResourceDefinition{})).To(Succeed())

			err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
				if err := testEnv.Client.Get(ctx, client.ObjectKeyFromObject(cr), cr); err != nil {
					return err
				}
				cr.Spec.ModelExpress.ManagementState = common.Removed
				return testEnv.Client.Update(ctx, cr)
			})
			Expect(err).NotTo(HaveOccurred())

			Eventually(func(g Gomega) {
				err := testEnv.Client.Get(ctx, mxKey, &appsv1.Deployment{})
				g.Expect(k8serr.IsNotFound(err)).To(BeTrue(),
					"ModelExpress Deployment should be deleted by defaultCleanup when Removed")
			}).WithContext(ctx).Should(Succeed())

			Consistently(func(g Gomega) {
				g.Expect(testEnv.Client.Get(ctx, mxCRDKey, &apiextensionsv1.CustomResourceDefinition{})).To(Succeed(),
					"ModelExpress CRD must be preserved by defaultCleanup when Removed")
			}).WithContext(ctx).WithTimeout(3 * time.Second).Should(Succeed())
		})
	})

	Context("ModelExpress readiness condition", Ordered, func() {
		var cr *platformv1alpha1.Kserve

		BeforeAll(func(ctx SpecContext) {
			testEnv.Reconciler.Deployer = &fixture.MockDeployer{}

			cr = fixture.KserveCR(fixture.WithModelExpressManagementState(common.Managed))
			Expect(testEnv.Client.Create(ctx, cr)).To(Succeed())

			DeferCleanup(func(ctx SpecContext) {
				deleteAndWaitGone(ctx, cr)
			})
		})

		It("reports ModelExpressReady=False when ModelExpress deployment is not available", func(ctx SpecContext) {
			triggerReconcile(ctx, cr, "modelexpress-readiness-false")

			Eventually(func(g Gomega) {
				g.Expect(testEnv.Client.Get(ctx, client.ObjectKeyFromObject(cr), cr)).To(Succeed())
				cond := fixture.FindCondition(cr, kservemodule.ConditionModelExpressReady)
				g.Expect(cond).NotTo(BeNil())
				g.Expect(cond.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(cond.Reason).To(Equal("DeploymentNotReady"))
			}).WithContext(ctx).Should(Succeed())
		})

		It("reports ModelExpressReady=True when ModelExpress deployment is available", func(ctx SpecContext) {
			createReadyDeployment(ctx, "modelexpress-operator", "opendatahub")

			triggerReconcile(ctx, cr, "modelexpress-readiness-true")

			Eventually(func(g Gomega) {
				g.Expect(testEnv.Client.Get(ctx, client.ObjectKeyFromObject(cr), cr)).To(Succeed())
				cond := fixture.FindCondition(cr, kservemodule.ConditionModelExpressReady)
				g.Expect(cond).NotTo(BeNil())
				g.Expect(cond.Status).To(Equal(metav1.ConditionTrue))
				g.Expect(cond.Reason).To(Equal("AllDeploymentsAvailable"))
			}).WithContext(ctx).Should(Succeed())
		})

		It("clears ModelExpressReady condition when ModelExpress is disabled", func(ctx SpecContext) {
			err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
				if err := testEnv.Client.Get(ctx, client.ObjectKeyFromObject(cr), cr); err != nil {
					return err
				}
				cr.Spec.ModelExpress.ManagementState = common.Removed
				return testEnv.Client.Update(ctx, cr)
			})
			Expect(err).NotTo(HaveOccurred())

			Eventually(func(g Gomega) {
				g.Expect(testEnv.Client.Get(ctx, client.ObjectKeyFromObject(cr), cr)).To(Succeed())
				cond := fixture.FindCondition(cr, kservemodule.ConditionModelExpressReady)
				g.Expect(cond).To(BeNil(), "ModelExpressReady condition should be cleared when ModelExpress is disabled")
			}).WithContext(ctx).Should(Succeed())
		})
	})

	Context("ModelExpress removal with finalizer-holding ModelExpressServers", Ordered, func() {
		var (
			cr  *platformv1alpha1.Kserve
			mxs *unstructured.Unstructured
		)
		mxKey := client.ObjectKey{Name: "modelexpress-operator", Namespace: "opendatahub"}

		BeforeAll(func(ctx SpecContext) {
			testEnv.Reconciler.Deployer = kservemodule.NewDeployer()

			cr = fixture.KserveCR(fixture.WithModelExpressManagementState(common.Managed))
			Expect(testEnv.Client.Create(ctx, cr)).To(Succeed())
			DeferCleanup(func(ctx SpecContext) {
				deleteAndWaitGone(ctx, cr)
			})

			Eventually(func(g Gomega) {
				g.Expect(testEnv.Client.Get(ctx, mxKey, &appsv1.Deployment{})).To(Succeed())
			}).WithContext(ctx).Should(Succeed())
			waitForCRDEstablished(ctx, "modelexpressservers.modelexpress.opendatahub.io")

			mxs = createModelExpressServer(ctx, "mx-removal-blocked", "enforced")
			DeferCleanup(func(ctx SpecContext) {
				releaseModelExpressServer(ctx, mxs)
			})
		})

		It("keeps the operator deployed and reports RemovalBlocked while the finalizer is held", func(ctx SpecContext) {
			setModelExpressState(ctx, cr, common.Removed)

			Eventually(func(g Gomega) {
				g.Expect(testEnv.Client.Get(ctx, client.ObjectKeyFromObject(cr), cr)).To(Succeed())
				cond := fixture.FindCondition(cr, kservemodule.ConditionModelExpressReady)
				g.Expect(cond).NotTo(BeNil())
				g.Expect(cond.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(cond.Reason).To(Equal(kservemodule.ReasonRemovalBlocked))
				g.Expect(cond.Message).To(ContainSubstring("mx-removal-blocked/enforced"))
				ready := fixture.FindCondition(cr, string(common.ConditionTypeReady))
				g.Expect(ready).NotTo(BeNil())
				g.Expect(ready.Status).To(Equal(metav1.ConditionFalse))
			}).WithContext(ctx).Should(Succeed())

			Consistently(func(g Gomega) {
				g.Expect(testEnv.Client.Get(ctx, mxKey, &appsv1.Deployment{})).To(Succeed(),
					"operator Deployment must stay while a ModelExpressServer holds its finalizer")
			}).WithContext(ctx).WithTimeout(3 * time.Second).Should(Succeed())
		})

		It("stays blocked while the ModelExpressServer is terminating", func(ctx SpecContext) {
			Expect(testEnv.Client.Delete(ctx, mxs)).To(Succeed())
			triggerReconcile(ctx, cr, "modelexpress-removal-terminating")

			Consistently(func(g Gomega) {
				g.Expect(testEnv.Client.Get(ctx, mxKey, &appsv1.Deployment{})).To(Succeed(),
					"operator Deployment must stay until it has released the terminating ModelExpressServer")
			}).WithContext(ctx).WithTimeout(3 * time.Second).Should(Succeed())
		})

		It("removes the operator and clears ModelExpressReady once the finalizer is released", func(ctx SpecContext) {
			releaseModelExpressServer(ctx, mxs)
			triggerReconcile(ctx, cr, "modelexpress-removal-released")

			Eventually(func(g Gomega) {
				err := testEnv.Client.Get(ctx, mxKey, &appsv1.Deployment{})
				g.Expect(k8serr.IsNotFound(err)).To(BeTrue(), "operator Deployment should be removed once unblocked")
				g.Expect(testEnv.Client.Get(ctx, client.ObjectKeyFromObject(cr), cr)).To(Succeed())
				g.Expect(fixture.FindCondition(cr, kservemodule.ConditionModelExpressReady)).To(BeNil())
			}).WithContext(ctx).Should(Succeed())
		})
	})

	Context("Kserve CR deletion with finalizer-holding ModelExpressServers", Ordered, func() {
		var (
			cr  *platformv1alpha1.Kserve
			mxs *unstructured.Unstructured
		)
		mxKey := client.ObjectKey{Name: "modelexpress-operator", Namespace: "opendatahub"}

		BeforeAll(func(ctx SpecContext) {
			testEnv.Reconciler.Deployer = kservemodule.NewDeployer()

			cr = fixture.KserveCR(fixture.WithModelExpressManagementState(common.Managed))
			Expect(testEnv.Client.Create(ctx, cr)).To(Succeed())
			DeferCleanup(func(ctx SpecContext) {
				deleteAndWaitGone(ctx, cr)
			})

			Eventually(func(g Gomega) {
				g.Expect(testEnv.Client.Get(ctx, mxKey, &appsv1.Deployment{})).To(Succeed())
			}).WithContext(ctx).Should(Succeed())
			waitForCRDEstablished(ctx, "modelexpressservers.modelexpress.opendatahub.io")

			mxs = createModelExpressServer(ctx, "mx-deletion-blocked", "enforced")
			DeferCleanup(func(ctx SpecContext) {
				releaseModelExpressServer(ctx, mxs)
			})
		})

		It("holds the Kserve CR and reports DeletionBlocked while the finalizer is held", func(ctx SpecContext) {
			Expect(testEnv.Client.Delete(ctx, cr)).To(Succeed())

			Eventually(func(g Gomega) {
				g.Expect(testEnv.Client.Get(ctx, client.ObjectKeyFromObject(cr), cr)).To(Succeed())
				g.Expect(cr.DeletionTimestamp.IsZero()).To(BeFalse())
				cond := fixture.FindCondition(cr, string(common.ConditionTypeDegraded))
				g.Expect(cond).NotTo(BeNil())
				g.Expect(cond.Reason).To(Equal(kservemodule.ReasonDeletionBlocked))
				g.Expect(cond.Message).To(ContainSubstring("modelexpress: mx-deletion-blocked/enforced"))
			}).WithContext(ctx).Should(Succeed())

			Consistently(func(g Gomega) {
				g.Expect(testEnv.Client.Get(ctx, client.ObjectKeyFromObject(cr), &platformv1alpha1.Kserve{})).To(Succeed(),
					"Kserve CR must stay while a ModelExpressServer holds the operator's finalizer")
				g.Expect(testEnv.Client.Get(ctx, mxKey, &appsv1.Deployment{})).To(Succeed(),
					"operator Deployment must not be torn down while deletion is blocked")
			}).WithContext(ctx).WithTimeout(3 * time.Second).Should(Succeed())
		})

		It("finishes deleting the Kserve CR once the finalizer is released", func(ctx SpecContext) {
			releaseModelExpressServer(ctx, mxs)
			triggerReconcile(ctx, cr, "modelexpress-deletion-released")

			Eventually(func(g Gomega) {
				err := testEnv.Client.Get(ctx, client.ObjectKeyFromObject(cr), &platformv1alpha1.Kserve{})
				g.Expect(k8serr.IsNotFound(err)).To(BeTrue(), "Kserve CR should be deleted once unblocked")
			}).WithContext(ctx).Should(Succeed())
		})
	})

	Context("console dashboards lifecycle", Ordered, func() {
		var cr *platformv1alpha1.Kserve

		BeforeAll(func(ctx SpecContext) {
			// Mock: assert deploy-set intent via LastCall; console removal is GC-based,
			// not observable in envtest. Set before Create; Ordered keeps it for all specs.
			testEnv.Reconciler.Deployer = &fixture.MockDeployer{}

			cr = fixture.KserveCR()
			Expect(testEnv.Client.Create(ctx, cr)).To(Succeed())

			DeferCleanup(func(ctx SpecContext) {
				deleteAndWaitGone(ctx, cr)
			})
		})

		It("does not include console dashboard resources when namespace does not exist", func(ctx SpecContext) {
			triggerReconcile(ctx, cr, "console-dashboards-no-ns")

			Eventually(func(g Gomega) {
				g.Expect(testEnv.Client.Get(ctx, client.ObjectKeyFromObject(cr), cr)).To(Succeed())
				cond := fixture.FindCondition(cr, string(common.ConditionTypeProvisioningSucceeded))
				g.Expect(cond).NotTo(BeNil())
				g.Expect(cond.Status).To(Equal(metav1.ConditionTrue))
			}).WithContext(ctx).Should(Succeed())

			lastCall := mockDeployer().LastCall()
			Expect(lastCall).NotTo(BeNil())
			for _, res := range lastCall.Resources {
				Expect(res.GetName()).NotTo(Equal("model-serving-llms-cluster-health"),
					"console dashboard ConfigMaps should not be deployed when namespace does not exist")
			}
		})

		It("includes console dashboard resources when namespace exists", func(ctx SpecContext) {
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "openshift-config-managed"}}
			Expect(testEnv.Client.Create(ctx, ns)).To(Succeed())
			DeferCleanup(func(ctx SpecContext) {
				Expect(client.IgnoreNotFound(testEnv.Client.Delete(ctx, ns))).To(Succeed())
			})

			triggerReconcile(ctx, cr, "console-dashboards-with-ns")

			Eventually(func(g Gomega) {
				lastCall := mockDeployer().LastCall()
				g.Expect(lastCall).NotTo(BeNil())

				hasDashboard := false
				for _, res := range lastCall.Resources {
					if res.GetKind() == "ConfigMap" && res.GetName() == "model-serving-llms-cluster-health" {
						g.Expect(res.GetNamespace()).To(Equal("openshift-config-managed"))
						hasDashboard = true
						break
					}
				}
				g.Expect(hasDashboard).To(BeTrue(), "console dashboard ConfigMap should be in deployed resources")
			}).WithContext(ctx).Should(Succeed())
		})

		It("does not include console dashboard resources when explicitly disabled", func(ctx SpecContext) {
			err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
				if err := testEnv.Client.Get(ctx, client.ObjectKeyFromObject(cr), cr); err != nil {
					return err
				}
				cr.Spec.EnableLLMInferenceServiceConsoleDashboards = ptr.To(false)
				return testEnv.Client.Update(ctx, cr)
			})
			Expect(err).NotTo(HaveOccurred())

			Eventually(func(g Gomega) {
				lastCall := mockDeployer().LastCall()
				g.Expect(lastCall).NotTo(BeNil())

				for _, res := range lastCall.Resources {
					g.Expect(res.GetName()).NotTo(Equal("model-serving-llms-cluster-health"),
						"console dashboard ConfigMaps should not be deployed when explicitly disabled")
				}
			}).WithContext(ctx).Should(Succeed())
		})

		It("re-enables console dashboard resources when flag is set back to true", func(ctx SpecContext) {
			err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
				if err := testEnv.Client.Get(ctx, client.ObjectKeyFromObject(cr), cr); err != nil {
					return err
				}
				cr.Spec.EnableLLMInferenceServiceConsoleDashboards = ptr.To(true)
				return testEnv.Client.Update(ctx, cr)
			})
			Expect(err).NotTo(HaveOccurred())

			Eventually(func(g Gomega) {
				lastCall := mockDeployer().LastCall()
				g.Expect(lastCall).NotTo(BeNil())

				hasDashboard := false
				for _, res := range lastCall.Resources {
					if res.GetKind() == "ConfigMap" && res.GetName() == "model-serving-llms-cluster-health" {
						g.Expect(res.GetNamespace()).To(Equal("openshift-config-managed"))
						hasDashboard = true
						break
					}
				}
				g.Expect(hasDashboard).To(BeTrue(), "console dashboard ConfigMap should be deployed after re-enabling")
			}).WithContext(ctx).Should(Succeed())
		})
	})

	Context("module finalizer lifecycle", func() {
		It("adds finalizer during reconcile and removes it on deletion after cleanup", func(ctx SpecContext) {
			cr := fixture.KserveCR()
			Expect(testEnv.Client.Create(ctx, cr)).To(Succeed())

			Eventually(func(g Gomega) {
				g.Expect(testEnv.Client.Get(ctx, client.ObjectKeyFromObject(cr), cr)).To(Succeed())
				g.Expect(cr.Status.ObservedGeneration).To(Equal(cr.Generation))
			}).WithContext(ctx).WithTimeout(30 * time.Second).Should(Succeed())

			Expect(cr.Finalizers).To(ContainElement(kservemodule.ModuleFinalizerName),
				"module operator should add its own finalizer during reconcile")

			Expect(testEnv.Client.Delete(ctx, cr)).To(Succeed())

			Eventually(func(g Gomega) {
				err := testEnv.Client.Get(ctx, client.ObjectKeyFromObject(cr), cr)
				g.Expect(k8serr.IsNotFound(err)).To(BeTrue(), "CR should be deleted after module finalizer is removed")
			}).WithContext(ctx).WithTimeout(30 * time.Second).Should(Succeed())
		})
	})

	Context("oauthProxy configuration", Ordered, func() {
		var cr *platformv1alpha1.Kserve

		BeforeAll(func(ctx SpecContext) {
			// Real: assert oauthProxy projection lands in the actual ConfigMap. Set
			// before Create so the create-time reconcile uses it; Ordered keeps it for all specs.
			testEnv.Reconciler.Deployer = kservemodule.NewDeployer()

			cr = fixture.KserveCR()
			Expect(testEnv.Client.Create(ctx, cr)).To(Succeed())

			DeferCleanup(func(ctx SpecContext) {
				deleteAndWaitGone(ctx, cr)
			})
		})

		It("overrides oauthProxy on patch and restores defaults on removal", func(ctx SpecContext) {
			triggerReconcile(ctx, cr, "oauth-proxy-default")

			By("defaults are applied to the ConfigMap")
			Eventually(func(g Gomega) {
				d := oauthProxyFromConfigMap(ctx, g)
				g.Expect(d["memoryRequest"]).To(Equal("64Mi"))
				g.Expect(d["memoryLimit"]).To(Equal("128Mi"))
				g.Expect(d["cpuRequest"]).To(Equal("100m"))
				g.Expect(d["cpuLimit"]).To(Equal("200m"))
				g.Expect(d["image"]).To(Equal("registry.example.com/oauth-proxy:latest"))
			}).WithContext(ctx).Should(Succeed())

			By("patching CR with oauthProxy overrides")
			err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
				if err := testEnv.Client.Get(ctx, client.ObjectKeyFromObject(cr), cr); err != nil {
					return err
				}
				cr.Spec.OAuthProxy = &platformv1alpha1.OAuthProxyConfig{
					Resources: &platformv1alpha1.OAuthProxyResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceMemory: resource.MustParse("256Mi"),
							corev1.ResourceCPU:    resource.MustParse("200m"),
						},
						Limits: corev1.ResourceList{
							corev1.ResourceMemory: resource.MustParse("512Mi"),
							corev1.ResourceCPU:    resource.MustParse("500m"),
						},
					},
				}
				return testEnv.Client.Update(ctx, cr)
			})
			Expect(err).NotTo(HaveOccurred())

			Eventually(func(g Gomega) {
				d := oauthProxyFromConfigMap(ctx, g)
				g.Expect(d["memoryRequest"]).To(Equal("256Mi"))
				g.Expect(d["memoryLimit"]).To(Equal("512Mi"))
				g.Expect(d["cpuRequest"]).To(Equal("200m"))
				g.Expect(d["cpuLimit"]).To(Equal("500m"))
				g.Expect(d["image"]).To(Equal("registry.example.com/oauth-proxy:latest"))
			}).WithContext(ctx).Should(Succeed())

			By("removing oauthProxy from CR restores defaults")
			err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
				if err := testEnv.Client.Get(ctx, client.ObjectKeyFromObject(cr), cr); err != nil {
					return err
				}
				cr.Spec.OAuthProxy = nil
				return testEnv.Client.Update(ctx, cr)
			})
			Expect(err).NotTo(HaveOccurred())

			Eventually(func(g Gomega) {
				d := oauthProxyFromConfigMap(ctx, g)
				g.Expect(d["memoryRequest"]).To(Equal("64Mi"))
				g.Expect(d["memoryLimit"]).To(Equal("128Mi"))
				g.Expect(d["cpuRequest"]).To(Equal("100m"))
				g.Expect(d["cpuLimit"]).To(Equal("200m"))
				g.Expect(d["image"]).To(Equal("registry.example.com/oauth-proxy:latest"))
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

// oauthProxyFromConfigMap reads and parses the oauthProxy JSON block from the
// real inferenceservice-config ConfigMap in the cluster.
func oauthProxyFromConfigMap(ctx SpecContext, g Gomega) map[string]any {
	cm := &corev1.ConfigMap{}
	g.Expect(testEnv.Client.Get(ctx,
		client.ObjectKey{Name: "inferenceservice-config", Namespace: "opendatahub"}, cm)).To(Succeed())
	raw, ok := cm.Data["oauthProxy"]
	g.Expect(ok).To(BeTrue(), "inferenceservice-config should contain oauthProxy data")
	var data map[string]any
	g.Expect(json.Unmarshal([]byte(raw), &data)).To(Succeed())
	return data
}

// mockDeployer returns the reconciler's deployer as a *MockDeployer, failing the
// spec if it isn't one (i.e. this context didn't set the mock).
func mockDeployer() *fixture.MockDeployer {
	m, ok := testEnv.Reconciler.Deployer.(*fixture.MockDeployer)
	Expect(ok).To(BeTrue(), "expected Reconciler.Deployer to be *MockDeployer; did this context set it?")
	return m
}

func deleteAndWaitGone(ctx SpecContext, obj client.Object) {
	Expect(client.IgnoreNotFound(testEnv.Client.Delete(ctx, obj))).To(Succeed())
	Eventually(func(g Gomega) {
		err := testEnv.Client.Get(ctx, client.ObjectKeyFromObject(obj), obj)
		g.Expect(k8serr.IsNotFound(err)).To(BeTrue(),
			"waiting for %s %s to be fully deleted", obj.GetObjectKind().GroupVersionKind().Kind, obj.GetName())
	}).WithContext(ctx).WithTimeout(30 * time.Second).Should(Succeed())
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

func setModelExpressState(ctx SpecContext, cr *platformv1alpha1.Kserve, state common.ManagementState) {
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := testEnv.Client.Get(ctx, client.ObjectKeyFromObject(cr), cr); err != nil {
			return err
		}
		cr.Spec.ModelExpress.ManagementState = state
		return testEnv.Client.Update(ctx, cr)
	})
	Expect(err).NotTo(HaveOccurred())
}

func waitForCRDEstablished(ctx SpecContext, name string) {
	Eventually(func(g Gomega) {
		crd := &apiextensionsv1.CustomResourceDefinition{}
		g.Expect(testEnv.Client.Get(ctx, client.ObjectKey{Name: name}, crd)).To(Succeed())
		established := false
		for _, c := range crd.Status.Conditions {
			if c.Type == apiextensionsv1.Established && c.Status == apiextensionsv1.ConditionTrue {
				established = true
			}
		}
		g.Expect(established).To(BeTrue(), "CRD %s should be Established", name)
	}).WithContext(ctx).Should(Succeed())
}

func createModelExpressServer(ctx SpecContext, namespace, name string) *unstructured.Unstructured {
	Expect(client.IgnoreAlreadyExists(testEnv.Client.Create(ctx,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}))).To(Succeed())

	mxs := &unstructured.Unstructured{}
	mxs.SetAPIVersion("modelexpress.opendatahub.io/v1alpha1")
	mxs.SetKind("ModelExpressServer")
	mxs.SetNamespace(namespace)
	mxs.SetName(name)
	mxs.SetFinalizers([]string{"modelexpress.opendatahub.io/auth-delegator"})
	Eventually(func() error {
		return testEnv.Client.Create(ctx, mxs)
	}).WithContext(ctx).Should(Succeed())
	return mxs
}

func releaseModelExpressServer(ctx SpecContext, mxs *unstructured.Unstructured) {
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := testEnv.Client.Get(ctx, client.ObjectKeyFromObject(mxs), mxs); err != nil {
			return err
		}
		mxs.SetFinalizers(nil)
		return testEnv.Client.Update(ctx, mxs)
	})
	Expect(client.IgnoreNotFound(err)).To(Succeed())
	deleteAndWaitGone(ctx, mxs)
}
