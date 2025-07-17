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

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	gatewayapi "sigs.k8s.io/gateway-api/apis/v1"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"knative.dev/pkg/apis"
	"knative.dev/pkg/kmeta"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
	"github.com/kserve/kserve/pkg/constants"
	"github.com/kserve/kserve/pkg/utils"

	"github.com/kserve/kserve/pkg/controller/llmisvc"
	. "github.com/kserve/kserve/pkg/controller/llmisvc/fixture"
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
						Route:     &v1alpha1.GatewayRoutesSpec{},
						Gateway:   &v1alpha1.GatewaySpec{},
						Scheduler: &v1alpha1.SchedulerSpec{},
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
				return envTest.Get(ctx, types.NamespacedName{
					Name:      svcName + "-kserve",
					Namespace: nsName,
				}, expectedDeployment)
			}).WithContext(ctx).Should(Succeed())

			Expect(expectedDeployment.Spec.Replicas).To(Equal(ptr.To[int32](1)))
			Expect(expectedDeployment).To(HaveContainerImage("ghcr.io/llm-d/llm-d:0.0.8")) // Coming from preset
			Expect(expectedDeployment).To(BeOwnedBy(llmSvc))

			Eventually(LLMInferenceServiceIsReady(llmSvc)).WithContext(ctx).Should(Succeed())
		})
	})

	Context("Routing reconciliation ", func() {
		When("HTTP route is managed", func() {
			It("should create routes pointing to the default gateway when both are managed", func(ctx SpecContext) {
				// given
				svcName := "test-llm-create-http-route"
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
							Route:   &v1alpha1.GatewayRoutesSpec{},
							Gateway: &v1alpha1.GatewaySpec{},
						},
					},
				}

				// when
				Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
				defer func() {
					Expect(envTest.Delete(ctx, llmSvc)).To(Succeed())
				}()

				// then
				expectedHTTPRoute := &gatewayapi.HTTPRoute{}
				Eventually(func(g Gomega, ctx context.Context) error {
					routes, errList := managedRoutes(ctx, llmSvc)
					g.Expect(errList).ToNot(HaveOccurred())
					g.Expect(routes).To(HaveLen(1))
					expectedHTTPRoute = &routes[0]

					return nil
				}).WithContext(ctx).Should(Succeed())

				Expect(expectedHTTPRoute).To(BeControllerBy(llmSvc))
				Expect(expectedHTTPRoute).To(HaveGatewayRefs(gatewayapi.ParentReference{Name: "kserve-ingress-gateway"}))
				Expect(expectedHTTPRoute).To(HaveBackendRefs(svcName + "-inference-pool"))

				Eventually(LLMInferenceServiceIsReady(llmSvc)).WithContext(ctx).Should(Succeed())
			})

			It("should create HTTPRoute with defined spec", func(ctx SpecContext) {
				// given
				svcName := "test-llm-defined-http-route"
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
									Spec: customRouteSpec(ctx, envTest.Client, nsName, "my-ingress-gateway", "my-inference-pool"),
								},
							},
							Gateway: &v1alpha1.GatewaySpec{},
						},
					},
				}

				// when
				Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
				defer func() {
					Expect(envTest.Delete(ctx, llmSvc)).To(Succeed())
				}()

				expectedHTTPRoute := &gatewayapi.HTTPRoute{}

				Eventually(func(g Gomega, ctx context.Context) error {
					routes, errList := managedRoutes(ctx, llmSvc)
					g.Expect(errList).ToNot(HaveOccurred())
					g.Expect(routes).To(HaveLen(1))
					expectedHTTPRoute = &routes[0]
					return nil
				}).WithContext(ctx).Should(Not(HaveOccurred()), "HTTPRoute should be created")

				Expect(expectedHTTPRoute).To(BeControllerBy(llmSvc))
				Expect(expectedHTTPRoute).To(HaveGatewayRefs(gatewayapi.ParentReference{Name: "my-ingress-gateway"}))
				Expect(expectedHTTPRoute).To(HaveBackendRefs("my-inference-pool"))

				Eventually(LLMInferenceServiceIsReady(llmSvc)).WithContext(ctx).Should(Succeed())
			})

			It("should delete managed HTTPRoute when ref is defined", func(ctx SpecContext) {
				// given
				svcName := "test-llm-update-http-route"
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
								HTTP: &v1alpha1.HTTPRouteSpec{},
							},
							Gateway: &v1alpha1.GatewaySpec{},
						},
					},
				}

				Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
				defer func() {
					Expect(envTest.Delete(ctx, llmSvc)).To(Succeed())
				}()

				Eventually(func(g Gomega, ctx context.Context) error {
					routes, errList := managedRoutes(ctx, llmSvc)
					g.Expect(errList).ToNot(HaveOccurred())
					g.Expect(routes).To(HaveLen(1))

					return nil
				}).WithContext(ctx).Should(Succeed())

				// when - Update the HTTPRoute spec
				errRetry := retry.RetryOnConflict(retry.DefaultRetry, func() error {
					_, errUpdate := ctrl.CreateOrUpdate(ctx, envTest.Client, llmSvc, func() error {
						llmSvc.Spec.Router.Route.HTTP.Refs = []corev1.LocalObjectReference{{Name: "my-custom-route"}}
						return nil
					})
					return errUpdate
				})
				Expect(errRetry).ToNot(HaveOccurred())

				// then
				Eventually(func(g Gomega, ctx context.Context) error {
					routes, errList := managedRoutes(ctx, llmSvc)
					g.Expect(errList).ToNot(HaveOccurred())
					g.Expect(routes).To(BeEmpty())

					return nil
				}).WithContext(ctx).Should(Succeed())

				Eventually(LLMInferenceServiceIsReady(llmSvc)).WithContext(ctx).Should(Succeed())
			})
		})

		When("transitioning from managed to unmanaged router", func() {
			DescribeTable("owned resources should be deleted",

				func(ctx SpecContext, testName string, initialRouterSpec *v1alpha1.RouterSpec, specMutation func(*v1alpha1.LLMInferenceService)) {
					// given
					svcName := "test-llm-" + testName
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
							Router:       initialRouterSpec,
						},
					}

					// when - Create LLMInferenceService
					Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
					defer func() {
						Expect(envTest.Delete(ctx, llmSvc)).To(Succeed())
					}()

					// then - HTTPRoute should be created with router labels
					Eventually(func(g Gomega, ctx context.Context) error {
						routes, errList := managedRoutes(ctx, llmSvc)
						g.Expect(errList).ToNot(HaveOccurred())
						g.Expect(routes).To(HaveLen(1))

						return nil
					}).WithContext(ctx).Should(Succeed(), "Should have managed HTTPRoute")

					// when - Update LLMInferenceService using the provided update function
					errRetry := retry.RetryOnConflict(retry.DefaultRetry, func() error {
						_, errUpdate := ctrl.CreateOrUpdate(ctx, envTest.Client, llmSvc, func() error {
							specMutation(llmSvc)
							return nil
						})
						return errUpdate
					})

					Expect(errRetry).ToNot(HaveOccurred())

					// then - HTTPRoute with router labels should be deleted
					Eventually(func(g Gomega, ctx context.Context) error {
						routes, errList := managedRoutes(ctx, llmSvc)
						g.Expect(errList).ToNot(HaveOccurred())
						g.Expect(routes).To(BeEmpty())

						return nil
					}).WithContext(ctx).Should(Succeed(), "Should have no managed HTTPRoutes with router when ")

					Eventually(LLMInferenceServiceIsReady(llmSvc)).WithContext(ctx).Should(Succeed())
				},
				Entry("should delete HTTPRoutes when spec.Router is set to nil",
					"router-spec-nil",
					&v1alpha1.RouterSpec{
						Route: &v1alpha1.GatewayRoutesSpec{
							HTTP: &v1alpha1.HTTPRouteSpec{}, // Default empty spec
						},
						Gateway: &v1alpha1.GatewaySpec{},
					},
					func(llmSvc *v1alpha1.LLMInferenceService) {
						llmSvc.Spec.Router = nil
					},
				),
				Entry("should delete HTTPRoutes when entire route spec is set to nil",
					"router-route-spec-nil",
					&v1alpha1.RouterSpec{
						Route: &v1alpha1.GatewayRoutesSpec{
							HTTP: &v1alpha1.HTTPRouteSpec{}, // Default empty spec
						},
						Gateway: &v1alpha1.GatewaySpec{},
					},
					func(llmSvc *v1alpha1.LLMInferenceService) {
						llmSvc.Spec.Router.Route = nil
					},
				),
			)
		})
	})

	Context("Storage configuration", func() {
		It("should configure direct PVC mount when model uri starts with pvc://", func(ctx SpecContext) {
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

			modelURL, err := apis.ParseURL("pvc://facebook-models/opt-125m")
			Expect(err).ToNot(HaveOccurred())

			llmSvc := &v1alpha1.LLMInferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      svcName,
					Namespace: nsName,
				},
				Spec: v1alpha1.LLMInferenceServiceSpec{
					Model: v1alpha1.LLMModelSpec{
						Name: ptr.To("foo"),
						URI:  *modelURL,
					},
					WorkloadSpec: v1alpha1.WorkloadSpec{},
					Router: &v1alpha1.RouterSpec{
						Route:     &v1alpha1.GatewayRoutesSpec{},
						Gateway:   &v1alpha1.GatewaySpec{},
						Scheduler: &v1alpha1.SchedulerSpec{},
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
				return envTest.Get(ctx, types.NamespacedName{
					Name:      svcName + "-kserve",
					Namespace: nsName,
				}, expectedDeployment)
			}).WithContext(ctx).Should(Succeed())

			mainContainer := utils.GetContainerWithName(&expectedDeployment.Spec.Template.Spec, "main")
			Expect(mainContainer).ToNot(BeNil())

			Expect(mainContainer.Args).To(ContainElement(constants.DefaultModelLocalMountPath))
			Expect(expectedDeployment.Spec.Template.Spec.Volumes).To(ContainElement(And(
				HaveField("Name", constants.PvcSourceMountName),
				HaveField("VolumeSource.PersistentVolumeClaim.ClaimName", "facebook-models"),
			)))

			Expect(mainContainer.VolumeMounts).To(ContainElement(And(
				HaveField("Name", constants.PvcSourceMountName),
				HaveField("MountPath", constants.DefaultModelLocalMountPath),
				HaveField("ReadOnly", BeTrue()),
				HaveField("SubPath", "opt-125m"),
			)))
		})

		It("should configure a modelcar when model uri starts with oci://", func(ctx SpecContext) {
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

			modelURL, err := apis.ParseURL("oci://registry.io/user-id/repo-id:tag")
			Expect(err).ToNot(HaveOccurred())

			llmSvc := &v1alpha1.LLMInferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      svcName,
					Namespace: nsName,
				},
				Spec: v1alpha1.LLMInferenceServiceSpec{
					Model: v1alpha1.LLMModelSpec{
						Name: ptr.To("foo"),
						URI:  *modelURL,
					},
					WorkloadSpec: v1alpha1.WorkloadSpec{},
					Router: &v1alpha1.RouterSpec{
						Route:     &v1alpha1.GatewayRoutesSpec{},
						Gateway:   &v1alpha1.GatewaySpec{},
						Scheduler: &v1alpha1.SchedulerSpec{},
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
				return envTest.Get(ctx, types.NamespacedName{
					Name:      svcName + "-kserve",
					Namespace: nsName,
				}, expectedDeployment)
			}).WithContext(ctx).Should(Succeed())

			// Check the main container and modelcar container are present.
			mainContainer := utils.GetContainerWithName(&expectedDeployment.Spec.Template.Spec, "main")
			Expect(mainContainer).ToNot(BeNil())
			modelcarContainer := utils.GetContainerWithName(&expectedDeployment.Spec.Template.Spec, constants.ModelcarContainerName)
			Expect(modelcarContainer).ToNot(BeNil())

			// Check container are sharing resources.
			Expect(expectedDeployment.Spec.Template.Spec.ShareProcessNamespace).To(Not(BeNil()))
			Expect(*expectedDeployment.Spec.Template.Spec.ShareProcessNamespace).To(BeTrue())

			// Check the model server is directed to the mount point of the OCI container
			Expect(mainContainer.Args).To(ContainElement(constants.DefaultModelLocalMountPath))

			// Check the model server has an envvar indicating that the model may not be mounted immediately.
			Expect(mainContainer.Env).To(ContainElement(And(
				HaveField("Name", constants.ModelInitModeEnv),
				HaveField("Value", "async"),
			)))

			// Check OCI init container for the pre-fetch
			Expect(expectedDeployment.Spec.Template.Spec.InitContainers).To(ContainElement(And(
				HaveField("Name", constants.ModelcarInitContainerName),
				HaveField("Resources.Limits", And(HaveKey(corev1.ResourceCPU), HaveKey(corev1.ResourceMemory))),
				HaveField("Resources.Requests", And(HaveKey(corev1.ResourceCPU), HaveKey(corev1.ResourceMemory))),
			)))

			// Basic check of empty dir volume is configured (shared mount between the containers)
			Expect(expectedDeployment.Spec.Template.Spec.Volumes).To(ContainElement(HaveField("Name", constants.StorageInitializerVolumeName)))

			// Check that the empty-dir volume is mounted to the modelcar and main container (shared storage)
			Expect(mainContainer.VolumeMounts).To(ContainElement(And(
				HaveField("Name", constants.StorageInitializerVolumeName),
				HaveField("MountPath", "/mnt"),
			)))
			Expect(modelcarContainer.VolumeMounts).To(ContainElement(And(
				HaveField("Name", constants.StorageInitializerVolumeName),
				HaveField("MountPath", "/mnt"),
				HaveField("ReadOnly", false),
			)))
		})
	})
})

