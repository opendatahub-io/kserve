/*
Copyright 2021 The KServe Authors.

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
	"reflect"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	routev1 "github.com/openshift/api/route/v1"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierr "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"knative.dev/pkg/apis"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/kserve/kserve/pkg/apis/serving/v1beta1"
	"github.com/kserve/kserve/pkg/constants"
)

var _ = Describe("v1beta1 inference service controller - ODH specific tests", func() {
	configs := getRawKubeTestConfigs()

	Context("When creating an inferenceservice with raw kube predictor and ODH auth enabled", func() {
		authConfigs := map[string]string{
			"oauthProxy":         `{"image": "quay.io/opendatahub/odh-kube-auth-proxy@sha256:dcb09fbabd8811f0956ef612a0c9ddd5236804b9bd6548a0647d2b531c9d01b3", "memoryRequest": "64Mi", "memoryLimit": "128Mi", "cpuRequest": "100m", "cpuLimit": "200m"}`,
			"ingress":            `{"ingressGateway": "knative-serving/knative-ingress-gateway", "ingressService": "test-destination", "localGateway": "knative-serving/knative-local-gateway", "localGatewayService": "knative-local-gateway.istio-system.svc.cluster.local"}`,
			"storageInitializer": `{"image": "kserve/storage-initializer:latest", "memoryRequest": "100Mi", "memoryLimit": "1Gi", "cpuRequest": "100m", "cpuLimit": "1", "CaBundleConfigMapName": "", "caBundleVolumeMountPath": "/etc/ssl/custom-certs", "enableDirectPvcVolumeMount": false, "cpuModelcar": "10m", "memoryModelcar": "15Mi"}`,
		}

		It("Should have ingress/service/deployment/hpa/configMap SAR created", func() {
			By("By creating a new InferenceService")
			// Create configmap
			configMap := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      constants.InferenceServiceConfigMapName,
					Namespace: constants.KServeNamespace,
				},
				Data: authConfigs,
			}
			Expect(k8sClient.Create(context.TODO(), configMap)).NotTo(HaveOccurred())
			defer k8sClient.Delete(context.TODO(), configMap)
			// Create ServingRuntime
			servingRuntime := getServingRuntime("tf-serving-raw", "default")
			Expect(k8sClient.Create(context.TODO(), &servingRuntime)).NotTo(HaveOccurred())
			defer k8sClient.Delete(context.TODO(), &servingRuntime)
			serviceName := "raw-auth"
			expectedRequest := reconcile.Request{NamespacedName: types.NamespacedName{Name: serviceName, Namespace: "default"}}
			serviceKey := expectedRequest.NamespacedName
			ctx := context.Background()
			isvc := &v1beta1.InferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      serviceKey.Name,
					Namespace: serviceKey.Namespace,
					Annotations: map[string]string{
						"serving.kserve.io/deploymentMode": "Standard",
						constants.ODHKserveRawAuth:         "true",
					},
					Labels: map[string]string{
						constants.NetworkVisibility: constants.ODHRouteEnabled,
					},
				},
				Spec: v1beta1.InferenceServiceSpec{
					Predictor: v1beta1.PredictorSpec{
						ComponentExtensionSpec: v1beta1.ComponentExtensionSpec{
							MinReplicas: ptr.To(int32(1)),
							MaxReplicas: 3,
						},
						Tensorflow: &v1beta1.TFServingSpec{
							PredictorExtensionSpec: getCommonPredictorExtensionSpec(),
						},
					},
				},
			}
			isvc.DefaultInferenceService(nil, nil, &v1beta1.SecurityConfig{AutoMountServiceAccountToken: false}, nil, nil)
			Expect(k8sClient.Create(ctx, isvc)).Should(Succeed())
			defer k8sClient.Delete(ctx, isvc)

			inferenceService := &v1beta1.InferenceService{}

			Eventually(func() bool {
				err := k8sClient.Get(ctx, serviceKey, inferenceService)
				return err == nil
			}, timeout, interval).Should(BeTrue())

			actualDeployment := &appsv1.Deployment{}
			predictorDeploymentKey := types.NamespacedName{
				Name:      constants.PredictorServiceName(serviceKey.Name),
				Namespace: serviceKey.Namespace,
			}
			Eventually(func() error {
				return k8sClient.Get(context.TODO(), predictorDeploymentKey, actualDeployment)
			}, timeout).Should(Succeed())
			expectedDeployment := getDeploymentWithKServiceLabel(predictorDeploymentKey, serviceName, isvc)

			// Add ODH-specific labels
			expectedDeployment.Spec.Template.Labels["serving.kserve.io/inferenceservice"] = serviceName
			expectedDeployment.Spec.Template.Labels[constants.NetworkVisibility] = constants.ODHRouteEnabled

			// Fix annotations: remove AutoscalerClass, add auth-specific ones
			delete(expectedDeployment.Spec.Template.Annotations, constants.AutoscalerClass)
			expectedDeployment.Spec.Template.Annotations[constants.ModelFormatAnnotationKey] = "tensorflow"
			expectedDeployment.Spec.Template.Annotations[constants.ODHKserveRawAuth] = "true"

			// Add proxy-tls VolumeMount to main container
			expectedDeployment.Spec.Template.Spec.Containers[0].VolumeMounts = []corev1.VolumeMount{
				{
					Name:      "proxy-tls",
					MountPath: "/etc/tls/private",
				},
			}

			// Append kube-rbac-proxy sidecar container
			expectedDeployment.Spec.Template.Spec.Containers = append(
				expectedDeployment.Spec.Template.Spec.Containers,
				corev1.Container{
					Name:  constants.KubeRbacContainerName,
					Image: constants.OauthProxyImage,
					Args: []string{
						`--secure-listen-address=:8443`,
						`--proxy-endpoints-port=8643`,
						`--upstream=http://localhost:8080`,
						`--auth-header-fields-enabled=true`,
						`--tls-cert-file=/etc/tls/private/tls.crt`,
						`--tls-private-key-file=/etc/tls/private/tls.key`,
						`--config-file=/etc/kube-rbac-proxy/config-file.yaml`,
						`--v=4`,
					},
					Ports: []corev1.ContainerPort{
						{
							ContainerPort: constants.OauthProxyPort,
							Name:          "https",
							Protocol:      corev1.ProtocolTCP,
						},
						{
							ContainerPort: constants.OauthProxyProbePort,
							Name:          "proxy",
							Protocol:      corev1.ProtocolTCP,
						},
					},
					LivenessProbe: &corev1.Probe{
						ProbeHandler: corev1.ProbeHandler{
							HTTPGet: &corev1.HTTPGetAction{
								Path:   "/healthz",
								Port:   intstr.FromInt32(constants.OauthProxyProbePort),
								Scheme: corev1.URISchemeHTTPS,
							},
						},
						InitialDelaySeconds: 30,
						TimeoutSeconds:      1,
						PeriodSeconds:       5,
						SuccessThreshold:    1,
						FailureThreshold:    3,
					},
					ReadinessProbe: &corev1.Probe{
						ProbeHandler: corev1.ProbeHandler{
							HTTPGet: &corev1.HTTPGetAction{
								Path:   "/healthz",
								Port:   intstr.FromInt32(constants.OauthProxyProbePort),
								Scheme: corev1.URISchemeHTTPS,
							},
						},
						InitialDelaySeconds: 5,
						TimeoutSeconds:      1,
						PeriodSeconds:       5,
						SuccessThreshold:    1,
						FailureThreshold:    3,
					},
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse(constants.OauthProxyResourceCPULimit),
							corev1.ResourceMemory: resource.MustParse(constants.OauthProxyResourceMemoryLimit),
						},
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse(constants.OauthProxyResourceCPURequest),
							corev1.ResourceMemory: resource.MustParse(constants.OauthProxyResourceMemoryRequest),
						},
					},
					VolumeMounts: []corev1.VolumeMount{
						{
							Name:      "proxy-tls",
							MountPath: "/etc/tls/private",
						},
						{
							Name:      fmt.Sprintf("%s-%s", serviceKey.Name, constants.OauthProxySARCMName),
							MountPath: "/etc/kube-rbac-proxy",
							ReadOnly:  true,
						},
					},
					TerminationMessagePath:   "/dev/termination-log",
					TerminationMessagePolicy: "File",
					ImagePullPolicy:          "IfNotPresent",
				},
			)

			// Add volumes (proxy-tls secret + SAR configmap)
			expectedDeployment.Spec.Template.Spec.Volumes = []corev1.Volume{
				{
					Name: "proxy-tls",
					VolumeSource: corev1.VolumeSource{
						Secret: &corev1.SecretVolumeSource{
							SecretName:  predictorDeploymentKey.Name + constants.ServingCertSecretSuffix,
							DefaultMode: func(i int32) *int32 { return &i }(420),
						},
					},
				},
				{
					Name: fmt.Sprintf("%s-%s", serviceKey.Name, constants.OauthProxySARCMName),
					VolumeSource: corev1.VolumeSource{
						ConfigMap: &corev1.ConfigMapVolumeSource{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: fmt.Sprintf("%s-%s", serviceKey.Name, constants.OauthProxySARCMName),
							},
							DefaultMode: func(i int32) *int32 { return &i }(420),
						},
					},
				},
			}

			// ODH override. See : https://issues.redhat.com/browse/RHOAIENG-19904
			expectedDeployment.Spec.Template.Spec.AutomountServiceAccountToken = ptr.To(true)

			// Use cmpopts.SortMaps for consistent comparison that ignores map key ordering
			Expect(actualDeployment.Spec).To(Equal(expectedDeployment.Spec),
				cmp.Diff(expectedDeployment.Spec, actualDeployment.Spec, cmpopts.SortMaps(func(a, b string) bool { return a < b })))

			// check the SAR configMap
			actualCM := &corev1.ConfigMap{}
			predictorCMKey := types.NamespacedName{
				Name:      fmt.Sprintf("%s-%s", serviceKey.Name, constants.OauthProxySARCMName),
				Namespace: serviceKey.Namespace,
			}
			Eventually(func() error { return k8sClient.Get(context.TODO(), predictorCMKey, actualCM) }, timeout).
				Should(Succeed())

			expectedCM := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("%s-%s", serviceName, constants.OauthProxySARCMName),
					Namespace: serviceKey.Namespace,
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion:         "serving.kserve.io/v1beta1",
							Kind:               "InferenceService",
							Name:               serviceKey.Name,
							UID:                isvc.GetUID(),
							Controller:         ptr.To(true),
							BlockOwnerDeletion: ptr.To(true),
						},
					},
				},
				Data: map[string]string{
					"config-file.yaml": fmt.Sprintf(`authorization:
  resourceAttributes:
    namespace: "%s"
    apiGroup: "serving.kserve.io"
    apiVersion: "v1beta1"
    resource: "inferenceservices"
    name: "%s"
    verb: "get"`, serviceKey.Namespace, serviceKey.Name),
				},
				Immutable: ptr.To(true),
			}

			// Compare the actual ConfigMap with expected (excluding UID which is generated)
			Expect(actualCM.ObjectMeta.Name).To(Equal(expectedCM.Name))
			Expect(actualCM.ObjectMeta.Namespace).To(Equal(expectedCM.Namespace))
			Expect(actualCM.Data).To(Equal(expectedCM.Data))
			Expect(actualCM.Immutable).To(Equal(expectedCM.Immutable))
			Expect(actualCM.UID).NotTo(BeEmpty())

			// check service
			actualService := &corev1.Service{}
			predictorServiceKey := types.NamespacedName{
				Name:      constants.PredictorServiceName(serviceKey.Name),
				Namespace: serviceKey.Namespace,
			}
			Eventually(func() error { return k8sClient.Get(context.TODO(), predictorServiceKey, actualService) }, timeout).
				Should(Succeed())

			expectedService := &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      predictorServiceKey.Name,
					Namespace: predictorServiceKey.Namespace,
				},
				Spec: corev1.ServiceSpec{
					Ports: []corev1.ServicePort{
						{
							Name:       "https",
							Protocol:   "TCP",
							Port:       8443,
							TargetPort: intstr.IntOrString{Type: intstr.String, StrVal: "https"},
						},
					},
					Type:            "ClusterIP",
					SessionAffinity: "None",
					Selector: map[string]string{
						"app": "isvc." + constants.PredictorServiceName(serviceName),
					},
				},
			}
			actualService.Spec.ClusterIP = ""
			actualService.Spec.ClusterIPs = nil
			actualService.Spec.IPFamilies = nil
			actualService.Spec.IPFamilyPolicy = nil
			actualService.Spec.InternalTrafficPolicy = nil
			Expect(actualService.Spec).To(Equal(expectedService.Spec))

			route := &routev1.Route{
				ObjectMeta: metav1.ObjectMeta{
					Name:      serviceKey.Name,
					Namespace: serviceKey.Namespace,
					Labels: map[string]string{
						"inferenceservice-name": serviceName,
					},
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion:         "serving.kserve.io/v1beta1",
							Kind:               "InferenceService",
							Name:               serviceKey.Name,
							UID:                isvc.GetUID(),
							Controller:         ptr.To(true),
							BlockOwnerDeletion: ptr.To(true),
						},
					},
				},
				Spec: routev1.RouteSpec{
					Host: "raw-auth-default.example.com",
					To: routev1.RouteTargetReference{
						Kind:   "Service",
						Name:   predictorServiceKey.Name,
						Weight: ptr.To(int32(100)),
					},
					Port: &routev1.RoutePort{
						TargetPort: intstr.FromInt(8443),
					},
					TLS: &routev1.TLSConfig{
						Termination:                   routev1.TLSTerminationReencrypt,
						InsecureEdgeTerminationPolicy: routev1.InsecureEdgeTerminationPolicyRedirect,
					},
					WildcardPolicy: routev1.WildcardPolicyNone,
				},
			}
			Expect(k8sClient.Create(context.TODO(), route)).Should(Succeed())
			defer k8sClient.Delete(context.TODO(), route)
			route.Status = routev1.RouteStatus{
				Ingress: []routev1.RouteIngress{
					{
						Host: "raw-auth-default.example.com",
						Conditions: []routev1.RouteIngressCondition{
							{
								Type:   routev1.RouteAdmitted,
								Status: corev1.ConditionTrue,
							},
						},
					},
				},
			}
			Expect(k8sClient.Status().Update(ctx, route)).Should(Succeed())

			// check isvc status
			updatedDeployment := actualDeployment.DeepCopy()
			updatedDeployment.Status.Conditions = []appsv1.DeploymentCondition{
				{
					Type:   appsv1.DeploymentAvailable,
					Status: corev1.ConditionTrue,
				},
			}
			Expect(k8sClient.Status().Update(context.TODO(), updatedDeployment)).NotTo(HaveOccurred())

			// verify if InferenceService status is updated
			expectedIsvcStatus := getExpectedIsvcStatus(serviceKey, "https", "raw-auth-default.example.com",
				"raw-auth-predictor-default.example.com", "8443")
			Eventually(func() string {
				isvc := &v1beta1.InferenceService{}
				if err := k8sClient.Get(context.TODO(), serviceKey, isvc); err != nil {
					return err.Error()
				}
				return cmp.Diff(&expectedIsvcStatus, &isvc.Status, cmpopts.IgnoreTypes(apis.VolatileTime{}))
			}, timeout, interval).Should(BeEmpty())
		})

		It("Should recreate configMapSAR when deleted", func() {
			By("By creating a new InferenceService with auth enabled")
			// Create configmap
			configMap := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      constants.InferenceServiceConfigMapName,
					Namespace: constants.KServeNamespace,
				},
				Data: authConfigs,
			}
			Expect(k8sClient.Create(context.TODO(), configMap)).NotTo(HaveOccurred())
			defer k8sClient.Delete(context.TODO(), configMap)

			// Create ServingRuntime
			servingRuntime := getServingRuntime("tf-serving-raw", "default")
			Expect(k8sClient.Create(context.TODO(), &servingRuntime)).NotTo(HaveOccurred())
			defer k8sClient.Delete(context.TODO(), &servingRuntime)

			serviceName := "raw-auth-recreate"
			expectedRequest := reconcile.Request{NamespacedName: types.NamespacedName{Name: serviceName, Namespace: "default"}}
			serviceKey := expectedRequest.NamespacedName
			ctx := context.Background()

			isvc := &v1beta1.InferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      serviceKey.Name,
					Namespace: serviceKey.Namespace,
					Annotations: map[string]string{
						"serving.kserve.io/deploymentMode": string(constants.Standard),
						constants.ODHKserveRawAuth:         "true",
					},
				},
				Spec: v1beta1.InferenceServiceSpec{
					Predictor: v1beta1.PredictorSpec{
						ComponentExtensionSpec: v1beta1.ComponentExtensionSpec{
							MinReplicas: ptr.To(int32(1)),
							MaxReplicas: 3,
						},
						Tensorflow: &v1beta1.TFServingSpec{
							PredictorExtensionSpec: getCommonPredictorExtensionSpec(),
						},
					},
				},
			}
			isvc.DefaultInferenceService(nil, nil, &v1beta1.SecurityConfig{AutoMountServiceAccountToken: false}, nil, nil)
			Expect(k8sClient.Create(ctx, isvc)).Should(Succeed())
			defer k8sClient.Delete(ctx, isvc)

			// Wait for the InferenceService to be created
			inferenceService := &v1beta1.InferenceService{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, serviceKey, inferenceService)
				return err == nil
			}, timeout, interval).Should(BeTrue())

			// Verify the SAR configMap is created initially
			actualCM := &corev1.ConfigMap{}
			predictorCMKey := types.NamespacedName{
				Name:      fmt.Sprintf("%s-%s", serviceKey.Name, constants.OauthProxySARCMName),
				Namespace: serviceKey.Namespace,
			}
			Eventually(func() error {
				return k8sClient.Get(context.TODO(), predictorCMKey, actualCM)
			}, timeout, interval).Should(Succeed())

			// Verify the initial content
			expectedContent := fmt.Sprintf(`authorization:
  resourceAttributes:
    namespace: "%s"
    apiGroup: "serving.kserve.io"
    apiVersion: "v1beta1"
    resource: "inferenceservices"
    name: "%s"
    verb: "get"`, serviceKey.Namespace, serviceKey.Name)
			Expect(actualCM.Data["config-file.yaml"]).To(Equal(expectedContent))

			By("Deleting the SAR configMap")
			Expect(k8sClient.Delete(ctx, actualCM)).Should(Succeed())

			// Verify configMap is deleted
			Eventually(func() bool {
				err := k8sClient.Get(context.TODO(), predictorCMKey, actualCM)
				return apierr.IsNotFound(err)
			}, timeout, interval).Should(BeTrue())

			By("Triggering reconciliation by updating the InferenceService")
			// Update the InferenceService to trigger reconciliation
			Eventually(func() error {
				if err := k8sClient.Get(ctx, serviceKey, inferenceService); err != nil {
					return err
				}
				if inferenceService.Annotations == nil {
					inferenceService.Annotations = make(map[string]string)
				}
				inferenceService.Annotations["test-reconcile"] = "trigger-recreation"
				return k8sClient.Update(ctx, inferenceService)
			}, timeout, interval).Should(Succeed())

			By("Verifying the SAR configMap is recreated")
			newCM := &corev1.ConfigMap{}
			Eventually(func() error {
				return k8sClient.Get(context.TODO(), predictorCMKey, newCM)
			}, timeout, interval).Should(Succeed())

			// Verify the recreated configMap has the correct content
			Expect(newCM.Data["config-file.yaml"]).To(Equal(expectedContent))
			Expect(newCM.ObjectMeta.Name).To(Equal(fmt.Sprintf("%s-%s", serviceKey.Name, constants.OauthProxySARCMName)))
			Expect(newCM.ObjectMeta.Namespace).To(Equal(serviceKey.Namespace))

			// Verify owner reference is set correctly
			ownerFound := false
			for _, owner := range newCM.OwnerReferences {
				if owner.Kind == "InferenceService" && owner.Name == serviceKey.Name {
					ownerFound = true
					Expect(owner.Controller).To(Equal(ptr.To(true)))
					Expect(owner.BlockOwnerDeletion).To(Equal(ptr.To(true)))
					break
				}
			}
			Expect(ownerFound).To(BeTrue(), "Owner reference should be set correctly")
		})
	})

	It("Should only have the ImagePullSecrets that are specified in the InferenceService", func() {
		By("Updating an InferenceService with a new ImagePullSecret and checking the deployment")
		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      constants.InferenceServiceConfigMapName,
				Namespace: constants.KServeNamespace,
			},
			Data: configs,
		}
		Expect(k8sClient.Create(context.TODO(), configMap)).NotTo(HaveOccurred())
		defer k8sClient.Delete(context.TODO(), configMap)

		servingRuntime := getServingRuntime("tf-serving-raw", constants.KServeNamespace)
		Expect(k8sClient.Create(context.TODO(), &servingRuntime)).NotTo(HaveOccurred())
		defer k8sClient.Delete(context.TODO(), &servingRuntime)

		serviceName := "raw-isvc-image-pull-secret"
		serviceKey := types.NamespacedName{Name: serviceName, Namespace: constants.KServeNamespace}
		storageUri := "s3://test/mnist/export"
		ctx := context.Background()

		isvc := &v1beta1.InferenceService{
			ObjectMeta: metav1.ObjectMeta{
				Name:      serviceKey.Name,
				Namespace: serviceKey.Namespace,
				Annotations: map[string]string{
					"serving.kserve.io/deploymentMode": string(constants.Standard),
				},
			},
			Spec: v1beta1.InferenceServiceSpec{
				Predictor: v1beta1.PredictorSpec{
					ComponentExtensionSpec: v1beta1.ComponentExtensionSpec{
						MinReplicas: ptr.To(int32(1)),
						MaxReplicas: 3,
					},
					PodSpec: v1beta1.PodSpec{
						ImagePullSecrets: []corev1.LocalObjectReference{
							{Name: "isvc-image-pull-secret"},
						},
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
		isvc.DefaultInferenceService(nil, nil, &v1beta1.SecurityConfig{AutoMountServiceAccountToken: false}, nil, nil)
		Expect(k8sClient.Create(ctx, isvc)).Should(Succeed())
		defer k8sClient.Delete(ctx, isvc)

		// Wait for the deployment to be created
		predictorDeploymentKey := types.NamespacedName{
			Name:      constants.PredictorServiceName(serviceKey.Name),
			Namespace: serviceKey.Namespace,
		}
		actualDeployment := &appsv1.Deployment{}
		Eventually(func() error {
			return k8sClient.Get(ctx, predictorDeploymentKey, actualDeployment)
		}, timeout, interval).Should(Succeed())

		// Verify initial ImagePullSecrets
		expectedImagePullSecrets := []corev1.LocalObjectReference{
			{Name: "isvc-image-pull-secret"},
		}
		Expect(actualDeployment.Spec.Template.Spec.ImagePullSecrets).To(Equal(expectedImagePullSecrets))

		// Update the ISVC with a new ImagePullSecret
		By("Updating the InferenceService with a new ImagePullSecret")
		Eventually(func() error {
			if err := k8sClient.Get(ctx, serviceKey, isvc); err != nil {
				return err
			}
			isvc.Spec.Predictor.ImagePullSecrets = []corev1.LocalObjectReference{
				{Name: "new-image-pull-secret"},
			}
			return k8sClient.Update(ctx, isvc)
		}, timeout, interval).Should(Succeed())

		// Verify the deployment has only the new ImagePullSecret
		expectedImagePullSecrets = []corev1.LocalObjectReference{
			{Name: "new-image-pull-secret"},
		}
		updatedDeployment := &appsv1.Deployment{}
		Eventually(func() (bool, error) {
			if err := k8sClient.Get(ctx, predictorDeploymentKey, updatedDeployment); err != nil {
				return false, err
			}
			if len(updatedDeployment.Spec.Template.Spec.ImagePullSecrets) != 1 {
				return false, nil
			}
			return reflect.DeepEqual(updatedDeployment.Spec.Template.Spec.ImagePullSecrets, expectedImagePullSecrets), nil
		}, timeout, interval).Should(BeTrue())
	})
})
