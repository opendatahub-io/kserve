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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	istioapi "istio.io/client-go/pkg/apis/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"knative.dev/pkg/kmeta"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
	"github.com/kserve/kserve/pkg/constants"
	. "github.com/kserve/kserve/pkg/controller/v1alpha2/llmisvc/fixture"
)

var _ = Describe("Stopping a service whose route fails to render", func() {
	It("should still tear down its DestinationRules", func(ctx SpecContext) {
		// given a running service with a scheduler DestinationRule
		svc := LLMInferenceService("stopped",
			InNamespace[*v1alpha2.LLMInferenceService](NewTestNamespace(ctx, envTest).Name),
			WithModelURI("hf://facebook/opt-125m"), WithManagedRoute(), WithManagedGateway(), WithManagedScheduler())
		Expect(envTest.Create(ctx, svc)).To(Succeed())
		DeferCleanup(func(ctx SpecContext) {
			Expect(client.IgnoreNotFound(envTest.Delete(ctx, svc))).To(Succeed())
			Eventually(func(g Gomega, ctx context.Context) {
				g.Expect(apierrors.IsNotFound(envTest.Get(ctx, client.ObjectKeyFromObject(svc), &v1alpha2.LLMInferenceService{}))).To(BeTrue())
			}).WithContext(ctx).Should(Succeed())
		})
		schedulerRule := types.NamespacedName{Name: kmeta.ChildName(svc.Name, "-kserve-scheduler"), Namespace: svc.Namespace}
		Eventually(func(ctx context.Context) error {
			return envTest.Get(ctx, schedulerRule, &istioapi.DestinationRule{})
		}).WithContext(ctx).Should(Succeed())
		waitForReconcilesToSettle(ctx)

		// and a ConfigMap value its route cannot render with
		DeferCleanup(func(ctx SpecContext) { PatchIngressConfigKey(ctx, envTest.Client, routeRuleNamingKey, nil) })
		PatchIngressConfigKey(ctx, envTest.Client, routeRuleNamingKey, "unsupported")
		routesReadyReason := func(g Gomega, ctx context.Context) string {
			current := &v1alpha2.LLMInferenceService{}
			g.Expect(envTest.Get(ctx, client.ObjectKeyFromObject(svc), current)).To(Succeed())
			condition := current.Status.GetCondition(v1alpha2.HTTPRoutesReady)
			g.Expect(condition).NotTo(BeNil())
			return condition.Reason
		}
		Eventually(func(g Gomega, ctx context.Context) {
			g.Expect(routesReadyReason(g, ctx)).To(Equal("RoutingPreconditionNotMet"))
		}).WithContext(ctx).Should(Succeed())

		// when the service is stopped
		Expect(retry.RetryOnConflict(retry.DefaultRetry, func() error {
			current := &v1alpha2.LLMInferenceService{}
			if err := envTest.Get(ctx, client.ObjectKeyFromObject(svc), current); err != nil {
				return err
			}
			if current.Annotations == nil {
				current.Annotations = map[string]string{}
			}
			current.Annotations[constants.StopAnnotationKey] = "true"
			return envTest.Update(ctx, current)
		})).To(Succeed())

		// then the route and the DestinationRule are gone and the stop is reported as such
		Eventually(func(g Gomega, ctx context.Context) {
			g.Expect(routesReadyReason(g, ctx)).To(Equal("Stopped"))
			routes, err := managedRoutes(ctx, svc)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(routes).To(BeEmpty())
			g.Expect(apierrors.IsNotFound(envTest.Get(ctx, schedulerRule, &istioapi.DestinationRule{}))).To(BeTrue())
		}).WithContext(ctx).Should(Succeed())
	})
})
