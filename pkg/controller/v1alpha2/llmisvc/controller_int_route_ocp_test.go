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

package llmisvc_test

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	routev1 "github.com/openshift/api/route/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
	. "github.com/kserve/kserve/pkg/controller/v1alpha2/llmisvc/fixture"
)

var _ = Describe("LLMInferenceService Route Discovery", func() {
	It("should advertise Route host when gateway has no external URLs", func(ctx SpecContext) {
		svcName := "test-llm-route-discovery"
		testNs := NewTestNamespace(ctx, envTest, WithIstioShadowService(svcName))

		gatewayNs := testNs.Name

		gateway := Gateway("route-test-gateway",
			InNamespace[*gwapiv1.Gateway](gatewayNs),
			WithListener(gwapiv1.HTTPProtocolType),
			WithHostnameAddresses("route-test-gateway-svc."+gatewayNs+".svc.cluster.local"),
		)
		Expect(envTest.Client.Create(ctx, gateway)).To(Succeed())
		ensureGatewayReady(ctx, envTest.Client, gateway)

		gwService := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "route-test-gateway-svc",
				Namespace: gatewayNs,
				Labels: map[string]string{
					"gateway.networking.k8s.io/gateway-name": "route-test-gateway",
				},
			},
			Spec: corev1.ServiceSpec{
				Selector: map[string]string{
					"gateway.networking.k8s.io/gateway-name": "route-test-gateway",
				},
				Ports: []corev1.ServicePort{
					{Name: "https", Port: 443, Protocol: corev1.ProtocolTCP},
				},
			},
		}
		Expect(envTest.Client.Create(ctx, gwService)).To(Succeed())

		route := &routev1.Route{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "route-test-gateway",
				Namespace: gatewayNs,
			},
			Spec: routev1.RouteSpec{
				Host: "route-test-gateway.apps.example.com",
				To: routev1.RouteTargetReference{
					Kind: "Service",
					Name: gwService.Name,
				},
				TLS: &routev1.TLSConfig{
					Termination: routev1.TLSTerminationReencrypt,
				},
			},
		}
		Expect(envTest.Client.Create(ctx, route)).To(Succeed())
		ensureRouteAdmitted(ctx, envTest.Client, route)

		defer func() {
			testNs.DeleteAndWait(ctx, route)
			testNs.DeleteAndWait(ctx, gwService)
			testNs.DeleteAndWait(ctx, gateway)
		}()

		llmSvc := LLMInferenceService(svcName,
			InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
			WithModelURI("hf://facebook/opt-125m"),
			WithManagedRoute(),
			WithGatewayRefs(LLMGatewayRef("route-test-gateway", gatewayNs)),
		)
		Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
		defer func() {
			testNs.DeleteAndWait(ctx, llmSvc)
		}()

		Eventually(func(g Gomega, ctx context.Context) {
			current := &v1alpha2.LLMInferenceService{}
			g.Expect(envTest.Client.Get(ctx, client.ObjectKeyFromObject(llmSvc), current)).To(Succeed())

			g.Expect(current.Status.Addresses).ToNot(BeEmpty(), "expected addresses in status")

			var hasRouteAddress bool
			for _, addr := range current.Status.Addresses {
				if addr.Origin != nil && string(addr.Origin.Kind) == "Route" {
					hasRouteAddress = true
					g.Expect(addr.URL.Scheme).To(Equal("https"))
					g.Expect(addr.URL.String()).To(ContainSubstring("route-test-gateway.apps.example.com"))
					g.Expect(string(addr.Origin.Group)).To(Equal(routev1.GroupName))
					g.Expect(string(addr.Origin.Name)).To(Equal("route-test-gateway"))
					g.Expect(string(*addr.Origin.Namespace)).To(Equal(gatewayNs))
				}
			}
			g.Expect(hasRouteAddress).To(BeTrue(), "expected at least one address with Route origin")
		}).WithTimeout(30 * time.Second).WithPolling(time.Second).WithContext(ctx).Should(Succeed())
	})
})

func ensureRouteAdmitted(ctx context.Context, c client.Client, route *routev1.Route) {
	if envTest.UsingExistingCluster() {
		return
	}

	created := &routev1.Route{}
	Expect(c.Get(ctx, client.ObjectKeyFromObject(route), created)).To(Succeed())

	created.Status = routev1.RouteStatus{
		Ingress: []routev1.RouteIngress{
			{
				Host:       route.Spec.Host,
				RouterName: "default",
				Conditions: []routev1.RouteIngressCondition{
					{
						Type:               routev1.RouteAdmitted,
						Status:             corev1.ConditionTrue,
						LastTransitionTime: &metav1.Time{Time: metav1.Now().Time},
					},
				},
			},
		},
	}

	Expect(c.Status().Update(ctx, created)).To(Succeed())
}
