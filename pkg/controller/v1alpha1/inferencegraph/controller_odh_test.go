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

package inferencegraph

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	osv1 "github.com/openshift/api/route/v1"
	"google.golang.org/protobuf/proto"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"knative.dev/serving/pkg/apis/autoscaling"
	knservingv1 "knative.dev/serving/pkg/apis/serving/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
	"github.com/kserve/kserve/pkg/constants"
)

var _ = Describe("Inference Graph controller ODH test", func() {
	const (
		timeout  = time.Second * 10
		interval = time.Millisecond * 250
	)

	configs := map[string]string{
		"router": `{
				"image": "kserve/router:v0.10.0",
				"memoryRequest": "100Mi",
				"memoryLimit": "500Mi",
				"cpuRequest": "100m",
				"cpuLimit": "100m",
				"headers": {
				"propagate": [
					"Authorization",
					"Intuit_tid"
				]
				}
		}`,
		"ingress": `{
			"kserveIngressGateway": "kserve/kserve-ingress-gateway",
			"ingressGateway": "knative-serving/knative-ingress-gateway",
			"localGateway": "knative-serving/knative-local-gateway",
			"localGatewayService": "knative-local-gateway.istio-system.svc.cluster.local"
		}`,
		"storageInitializer": `{
			"image" : "kserve/storage-initializer:latest",
			"memoryRequest": "100Mi",
			"memoryLimit": "1Gi",
			"cpuRequest": "100m",
			"cpuLimit": "1",
			"cpuModelcar": "10m",
			"memoryModelcar": "15Mi",
			"CaBundleConfigMapName": "",
			"caBundleVolumeMountPath": "/etc/ssl/custom-certs"
		}`,
	}

	createConfigMap := func(ctx SpecContext) {
		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      constants.InferenceServiceConfigMapName,
				Namespace: constants.KServeNamespace,
			},
			Data: configs,
		}
		Expect(k8sClient.Create(ctx, configMap)).NotTo(HaveOccurred())
		DeferCleanup(func(ctx SpecContext) {
			_ = k8sClient.Delete(ctx, configMap)
		})
	}

	// The upstream suite restores config-autoscaler with an empty strategic merge patch, which
	// leaves allow-zero-initial-scale in place, so each context sets the key explicitly.
	patchAllowZeroInitialScale := func(ctx SpecContext, value string) {
		configAutoscaler := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      constants.AutoscalerConfigmapName,
				Namespace: constants.AutoscalerConfigmapNamespace,
			},
		}
		configPatch := []byte(fmt.Sprintf(`{"data":{%q:%s}}`, constants.AutoscalerAllowZeroScaleKey, value))
		Eventually(func() error {
			return k8sClient.Patch(ctx, configAutoscaler, client.RawPatch(types.MergePatchType, configPatch))
		}, timeout).Should(Succeed())
	}

	zeroMinReplicasGraph := func(graphName string) *v1alpha1.InferenceGraph {
		var minScale int32 = 0
		return &v1alpha1.InferenceGraph{
			ObjectMeta: metav1.ObjectMeta{
				Name:      graphName,
				Namespace: "default",
				Annotations: map[string]string{
					"serving.kserve.io/deploymentMode": string(constants.Knative),
				},
			},
			Spec: v1alpha1.InferenceGraphSpec{
				MinReplicas: &minScale,
				Nodes: map[string]v1alpha1.InferenceRouter{
					v1alpha1.GraphRootNodeName: {
						RouterType: v1alpha1.Sequence,
						Steps: []v1alpha1.InferenceStep{
							{
								InferenceTarget: v1alpha1.InferenceTarget{
									ServiceURL: "http://someservice.exmaple.com",
								},
							},
						},
					},
				},
			},
		}
	}

	rawGraph := func(graphName string, labels map[string]string) *v1alpha1.InferenceGraph {
		return &v1alpha1.InferenceGraph{
			ObjectMeta: metav1.ObjectMeta{
				Name:      graphName,
				Namespace: "default",
				Annotations: map[string]string{
					"serving.kserve.io/deploymentMode": string(constants.Standard),
				},
				Labels: labels,
			},
			Spec: v1alpha1.InferenceGraphSpec{
				Nodes: map[string]v1alpha1.InferenceRouter{
					v1alpha1.GraphRootNodeName: {
						RouterType: v1alpha1.Sequence,
						Steps: []v1alpha1.InferenceStep{
							{
								InferenceTarget: v1alpha1.InferenceTarget{
									ServiceURL: "http://someservice.exmaple.com",
								},
							},
						},
					},
				},
			},
		}
	}

	// envtest runs no Deployment controller, so the graph only moves on to networking once the
	// test marks the router Deployment available.
	markDeploymentAvailable := func(ctx SpecContext, key types.NamespacedName) {
		Eventually(func(g Gomega) {
			deployment := &appsv1.Deployment{}
			g.Expect(k8sClient.Get(ctx, key, deployment)).To(Succeed())
			deployment.Status.Conditions = []appsv1.DeploymentCondition{
				{Type: appsv1.DeploymentAvailable},
			}
			g.Expect(k8sClient.Status().Update(ctx, deployment)).To(Succeed())
		}, timeout, interval).Should(Succeed())
	}

	Context("with knative configured to not allow zero initial scale", func() {
		BeforeEach(func(ctx SpecContext) {
			patchAllowZeroInitialScale(ctx, "null")
		})

		When("a Serverless InferenceGraph is created with zero min replicas", func() {
			It("should use the default initial scale value", func(ctx SpecContext) {
				createConfigMap(ctx)

				ig := zeroMinReplicasGraph("initialscale7")
				Expect(k8sClient.Create(ctx, ig)).Should(Succeed())
				DeferCleanup(func(ctx SpecContext) {
					_ = k8sClient.Delete(ctx, ig)
				})

				actualService := &knservingv1.Service{}
				Eventually(func() error {
					return k8sClient.Get(ctx, client.ObjectKeyFromObject(ig), actualService)
				}, timeout).
					Should(Succeed())
				Expect(actualService.Spec.Template.Annotations).NotTo(HaveKey(autoscaling.InitialScaleAnnotationKey))
			})
		})
	})

	Context("with knative configured to allow zero initial scale", func() {
		BeforeEach(func(ctx SpecContext) {
			patchAllowZeroInitialScale(ctx, `"true"`)
		})
		AfterEach(func(ctx SpecContext) {
			patchAllowZeroInitialScale(ctx, "null")
		})

		When("a Serverless InferenceGraph is created with zero min replicas", func() {
			It("should override the default initial scale value with zero", func(ctx SpecContext) {
				createConfigMap(ctx)

				ig := zeroMinReplicasGraph("initialscale8")
				Expect(k8sClient.Create(ctx, ig)).Should(Succeed())
				DeferCleanup(func(ctx SpecContext) {
					_ = k8sClient.Delete(ctx, ig)
				})

				actualService := &knservingv1.Service{}
				Eventually(func() error {
					return k8sClient.Get(ctx, client.ObjectKeyFromObject(ig), actualService)
				}, timeout).
					Should(Succeed())
				Expect(actualService.Spec.Template.Annotations[autoscaling.InitialScaleAnnotationKey]).To(Equal("0"))
			})
		})
	})

	Context("When creating an inferencegraph in Raw deployment mode on OpenShift", func() {
		It("Should serve TLS on port 443 and expose the graph through an OpenShift Route", func(ctx SpecContext) {
			createConfigMap(ctx)

			graphName := "igraw-odh-route"
			ig := rawGraph(graphName, nil)
			serviceKey := client.ObjectKeyFromObject(ig)
			Expect(k8sClient.Create(ctx, ig)).Should(Succeed())
			DeferCleanup(func(ctx SpecContext) {
				_ = k8sClient.Delete(ctx, ig)
			})

			actualK8sServiceCreated := &corev1.Service{}
			Eventually(func() error {
				return k8sClient.Get(ctx, serviceKey, actualK8sServiceCreated)
			}, timeout, interval).Should(Succeed())
			Expect(actualK8sServiceCreated.Spec.Ports[0].Port).To(Equal(int32(443)))
			Expect(actualK8sServiceCreated.Spec.Ports[0].TargetPort.IntVal).To(Equal(int32(8080)))
			Expect(actualK8sServiceCreated.Annotations[constants.OpenshiftServingCertAnnotation]).To(Equal(graphName + constants.ServingCertSecretSuffix))

			markDeploymentAvailable(ctx, serviceKey)

			osRoute := osv1.Route{}
			osRouteKey := types.NamespacedName{Name: graphName + "-route", Namespace: ig.GetNamespace()}
			Eventually(func() error {
				return k8sClient.Get(ctx, osRouteKey, &osRoute)
			}, timeout, interval).Should(Succeed())

			// OpenShift route hostname should be set to InferenceGraph
			routeHost := "openshift-route-example.com"
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, osRouteKey, &osRoute)).To(Succeed())
				osRoute.Status.Ingress = []osv1.RouteIngress{
					{
						Host: routeHost,
					},
				}
				g.Expect(k8sClient.Status().Update(ctx, &osRoute)).To(Succeed())
			}, timeout, interval).Should(Succeed())
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, serviceKey, ig)).Should(Succeed())
				g.Expect(ig.Status.URL).NotTo(BeNil())
				g.Expect(ig.Status.URL.Host).To(Equal(routeHost))
				g.Expect(ig.Status.URL.Scheme).To(Equal("https"))
			}, timeout, interval).Should(Succeed())
		})

		It("Should not create ingress when cluster-local visibility is configured", func(ctx SpecContext) {
			createConfigMap(ctx)

			graphName := "igraw-private"
			ig := rawGraph(graphName, map[string]string{
				constants.NetworkVisibility: constants.ClusterLocalVisibility,
			})
			serviceKey := client.ObjectKeyFromObject(ig)
			Expect(k8sClient.Create(ctx, ig)).Should(Succeed())
			DeferCleanup(func(ctx SpecContext) {
				_ = k8sClient.Delete(ctx, ig)
			})

			markDeploymentAvailable(ctx, serviceKey)

			// The InferenceGraph should have a cluster-internal hostname
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, serviceKey, ig)).Should(Succeed())
				g.Expect(ig.Status.URL).NotTo(BeNil())
				g.Expect(ig.Status.URL.Host).To(Equal(fmt.Sprintf("%s.%s.svc.cluster.local", graphName, "default")))
				g.Expect(ig.Status.URL.Scheme).To(Equal("https"))
			}, timeout, interval).Should(Succeed())

			// The OpenShift route must not be created
			osRoute := osv1.Route{}
			osRouteKey := types.NamespacedName{Name: graphName + "-route", Namespace: ig.GetNamespace()}
			Consistently(func() error {
				return k8sClient.Get(ctx, osRouteKey, &osRoute)
			}).WithTimeout(2 * time.Second).WithPolling(interval).Should(WithTransform(errors.IsNotFound, BeTrue()))
		})

		It("Should reconfigure InferenceGraph as private when cluster-local visibility is configured", func(ctx SpecContext) {
			createConfigMap(ctx)

			graphName := "igraw-exposed-to-private"
			ig := rawGraph(graphName, nil)
			serviceKey := client.ObjectKeyFromObject(ig)
			Expect(k8sClient.Create(ctx, ig)).Should(Succeed())
			DeferCleanup(func(ctx SpecContext) {
				_ = k8sClient.Delete(ctx, ig)
			})

			markDeploymentAvailable(ctx, serviceKey)

			// Wait the OpenShift route to be created
			osRoute := osv1.Route{}
			osRouteKey := types.NamespacedName{Name: graphName + "-route", Namespace: ig.GetNamespace()}
			Eventually(func() error {
				return k8sClient.Get(ctx, osRouteKey, &osRoute)
			}, timeout, interval).Should(Succeed())

			// Reconfigure as private
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, serviceKey, ig)).Should(Succeed())
				if ig.Labels == nil {
					ig.Labels = map[string]string{}
				}
				ig.Labels[constants.NetworkVisibility] = constants.ClusterLocalVisibility
				g.Expect(k8sClient.Update(ctx, ig)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			// The OpenShift route should be deleted
			Eventually(func() error {
				return k8sClient.Get(ctx, osRouteKey, &osRoute)
			}, timeout, interval).Should(WithTransform(errors.IsNotFound, BeTrue()))

			// The InferenceGraph should have a cluster-internal hostname
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, serviceKey, ig)).Should(Succeed())
				g.Expect(ig.Status.URL).NotTo(BeNil())
				g.Expect(ig.Status.URL.Host).To(Equal(fmt.Sprintf("%s.%s.svc.cluster.local", graphName, "default")))
			}, timeout, interval).Should(Succeed())
		})
	})

	Context("When creating an IG in Raw deployment mode with auth", func() {
		var inferenceGraph *v1alpha1.InferenceGraph

		BeforeEach(func(ctx SpecContext) {
			configMap := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      constants.InferenceServiceConfigMapName,
					Namespace: constants.KServeNamespace,
				},
				Data: configs,
			}
			Expect(k8sClient.Create(ctx, configMap)).NotTo(HaveOccurred())

			inferenceGraph = rawGraph("igrawauth1", nil)
			inferenceGraph.Annotations[constants.ODHKserveRawAuth] = "true"
			Expect(k8sClient.Create(ctx, inferenceGraph)).Should(Succeed())

			// Without an available Deployment the controller requeues with exponential backoff,
			// and the rate limiter delays can exceed the timeout of the deletion specs.
			markDeploymentAvailable(ctx, client.ObjectKeyFromObject(inferenceGraph))

			// The finalizer cleanup reads the InferenceService config map, so the graph has to be
			// gone before the config map is deleted. envtest runs no garbage collector, so the
			// router Deployment is deleted explicitly to keep the next spec from reading it.
			DeferCleanup(func(ctx SpecContext) {
				_ = k8sClient.Delete(ctx, inferenceGraph)
				igKey := client.ObjectKeyFromObject(inferenceGraph)
				Eventually(func() error { return k8sClient.Get(ctx, igKey, inferenceGraph) }, timeout, interval).ShouldNot(Succeed())

				deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: igKey.Name, Namespace: igKey.Namespace}}
				_ = k8sClient.Delete(ctx, deployment)
				Eventually(func() error { return k8sClient.Get(ctx, igKey, deployment) }, timeout, interval).Should(WithTransform(errors.IsNotFound, BeTrue()))

				_ = k8sClient.Delete(ctx, configMap)
				cmKey := client.ObjectKeyFromObject(configMap)
				Eventually(func() error { return k8sClient.Get(ctx, cmKey, configMap) }, timeout, interval).ShouldNot(Succeed())
			})
		})

		It("Should create or update a ClusterRoleBinding giving privileges to validate auth", func(ctx SpecContext) {
			Eventually(func(g Gomega) {
				crbKey := types.NamespacedName{Name: constants.InferenceGraphAuthCRBName}
				clusterRoleBinding := rbacv1.ClusterRoleBinding{}
				g.Expect(k8sClient.Get(ctx, crbKey, &clusterRoleBinding)).To(Succeed())

				crGVK, err := apiutil.GVKForObject(&rbacv1.ClusterRole{}, scheme.Scheme)
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(clusterRoleBinding.RoleRef).To(Equal(rbacv1.RoleRef{
					APIGroup: crGVK.Group,
					Kind:     crGVK.Kind,
					Name:     "system:auth-delegator",
				}))
				g.Expect(clusterRoleBinding.Subjects).To(ContainElement(rbacv1.Subject{
					Kind:      "ServiceAccount",
					APIGroup:  "",
					Name:      getServiceAccountNameForGraph(inferenceGraph),
					Namespace: inferenceGraph.GetNamespace(),
				}))
			}, timeout, interval).Should(Succeed())
		})

		It("Should create a ServiceAccount for querying the Kubernetes API to check tokens", func(ctx SpecContext) {
			Eventually(func(g Gomega) {
				saKey := types.NamespacedName{Namespace: inferenceGraph.GetNamespace(), Name: getServiceAccountNameForGraph(inferenceGraph)}
				serviceAccount := corev1.ServiceAccount{}
				g.Expect(k8sClient.Get(ctx, saKey, &serviceAccount)).To(Succeed())
				g.Expect(serviceAccount.OwnerReferences).ToNot(BeEmpty())
			}, timeout, interval).Should(Succeed())
		})

		It("Should configure the InferenceGraph deployment with auth enabled", func(ctx SpecContext) {
			Eventually(func(g Gomega) {
				igDeployment := appsv1.Deployment{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(inferenceGraph), &igDeployment)).To(Succeed())
				g.Expect(igDeployment.Spec.Template.Spec.AutomountServiceAccountToken).To(Equal(proto.Bool(true)))
				g.Expect(igDeployment.Spec.Template.Spec.ServiceAccountName).To(Equal(getServiceAccountNameForGraph(inferenceGraph)))
				g.Expect(igDeployment.Spec.Template.Spec.Containers).To(HaveLen(1))
				g.Expect(igDeployment.Spec.Template.Spec.Containers[0].Args).To(ContainElements("--enable-auth", "--inferencegraph-name", inferenceGraph.GetName()))
			}, timeout, interval).Should(Succeed())
		})

		It("Should delete the ServiceAccount when the InferenceGraph is deleted", func(ctx SpecContext) {
			serviceAccount := corev1.ServiceAccount{}
			saKey := types.NamespacedName{Namespace: inferenceGraph.GetNamespace(), Name: getServiceAccountNameForGraph(inferenceGraph)}

			Eventually(func() error {
				return k8sClient.Get(ctx, saKey, &serviceAccount)
			}, timeout, interval).Should(Succeed())

			Expect(k8sClient.Delete(ctx, inferenceGraph)).To(Succeed())
			Eventually(func() error {
				return k8sClient.Get(ctx, saKey, &serviceAccount)
			}, timeout, interval).Should(WithTransform(errors.IsNotFound, BeTrue()))
		})

		It("Should remove the ServiceAccount as subject of the ClusterRoleBinding when the InferenceGraph is deleted", func(ctx SpecContext) {
			crbKey := types.NamespacedName{Name: constants.InferenceGraphAuthCRBName}

			Eventually(func() []rbacv1.Subject {
				clusterRoleBinding := rbacv1.ClusterRoleBinding{}
				_ = k8sClient.Get(ctx, crbKey, &clusterRoleBinding)
				return clusterRoleBinding.Subjects
			}, timeout, interval).Should(ContainElement(HaveField("Name", getServiceAccountNameForGraph(inferenceGraph))))

			Expect(k8sClient.Delete(ctx, inferenceGraph)).To(Succeed())
			Eventually(func() []rbacv1.Subject {
				clusterRoleBinding := rbacv1.ClusterRoleBinding{}
				_ = k8sClient.Get(ctx, crbKey, &clusterRoleBinding)
				return clusterRoleBinding.Subjects
			}, timeout, interval).ShouldNot(ContainElement(HaveField("Name", getServiceAccountNameForGraph(inferenceGraph))))
		})
	})
})
