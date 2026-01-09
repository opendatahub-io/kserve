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
	"testing"

	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"knative.dev/pkg/apis"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gatewayapi "sigs.k8s.io/gateway-api/apis/v1"

	. "github.com/kserve/kserve/pkg/controller/llmisvc/fixture"

	"github.com/kserve/kserve/pkg/controller/llmisvc"
)

// expectURLs returns an assert function that expects no error and exact URL match
func expectURLs(expected ...string) func(g Gomega, urls []string, err error) {
	return func(g Gomega, urls []string, err error) {
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(urls).To(Equal(expected))
	}
}

// expectURLsContain returns an assert function that expects no error and URLs containing the expected elements
func expectURLsContain(expected ...string) func(g Gomega, urls []string, err error) {
	return func(g Gomega, urls []string, err error) {
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(urls).To(ContainElements(expected))
	}
}

// expectError returns an assert function that expects an error matching the predicate
func expectError(check func(error) bool) func(g Gomega, urls []string, err error) {
	return func(g Gomega, urls []string, err error) {
		g.Expect(err).To(HaveOccurred())
		g.Expect(check(err)).To(BeTrue(), "Error check failed for: %v", err)
	}
}

func TestDiscoverURLs(t *testing.T) {
	tests := []struct {
		name               string
		route              *gatewayapi.HTTPRoute
		gateways           []*gatewayapi.Gateway
		services           []*corev1.Service
		preferredUrlScheme string
		assert             func(g Gomega, urls []string, err error)
	}{
		// ===== Basic address resolution =====
		{
			name: "basic external address resolution",
			route: HTTPRoute("test-route",
				InNamespace[*gatewayapi.HTTPRoute]("test-ns"),
				WithParentRef(GatewayRef("test-gateway", RefInNamespace("test-ns"))),
			),
			gateways: []*gatewayapi.Gateway{HTTPGateway("test-gateway", "test-ns", "203.0.113.1")},
			assert:   expectURLs("http://203.0.113.1/"),
		},
		{
			name: "address ordering consistency - same addresses different order",
			route: HTTPRoute("consistency-route",
				InNamespace[*gatewayapi.HTTPRoute]("test-ns"),
				WithParentRef(GatewayRef("consistency-gateway", RefInNamespace("test-ns"))),
			),
			gateways: []*gatewayapi.Gateway{
				Gateway("consistency-gateway",
					InNamespace[*gatewayapi.Gateway]("test-ns"),
					WithListener(gatewayapi.HTTPProtocolType),
					WithAddresses("203.0.113.200", "203.0.113.100"),
				),
			},
			assert: expectURLs("http://203.0.113.100/", "http://203.0.113.200/"),
		},
		// ===== Hostname handling =====
		{
			name: "route hostname within listener wildcard",
			route: HTTPRoute("hostname-route",
				InNamespace[*gatewayapi.HTTPRoute]("test-ns"),
				WithParentRef(GatewayRef("hostname-gateway", RefInNamespace("test-ns"))),
				WithHostnames("api.example.com"),
			),
			gateways: []*gatewayapi.Gateway{
				Gateway("hostname-gateway",
					InNamespace[*gatewayapi.Gateway]("test-ns"),
					WithListeners(gatewayapi.Listener{
						Protocol: gatewayapi.HTTPProtocolType,
						Port:     80,
						Hostname: ptr.To(gatewayapi.Hostname("*.example.com")),
					}),
					WithAddresses("203.0.113.1"),
				),
			},
			assert: expectURLs("http://api.example.com/"),
		},
		{
			name: "route wildcard hostname - use gateway address",
			route: HTTPRoute("wildcard-route",
				InNamespace[*gatewayapi.HTTPRoute]("test-ns"),
				WithParentRef(GatewayRef("wildcard-gateway", RefInNamespace("test-ns"))),
				WithHostnames("*"),
			),
			gateways: []*gatewayapi.Gateway{
				Gateway("wildcard-gateway",
					InNamespace[*gatewayapi.Gateway]("test-ns"),
					WithListener(gatewayapi.HTTPProtocolType),
					WithAddresses("203.0.113.100"),
				),
			},
			assert: expectURLs("http://203.0.113.100/"),
		},
		{
			name: "multiple hostnames - generates multiple URLs",
			route: HTTPRoute("multi-hostname-route",
				InNamespace[*gatewayapi.HTTPRoute]("test-ns"),
				WithParentRef(GatewayRef("multi-hostname-gateway", RefInNamespace("test-ns"))),
				WithHostnames("api.example.com", "alt.example.com"),
			),
			gateways: []*gatewayapi.Gateway{
				Gateway("multi-hostname-gateway",
					InNamespace[*gatewayapi.Gateway]("test-ns"),
					WithListeners(gatewayapi.Listener{
						Protocol: gatewayapi.HTTPProtocolType,
						Port:     80,
						Hostname: ptr.To(gatewayapi.Hostname("*.example.com")),
					}),
					WithAddresses("203.0.113.1"),
				),
			},
			assert: expectURLs("http://alt.example.com/", "http://api.example.com/"),
		},

		// ===== Path handling =====
		{
			name: "custom path extraction",
			route: HTTPRoute("path-route",
				InNamespace[*gatewayapi.HTTPRoute]("test-ns"),
				WithParentRef(GatewayRef("path-gateway", RefInNamespace("test-ns"))),
				WithPath("/api/v1"),
			),
			gateways: []*gatewayapi.Gateway{HTTPGateway("path-gateway", "test-ns", "203.0.113.1")},
			assert:   expectURLs("http://203.0.113.1/api/v1"),
		},
		{
			name: "empty route rules - default path",
			route: HTTPRoute("empty-rules-route",
				InNamespace[*gatewayapi.HTTPRoute]("test-ns"),
				WithParentRef(GatewayRef("empty-rules-gateway", RefInNamespace("test-ns"))),
			),
			gateways: []*gatewayapi.Gateway{HTTPGateway("empty-rules-gateway", "test-ns", "203.0.113.1")},
			assert:   expectURLs("http://203.0.113.1/"),
		},

		// ===== Scheme from listener =====
		{
			name: "HTTPS scheme from gateway listener",
			route: HTTPRoute("https-route",
				InNamespace[*gatewayapi.HTTPRoute]("test-ns"),
				WithParentRef(GatewayRef("https-gateway", RefInNamespace("test-ns"))),
			),
			gateways: []*gatewayapi.Gateway{HTTPSGateway("https-gateway", "test-ns", "203.0.113.1")},
			assert:   expectURLs("https://203.0.113.1/"),
		},

		// ===== Multiple parent refs =====
		{
			name: "multiple parent refs - sorted selection",
			route: HTTPRoute("multi-parent-route",
				InNamespace[*gatewayapi.HTTPRoute]("default-ns"),
				WithParentRefs(
					GatewayRef("z-gateway", RefInNamespace("z-namespace")),
					GatewayRef("a-gateway", RefInNamespace("a-namespace")),
					GatewayRef("b-gateway", RefInNamespace("a-namespace")),
				),
			),
			gateways: []*gatewayapi.Gateway{
				Gateway("a-gateway",
					InNamespace[*gatewayapi.Gateway]("a-namespace"),
					WithListener(gatewayapi.HTTPProtocolType),
					WithAddresses("203.0.113.1"),
				),
				Gateway("z-gateway",
					InNamespace[*gatewayapi.Gateway]("z-namespace"),
					WithListener(gatewayapi.HTTPProtocolType),
					WithAddresses("203.0.113.2"),
				),
				Gateway("b-gateway",
					InNamespace[*gatewayapi.Gateway]("a-namespace"),
					WithListener(gatewayapi.HTTPProtocolType),
					WithAddresses("203.0.113.3"),
				),
			},
			assert: expectURLs(
				"http://203.0.113.2/",
				"http://203.0.113.1/",
				"http://203.0.113.3/",
			),
		},
		{
			name: "route with multiple parent refs to different gateways",
			route: HTTPRoute("multi-gateway-route",
				InNamespace[*gatewayapi.HTTPRoute]("test-ns"),
				WithParentRefs(
					gatewayapi.ParentReference{
						Name:      "gateway-a",
						Namespace: ptr.To(gatewayapi.Namespace("gw-ns")),
						Group:     ptr.To(gatewayapi.Group("gateway.networking.k8s.io")),
						Kind:      ptr.To(gatewayapi.Kind("Gateway")),
					},
					gatewayapi.ParentReference{
						Name:      "gateway-b",
						Namespace: ptr.To(gatewayapi.Namespace("gw-ns")),
						Group:     ptr.To(gatewayapi.Group("gateway.networking.k8s.io")),
						Kind:      ptr.To(gatewayapi.Kind("Gateway")),
					},
				),
			),
			gateways: []*gatewayapi.Gateway{
				Gateway("gateway-a",
					InNamespace[*gatewayapi.Gateway]("gw-ns"),
					WithListeners(gatewayapi.Listener{
						Name:     "https",
						Protocol: gatewayapi.HTTPSProtocolType,
						Port:     443,
					}),
					WithAddresses("gateway-a.example.com"),
				),
				Gateway("gateway-b",
					InNamespace[*gatewayapi.Gateway]("gw-ns"),
					WithListeners(gatewayapi.Listener{
						Name:     "http",
						Protocol: gatewayapi.HTTPProtocolType,
						Port:     8080,
					}),
					WithAddresses("gateway-b.example.com"),
				),
			},
			assert: expectURLsContain(
				"https://gateway-a.example.com/",
				"http://gateway-b.example.com:8080/",
			),
		},
		{
			name: "parent ref without namespace - use route namespace",
			route: HTTPRoute("no-ns-route",
				InNamespace[*gatewayapi.HTTPRoute]("route-ns"),
				WithParentRef(GatewayRef("same-ns-gateway")),
			),
			gateways: []*gatewayapi.Gateway{HTTPGateway("same-ns-gateway", "route-ns", "203.0.113.1")},
			assert:   expectURLs("http://203.0.113.1/"),
		},

		// ===== Address types =====
		{
			name: "private addresses only - still returns URLs",
			route: HTTPRoute("private-route",
				InNamespace[*gatewayapi.HTTPRoute]("test-ns"),
				WithParentRef(GatewayRef("private-gateway", RefInNamespace("test-ns"))),
			),
			gateways: []*gatewayapi.Gateway{
				Gateway("private-gateway",
					InNamespace[*gatewayapi.Gateway]("test-ns"),
					WithListener(gatewayapi.HTTPProtocolType),
					WithAddresses("192.168.1.100", "10.0.0.50"),
				),
			},
			assert: expectURLs("http://10.0.0.50/", "http://192.168.1.100/"),
		},
		{
			name: "hostname addresses - basic resolution",
			route: HTTPRoute("hostname-addr-route",
				InNamespace[*gatewayapi.HTTPRoute]("test-ns"),
				WithParentRef(GatewayRef("hostname-addr-gateway", RefInNamespace("test-ns"))),
			),
			gateways: []*gatewayapi.Gateway{
				Gateway("hostname-addr-gateway",
					InNamespace[*gatewayapi.Gateway]("test-ns"),
					WithListener(gatewayapi.HTTPProtocolType),
					WithHostnameAddresses("api.example.com", "lb.example.com"),
				),
			},
			assert: expectURLs("http://api.example.com/", "http://lb.example.com/"),
		},
		{
			name: "mixed hostname and IP addresses - deterministic selection",
			route: HTTPRoute("mixed-addr-route",
				InNamespace[*gatewayapi.HTTPRoute]("test-ns"),
				WithParentRef(GatewayRef("mixed-addr-gateway", RefInNamespace("test-ns"))),
			),
			gateways: []*gatewayapi.Gateway{
				Gateway("mixed-addr-gateway",
					InNamespace[*gatewayapi.Gateway]("test-ns"),
					WithListener(gatewayapi.HTTPProtocolType),
					WithMixedAddresses(
						IPAddress("203.0.113.1"),
						HostnameAddress("api.example.com"),
						HostnameAddress("lb.example.com"),
					),
				),
			},
			assert: expectURLs(
				"http://203.0.113.1/",
				"http://api.example.com/",
				"http://lb.example.com/",
			),
		},
		// ===== Error cases =====
		{
			name: "gateway not found should cause not found error",
			route: HTTPRoute("nonexistent-route",
				InNamespace[*gatewayapi.HTTPRoute]("test-ns"),
				WithParentRef(GatewayRef("nonexistent-gateway", RefInNamespace("test-ns"))),
			),
			gateways: nil,
			assert:   expectError(apierrors.IsNotFound),
		},
		{
			name: "no addresses at all - NoURLsDiscoveredError",
			route: HTTPRoute("no-addr-route",
				InNamespace[*gatewayapi.HTTPRoute]("test-ns"),
				WithParentRef(GatewayRef("no-addr-gateway", RefInNamespace("test-ns"))),
			),
			gateways: []*gatewayapi.Gateway{
				Gateway("no-addr-gateway",
					InNamespace[*gatewayapi.Gateway]("test-ns"),
					WithListener(gatewayapi.HTTPProtocolType),
				),
			},
			assert: expectError(llmisvc.IsNoURLsDiscovered),
		},
		{
			name: "gateway with only TCP listeners returns error",
			route: HTTPRoute("test-route",
				InNamespace[*gatewayapi.HTTPRoute]("test-ns"),
				WithParentRef(GatewayRef("test-gateway", RefInNamespace("test-ns"))),
			),
			gateways: []*gatewayapi.Gateway{
				Gateway("test-gateway",
					InNamespace[*gatewayapi.Gateway]("test-ns"),
					WithListeners(gatewayapi.Listener{
						Name:     "tcp-listener",
						Protocol: gatewayapi.TCPProtocolType,
						Port:     9000,
					}),
					WithAddresses("203.0.113.1"),
				),
			},
			assert: expectError(func(err error) bool { return err != nil }),
		},

		// ===== Port handling =====
		{
			name: "custom port handling - non-standard HTTP port",
			route: HTTPRoute("custom-port-route",
				InNamespace[*gatewayapi.HTTPRoute]("test-ns"),
				WithParentRef(GatewayRef("custom-port-gateway", RefInNamespace("test-ns"))),
			),
			gateways: []*gatewayapi.Gateway{
				Gateway("custom-port-gateway",
					InNamespace[*gatewayapi.Gateway]("test-ns"),
					WithListeners(gatewayapi.Listener{
						Protocol: gatewayapi.HTTPProtocolType,
						Port:     8080,
					}),
					WithAddresses("203.0.113.1"),
				),
			},
			assert: expectURLs("http://203.0.113.1:8080/"),
		},
		{
			name: "custom port handling - non-standard HTTPS port",
			route: HTTPRoute("custom-https-port-route",
				InNamespace[*gatewayapi.HTTPRoute]("test-ns"),
				WithParentRef(GatewayRef("custom-https-port-gateway", RefInNamespace("test-ns"))),
				WithHostnames("secure.example.com"),
			),
			gateways: []*gatewayapi.Gateway{
				Gateway("custom-https-port-gateway",
					InNamespace[*gatewayapi.Gateway]("test-ns"),
					WithListeners(gatewayapi.Listener{
						Protocol: gatewayapi.HTTPSProtocolType,
						Port:     8443,
					}),
					WithAddresses("203.0.113.1"),
				),
			},
			assert: expectURLs("https://secure.example.com:8443/"),
		},
		{
			name: "standard ports omitted - HTTP port 80",
			route: HTTPRoute("standard-http-route",
				InNamespace[*gatewayapi.HTTPRoute]("test-ns"),
				WithParentRef(GatewayRef("standard-http-gateway", RefInNamespace("test-ns"))),
			),
			gateways: []*gatewayapi.Gateway{
				Gateway("standard-http-gateway",
					InNamespace[*gatewayapi.Gateway]("test-ns"),
					WithListeners(gatewayapi.Listener{
						Protocol: gatewayapi.HTTPProtocolType,
						Port:     80,
					}),
					WithAddresses("203.0.113.1"),
				),
			},
			assert: expectURLs("http://203.0.113.1/"),
		},
		{
			name: "standard ports omitted - HTTPS port 443",
			route: HTTPRoute("standard-https-route",
				InNamespace[*gatewayapi.HTTPRoute]("test-ns"),
				WithParentRef(GatewayRef("standard-https-gateway", RefInNamespace("test-ns"))),
			),
			gateways: []*gatewayapi.Gateway{
				Gateway("standard-https-gateway",
					InNamespace[*gatewayapi.Gateway]("test-ns"),
					WithListeners(gatewayapi.Listener{
						Protocol: gatewayapi.HTTPSProtocolType,
						Port:     443,
					}),
					WithAddresses("203.0.113.1"),
				),
			},
			assert: expectURLs("https://203.0.113.1/"),
		},

		// ===== sectionName listener selection =====
		{
			name: "sectionName isolates to specific listener - no leakage from other listeners",
			route: HTTPRoute("test-route",
				InNamespace[*gatewayapi.HTTPRoute]("test-ns"),
				WithParentRef(gatewayapi.ParentReference{
					Name:        "multi-listener-gateway",
					Namespace:   ptr.To(gatewayapi.Namespace("gw-ns")),
					SectionName: ptr.To(gatewayapi.SectionName("http")),
					Group:       ptr.To(gatewayapi.Group("gateway.networking.k8s.io")),
					Kind:        ptr.To(gatewayapi.Kind("Gateway")),
				}),
			),
			gateways: []*gatewayapi.Gateway{
				Gateway("multi-listener-gateway",
					InNamespace[*gatewayapi.Gateway]("gw-ns"),
					WithListeners(
						gatewayapi.Listener{
							Name:     "http",
							Protocol: gatewayapi.HTTPProtocolType,
							Port:     80,
							Hostname: ptr.To(gatewayapi.Hostname("http.example.com")),
						},
						gatewayapi.Listener{
							Name:     "https",
							Protocol: gatewayapi.HTTPSProtocolType,
							Port:     443,
							Hostname: ptr.To(gatewayapi.Hostname("https.example.com")),
						},
						gatewayapi.Listener{
							Name:     "internal",
							Protocol: gatewayapi.HTTPProtocolType,
							Port:     8080,
							Hostname: ptr.To(gatewayapi.Hostname("internal.example.com")),
						},
					),
					WithAddresses("203.0.113.1"),
				),
			},
			assert: func(g Gomega, urls []string, err error) {
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(urls).To(Equal([]string{"http://http.example.com/"}))
				g.Expect(urls).ToNot(ContainElement(ContainSubstring("https.example.com")))
				g.Expect(urls).ToNot(ContainElement(ContainSubstring("internal.example.com")))
			},
		},

		// ===== Comprehensive URL generation =====
		{
			name: "multiple hostnames and addresses - comprehensive URL generation",
			route: HTTPRoute("comprehensive-route",
				InNamespace[*gatewayapi.HTTPRoute]("test-ns"),
				WithParentRef(GatewayRef("comprehensive-gateway", RefInNamespace("test-ns"))),
				WithHostnames("api.example.com", "v2.example.com"),
			),
			gateways: []*gatewayapi.Gateway{
				Gateway("comprehensive-gateway",
					InNamespace[*gatewayapi.Gateway]("test-ns"),
					WithListener(gatewayapi.HTTPProtocolType),
					WithAddresses("203.0.113.1", "203.0.113.2"),
				),
			},
			assert: expectURLs("http://api.example.com/", "http://v2.example.com/"),
		},

		// ===== Listener hostname fallback =====
		{
			name: "listener hostname fallback - no route hostnames",
			route: HTTPRoute("listener-hostname-route",
				InNamespace[*gatewayapi.HTTPRoute]("test-ns"),
				WithParentRef(GatewayRef("listener-hostname-gateway", RefInNamespace("test-ns"))),
			),
			gateways: []*gatewayapi.Gateway{
				Gateway("listener-hostname-gateway",
					InNamespace[*gatewayapi.Gateway]("test-ns"),
					WithListeners(gatewayapi.Listener{
						Protocol: gatewayapi.HTTPProtocolType,
						Port:     80,
						Hostname: ptr.To(gatewayapi.Hostname("listener.example.com")),
					}),
					WithAddresses("203.0.113.1"),
				),
			},
			assert: expectURLs("http://listener.example.com/"),
		},
		{
			name: "listener hostname fallback - route has wildcard hostname",
			route: HTTPRoute("wildcard-listener-route",
				InNamespace[*gatewayapi.HTTPRoute]("test-ns"),
				WithParentRef(GatewayRef("wildcard-listener-gateway", RefInNamespace("test-ns"))),
				WithHostnames("*"),
			),
			gateways: []*gatewayapi.Gateway{
				Gateway("wildcard-listener-gateway",
					InNamespace[*gatewayapi.Gateway]("test-ns"),
					WithListeners(gatewayapi.Listener{
						Protocol: gatewayapi.HTTPProtocolType,
						Port:     80,
						Hostname: ptr.To(gatewayapi.Hostname("listener.example.com")),
					}),
					WithAddresses("203.0.113.1"),
				),
			},
			assert: expectURLs("http://listener.example.com/"),
		},
		{
			name: "listener hostname fallback - route hostname takes precedence",
			route: HTTPRoute("precedence-route",
				InNamespace[*gatewayapi.HTTPRoute]("test-ns"),
				WithParentRef(GatewayRef("precedence-gateway", RefInNamespace("test-ns"))),
				WithHostnames("route.example.com"),
			),
			gateways: []*gatewayapi.Gateway{
				Gateway("precedence-gateway",
					InNamespace[*gatewayapi.Gateway]("test-ns"),
					WithListeners(gatewayapi.Listener{
						Protocol: gatewayapi.HTTPProtocolType,
						Port:     80,
						Hostname: ptr.To(gatewayapi.Hostname("listener.example.com")),
					}),
					WithAddresses("203.0.113.1"),
				),
			},
			assert: expectURLs("http://route.example.com/"),
		},
		{
			name: "listener hostname fallback - empty listener hostname uses addresses",
			route: HTTPRoute("empty-listener-route",
				InNamespace[*gatewayapi.HTTPRoute]("test-ns"),
				WithParentRef(GatewayRef("empty-listener-gateway", RefInNamespace("test-ns"))),
			),
			gateways: []*gatewayapi.Gateway{
				Gateway("empty-listener-gateway",
					InNamespace[*gatewayapi.Gateway]("test-ns"),
					WithListeners(gatewayapi.Listener{
						Protocol: gatewayapi.HTTPProtocolType,
						Port:     80,
					}),
					WithAddresses("203.0.113.1"),
				),
			},
			assert: expectURLs("http://203.0.113.1/"),
		},

		// ===== Listener wildcard hostname =====
		{
			name: "listener wildcard hostname - basic wildcard expansion",
			route: HTTPRoute("wildcard-expansion-route",
				InNamespace[*gatewayapi.HTTPRoute]("test-ns"),
				WithParentRef(GatewayRef("wildcard-expansion-gateway", RefInNamespace("test-ns"))),
			),
			gateways: []*gatewayapi.Gateway{
				Gateway("wildcard-expansion-gateway",
					InNamespace[*gatewayapi.Gateway]("test-ns"),
					WithListeners(gatewayapi.Listener{
						Protocol: gatewayapi.HTTPProtocolType,
						Port:     80,
						Hostname: ptr.To(gatewayapi.Hostname("*.example.com")),
					}),
					WithAddresses("203.0.113.1"),
				),
			},
			// Wildcards are expanded to inference.example.com
			assert: expectURLs("http://inference.example.com/"),
		},
		{
			name: "listener wildcard hostname - wildcard with subdomain",
			route: HTTPRoute("subdomain-wildcard-route",
				InNamespace[*gatewayapi.HTTPRoute]("test-ns"),
				WithParentRef(GatewayRef("subdomain-wildcard-gateway", RefInNamespace("test-ns"))),
			),
			gateways: []*gatewayapi.Gateway{
				Gateway("subdomain-wildcard-gateway",
					InNamespace[*gatewayapi.Gateway]("test-ns"),
					WithListeners(gatewayapi.Listener{
						Protocol: gatewayapi.HTTPProtocolType,
						Port:     80,
						Hostname: ptr.To(gatewayapi.Hostname("*.api.example.com")),
					}),
					WithAddresses("203.0.113.1"),
				),
			},
			// Wildcards are expanded to inference.api.example.com
			assert: expectURLs("http://inference.api.example.com/"),
		},
		{
			name: "listener wildcard hostname - route hostname takes precedence over wildcard",
			route: HTTPRoute("wildcard-precedence-route",
				InNamespace[*gatewayapi.HTTPRoute]("test-ns"),
				WithParentRef(GatewayRef("wildcard-precedence-gateway", RefInNamespace("test-ns"))),
				WithHostnames("specific.example.com"),
			),
			gateways: []*gatewayapi.Gateway{
				Gateway("wildcard-precedence-gateway",
					InNamespace[*gatewayapi.Gateway]("test-ns"),
					WithListeners(gatewayapi.Listener{
						Protocol: gatewayapi.HTTPProtocolType,
						Port:     80,
						Hostname: ptr.To(gatewayapi.Hostname("*.example.com")),
					}),
					WithAddresses("203.0.113.1"),
				),
			},
			assert: expectURLs("http://specific.example.com/"),
		},
		{
			name: "listener wildcard hostname - HTTPS with wildcard",
			route: HTTPRoute("https-wildcard-route",
				InNamespace[*gatewayapi.HTTPRoute]("test-ns"),
				WithParentRef(GatewayRef("https-wildcard-gateway", RefInNamespace("test-ns"))),
			),
			gateways: []*gatewayapi.Gateway{
				Gateway("https-wildcard-gateway",
					InNamespace[*gatewayapi.Gateway]("test-ns"),
					WithListeners(gatewayapi.Listener{
						Protocol: gatewayapi.HTTPSProtocolType,
						Port:     443,
						Hostname: ptr.To(gatewayapi.Hostname("*.secure.example.com")),
					}),
					WithAddresses("203.0.113.1"),
				),
			},
			// HTTPS with wildcard expanded to inference.secure.example.com
			assert: expectURLs("https://inference.secure.example.com/"),
		},
		{
			name: "listener wildcard hostname - custom port with wildcard",
			route: HTTPRoute("custom-port-wildcard-route",
				InNamespace[*gatewayapi.HTTPRoute]("test-ns"),
				WithParentRef(GatewayRef("custom-port-wildcard-gateway", RefInNamespace("test-ns"))),
			),
			gateways: []*gatewayapi.Gateway{
				Gateway("custom-port-wildcard-gateway",
					InNamespace[*gatewayapi.Gateway]("test-ns"),
					WithListeners(gatewayapi.Listener{
						Protocol: gatewayapi.HTTPProtocolType,
						Port:     8080,
						Hostname: ptr.To(gatewayapi.Hostname("*.example.com")),
					}),
					WithAddresses("203.0.113.1"),
				),
			},
			// Custom port with wildcard expanded
			assert: expectURLs("http://inference.example.com:8080/"),
		},

		// ===== preferredUrlScheme =====
		{
			name: "preferredUrlScheme=https prioritizes HTTPS listener",
			route: HTTPRoute("test-route",
				InNamespace[*gatewayapi.HTTPRoute]("test-ns"),
				WithParentRef(GatewayRef("test-gateway", RefInNamespace("test-ns"))),
			),
			gateways: []*gatewayapi.Gateway{
				Gateway("test-gateway",
					InNamespace[*gatewayapi.Gateway]("test-ns"),
					WithListeners(
						gatewayapi.Listener{
							Name:     "http-listener",
							Protocol: gatewayapi.HTTPProtocolType,
							Port:     80,
						},
						gatewayapi.Listener{
							Name:     "https-listener",
							Protocol: gatewayapi.HTTPSProtocolType,
							Port:     443,
						},
					),
					WithAddresses("203.0.113.1"),
				),
			},
			preferredUrlScheme: "https",
			assert:             expectURLs("https://203.0.113.1/", "http://203.0.113.1/"),
		},
		{
			name: "preferredUrlScheme=http prioritizes HTTP listener",
			route: HTTPRoute("test-route",
				InNamespace[*gatewayapi.HTTPRoute]("test-ns"),
				WithParentRef(GatewayRef("test-gateway", RefInNamespace("test-ns"))),
			),
			gateways: []*gatewayapi.Gateway{
				Gateway("test-gateway",
					InNamespace[*gatewayapi.Gateway]("test-ns"),
					WithListeners(
						gatewayapi.Listener{
							Name:     "http-listener",
							Protocol: gatewayapi.HTTPProtocolType,
							Port:     80,
						},
						gatewayapi.Listener{
							Name:     "https-listener",
							Protocol: gatewayapi.HTTPSProtocolType,
							Port:     443,
						},
					),
					WithAddresses("203.0.113.1"),
				),
			},
			preferredUrlScheme: "http",
			assert:             expectURLs("http://203.0.113.1/", "https://203.0.113.1/"),
		},
		{
			name: "preferredUrlScheme mismatch falls back to available listener",
			route: HTTPRoute("test-route",
				InNamespace[*gatewayapi.HTTPRoute]("test-ns"),
				WithParentRef(GatewayRef("test-gateway", RefInNamespace("test-ns"))),
			),
			gateways: []*gatewayapi.Gateway{
				Gateway("test-gateway",
					InNamespace[*gatewayapi.Gateway]("test-ns"),
					WithListeners(gatewayapi.Listener{
						Name:     "http-listener",
						Protocol: gatewayapi.HTTPProtocolType,
						Port:     80,
					}),
					WithAddresses("203.0.113.1"),
				),
			},
			preferredUrlScheme: "https",
			assert:             expectURLs("http://203.0.113.1/"),
		},
		{
			name: "preferredUrlScheme matching listener preserves non-standard port",
			route: HTTPRoute("test-route",
				InNamespace[*gatewayapi.HTTPRoute]("test-ns"),
				WithParentRef(GatewayRef("test-gateway", RefInNamespace("test-ns"))),
			),
			gateways: []*gatewayapi.Gateway{
				Gateway("test-gateway",
					InNamespace[*gatewayapi.Gateway]("test-ns"),
					WithListeners(gatewayapi.Listener{
						Name:     "https-listener",
						Protocol: gatewayapi.HTTPSProtocolType,
						Port:     8443,
					}),
					WithAddresses("203.0.113.1"),
				),
			},
			preferredUrlScheme: "https",
			assert:             expectURLs("https://203.0.113.1:8443/"),
		},
		{
			name: "empty preferredUrlScheme returns ALL listeners (HTTPS first)",
			route: HTTPRoute("test-route",
				InNamespace[*gatewayapi.HTTPRoute]("test-ns"),
				WithParentRef(GatewayRef("test-gateway", RefInNamespace("test-ns"))),
			),
			gateways: []*gatewayapi.Gateway{
				Gateway("test-gateway",
					InNamespace[*gatewayapi.Gateway]("test-ns"),
					WithListeners(
						gatewayapi.Listener{
							Name:     "http-listener",
							Protocol: gatewayapi.HTTPProtocolType,
							Port:     80,
						},
						gatewayapi.Listener{
							Name:     "https-listener",
							Protocol: gatewayapi.HTTPSProtocolType,
							Port:     443,
						},
					),
					WithAddresses("203.0.113.1"),
				),
			},
			preferredUrlScheme: "",
			assert:             expectURLs("https://203.0.113.1/", "http://203.0.113.1/"),
		},
		{
			name: "empty preferredUrlScheme falls back to HTTP if no HTTPS",
			route: HTTPRoute("test-route",
				InNamespace[*gatewayapi.HTTPRoute]("test-ns"),
				WithParentRef(GatewayRef("test-gateway", RefInNamespace("test-ns"))),
			),
			gateways: []*gatewayapi.Gateway{
				Gateway("test-gateway",
					InNamespace[*gatewayapi.Gateway]("test-ns"),
					WithListeners(gatewayapi.Listener{
						Name:     "http-listener",
						Protocol: gatewayapi.HTTPProtocolType,
						Port:     8080,
					}),
					WithAddresses("203.0.113.1"),
				),
			},
			preferredUrlScheme: "",
			assert:             expectURLs("http://203.0.113.1:8080/"),
		},
		{
			name: "sectionName takes precedence over preferredUrlScheme",
			route: HTTPRoute("test-route",
				InNamespace[*gatewayapi.HTTPRoute]("test-ns"),
				WithParentRef(gatewayapi.ParentReference{
					Name:        "test-gateway",
					Namespace:   ptr.To(gatewayapi.Namespace("test-ns")),
					SectionName: ptr.To(gatewayapi.SectionName("http-listener")),
					Group:       ptr.To(gatewayapi.Group("gateway.networking.k8s.io")),
					Kind:        ptr.To(gatewayapi.Kind("Gateway")),
				}),
			),
			gateways: []*gatewayapi.Gateway{
				Gateway("test-gateway",
					InNamespace[*gatewayapi.Gateway]("test-ns"),
					WithListeners(
						gatewayapi.Listener{
							Name:     "http-listener",
							Protocol: gatewayapi.HTTPProtocolType,
							Port:     80,
						},
						gatewayapi.Listener{
							Name:     "https-listener",
							Protocol: gatewayapi.HTTPSProtocolType,
							Port:     443,
						},
					),
					WithAddresses("203.0.113.1"),
				),
			},
			preferredUrlScheme: "https", // ignored because sectionName is set
			assert:             expectURLs("http://203.0.113.1/"),
		},
		{
			name: "gateway with multiple HTTP-capable listeners - all listeners advertised",
			route: HTTPRoute("test-route",
				InNamespace[*gatewayapi.HTTPRoute]("test-ns"),
				WithParentRef(GatewayRef("multi-listener-gateway", RefInNamespace("gw-ns"))),
			),
			gateways: []*gatewayapi.Gateway{
				Gateway("multi-listener-gateway",
					InNamespace[*gatewayapi.Gateway]("gw-ns"),
					WithListeners(
						gatewayapi.Listener{
							Name:     "http",
							Protocol: gatewayapi.HTTPProtocolType,
							Port:     80,
							Hostname: ptr.To(gatewayapi.Hostname("api.example.com")),
						},
						gatewayapi.Listener{
							Name:     "https",
							Protocol: gatewayapi.HTTPSProtocolType,
							Port:     443,
							Hostname: ptr.To(gatewayapi.Hostname("api.example.com")),
						},
					),
					WithAddresses("203.0.113.1"),
				),
			},
			assert: expectURLs("https://api.example.com/", "http://api.example.com/"),
		},

		// ===== Internal service discovery =====
		{
			name: "discovers internal URL via gateway label",
			route: HTTPRoute("test-route",
				InNamespace[*gatewayapi.HTTPRoute]("test-ns"),
				WithParentRef(GatewayRef("my-gateway", RefInNamespace("gateway-ns"))),
			),
			gateways: []*gatewayapi.Gateway{
				Gateway("my-gateway",
					InNamespace[*gatewayapi.Gateway]("gateway-ns"),
					WithListeners(gatewayapi.Listener{
						Name:     "http",
						Protocol: gatewayapi.HTTPProtocolType,
						Port:     80,
					}),
					WithAddresses("203.0.113.1"),
				),
			},
			services: []*corev1.Service{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "gateway-service",
						Namespace: "gateway-ns",
						Labels: map[string]string{
							"gateway.networking.k8s.io/gateway-name": "my-gateway",
						},
					},
				},
			},
			assert: expectURLs(
				"http://203.0.113.1/",
				"http://gateway-service.gateway-ns.svc.cluster.local/",
			),
		},
		{
			name: "discovers internal URL via same-name service fallback",
			route: HTTPRoute("test-route",
				InNamespace[*gatewayapi.HTTPRoute]("test-ns"),
				WithParentRef(GatewayRef("my-gateway", RefInNamespace("gateway-ns"))),
			),
			gateways: []*gatewayapi.Gateway{
				Gateway("my-gateway",
					InNamespace[*gatewayapi.Gateway]("gateway-ns"),
					WithListeners(gatewayapi.Listener{
						Name:     "http",
						Protocol: gatewayapi.HTTPProtocolType,
						Port:     80,
					}),
					WithAddresses("203.0.113.1"),
				),
			},
			services: []*corev1.Service{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "my-gateway", // Same name as gateway
						Namespace: "gateway-ns",
					},
				},
			},
			assert: expectURLs(
				"http://203.0.113.1/",
				"http://my-gateway.gateway-ns.svc.cluster.local/",
			),
		},
		{
			name: "internal URL includes non-standard port",
			route: HTTPRoute("test-route",
				InNamespace[*gatewayapi.HTTPRoute]("test-ns"),
				WithParentRef(GatewayRef("my-gateway", RefInNamespace("gateway-ns"))),
			),
			gateways: []*gatewayapi.Gateway{
				Gateway("my-gateway",
					InNamespace[*gatewayapi.Gateway]("gateway-ns"),
					WithListeners(gatewayapi.Listener{
						Name:     "http-alt",
						Protocol: gatewayapi.HTTPProtocolType,
						Port:     8080,
					}),
					WithAddresses("203.0.113.1"),
				),
			},
			services: []*corev1.Service{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "gateway-service",
						Namespace: "gateway-ns",
						Labels: map[string]string{
							"gateway.networking.k8s.io/gateway-name": "my-gateway",
						},
					},
				},
			},
			assert: expectURLs(
				"http://203.0.113.1:8080/",
				"http://gateway-service.gateway-ns.svc.cluster.local:8080/",
			),
		},
		{
			name: "internal URL uses same scheme as gateway listener",
			route: HTTPRoute("test-route",
				InNamespace[*gatewayapi.HTTPRoute]("test-ns"),
				WithParentRef(GatewayRef("my-gateway", RefInNamespace("gateway-ns"))),
			),
			gateways: []*gatewayapi.Gateway{
				Gateway("my-gateway",
					InNamespace[*gatewayapi.Gateway]("gateway-ns"),
					WithListeners(gatewayapi.Listener{
						Name:     "https",
						Protocol: gatewayapi.HTTPSProtocolType,
						Port:     443,
					}),
					WithAddresses("203.0.113.1"),
				),
			},
			services: []*corev1.Service{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "gateway-service",
						Namespace: "gateway-ns",
						Labels: map[string]string{
							"gateway.networking.k8s.io/gateway-name": "my-gateway",
						},
					},
				},
			},
			// Internal URL matches the listener's scheme and port
			assert: expectURLs(
				"https://203.0.113.1/",
				"https://gateway-service.gateway-ns.svc.cluster.local/",
			),
		},
		{
			name: "no internal URL without backing service",
			route: HTTPRoute("test-route",
				InNamespace[*gatewayapi.HTTPRoute]("test-ns"),
				WithParentRef(GatewayRef("my-gateway", RefInNamespace("gateway-ns"))),
			),
			gateways: []*gatewayapi.Gateway{
				Gateway("my-gateway",
					InNamespace[*gatewayapi.Gateway]("gateway-ns"),
					WithListeners(gatewayapi.Listener{
						Name:     "http",
						Protocol: gatewayapi.HTTPProtocolType,
						Port:     80,
					}),
					WithAddresses("203.0.113.1"),
				),
			},
			services: nil,
			assert:   expectURLs("http://203.0.113.1/"),
		},
		{
			name: "no external addresses but backing service exists - returns internal URL only",
			route: HTTPRoute("test-route",
				InNamespace[*gatewayapi.HTTPRoute]("test-ns"),
				WithParentRef(GatewayRef("my-gateway", RefInNamespace("gateway-ns"))),
			),
			gateways: []*gatewayapi.Gateway{
				Gateway("my-gateway",
					InNamespace[*gatewayapi.Gateway]("gateway-ns"),
					WithListeners(gatewayapi.Listener{
						Name:     "http",
						Protocol: gatewayapi.HTTPProtocolType,
						Port:     8080,
					}),
					// No addresses - LoadBalancer pending
				),
			},
			services: []*corev1.Service{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "gateway-service",
						Namespace: "gateway-ns",
						Labels: map[string]string{
							"gateway.networking.k8s.io/gateway-name": "my-gateway",
						},
					},
				},
			},
			assert: expectURLs("http://gateway-service.gateway-ns.svc.cluster.local:8080/"),
		},
		{
			name: "multiple gateways with backing services - each gets internal URL",
			route: HTTPRoute("test-route",
				InNamespace[*gatewayapi.HTTPRoute]("test-ns"),
				WithParentRefs(
					gatewayapi.ParentReference{
						Name:      "gateway-a",
						Namespace: ptr.To(gatewayapi.Namespace("gw-ns")),
						Group:     ptr.To(gatewayapi.Group("gateway.networking.k8s.io")),
						Kind:      ptr.To(gatewayapi.Kind("Gateway")),
					},
					gatewayapi.ParentReference{
						Name:      "gateway-b",
						Namespace: ptr.To(gatewayapi.Namespace("gw-ns")),
						Group:     ptr.To(gatewayapi.Group("gateway.networking.k8s.io")),
						Kind:      ptr.To(gatewayapi.Kind("Gateway")),
					},
				),
			),
			gateways: []*gatewayapi.Gateway{
				Gateway("gateway-a",
					InNamespace[*gatewayapi.Gateway]("gw-ns"),
					WithListeners(gatewayapi.Listener{
						Name:     "https",
						Protocol: gatewayapi.HTTPSProtocolType,
						Port:     443,
					}),
					WithAddresses("gw-a.example.com"),
				),
				Gateway("gateway-b",
					InNamespace[*gatewayapi.Gateway]("gw-ns"),
					WithListeners(gatewayapi.Listener{
						Name:     "http",
						Protocol: gatewayapi.HTTPProtocolType,
						Port:     80,
					}),
					WithAddresses("gw-b.example.com"),
				),
			},
			services: []*corev1.Service{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "gateway-a",
						Namespace: "gw-ns",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "gateway-b-svc",
						Namespace: "gw-ns",
						Labels: map[string]string{
							"gateway.networking.k8s.io/gateway-name": "gateway-b",
						},
					},
				},
			},
			assert: expectURLsContain(
				"https://gw-a.example.com/",
				"https://gateway-a.gw-ns.svc.cluster.local/",
				"http://gw-b.example.com/",
				"http://gateway-b-svc.gw-ns.svc.cluster.local/",
			),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)
			ctx := t.Context()

			scheme := runtime.NewScheme()
			g.Expect(gatewayapi.Install(scheme)).To(Succeed())
			g.Expect(corev1.AddToScheme(scheme)).To(Succeed())

			var objects []client.Object
			if tt.route != nil {
				objects = append(objects, tt.route)
			}
			for _, gw := range tt.gateways {
				objects = append(objects, gw)
			}
			for _, svc := range tt.services {
				objects = append(objects, svc)
			}
			objects = append(objects, DefaultGatewayClass())

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(objects...).
				Build()

			urls, err := llmisvc.DiscoverURLs(ctx, fakeClient, tt.route, tt.preferredUrlScheme)

			var actualURLs []string
			for _, url := range urls {
				actualURLs = append(actualURLs, url.String())
			}

			tt.assert(g, actualURLs, err)
		})
	}
}

