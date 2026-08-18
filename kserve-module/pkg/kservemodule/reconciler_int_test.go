package kservemodule_test

import (
	"encoding/json"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8serr "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
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

	// Uses the real deployer so assertions check actual cluster state: the WVA
	// Deployment is really applied when Managed and really deleted (via
	// defaultCleanup, not GC) when Removed. envtest has no garbage collector, so
	// only defaultCleanup-based removal is observable here.
	Context("WVA ManagementState lifecycle", Ordered, func() {
		var cr *platformv1alpha1.Kserve
		wvaKey := client.ObjectKey{Name: "workload-variant-autoscaler-controller-manager", Namespace: "opendatahub"}

		BeforeAll(func(ctx SpecContext) {
			// Real: assert the WVA Deployment is really applied/deleted. Set before
			// Create so the create-time reconcile uses it; Ordered keeps it for all specs.
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

		It("creates the WVA Deployment when ManagementState is Managed", func(ctx SpecContext) {
			err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
				if err := testEnv.Client.Get(ctx, client.ObjectKeyFromObject(cr), cr); err != nil {
					return err
				}
				cr.Spec.WVA.ManagementState = common.Managed
				return testEnv.Client.Update(ctx, cr)
			})
			Expect(err).NotTo(HaveOccurred())

			Eventually(func(g Gomega) {
				g.Expect(testEnv.Client.Get(ctx, wvaKey, &appsv1.Deployment{})).To(Succeed(),
					"WVA Deployment should be applied to the cluster when Managed")
			}).WithContext(ctx).Should(Succeed())
		})

		It("deletes the WVA Deployment when ManagementState changes to Removed", func(ctx SpecContext) {
			// Precondition: WVA Deployment exists from the previous (Managed) spec.
			Expect(testEnv.Client.Get(ctx, wvaKey, &appsv1.Deployment{})).To(Succeed())

			err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
				if err := testEnv.Client.Get(ctx, client.ObjectKeyFromObject(cr), cr); err != nil {
					return err
				}
				cr.Spec.WVA.ManagementState = common.Removed
				return testEnv.Client.Update(ctx, cr)
			})
			Expect(err).NotTo(HaveOccurred())

			Eventually(func(g Gomega) {
				err := testEnv.Client.Get(ctx, wvaKey, &appsv1.Deployment{})
				g.Expect(k8serr.IsNotFound(err)).To(BeTrue(),
					"WVA Deployment should be deleted by defaultCleanup when Removed")
			}).WithContext(ctx).Should(Succeed())
		})
	})

	Context("WVA readiness condition", Ordered, func() {
		var cr *platformv1alpha1.Kserve

		BeforeAll(func(ctx SpecContext) {
			// Mock: readiness is driven by manually-created Deployments; deployer output
			// is irrelevant. Set before Create; Ordered keeps it for all specs.
			testEnv.Reconciler.Deployer = &fixture.MockDeployer{}

			cr = fixture.KserveCR(fixture.WithWVAManagementState(common.Managed))
			Expect(testEnv.Client.Create(ctx, cr)).To(Succeed())

			DeferCleanup(func(ctx SpecContext) {
				deleteAndWaitGone(ctx, cr)
			})
		})

		It("reports WVAReady=False when WVA deployment is not available", func(ctx SpecContext) {
			triggerReconcile(ctx, cr, "wva-readiness-false")

			Eventually(func(g Gomega) {
				g.Expect(testEnv.Client.Get(ctx, client.ObjectKeyFromObject(cr), cr)).To(Succeed())
				cond := fixture.FindCondition(cr, kservemodule.ConditionWVAReady)
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
				cond := fixture.FindCondition(cr, kservemodule.ConditionWVAReady)
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
				cond := fixture.FindCondition(cr, kservemodule.ConditionWVAReady)
				g.Expect(cond).To(BeNil(), "WVAReady condition should be cleared when WVA is disabled")
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
