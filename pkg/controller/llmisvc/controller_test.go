/*
Copyright 2023 The KServe Authors.

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

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha1"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"knative.dev/pkg/apis"
	"knative.dev/pkg/kmeta"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayapi "sigs.k8s.io/gateway-api/apis/v1"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	. "github.com/kserve/kserve/pkg/testing"
)

var _ = Describe("LLMInferenceService Controller", func() {
	Context("Basic Reconciliation", func() {
		It("should create a basic single node deployment when LLMInferenceService is created", func(ctx SpecContext) {
			// given
			svcName := "test-llm"
			nsName := kmeta.ChildName(svcName, "-test")
			namespace := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: nsName,
				},
			}
			Expect(envTest.Client.Create(ctx, namespace)).To(Succeed())
			defer func() {
				envTest.DeleteAll(namespace)
			}()

			modelURL, err := apis.ParseURL("hf://facebook/opt-125m")
			Expect(err).ToNot(HaveOccurred())

			llmSvc := &v1alpha1.LLMInferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      svcName,
					Namespace: nsName,
				},
				Spec: v1alpha1.LLMInferenceServiceSpec{
					Model: v1alpha1.LLMModelSpec{
						URI: *modelURL,
					},
					WorkloadSpec: v1alpha1.WorkloadSpec{},
					Router: &v1alpha1.RouterSpec{
						Route: &v1alpha1.GatewayRoutesSpec{
							HTTP: &v1alpha1.HTTPRouteSpec{
								Refs: []corev1.LocalObjectReference{
									{Name: "test-gateway"},
								},
							},
						},
						Gateway: &v1alpha1.GatewaySpec{
							Refs: []v1alpha1.UntypedObjectReference{
								{
									Name:      "test-gateway",
									Namespace: gatewayapi.Namespace(nsName),
								},
							},
						},
					},
				},
			}

			// when
			Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
			defer func() {
				Expect(envTest.Delete(ctx, llmSvc)).To(Succeed())
			}()

			// then
			expectedDeployment := &appsv1.Deployment{}
			Eventually(func(g Gomega, ctx context.Context) error {
				err := envTest.Get(ctx, types.NamespacedName{
					Name:      "test-llm-kserve",
					Namespace: nsName,
				}, expectedDeployment)

				g.Expect(client.IgnoreNotFound(err)).NotTo(HaveOccurred())

				return err
			}).WithContext(ctx).Should(Succeed())

			Expect(expectedDeployment.Spec.Replicas).To(Equal(ptr.To[int32](1)))
			Expect(expectedDeployment.Spec.Template.Spec.Containers).To(HaveLen(1))
			Expect(expectedDeployment.Spec.Template.Spec.Containers[0].Image).To(Equal("facebook/opt-125m:latest"))

			Expect(expectedDeployment.OwnerReferences).To(HaveLen(1))
			Expect(expectedDeployment.OwnerReferences[0].Name).To(Equal(svcName))
			Expect(expectedDeployment.OwnerReferences[0].Kind).To(Equal("LLMInferenceService"))

			Eventually(func(g Gomega, ctx context.Context) error {
				if err := envTest.Get(ctx, client.ObjectKeyFromObject(llmSvc), llmSvc); err != nil {
					return err
				}
				g.Expect(llmSvc.Status).To(HaveCondition(string(v1alpha1.PresetsCombined), "True"))

				// Overall condition depends on owned resources such as Deployment.
				// When running on EnvTest certain controllers are not built-in, and that
				// includes deployment controllers, ReplicaSet controllers, etc.
				// Therefore, we can only observe a successful reconcile when testing against actual cluster
				if envTest.Environment.UseExistingCluster == ptr.To[bool](true) {
					g.Expect(llmSvc.Status).To(HaveCondition(string(v1alpha1.WorkloadReady), "True"))
				}

				return nil
			}).WithContext(ctx).Should(Succeed())
		})
	})

	PContext("HTTPRoute reconciliation", func() {
		When("spec.router.route.http is present and refs is empty", func() {
			It("should create HTTPRoute(s) pointing to the gateways in spec.router.gateway.refs", func() {
				// TODO: Create LLMInferenceService with spec.router.route.http and no refs
				// TODO: Assert HTTPRoute(s) are created, owned, and labeled correctly
			})
		})

		When("spec.router.route.http is removed", func() {
			It("should delete owned HTTPRoute(s)", func() {
				// TODO: Remove spec.router.route.http from LLMInferenceService
				// TODO: Assert HTTPRoute(s) are deleted
			})
		})
	})
})