func TestFilterURLs(t *testing.T) {
	convertToURLs := func(urls []string) ([]*apis.URL, error) {
		var parsedURLs []*apis.URL
		for _, urlStr := range urls {
			url, err := apis.ParseURL(urlStr)
			if err != nil {
				return nil, err
			}
			parsedURLs = append(parsedURLs, url)
		}

		return parsedURLs, nil
	}
	t.Run("mixed internal and external URLs", func(t *testing.T) {
		g := NewGomegaWithT(t)
		inputURLs := []string{
			"http://192.168.1.10/",
			"http://api.example.com/",
			"http://10.0.0.20/",
			"https://secure.example.com/",
			"http://localhost/",
			"http://203.0.113.1/",
		}
		expectedInternal := []string{
			"http://192.168.1.10/",
			"http://10.0.0.20/",
			"http://localhost/",
		}
		expectedExternal := []string{
			"http://api.example.com/",
			"https://secure.example.com/",
			"http://203.0.113.1/",
		}

		parsedURLs, err := convertToURLs(inputURLs)
		g.Expect(err).ToNot(HaveOccurred())

		internalURLs := llmisvc.FilterInternalURLs(parsedURLs)
		actualInternal := make([]string, 0, len(internalURLs))
		for _, url := range internalURLs {
			actualInternal = append(actualInternal, url.String())
		}
		g.Expect(actualInternal).To(Equal(expectedInternal))

		externalURLs := llmisvc.FilterExternalURLs(parsedURLs)
		actualExternal := make([]string, 0, len(externalURLs))
		for _, url := range externalURLs {
			actualExternal = append(actualExternal, url.String())
		}
		g.Expect(actualExternal).To(Equal(expectedExternal))
	})

	t.Run("URLs with custom ports", func(t *testing.T) {
		g := NewGomegaWithT(t)
		inputURLs := []string{
			"http://192.168.1.10:8080/",
			"http://api.example.com:8080/",
			"https://secure.example.com:8443/",
			"http://localhost:3000/",
		}
		expectedInternal := []string{
			"http://192.168.1.10:8080/",
			"http://localhost:3000/",
		}
		expectedExternal := []string{
			"http://api.example.com:8080/",
			"https://secure.example.com:8443/",
		}

		parsedURLs, err := convertToURLs(inputURLs)
		g.Expect(err).ToNot(HaveOccurred())

		internalURLs := llmisvc.FilterInternalURLs(parsedURLs)
		actualInternal := make([]string, 0, len(internalURLs))
		for _, url := range internalURLs {
			actualInternal = append(actualInternal, url.String())
		}
		g.Expect(actualInternal).To(Equal(expectedInternal))

		externalURLs := llmisvc.FilterExternalURLs(parsedURLs)
		actualExternal := make([]string, 0, len(externalURLs))
		for _, url := range externalURLs {
			actualExternal = append(actualExternal, url.String())
		}
		g.Expect(actualExternal).To(Equal(expectedExternal))
	})

	t.Run("internal hostname types", func(t *testing.T) {
		g := NewGomegaWithT(t)
		inputURLs := []string{
			"http://localhost/",
			"http://service.local/",
			"http://app.localhost/",
			"http://backend.internal/",
			"http://api.example.com/",
		}
		expectedInternal := []string{
			"http://localhost/",
			"http://service.local/",
			"http://app.localhost/",
			"http://backend.internal/",
		}
		expectedExternal := []string{
			"http://api.example.com/",
		}

		parsedURLs, err := convertToURLs(inputURLs)
		g.Expect(err).ToNot(HaveOccurred())

		internalURLs := llmisvc.FilterInternalURLs(parsedURLs)
		actualInternal := make([]string, 0, len(internalURLs))
		for _, url := range internalURLs {
			actualInternal = append(actualInternal, url.String())
		}
		g.Expect(actualInternal).To(Equal(expectedInternal))

		externalURLs := llmisvc.FilterExternalURLs(parsedURLs)
		actualExternal := make([]string, 0, len(externalURLs))
		for _, url := range externalURLs {
			actualExternal = append(actualExternal, url.String())
		}
		g.Expect(actualExternal).To(Equal(expectedExternal))
	})

	t.Run("all internal URLs", func(t *testing.T) {
		g := NewGomegaWithT(t)
		inputURLs := []string{
			"http://192.168.1.10/",
			"http://10.0.0.20/",
			"http://localhost/",
		}
		expectedInternal := []string{
			"http://192.168.1.10/",
			"http://10.0.0.20/",
			"http://localhost/",
		}
		expectedExternal := []string{}

		parsedURLs, err := convertToURLs(inputURLs)
		g.Expect(err).ToNot(HaveOccurred())

		internalURLs := llmisvc.FilterInternalURLs(parsedURLs)
		actualInternal := make([]string, 0, len(internalURLs))
		for _, url := range internalURLs {
			actualInternal = append(actualInternal, url.String())
		}
		g.Expect(actualInternal).To(Equal(expectedInternal))

		externalURLs := llmisvc.FilterExternalURLs(parsedURLs)
		actualExternal := make([]string, 0, len(externalURLs))
		for _, url := range externalURLs {
			actualExternal = append(actualExternal, url.String())
		}
		g.Expect(actualExternal).To(Equal(expectedExternal))
	})

	t.Run("all external URLs", func(t *testing.T) {
		g := NewGomegaWithT(t)
		inputURLs := []string{
			"http://api.example.com/",
			"https://secure.example.com/",
			"http://203.0.113.1/",
		}
		expectedInternal := []string{}
		expectedExternal := []string{
			"http://api.example.com/",
			"https://secure.example.com/",
			"http://203.0.113.1/",
		}

		parsedURLs, err := convertToURLs(inputURLs)
		g.Expect(err).ToNot(HaveOccurred())

		internalURLs := llmisvc.FilterInternalURLs(parsedURLs)
		actualInternal := make([]string, 0, len(internalURLs))
		for _, url := range internalURLs {
			actualInternal = append(actualInternal, url.String())
		}
		g.Expect(actualInternal).To(Equal(expectedInternal))

		externalURLs := llmisvc.FilterExternalURLs(parsedURLs)
		actualExternal := make([]string, 0, len(externalURLs))
		for _, url := range externalURLs {
			actualExternal = append(actualExternal, url.String())
		}
		g.Expect(actualExternal).To(Equal(expectedExternal))
	})

	t.Run("empty URL slice", func(t *testing.T) {
		g := NewGomegaWithT(t)
		inputURLs := []string{}
		expectedInternal := []string{}
		expectedExternal := []string{}

		parsedURLs, err := convertToURLs(inputURLs)
		g.Expect(err).ToNot(HaveOccurred())

		internalURLs := llmisvc.FilterInternalURLs(parsedURLs)
		actualInternal := make([]string, 0, len(internalURLs))
		for _, url := range internalURLs {
			actualInternal = append(actualInternal, url.String())
		}
		g.Expect(actualInternal).To(Equal(expectedInternal))

		externalURLs := llmisvc.FilterExternalURLs(parsedURLs)
		actualExternal := make([]string, 0, len(externalURLs))
		for _, url := range externalURLs {
			actualExternal = append(actualExternal, url.String())
		}
		g.Expect(actualExternal).To(Equal(expectedExternal))
	})

	t.Run("URLs with paths", func(t *testing.T) {
		g := NewGomegaWithT(t)
		inputURLs := []string{
			"http://192.168.1.10/api/v1/models",
			"http://api.example.com/api/v1/models",
			"http://localhost:8080/health",
		}
		expectedInternal := []string{
			"http://192.168.1.10/api/v1/models",
			"http://localhost:8080/health",
		}
		expectedExternal := []string{
			"http://api.example.com/api/v1/models",
		}

		parsedURLs, err := convertToURLs(inputURLs)
		g.Expect(err).ToNot(HaveOccurred())

		internalURLs := llmisvc.FilterInternalURLs(parsedURLs)
		actualInternal := make([]string, 0, len(internalURLs))
		for _, url := range internalURLs {
			actualInternal = append(actualInternal, url.String())
		}
		g.Expect(actualInternal).To(Equal(expectedInternal))

		externalURLs := llmisvc.FilterExternalURLs(parsedURLs)
		actualExternal := make([]string, 0, len(externalURLs))
		for _, url := range externalURLs {
			actualExternal = append(actualExternal, url.String())
		}
		g.Expect(actualExternal).To(Equal(expectedExternal))
	})

	t.Run("IsInternalURL and IsExternalURL are opposites", func(t *testing.T) {
		g := NewGomegaWithT(t)
		testURLs := []string{
			"http://192.168.1.10/",
			"http://api.example.com/",
			"http://localhost/",
			"https://secure.example.com:8443/",
		}

		for _, urlStr := range testURLs {
			url, err := apis.ParseURL(urlStr)
			g.Expect(err).ToNot(HaveOccurred())

			isInternal := llmisvc.IsInternalURL(url)
			isExternal := llmisvc.IsExternalURL(url)

			g.Expect(isInternal).To(Equal(!isExternal), "URL %s should be either internal or external, not both", urlStr)
		}
	})

	t.Run("AddressTypeName", func(t *testing.T) {
		tests := []struct {
			name     string
			url      string
			expected string
		}{
			{
				name:     "external hostname",
				url:      "https://api.example.com/",
				expected: "gateway-external",
			},
			{
				name:     "external IP",
				url:      "http://203.0.113.1/",
				expected: "gateway-external",
			},
			{
				name:     "private IP",
				url:      "http://192.168.1.100/",
				expected: "internal",
			},
			{
				name:     "localhost",
				url:      "http://localhost/",
				expected: "internal",
			},
			{
				name:     "cluster-local service",
				url:      "http://my-service.default.svc.cluster.local/",
				expected: "gateway-internal",
			},
			{
				name:     "cluster-local with port",
				url:      "http://gateway.ns.svc.cluster.local:8080/",
				expected: "gateway-internal",
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				g := NewGomegaWithT(t)
				parsedURL, err := apis.ParseURL(tt.url)
				g.Expect(err).ToNot(HaveOccurred())

				result := llmisvc.AddressTypeName(parsedURL)
				g.Expect(result).To(Equal(tt.expected))
			})
		}
	})
}
