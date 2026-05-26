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

package inferenceservice

import (
	"context"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierr "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	knativeapis "knative.dev/pkg/apis"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
	"github.com/kserve/kserve/pkg/apis/serving/v1beta1"
	"github.com/kserve/kserve/pkg/constants"
)

var _ = Describe("Pod InitContainers Watch", func() {
	// Test the mapper function that maps pods to InferenceService reconcile requests
	Describe("podInitContainersFunc", func() {
		var reconciler *InferenceServiceReconciler

		BeforeEach(func() {
			// Note: Client is not needed for the podInitContainersFunc mapper
			// as it only reads labels from the pod object passed directly
			reconciler = &InferenceServiceReconciler{}
		})

		Context("when pod has the InferenceService label", func() {
			It("should return a reconcile request for the owning InferenceService", func() {
				pod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pod",
						Namespace: "default",
						Labels: map[string]string{
							constants.InferenceServicePodLabelKey: "my-isvc",
						},
					},
				}

				requests := reconciler.podInitContainersFunc(context.Background(), pod)

				Expect(requests).To(HaveLen(1))
				Expect(requests[0]).To(Equal(reconcile.Request{
					NamespacedName: types.NamespacedName{
						Namespace: "default",
						Name:      "my-isvc",
					},
				}))
			})

			It("should use the correct namespace from the pod", func() {
				pod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pod",
						Namespace: "custom-namespace",
						Labels: map[string]string{
							constants.InferenceServicePodLabelKey: "my-isvc",
						},
					},
				}

				requests := reconciler.podInitContainersFunc(context.Background(), pod)

				Expect(requests).To(HaveLen(1))
				Expect(requests[0].NamespacedName.Namespace).To(Equal("custom-namespace"))
				Expect(requests[0].NamespacedName.Name).To(Equal("my-isvc"))
			})
		})

		Context("when pod does not have the InferenceService label", func() {
			It("should return nil for pods without the label", func() {
				pod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pod",
						Namespace: "default",
						Labels:    map[string]string{},
					},
				}

				requests := reconciler.podInitContainersFunc(context.Background(), pod)

				Expect(requests).To(BeNil())
			})

			It("should return nil for pods with empty label value", func() {
				pod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pod",
						Namespace: "default",
						Labels: map[string]string{
							constants.InferenceServicePodLabelKey: "",
						},
					},
				}

				requests := reconciler.podInitContainersFunc(context.Background(), pod)

				Expect(requests).To(BeNil())
			})

			It("should return nil for pods with nil labels", func() {
				pod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pod",
						Namespace: "default",
					},
				}

				requests := reconciler.podInitContainersFunc(context.Background(), pod)

				Expect(requests).To(BeNil())
			})
		})

		Context("when object is not a pod", func() {
			It("should return nil for non-pod objects", func() {
				configMap := &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-configmap",
						Namespace: "default",
					},
				}

				requests := reconciler.podInitContainersFunc(context.Background(), configMap)

				Expect(requests).To(BeNil())
			})

			It("should return nil for nil object", func() {
				requests := reconciler.podInitContainersFunc(context.Background(), nil)

				Expect(requests).To(BeNil())
			})
		})
	})

	// Test the predicate that filters pod updates
	Describe("podInitContainersPredicate", func() {
		var pred predicate.Funcs

		BeforeEach(func() {
			pred = podInitContainersPredicate()
		})

		Describe("UpdateFunc", func() {
			Context("when pod has InferenceService label and InitContainerStatuses change", func() {
				It("should return true when InitContainerStatuses change from empty to waiting", func() {
					oldPod := &corev1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "test-pod",
							Namespace: "default",
							Labels: map[string]string{
								constants.InferenceServicePodLabelKey: "my-isvc",
							},
						},
						Status: corev1.PodStatus{
							InitContainerStatuses: []corev1.ContainerStatus{},
						},
					}

					newPod := &corev1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "test-pod",
							Namespace: "default",
							Labels: map[string]string{
								constants.InferenceServicePodLabelKey: "my-isvc",
							},
						},
						Status: corev1.PodStatus{
							InitContainerStatuses: []corev1.ContainerStatus{
								{
									Name: "storage-initializer",
									State: corev1.ContainerState{
										Waiting: &corev1.ContainerStateWaiting{
											Reason:  "PodInitializing",
											Message: "Initializing",
										},
									},
								},
							},
						},
					}

					result := pred.Update(event.UpdateEvent{
						ObjectOld: oldPod,
						ObjectNew: newPod,
					})

					Expect(result).To(BeTrue())
				})

				It("should return true when InitContainerStatuses change from waiting to terminated with error", func() {
					oldPod := &corev1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "test-pod",
							Namespace: "default",
							Labels: map[string]string{
								constants.InferenceServicePodLabelKey: "my-isvc",
							},
						},
						Status: corev1.PodStatus{
							InitContainerStatuses: []corev1.ContainerStatus{
								{
									Name: "storage-initializer",
									State: corev1.ContainerState{
										Waiting: &corev1.ContainerStateWaiting{
											Reason: "PodInitializing",
										},
									},
								},
							},
						},
					}

					newPod := &corev1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "test-pod",
							Namespace: "default",
							Labels: map[string]string{
								constants.InferenceServicePodLabelKey: "my-isvc",
							},
						},
						Status: corev1.PodStatus{
							InitContainerStatuses: []corev1.ContainerStatus{
								{
									Name: "storage-initializer",
									State: corev1.ContainerState{
										Terminated: &corev1.ContainerStateTerminated{
											ExitCode: 1,
											Reason:   "Error",
											Message:  "Failed to download model: certificate verify failed",
										},
									},
								},
							},
						},
					}

					result := pred.Update(event.UpdateEvent{
						ObjectOld: oldPod,
						ObjectNew: newPod,
					})

					Expect(result).To(BeTrue())
				})

				It("should return true when InitContainerStatuses change from waiting to running", func() {
					oldPod := &corev1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "test-pod",
							Namespace: "default",
							Labels: map[string]string{
								constants.InferenceServicePodLabelKey: "my-isvc",
							},
						},
						Status: corev1.PodStatus{
							InitContainerStatuses: []corev1.ContainerStatus{
								{
									Name: "storage-initializer",
									State: corev1.ContainerState{
										Waiting: &corev1.ContainerStateWaiting{
											Reason: "PodInitializing",
										},
									},
								},
							},
						},
					}

					newPod := &corev1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "test-pod",
							Namespace: "default",
							Labels: map[string]string{
								constants.InferenceServicePodLabelKey: "my-isvc",
							},
						},
						Status: corev1.PodStatus{
							InitContainerStatuses: []corev1.ContainerStatus{
								{
									Name: "storage-initializer",
									State: corev1.ContainerState{
										Running: &corev1.ContainerStateRunning{},
									},
								},
							},
						},
					}

					result := pred.Update(event.UpdateEvent{
						ObjectOld: oldPod,
						ObjectNew: newPod,
					})

					Expect(result).To(BeTrue())
				})
			})

			Context("when InitContainerStatuses do not change", func() {
				It("should return false when only other status fields change", func() {
					initStatus := []corev1.ContainerStatus{
						{
							Name: "storage-initializer",
							State: corev1.ContainerState{
								Waiting: &corev1.ContainerStateWaiting{
									Reason: "PodInitializing",
								},
							},
						},
					}

					oldPod := &corev1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "test-pod",
							Namespace: "default",
							Labels: map[string]string{
								constants.InferenceServicePodLabelKey: "my-isvc",
							},
						},
						Status: corev1.PodStatus{
							Phase:                 corev1.PodPending,
							InitContainerStatuses: initStatus,
						},
					}

					newPod := &corev1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "test-pod",
							Namespace: "default",
							Labels: map[string]string{
								constants.InferenceServicePodLabelKey: "my-isvc",
							},
						},
						Status: corev1.PodStatus{
							Phase:                 corev1.PodRunning, // Phase changed
							InitContainerStatuses: initStatus,        // But InitContainerStatuses unchanged
						},
					}

					result := pred.Update(event.UpdateEvent{
						ObjectOld: oldPod,
						ObjectNew: newPod,
					})

					Expect(result).To(BeFalse())
				})

				It("should return false when only ContainerStatuses change (not InitContainerStatuses)", func() {
					// This is critical for preventing event storms - main containers constantly
					// update their status but we only care about init container changes
					initStatus := []corev1.ContainerStatus{
						{
							Name: "storage-initializer",
							State: corev1.ContainerState{
								Terminated: &corev1.ContainerStateTerminated{
									ExitCode: 0,
								},
							},
						},
					}

					oldPod := &corev1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "test-pod",
							Namespace: "default",
							Labels: map[string]string{
								constants.InferenceServicePodLabelKey: "my-isvc",
							},
						},
						Status: corev1.PodStatus{
							InitContainerStatuses: initStatus,
							ContainerStatuses: []corev1.ContainerStatus{
								{
									Name:         "kserve-container",
									Ready:        false,
									RestartCount: 0,
								},
							},
						},
					}

					newPod := &corev1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "test-pod",
							Namespace: "default",
							Labels: map[string]string{
								constants.InferenceServicePodLabelKey: "my-isvc",
							},
						},
						Status: corev1.PodStatus{
							InitContainerStatuses: initStatus, // Unchanged
							ContainerStatuses: []corev1.ContainerStatus{
								{
									Name:         "kserve-container",
									Ready:        true, // Changed
									RestartCount: 1,    // Changed
								},
							},
						},
					}

					result := pred.Update(event.UpdateEvent{
						ObjectOld: oldPod,
						ObjectNew: newPod,
					})

					Expect(result).To(BeFalse())
				})

				It("should return false when InitContainerStatuses are identical", func() {
					oldPod := &corev1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "test-pod",
							Namespace: "default",
							Labels: map[string]string{
								constants.InferenceServicePodLabelKey: "my-isvc",
							},
						},
						Status: corev1.PodStatus{
							InitContainerStatuses: []corev1.ContainerStatus{
								{
									Name: "storage-initializer",
									State: corev1.ContainerState{
										Terminated: &corev1.ContainerStateTerminated{
											ExitCode: 0,
										},
									},
								},
							},
						},
					}

					newPod := oldPod.DeepCopy()

					result := pred.Update(event.UpdateEvent{
						ObjectOld: oldPod,
						ObjectNew: newPod,
					})

					Expect(result).To(BeFalse())
				})
			})

			Context("when pod does not have InferenceService label", func() {
				It("should return false for pods without the label", func() {
					oldPod := &corev1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "unrelated-pod",
							Namespace: "default",
							Labels:    map[string]string{},
						},
						Status: corev1.PodStatus{
							InitContainerStatuses: []corev1.ContainerStatus{},
						},
					}

					newPod := &corev1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "unrelated-pod",
							Namespace: "default",
							Labels:    map[string]string{},
						},
						Status: corev1.PodStatus{
							InitContainerStatuses: []corev1.ContainerStatus{
								{
									Name: "init",
									State: corev1.ContainerState{
										Terminated: &corev1.ContainerStateTerminated{
											ExitCode: 1,
										},
									},
								},
							},
						},
					}

					result := pred.Update(event.UpdateEvent{
						ObjectOld: oldPod,
						ObjectNew: newPod,
					})

					Expect(result).To(BeFalse())
				})

				It("should return false for pods with other labels but not InferenceService label", func() {
					oldPod := &corev1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "other-pod",
							Namespace: "default",
							Labels: map[string]string{
								"app": "some-other-app",
							},
						},
						Status: corev1.PodStatus{
							InitContainerStatuses: []corev1.ContainerStatus{},
						},
					}

					newPod := &corev1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "other-pod",
							Namespace: "default",
							Labels: map[string]string{
								"app": "some-other-app",
							},
						},
						Status: corev1.PodStatus{
							InitContainerStatuses: []corev1.ContainerStatus{
								{
									Name: "init",
									State: corev1.ContainerState{
										Terminated: &corev1.ContainerStateTerminated{
											ExitCode: 1,
										},
									},
								},
							},
						},
					}

					result := pred.Update(event.UpdateEvent{
						ObjectOld: oldPod,
						ObjectNew: newPod,
					})

					Expect(result).To(BeFalse())
				})
			})

			Context("when object is not a pod", func() {
				It("should return false for non-pod objects", func() {
					result := pred.Update(event.UpdateEvent{
						ObjectOld: &corev1.ConfigMap{},
						ObjectNew: &corev1.ConfigMap{},
					})

					Expect(result).To(BeFalse())
				})
			})
		})
	})

	// Integration-style tests that verify the mapper doesn't cause "event storms"
	Describe("Event Storm Prevention", func() {
		Context("when multiple pods exist for different InferenceServices", func() {
			It("should only return reconcile request for the specific InferenceService", func() {
				reconciler := &InferenceServiceReconciler{}

				pod1 := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "isvc1-predictor-pod",
						Namespace: "default",
						Labels: map[string]string{
							constants.InferenceServicePodLabelKey: "isvc1",
						},
					},
				}

				pod2 := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "isvc2-predictor-pod",
						Namespace: "default",
						Labels: map[string]string{
							constants.InferenceServicePodLabelKey: "isvc2",
						},
					},
				}

				// Pod1 change should only reconcile isvc1
				requests1 := reconciler.podInitContainersFunc(context.Background(), pod1)
				Expect(requests1).To(HaveLen(1))
				Expect(requests1[0].Name).To(Equal("isvc1"))

				// Pod2 change should only reconcile isvc2
				requests2 := reconciler.podInitContainersFunc(context.Background(), pod2)
				Expect(requests2).To(HaveLen(1))
				Expect(requests2[0].Name).To(Equal("isvc2"))
			})
		})

		Context("when pod is not managed by any InferenceService", func() {
			It("should not trigger any reconciliation", func() {
				reconciler := &InferenceServiceReconciler{}

				// A regular pod without the InferenceService label
				regularPod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "regular-pod",
						Namespace: "default",
						Labels: map[string]string{
							"app": "some-other-app",
						},
					},
				}

				requests := reconciler.podInitContainersFunc(context.Background(), regularPod)
				Expect(requests).To(BeNil())
			})
		})
	})
})

