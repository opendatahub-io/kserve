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

//go:build distro

package inferencegraph

import (
	"context"
	"time"

	. "github.com/onsi/gomega"
	routev1 "github.com/openshift/api/route/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"knative.dev/pkg/apis"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
)

func admitRouteForURLTest(ctx context.Context, route *routev1.Route, graphKey types.NamespacedName) {
	const (
		timeout  = 10 * time.Second
		interval = 250 * time.Millisecond
	)

	Consistently(func() *apis.URL {
		graph := &v1alpha1.InferenceGraph{}
		Expect(k8sClient.Get(ctx, graphKey, graph)).To(Succeed())
		return graph.Status.URL
	}, time.Second, interval).Should(BeNil())

	route.Status.Ingress[0].Conditions = []routev1.RouteIngressCondition{
		{Type: routev1.RouteAdmitted, Status: corev1.ConditionTrue},
	}
	Expect(k8sClient.Status().Update(ctx, route)).To(Succeed())
	Eventually(func() *apis.URL {
		graph := &v1alpha1.InferenceGraph{}
		Expect(k8sClient.Get(ctx, graphKey, graph)).To(Succeed())
		return graph.Status.URL
	}, timeout, interval).ShouldNot(BeNil())
}
