//go:build distro

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
	"fmt"
	"strconv"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"knative.dev/serving/pkg/apis/autoscaling"
	knservingv1 "knative.dev/serving/pkg/apis/serving/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/kserve/kserve/pkg/apis/serving/v1beta1"
	"github.com/kserve/kserve/pkg/constants"
)

// ODH sets the Knative initial scale to zero for a predictor with zero min replicas when the
// cluster allows it (ValidateInitialScaleAnnotationWithReplicas in utils_zeroscale_odh.go).
var _ = Describe("v1beta1 inference service controller on ODH", func() {
	configs := getKnativeTestConfigs()

	Context("with knative configured to not allow zero initial scale", func() {
		BeforeEach(func(ctx SpecContext) {
			patchAllowZeroInitialScale(ctx, ptr.To(false))
		})
		AfterEach(func(ctx SpecContext) {
			patchAllowZeroInitialScale(ctx, nil)
		})
		When("a Serverless InferenceService is created with zero min replicas", func() {
			It("should use the default initial scale value", func() {
				// Create configmap
				configMap := &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      constants.InferenceServiceConfigMapName,
						Namespace: constants.KServeNamespace,
					},
					Data: configs,
				}
				Expect(k8sClient.Create(context.TODO(), configMap)).NotTo(HaveOccurred())
				defer k8sClient.Delete(context.TODO(), configMap)

				// Create InferenceService
				serviceName := "odh-initialscale1"
				expectedRequest := reconcile.Request{NamespacedName: types.NamespacedName{Name: serviceName, Namespace: "default"}}
				serviceKey := expectedRequest.NamespacedName
				storageUri := "s3://test/mnist/export"
				ctx := context.Background()
				var minScale int32 = 0
				isvc := &v1beta1.InferenceService{
					ObjectMeta: metav1.ObjectMeta{
						Name:      serviceKey.Name,
						Namespace: serviceKey.Namespace,
						Annotations: map[string]string{
							"serving.kserve.io/deploymentMode": "Serverless",
						},
					},
					Spec: v1beta1.InferenceServiceSpec{
						Predictor: v1beta1.PredictorSpec{
							ComponentExtensionSpec: v1beta1.ComponentExtensionSpec{
								MinReplicas: &minScale,
							},
							Tensorflow: &v1beta1.TFServingSpec{
								PredictorExtensionSpec: v1beta1.PredictorExtensionSpec{
									StorageURI:     &storageUri,
									RuntimeVersion: ptr.To("1.14.0"),
									Container: corev1.Container{
										Name:      constants.InferenceServiceContainerName,
										Resources: defaultResource,
									},
								},
							},
						},
					},
				}
				Expect(k8sClient.Create(ctx, isvc)).Should(Succeed())
				defer k8sClient.Delete(ctx, isvc)

				predictorServiceKey := types.NamespacedName{
					Name:      constants.PredictorServiceName(serviceKey.Name),
					Namespace: serviceKey.Namespace,
				}
				actualService := &knservingv1.Service{}
				Eventually(func() error {
					return k8sClient.Get(context.TODO(), predictorServiceKey, actualService)
				},
					timeout, interval).Should(Succeed())

				Expect(actualService.Spec.Template.Annotations).NotTo(HaveKey(autoscaling.InitialScaleAnnotationKey))
			})
		})
	})

	Context("with knative configured to allow zero initial scale", func() {
		BeforeEach(func(ctx SpecContext) {
			patchAllowZeroInitialScale(ctx, ptr.To(true))
		})
		AfterEach(func(ctx SpecContext) {
			patchAllowZeroInitialScale(ctx, nil)
		})
		When("a Serverless InferenceService is created with zero min replicas", func() {
			It("should override the default initial scale value with zero", func() {
				// Create configmap
				configMap := &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      constants.InferenceServiceConfigMapName,
						Namespace: constants.KServeNamespace,
					},
					Data: configs,
				}
				Expect(k8sClient.Create(context.TODO(), configMap)).NotTo(HaveOccurred())
				defer k8sClient.Delete(context.TODO(), configMap)

				// Create InferenceService
				serviceName := "odh-initialscale2"
				expectedRequest := reconcile.Request{NamespacedName: types.NamespacedName{Name: serviceName, Namespace: "default"}}
				serviceKey := expectedRequest.NamespacedName
				storageUri := "s3://test/mnist/export"
				ctx := context.Background()
				var minScale int32 = 0
				isvc := &v1beta1.InferenceService{
					ObjectMeta: metav1.ObjectMeta{
						Name:      serviceKey.Name,
						Namespace: serviceKey.Namespace,
						Annotations: map[string]string{
							"serving.kserve.io/deploymentMode": "Serverless",
						},
					},
					Spec: v1beta1.InferenceServiceSpec{
						Predictor: v1beta1.PredictorSpec{
							ComponentExtensionSpec: v1beta1.ComponentExtensionSpec{
								MinReplicas: &minScale,
							},
							Tensorflow: &v1beta1.TFServingSpec{
								PredictorExtensionSpec: v1beta1.PredictorExtensionSpec{
									StorageURI:     &storageUri,
									RuntimeVersion: ptr.To("1.14.0"),
									Container: corev1.Container{
										Name:      constants.InferenceServiceContainerName,
										Resources: defaultResource,
									},
								},
							},
						},
					},
				}
				Expect(k8sClient.Create(ctx, isvc)).Should(Succeed())
				defer k8sClient.Delete(ctx, isvc)

				predictorServiceKey := types.NamespacedName{
					Name:      constants.PredictorServiceName(serviceKey.Name),
					Namespace: serviceKey.Namespace,
				}
				actualService := &knservingv1.Service{}
				Eventually(func() error {
					return k8sClient.Get(context.TODO(), predictorServiceKey, actualService)
				}, timeout, interval).Should(Succeed())

				Expect(actualService.Spec.Template.Annotations[autoscaling.InitialScaleAnnotationKey]).To(Equal("0"))
			})
		})
	})
})

// patchAllowZeroInitialScale sets allow-zero-initial-scale in config-autoscaler, or removes it when
// allowed is nil. Each container sets the key itself because Ginkgo shuffles top-level containers
// and upstream's allow-zero AfterEach patches {"data":{}}, which leaves the key in place.
func patchAllowZeroInitialScale(ctx context.Context, allowed *bool) {
	value := "null"
	if allowed != nil {
		value = strconv.Quote(strconv.FormatBool(*allowed))
	}
	configAutoscaler := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      constants.AutoscalerConfigmapName,
			Namespace: constants.AutoscalerConfigmapNamespace,
		},
	}
	configPatch := fmt.Appendf(nil, `{"data":{%q:%s}}`, constants.AutoscalerAllowZeroScaleKey, value)
	Eventually(func() error {
		return k8sClient.Patch(ctx, configAutoscaler, client.RawPatch(types.StrategicMergePatchType, configPatch))
	}, timeout, interval).Should(Succeed())
}