func LLMInferenceServiceIsReady(llmSvc *v1alpha1.LLMInferenceService) func(g Gomega, ctx context.Context) error {
	return func(g Gomega, ctx context.Context) error {
		llmSvc := llmSvc.DeepCopy()
		g.Expect(envTest.Get(ctx, client.ObjectKeyFromObject(llmSvc), llmSvc)).To(Succeed())

		g.Expect(llmSvc.Status).To(HaveCondition(string(v1alpha1.PresetsCombined), "True"))
		g.Expect(llmSvc.Status).To(HaveCondition(string(v1alpha1.RouterReady), "True"))

		// Overall condition depends on owned resources such as Deployment.
		// When running on EnvTest certain controllers are not built-in, and that
		// includes deployment controllers, ReplicaSet controllers, etc.
		// Therefore, we can only observe a successful reconcile when testing against the actual cluster
		if envTest.Environment.UseExistingCluster == ptr.To[bool](true) {
			g.Expect(llmSvc.Status).To(HaveCondition("Ready", "True"))
		}

		return nil
	}
}

func managedRoutes(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService) ([]gatewayapi.HTTPRoute, error) {
	httpRoutes := &gatewayapi.HTTPRouteList{}
	listOpts := &client.ListOptions{
		Namespace:     llmSvc.Namespace,
		LabelSelector: labels.SelectorFromSet(llmisvc.RouterLabels(llmSvc)),
	}
	err := envTest.List(ctx, httpRoutes, listOpts)
	return httpRoutes.Items, ignoreNoMatch(err)
}

func ignoreNoMatch(err error) error {
	if meta.IsNoMatchError(err) {
		return nil
	}

	return err
}

func customRouteSpec(ctx context.Context, c client.Client, nsName, gatewayRefName, backendRefName string) *gatewayapi.HTTPRouteSpec {
	customGateway := Gateway(gatewayRefName,
		InNamespace[*gatewayapi.Gateway](nsName),
		WithClassName("istio"),
		WithListeners(gatewayapi.Listener{
			Name:     "http",
			Port:     9991,
			Protocol: gatewayapi.HTTPProtocolType,
			AllowedRoutes: &gatewayapi.AllowedRoutes{
				Namespaces: &gatewayapi.RouteNamespaces{
					From: ptr.To(gatewayapi.NamespacesFromAll),
				},
			},
		}),
	)

	Expect(c.Create(ctx, customGateway)).To(Succeed())

	route := HTTPRoute("custom-route", []HTTPRouteOption{
		WithParentRef(GatewayParentRef(gatewayRefName, nsName)),
		WithHTTPRouteRule(
			HTTPRouteRuleWithBackendAndTimeouts(backendRefName, 8000, "/", "0s", "0s"),
		),
	}...)
	httpRouteSpec := &route.Spec

	return httpRouteSpec
}
