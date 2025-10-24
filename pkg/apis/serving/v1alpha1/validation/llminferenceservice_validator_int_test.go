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

package validation_test

import (
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"knative.dev/pkg/apis"
	gatewayapi "sigs.k8s.io/gateway-api/apis/v1"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
	"github.com/kserve/kserve/pkg/controller/llmisvc/fixture"
)

var _ = Describe("LLMInferenceService webhook validation", func() {
	var (
		ns        *corev1.Namespace
		nsName    string
		gateway   *gatewayapi.Gateway
		httpRoute *gatewayapi.HTTPRoute
	)

	BeforeEach(func(ctx SpecContext) {
		nsName = fmt.Sprintf("test-llmisvc-validation-%d", time.Now().UnixNano())

		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: nsName,
			},
		}
		Expect(envTest.Client.Create(ctx, ns)).To(Succeed())

		gateway = fixture.Gateway("test-gateway",
			fixture.InNamespace[*gatewayapi.Gateway](nsName),
			fixture.WithClassName("test-gateway-class"),
			fixture.WithListener(gatewayapi.HTTPProtocolType),
		)
		Expect(envTest.Client.Create(ctx, gateway)).To(Succeed())

		httpRoute = fixture.HTTPRoute("test-route",
			fixture.InNamespace[*gatewayapi.HTTPRoute](nsName),
			fixture.WithParentRef(fixture.GatewayRef("test-gateway")),
			fixture.WithPath("/test"),
		)
		Expect(envTest.Client.Create(ctx, httpRoute)).To(Succeed())

		DeferCleanup(func() {
			httpRoute := httpRoute
			gateway := gateway
			ns := ns
			envTest.DeleteAll(httpRoute, gateway, ns)
		})
	})

	Context("cross-field constraints validation", func() {
		It("should reject LLMInferenceService with both refs and spec in HTTPRoute", func(ctx SpecContext) {
			// given
			modelURL, _ := apis.ParseURL("hf://facebook/opt-125m")
			pathPrefix := gatewayapi.PathMatchPathPrefix
			llmSvc := &v1alpha1.LLMInferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-both-refs-and-spec",
					Namespace: nsName,
				},
				Spec: v1alpha1.LLMInferenceServiceSpec{
					Model: v1alpha1.LLMModelSpec{
						URI: *modelURL,
					},
					Router: &v1alpha1.RouterSpec{
						Route: &v1alpha1.GatewayRoutesSpec{
							HTTP: &v1alpha1.HTTPRouteSpec{
								Refs: []corev1.LocalObjectReference{
									{Name: "test-route"},
								},
								Spec: &gatewayapi.HTTPRouteSpec{
									Rules: []gatewayapi.HTTPRouteRule{
										{
											Matches: []gatewayapi.HTTPRouteMatch{
												{
													Path: &gatewayapi.HTTPPathMatch{
														Type:  &pathPrefix,
														Value: ptr.To("/test"),
													},
												},
											},
											BackendRefs: []gatewayapi.HTTPBackendRef{
												{
													BackendRef: gatewayapi.BackendRef{
														BackendObjectReference: gatewayapi.BackendObjectReference{
															Name: "test-service",
															Port: ptr.To(gatewayapi.PortNumber(80)),
														},
														Weight: ptr.To(int32(1)),
													},
												},
											},
										},
									},
								},
							},
						},
					},
				},
			}

			// when
			errValidation := envTest.Client.Create(ctx, llmSvc)

			// then
			Expect(errValidation).To(HaveOccurred())
			Expect(errValidation.Error()).To(ContainSubstring("unsupported configuration"))
		})

		It("should reject LLMInferenceService with user-defined routes and managed gateway", func(ctx SpecContext) {
			// given
			modelURL, _ := apis.ParseURL("hf://facebook/opt-125m")
			llmSvc := &v1alpha1.LLMInferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-refs-with-managed-gateway",
					Namespace: nsName,
				},
				Spec: v1alpha1.LLMInferenceServiceSpec{
					Model: v1alpha1.LLMModelSpec{
						URI: *modelURL,
					},
					Router: &v1alpha1.RouterSpec{
						Route: &v1alpha1.GatewayRoutesSpec{
							HTTP: &v1alpha1.HTTPRouteSpec{
								Refs: []corev1.LocalObjectReference{
									{Name: "test-route"},
								},
							},
						},
						Gateway: &v1alpha1.GatewaySpec{},
					},
				},
			}

			// when
			errValidation := envTest.Client.Create(ctx, llmSvc)

			// then
			Expect(errValidation).To(HaveOccurred())
			Expect(errValidation.Error()).To(ContainSubstring("cannot be used with a managed gateway"))
		})

		It("should reject LLMInferenceService with managed route spec with gateway ref and user-defined gateway refs", func(ctx SpecContext) {
			// given
			modelURL, _ := apis.ParseURL("hf://facebook/opt-125m")
			pathPrefix := gatewayapi.PathMatchPathPrefix
			llmSvc := &v1alpha1.LLMInferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-spec-with-gateway-refs",
					Namespace: nsName,
				},
				Spec: v1alpha1.LLMInferenceServiceSpec{
					Model: v1alpha1.LLMModelSpec{
						URI: *modelURL,
					},
					Router: &v1alpha1.RouterSpec{
						Gateway: &v1alpha1.GatewaySpec{
							Refs: []v1alpha1.UntypedObjectReference{
								{
									Name:      gatewayapi.ObjectName("test-gateway"),
									Namespace: gatewayapi.Namespace(nsName),
								},
							},
						},
						Route: &v1alpha1.GatewayRoutesSpec{
							HTTP: &v1alpha1.HTTPRouteSpec{
								Spec: &gatewayapi.HTTPRouteSpec{
									CommonRouteSpec: gatewayapi.CommonRouteSpec{
										ParentRefs: []gatewayapi.ParentReference{
											{
												Name:      "test-gateway",
												Namespace: (*gatewayapi.Namespace)(&nsName),
											},
										},
									},
									Rules: []gatewayapi.HTTPRouteRule{
										{
											Matches: []gatewayapi.HTTPRouteMatch{
												{
													Path: &gatewayapi.HTTPPathMatch{
														Type:  &pathPrefix,
														Value: ptr.To("/test"),
													},
												},
											},
											BackendRefs: []gatewayapi.HTTPBackendRef{
												{
													BackendRef: gatewayapi.BackendRef{
														BackendObjectReference: gatewayapi.BackendObjectReference{
															Name: "custom-backend",
															Port: ptr.To(gatewayapi.PortNumber(8080)),
														},
														Weight: ptr.To(int32(1)),
													},
												},
											},
										},
									},
								},
							},
						},
					},
				},
			}

			// when
			errValidation := envTest.Client.Create(ctx, llmSvc)

			// then
			Expect(errValidation).To(HaveOccurred())
			Expect(errValidation.Error()).To(ContainSubstring("unsupported configuration"))
		})
	})

	Context("parallelism constraints validation", func() {
		It("should reject LLMInferenceService with both pipeline and data parallelism", func(ctx SpecContext) {
			// given
			modelURL, _ := apis.ParseURL("hf://facebook/opt-125m")
			llmSvc := &v1alpha1.LLMInferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-both-pipeline-and-data",
					Namespace: nsName,
				},
				Spec: v1alpha1.LLMInferenceServiceSpec{
					Model: v1alpha1.LLMModelSpec{
						URI: *modelURL,
					},
					WorkloadSpec: v1alpha1.WorkloadSpec{
						Parallelism: &v1alpha1.ParallelismSpec{
							Pipeline:  ptr.To(int32(2)),
							Data:      ptr.To(int32(4)),
							DataLocal: ptr.To(int32(2)),
						},
					},
				},
			}

			// when
			errValidation := envTest.Client.Create(ctx, llmSvc)

			// then
			Expect(errValidation).To(HaveOccurred())
			Expect(errValidation.Error()).To(ContainSubstring("cannot set both pipeline parallelism and data parallelism"))
		})

		It("should reject LLMInferenceService with data parallelism but missing dataLocal", func(ctx SpecContext) {
			// given
			modelURL, _ := apis.ParseURL("hf://facebook/opt-125m")
			llmSvc := &v1alpha1.LLMInferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-data-without-datalocal",
					Namespace: nsName,
				},
				Spec: v1alpha1.LLMInferenceServiceSpec{
					Model: v1alpha1.LLMModelSpec{
						URI: *modelURL,
					},
					WorkloadSpec: v1alpha1.WorkloadSpec{
						Parallelism: &v1alpha1.ParallelismSpec{
							Data: ptr.To(int32(4)),
						},
					},
				},
			}

			// when
			errValidation := envTest.Client.Create(ctx, llmSvc)

			// then
			Expect(errValidation).To(HaveOccurred())
			Expect(errValidation.Error()).To(ContainSubstring("dataLocal must be set when data is set"))
		})

		It("should reject LLMInferenceService with dataLocal parallelism but missing data", func(ctx SpecContext) {
			// given
			modelURL, _ := apis.ParseURL("hf://facebook/opt-125m")
			llmSvc := &v1alpha1.LLMInferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-datalocal-without-data",
					Namespace: nsName,
				},
				Spec: v1alpha1.LLMInferenceServiceSpec{
					Model: v1alpha1.LLMModelSpec{
						URI: *modelURL,
					},
					WorkloadSpec: v1alpha1.WorkloadSpec{
						Parallelism: &v1alpha1.ParallelismSpec{
							DataLocal: ptr.To(int32(2)),
						},
					},
				},
			}

			// when
			errValidation := envTest.Client.Create(ctx, llmSvc)

			// then
			Expect(errValidation).To(HaveOccurred())
			Expect(errValidation.Error()).To(ContainSubstring("data must be set when dataLocal is set"))
		})

		It("should reject LLMInferenceService with worker but no parallelism configuration", func(ctx SpecContext) {
			// given
			modelURL, _ := apis.ParseURL("hf://facebook/opt-125m")
			llmSvc := &v1alpha1.LLMInferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-worker-no-parallelism",
					Namespace: nsName,
				},
				Spec: v1alpha1.LLMInferenceServiceSpec{
					Model: v1alpha1.LLMModelSpec{
						URI: *modelURL,
					},
					WorkloadSpec: v1alpha1.WorkloadSpec{
						Worker: &corev1.PodSpec{
							Containers: []corev1.Container{
								{Name: "worker", Image: "test:latest"},
							},
						},
					},
				},
			}

			// when
			errValidation := envTest.Client.Create(ctx, llmSvc)

			// then
			Expect(errValidation).To(HaveOccurred())
			Expect(errValidation.Error()).To(ContainSubstring("when worker is specified, parallelism must be configured"))
		})

		It("should reject LLMInferenceService with prefill having both pipeline and data parallelism", func(ctx SpecContext) {
			// given
			modelURL, _ := apis.ParseURL("hf://facebook/opt-125m")
			llmSvc := &v1alpha1.LLMInferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-prefill-both-parallelism",
					Namespace: nsName,
				},
				Spec: v1alpha1.LLMInferenceServiceSpec{
					Model: v1alpha1.LLMModelSpec{
						URI: *modelURL,
					},
					Prefill: &v1alpha1.WorkloadSpec{
						Parallelism: &v1alpha1.ParallelismSpec{
							Pipeline:  ptr.To(int32(2)),
							Data:      ptr.To(int32(4)),
							DataLocal: ptr.To(int32(2)),
						},
					},
				},
			}

			// when
			errValidation := envTest.Client.Create(ctx, llmSvc)

			// then
			Expect(errValidation).To(HaveOccurred())
			Expect(errValidation.Error()).To(ContainSubstring("cannot set both pipeline parallelism and data parallelism"))
		})

		It("should reject LLMInferenceService with prefill worker but no parallelism configuration", func(ctx SpecContext) {
			// given
			modelURL, _ := apis.ParseURL("hf://facebook/opt-125m")
			llmSvc := &v1alpha1.LLMInferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-prefill-worker-no-parallelism",
					Namespace: nsName,
				},
				Spec: v1alpha1.LLMInferenceServiceSpec{
					Model: v1alpha1.LLMModelSpec{
						URI: *modelURL,
					},
					Prefill: &v1alpha1.WorkloadSpec{
						Worker: &corev1.PodSpec{
							Containers: []corev1.Container{
								{Name: "worker", Image: "test:latest"},
							},
						},
					},
				},
			}

			// when
			errValidation := envTest.Client.Create(ctx, llmSvc)

			// then
			Expect(errValidation).To(HaveOccurred())
			Expect(errValidation.Error()).To(ContainSubstring("when worker is specified, parallelism must be configured"))
		})

		It("should accept LLMInferenceService with valid pipeline parallelism", func(ctx SpecContext) {
			// given
			modelURL, _ := apis.ParseURL("hf://facebook/opt-125m")
			llmSvc := &v1alpha1.LLMInferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-valid-pipeline",
					Namespace: nsName,
				},
				Spec: v1alpha1.LLMInferenceServiceSpec{
					Model: v1alpha1.LLMModelSpec{
						URI: *modelURL,
					},
					WorkloadSpec: v1alpha1.WorkloadSpec{
						Parallelism: &v1alpha1.ParallelismSpec{
							Pipeline: ptr.To(int32(2)),
						},
						Worker: &corev1.PodSpec{
							Containers: []corev1.Container{
								{Name: "worker", Image: "test:latest"},
							},
						},
					},
				},
			}

			// then
			Expect(envTest.Client.Create(ctx, llmSvc)).To(Succeed())
		})

		It("should accept LLMInferenceService with valid data parallelism", func(ctx SpecContext) {
			// given
			modelURL, _ := apis.ParseURL("hf://facebook/opt-125m")
			llmSvc := &v1alpha1.LLMInferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-valid-data",
					Namespace: nsName,
				},
				Spec: v1alpha1.LLMInferenceServiceSpec{
					Model: v1alpha1.LLMModelSpec{
						URI: *modelURL,
					},
					WorkloadSpec: v1alpha1.WorkloadSpec{
						Parallelism: &v1alpha1.ParallelismSpec{
							Data:      ptr.To(int32(4)),
							DataLocal: ptr.To(int32(2)),
						},
						Worker: &corev1.PodSpec{
							Containers: []corev1.Container{
								{Name: "worker", Image: "test:latest"},
							},
						},
					},
				},
			}

			// then
			Expect(envTest.Client.Create(ctx, llmSvc)).To(Succeed())
		})

		It("should accept LLMInferenceService with valid prefill parallelism configuration", func(ctx SpecContext) {
			// given
			modelURL, _ := apis.ParseURL("hf://facebook/opt-125m")
			llmSvc := &v1alpha1.LLMInferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-valid-prefill",
					Namespace: nsName,
				},
				Spec: v1alpha1.LLMInferenceServiceSpec{
					Model: v1alpha1.LLMModelSpec{
						URI: *modelURL,
					},
					Prefill: &v1alpha1.WorkloadSpec{
						Parallelism: &v1alpha1.ParallelismSpec{
							Pipeline: ptr.To(int32(2)),
						},
						Worker: &corev1.PodSpec{
							Containers: []corev1.Container{
								{Name: "worker", Image: "test:latest"},
							},
						},
					},
				},
			}

			// then
			Expect(envTest.Client.Create(ctx, llmSvc)).To(Succeed())
		})

		It("should reject LLMInferenceService update with different decode parallelism 'size'", func(ctx SpecContext) {
			name := "test-update-decode-parallelism-different-size"
			// given
			modelURL, _ := apis.ParseURL("hf://facebook/opt-125m")
			llmSvc := &v1alpha1.LLMInferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: nsName,
				},
				Spec: v1alpha1.LLMInferenceServiceSpec{
					Model: v1alpha1.LLMModelSpec{
						URI: *modelURL,
					},
					WorkloadSpec: v1alpha1.WorkloadSpec{
						Parallelism: &v1alpha1.ParallelismSpec{
							Data:      ptr.To(int32(1)),
							DataLocal: ptr.To(int32(8)),
						},
						Worker: &corev1.PodSpec{
							Containers: []corev1.Container{
								{Name: "worker", Image: "test:latest"},
							},
						},
					},
				},
			}

			// Consistency check
			Expect(llmSvc.Spec.Parallelism.GetSize()).To(Equal(ptr.To(int32(1))))
			Expect(envTest.Client.Create(ctx, llmSvc)).To(Succeed())

			updated := &v1alpha1.LLMInferenceService{}
			Expect(envTest.Client.Get(ctx, types.NamespacedName{Namespace: llmSvc.GetNamespace(), Name: llmSvc.GetName()}, updated)).To(Succeed())

			updated.Spec.Parallelism.Data = ptr.To[int32](8)
			updated.Spec.Parallelism.DataLocal = ptr.To[int32](1)

			// Consistency check
			Expect(updated.Spec.Parallelism.GetSize()).To(Equal(ptr.To(int32(8))))

			// then
			Expect(envTest.Client.Update(ctx, updated)).To(HaveOccurred())
		})

		It("should reject LLMInferenceService update with different prefill parallelism 'size'", func(ctx SpecContext) {
			name := "test-update-prefill-parallelism-different-size"
			// given
			modelURL, _ := apis.ParseURL("hf://facebook/opt-125m")
			llmSvc := &v1alpha1.LLMInferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: nsName,
				},
				Spec: v1alpha1.LLMInferenceServiceSpec{
					Model: v1alpha1.LLMModelSpec{
						URI: *modelURL,
					},
					Prefill: &v1alpha1.WorkloadSpec{
						Parallelism: &v1alpha1.ParallelismSpec{
							Data:      ptr.To(int32(1)),
							DataLocal: ptr.To(int32(8)),
						},
						Worker: &corev1.PodSpec{
							Containers: []corev1.Container{
								{Name: "worker", Image: "test:latest"},
							},
						},
					},
				},
			}

			// Consistency check
			Expect(llmSvc.Spec.Prefill.Parallelism.GetSize()).To(Equal(ptr.To(int32(1))))
			Expect(envTest.Client.Create(ctx, llmSvc)).To(Succeed())

			updated := &v1alpha1.LLMInferenceService{}
			Expect(envTest.Client.Get(ctx, types.NamespacedName{Namespace: llmSvc.GetNamespace(), Name: llmSvc.GetName()}, updated)).To(Succeed())

			updated.Spec.Prefill.Parallelism.Data = ptr.To[int32](9)
			updated.Spec.Prefill.Parallelism.DataLocal = ptr.To[int32](1)

			// Consistency check
			Expect(updated.Spec.Prefill.Parallelism.GetSize()).To(Equal(ptr.To[int32](9)))

			// then
			Expect(envTest.Client.Update(ctx, updated)).To(HaveOccurred())
		})

		It("should accept LLMInferenceService without parallelism configuration", func(ctx SpecContext) {
			// given
			modelURL, _ := apis.ParseURL("hf://facebook/opt-125m")
			llmSvc := &v1alpha1.LLMInferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-no-parallelism",
					Namespace: nsName,
				},
				Spec: v1alpha1.LLMInferenceServiceSpec{
					Model: v1alpha1.LLMModelSpec{
						URI: *modelURL,
					},
				},
			}

			// then
			Expect(envTest.Client.Create(ctx, llmSvc)).To(Succeed())
		})
	})
})

