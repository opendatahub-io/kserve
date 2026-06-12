/*
Copyright 2026 The KServe Authors.

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

	apierr "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	"github.com/kserve/kserve/pkg/apis/serving/v1beta1"
	"github.com/kserve/kserve/pkg/constants"
)

// hwpObject builds a HardwareProfile unstructured object for IS integration tests.
func hwpObject(name, namespace string, spec map[string]interface{}) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": constants.HardwareProfileGroup + "/" + constants.HardwareProfileVersion,
			"kind":       "HardwareProfile",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": namespace,
			},
			"spec": spec,
		},
	}
}

// hwpResourceSpec returns a HardwareProfile spec with resource identifiers.
// Each identifier is a []string{resourceName, defaultCount}.
func hwpResourceSpec(identifiers ...[]string) map[string]interface{} {
	items := make([]interface{}, 0, len(identifiers))
	for _, id := range identifiers {
		item := map[string]interface{}{"identifier": id[0]}
		if len(id) > 1 && id[1] != "" {
			item["defaultCount"] = id[1]
		}
		items = append(items, item)
	}
	return map[string]interface{}{"identifiers": items}
}

// hwpNodeSpec returns a HardwareProfile spec with node scheduling.
func hwpNodeSpec(nodeSelector map[string]interface{}, tolerations []interface{}) map[string]interface{} {
	node := map[string]interface{}{}
	if nodeSelector != nil {
		node["nodeSelector"] = nodeSelector
	}
	if tolerations != nil {
		node["tolerations"] = tolerations
	}
	return map[string]interface{}{
		"schedulingSpec": map[string]interface{}{
			"type": "Node",
			"node": node,
		},
	}
}

// hwpKueueSpec returns a HardwareProfile spec with Kueue queue scheduling.
func hwpKueueSpec(localQueueName string) map[string]interface{} {
	return map[string]interface{}{
		"schedulingSpec": map[string]interface{}{
			"type": "Queue",
			"kueue": map[string]interface{}{
				"localQueueName": localQueueName,
			},
		},
	}
}

// rawIsvcAnnotations returns annotations required for RawDeployment mode.
func rawIsvcAnnotations(extra ...map[string]string) map[string]string {
	anns := map[string]string{
		constants.DeploymentMode: "RawDeployment",
	}
	for _, m := range extra {
		for k, v := range m {
			anns[k] = v
		}
	}
	return anns
}

// minimalPredictorExtensionSpec returns a PredictorExtensionSpec without any container
// resources, so that HWP resources can be injected freely.
func minimalPredictorExtensionSpec() v1beta1.PredictorExtensionSpec {
	return v1beta1.PredictorExtensionSpec{
		StorageURI:     &storageUri,
		RuntimeVersion: ptr.To("1.14.0"),
		Container: corev1.Container{
			Name: constants.InferenceServiceContainerName,
			// No resources — HWP can inject
		},
	}
}

var _ = Describe("InferenceService HardwareProfile injection", func() {
	configs := getRawKubeTestConfigs()

	// ---------- Test Group 1: Basic injection scenarios ----------

	Context("Basic HWP injection scenarios", func() {
		It("IS-1: should not modify Deployment when no HWP annotation is set", func() {
			// given
			ctx := context.Background()
			configMap := createInferenceServiceConfigMap(configs)
			Expect(k8sClient.Create(ctx, configMap)).NotTo(HaveOccurred())
			defer k8sClient.Delete(ctx, configMap) //nolint:errcheck

			servingRuntime := getServingRuntime("tf-hwp-is1", "default")
			Expect(k8sClient.Create(ctx, &servingRuntime)).To(Succeed())
			defer k8sClient.Delete(ctx, &servingRuntime) //nolint:errcheck

			isvc := &v1beta1.InferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "hwp-is1-no-ann",
					Namespace:   "default",
					Annotations: rawIsvcAnnotations(),
				},
				Spec: v1beta1.InferenceServiceSpec{
					Predictor: v1beta1.PredictorSpec{
						Tensorflow: &v1beta1.TFServingSpec{
							PredictorExtensionSpec: minimalPredictorExtensionSpec(),
						},
					},
				},
			}
			isvc.DefaultInferenceService(nil, nil, &v1beta1.SecurityConfig{AutoMountServiceAccountToken: false}, nil, nil)
			Expect(k8sClient.Create(ctx, isvc)).To(Succeed())
			defer k8sClient.Delete(ctx, isvc) //nolint:errcheck

			// when / then
			dep := &appsv1.Deployment{}
			depKey := types.NamespacedName{
				Name:      constants.PredictorServiceName(isvc.Name),
				Namespace: "default",
			}
			Eventually(func() error {
				return k8sClient.Get(ctx, depKey, dep)
			}, timeout, interval).Should(Succeed())

			c := findISContainer(dep.Spec.Template.Spec.Containers)
			Expect(c).NotTo(BeNil())
			Expect(dep.Labels).NotTo(HaveKey(constants.KueueQueueNameLabel))
		})

		It("IS-2: should inject resources from HWP into kserve-container", func() {
			// given
			ctx := context.Background()
			configMap := createInferenceServiceConfigMap(configs)
			Expect(k8sClient.Create(ctx, configMap)).NotTo(HaveOccurred())
			defer k8sClient.Delete(ctx, configMap) //nolint:errcheck

			hwp := hwpObject("hwp-is2-resources", "default", hwpResourceSpec(
				[]string{"nvidia.com/gpu", "2"},
			))
			Expect(k8sClient.Create(ctx, hwp)).To(Succeed())
			defer k8sClient.Delete(ctx, hwp) //nolint:errcheck

			servingRuntime := getServingRuntime("tf-hwp-is2", "default")
			Expect(k8sClient.Create(ctx, &servingRuntime)).To(Succeed())
			defer k8sClient.Delete(ctx, &servingRuntime) //nolint:errcheck

			isvc := &v1beta1.InferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "hwp-is2-resources",
					Namespace: "default",
					Annotations: rawIsvcAnnotations(map[string]string{
						constants.HardwareProfileAnnotationName: "hwp-is2-resources",
					}),
				},
				Spec: v1beta1.InferenceServiceSpec{
					Predictor: v1beta1.PredictorSpec{
						Tensorflow: &v1beta1.TFServingSpec{
							PredictorExtensionSpec: minimalPredictorExtensionSpec(),
						},
					},
				},
			}
			isvc.DefaultInferenceService(nil, nil, &v1beta1.SecurityConfig{AutoMountServiceAccountToken: false}, nil, nil)
			originalPredictor := isvc.Spec.Predictor.DeepCopy()
			Expect(k8sClient.Create(ctx, isvc)).To(Succeed())
			defer k8sClient.Delete(ctx, isvc) //nolint:errcheck

			// when / then
			dep := &appsv1.Deployment{}
			depKey := types.NamespacedName{
				Name:      constants.PredictorServiceName(isvc.Name),
				Namespace: "default",
			}
			Eventually(func() error {
				return k8sClient.Get(ctx, depKey, dep)
			}, timeout, interval).Should(Succeed())

			c := findISContainer(dep.Spec.Template.Spec.Containers)
			Expect(c).NotTo(BeNil())
			Expect(c.Resources.Requests["nvidia.com/gpu"]).To(Equal(resource.MustParse("2")))
			Expect(c.Resources.Limits["nvidia.com/gpu"]).To(Equal(resource.MustParse("2")))

			// IS spec must not be mutated
			updatedIsvc := &v1beta1.InferenceService{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: isvc.Name, Namespace: "default"}, updatedIsvc)).To(Succeed())
			Expect(updatedIsvc.Spec.Predictor).To(Equal(*originalPredictor))
		})

		It("IS-3: should inject nodeSelector and tolerations from HWP node scheduling", func() {
			// given
			ctx := context.Background()
			configMap := createInferenceServiceConfigMap(configs)
			Expect(k8sClient.Create(ctx, configMap)).NotTo(HaveOccurred())
			defer k8sClient.Delete(ctx, configMap) //nolint:errcheck

			hwp := hwpObject("hwp-is3-node", "default", hwpNodeSpec(
				map[string]interface{}{"nvidia.com/gpu.product": "A100-PCIE-80GB"},
				[]interface{}{
					map[string]interface{}{
						"key":      "nvidia.com/gpu",
						"operator": "Exists",
						"effect":   "NoSchedule",
					},
				},
			))
			Expect(k8sClient.Create(ctx, hwp)).To(Succeed())
			defer k8sClient.Delete(ctx, hwp) //nolint:errcheck

			servingRuntime := getServingRuntime("tf-hwp-is3", "default")
			Expect(k8sClient.Create(ctx, &servingRuntime)).To(Succeed())
			defer k8sClient.Delete(ctx, &servingRuntime) //nolint:errcheck

			isvc := &v1beta1.InferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "hwp-is3-node",
					Namespace: "default",
					Annotations: rawIsvcAnnotations(map[string]string{
						constants.HardwareProfileAnnotationName: "hwp-is3-node",
					}),
				},
				Spec: v1beta1.InferenceServiceSpec{
					Predictor: v1beta1.PredictorSpec{
						Tensorflow: &v1beta1.TFServingSpec{
							PredictorExtensionSpec: minimalPredictorExtensionSpec(),
						},
					},
				},
			}
			isvc.DefaultInferenceService(nil, nil, &v1beta1.SecurityConfig{AutoMountServiceAccountToken: false}, nil, nil)
			Expect(k8sClient.Create(ctx, isvc)).To(Succeed())
			defer k8sClient.Delete(ctx, isvc) //nolint:errcheck

			// when / then
			dep := &appsv1.Deployment{}
			depKey := types.NamespacedName{
				Name:      constants.PredictorServiceName(isvc.Name),
				Namespace: "default",
			}
			Eventually(func() error {
				return k8sClient.Get(ctx, depKey, dep)
			}, timeout, interval).Should(Succeed())

			Expect(dep.Spec.Template.Spec.NodeSelector).To(HaveKeyWithValue("nvidia.com/gpu.product", "A100-PCIE-80GB"))
			Expect(dep.Spec.Template.Spec.Tolerations).To(ContainElement(
				corev1.Toleration{
					Key:      "nvidia.com/gpu",
					Operator: corev1.TolerationOpExists,
					Effect:   corev1.TaintEffectNoSchedule,
				},
			))
		})

		It("IS-4: should set Kueue label from HWP queue scheduling", func() {
			// given
			ctx := context.Background()
			configMap := createInferenceServiceConfigMap(configs)
			Expect(k8sClient.Create(ctx, configMap)).NotTo(HaveOccurred())
			defer k8sClient.Delete(ctx, configMap) //nolint:errcheck

			hwp := hwpObject("hwp-is4-kueue", "default", hwpKueueSpec("test-queue"))
			Expect(k8sClient.Create(ctx, hwp)).To(Succeed())
			defer k8sClient.Delete(ctx, hwp) //nolint:errcheck

			servingRuntime := getServingRuntime("tf-hwp-is4", "default")
			Expect(k8sClient.Create(ctx, &servingRuntime)).To(Succeed())
			defer k8sClient.Delete(ctx, &servingRuntime) //nolint:errcheck

			isvc := &v1beta1.InferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "hwp-is4-kueue",
					Namespace: "default",
					Annotations: rawIsvcAnnotations(map[string]string{
						constants.HardwareProfileAnnotationName: "hwp-is4-kueue",
					}),
				},
				Spec: v1beta1.InferenceServiceSpec{
					Predictor: v1beta1.PredictorSpec{
						Tensorflow: &v1beta1.TFServingSpec{
							PredictorExtensionSpec: minimalPredictorExtensionSpec(),
						},
					},
				},
			}
			isvc.DefaultInferenceService(nil, nil, &v1beta1.SecurityConfig{AutoMountServiceAccountToken: false}, nil, nil)
			Expect(k8sClient.Create(ctx, isvc)).To(Succeed())
			defer k8sClient.Delete(ctx, isvc) //nolint:errcheck

			// when / then
			dep := &appsv1.Deployment{}
			depKey := types.NamespacedName{
				Name:      constants.PredictorServiceName(isvc.Name),
				Namespace: "default",
			}
			Eventually(func() error {
				return k8sClient.Get(ctx, depKey, dep)
			}, timeout, interval).Should(Succeed())

			Expect(dep.Labels).To(HaveKeyWithValue(constants.KueueQueueNameLabel, "test-queue"))
		})

		It("IS-5: should not create Deployment when referenced HWP does not exist", func() {
			// given
			ctx := context.Background()
			configMap := createInferenceServiceConfigMap(configs)
			Expect(k8sClient.Create(ctx, configMap)).NotTo(HaveOccurred())
			defer k8sClient.Delete(ctx, configMap) //nolint:errcheck

			servingRuntime := getServingRuntime("tf-hwp-is5", "default")
			Expect(k8sClient.Create(ctx, &servingRuntime)).To(Succeed())
			defer k8sClient.Delete(ctx, &servingRuntime) //nolint:errcheck

			isvc := &v1beta1.InferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "hwp-is5-not-found",
					Namespace: "default",
					Annotations: rawIsvcAnnotations(map[string]string{
						constants.HardwareProfileAnnotationName: "missing-hwp-is5",
					}),
				},
				Spec: v1beta1.InferenceServiceSpec{
					Predictor: v1beta1.PredictorSpec{
						Tensorflow: &v1beta1.TFServingSpec{
							PredictorExtensionSpec: minimalPredictorExtensionSpec(),
						},
					},
				},
			}
			isvc.DefaultInferenceService(nil, nil, &v1beta1.SecurityConfig{AutoMountServiceAccountToken: false}, nil, nil)
			Expect(k8sClient.Create(ctx, isvc)).To(Succeed())
			defer k8sClient.Delete(ctx, isvc) //nolint:errcheck

			depKey := types.NamespacedName{
				Name:      constants.PredictorServiceName(isvc.Name),
				Namespace: "default",
			}

			// then - Deployment is never created while HWP is absent
			Consistently(func() bool {
				dep := &appsv1.Deployment{}
				err := k8sClient.Get(ctx, depKey, dep)
				return apierr.IsNotFound(err)
			}, fastTimeout, interval).Should(BeTrue(), "Deployment should not be created when HWP is missing")

			// sub-step: create the missing HWP → reconciliation unblocked
			hwp := hwpObject("missing-hwp-is5", "default", hwpResourceSpec([]string{"nvidia.com/gpu", "1"}))
			Expect(k8sClient.Create(ctx, hwp)).To(Succeed())
			defer k8sClient.Delete(ctx, hwp) //nolint:errcheck

			Eventually(func() error {
				dep := &appsv1.Deployment{}
				return k8sClient.Get(ctx, depKey, dep)
			}, timeout, interval).Should(Succeed(), "Deployment should be created after HWP becomes available")
		})
	})

	// ---------- Test Group 2: Priority / override semantics ----------

	Context("Priority and override semantics", func() {
		It("IS-6: IS-specified resource takes priority over HWP resource", func() {
			// given
			ctx := context.Background()
			configMap := createInferenceServiceConfigMap(configs)
			Expect(k8sClient.Create(ctx, configMap)).NotTo(HaveOccurred())
			defer k8sClient.Delete(ctx, configMap) //nolint:errcheck

			// HWP wants CPU "4", but IS already has CPU "2" and GPU
			hwp := hwpObject("hwp-is6-prio", "default", hwpResourceSpec(
				[]string{"cpu", "4"},
				[]string{"nvidia.com/gpu", "2"},
			))
			Expect(k8sClient.Create(ctx, hwp)).To(Succeed())
			defer k8sClient.Delete(ctx, hwp) //nolint:errcheck

			servingRuntime := getServingRuntime("tf-hwp-is6", "default")
			Expect(k8sClient.Create(ctx, &servingRuntime)).To(Succeed())
			defer k8sClient.Delete(ctx, &servingRuntime) //nolint:errcheck

			isvc := &v1beta1.InferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "hwp-is6-prio",
					Namespace: "default",
					Annotations: rawIsvcAnnotations(map[string]string{
						constants.HardwareProfileAnnotationName: "hwp-is6-prio",
					}),
				},
				Spec: v1beta1.InferenceServiceSpec{
					Predictor: v1beta1.PredictorSpec{
						Tensorflow: &v1beta1.TFServingSpec{
							PredictorExtensionSpec: v1beta1.PredictorExtensionSpec{
								StorageURI:     &storageUri,
								RuntimeVersion: ptr.To("1.14.0"),
								Container: corev1.Container{
									Name: constants.InferenceServiceContainerName,
									Resources: corev1.ResourceRequirements{
										Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")},
										Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")},
									},
								},
							},
						},
					},
				},
			}
			isvc.DefaultInferenceService(nil, nil, &v1beta1.SecurityConfig{AutoMountServiceAccountToken: false}, nil, nil)
			Expect(k8sClient.Create(ctx, isvc)).To(Succeed())
			defer k8sClient.Delete(ctx, isvc) //nolint:errcheck

			// when / then
			dep := &appsv1.Deployment{}
			depKey := types.NamespacedName{
				Name:      constants.PredictorServiceName(isvc.Name),
				Namespace: "default",
			}
			Eventually(func() error {
				return k8sClient.Get(ctx, depKey, dep)
			}, timeout, interval).Should(Succeed())

			c := findISContainer(dep.Spec.Template.Spec.Containers)
			Expect(c).NotTo(BeNil())
			// IS-specified cpu "2" wins over HWP cpu "4"
			Expect(c.Resources.Requests[corev1.ResourceCPU]).To(Equal(resource.MustParse("2")))
			// GPU is injected since IS doesn't set it
			Expect(c.Resources.Requests["nvidia.com/gpu"]).To(Equal(resource.MustParse("2")))
		})

		It("IS-7: IS-specified nodeSelector key takes priority over HWP key", func() {
			// given
			ctx := context.Background()
			configMap := createInferenceServiceConfigMap(configs)
			Expect(k8sClient.Create(ctx, configMap)).NotTo(HaveOccurred())
			defer k8sClient.Delete(ctx, configMap) //nolint:errcheck

			hwp := hwpObject("hwp-is7-node-prio", "default", hwpNodeSpec(
				map[string]interface{}{
					"zone": "eu-west",
					"tier": "gpu",
				},
				nil,
			))
			Expect(k8sClient.Create(ctx, hwp)).To(Succeed())
			defer k8sClient.Delete(ctx, hwp) //nolint:errcheck

			servingRuntime := getServingRuntime("tf-hwp-is7", "default")
			Expect(k8sClient.Create(ctx, &servingRuntime)).To(Succeed())
			defer k8sClient.Delete(ctx, &servingRuntime) //nolint:errcheck

			isvc := &v1beta1.InferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "hwp-is7-node-prio",
					Namespace: "default",
					Annotations: rawIsvcAnnotations(map[string]string{
						constants.HardwareProfileAnnotationName: "hwp-is7-node-prio",
					}),
				},
				Spec: v1beta1.InferenceServiceSpec{
					Predictor: v1beta1.PredictorSpec{
						PodSpec: v1beta1.PodSpec{
							NodeSelector: map[string]string{"zone": "us-east"},
						},
						Tensorflow: &v1beta1.TFServingSpec{
							PredictorExtensionSpec: minimalPredictorExtensionSpec(),
						},
					},
				},
			}
			isvc.DefaultInferenceService(nil, nil, &v1beta1.SecurityConfig{AutoMountServiceAccountToken: false}, nil, nil)
			Expect(k8sClient.Create(ctx, isvc)).To(Succeed())
			defer k8sClient.Delete(ctx, isvc) //nolint:errcheck

			// when / then
			dep := &appsv1.Deployment{}
			depKey := types.NamespacedName{
				Name:      constants.PredictorServiceName(isvc.Name),
				Namespace: "default",
			}
			Eventually(func() error {
				return k8sClient.Get(ctx, depKey, dep)
			}, timeout, interval).Should(Succeed())

			Expect(dep.Spec.Template.Spec.NodeSelector).To(HaveKeyWithValue("zone", "us-east"), "IS value should win")
			Expect(dep.Spec.Template.Spec.NodeSelector).To(HaveKeyWithValue("tier", "gpu"), "HWP-only key should be added")
		})

		It("IS-7b: IS-specified toleration is not duplicated", func() {
			// given
			ctx := context.Background()
			configMap := createInferenceServiceConfigMap(configs)
			Expect(k8sClient.Create(ctx, configMap)).NotTo(HaveOccurred())
			defer k8sClient.Delete(ctx, configMap) //nolint:errcheck

			sharedTol := corev1.Toleration{
				Key:      "nvidia.com/gpu",
				Operator: corev1.TolerationOpExists,
				Effect:   corev1.TaintEffectNoSchedule,
			}
			hwp := hwpObject("hwp-is7b-tol", "default", hwpNodeSpec(
				nil,
				[]interface{}{
					map[string]interface{}{
						"key":      "nvidia.com/gpu",
						"operator": "Exists",
						"effect":   "NoSchedule",
					},
				},
			))
			Expect(k8sClient.Create(ctx, hwp)).To(Succeed())
			defer k8sClient.Delete(ctx, hwp) //nolint:errcheck

			servingRuntime := getServingRuntime("tf-hwp-is7b", "default")
			Expect(k8sClient.Create(ctx, &servingRuntime)).To(Succeed())
			defer k8sClient.Delete(ctx, &servingRuntime) //nolint:errcheck

			isvc := &v1beta1.InferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "hwp-is7b-tol",
					Namespace: "default",
					Annotations: rawIsvcAnnotations(map[string]string{
						constants.HardwareProfileAnnotationName: "hwp-is7b-tol",
					}),
				},
				Spec: v1beta1.InferenceServiceSpec{
					Predictor: v1beta1.PredictorSpec{
						PodSpec: v1beta1.PodSpec{
							Tolerations: []corev1.Toleration{sharedTol},
						},
						Tensorflow: &v1beta1.TFServingSpec{
							PredictorExtensionSpec: minimalPredictorExtensionSpec(),
						},
					},
				},
			}
			isvc.DefaultInferenceService(nil, nil, &v1beta1.SecurityConfig{AutoMountServiceAccountToken: false}, nil, nil)
			Expect(k8sClient.Create(ctx, isvc)).To(Succeed())
			defer k8sClient.Delete(ctx, isvc) //nolint:errcheck

			// when / then
			dep := &appsv1.Deployment{}
			depKey := types.NamespacedName{
				Name:      constants.PredictorServiceName(isvc.Name),
				Namespace: "default",
			}
			Eventually(func() error {
				return k8sClient.Get(ctx, depKey, dep)
			}, timeout, interval).Should(Succeed())

			// Count the exact toleration
			count := 0
			for _, t := range dep.Spec.Template.Spec.Tolerations {
				if t.Key == sharedTol.Key && t.Operator == sharedTol.Operator && t.Effect == sharedTol.Effect {
					count++
				}
			}
			Expect(count).To(Equal(1), "duplicate toleration should appear exactly once")
		})

		It("IS-8: IS-specified Kueue label takes priority over HWP Kueue label", func() {
			// given
			ctx := context.Background()
			configMap := createInferenceServiceConfigMap(configs)
			Expect(k8sClient.Create(ctx, configMap)).NotTo(HaveOccurred())
			defer k8sClient.Delete(ctx, configMap) //nolint:errcheck

			hwp := hwpObject("hwp-is8-kueue-prio", "default", hwpKueueSpec("hwp-queue"))
			Expect(k8sClient.Create(ctx, hwp)).To(Succeed())
			defer k8sClient.Delete(ctx, hwp) //nolint:errcheck

			servingRuntime := getServingRuntime("tf-hwp-is8", "default")
			Expect(k8sClient.Create(ctx, &servingRuntime)).To(Succeed())
			defer k8sClient.Delete(ctx, &servingRuntime) //nolint:errcheck

			isvc := &v1beta1.InferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "hwp-is8-kueue-prio",
					Namespace: "default",
					Annotations: rawIsvcAnnotations(map[string]string{
						constants.HardwareProfileAnnotationName: "hwp-is8-kueue-prio",
					}),
					Labels: map[string]string{
						constants.KueueQueueNameLabel: "user-queue",
					},
				},
				Spec: v1beta1.InferenceServiceSpec{
					Predictor: v1beta1.PredictorSpec{
						Tensorflow: &v1beta1.TFServingSpec{
							PredictorExtensionSpec: minimalPredictorExtensionSpec(),
						},
					},
				},
			}
			isvc.DefaultInferenceService(nil, nil, &v1beta1.SecurityConfig{AutoMountServiceAccountToken: false}, nil, nil)
			Expect(k8sClient.Create(ctx, isvc)).To(Succeed())
			defer k8sClient.Delete(ctx, isvc) //nolint:errcheck

			// when / then
			dep := &appsv1.Deployment{}
			depKey := types.NamespacedName{
				Name:      constants.PredictorServiceName(isvc.Name),
				Namespace: "default",
			}
			Eventually(func() error {
				return k8sClient.Get(ctx, depKey, dep)
			}, timeout, interval).Should(Succeed())

			Expect(dep.Labels).To(HaveKeyWithValue(constants.KueueQueueNameLabel, "user-queue"), "IS label should win")
		})
	})

	// ---------- Test Group 3: Mutation / update semantics ----------

	Context("Mutation and update semantics", func() {
		It("IS-9: should update Deployment when HWP annotation changes to different HWP", func() {
			// given
			ctx := context.Background()
			configMap := createInferenceServiceConfigMap(configs)
			Expect(k8sClient.Create(ctx, configMap)).NotTo(HaveOccurred())
			defer k8sClient.Delete(ctx, configMap) //nolint:errcheck

			hwpA := hwpObject("hwp-is9-a", "default", hwpResourceSpec([]string{"nvidia.com/gpu", "2"}))
			Expect(k8sClient.Create(ctx, hwpA)).To(Succeed())
			defer k8sClient.Delete(ctx, hwpA) //nolint:errcheck

			hwpB := hwpObject("hwp-is9-b", "default", hwpResourceSpec([]string{"nvidia.com/gpu", "8"}))
			Expect(k8sClient.Create(ctx, hwpB)).To(Succeed())
			defer k8sClient.Delete(ctx, hwpB) //nolint:errcheck

			servingRuntime := getServingRuntime("tf-hwp-is9", "default")
			Expect(k8sClient.Create(ctx, &servingRuntime)).To(Succeed())
			defer k8sClient.Delete(ctx, &servingRuntime) //nolint:errcheck

			isvc := &v1beta1.InferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "hwp-is9-update",
					Namespace: "default",
					Annotations: rawIsvcAnnotations(map[string]string{
						constants.HardwareProfileAnnotationName: "hwp-is9-a",
					}),
				},
				Spec: v1beta1.InferenceServiceSpec{
					Predictor: v1beta1.PredictorSpec{
						Tensorflow: &v1beta1.TFServingSpec{
							PredictorExtensionSpec: minimalPredictorExtensionSpec(),
						},
					},
				},
			}
			isvc.DefaultInferenceService(nil, nil, &v1beta1.SecurityConfig{AutoMountServiceAccountToken: false}, nil, nil)
			Expect(k8sClient.Create(ctx, isvc)).To(Succeed())
			defer k8sClient.Delete(ctx, isvc) //nolint:errcheck

			depKey := types.NamespacedName{
				Name:      constants.PredictorServiceName(isvc.Name),
				Namespace: "default",
			}

			// Wait for initial Deployment and assert GPU "2" from hwp-a
			dep := &appsv1.Deployment{}
			Eventually(func() bool {
				if err := k8sClient.Get(ctx, depKey, dep); err != nil {
					return false
				}
				for _, c := range dep.Spec.Template.Spec.Containers {
					if c.Name == constants.InferenceServiceContainerName {
						gpu, ok := c.Resources.Requests["nvidia.com/gpu"]
						return ok && gpu.Cmp(resource.MustParse("2")) == 0
					}
				}
				return false
			}, timeout, interval).Should(BeTrue(), "initial Deployment should have GPU '2' from hwp-a")

			// when — update IS annotation to hwp-b
			errRetry := retry.RetryOnConflict(retry.DefaultRetry, func() error {
				_, errUpdate := ctrl.CreateOrUpdate(ctx, k8sClient, isvc, func() error {
					if isvc.Annotations == nil {
						isvc.Annotations = make(map[string]string)
					}
					isvc.Annotations[constants.HardwareProfileAnnotationName] = "hwp-is9-b"
					return nil
				})
				return errUpdate
			})
			Expect(errRetry).NotTo(HaveOccurred())

			// then — Deployment is eventually updated to GPU "8"
			Eventually(func() bool {
				if err := k8sClient.Get(ctx, depKey, dep); err != nil {
					return false
				}
				c := findISContainer(dep.Spec.Template.Spec.Containers)
				if c == nil {
					return false
				}
				gpu, ok := c.Resources.Requests["nvidia.com/gpu"]
				return ok && gpu.Cmp(resource.MustParse("8")) == 0
			}, timeout, interval).Should(BeTrue(), "GPU should be updated to 8 after HWP annotation change")
		})

		It("IS-10: should rebuild Deployment without HWP stanzas when annotation is removed", func() {
			// given
			ctx := context.Background()
			configMap := createInferenceServiceConfigMap(configs)
			Expect(k8sClient.Create(ctx, configMap)).NotTo(HaveOccurred())
			defer k8sClient.Delete(ctx, configMap) //nolint:errcheck

			hwp := hwpObject("hwp-is10-remove", "default", func() map[string]interface{} {
				spec := hwpResourceSpec([]string{"nvidia.com/gpu", "4"})
				nodePart := hwpNodeSpec(map[string]interface{}{"tier": "gpu"}, nil)
				// Merge both into a combined spec
				for k, v := range nodePart {
					spec[k] = v
				}
				return spec
			}())
			Expect(k8sClient.Create(ctx, hwp)).To(Succeed())
			defer k8sClient.Delete(ctx, hwp) //nolint:errcheck

			servingRuntime := getServingRuntime("tf-hwp-is10", "default")
			Expect(k8sClient.Create(ctx, &servingRuntime)).To(Succeed())
			defer k8sClient.Delete(ctx, &servingRuntime) //nolint:errcheck

			isvc := &v1beta1.InferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "hwp-is10-remove",
					Namespace: "default",
					Annotations: rawIsvcAnnotations(map[string]string{
						constants.HardwareProfileAnnotationName: "hwp-is10-remove",
					}),
				},
				Spec: v1beta1.InferenceServiceSpec{
					Predictor: v1beta1.PredictorSpec{
						Tensorflow: &v1beta1.TFServingSpec{
							PredictorExtensionSpec: minimalPredictorExtensionSpec(),
						},
					},
				},
			}
			isvc.DefaultInferenceService(nil, nil, &v1beta1.SecurityConfig{AutoMountServiceAccountToken: false}, nil, nil)
			Expect(k8sClient.Create(ctx, isvc)).To(Succeed())
			defer k8sClient.Delete(ctx, isvc) //nolint:errcheck

			depKey := types.NamespacedName{
				Name:      constants.PredictorServiceName(isvc.Name),
				Namespace: "default",
			}

			// Wait for Deployment with GPU injected
			dep := &appsv1.Deployment{}
			Eventually(func() bool {
				if err := k8sClient.Get(ctx, depKey, dep); err != nil {
					return false
				}
				c := findISContainer(dep.Spec.Template.Spec.Containers)
				if c == nil {
					return false
				}
				_, ok := c.Resources.Requests["nvidia.com/gpu"]
				return ok
			}, timeout, interval).Should(BeTrue(), "Initial Deployment should have GPU resource from HWP")

			// when — remove HWP annotation
			errRetry := retry.RetryOnConflict(retry.DefaultRetry, func() error {
				_, errUpdate := ctrl.CreateOrUpdate(ctx, k8sClient, isvc, func() error {
					delete(isvc.Annotations, constants.HardwareProfileAnnotationName)
					return nil
				})
				return errUpdate
			})
			Expect(errRetry).NotTo(HaveOccurred())

			// then — Deployment is eventually rebuilt without HWP GPU and HWP nodeSelector
			Eventually(func() bool {
				if err := k8sClient.Get(ctx, depKey, dep); err != nil {
					return false
				}
				c := findISContainer(dep.Spec.Template.Spec.Containers)
				if c == nil {
					return false
				}
				_, hasGPU := c.Resources.Requests["nvidia.com/gpu"]
				_, hasTier := dep.Spec.Template.Spec.NodeSelector["tier"]
				return !hasGPU && !hasTier
			}, timeout, interval).Should(BeTrue(), "Deployment should not have HWP resources/nodeSelector after annotation removal")
		})

		It("IS-11: cross-namespace HWP reference applies resources correctly", func() {
			// given
			ctx := context.Background()
			configMap := createInferenceServiceConfigMap(configs)
			Expect(k8sClient.Create(ctx, configMap)).NotTo(HaveOccurred())
			defer k8sClient.Delete(ctx, configMap) //nolint:errcheck

			// Create a separate namespace for the HWP
			hwpNs := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: "hwp-cross-ns-test"},
			}
			Expect(k8sClient.Create(ctx, hwpNs)).To(Succeed())
			defer k8sClient.Delete(ctx, hwpNs) //nolint:errcheck

			hwp := hwpObject("hwp-is11-cross", "hwp-cross-ns-test", hwpResourceSpec(
				[]string{"nvidia.com/gpu", "4"},
			))
			Expect(k8sClient.Create(ctx, hwp)).To(Succeed())
			defer k8sClient.Delete(ctx, hwp) //nolint:errcheck

			servingRuntime := getServingRuntime("tf-hwp-is11", "default")
			Expect(k8sClient.Create(ctx, &servingRuntime)).To(Succeed())
			defer k8sClient.Delete(ctx, &servingRuntime) //nolint:errcheck

			isvc := &v1beta1.InferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "hwp-is11-cross",
					Namespace: "default",
					Annotations: rawIsvcAnnotations(map[string]string{
						constants.HardwareProfileAnnotationName:      "hwp-is11-cross",
						constants.HardwareProfileAnnotationNamespace: "hwp-cross-ns-test",
					}),
				},
				Spec: v1beta1.InferenceServiceSpec{
					Predictor: v1beta1.PredictorSpec{
						Tensorflow: &v1beta1.TFServingSpec{
							PredictorExtensionSpec: minimalPredictorExtensionSpec(),
						},
					},
				},
			}
			isvc.DefaultInferenceService(nil, nil, &v1beta1.SecurityConfig{AutoMountServiceAccountToken: false}, nil, nil)
			Expect(k8sClient.Create(ctx, isvc)).To(Succeed())
			defer k8sClient.Delete(ctx, isvc) //nolint:errcheck

			// when / then
			dep := &appsv1.Deployment{}
			depKey := types.NamespacedName{
				Name:      constants.PredictorServiceName(isvc.Name),
				Namespace: "default",
			}
			Eventually(func() error {
				return k8sClient.Get(ctx, depKey, dep)
			}, timeout, interval).Should(Succeed())

			c := findISContainer(dep.Spec.Template.Spec.Containers)
			Expect(c).NotTo(BeNil())
			Expect(c.Resources.Requests["nvidia.com/gpu"]).To(Equal(resource.MustParse("4")))
		})
	})
})

// findISContainer finds the kserve-container in a slice, returning nil if not found.
func findISContainer(containers []corev1.Container) *corev1.Container {
	for i := range containers {
		if containers[i].Name == constants.InferenceServiceContainerName {
			return &containers[i]
		}
	}
	return nil
}
