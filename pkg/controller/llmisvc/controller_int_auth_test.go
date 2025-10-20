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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"knative.dev/pkg/kmeta"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayapi "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
	"github.com/kserve/kserve/pkg/controller/llmisvc"
	. "github.com/kserve/kserve/pkg/controller/llmisvc/fixture"
	. "github.com/kserve/kserve/pkg/testing"
)

var _ = Describe("LLMInferenceService Auth Integration Tests", func() {
	Context("Authentication enforcement", func() {
		When("auth is enabled by default (no annotation)", func() {
			It("should require AuthPolicyAffected condition on HTTPRoute", func(ctx SpecContext) {
				// given
				svcName := "test-llm-auth-enabled-default"
				nsName := kmeta.ChildName(svcName, "-test")
				namespace := &corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: nsName,
					},
				}

				Expect(envTest.Client.Create(ctx, namespace)).To(Succeed())
				Expect(envTest.Client.Create(ctx, IstioShadowService(svcName, nsName))).To(Succeed())
				defer func() {
					envTest.DeleteAll(namespace)
				}()

				llmSvc := LLMInferenceService(svcName,
					InNamespace[*v1alpha1.LLMInferenceService](nsName),
					WithModelURI("hf://facebook/opt-125m"),
					WithManagedRoute(),
					WithManagedGateway(),
					WithManagedScheduler(),
				)
				// No annotation set - auth enabled by default

				// when
				Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
				defer func() {
					Expect(envTest.Delete(ctx, llmSvc)).To(Succeed())
				}()

				// then - HTTPRoute should be created
				var createdRoute *gatewayapi.HTTPRoute
				Eventually(func(g Gomega, ctx context.Context) error {
					routes, errList := managedRoutes(ctx, llmSvc)
					g.Expect(errList).ToNot(HaveOccurred())
					g.Expect(routes).To(HaveLen(1))
					createdRoute = &routes[0]
					return nil
				}).WithContext(ctx).Should(Succeed())

				// Simulate HTTPRoute with only gateway controller status (no AuthPolicy)
				ensureHTTPRouteReadyWithoutAuth(ctx, envTest.Client, createdRoute)

				// then - LLMInferenceService should mark HTTPRoutes as NOT ready
				// because AuthPolicy enforcement is missing
				Eventually(func(g Gomega, ctx context.Context) error {
					current := &v1alpha1.LLMInferenceService{}
					g.Expect(envTest.Get(ctx, client.ObjectKeyFromObject(llmSvc), current)).To(Succeed())

					httpRoutesCondition := current.Status.GetCondition(v1alpha1.HTTPRoutesReady)
					g.Expect(httpRoutesCondition).ToNot(BeNil(), "HTTPRoutesReady condition should be set")
					g.Expect(httpRoutesCondition.IsFalse()).To(BeTrue(), "HTTPRoutesReady should be False when AuthPolicy is missing")
					g.Expect(httpRoutesCondition.Message).To(ContainSubstring("Authentication is not enforced"))

					return nil
				}).WithContext(ctx).Should(Succeed())
			})

			It("should be ready when AuthPolicyAffected condition is present and True", func(ctx SpecContext) {
				// given
				svcName := "test-llm-auth-enforced"
				nsName := kmeta.ChildName(svcName, "-test")
				namespace := &corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: nsName,
					},
				}

				Expect(envTest.Client.Create(ctx, namespace)).To(Succeed())
				Expect(envTest.Client.Create(ctx, IstioShadowService(svcName, nsName))).To(Succeed())
				defer func() {
					envTest.DeleteAll(namespace)
				}()

				llmSvc := LLMInferenceService(svcName,
					InNamespace[*v1alpha1.LLMInferenceService](nsName),
					WithModelURI("hf://facebook/opt-125m"),
					WithManagedRoute(),
					WithManagedGateway(),
					WithManagedScheduler(),
				)

				// when
				Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
				defer func() {
					Expect(envTest.Delete(ctx, llmSvc)).To(Succeed())
				}()

				// then - HTTPRoute should be created
				var createdRoute *gatewayapi.HTTPRoute
				Eventually(func(g Gomega, ctx context.Context) error {
					routes, errList := managedRoutes(ctx, llmSvc)
					g.Expect(errList).ToNot(HaveOccurred())
					g.Expect(routes).To(HaveLen(1))
					createdRoute = &routes[0]
					return nil
				}).WithContext(ctx).Should(Succeed())

				// Simulate HTTPRoute with AuthPolicy enforcement
				ensureHTTPRouteReadyWithAuth(ctx, envTest.Client, llmSvc, createdRoute)
				ensureRouterManagedResourcesAreReady(ctx, envTest.Client, llmSvc)

				// then - LLMInferenceService should be ready
				Eventually(LLMInferenceServiceIsReady(llmSvc, func(g Gomega, current *v1alpha1.LLMInferenceService) {
					g.Expect(current.Status).To(HaveCondition(string(v1alpha1.HTTPRoutesReady), "True"))
				})).WithContext(ctx).Should(Succeed())
			})
		})

		When("auth is explicitly disabled via annotation", func() {
			It("should not require AuthPolicyAffected condition on HTTPRoute", func(ctx SpecContext) {
				// given
				svcName := "test-llm-auth-disabled"
				nsName := kmeta.ChildName(svcName, "-test")
				namespace := &corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: nsName,
					},
				}

				Expect(envTest.Client.Create(ctx, namespace)).To(Succeed())
				Expect(envTest.Client.Create(ctx, IstioShadowService(svcName, nsName))).To(Succeed())
				defer func() {
					envTest.DeleteAll(namespace)
				}()

				llmSvc := LLMInferenceService(svcName,
					InNamespace[*v1alpha1.LLMInferenceService](nsName),
					WithModelURI("hf://facebook/opt-125m"),
					WithManagedRoute(),
					WithManagedGateway(),
					WithManagedScheduler(),
					WithAnnotations(map[string]string{
						"security.opendatahub.io/enable-auth": "false",
					}),
				)

				// when
				Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
				defer func() {
					Expect(envTest.Delete(ctx, llmSvc)).To(Succeed())
				}()

				// then - HTTPRoute should be created
				var createdRoute *gatewayapi.HTTPRoute
				Eventually(func(g Gomega, ctx context.Context) error {
					routes, errList := managedRoutes(ctx, llmSvc)
					g.Expect(errList).ToNot(HaveOccurred())
					g.Expect(routes).To(HaveLen(1))
					createdRoute = &routes[0]
					return nil
				}).WithContext(ctx).Should(Succeed())

				// Simulate HTTPRoute ready WITHOUT AuthPolicy (auth is disabled)
				ensureHTTPRouteReadyWithoutAuth(ctx, envTest.Client, createdRoute)
				ensureRouterManagedResourcesAreReady(ctx, envTest.Client, llmSvc)

				// then - LLMInferenceService should be ready (no AuthPolicy required)
				Eventually(LLMInferenceServiceIsReady(llmSvc, func(g Gomega, current *v1alpha1.LLMInferenceService) {
					g.Expect(current.Status).To(HaveCondition(string(v1alpha1.HTTPRoutesReady), "True"))
				})).WithContext(ctx).Should(Succeed())
			})
		})

		When("auth is explicitly enabled via annotation", func() {
			It("should require AuthPolicyAffected condition on HTTPRoute", func(ctx SpecContext) {
				// given
				svcName := "test-llm-auth-explicit-enabled"
				nsName := kmeta.ChildName(svcName, "-test")
				namespace := &corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: nsName,
					},
				}

				Expect(envTest.Client.Create(ctx, namespace)).To(Succeed())
				Expect(envTest.Client.Create(ctx, IstioShadowService(svcName, nsName))).To(Succeed())
				defer func() {
					envTest.DeleteAll(namespace)
				}()

				llmSvc := LLMInferenceService(svcName,
					InNamespace[*v1alpha1.LLMInferenceService](nsName),
					WithModelURI("hf://facebook/opt-125m"),
					WithManagedRoute(),
					WithManagedGateway(),
					WithManagedScheduler(),
					WithAnnotations(map[string]string{
						"security.opendatahub.io/enable-auth": "true",
					}),
				)

				// when
				Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
				defer func() {
					Expect(envTest.Delete(ctx, llmSvc)).To(Succeed())
				}()

				// then - HTTPRoute should be created
				var createdRoute *gatewayapi.HTTPRoute
				Eventually(func(g Gomega, ctx context.Context) error {
					routes, errList := managedRoutes(ctx, llmSvc)
					g.Expect(errList).ToNot(HaveOccurred())
					g.Expect(routes).To(HaveLen(1))
					createdRoute = &routes[0]
					return nil
				}).WithContext(ctx).Should(Succeed())

				// Simulate HTTPRoute without AuthPolicy
				ensureHTTPRouteReadyWithoutAuth(ctx, envTest.Client, createdRoute)

				// then - HTTPRoutes should be marked as NOT ready
				Eventually(func(g Gomega, ctx context.Context) error {
					current := &v1alpha1.LLMInferenceService{}
					g.Expect(envTest.Get(ctx, client.ObjectKeyFromObject(llmSvc), current)).To(Succeed())

					httpRoutesCondition := current.Status.GetCondition(v1alpha1.HTTPRoutesReady)
					g.Expect(httpRoutesCondition).ToNot(BeNil())
					g.Expect(httpRoutesCondition.IsFalse()).To(BeTrue())
					g.Expect(httpRoutesCondition.Message).To(ContainSubstring("Authentication is not enforced"))

					return nil
				}).WithContext(ctx).Should(Succeed())
			})
		})

		When("AuthPolicy condition exists but is not True", func() {
			It("should mark HTTPRoutes as not ready", func(ctx SpecContext) {
				// given
				svcName := "test-llm-auth-policy-false"
				nsName := kmeta.ChildName(svcName, "-test")
				namespace := &corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: nsName,
					},
				}

				Expect(envTest.Client.Create(ctx, namespace)).To(Succeed())
				Expect(envTest.Client.Create(ctx, IstioShadowService(svcName, nsName))).To(Succeed())
				defer func() {
					envTest.DeleteAll(namespace)
				}()

				llmSvc := LLMInferenceService(svcName,
					InNamespace[*v1alpha1.LLMInferenceService](nsName),
					WithModelURI("hf://facebook/opt-125m"),
					WithManagedRoute(),
					WithManagedGateway(),
					WithManagedScheduler(),
				)

				// when
				Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
				defer func() {
					Expect(envTest.Delete(ctx, llmSvc)).To(Succeed())
				}()

				// then - HTTPRoute should be created
				var createdRoute *gatewayapi.HTTPRoute
				Eventually(func(g Gomega, ctx context.Context) error {
					routes, errList := managedRoutes(ctx, llmSvc)
					g.Expect(errList).ToNot(HaveOccurred())
					g.Expect(routes).To(HaveLen(1))
					createdRoute = &routes[0]
					return nil
				}).WithContext(ctx).Should(Succeed())

				// Simulate HTTPRoute with AuthPolicy condition = False
				ensureHTTPRouteWithAuthPolicyFalse(ctx, envTest.Client, createdRoute)

				// then - HTTPRoutes should be marked as NOT ready
				Eventually(func(g Gomega, ctx context.Context) error {
					current := &v1alpha1.LLMInferenceService{}
					g.Expect(envTest.Get(ctx, client.ObjectKeyFromObject(llmSvc), current)).To(Succeed())

					httpRoutesCondition := current.Status.GetCondition(v1alpha1.HTTPRoutesReady)
					g.Expect(httpRoutesCondition).ToNot(BeNil())
					g.Expect(httpRoutesCondition.IsFalse()).To(BeTrue())

					return nil
				}).WithContext(ctx).Should(Succeed())
			})
		})
	})

	Context("IsAuthEnabled function", func() {
		It("should return true when annotation is missing", func() {
			llmSvc := &v1alpha1.LLMInferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test",
					Namespace: "default",
				},
			}

			Expect(llmSvc.IsAuthEnabled()).To(BeTrue())
		})

		It("should return true when annotation is 'true'", func() {
			llmSvc := &v1alpha1.LLMInferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test",
					Namespace: "default",
					Annotations: map[string]string{
						"security.opendatahub.io/enable-auth": "true",
					},
				},
			}

			Expect(llmSvc.IsAuthEnabled()).To(BeTrue())
		})

		It("should return true when annotation is 'TRUE' (case insensitive)", func() {
			llmSvc := &v1alpha1.LLMInferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test",
					Namespace: "default",
					Annotations: map[string]string{
						"security.opendatahub.io/enable-auth": "TRUE",
					},
				},
			}

			Expect(llmSvc.IsAuthEnabled()).To(BeTrue())
		})

		It("should return false when annotation is 'false'", func() {
			llmSvc := &v1alpha1.LLMInferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test",
					Namespace: "default",
					Annotations: map[string]string{
						"security.opendatahub.io/enable-auth": "false",
					},
				},
			}

			Expect(llmSvc.IsAuthEnabled()).To(BeFalse())
		})

		It("should return false when annotation is 'FALSE' (case insensitive)", func() {
			llmSvc := &v1alpha1.LLMInferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test",
					Namespace: "default",
					Annotations: map[string]string{
						"security.opendatahub.io/enable-auth": "FALSE",
					},
				},
			}

			Expect(llmSvc.IsAuthEnabled()).To(BeFalse())
		})
	})
})

