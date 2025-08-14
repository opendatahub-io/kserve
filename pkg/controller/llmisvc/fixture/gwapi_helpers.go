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

package fixture

import (
	"context"

	"github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayapi "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
	"github.com/kserve/kserve/pkg/controller/llmisvc"
)

const (
	DefaultGatewayControllerName = "gateway.networking.k8s.io/gateway-controller"
)

// Only runs in non-cluster mode
func EnsureRouterManagedResourcesAreReady(ctx context.Context, c client.Client, llmSvc *v1alpha1.LLMInferenceService, inCluster bool) {
	if inCluster {
		return
	}

	gomega.Eventually(func(g gomega.Gomega, ctx context.Context) {
		// Get managed gateways and make them ready
		gateways := &gatewayapi.GatewayList{}
		listOpts := &client.ListOptions{
			Namespace:     llmSvc.Namespace,
			LabelSelector: labels.SelectorFromSet(llmisvc.RouterLabels(llmSvc)),
		}
		err := c.List(ctx, gateways, listOpts)
		if err != nil && !errors.IsNotFound(err) {
			g.Expect(err).NotTo(gomega.HaveOccurred())
		}

		for _, gateway := range gateways.Items {
			// Update gateway status to ready
			updatedGateway := gateway.DeepCopy()
			WithGatewayReadyStatus()(updatedGateway)
			g.Expect(c.Status().Update(ctx, updatedGateway)).To(gomega.Succeed())
		}

		// Get managed HTTPRoutes and make them ready
		httpRoutes := &gatewayapi.HTTPRouteList{}
		err = c.List(ctx, httpRoutes, listOpts)
		if err != nil && !errors.IsNotFound(err) {
			g.Expect(err).NotTo(gomega.HaveOccurred())
		}

		for _, route := range httpRoutes.Items {
			// Update HTTPRoute status to ready
			updatedRoute := route.DeepCopy()
			WithHTTPRouteReadyStatus(DefaultGatewayControllerName)(updatedRoute)
			g.Expect(c.Status().Update(ctx, updatedRoute)).To(gomega.Succeed())
		}

		// Ensure at least one HTTPRoute was found and made ready
		g.Expect(httpRoutes.Items).To(gomega.HaveLen(1), "Expected exactly one managed HTTPRoute")
	}).WithContext(ctx).Should(gomega.Succeed())
}