var _ = Describe("LLMInferenceService API validation", func() {
	var (
		ns     *corev1.Namespace
		nsName string
	)
	BeforeEach(func(ctx SpecContext) {
		nsName = fmt.Sprintf("test-llmisvc-api-validation-%d", time.Now().UnixNano())

		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: nsName,
			},
		}
		Expect(envTest.Client.Create(ctx, ns)).To(Succeed())

		DeferCleanup(func() {
			ns := ns
			envTest.DeleteAll(ns)
		})
	})
	Context("Integer value validation", func() {
		It("should reject LLMInferenceService with negative workload replicas", func(ctx SpecContext) {
			// given
			modelURL, _ := apis.ParseURL("hf://facebook/opt-125m")
			llmSvc := &v1alpha1.LLMInferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-negative-replicas",
					Namespace: nsName,
				},
				Spec: v1alpha1.LLMInferenceServiceSpec{
					Model: v1alpha1.LLMModelSpec{
						URI: *modelURL,
					},
					WorkloadSpec: v1alpha1.WorkloadSpec{
						Replicas: ptr.To(int32(-1)),
					},
				},
			}

			// when
			errValidation := envTest.Client.Create(ctx, llmSvc)

			// then
			Expect(errValidation).To(HaveOccurred(), "Expected the Create call to fail due to a validation error, but it succeeded")
			Expect(errValidation.Error()).To(ContainSubstring("spec.replicas in body should be greater than or equal to 0"))
		})

		It("should reject LLMInferenceService with negative tensor parallelism", func(ctx SpecContext) {
			// given
			modelURL, _ := apis.ParseURL("hf://facebook/opt-125m")
			llmSvc := &v1alpha1.LLMInferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-negative-int-parallelism",
					Namespace: nsName,
				},
				Spec: v1alpha1.LLMInferenceServiceSpec{
					Model: v1alpha1.LLMModelSpec{
						URI: *modelURL,
					},
					WorkloadSpec: v1alpha1.WorkloadSpec{
						Parallelism: &v1alpha1.ParallelismSpec{
							Tensor: ptr.To(int32(-1)),
						},
					},
				},
			}

			// when
			errValidation := envTest.Client.Create(ctx, llmSvc)

			// then
			Expect(errValidation).To(HaveOccurred(), "Expected the Create call to fail due to a validation error, but it succeeded")
			Expect(errValidation.Error()).To(ContainSubstring("spec.parallelism.tensor in body should be greater than or equal to 1"))
		})

		It("should reject LLMInferenceService with negative pipeline parallelism", func(ctx SpecContext) {
			// given
			modelURL, _ := apis.ParseURL("hf://facebook/opt-125m")
			llmSvc := &v1alpha1.LLMInferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-negative-int-pipeline",
					Namespace: nsName,
				},
				Spec: v1alpha1.LLMInferenceServiceSpec{
					Model: v1alpha1.LLMModelSpec{
						URI: *modelURL,
					},
					WorkloadSpec: v1alpha1.WorkloadSpec{
						Parallelism: &v1alpha1.ParallelismSpec{
							Pipeline: ptr.To(int32(-1)),
						},
					},
				},
			}

			// when
			errValidation := envTest.Client.Create(ctx, llmSvc)

			// then
			Expect(errValidation).To(HaveOccurred(), "Expected the Create call to fail due to a validation error, but it succeeded")
			Expect(errValidation.Error()).To(ContainSubstring("spec.parallelism.pipeline in body should be greater than or equal to 1"))
		})

		It("should reject LLMInferenceService with negative data parallelism", func(ctx SpecContext) {
			// given
			modelURL, _ := apis.ParseURL("hf://facebook/opt-125m")
			llmSvc := &v1alpha1.LLMInferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-negative-data",
					Namespace: nsName,
				},
				Spec: v1alpha1.LLMInferenceServiceSpec{
					Model: v1alpha1.LLMModelSpec{
						URI: *modelURL,
					},
					WorkloadSpec: v1alpha1.WorkloadSpec{
						Parallelism: &v1alpha1.ParallelismSpec{
							Data:      ptr.To(int32(-1)),
							DataLocal: ptr.To(int32(1)),
						},
					},
				},
			}

			// when
			errValidation := envTest.Client.Create(ctx, llmSvc)

			// then
			Expect(errValidation).To(HaveOccurred(), "Expected the Create call to fail due to a validation error, but it succeeded")
			Expect(errValidation.Error()).To(ContainSubstring("spec.parallelism.data in body should be greater than or equal to 1"))
		})

		It("should reject LLMInferenceService with zero dataLocal parallelism", func(ctx SpecContext) {
			// given
			modelURL, _ := apis.ParseURL("hf://facebook/opt-125m")
			llmSvc := &v1alpha1.LLMInferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-negative-datalocal",
					Namespace: nsName,
				},
				Spec: v1alpha1.LLMInferenceServiceSpec{
					Model: v1alpha1.LLMModelSpec{
						URI: *modelURL,
					},
					WorkloadSpec: v1alpha1.WorkloadSpec{
						Parallelism: &v1alpha1.ParallelismSpec{
							Data:      ptr.To(int32(4)),
							DataLocal: ptr.To(int32(0)),
						},
					},
				},
			}

			// when
			errValidation := envTest.Client.Create(ctx, llmSvc)

			// then
			Expect(errValidation).To(HaveOccurred(), "Expected the Create call to fail due to a validation error, but it succeeded")
			Expect(errValidation.Error()).To(ContainSubstring("spec.parallelism.dataLocal in body should be greater than or equal to 1"))
		})

		It("should reject LLMInferenceService with zero data parallelism RPC Port", func(ctx SpecContext) {
			// given
			modelURL, _ := apis.ParseURL("hf://facebook/opt-125m")
			llmSvc := &v1alpha1.LLMInferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-zero-data-rpc-port",
					Namespace: nsName,
				},
				Spec: v1alpha1.LLMInferenceServiceSpec{
					Model: v1alpha1.LLMModelSpec{
						URI: *modelURL,
					},
					WorkloadSpec: v1alpha1.WorkloadSpec{
						Parallelism: &v1alpha1.ParallelismSpec{
							DataRPCPort: ptr.To(int32(0)),
						},
					},
				},
			}

			// when
			errValidation := envTest.Client.Create(ctx, llmSvc)

			// then
			Expect(errValidation).To(HaveOccurred(), "Expected the Create call to fail due to a validation error, but it succeeded")
			Expect(errValidation.Error()).To(ContainSubstring("spec.parallelism.dataRPCPort in body should be greater than or equal to 1"))
		})

		It("should reject LLMInferenceService with too large data parallelism RPC Port", func(ctx SpecContext) {
			// given
			modelURL, _ := apis.ParseURL("hf://facebook/opt-125m")
			llmSvc := &v1alpha1.LLMInferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-max-data-rpc-port-exceeded",
					Namespace: nsName,
				},
				Spec: v1alpha1.LLMInferenceServiceSpec{
					Model: v1alpha1.LLMModelSpec{
						URI: *modelURL,
					},
					WorkloadSpec: v1alpha1.WorkloadSpec{
						Parallelism: &v1alpha1.ParallelismSpec{
							DataRPCPort: ptr.To(int32(99999)),
						},
					},
				},
			}

			// when
			errValidation := envTest.Client.Create(ctx, llmSvc)

			// then
			Expect(errValidation).To(HaveOccurred(), "Expected the Create call to fail due to a validation error, but it succeeded")
			Expect(errValidation.Error()).To(ContainSubstring("spec.parallelism.dataRPCPort in body should be less than or equal to 65535"))
		})
	})
})