// ensureHTTPRouteReadyWithAuth sets up HTTPRoute status with both gateway controller
// AND Kuadrant policy controller conditions
func ensureHTTPRouteReadyWithAuth(ctx context.Context, c client.Client, llmSvc *v1alpha1.LLMInferenceService, route *gatewayapi.HTTPRoute) {
	if envTest.UsingExistingCluster() {
		return
	}

	createdRoute := &gatewayapi.HTTPRoute{}
	Expect(c.Get(ctx, client.ObjectKeyFromObject(route), createdRoute)).To(Succeed())

	if len(createdRoute.Spec.ParentRefs) > 0 {
		createdRoute.Status.RouteStatus.Parents = []gatewayapi.RouteParentStatus{
			// Gateway controller status
			{
				ParentRef:      createdRoute.Spec.ParentRefs[0],
				ControllerName: "gateway.networking.k8s.io/gateway-controller",
				Conditions: []metav1.Condition{
					{
						Type:               string(gatewayapi.RouteConditionAccepted),
						Status:             metav1.ConditionTrue,
						Reason:             "Accepted",
						Message:            "HTTPRoute accepted",
						LastTransitionTime: metav1.Now(),
					},
				},
			},
			// Kuadrant policy controller status
			{
				ParentRef:      createdRoute.Spec.ParentRefs[0],
				ControllerName: "kuadrant.io/policy-controller",
				Conditions: []metav1.Condition{
					{
						Type:               "kuadrant.io/AuthPolicyAffected",
						Status:             metav1.ConditionTrue,
						Reason:             "Accepted",
						Message:            "AuthPolicy is enforced",
						LastTransitionTime: metav1.Now(),
					},
				},
			},
		}
	}

	Expect(c.Status().Update(ctx, createdRoute)).To(Succeed())

	Eventually(func(g Gomega, ctx context.Context) bool {
		updatedRoute := &gatewayapi.HTTPRoute{}
		g.Expect(c.Get(ctx, client.ObjectKeyFromObject(route), updatedRoute)).To(Succeed())
		return llmisvc.IsHTTPRouteReady(llmSvc, updatedRoute)
	}).WithContext(ctx).Should(BeTrue())
}

