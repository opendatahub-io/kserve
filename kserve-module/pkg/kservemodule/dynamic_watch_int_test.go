package kservemodule_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	k8serr "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/opendatahub-io/odh-platform-utilities/pkg/cluster"

	platformv1alpha1 "github.com/opendatahub-io/kserve-module/pkg/apis/v1alpha1"
	kservemodule "github.com/opendatahub-io/kserve-module/pkg/kservemodule"
	"github.com/opendatahub-io/kserve-module/pkg/kservemodule/fixture"
)

var _ = Describe("Dynamic Watch Integration", Ordered, func() {
	var kserve *platformv1alpha1.Kserve

	BeforeAll(func(ctx SpecContext) {
		// Mock: status-only context, deployer output is irrelevant. Set before Create;
		// Ordered keeps it for all specs.
		testEnv.Reconciler.Deployer = &fixture.MockDeployer{}
		testEnv.Reconciler.SetClusterType(cluster.ClusterTypeOpenShift)

		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "openshift-operators"}}
		Expect(client.IgnoreAlreadyExists(testEnv.Client.Create(ctx, ns))).To(Succeed())

		kserve = fixture.KserveCR()
		// Ensure any leftover CR (possibly deleting with a finalizer) is fully gone.
		Expect(client.IgnoreNotFound(testEnv.Client.Delete(ctx, kserve))).To(Succeed())
		Eventually(func(g Gomega) {
			err := testEnv.Client.Get(ctx, client.ObjectKeyFromObject(kserve), kserve)
			g.Expect(k8serr.IsNotFound(err)).To(BeTrue())
		}).WithContext(ctx).WithTimeout(30 * time.Second).Should(Succeed())

		kserve = fixture.KserveCR()
		Expect(testEnv.Client.Create(ctx, kserve)).To(Succeed())

		Eventually(func(g Gomega) {
			g.Expect(testEnv.Client.Get(ctx, client.ObjectKeyFromObject(kserve), kserve)).To(Succeed())
			g.Expect(kserve.Status.ObservedGeneration).To(Equal(kserve.Generation))
		}).WithContext(ctx).WithTimeout(30 * time.Second).Should(Succeed())
	})

	AfterAll(func(ctx SpecContext) {
		if kserve != nil {
			Expect(client.IgnoreNotFound(testEnv.Client.Delete(ctx, kserve))).To(Succeed())
			Eventually(func(g Gomega) {
				err := testEnv.Client.Get(ctx, client.ObjectKeyFromObject(kserve), kserve)
				g.Expect(k8serr.IsNotFound(err)).To(BeTrue())
			}).WithContext(ctx).WithTimeout(30 * time.Second).Should(Succeed())
		}
		testEnv.Reconciler.SetClusterType(cluster.ClusterTypeOpenShift)
	})

	Context("Subscription watch", Ordered, func() {
		BeforeAll(func(ctx SpecContext) {
			fixture.CreateSubscription(ctx, testEnv.Client, "openshift-cert-manager-operator", "openshift-operators")
		})

		AfterAll(func(ctx SpecContext) {
			for _, name := range []string{"rhcl-operator", "openshift-cert-manager-operator"} {
				sub := &unstructured.Unstructured{}
				sub.SetGroupVersionKind(schema.GroupVersionKind{
					Group: "operators.coreos.com", Version: "v1alpha1", Kind: "Subscription",
				})
				sub.SetName(name)
				sub.SetNamespace("openshift-operators")
				client.IgnoreNotFound(testEnv.Client.Delete(ctx, sub))
			}
		})

		It("shows PreConditionFailed then clears after installing rhcl-operator", func(ctx SpecContext) {
			triggerReconcile(ctx, kserve, "dw-sub-partial")

			Eventually(func(g Gomega) {
				g.Expect(testEnv.Client.Get(ctx, client.ObjectKeyFromObject(kserve), kserve)).To(Succeed())
				cond := fixture.FindCondition(kserve, kservemodule.ConditionLLMISVCDeps)
				g.Expect(cond).NotTo(BeNil())
				g.Expect(cond.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(cond.Reason).To(Equal("PreConditionFailed"))
				g.Expect(cond.Message).To(ContainSubstring("Red Hat Connectivity Link"))
				g.Expect(cond.Message).NotTo(ContainSubstring("cert-manager"))
			}).WithContext(ctx).WithTimeout(30 * time.Second).WithPolling(2 * time.Second).Should(Succeed())

			fixture.CreateSubscription(ctx, testEnv.Client, "rhcl-operator", "openshift-operators")

			triggerReconcile(ctx, kserve, "dw-sub-all-installed")

			Eventually(func(g Gomega) {
				g.Expect(testEnv.Client.Get(ctx, client.ObjectKeyFromObject(kserve), kserve)).To(Succeed())
				cond := fixture.FindCondition(kserve, kservemodule.ConditionLLMISVCDeps)
				g.Expect(cond).NotTo(BeNil())
				g.Expect(cond.Status).To(Equal(metav1.ConditionTrue))
			}).WithContext(ctx).WithTimeout(30 * time.Second).WithPolling(2 * time.Second).Should(Succeed())
		})

		It("shows PreConditionFailed after Subscription deletion", func(ctx SpecContext) {
			sub := &unstructured.Unstructured{}
			sub.SetGroupVersionKind(schema.GroupVersionKind{
				Group: "operators.coreos.com", Version: "v1alpha1", Kind: "Subscription",
			})
			sub.SetName("rhcl-operator")
			sub.SetNamespace("openshift-operators")
			Expect(testEnv.Client.Delete(ctx, sub)).To(Succeed())

			Eventually(func(g Gomega) {
				g.Expect(testEnv.Client.Get(ctx, client.ObjectKeyFromObject(kserve), kserve)).To(Succeed())
				cond := fixture.FindCondition(kserve, kservemodule.ConditionLLMISVCDeps)
				g.Expect(cond).NotTo(BeNil())
				g.Expect(cond.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(cond.Reason).To(Equal("PreConditionFailed"))
				g.Expect(cond.Message).To(ContainSubstring("Red Hat Connectivity Link"))
				g.Expect(cond.Message).NotTo(ContainSubstring("cert-manager"))
			}).WithContext(ctx).WithTimeout(30 * time.Second).WithPolling(2 * time.Second).Should(Succeed())
		})
	})

	Context("LeaderWorkerSet operator watch", func() {
		var lwsCRD *apiextensionsv1.CustomResourceDefinition

		lwsGVK := schema.GroupVersionKind{Group: "operator.openshift.io", Version: "v1", Kind: "LeaderWorkerSetOperator"}

		BeforeAll(func(ctx SpecContext) {
			lwsCRD = fixture.CreateCRD(ctx, testEnv.Client,
				lwsGVK.Group, lwsGVK.Version, lwsGVK.Kind, apiextensionsv1.ClusterScoped)

			triggerReconcile(ctx, kserve, "dw-lws-crd-created")
			Eventually(func(g Gomega) {
				g.Expect(testEnv.Client.Get(ctx, client.ObjectKeyFromObject(kserve), kserve)).To(Succeed())
				g.Expect(kserve.Status.ObservedGeneration).To(Equal(kserve.Generation))
			}).WithContext(ctx).WithTimeout(30 * time.Second).Should(Succeed())

			Eventually(func(g Gomega) {
				list := &unstructured.UnstructuredList{}
				list.SetGroupVersionKind(lwsGVK)
				g.Expect(testEnv.Reconciler.Client.List(ctx, list)).To(Succeed())
			}).WithContext(ctx).WithTimeout(30 * time.Second).Should(Succeed())
		})

		AfterAll(func(ctx SpecContext) {
			if lwsCRD != nil {
				client.IgnoreNotFound(testEnv.Client.Delete(ctx, lwsCRD))
			}
		})

		It("reflects operator health in conditions", func(ctx SpecContext) {
			lwsCR := &unstructured.Unstructured{}
			lwsCR.SetGroupVersionKind(lwsGVK)
			lwsCR.SetName("cluster")
			Expect(testEnv.Client.Create(ctx, lwsCR)).To(Succeed())

			// Re-fetch to get the latest ResourceVersion before status update
			Expect(testEnv.Client.Get(ctx, client.ObjectKey{Name: "cluster"}, lwsCR)).To(Succeed())
			lwsCR.Object["status"] = map[string]any{
				"conditions": []any{
					map[string]any{
						"type":               "Degraded",
						"status":             "True",
						"reason":             "TestDegraded",
						"message":            "LWS operator is degraded",
						"lastTransitionTime": "2026-01-01T00:00:00Z",
					},
				},
			}
			Expect(testEnv.Client.Status().Update(ctx, lwsCR)).To(Succeed())

			DeferCleanup(func(ctx SpecContext) {
				client.IgnoreNotFound(testEnv.Client.Delete(ctx, lwsCR))
			})

			triggerReconcile(ctx, kserve, "dw-lws-cr-created")

			Eventually(func(g Gomega) {
				g.Expect(testEnv.Client.Get(ctx, client.ObjectKeyFromObject(kserve), kserve)).To(Succeed())
				cond := fixture.FindCondition(kserve, kservemodule.ConditionLLMISVCWideEPDeps)
				g.Expect(cond).NotTo(BeNil(), "KserveLLMInferenceServiceWideEPDependencies condition should exist")
				g.Expect(cond.Message).To(ContainSubstring("LWS operator is degraded"))
			}).WithContext(ctx).WithTimeout(30 * time.Second).WithPolling(2 * time.Second).Should(Succeed())
		})
	})

	// TestPresetWatchFilter covers what the predicate accepts, driving the
	// filter the controller uses. This spec covers the wiring around it: that
	// the dynamic watch registers for the preset GVK and that its events reach
	// the reconciler.
	//
	// The deployer is mocked here, so nothing applies resources and a restored
	// preset is not observable. A reconcile having run is the only signal
	// available, which is why the assertion is a timing one.
	Context("LLMInferenceServiceConfig preset watch", Ordered, func() {
		var presetCRD *apiextensionsv1.CustomResourceDefinition
		var deployer *fixture.MockDeployer

		presetGVK := schema.GroupVersionKind{
			Group: "serving.kserve.io", Version: "v1alpha2", Kind: "LLMInferenceServiceConfig",
		}
		const wellKnownAnnotation = "serving.kserve.io/well-known-config"

		// Comfortably under the reconciler's shortest requeue interval (15s),
		// so a reconcile inside it can only have come from the watch.
		const eventDrivenReconcileWindow = 8 * time.Second

		shippedPreset := func() *unstructured.Unstructured {
			preset := &unstructured.Unstructured{}
			preset.SetGroupVersionKind(presetGVK)
			preset.SetName("v1-2-3-kserve-config-llm-decode")
			preset.SetNamespace("opendatahub")
			preset.SetAnnotations(map[string]string{wellKnownAnnotation: "true"})

			return preset
		}

		BeforeAll(func(ctx SpecContext) {
			var ok bool
			deployer, ok = testEnv.Reconciler.Deployer.(*fixture.MockDeployer)
			Expect(ok).To(BeTrue(), "this context counts Deploy calls to detect reconciles")

			// Dependencies the Subscription context deletes in its AfterAll. A
			// critical failure returns before the deployer runs, which would
			// leave nothing to count.
			for _, name := range []string{"rhcl-operator", "openshift-cert-manager-operator"} {
				fixture.CreateSubscription(ctx, testEnv.Client, name, "openshift-operators")
			}

			presetCRD = fixture.CreateCRD(ctx, testEnv.Client,
				presetGVK.Group, presetGVK.Version, presetGVK.Kind, apiextensionsv1.NamespaceScoped)

			triggerReconcile(ctx, kserve, "dw-preset-crd-created")
			Eventually(func(g Gomega) {
				g.Expect(testEnv.Client.Get(ctx, client.ObjectKeyFromObject(kserve), kserve)).To(Succeed())
				g.Expect(kserve.Status.ObservedGeneration).To(Equal(kserve.Generation))
			}).WithContext(ctx).WithTimeout(30 * time.Second).Should(Succeed())

			// Wait for registerDynamicWatches to claim the GVK.
			Eventually(func(g Gomega) {
				list := &unstructured.UnstructuredList{}
				list.SetGroupVersionKind(presetGVK)
				g.Expect(testEnv.Reconciler.Client.List(ctx, list)).To(Succeed())
			}).WithContext(ctx).WithTimeout(30 * time.Second).Should(Succeed())
		})

		AfterAll(func(ctx SpecContext) {
			if presetCRD != nil {
				client.IgnoreNotFound(testEnv.Client.Delete(ctx, presetCRD))
			}
		})

		It("reconciles when a preset loses the annotation that marks it as ours", func(ctx SpecContext) {
			preset := shippedPreset()
			Expect(testEnv.Client.Create(ctx, preset)).To(Succeed())
			DeferCleanup(func(ctx SpecContext) {
				client.IgnoreNotFound(testEnv.Client.Delete(ctx, shippedPreset()))
			})

			// The reconciler requeues itself periodically, so a Deploy call on
			// its own proves nothing. Wait for one to land first: the next
			// periodic call is then a full requeue interval away, and anything
			// arriving in the short window below came from the watch.
			start := deployer.CallCount()
			Eventually(deployer.CallCount).
				WithContext(ctx).WithTimeout(60*time.Second).WithPolling(100*time.Millisecond).
				Should(BeNumerically(">", start), "reconciles must reach the deployer for this spec to mean anything")
			mark := deployer.CallCount()

			// Stripping the annotation must not change whether the event is
			// delivered: the filter is scoped by namespace, which the edit
			// leaves alone.
			Expect(retry.RetryOnConflict(retry.DefaultRetry, func() error {
				live := &unstructured.Unstructured{}
				live.SetGroupVersionKind(presetGVK)
				if err := testEnv.Client.Get(ctx, client.ObjectKeyFromObject(preset), live); err != nil {
					return err
				}
				live.SetAnnotations(nil)

				return testEnv.Client.Update(ctx, live)
			})).To(Succeed())

			Eventually(deployer.CallCount).
				WithContext(ctx).WithTimeout(eventDrivenReconcileWindow).WithPolling(100*time.Millisecond).
				Should(BeNumerically(">", mark), "the update should have enqueued a reconcile well before the next periodic requeue")
		})
	})
})
