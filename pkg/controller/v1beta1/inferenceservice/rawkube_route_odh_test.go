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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	routev1 "github.com/openshift/api/route/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kserve/kserve/pkg/apis/serving/v1beta1"
	"github.com/kserve/kserve/pkg/constants"
	"github.com/kserve/kserve/pkg/controller/v1beta1/inferenceservice/reconcilers/ingress"
)

// admitRoute stands in for OpenShift: the API server allocates spec.host when a Route is created
// without one, and a router records admission in status.
func admitRoute(ctx context.Context, key types.NamespacedName, host string) {
	GinkgoHelper()
	Eventually(func(g Gomega) {
		route := &routev1.Route{}
		g.Expect(k8sClient.Get(ctx, key, route)).To(Succeed())
		route.Spec.Host = host
		g.Expect(k8sClient.Update(ctx, route)).To(Succeed())
		route.Status.Ingress = []routev1.RouteIngress{{
			Host:       host,
			Conditions: []routev1.RouteIngressCondition{{Type: routev1.RouteAdmitted, Status: corev1.ConditionTrue}},
		}}
		g.Expect(k8sClient.Status().Update(ctx, route)).To(Succeed())
	}, timeout, interval).Should(Succeed())
}

var _ = Describe("OpenShift Route and auth-delegator binding of raw InferenceServices", func() {
	// The ODH overlay disables Kubernetes Ingress creation.
	configs := mergeJSONField(getBaseTestConfigs(), "ingress", map[string]interface{}{
		"disableIngressCreation": true,
	})

	var namespace string

	BeforeEach(func(ctx SpecContext) {
		configMap := createInferenceServiceConfigMap(configs)
		Expect(k8sClient.Create(ctx, configMap)).To(Succeed())
		DeferCleanup(func(ctx SpecContext) {
			Expect(k8sClient.Delete(ctx, configMap)).To(Succeed())
		})

		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "route-handoff-"}}
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())
		namespace = ns.Name

		servingRuntime := getServingRuntime("tf-serving-raw", namespace)
		Expect(k8sClient.Create(ctx, &servingRuntime)).To(Succeed())
	})

	newISVC := func(name string) *v1beta1.InferenceService {
		isvc := &v1beta1.InferenceService{
			ObjectMeta: metav1.ObjectMeta{
				Name:        name,
				Namespace:   namespace,
				Labels:      map[string]string{},
				Annotations: getDefaultAnnotations(constants.AutoscalerClassNone),
			},
			Spec: v1beta1.InferenceServiceSpec{
				Predictor: v1beta1.PredictorSpec{
					ComponentExtensionSpec: v1beta1.ComponentExtensionSpec{MinReplicas: ptr.To(int32(1))},
					Model: &v1beta1.ModelSpec{
						ModelFormat: v1beta1.ModelFormat{Name: "tensorflow"},
						PredictorExtensionSpec: v1beta1.PredictorExtensionSpec{
							StorageURI:     ptr.To("s3://test/model"),
							RuntimeVersion: ptr.To("1.14.0"),
						},
					},
				},
			},
		}
		return isvc
	}

	createISVC := func(ctx context.Context, isvc *v1beta1.InferenceService) {
		GinkgoHelper()
		isvc.DefaultInferenceService(nil, nil, &v1beta1.SecurityConfig{AutoMountServiceAccountToken: false}, nil, nil)
		Expect(k8sClient.Create(ctx, isvc)).To(Succeed())
		// Finalization reads the inferenceservice-config ConfigMap, so the InferenceService has to
		// be gone before the ConfigMap cleanup registered earlier runs.
		DeferCleanup(func(ctx SpecContext) {
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, isvc))).To(Succeed())
			Eventually(func() bool {
				return apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKeyFromObject(isvc), &v1beta1.InferenceService{}))
			}, timeout, interval).Should(BeTrue())
		})
	}

	updateISVC := func(ctx context.Context, key types.NamespacedName, mutate func(*v1beta1.InferenceService)) {
		GinkgoHelper()
		Eventually(func(g Gomega) {
			isvc := &v1beta1.InferenceService{}
			g.Expect(k8sClient.Get(ctx, key, isvc)).To(Succeed())
			mutate(isvc)
			g.Expect(k8sClient.Update(ctx, isvc)).To(Succeed())
		}, timeout, interval).Should(Succeed())
	}

	deleteISVC := func(ctx context.Context, isvc *v1beta1.InferenceService) {
		GinkgoHelper()
		Expect(k8sClient.Delete(ctx, isvc)).To(Succeed())
		Eventually(func() bool {
			return apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKeyFromObject(isvc), &v1beta1.InferenceService{}))
		}, timeout, interval).Should(BeTrue())
	}

	// waitForIngressReconcile returns once the controller got past the platform permissions and
	// recorded an ingress outcome for the InferenceService.
	waitForIngressReconcile := func(ctx context.Context, key types.NamespacedName) {
		GinkgoHelper()
		Eventually(func(g Gomega) {
			isvc := &v1beta1.InferenceService{}
			g.Expect(k8sClient.Get(ctx, key, isvc)).To(Succeed())
			g.Expect(isvc.Status.GetCondition(v1beta1.IngressReady)).NotTo(BeNil())
			g.Expect(isvc.Status.GetCondition(v1beta1.IngressReady).Status).NotTo(Equal(corev1.ConditionUnknown))
		}, timeout, interval).Should(Succeed())
	}

	bindingKey := func(serviceAccount string) types.NamespacedName {
		return types.NamespacedName{Name: namespace + "-" + serviceAccount + "-auth-delegator"}
	}

	bindingExists := func(ctx context.Context, serviceAccount string) func() error {
		return func() error {
			return k8sClient.Get(ctx, bindingKey(serviceAccount), &rbacv1.ClusterRoleBinding{})
		}
	}

	bindingGone := func(ctx context.Context, serviceAccount string) func() bool {
		return func() bool {
			return apierrors.IsNotFound(k8sClient.Get(ctx, bindingKey(serviceAccount), &rbacv1.ClusterRoleBinding{}))
		}
	}

	Context("auth-delegator ClusterRoleBinding", func() {
		It("binds the default service account of an auth-enabled InferenceService", func(ctx SpecContext) {
			isvc := newISVC("auth-default")
			isvc.Annotations[constants.ODHKserveRawAuth] = "true"
			createISVC(ctx, isvc)

			crb := &rbacv1.ClusterRoleBinding{}
			Eventually(func() error { return k8sClient.Get(ctx, bindingKey("default"), crb) }, timeout, interval).Should(Succeed())
			Expect(crb.OwnerReferences).To(BeEmpty())
			Expect(crb.Subjects).To(Equal([]rbacv1.Subject{{Kind: "ServiceAccount", Name: "default", Namespace: namespace}}))
			Expect(crb.RoleRef).To(Equal(rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: "system:auth-delegator"}))

			// Not exposed, so no Route.
			waitForIngressReconcile(ctx, client.ObjectKeyFromObject(isvc))
			err := k8sClient.Get(ctx, client.ObjectKeyFromObject(isvc), &routev1.Route{})
			Expect(apierrors.IsNotFound(err)).To(BeTrue(), "unexpected error: %v", err)
		})

		It("binds a custom predictor service account", func(ctx SpecContext) {
			isvc := newISVC("auth-custom-sa")
			isvc.Annotations[constants.ODHKserveRawAuth] = "true"
			isvc.Spec.Predictor.ServiceAccountName = "custom-sa"
			createISVC(ctx, isvc)

			Eventually(bindingExists(ctx, "custom-sa"), timeout, interval).Should(Succeed())
		})

		It("keeps the binding while an auth-enabled InferenceService shares the service account", func(ctx SpecContext) {
			withAuth := newISVC("shared-sa-auth")
			withAuth.Annotations[constants.ODHKserveRawAuth] = "true"
			withAuth.Spec.Predictor.ServiceAccountName = "custom-sa"
			createISVC(ctx, withAuth)
			Eventually(bindingExists(ctx, "custom-sa"), timeout, interval).Should(Succeed())

			withoutAuth := newISVC("shared-sa-no-auth")
			withoutAuth.Spec.Predictor.ServiceAccountName = "custom-sa"
			createISVC(ctx, withoutAuth)
			waitForIngressReconcile(ctx, client.ObjectKeyFromObject(withoutAuth))

			Expect(bindingExists(ctx, "custom-sa")()).To(Succeed())
		})

		It("restores a binding deleted while an auth-enabled InferenceService still uses it", func(ctx SpecContext) {
			isvc := newISVC("binding-restored")
			isvc.Annotations[constants.ODHKserveRawAuth] = "true"
			isvc.Spec.Predictor.ServiceAccountName = "restored-sa"
			createISVC(ctx, isvc)
			Eventually(bindingExists(ctx, "restored-sa"), timeout, interval).Should(Succeed())

			Expect(k8sClient.Delete(ctx, &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: bindingKey("restored-sa").Name}})).To(Succeed())

			Eventually(bindingExists(ctx, "restored-sa"), timeout, interval).Should(Succeed())
		})

		// odh-model-controller never removed the default service account binding.
		It("removes the default service account binding once auth is turned off", func(ctx SpecContext) {
			isvc := newISVC("auth-turned-off")
			isvc.Annotations[constants.ODHKserveRawAuth] = "true"
			createISVC(ctx, isvc)
			Eventually(bindingExists(ctx, "default"), timeout, interval).Should(Succeed())

			updateISVC(ctx, client.ObjectKeyFromObject(isvc), func(isvc *v1beta1.InferenceService) {
				isvc.Annotations[constants.ODHKserveRawAuth] = "false"
			})

			Eventually(bindingGone(ctx, "default"), timeout, interval).Should(BeTrue())
		})

		It("deletes the binding only after the last InferenceService using it is deleted", func(ctx SpecContext) {
			authISVC := func(name, serviceAccount string) *v1beta1.InferenceService {
				isvc := newISVC(name)
				isvc.Annotations[constants.ODHKserveRawAuth] = "true"
				isvc.Spec.Predictor.ServiceAccountName = serviceAccount
				createISVC(ctx, isvc)
				return isvc
			}
			default1, default2 := authISVC("default-1", ""), authISVC("default-2", "")
			custom1, custom2 := authISVC("custom-1", "custom-sa"), authISVC("custom-2", "custom-sa")
			Eventually(bindingExists(ctx, "default"), timeout, interval).Should(Succeed())
			Eventually(bindingExists(ctx, "custom-sa"), timeout, interval).Should(Succeed())

			deleteISVC(ctx, default1)
			deleteISVC(ctx, custom1)
			Expect(bindingExists(ctx, "default")()).To(Succeed())
			Expect(bindingExists(ctx, "custom-sa")()).To(Succeed())

			deleteISVC(ctx, default2)
			deleteISVC(ctx, custom2)
			Eventually(bindingGone(ctx, "default"), timeout, interval).Should(BeTrue())
			Eventually(bindingGone(ctx, "custom-sa"), timeout, interval).Should(BeTrue())
		})
	})

	Context("Route", func() {
		exposed := func(isvc *v1beta1.InferenceService) *v1beta1.InferenceService {
			isvc.Labels[constants.NetworkVisibility] = constants.ODHRouteEnabled
			return isvc
		}

		getRoute := func(ctx context.Context, key types.NamespacedName) func() (*routev1.Route, error) {
			return func() (*routev1.Route, error) {
				route := &routev1.Route{}
				return route, k8sClient.Get(ctx, key, route)
			}
		}

		It("creates the Route odh-model-controller creates for an exposed InferenceService", func(ctx SpecContext) {
			isvc := exposed(newISVC("exposed"))
			isvc.Annotations[constants.ODHKserveRawAuth] = "true"
			createISVC(ctx, isvc)
			key := client.ObjectKeyFromObject(isvc)

			route := &routev1.Route{}
			Eventually(func() error { return k8sClient.Get(ctx, key, route) }, timeout, interval).Should(Succeed())

			Expect(route.Labels).To(Equal(map[string]string{"inferenceservice-name": isvc.Name}))
			Expect(route.Annotations).To(Equal(map[string]string{"haproxy.router.openshift.io/timeout": "30s"}))
			Expect(route.OwnerReferences).To(Equal([]metav1.OwnerReference{{
				APIVersion: "serving.kserve.io/v1beta1", Kind: "InferenceService", Name: isvc.Name, UID: isvc.UID,
				Controller: ptr.To(true), BlockOwnerDeletion: ptr.To(true),
			}}))
			Expect(route.Spec).To(Equal(routev1.RouteSpec{
				To:   routev1.RouteTargetReference{Kind: "Service", Name: constants.PredictorServiceName(isvc.Name), Weight: ptr.To[int32](100)},
				Port: &routev1.RoutePort{TargetPort: intstr.FromString("https")},
				TLS: &routev1.TLSConfig{
					Termination:                   routev1.TLSTerminationReencrypt,
					InsecureEdgeTerminationPolicy: routev1.InsecureEdgeTerminationPolicyRedirect,
				},
				WildcardPolicy: routev1.WildcardPolicyNone,
			}))
		})

		It("reports the Route URL once a router admits it", func(ctx SpecContext) {
			isvc := exposed(newISVC("admission"))
			createISVC(ctx, isvc)
			key := client.ObjectKeyFromObject(isvc)

			Eventually(func(g Gomega) {
				current := &v1beta1.InferenceService{}
				g.Expect(k8sClient.Get(ctx, key, current)).To(Succeed())
				cond := current.Status.GetCondition(v1beta1.IngressReady)
				g.Expect(cond).NotTo(BeNil())
				g.Expect(cond.Status).To(Equal(corev1.ConditionFalse))
				g.Expect(cond.Reason).To(Equal(ingress.RouteNotAdmittedReason))
				g.Expect(current.Status.URL).To(BeNil())
			}, timeout, interval).Should(Succeed())

			admitRoute(ctx, key, "admission.apps.example.com")

			// Well inside the admission requeue interval: the Route watch has to deliver it.
			Eventually(func(g Gomega) {
				current := &v1beta1.InferenceService{}
				g.Expect(k8sClient.Get(ctx, key, current)).To(Succeed())
				g.Expect(current.Status.IsConditionReady(v1beta1.IngressReady)).To(BeTrue())
				g.Expect(current.Status.URL).NotTo(BeNil())
				g.Expect(current.Status.URL.String()).To(Equal("https://admission.apps.example.com"))
			}, 15*time.Second, interval).Should(Succeed())
		})

		It("adopts the Route and binding odh-model-controller created without writing them", func(ctx SpecContext) {
			isvc := exposed(newISVC("adopted"))
			isvc.Annotations[constants.ODHKserveRawAuth] = "true"
			key := types.NamespacedName{Name: isvc.Name, Namespace: namespace}

			// odh-model-controller also set metadata.namespace, which the API server drops.
			odhBinding := &rbacv1.ClusterRoleBinding{
				ObjectMeta: metav1.ObjectMeta{Name: namespace + "-default-auth-delegator", Namespace: namespace},
				Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Namespace: namespace, Name: "default"}},
				RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: "system:auth-delegator"},
			}
			Expect(k8sClient.Create(ctx, odhBinding)).To(Succeed())
			DeferCleanup(func(ctx SpecContext) {
				Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, odhBinding))).To(Succeed())
			})
			// The Route exists before kserve reconciles the InferenceService; its owner reference
			// is added below, once the InferenceService UID is known.
			odhRoute := &routev1.Route{
				ObjectMeta: metav1.ObjectMeta{
					Name:        key.Name,
					Namespace:   key.Namespace,
					Labels:      map[string]string{"inferenceservice-name": key.Name},
					Annotations: map[string]string{"haproxy.router.openshift.io/timeout": "30s"},
				},
				Spec: routev1.RouteSpec{
					To:   routev1.RouteTargetReference{Kind: "Service", Name: constants.PredictorServiceName(key.Name), Weight: ptr.To[int32](100)},
					Port: &routev1.RoutePort{TargetPort: intstr.FromString("https")},
					TLS: &routev1.TLSConfig{
						Termination:                   routev1.TLSTerminationReencrypt,
						InsecureEdgeTerminationPolicy: routev1.InsecureEdgeTerminationPolicyRedirect,
					},
					WildcardPolicy: routev1.WildcardPolicyNone,
				},
			}
			Expect(k8sClient.Create(ctx, odhRoute)).To(Succeed())

			createISVC(ctx, isvc)
			Eventually(func(g Gomega) {
				current := &v1beta1.InferenceService{}
				g.Expect(k8sClient.Get(ctx, key, current)).To(Succeed())
				cond := current.Status.GetCondition(v1beta1.IngressReady)
				g.Expect(cond).NotTo(BeNil())
				g.Expect(cond.Reason).To(Equal(ingress.RouteNotOwnedReason))
			}, timeout, interval).Should(Succeed())

			created := &v1beta1.InferenceService{}
			Expect(k8sClient.Get(ctx, key, created)).To(Succeed())
			Eventually(func(g Gomega) {
				route, err := getRoute(ctx, key)()
				g.Expect(err).NotTo(HaveOccurred())
				route.OwnerReferences = []metav1.OwnerReference{{
					APIVersion: "serving.kserve.io/v1beta1", Kind: "InferenceService", Name: key.Name, UID: created.UID,
					Controller: ptr.To(true), BlockOwnerDeletion: ptr.To(true),
				}}
				g.Expect(k8sClient.Update(ctx, route)).To(Succeed())
			}, timeout, interval).Should(Succeed())
			admitRoute(ctx, key, "adopted.apps.example.com")
			admittedRoute, err := getRoute(ctx, key)()
			Expect(err).NotTo(HaveOccurred())

			Eventually(func(g Gomega) {
				current := &v1beta1.InferenceService{}
				g.Expect(k8sClient.Get(ctx, key, current)).To(Succeed())
				g.Expect(current.Status.URL).NotTo(BeNil())
				g.Expect(current.Status.URL.String()).To(Equal("https://adopted.apps.example.com"))
			}, timeout, interval).Should(Succeed())
			binding := &rbacv1.ClusterRoleBinding{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(odhBinding), binding)).To(Succeed())

			Consistently(func(g Gomega) {
				route, err := getRoute(ctx, key)()
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(route.ResourceVersion).To(Equal(admittedRoute.ResourceVersion))
				current := &rbacv1.ClusterRoleBinding{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(odhBinding), current)).To(Succeed())
				g.Expect(current.ResourceVersion).To(Equal(binding.ResourceVersion))
				g.Expect(current.CreationTimestamp).To(Equal(binding.CreationTimestamp))
			}, 5*time.Second, interval).Should(Succeed())
			// Neither object was rewritten since the test created them.
			Expect(binding.ResourceVersion).To(Equal(odhBinding.ResourceVersion))
		})

		It("follows the predictor timeout and the timeout annotation", func(ctx SpecContext) {
			isvc := exposed(newISVC("timeout"))
			isvc.Spec.Predictor.TimeoutSeconds = ptr.To[int64](45)
			createISVC(ctx, isvc)
			key := client.ObjectKeyFromObject(isvc)

			Eventually(func(g Gomega) {
				route, err := getRoute(ctx, key)()
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(route.Annotations).To(HaveKeyWithValue("haproxy.router.openshift.io/timeout", "45s"))
			}, timeout, interval).Should(Succeed())

			updateISVC(ctx, key, func(isvc *v1beta1.InferenceService) {
				isvc.Annotations["haproxy.router.openshift.io/timeout"] = "1m"
			})

			Eventually(func(g Gomega) {
				route, err := getRoute(ctx, key)()
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(route.Annotations).To(HaveKeyWithValue("haproxy.router.openshift.io/timeout", "1m"))
			}, timeout, interval).Should(Succeed())
		})

		It("splits traffic between the stable predictor and canaries", func(ctx SpecContext) {
			isvc := exposed(newISVC("canary"))
			isvc.Spec.Canary = []v1beta1.CanarySpec{{
				TrafficPercent: 20,
				Predictor: v1beta1.PredictorSpec{
					Name: "v2",
					Model: &v1beta1.ModelSpec{
						ModelFormat: v1beta1.ModelFormat{Name: "tensorflow"},
						PredictorExtensionSpec: v1beta1.PredictorExtensionSpec{
							StorageURI:     ptr.To("s3://test/model-v2"),
							RuntimeVersion: ptr.To("1.14.0"),
						},
					},
				},
			}}
			createISVC(ctx, isvc)
			key := client.ObjectKeyFromObject(isvc)

			Eventually(func(g Gomega) {
				route, err := getRoute(ctx, key)()
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(route.Spec.To).To(Equal(routev1.RouteTargetReference{
					Kind: "Service", Name: constants.PredictorServiceName(isvc.Name), Weight: ptr.To[int32](80),
				}))
				g.Expect(route.Spec.AlternateBackends).To(Equal([]routev1.RouteTargetReference{{
					Kind: "Service", Name: constants.PredictorServiceName(isvc.Name, "v2"), Weight: ptr.To[int32](20),
				}}))
				// Resolved to the target port, which every backend Service shares.
				g.Expect(route.Spec.Port).To(Equal(&routev1.RoutePort{TargetPort: intstr.FromInt32(8080)}))
			}, timeout, interval).Should(Succeed())
		})

		It("deletes its Route once the InferenceService is no longer exposed", func(ctx SpecContext) {
			isvc := exposed(newISVC("unexposed"))
			createISVC(ctx, isvc)
			key := client.ObjectKeyFromObject(isvc)
			Eventually(func() error { _, err := getRoute(ctx, key)(); return err }, timeout, interval).Should(Succeed())

			updateISVC(ctx, key, func(isvc *v1beta1.InferenceService) {
				delete(isvc.Labels, constants.NetworkVisibility)
			})

			Eventually(func() bool {
				_, err := getRoute(ctx, key)()
				return apierrors.IsNotFound(err)
			}, timeout, interval).Should(BeTrue())
		})

		It("never modifies or deletes a Route it does not control", func(ctx SpecContext) {
			isvc := exposed(newISVC("foreign-route"))
			key := types.NamespacedName{Name: isvc.Name, Namespace: namespace}
			foreign := &routev1.Route{
				ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
				Spec: routev1.RouteSpec{
					To:             routev1.RouteTargetReference{Kind: "Service", Name: "user-service", Weight: ptr.To[int32](100)},
					WildcardPolicy: routev1.WildcardPolicyNone,
				},
			}
			Expect(k8sClient.Create(ctx, foreign)).To(Succeed())
			createISVC(ctx, isvc)

			Eventually(func(g Gomega) {
				current := &v1beta1.InferenceService{}
				g.Expect(k8sClient.Get(ctx, key, current)).To(Succeed())
				cond := current.Status.GetCondition(v1beta1.IngressReady)
				g.Expect(cond).NotTo(BeNil())
				g.Expect(cond.Reason).To(Equal(ingress.RouteNotOwnedReason))
			}, timeout, interval).Should(Succeed())
			route, err := getRoute(ctx, key)()
			Expect(err).NotTo(HaveOccurred())
			Expect(route.ResourceVersion).To(Equal(foreign.ResourceVersion))

			updateISVC(ctx, key, func(isvc *v1beta1.InferenceService) {
				delete(isvc.Labels, constants.NetworkVisibility)
			})
			waitForIngressReconcile(ctx, key)
			Eventually(func(g Gomega) {
				current := &v1beta1.InferenceService{}
				g.Expect(k8sClient.Get(ctx, key, current)).To(Succeed())
				g.Expect(current.Status.IsConditionReady(v1beta1.IngressReady)).To(BeTrue())
			}, timeout, interval).Should(Succeed())

			deleteISVC(ctx, isvc)
			route, err = getRoute(ctx, key)()
			Expect(err).NotTo(HaveOccurred())
			Expect(route.ResourceVersion).To(Equal(foreign.ResourceVersion))
		})
	})
})