// ensureHTTPRouteReadyWithoutAuth sets up HTTPRoute status with only gateway controller
// (no Kuadrant policy controller)
func ensureHTTPRouteReadyWithoutAuth(ctx context.Context, c client.Client, route *gatewayapi.HTTPRoute) {
	if envTest.UsingExistingCluster() {
		return
	}

	createdRoute := &gatewayapi.HTTPRoute{}
	Expect(c.Get(ctx, client.ObjectKeyFromObject(route), createdRoute)).To(Succeed())

	if len(createdRoute.Spec.ParentRefs) > 0 {
		createdRoute.Status.RouteStatus.Parents = []gatewayapi.RouteParentStatus{
			// Only gateway controller status (no AuthPolicy)
			{
				ParentRef:      createdRoute.Spec.ParentRefs[0],
				ControllerName: "gateway.networking.k8s.io/gateway-controller",
				Conditions: []metav1.Condition{
					{
						Type:               string(gatewayapi.RouteConditionAccepted),
						Status:             metav1.ConditionTrue,
						Reason:             "Accepted",
						Message:            "HTTPRoute accepted",
						LastTransitionTime: metav1.Now(),
					},
				},
			},
		}
	}

	Expect(c.Status().Update(ctx, createdRoute)).To(Succeed())
}

