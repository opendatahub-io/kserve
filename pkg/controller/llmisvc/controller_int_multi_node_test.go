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
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	lwsapi "sigs.k8s.io/lws/api/leaderworkerset/v1"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"knative.dev/pkg/apis"
	"knative.dev/pkg/kmeta"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha1"

	. "github.com/kserve/kserve/pkg/controller/llmisvc/fixture"
	. "github.com/kserve/kserve/pkg/testing"
)

var _ = Describe("LLMInferenceService Multi-Node Controller", func() {
	Context("Multi-Node Workload Reconciliation", func() {
		It("should create a basic multi-node deployment with worker spec", func(ctx SpecContext) {
			// given
			svcName := "test-llm-multinode"
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
					WorkloadSpec: v1alpha1.WorkloadSpec{
						Replicas: ptr.To[int32](2),
						Parallelism: &v1alpha1.ParallelismSpec{
							Data:      ptr.To[int32](4),
							DataLocal: ptr.To[int32](1),
							Tensor:    ptr.To[int32](3),
						},
						Template: &corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "main",
									Image: "quay.io/pierdipi/vllm-cpu:latest",
									Resources: corev1.ResourceRequirements{
										Limits: corev1.ResourceList{
											corev1.ResourceCPU:    resource.MustParse("1"),
											corev1.ResourceMemory: resource.MustParse("4Gi"),
										},
									},
								},
							},
						},
						Worker: &corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "main",
									Image: "quay.io/pierdipi/vllm-cpu:latest",
									Resources: corev1.ResourceRequirements{
										Limits: corev1.ResourceList{
											corev1.ResourceCPU:    resource.MustParse("500m"),
											corev1.ResourceMemory: resource.MustParse("2Gi"),
										},
									},
								},
							},
						},
					},
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
			expectedLWS := &lwsapi.LeaderWorkerSet{}
			Eventually(func(g Gomega, ctx context.Context) error {
				return envTest.Get(ctx, types.NamespacedName{
					Name:      svcName + "-kserve-mn",
					Namespace: nsName,
				}, expectedLWS)
			}).WithTimeout(30 * time.Second).WithPolling(time.Second).WithContext(ctx).Should(Succeed())

			Expect(expectedLWS.Spec.Replicas).To(Equal(ptr.To[int32](2)))
			Expect(expectedLWS.Spec.LeaderWorkerTemplate.Size).To(Equal(ptr.To[int32](4)))
			Expect(expectedLWS).To(BeOwnedBy(llmSvc))

			// Verify leader template is set
			Expect(expectedLWS.Spec.LeaderWorkerTemplate.LeaderTemplate).ToNot(BeNil())
			Expect(expectedLWS.Spec.LeaderWorkerTemplate.LeaderTemplate.Spec.Containers).To(HaveLen(1))
			Expect(expectedLWS.Spec.LeaderWorkerTemplate.LeaderTemplate.Spec.Containers[0].Name).To(Equal("main"))

			// Verify worker template is set
			Expect(expectedLWS.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec.Containers).To(HaveLen(1))
			Expect(expectedLWS.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec.Containers[0].Name).To(Equal("main"))

			// Verify labels
			Expect(expectedLWS.Spec.LeaderWorkerTemplate.LeaderTemplate.Labels).To(HaveKeyWithValue("kserve.io/component", "workload"))
			Expect(expectedLWS.Spec.LeaderWorkerTemplate.LeaderTemplate.Labels).To(HaveKeyWithValue("llm-d.ai/role", "decode"))
		})

		It("should create multi-node deployment with prefill workload", func(ctx SpecContext) {
			// given
			svcName := "test-llm-multinode-prefill"
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
					WorkloadSpec: v1alpha1.WorkloadSpec{
						Replicas: ptr.To[int32](1),
						Parallelism: &v1alpha1.ParallelismSpec{
							Data:      ptr.To[int32](10),
							DataLocal: ptr.To[int32](2),
							Tensor:    ptr.To[int32](4),
						},
						Worker: &corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "main",
									Image: "quay.io/pierdipi/vllm-cpu:latest",
									Resources: corev1.ResourceRequirements{
										Limits: corev1.ResourceList{
											corev1.ResourceCPU:    resource.MustParse("500m"),
											corev1.ResourceMemory: resource.MustParse("2Gi"),
										},
									},
								},
							},
						},
					},
					Prefill: &v1alpha1.WorkloadSpec{
						Replicas: ptr.To[int32](1),
						Parallelism: &v1alpha1.ParallelismSpec{
							Data:      ptr.To[int32](3),
							DataLocal: ptr.To[int32](1),
							Tensor:    ptr.To[int32](4),
						},
						Template: &corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "main",
									Image: "quay.io/pierdipi/vllm-prefill:latest",
									Resources: corev1.ResourceRequirements{
										Limits: corev1.ResourceList{
											corev1.ResourceCPU:    resource.MustParse("1"),
											corev1.ResourceMemory: resource.MustParse("4Gi"),
										},
									},
								},
							},
						},
						Worker: &corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "main",
									Image: "quay.io/pierdipi/vllm-prefill:latest",
									Resources: corev1.ResourceRequirements{
										Limits: corev1.ResourceList{
											corev1.ResourceCPU:    resource.MustParse("500m"),
											corev1.ResourceMemory: resource.MustParse("2Gi"),
										},
									},
								},
							},
						},
					},
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

			// then - Check main workload LWS
			expectedMainLWS := &lwsapi.LeaderWorkerSet{}
			Eventually(func(g Gomega, ctx context.Context) error {
				return envTest.Get(ctx, types.NamespacedName{
					Name:      svcName + "-kserve-mn",
					Namespace: nsName,
				}, expectedMainLWS)
			}).WithTimeout(30 * time.Second).WithPolling(time.Second).WithContext(ctx).Should(Succeed())

			Expect(expectedMainLWS.Spec.Replicas).To(Equal(ptr.To[int32](1)))
			Expect(expectedMainLWS.Spec.LeaderWorkerTemplate.Size).To(Equal(ptr.To[int32](5)))

			// then - Check prefill workload LWS
			expectedPrefillLWS := &lwsapi.LeaderWorkerSet{}
			Eventually(func(g Gomega, ctx context.Context) error {
				return envTest.Get(ctx, types.NamespacedName{
					Name:      svcName + "-kserve-mn-prefill",
					Namespace: nsName,
				}, expectedPrefillLWS)
			}).WithTimeout(30 * time.Second).WithPolling(time.Second).WithContext(ctx).Should(Succeed())

			Expect(expectedPrefillLWS.Spec.Replicas).To(Equal(ptr.To[int32](1)))
			Expect(expectedPrefillLWS.Spec.LeaderWorkerTemplate.Size).To(Equal(ptr.To[int32](3)))
			Expect(expectedPrefillLWS).To(BeOwnedBy(llmSvc))

			// Verify prefill-specific labels
			Expect(expectedPrefillLWS.Spec.LeaderWorkerTemplate.LeaderTemplate.Labels).To(HaveKeyWithValue("llm-d.ai/role", "prefill"))
		})

		It("should create RBAC resources when routing sidecar is present", func(ctx SpecContext) {
			// given
			svcName := "test-llm-multinode-rbac"
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
					WorkloadSpec: v1alpha1.WorkloadSpec{
						Replicas: ptr.To[int32](1),
						Parallelism: &v1alpha1.ParallelismSpec{
							Data:      ptr.To[int32](2),
							DataLocal: ptr.To[int32](1),
							Tensor:    ptr.To[int32](4),
						},
						Template: &corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "main",
									Image: "quay.io/pierdipi/vllm-cpu:latest",
								},
							},
							InitContainers: []corev1.Container{
								{
									Name:  "llm-d-routing-sidecar",
									Image: "quay.io/kserve/router:latest",
								},
							},
						},
						Worker: &corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "main",
									Image: "quay.io/pierdipi/vllm-cpu:latest",
								},
							},
							InitContainers: []corev1.Container{
								{
									Name:  "llm-d-routing-sidecar",
									Image: "quay.io/kserve/router:latest",
								},
							},
						},
					},
					Router: &v1alpha1.RouterSpec{
						Route:   &v1alpha1.GatewayRoutesSpec{},
						Gateway: &v1alpha1.GatewaySpec{},
						Scheduler: &v1alpha1.SchedulerSpec{
							Template: &corev1.PodSpec{
								Containers: []corev1.Container{
									{
										Name:  "main",
										Image: "quay.io/kserve/router:latest",
									},
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

			// then - Check ServiceAccount is created
			expectedSA := &corev1.ServiceAccount{}
			Eventually(func(g Gomega, ctx context.Context) error {
				return envTest.Get(ctx, types.NamespacedName{
					Name:      svcName + "-kserve-mn",
					Namespace: nsName,
				}, expectedSA)
			}).WithTimeout(30 * time.Second).WithPolling(time.Second).WithContext(ctx).Should(Succeed())

			Expect(expectedSA).To(BeOwnedBy(llmSvc))
			Expect(expectedSA.Labels).To(HaveKeyWithValue("app.kubernetes.io/name", svcName))

			// then - Check Role is created
			expectedRole := &rbacv1.Role{}
			Eventually(func(g Gomega, ctx context.Context) error {
				return envTest.Get(ctx, types.NamespacedName{
					Name:      svcName + "-kserve-mn-role",
					Namespace: nsName,
				}, expectedRole)
			}).WithTimeout(30 * time.Second).WithPolling(time.Second).WithContext(ctx).Should(Succeed())

			Expect(expectedRole).To(BeOwnedBy(llmSvc))
			Expect(expectedRole.Rules).ToNot(BeEmpty())

			// then - Check RoleBinding is created
			expectedRB := &rbacv1.RoleBinding{}
			Eventually(func(g Gomega, ctx context.Context) error {
				return envTest.Get(ctx, types.NamespacedName{
					Name:      svcName + "-kserve-mn-rb",
					Namespace: nsName,
				}, expectedRB)
			}).WithTimeout(30 * time.Second).WithPolling(time.Second).WithContext(ctx).Should(Succeed())

			Expect(expectedRB).To(BeOwnedBy(llmSvc))
			Expect(expectedRB.Subjects).To(HaveLen(1))
			Expect(expectedRB.Subjects[0].Name).To(Equal(expectedSA.Name))
			Expect(expectedRB.RoleRef.Name).To(Equal(expectedRole.Name))

			// then - Check LWS uses the ServiceAccount
			expectedLWS := &lwsapi.LeaderWorkerSet{}
			Eventually(func(g Gomega, ctx context.Context) error {
				return envTest.Get(ctx, types.NamespacedName{
					Name:      svcName + "-kserve-mn",
					Namespace: nsName,
				}, expectedLWS)
			}).WithTimeout(30 * time.Second).WithPolling(time.Second).WithContext(ctx).Should(Succeed())

			Expect(expectedLWS.Spec.LeaderWorkerTemplate.LeaderTemplate.Spec.ServiceAccountName).To(Equal(expectedSA.Name))
			Expect(expectedLWS.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec.ServiceAccountName).To(Equal(expectedSA.Name))
		})

		It("should delete multi-node resources when worker spec is removed", func(ctx SpecContext) {
			// given
			svcName := "test-llm-multinode-cleanup"
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
					WorkloadSpec: v1alpha1.WorkloadSpec{
						Worker: &corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "main",
									Image: "quay.io/pierdipi/vllm-cpu:latest",
								},
							},
						},
					},
					Router: &v1alpha1.RouterSpec{
						Route:   &v1alpha1.GatewayRoutesSpec{},
						Gateway: &v1alpha1.GatewaySpec{},
					},
				},
			}

			Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
			defer func() {
				Expect(envTest.Delete(ctx, llmSvc)).To(Succeed())
			}()

			// Verify LWS is created
			Eventually(func(g Gomega, ctx context.Context) error {
				lws := &lwsapi.LeaderWorkerSet{}
				return envTest.Get(ctx, types.NamespacedName{
					Name:      svcName + "-kserve-mn",
					Namespace: nsName,
				}, lws)
			}).WithTimeout(30 * time.Second).WithPolling(time.Second).WithContext(ctx).Should(Succeed())

			// when - Remove worker spec
			errRetry := retry.RetryOnConflict(retry.DefaultRetry, func() error {
				_, errUpdate := ctrl.CreateOrUpdate(ctx, envTest.Client, llmSvc, func() error {
					llmSvc.Spec.Worker = nil
					return nil
				})
				return errUpdate
			})
			Expect(errRetry).ToNot(HaveOccurred())

			// then - LWS should be deleted
			Eventually(func(g Gomega, ctx context.Context) error {
				lws := &lwsapi.LeaderWorkerSet{}
				err := envTest.Get(ctx, types.NamespacedName{
					Name:      svcName + "-kserve-mn",
					Namespace: nsName,
				}, lws)
				g.Expect(err).To(HaveOccurred())
				return nil
			}).WithTimeout(30 * time.Second).WithPolling(time.Second).WithContext(ctx).Should(Succeed())
		})

		It("should delete prefill resources when prefill spec is removed", func(ctx SpecContext) {
			// given
			svcName := "test-llm-prefill-cleanup"
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
					Prefill: &v1alpha1.WorkloadSpec{
						Worker: &corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "main",
									Image: "quay.io/pierdipi/vllm-prefill:latest",
								},
							},
						},
					},
					Router: &v1alpha1.RouterSpec{
						Route:   &v1alpha1.GatewayRoutesSpec{},
						Gateway: &v1alpha1.GatewaySpec{},
					},
				},
			}

			Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
			defer func() {
				Expect(envTest.Delete(ctx, llmSvc)).To(Succeed())
			}()

			// Verify prefill LWS is created
			Eventually(func(g Gomega, ctx context.Context) error {
				lws := &lwsapi.LeaderWorkerSet{}
				return envTest.Get(ctx, types.NamespacedName{
					Name:      svcName + "-kserve-mn-prefill",
					Namespace: nsName,
				}, lws)
			}).WithTimeout(30 * time.Second).WithPolling(time.Second).WithContext(ctx).Should(Succeed())

			// when - Remove prefill spec
			errRetry := retry.RetryOnConflict(retry.DefaultRetry, func() error {
				_, errUpdate := ctrl.CreateOrUpdate(ctx, envTest.Client, llmSvc, func() error {
					llmSvc.Spec.Prefill = nil
					return nil
				})
				return errUpdate
			})
			Expect(errRetry).ToNot(HaveOccurred())

			// then - Prefill LWS should be deleted
			Eventually(func(g Gomega, ctx context.Context) error {
				lws := &lwsapi.LeaderWorkerSet{}
				err := envTest.Get(ctx, types.NamespacedName{
					Name:      svcName + "-kserve-mn-prefill",
					Namespace: nsName,
				}, lws)
				g.Expect(err).To(HaveOccurred())
				return nil
			}).WithTimeout(30 * time.Second).WithPolling(time.Second).WithContext(ctx).Should(Succeed())
		})
	})

	Context("Multi-Node Label Management", func() {
		It("should set correct labels when no leader template is provided", func(ctx SpecContext) {
			// given
			svcName := "test-llm-no-leader"
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
					WorkloadSpec: v1alpha1.WorkloadSpec{
						Parallelism: &v1alpha1.ParallelismSpec{
							Data:      ptr.To[int32](1),
							DataLocal: ptr.To[int32](1),
						},
						// No Template specified, only Worker
						Worker: &corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "main",
									Image: "quay.io/pierdipi/vllm-cpu:latest",
								},
							},
						},
					},
					Router: &v1alpha1.RouterSpec{
						Route:   &v1alpha1.GatewayRoutesSpec{},
						Gateway: &v1alpha1.GatewaySpec{},
					},
				},
			}

			// safety check
			Expect(llmSvc.Spec.Parallelism.IsDataParallel()).To(BeTrue())
			Expect(llmSvc.Spec.Worker).To(Not(BeNil()))

			// when
			Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
			defer func() {
				Expect(envTest.Delete(ctx, llmSvc)).To(Succeed())
			}()

			// then
			expectedLWS := &lwsapi.LeaderWorkerSet{}
			Eventually(func(g Gomega, ctx context.Context) error {
				return envTest.Get(ctx, types.NamespacedName{
					Name:      svcName + "-kserve-mn",
					Namespace: nsName,
				}, expectedLWS)
			}).WithTimeout(30 * time.Second).WithPolling(time.Second).WithContext(ctx).Should(Succeed())

			// When no leader template, workers should get InferencePool selector labels
			Expect(expectedLWS.Spec.LeaderWorkerTemplate.WorkerTemplate.Labels).To(HaveKeyWithValue("kserve.io/component", "workload"), fmt.Sprintf("%#v", expectedLWS))
			Expect(expectedLWS.Spec.LeaderWorkerTemplate.WorkerTemplate.Labels).To(HaveKeyWithValue("llm-d.ai/role", "decode"), fmt.Sprintf("%#v", expectedLWS))
		})
	})
})
