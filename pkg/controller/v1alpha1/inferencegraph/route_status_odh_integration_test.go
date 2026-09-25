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
	Consistently(func() *apis.URL {
		graph := &v1alpha1.InferenceGraph{}
		Expect(k8sClient.Get(ctx, graphKey, graph)).To(Succeed())
		return graph.Status.URL
	}, time.Second, 250*time.Millisecond).Should(BeNil())

	route.Status.Ingress[0].Conditions = []routev1.RouteIngressCondition{
		{Type: routev1.RouteAdmitted, Status: corev1.ConditionTrue},
	}
	Expect(k8sClient.Status().Update(ctx, route)).To(Succeed())
}