// ensureHTTPRouteWithAuthPolicyFalse sets up HTTPRoute with AuthPolicy condition = False
func ensureHTTPRouteWithAuthPolicyFalse(ctx context.Context, c client.Client, route *gatewayapi.HTTPRoute) {
	if envTest.UsingExistingCluster() {
		return
	}

	createdRoute := &gatewayapi.HTTPRoute{}
	Expect(c.Get(ctx, client.ObjectKeyFromObject(route), createdRoute)).To(Succeed())

	if len(createdRoute.Spec.ParentRefs) > 0 {
		createdRoute.Status.RouteStatus.Parents = []gatewayapi.RouteParentStatus{
			// Gateway controller status
			{
				ParentRef:      createdRoute.Spec.ParentRefs[0],
				ControllerName: "gateway.networking.k8s.io/gateway-controller",
				Conditions: []metav1.Condition{
					{
						Type:               string(gatewayapi.RouteConditionAccepted),
						Status:             metav1.ConditionTrue,
						Reason:             "Accepted",
						Message:            "HTTPRoute accepted",
						LastTransitionTime: metav1.Now(),
					},
				},
			},
			// Kuadrant policy controller with False status
			{
				ParentRef:      createdRoute.Spec.ParentRefs[0],
				ControllerName: "kuadrant.io/policy-controller",
				Conditions: []metav1.Condition{
					{
						Type:               "kuadrant.io/AuthPolicyAffected",
						Status:             metav1.ConditionFalse,
						Reason:             "PolicyNotApplied",
						Message:            "AuthPolicy could not be applied",
						LastTransitionTime: metav1.Now(),
					},
				},
			},
		}
	}

	Expect(c.Status().Update(ctx, createdRoute)).To(Succeed())
}