var _ = Describe("Event Storm Prevention Integration", func() {
	// This test verifies that pod events for one InferenceService do NOT trigger
	// reconciliation of an unrelated InferenceService. It uses the OnReconcile
	// callback to count reconciler invocations per ISVC name, which directly
	// measures unnecessary work rather than relying on resourceVersion side effects.

	var (
		testNamespace string
		configs       map[string]string
	)

	BeforeEach(func() {
		testNamespace = "event-storm-test"
		configs = getKnativeTestConfigs()

		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: testNamespace,
			},
		}
		err := k8sClient.Create(context.Background(), ns)
		if err != nil && !apierr.IsAlreadyExists(err) {
			Expect(err).ToNot(HaveOccurred(), "unexpected error creating namespace")
		}
	})

	AfterEach(func() {
		// Remove the callback
		testReconciler.SetOnReconcile(nil)

		// Clean up ISVCs
		var isvcList v1beta1.InferenceServiceList
		_ = k8sClient.List(context.Background(), &isvcList, client.InNamespace(testNamespace))
		for i := range isvcList.Items {
			_ = k8sClient.Delete(context.Background(), &isvcList.Items[i])
		}
		// Clean up pods
		var podList corev1.PodList
		_ = k8sClient.List(context.Background(), &podList, client.InNamespace(testNamespace))
		for i := range podList.Items {
			_ = k8sClient.Delete(context.Background(), &podList.Items[i])
		}
		// Clean up configmap
		cm := &corev1.ConfigMap{}
		cmKey := types.NamespacedName{Name: constants.InferenceServiceConfigMapName, Namespace: constants.KServeNamespace}
		if err := k8sClient.Get(context.Background(), cmKey, cm); err == nil {
			_ = k8sClient.Delete(context.Background(), cm)
		}
	})

	It("should not reconcile the primary ISVC when pod events fire for the secondary ISVC", func() {
		ctx := context.Background()

		// Set up reconcile invocation counter
		var mu sync.Mutex
		reconcileCounts := make(map[string]int)
		testReconciler.SetOnReconcile(func(name types.NamespacedName) {
			mu.Lock()
			defer mu.Unlock()
			reconcileCounts[name.Name]++
		})

		// Create the inferenceservice-config ConfigMap
		configMap := createInferenceServiceConfigMap(configs)
		Expect(k8sClient.Create(ctx, configMap)).Should(Succeed())

		primaryISVC := "primary-isvc"
		secondaryISVC := "secondary-isvc"

		// Create two InferenceServices
		for _, name := range []string{primaryISVC, secondaryISVC} {
			isvc := &v1beta1.InferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: testNamespace,
				},
				Spec: v1beta1.InferenceServiceSpec{
					Predictor: v1beta1.PredictorSpec{
						SKLearn: &v1beta1.SKLearnSpec{
							PredictorExtensionSpec: v1beta1.PredictorExtensionSpec{
								StorageURI: ptr.To("gs://testbucket/testmodel"),
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, isvc)).Should(Succeed())
		}

		// Wait for initial reconciliation of both ISVCs to complete.
		// Each ISVC will be reconciled at least once when created.
		Eventually(func() bool {
			mu.Lock()
			defer mu.Unlock()
			return reconcileCounts[primaryISVC] > 0 && reconcileCounts[secondaryISVC] > 0
		}, 10*time.Second, 200*time.Millisecond).Should(BeTrue(),
			"both ISVCs should have been reconciled at least once")

		// Record baseline counts after initial reconciliation settles.
		// Snapshot the count, then verify it stays stable via Consistently.
		var primaryCountBefore, secondaryCountBefore int
		mu.Lock()
		primaryCountBefore = reconcileCounts[primaryISVC]
		secondaryCountBefore = reconcileCounts[secondaryISVC]
		mu.Unlock()
		Consistently(func() int {
			mu.Lock()
			defer mu.Unlock()
			return reconcileCounts[primaryISVC]
		}, 2*time.Second, 200*time.Millisecond).Should(Equal(primaryCountBefore),
			"primary ISVC reconcile count should stabilize before proceeding")

		// Create a pod labeled for the secondary ISVC (simulates a pod event storm)
		secondaryPod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "secondary-pod",
				Namespace: testNamespace,
				Labels: map[string]string{
					constants.InferenceServicePodLabelKey: secondaryISVC,
				},
			},
			Spec: corev1.PodSpec{
				InitContainers: []corev1.Container{
					{
						Name:    "storage-initializer",
						Image:   "kserve/storage-initializer:latest",
						Command: []string{"sh", "-c", "echo init"},
					},
				},
				Containers: []corev1.Container{
					{
						Name:    "kserve-container",
						Image:   "kserve/sklearnserver:latest",
						Command: []string{"sh", "-c", "echo serve"},
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, secondaryPod)).Should(Succeed())

		// Simulate pod status updates (init container status changes) to trigger
		// the pod watch predicate. Each status update should only reconcile
		// the secondary ISVC, not the primary.
		podKey := types.NamespacedName{Name: "secondary-pod", Namespace: testNamespace}
		statusTransitions := []corev1.ContainerState{
			{Waiting: &corev1.ContainerStateWaiting{Reason: "PodInitializing"}},
			{Running: &corev1.ContainerStateRunning{StartedAt: metav1.Now()}},
			{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0, Reason: "Completed"}},
		}

		for _, state := range statusTransitions {
			Eventually(func() error {
				pod := &corev1.Pod{}
				if err := k8sClient.Get(ctx, podKey, pod); err != nil {
					return err
				}
				pod.Status.InitContainerStatuses = []corev1.ContainerStatus{
					{
						Name:  "storage-initializer",
						State: state,
					},
				}
				return k8sClient.Status().Update(ctx, pod)
			}, 5*time.Second, 200*time.Millisecond).Should(Succeed())
		}

		// Wait for the secondary ISVC to be reconciled from the pod status transitions.
		// Assert growth relative to the baseline to prove pod events caused the extra reconciles.
		Eventually(func() int {
			mu.Lock()
			defer mu.Unlock()
			return reconcileCounts[secondaryISVC]
		}, 10*time.Second, 200*time.Millisecond).Should(BeNumerically(">", secondaryCountBefore),
			"secondary ISVC should have been reconciled from pod events; "+
				"count before=%d", secondaryCountBefore)

		// Assert the primary ISVC was NOT reconciled during the storm window.
		// Consistently verifies the count remains stable over 2 seconds.
		Consistently(func() int {
			mu.Lock()
			defer mu.Unlock()
			return reconcileCounts[primaryISVC]
		}, 2*time.Second, 200*time.Millisecond).Should(Equal(primaryCountBefore),
			"primary ISVC should not have been reconciled due to secondary ISVC pod events; "+
				"count before=%d", primaryCountBefore)
	})
})

