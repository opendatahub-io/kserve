/*
Copyright 2025 The KServe Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package llmisvc_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
	"github.com/kserve/kserve/pkg/constants"
	. "github.com/kserve/kserve/pkg/controller/v1alpha2/llmisvc/fixture"
)

// These tests verify that user-specified labels are propagated only to the Pod
// template labels, not to the Deployment's spec.selector.matchLabels. Kubernetes
// Deployment selectors are immutable after creation, so including user-mutable
// labels would cause "field is immutable" errors on update (RHOAIENG-94569).
//
// Unlike unit tests that reconstruct the deployment-building logic inline, these
// integration tests exercise the actual production reconciler through envtest,
// catching regressions if the maps.Clone calls are removed from the production
// deployment builders.
var _ = Describe("LLMInferenceService Controller", func() {
	Context("Label selector isolation (RHOAIENG-94569)", func() {
		It("should not leak user spec.labels into main Deployment selector", func(ctx SpecContext) {
			svcName := "test-label-selector-main"
			testNs := NewTestNamespace(ctx, envTest)

			userLabels := map[string]string{
				"custom/accelerator": "H100",
				"custom-label":       "custom-value",
			}

			llmSvc := LLMInferenceService(svcName,
				InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
				WithModelURI("hf://facebook/opt-125m"),
				WithWorkloadLabels(userLabels),
			)

			Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
			defer func() {
				testNs.DeleteAndWait(ctx, llmSvc)
			}()

			deployment := &appsv1.Deployment{}
			Eventually(func(g Gomega, ctx context.Context) error {
				return envTest.Get(ctx, types.NamespacedName{
					Name:      svcName + "-kserve",
					Namespace: testNs.Name,
				}, deployment)
			}).WithContext(ctx).Should(Succeed())

			// User labels must appear in the pod template labels
			for k, v := range userLabels {
				Expect(deployment.Spec.Template.Labels).To(HaveKeyWithValue(k, v),
					"user label %q should be propagated to pod template", k)
			}

			// User labels must NOT appear in the selector (immutable after creation)
			for k := range userLabels {
				Expect(deployment.Spec.Selector.MatchLabels).NotTo(HaveKey(k),
					"user label %q must not leak into Deployment selector", k)
			}

			// Selector must contain only internal/stable labels
			for k := range deployment.Spec.Selector.MatchLabels {
				Expect(k).To(SatisfyAny(
					Equal(constants.KubernetesComponentLabelKey),
					Equal(constants.KubernetesAppNameLabelKey),
					Equal(constants.KubernetesPartOfLabelKey),
					Equal(constants.KServeComponentLabelKey),
					Equal(constants.LLMDRoleLabelKey),
				), "selector must only contain internal labels, found %q", k)
			}

			// Template labels must be a strict superset of selector labels
			for k, v := range deployment.Spec.Selector.MatchLabels {
				Expect(deployment.Spec.Template.Labels).To(HaveKeyWithValue(k, v),
					"template must include selector label %s", k)
			}
			Expect(len(deployment.Spec.Template.Labels)).To(BeNumerically(">", len(deployment.Spec.Selector.MatchLabels)),
				"template labels should include user labels beyond selector labels")
		})

		It("should not leak scheduler labels into scheduler Deployment selector", func(ctx SpecContext) {
			svcName := "test-label-selector-sched"
			testNs := NewTestNamespace(ctx, envTest)

			schedulerLabels := map[string]string{
				"scheduler-custom/label": "scheduler-value",
			}

			llmSvc := LLMInferenceService(svcName,
				InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
				WithModelURI("hf://facebook/opt-125m"),
				WithManagedRoute(),
				WithManagedGateway(),
				WithManagedScheduler(),
			)
			// Set scheduler labels directly (no fixture builder exists for this)
			llmSvc.Spec.Router.Scheduler.Labels = schedulerLabels

			Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
			defer func() {
				testNs.DeleteAndWait(ctx, llmSvc)
			}()

			ensureRouterManagedResourcesAreReady(ctx, envTest.Client, llmSvc)

			schedulerDeployment := &appsv1.Deployment{}
			Eventually(func(g Gomega, ctx context.Context) error {
				return envTest.Get(ctx, types.NamespacedName{
					Name:      svcName + "-kserve-router-scheduler",
					Namespace: testNs.Name,
				}, schedulerDeployment)
			}).WithContext(ctx).Should(Succeed())

			// Scheduler labels must appear in the pod template labels
			for k, v := range schedulerLabels {
				Expect(schedulerDeployment.Spec.Template.Labels).To(HaveKeyWithValue(k, v),
					"scheduler label %q should be propagated to pod template", k)
			}

			// Scheduler labels must NOT appear in the selector
			for k := range schedulerLabels {
				Expect(schedulerDeployment.Spec.Selector.MatchLabels).NotTo(HaveKey(k),
					"scheduler label %q must not leak into Deployment selector", k)
			}

			// Template labels must be a strict superset of selector labels
			for k, v := range schedulerDeployment.Spec.Selector.MatchLabels {
				Expect(schedulerDeployment.Spec.Template.Labels).To(HaveKeyWithValue(k, v),
					"template must include selector label %s", k)
			}
			Expect(len(schedulerDeployment.Spec.Template.Labels)).To(BeNumerically(">", len(schedulerDeployment.Spec.Selector.MatchLabels)),
				"template labels should include scheduler labels beyond selector labels")
		})

		It("should not leak prefill labels into prefill Deployment selector", func(ctx SpecContext) {
			svcName := "test-label-selector-pf"
			testNs := NewTestNamespace(ctx, envTest)

			prefillLabels := map[string]string{
				"prefill-custom/label": "prefill-value",
			}

			llmSvc := LLMInferenceService(svcName,
				InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
				WithModelURI("hf://facebook/opt-125m"),
				WithTemplate(&corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "main", Image: "test-image:latest"},
					},
				}),
				WithPrefill(&corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "main", Image: "test-prefill-image:latest"},
					},
				}),
			)
			// Set prefill labels directly (no fixture builder exists for this)
			llmSvc.Spec.Prefill.Labels = prefillLabels

			Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
			defer func() {
				testNs.DeleteAndWait(ctx, llmSvc)
			}()

			prefillDeployment := &appsv1.Deployment{}
			Eventually(func(g Gomega, ctx context.Context) error {
				return envTest.Get(ctx, types.NamespacedName{
					Name:      svcName + "-kserve-prefill",
					Namespace: testNs.Name,
				}, prefillDeployment)
			}).WithContext(ctx).Should(Succeed())

			// Prefill labels must appear in the pod template labels
			for k, v := range prefillLabels {
				Expect(prefillDeployment.Spec.Template.Labels).To(HaveKeyWithValue(k, v),
					"prefill label %q should be propagated to pod template", k)
			}

			// Prefill labels must NOT appear in the selector
			for k := range prefillLabels {
				Expect(prefillDeployment.Spec.Selector.MatchLabels).NotTo(HaveKey(k),
					"prefill label %q must not leak into Deployment selector", k)
			}

			// Selector must contain only internal/stable labels
			for k := range prefillDeployment.Spec.Selector.MatchLabels {
				Expect(k).To(SatisfyAny(
					Equal(constants.KubernetesComponentLabelKey),
					Equal(constants.KubernetesAppNameLabelKey),
					Equal(constants.KubernetesPartOfLabelKey),
					Equal(constants.KServeComponentLabelKey),
					Equal(constants.LLMDRoleLabelKey),
				), "selector must only contain internal labels, found %q", k)
			}

			// Template labels must be a strict superset of selector labels
			for k, v := range prefillDeployment.Spec.Selector.MatchLabels {
				Expect(prefillDeployment.Spec.Template.Labels).To(HaveKeyWithValue(k, v),
					"template must include selector label %s", k)
			}
			Expect(len(prefillDeployment.Spec.Template.Labels)).To(BeNumerically(">", len(prefillDeployment.Spec.Selector.MatchLabels)),
				"template labels should include prefill labels beyond selector labels")
		})

		It("should preserve internal label value when user spec.labels conflict with main Deployment selector", func(ctx SpecContext) {
			svcName := "test-conflict-main"
			testNs := NewTestNamespace(ctx, envTest)

			// User label deliberately collides with an internal selector key
			userLabels := map[string]string{
				constants.KServeComponentLabelKey: "user-override-attempt",
			}

			llmSvc := LLMInferenceService(svcName,
				InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
				WithModelURI("hf://facebook/opt-125m"),
				WithWorkloadLabels(userLabels),
			)

			Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
			defer func() {
				testNs.DeleteAndWait(ctx, llmSvc)
			}()

			deployment := &appsv1.Deployment{}
			Eventually(func(g Gomega, ctx context.Context) error {
				return envTest.Get(ctx, types.NamespacedName{
					Name:      svcName + "-kserve",
					Namespace: testNs.Name,
				}, deployment)
			}).WithContext(ctx).Should(Succeed())

			// The internal value must be preserved in both selector AND template
			Expect(deployment.Spec.Selector.MatchLabels).To(HaveKeyWithValue(
				constants.KServeComponentLabelKey, constants.KServeComponentWorkload),
				"internal selector label must not be overridden by user label")
			Expect(deployment.Spec.Template.Labels).To(HaveKeyWithValue(
				constants.KServeComponentLabelKey, constants.KServeComponentWorkload),
				"internal template label must not be overridden by user label")
		})

		It("should preserve internal label value when user prefill labels conflict with prefill Deployment selector", func(ctx SpecContext) {
			svcName := "test-conflict-pf"
			testNs := NewTestNamespace(ctx, envTest)

			prefillLabels := map[string]string{
				constants.KServeComponentLabelKey: "user-override-attempt",
			}

			llmSvc := LLMInferenceService(svcName,
				InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
				WithModelURI("hf://facebook/opt-125m"),
				WithTemplate(&corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "main", Image: "test-image:latest"},
					},
				}),
				WithPrefill(&corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "main", Image: "test-prefill-image:latest"},
					},
				}),
			)
			llmSvc.Spec.Prefill.Labels = prefillLabels

			Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
			defer func() {
				testNs.DeleteAndWait(ctx, llmSvc)
			}()

			prefillDeployment := &appsv1.Deployment{}
			Eventually(func(g Gomega, ctx context.Context) error {
				return envTest.Get(ctx, types.NamespacedName{
					Name:      svcName + "-kserve-prefill",
					Namespace: testNs.Name,
				}, prefillDeployment)
			}).WithContext(ctx).Should(Succeed())

			// The internal value must be preserved in both selector AND template
			Expect(prefillDeployment.Spec.Selector.MatchLabels).To(HaveKeyWithValue(
				constants.KServeComponentLabelKey, constants.KServeComponentWorkload),
				"internal selector label must not be overridden by user prefill label")
			Expect(prefillDeployment.Spec.Template.Labels).To(HaveKeyWithValue(
				constants.KServeComponentLabelKey, constants.KServeComponentWorkload),
				"internal template label must not be overridden by user prefill label")
		})

		It("should preserve internal label value when user scheduler labels conflict with scheduler Deployment selector", func(ctx SpecContext) {
			svcName := "test-conflict-sched"
			testNs := NewTestNamespace(ctx, envTest)

			schedulerLabels := map[string]string{
				constants.KubernetesComponentLabelKey: "user-override-attempt",
			}

			llmSvc := LLMInferenceService(svcName,
				InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
				WithModelURI("hf://facebook/opt-125m"),
				WithManagedRoute(),
				WithManagedGateway(),
				WithManagedScheduler(),
			)
			llmSvc.Spec.Router.Scheduler.Labels = schedulerLabels

			Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
			defer func() {
				testNs.DeleteAndWait(ctx, llmSvc)
			}()

			ensureRouterManagedResourcesAreReady(ctx, envTest.Client, llmSvc)

			schedulerDeployment := &appsv1.Deployment{}
			Eventually(func(g Gomega, ctx context.Context) error {
				return envTest.Get(ctx, types.NamespacedName{
					Name:      svcName + "-kserve-router-scheduler",
					Namespace: testNs.Name,
				}, schedulerDeployment)
			}).WithContext(ctx).Should(Succeed())

			// The internal value must be preserved in both selector AND template
			Expect(schedulerDeployment.Spec.Selector.MatchLabels).To(HaveKeyWithValue(
				constants.KubernetesComponentLabelKey, constants.LLMComponentRouterScheduler),
				"internal selector label must not be overridden by user scheduler label")
			Expect(schedulerDeployment.Spec.Template.Labels).To(HaveKeyWithValue(
				constants.KubernetesComponentLabelKey, constants.LLMComponentRouterScheduler),
				"internal template label must not be overridden by user scheduler label")
		})
	})
})