var _ = Describe("ServingRuntime Watch", func() {
	var reconciler *InferenceServiceReconciler
	var testNamespace string

	BeforeEach(func() {
		testNamespace = "runtime-watch-test"
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: testNamespace,
			},
		}
		err := k8sClient.Create(context.Background(), ns)
		if err != nil && !apierr.IsAlreadyExists(err) {
			Expect(err).ToNot(HaveOccurred(), "unexpected error creating namespace")
		}

		reconciler = &InferenceServiceReconciler{
			Client: k8sClient,
		}
	})

	AfterEach(func() {
		// Clean up ISVCs created during tests
		var isvcList v1beta1.InferenceServiceList
		_ = k8sClient.List(context.Background(), &isvcList, client.InNamespace(testNamespace))
		for _, isvc := range isvcList.Items {
			_ = k8sClient.Delete(context.Background(), &isvc)
		}
	})

	// Describe("clusterServingRuntimeFunc", func() {
	//	It("should only reconcile ISVCs that use the specific ClusterServingRuntime", func() {
	//		// Create ISVC using clusterRuntime1
	//		isvc1 := &v1beta1.InferenceService{
	//			ObjectMeta: metav1.ObjectMeta{
	//				Name:      "isvc-cluster-runtime-1",
	//				Namespace: testNamespace,
	//			},
	//			Spec: v1beta1.InferenceServiceSpec{
	//				Predictor: v1beta1.PredictorSpec{
	//					SKLearn: &v1beta1.SKLearnSpec{},
	//				},
	//			},
	//		}
	//		Expect(k8sClient.Create(context.Background(), isvc1)).To(Succeed())
	//
	//		// Set the ClusterServingRuntimeName in status
	//		isvc1.Status.ClusterServingRuntimeName = "cluster-runtime-1"
	//		Expect(k8sClient.Status().Update(context.Background(), isvc1)).To(Succeed())
	//
	//		// Create ISVC using clusterRuntime2
	//		isvc2 := &v1beta1.InferenceService{
	//			ObjectMeta: metav1.ObjectMeta{
	//				Name:      "isvc-cluster-runtime-2",
	//				Namespace: testNamespace,
	//			},
	//			Spec: v1beta1.InferenceServiceSpec{
	//				Predictor: v1beta1.PredictorSpec{
	//					SKLearn: &v1beta1.SKLearnSpec{},
	//				},
	//			},
	//		}
	//		Expect(k8sClient.Create(context.Background(), isvc2)).To(Succeed())
	//
	//		// Set the ClusterServingRuntimeName in status
	//		isvc2.Status.ClusterServingRuntimeName = "cluster-runtime-2"
	//		Expect(k8sClient.Status().Update(context.Background(), isvc2)).To(Succeed())
	//
	//		// Create a ClusterServingRuntime object (only need metadata for the mapper)
	//		csr := &v1alpha1.ClusterServingRuntime{
	//			ObjectMeta: metav1.ObjectMeta{
	//				Name: "cluster-runtime-1",
	//			},
	//		}
	//
	//		// Wait for the cache to sync and verify the mapper returns the correct request.
	//		// The cached client may not immediately reflect status updates.
	//		Eventually(func() []reconcile.Request {
	//			return reconciler.clusterServingRuntimeFunc(context.Background(), csr)
	//		}).Should(HaveLen(1))
	//
	//		requests := reconciler.clusterServingRuntimeFunc(context.Background(), csr)
	//		Expect(requests[0].Name).To(Equal("isvc-cluster-runtime-1"))
	//		Expect(requests[0].Namespace).To(Equal(testNamespace))
	//	})
	//
	//	It("should not reconcile ISVCs that use a different ClusterServingRuntime", func() {
	//		// Create ISVC using a different runtime
	//		isvc := &v1beta1.InferenceService{
	//			ObjectMeta: metav1.ObjectMeta{
	//				Name:      "isvc-different-runtime",
	//				Namespace: testNamespace,
	//			},
	//			Spec: v1beta1.InferenceServiceSpec{
	//				Predictor: v1beta1.PredictorSpec{
	//					SKLearn: &v1beta1.SKLearnSpec{},
	//				},
	//			},
	//		}
	//		Expect(k8sClient.Create(context.Background(), isvc)).To(Succeed())
	//
	//		// Set the ClusterServingRuntimeName in status to a different runtime
	//		isvc.Status.ClusterServingRuntimeName = "cluster-runtime-other"
	//		Expect(k8sClient.Status().Update(context.Background(), isvc)).To(Succeed())
	//
	//		// Create a ClusterServingRuntime object with a unique name not used by any ISVC
	//		csr := &v1alpha1.ClusterServingRuntime{
	//			ObjectMeta: metav1.ObjectMeta{
	//				Name: "cluster-runtime-unused",
	//			},
	//		}
	//
	//		// Call the mapper function
	//		requests := reconciler.clusterServingRuntimeFunc(context.Background(), csr)
	//
	//		// Should return empty since no ISVC uses cluster-runtime-unused
	//		Expect(requests).To(BeEmpty())
	//	})
	//
	//	It("should not reconcile ISVCs with auto-update disabled when ready", func() {
	//		// Create ISVC with auto-update disabled
	//		isvc := &v1beta1.InferenceService{
	//			ObjectMeta: metav1.ObjectMeta{
	//				Name:      "isvc-auto-update-disabled",
	//				Namespace: testNamespace,
	//				Annotations: map[string]string{
	//					constants.DisableAutoUpdateAnnotationKey: "true",
	//				},
	//			},
	//			Spec: v1beta1.InferenceServiceSpec{
	//				Predictor: v1beta1.PredictorSpec{
	//					SKLearn: &v1beta1.SKLearnSpec{},
	//				},
	//			},
	//		}
	//		Expect(k8sClient.Create(context.Background(), isvc)).To(Succeed())
	//
	//		// Set the ClusterServingRuntimeName and make it ready
	//		isvc.Status.ClusterServingRuntimeName = "cluster-runtime-auto-update"
	//		isvc.Status.SetCondition(v1beta1.PredictorReady, &knativeapis.Condition{
	//			Type:   v1beta1.PredictorReady,
	//			Status: corev1.ConditionTrue,
	//		})
	//		isvc.Status.SetCondition(v1beta1.IngressReady, &knativeapis.Condition{
	//			Type:   v1beta1.IngressReady,
	//			Status: corev1.ConditionTrue,
	//		})
	//		Expect(k8sClient.Status().Update(context.Background(), isvc)).To(Succeed())
	//
	//		// Create a ClusterServingRuntime object
	//		csr := &v1alpha1.ClusterServingRuntime{
	//			ObjectMeta: metav1.ObjectMeta{
	//				Name: "cluster-runtime-auto-update",
	//			},
	//		}
	//
	//		// Call the mapper function
	//		requests := reconciler.clusterServingRuntimeFunc(context.Background(), csr)
	//
	//		// Should not reconcile the ISVC because auto-update is disabled and it's ready
	//		Expect(requests).To(BeEmpty())
	//	})
	// })

	Describe("servingRuntimeFunc", func() {
		It("should only reconcile ISVCs that use the specific ServingRuntime", func() {
			// Create ISVC using runtime1
			isvc1 := &v1beta1.InferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "isvc-serving-runtime-1",
					Namespace: testNamespace,
				},
				Spec: v1beta1.InferenceServiceSpec{
					Predictor: v1beta1.PredictorSpec{
						SKLearn: &v1beta1.SKLearnSpec{},
					},
				},
			}
			Expect(k8sClient.Create(context.Background(), isvc1)).To(Succeed())

			// Set the ServingRuntimeName in status
			isvc1.Status.ServingRuntimeName = "serving-runtime-1"
			Expect(k8sClient.Status().Update(context.Background(), isvc1)).To(Succeed())

			// Create ISVC using runtime2
			isvc2 := &v1beta1.InferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "isvc-serving-runtime-2",
					Namespace: testNamespace,
				},
				Spec: v1beta1.InferenceServiceSpec{
					Predictor: v1beta1.PredictorSpec{
						SKLearn: &v1beta1.SKLearnSpec{},
					},
				},
			}
			Expect(k8sClient.Create(context.Background(), isvc2)).To(Succeed())

			// Set the ServingRuntimeName in status
			isvc2.Status.ServingRuntimeName = "serving-runtime-2"
			Expect(k8sClient.Status().Update(context.Background(), isvc2)).To(Succeed())

			// Create a ServingRuntime object
			sr := &v1alpha1.ServingRuntime{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "serving-runtime-1",
					Namespace: testNamespace,
				},
			}

			// Wait for the cache to sync and verify the mapper returns the correct request.
			// The cached client may not immediately reflect status updates.
			Eventually(func() []reconcile.Request {
				return reconciler.servingRuntimeFunc(context.Background(), sr)
			}).Should(HaveLen(1))

			requests := reconciler.servingRuntimeFunc(context.Background(), sr)
			Expect(requests[0].Name).To(Equal("isvc-serving-runtime-1"))
			Expect(requests[0].Namespace).To(Equal(testNamespace))
		})

		It("should not reconcile ISVCs that use a different ServingRuntime", func() {
			// Create ISVC using a different runtime
			isvc := &v1beta1.InferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "isvc-different-serving-runtime",
					Namespace: testNamespace,
				},
				Spec: v1beta1.InferenceServiceSpec{
					Predictor: v1beta1.PredictorSpec{
						SKLearn: &v1beta1.SKLearnSpec{},
					},
				},
			}
			Expect(k8sClient.Create(context.Background(), isvc)).To(Succeed())

			// Set the ServingRuntimeName in status to a different runtime
			isvc.Status.ServingRuntimeName = "serving-runtime-other"
			Expect(k8sClient.Status().Update(context.Background(), isvc)).To(Succeed())

			// Create a ServingRuntime object with a unique name not used by any ISVC
			sr := &v1alpha1.ServingRuntime{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "serving-runtime-unused",
					Namespace: testNamespace,
				},
			}

			// Call the mapper function
			requests := reconciler.servingRuntimeFunc(context.Background(), sr)

			// Should return empty since no ISVC uses serving-runtime-unused
			Expect(requests).To(BeEmpty())
		})

		It("should not reconcile ISVCs with auto-update disabled when ready", func() {
			// Create ISVC with auto-update disabled
			isvc := &v1beta1.InferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "isvc-serving-auto-update-disabled",
					Namespace: testNamespace,
					Annotations: map[string]string{
						constants.DisableAutoUpdateAnnotationKey: "true",
					},
				},
				Spec: v1beta1.InferenceServiceSpec{
					Predictor: v1beta1.PredictorSpec{
						SKLearn: &v1beta1.SKLearnSpec{},
					},
				},
			}
			Expect(k8sClient.Create(context.Background(), isvc)).To(Succeed())

			// Set the ServingRuntimeName and make it ready
			isvc.Status.ServingRuntimeName = "serving-runtime-auto-update"
			isvc.Status.SetCondition(v1beta1.PredictorReady, &knativeapis.Condition{
				Type:   v1beta1.PredictorReady,
				Status: corev1.ConditionTrue,
			})
			isvc.Status.SetCondition(v1beta1.IngressReady, &knativeapis.Condition{
				Type:   v1beta1.IngressReady,
				Status: corev1.ConditionTrue,
			})
			Expect(k8sClient.Status().Update(context.Background(), isvc)).To(Succeed())

			// Create a ServingRuntime object
			sr := &v1alpha1.ServingRuntime{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "serving-runtime-auto-update",
					Namespace: testNamespace,
				},
			}

			// Call the mapper function
			requests := reconciler.servingRuntimeFunc(context.Background(), sr)

			// Should not reconcile the ISVC because auto-update is disabled and it's ready
			Expect(requests).To(BeEmpty())
		})
	})
})
