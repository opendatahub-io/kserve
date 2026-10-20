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

package ingress

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	. "github.com/onsi/gomega"
	routev1 "github.com/openshift/api/route/v1"
	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	apierr "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"knative.dev/pkg/apis"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/kserve/kserve/pkg/apis/serving/v1beta1"
	"github.com/kserve/kserve/pkg/constants"
)

const (
	parityNS   = "parity-ns"
	parityName = "sklearn"
	parityUID  = types.UID("0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0")
	parityHost = "sklearn-parity-ns.apps.example.com"
)

type parityCanary struct {
	name    string
	percent int32
}

// routeFixture describes an InferenceService and the Services kserve creates for it. The
// builders below mirror the generator that produced the expected Routes from
// odh-model-controller's createDesiredResource; keep them in step.
type routeFixture struct {
	visibility        string
	auth              *string
	transformer       bool
	explainer         bool
	predictorName     string
	canaries          []parityCanary
	timeoutAnnotation *string
	predictorTimeout  *int64
	transformerTO     *int64
	explainerTO       *int64
	skipPredictorSvc  bool
	skipCanarySvc     bool
	onlyExplainerSvc  bool
}

func (f routeFixture) isvc() *v1beta1.InferenceService {
	isvc := &v1beta1.InferenceService{
		ObjectMeta: metav1.ObjectMeta{Name: parityName, Namespace: parityNS, UID: parityUID},
		Spec: v1beta1.InferenceServiceSpec{
			Predictor: v1beta1.PredictorSpec{
				ComponentExtensionSpec: v1beta1.ComponentExtensionSpec{TimeoutSeconds: f.predictorTimeout},
			},
		},
	}
	isvc.Spec.Predictor.Name = f.predictorName
	if f.visibility != "" {
		isvc.Labels = map[string]string{constants.NetworkVisibility: f.visibility}
	}
	annotations := map[string]string{}
	if f.auth != nil {
		annotations[constants.ODHKserveRawAuth] = *f.auth
	}
	if f.timeoutAnnotation != nil {
		annotations[routeTimeoutAnnotation] = *f.timeoutAnnotation
	}
	if len(annotations) > 0 {
		isvc.Annotations = annotations
	}
	if f.transformer {
		isvc.Spec.Transformer = &v1beta1.TransformerSpec{
			ComponentExtensionSpec: v1beta1.ComponentExtensionSpec{TimeoutSeconds: f.transformerTO},
		}
	}
	if f.explainer {
		isvc.Spec.Explainer = &v1beta1.ExplainerSpec{
			ComponentExtensionSpec: v1beta1.ComponentExtensionSpec{TimeoutSeconds: f.explainerTO},
		}
	}
	for _, c := range f.canaries {
		canary := v1beta1.CanarySpec{TrafficPercent: c.percent}
		canary.Predictor.Name = c.name
		isvc.Spec.Canary = append(isvc.Spec.Canary, canary)
	}
	return isvc
}

// services returns the Services the kserve service reconciler creates: the https port when auth
// is enabled (strings.EqualFold "true"), otherwise one port named after the Service.
func (f routeFixture) services() []client.Object {
	auth := f.auth != nil && strings.EqualFold(*f.auth, "true")
	if f.onlyExplainerSvc {
		name := parityName + "-explainer"
		return []client.Object{paritySvc(name, "explainer", parityPredictorPorts(name, false))}
	}
	var objs []client.Object
	if !f.skipPredictorSvc {
		name := constants.PredictorServiceName(parityName, f.predictorName)
		objs = append(objs, paritySvc(name, "predictor", parityPredictorPorts(name, auth)))
	}
	if f.transformer {
		name := parityName + "-transformer"
		objs = append(objs, paritySvc(name, "transformer", parityTransformerPorts(name, auth)))
	}
	if f.explainer {
		name := parityName + "-explainer"
		objs = append(objs, paritySvc(name, "explainer", parityPredictorPorts(name, false)))
	}
	if !f.skipCanarySvc {
		for _, c := range f.canaries {
			name := constants.PredictorServiceName(parityName, c.name)
			objs = append(objs, paritySvc(name, "predictor", parityPredictorPorts(name, auth)))
		}
	}
	return objs
}

func paritySvc(name, component string, ports []corev1.ServicePort) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: parityNS,
			Labels: map[string]string{
				constants.InferenceServicePodLabelKey: parityName,
				constants.KServiceComponentLabel:      component,
			},
		},
		Spec: corev1.ServiceSpec{ClusterIP: "10.0.0.1", Ports: ports},
	}
}

func parityPredictorPorts(svcName string, auth bool) []corev1.ServicePort {
	if auth {
		return []corev1.ServicePort{{Name: "https", Port: 8443, TargetPort: intstr.FromString("https"), Protocol: corev1.ProtocolTCP}}
	}
	return []corev1.ServicePort{{Name: svcName, Port: 80, TargetPort: intstr.FromInt32(8080), Protocol: corev1.ProtocolTCP}}
}

func parityTransformerPorts(svcName string, auth bool) []corev1.ServicePort {
	if auth {
		return []corev1.ServicePort{{Name: "https", Port: 8443, TargetPort: intstr.FromInt32(8080), Protocol: corev1.ProtocolTCP}}
	}
	return []corev1.ServicePort{{Name: svcName, Port: 80, TargetPort: intstr.FromInt32(8080), Protocol: corev1.ProtocolTCP}}
}

// parityRoute is the Route odh-model-controller builds for the fixtures. Only the timeout
// annotation and the spec vary between cases.
func parityRoute(timeout string, spec routev1.RouteSpec) *routev1.Route {
	return &routev1.Route{
		ObjectMeta: metav1.ObjectMeta{
			Name:        parityName,
			Namespace:   parityNS,
			Labels:      map[string]string{"inferenceservice-name": parityName},
			Annotations: map[string]string{"haproxy.router.openshift.io/timeout": timeout},
			OwnerReferences: []metav1.OwnerReference{
				{APIVersion: "serving.kserve.io/v1beta1", Kind: "InferenceService", Name: parityName, UID: parityUID, Controller: ptr.To(true), BlockOwnerDeletion: ptr.To(true)},
			},
		},
		Spec: spec,
	}
}

// ownedRouteFields keeps what the Route reconciler writes.
func ownedRouteFields(route *routev1.Route) *routev1.Route {
	return &routev1.Route{
		ObjectMeta: metav1.ObjectMeta{
			Name:            route.Name,
			Namespace:       route.Namespace,
			Labels:          route.Labels,
			Annotations:     route.Annotations,
			OwnerReferences: route.OwnerReferences,
		},
		Spec: route.Spec,
	}
}

func routeTestScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = v1beta1.AddToScheme(s)
	_ = corev1.AddToScheme(s)
	_ = netv1.AddToScheme(s)
	_ = routev1.AddToScheme(s)
	return s
}

func newTestRouteReconciler(t *testing.T, objs ...client.Object) (*RawRouteReconciler, client.Client) {
	t.Helper()
	return newTestRouteReconcilerWithInterceptor(t, interceptor.Funcs{}, objs...)
}

func newTestRouteReconcilerWithInterceptor(t *testing.T, funcs interceptor.Funcs, objs ...client.Object) (*RawRouteReconciler, client.Client) {
	t.Helper()
	s := routeTestScheme()
	cl := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).WithStatusSubresource(&routev1.Route{}).
		WithInterceptorFuncs(funcs).Build()
	r, err := NewRawRouteReconciler(cl, s, &v1beta1.IngressConfig{
		DisableIngressCreation: true,
		UrlScheme:              "http",
		IngressDomain:          "example.com",
		DomainTemplate:         "{{.Name}}-{{.Namespace}}.{{.IngressDomain}}",
	}, &v1beta1.InferenceServicesConfig{})
	if err != nil {
		t.Fatal(err)
	}
	return r, cl
}

// TestDesiredRouteMatchesOdhModelController pins the desired Route to the output of
// odh-model-controller's KserveRawRouteReconciler.createDesiredResource (incubating 25e10f4) for
// the same InferenceService and Services, so Routes it created are adopted unchanged.
func TestDesiredRouteMatchesOdhModelController(t *testing.T) {
	edgeTLS := &routev1.TLSConfig{Termination: routev1.TLSTerminationEdge, InsecureEdgeTerminationPolicy: routev1.InsecureEdgeTerminationPolicyRedirect}
	reencryptTLS := &routev1.TLSConfig{Termination: routev1.TLSTerminationReencrypt, InsecureEdgeTerminationPolicy: routev1.InsecureEdgeTerminationPolicyRedirect}
	target := func(name string, weight int32) routev1.RouteTargetReference {
		return routev1.RouteTargetReference{Kind: "Service", Name: name, Weight: ptr.To(weight)}
	}

	tests := map[string]struct {
		fixture routeFixture
		want    *routev1.Route
		wantErr string
	}{
		"exposed predictor": {
			fixture: routeFixture{visibility: "exposed"},
			want: parityRoute("30s", routev1.RouteSpec{
				To:             target("sklearn-predictor", 100),
				Port:           &routev1.RoutePort{TargetPort: intstr.FromString("sklearn-predictor")},
				TLS:            edgeTLS,
				WildcardPolicy: routev1.WildcardPolicyNone,
			}),
		},
		"exposed predictor with auth": {
			fixture: routeFixture{visibility: "exposed", auth: ptr.To("true")},
			want: parityRoute("30s", routev1.RouteSpec{
				To:             target("sklearn-predictor", 100),
				Port:           &routev1.RoutePort{TargetPort: intstr.FromString("https")},
				TLS:            reencryptTLS,
				WildcardPolicy: routev1.WildcardPolicyNone,
			}),
		},
		"exposed predictor with auth disabled": {
			fixture: routeFixture{visibility: "exposed", auth: ptr.To("false")},
			want: parityRoute("30s", routev1.RouteSpec{
				To:             target("sklearn-predictor", 100),
				Port:           &routev1.RoutePort{TargetPort: intstr.FromString("sklearn-predictor")},
				TLS:            edgeTLS,
				WildcardPolicy: routev1.WildcardPolicyNone,
			}),
		},
		"exposed predictor with auth 1 parsed by ParseBool only": {
			fixture: routeFixture{visibility: "exposed", auth: ptr.To("1")},
			want: parityRoute("30s", routev1.RouteSpec{
				To:             target("sklearn-predictor", 100),
				Port:           &routev1.RoutePort{TargetPort: intstr.FromString("sklearn-predictor")},
				TLS:            reencryptTLS,
				WildcardPolicy: routev1.WildcardPolicyNone,
			}),
		},
		"exposed predictor with auth tRuE matched by EqualFold only": {
			fixture: routeFixture{visibility: "exposed", auth: ptr.To("tRuE")},
			want: parityRoute("30s", routev1.RouteSpec{
				To:             target("sklearn-predictor", 100),
				Port:           &routev1.RoutePort{TargetPort: intstr.FromString("https")},
				TLS:            edgeTLS,
				WildcardPolicy: routev1.WildcardPolicyNone,
			}),
		},
		"exposed transformer": {
			fixture: routeFixture{visibility: "exposed", transformer: true},
			want: parityRoute("60s", routev1.RouteSpec{
				To:             target("sklearn-transformer", 100),
				Port:           &routev1.RoutePort{TargetPort: intstr.FromString("sklearn-transformer")},
				TLS:            edgeTLS,
				WildcardPolicy: routev1.WildcardPolicyNone,
			}),
		},
		"exposed transformer with auth": {
			fixture: routeFixture{visibility: "exposed", transformer: true, auth: ptr.To("true")},
			want: parityRoute("60s", routev1.RouteSpec{
				To:             target("sklearn-transformer", 100),
				Port:           &routev1.RoutePort{TargetPort: intstr.FromString("https")},
				TLS:            reencryptTLS,
				WildcardPolicy: routev1.WildcardPolicyNone,
			}),
		},
		"exposed custom predictor name": {
			fixture: routeFixture{visibility: "exposed", predictorName: "blue"},
			want: parityRoute("30s", routev1.RouteSpec{
				To:             target("sklearn-blue-predictor", 100),
				Port:           &routev1.RoutePort{TargetPort: intstr.FromString("sklearn-blue-predictor")},
				TLS:            edgeTLS,
				WildcardPolicy: routev1.WildcardPolicyNone,
			}),
		},
		"exposed custom predictor name with auth": {
			fixture: routeFixture{visibility: "exposed", predictorName: "blue", auth: ptr.To("true")},
			want: parityRoute("30s", routev1.RouteSpec{
				To:             target("sklearn-blue-predictor", 100),
				Port:           &routev1.RoutePort{TargetPort: intstr.FromString("https")},
				TLS:            reencryptTLS,
				WildcardPolicy: routev1.WildcardPolicyNone,
			}),
		},
		"exposed custom predictor name with transformer": {
			fixture: routeFixture{visibility: "exposed", predictorName: "blue", transformer: true},
			want: parityRoute("60s", routev1.RouteSpec{
				To:             target("sklearn-transformer", 100),
				Port:           &routev1.RoutePort{TargetPort: intstr.FromString("sklearn-transformer")},
				TLS:            edgeTLS,
				WildcardPolicy: routev1.WildcardPolicyNone,
			}),
		},
		"exposed canary": {
			fixture: routeFixture{visibility: "exposed", canaries: []parityCanary{{"v2", 10}}},
			want: parityRoute("30s", routev1.RouteSpec{
				To:                target("sklearn-predictor", 90),
				AlternateBackends: []routev1.RouteTargetReference{target("sklearn-v2-predictor", 10)},
				Port:              &routev1.RoutePort{TargetPort: intstr.FromInt32(8080)},
				TLS:               edgeTLS,
				WildcardPolicy:    routev1.WildcardPolicyNone,
			}),
		},
		"exposed canaries with auth": {
			fixture: routeFixture{visibility: "exposed", auth: ptr.To("true"), canaries: []parityCanary{{"v2", 20}, {"v3", 30}}},
			want: parityRoute("30s", routev1.RouteSpec{
				To: target("sklearn-predictor", 50),
				AlternateBackends: []routev1.RouteTargetReference{
					target("sklearn-v2-predictor", 20),
					target("sklearn-v3-predictor", 30),
				},
				Port:           &routev1.RoutePort{TargetPort: intstr.FromString("https")},
				TLS:            reencryptTLS,
				WildcardPolicy: routev1.WildcardPolicyNone,
			}),
		},
		"exposed canary dark launch": {
			fixture: routeFixture{visibility: "exposed", canaries: []parityCanary{{"v2", 0}}},
			want: parityRoute("30s", routev1.RouteSpec{
				To:                target("sklearn-predictor", 100),
				AlternateBackends: []routev1.RouteTargetReference{target("sklearn-v2-predictor", 0)},
				Port:              &routev1.RoutePort{TargetPort: intstr.FromInt32(8080)},
				TLS:               edgeTLS,
				WildcardPolicy:    routev1.WildcardPolicyNone,
			}),
		},
		"exposed canary with transformer": {
			fixture: routeFixture{visibility: "exposed", transformer: true, canaries: []parityCanary{{"v2", 25}}},
			want: parityRoute("60s", routev1.RouteSpec{
				To:                target("sklearn-transformer", 75),
				AlternateBackends: []routev1.RouteTargetReference{target("sklearn-v2-predictor", 25)},
				Port:              &routev1.RoutePort{TargetPort: intstr.FromInt32(8080)},
				TLS:               edgeTLS,
				WildcardPolicy:    routev1.WildcardPolicyNone,
			}),
		},
		"exposed canary with transformer and auth": {
			fixture: routeFixture{visibility: "exposed", transformer: true, auth: ptr.To("true"), canaries: []parityCanary{{"v2", 25}}},
			want: parityRoute("60s", routev1.RouteSpec{
				To:                target("sklearn-transformer", 75),
				AlternateBackends: []routev1.RouteTargetReference{target("sklearn-v2-predictor", 25)},
				Port:              &routev1.RoutePort{TargetPort: intstr.FromInt32(8080)},
				TLS:               reencryptTLS,
				WildcardPolicy:    routev1.WildcardPolicyNone,
			}),
		},
		"timeout annotation wins over component timeouts": {
			fixture: routeFixture{
				visibility: "exposed", transformer: true, explainer: true, timeoutAnnotation: ptr.To("1m"),
				predictorTimeout: ptr.To[int64](45), transformerTO: ptr.To[int64](45), explainerTO: ptr.To[int64](45),
			},
			want: parityRoute("1m", routev1.RouteSpec{
				To:             target("sklearn-transformer", 100),
				Port:           &routev1.RoutePort{TargetPort: intstr.FromString("sklearn-transformer")},
				TLS:            edgeTLS,
				WildcardPolicy: routev1.WildcardPolicyNone,
			}),
		},
		"empty timeout annotation is copied": {
			fixture: routeFixture{visibility: "exposed", timeoutAnnotation: ptr.To("")},
			want: parityRoute("", routev1.RouteSpec{
				To:             target("sklearn-predictor", 100),
				Port:           &routev1.RoutePort{TargetPort: intstr.FromString("sklearn-predictor")},
				TLS:            edgeTLS,
				WildcardPolicy: routev1.WildcardPolicyNone,
			}),
		},
		"explicit component timeouts are summed": {
			fixture: routeFixture{
				visibility: "exposed", transformer: true, explainer: true,
				predictorTimeout: ptr.To[int64](45), transformerTO: ptr.To[int64](45), explainerTO: ptr.To[int64](45),
			},
			want: parityRoute("135s", routev1.RouteSpec{
				To:             target("sklearn-transformer", 100),
				Port:           &routev1.RoutePort{TargetPort: intstr.FromString("sklearn-transformer")},
				TLS:            edgeTLS,
				WildcardPolicy: routev1.WildcardPolicyNone,
			}),
		},
		"default component timeouts are summed": {
			fixture: routeFixture{visibility: "exposed", transformer: true, explainer: true},
			want: parityRoute("90s", routev1.RouteSpec{
				To:             target("sklearn-transformer", 100),
				Port:           &routev1.RoutePort{TargetPort: intstr.FromString("sklearn-transformer")},
				TLS:            edgeTLS,
				WildcardPolicy: routev1.WildcardPolicyNone,
			}),
		},
		"explicit predictor timeout with default transformer timeout": {
			fixture: routeFixture{visibility: "exposed", transformer: true, predictorTimeout: ptr.To[int64](120)},
			want: parityRoute("150s", routev1.RouteSpec{
				To:             target("sklearn-transformer", 100),
				Port:           &routev1.RoutePort{TargetPort: intstr.FromString("sklearn-transformer")},
				TLS:            edgeTLS,
				WildcardPolicy: routev1.WildcardPolicyNone,
			}),
		},
		"cluster-local": {
			fixture: routeFixture{visibility: "cluster-local", auth: ptr.To("true")},
		},
		"no visibility label": {
			fixture: routeFixture{auth: ptr.To("true")},
		},
		"visibility label is case sensitive": {
			fixture: routeFixture{visibility: "Exposed"},
		},
		"exposed without services": {
			fixture: routeFixture{visibility: "exposed", skipPredictorSvc: true},
			wantErr: `no services found for InferenceService "sklearn"; the backing Service may not have been created yet`,
		},
		"exposed with only an explainer service": {
			fixture: routeFixture{visibility: "exposed", onlyExplainerSvc: true},
			wantErr: `no predictor or transformer Service found for InferenceService "sklearn"`,
		},
		"exposed canary without canary service": {
			fixture: routeFixture{visibility: "exposed", canaries: []parityCanary{{"v2", 10}}, skipCanarySvc: true},
			wantErr: `canary service "sklearn-v2-predictor" not found: services "sklearn-v2-predictor" not found`,
		},
		"exposed canaries over 100 percent": {
			fixture: routeFixture{visibility: "exposed", canaries: []parityCanary{{"v2", 60}, {"v3", 50}}},
			wantErr: "canary traffic percents sum to 110 which exceeds 100; stable backend weight would be -10",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			g := NewGomegaWithT(t)
			isvc := tc.fixture.isvc()
			r, _ := newTestRouteReconciler(t, append([]client.Object{isvc}, tc.fixture.services()...)...)

			got, err := r.desiredRoute(t.Context(), isvc)

			if tc.wantErr != "" {
				g.Expect(err).To(MatchError(tc.wantErr))
				return
			}
			g.Expect(err).NotTo(HaveOccurred())
			if tc.want == nil {
				g.Expect(got).To(BeNil())
				return
			}
			g.Expect(got).NotTo(BeNil())
			if diff := cmp.Diff(tc.want, ownedRouteFields(got)); diff != "" {
				t.Errorf("desired Route differs from odh-model-controller (-want +got):\n%s", diff)
			}
		})
	}
}

func TestRouteTargetService(t *testing.T) {
	svc := func(name, component string) corev1.Service {
		return corev1.Service{ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: map[string]string{constants.KServiceComponentLabel: component},
		}}
	}

	tests := map[string]struct {
		predictorName string
		services      []corev1.Service
		want          string
	}{
		"transformer wins over predictors": {
			services: []corev1.Service{svc("sklearn-predictor", "predictor"), svc("sklearn-transformer", "transformer")},
			want:     "sklearn-transformer",
		},
		"default predictor name wins over other predictors": {
			services: []corev1.Service{svc("random-predictor", "predictor"), svc("sklearn-predictor", "predictor")},
			want:     "sklearn-predictor",
		},
		"default predictor name matches without the component label": {
			services: []corev1.Service{svc("other-predictor", "predictor"), svc("sklearn-predictor", "")},
			want:     "sklearn-predictor",
		},
		"any predictor when the default one is missing": {
			services: []corev1.Service{svc("some-predictor", "predictor")},
			want:     "some-predictor",
		},
		// odh-model-controller took whichever predictor Service the list returned first; with a
		// named predictor and canaries that could be a canary.
		"named stable predictor wins over canaries whatever the order": {
			predictorName: "blue",
			services: []corev1.Service{
				svc("sklearn-a-predictor", "predictor"),
				svc("sklearn-blue-predictor", "predictor"),
				svc("sklearn-z-predictor", "predictor"),
			},
			want: "sklearn-blue-predictor",
		},
		"first predictor by name when none is the stable one": {
			services: []corev1.Service{svc("sklearn-z-predictor", "predictor"), svc("sklearn-a-predictor", "predictor")},
			want:     "sklearn-a-predictor",
		},
		"explainer only": {
			services: []corev1.Service{svc("sklearn-explainer", "explainer")},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			g := NewGomegaWithT(t)
			isvc := &v1beta1.InferenceService{ObjectMeta: metav1.ObjectMeta{Name: parityName}}
			isvc.Spec.Predictor.Name = tc.predictorName

			got := routeTargetService(isvc, tc.services)
			if tc.want == "" {
				g.Expect(got).To(BeNil())
				return
			}
			g.Expect(got).NotTo(BeNil())
			g.Expect(got.Name).To(Equal(tc.want))

			// The choice does not depend on the list order.
			reversed := make([]corev1.Service, 0, len(tc.services))
			for i := len(tc.services) - 1; i >= 0; i-- {
				reversed = append(reversed, tc.services[i])
			}
			g.Expect(routeTargetService(isvc, reversed).Name).To(Equal(tc.want))
		})
	}
}

// TestRouteTargetPort ports odh-model-controller's setRouteTargetPort cases.
func TestRouteTargetPort(t *testing.T) {
	tests := map[string]struct {
		reencrypt bool
		ports     []corev1.ServicePort
		want      intstr.IntOrString
		wantErr   bool
	}{
		"https named port with re-encryption": {
			reencrypt: true,
			ports:     []corev1.ServicePort{{Name: "http", Port: 80}, {Name: "https", Port: 443}},
			want:      intstr.FromString("https"),
		},
		"any named port when https is missing": {
			reencrypt: true,
			ports:     []corev1.ServicePort{{Name: "foo", Port: 1234}},
			want:      intstr.FromString("foo"),
		},
		"http named port without re-encryption": {
			ports: []corev1.ServicePort{{Name: "foo", Port: 1234}, {Name: "http", Port: 80}},
			want:  intstr.FromString("http"),
		},
		"any named port when http is missing": {
			ports: []corev1.ServicePort{{Name: "foo", Port: 1234}},
			want:  intstr.FromString("foo"),
		},
		"first port number when no port is named": {
			ports: []corev1.ServicePort{{Port: 8080}, {Port: 9090}},
			want:  intstr.FromInt32(8080),
		},
		"no ports": {
			wantErr: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			g := NewGomegaWithT(t)
			svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "svc"}, Spec: corev1.ServiceSpec{Ports: tc.ports}}

			got, err := routeTargetPort(tc.reencrypt, svc)

			if tc.wantErr {
				g.Expect(err).To(MatchError(`service "svc" has no ports defined`))
				return
			}
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(got).To(Equal(tc.want))
		})
	}
}

// statusFixture is an InferenceService with its entry point Services and, optionally, a Route
// named after it.
type statusFixture struct {
	visibility    string
	auth          *string
	transformer   bool
	predictorName string
	headless      bool
	route         string // "", "admitted", "pending" or "unowned"
}

func (f statusFixture) isvc() *v1beta1.InferenceService {
	return routeFixture{visibility: f.visibility, auth: f.auth, transformer: f.transformer, predictorName: f.predictorName}.isvc()
}

func (f statusFixture) objects() []client.Object {
	auth := f.auth != nil && strings.EqualFold(*f.auth, "true")
	predictor := constants.PredictorServiceName(parityName, f.predictorName)
	objs := []client.Object{statusSvc(predictor, "predictor", auth, f.headless)}
	if f.transformer {
		objs = append(objs, statusSvc(parityName+"-transformer", "transformer", auth, f.headless))
	}
	if f.route == "" {
		return objs
	}
	route := &routev1.Route{
		ObjectMeta: metav1.ObjectMeta{
			Name:      parityName,
			Namespace: parityNS,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "serving.kserve.io/v1beta1", Kind: "InferenceService", Name: parityName, UID: parityUID,
				Controller: ptr.To(true), BlockOwnerDeletion: ptr.To(true),
			}},
		},
		Spec: routev1.RouteSpec{
			Host: parityHost,
			To:   routev1.RouteTargetReference{Kind: "Service", Name: predictor, Weight: ptr.To[int32](100)},
			TLS:  &routev1.TLSConfig{Termination: routev1.TLSTerminationEdge, InsecureEdgeTerminationPolicy: routev1.InsecureEdgeTerminationPolicyRedirect},
		},
	}
	if f.route == "unowned" {
		route.OwnerReferences[0].UID = "11111111-2222-3333-4444-555555555555"
	}
	if f.route != "pending" {
		route.Status.Ingress = []routev1.RouteIngress{{
			Host:       parityHost,
			Conditions: []routev1.RouteIngressCondition{{Type: routev1.RouteAdmitted, Status: corev1.ConditionTrue}},
		}}
	}
	return append(objs, route)
}

func statusSvc(name, component string, auth, headless bool) *corev1.Service {
	ports := []corev1.ServicePort{{Name: name, Port: 80, TargetPort: intstr.FromInt32(8080), Protocol: corev1.ProtocolTCP}}
	if auth {
		ports = []corev1.ServicePort{{Name: "https", Port: 8443, TargetPort: intstr.FromString("https"), Protocol: corev1.ProtocolTCP}}
	}
	svc := paritySvc(name, component, ports)
	if headless {
		svc.Spec.ClusterIP = corev1.ClusterIPNone
	}
	return svc
}

// TestRawRouteReconcilerKeepsMidstreamStatus pins the status URL and address to what the
// midstream RawIngressReconciler reported at 53dcb1e29 (createRawURLODH plus the auth address
// override) for the same fixtures.
func TestRawRouteReconcilerKeepsMidstreamStatus(t *testing.T) {
	tests := map[string]struct {
		fixture     statusFixture
		wantURL     string
		wantAddress string
	}{
		"predictor": {
			fixture:     statusFixture{},
			wantURL:     "http://sklearn-predictor.parity-ns.svc.cluster.local",
			wantAddress: "http://sklearn-predictor.parity-ns.svc.cluster.local",
		},
		"predictor with auth": {
			fixture:     statusFixture{auth: ptr.To("true")},
			wantURL:     "https://sklearn-predictor.parity-ns.svc.cluster.local:8443",
			wantAddress: "https://sklearn-predictor.parity-ns.svc.cluster.local:8443",
		},
		"predictor with auth TRUE": {
			fixture:     statusFixture{auth: ptr.To("TRUE")},
			wantURL:     "https://sklearn-predictor.parity-ns.svc.cluster.local:8443",
			wantAddress: "https://sklearn-predictor.parity-ns.svc.cluster.local:8443",
		},
		"predictor with auth 1": {
			fixture:     statusFixture{auth: ptr.To("1")},
			wantURL:     "http://sklearn-predictor.parity-ns.svc.cluster.local",
			wantAddress: "http://sklearn-predictor.parity-ns.svc.cluster.local",
		},
		"predictor with auth false": {
			fixture:     statusFixture{auth: ptr.To("false")},
			wantURL:     "http://sklearn-predictor.parity-ns.svc.cluster.local",
			wantAddress: "http://sklearn-predictor.parity-ns.svc.cluster.local",
		},
		"cluster-local transformer with auth": {
			fixture:     statusFixture{visibility: "cluster-local", transformer: true, auth: ptr.To("true")},
			wantURL:     "https://sklearn-transformer.parity-ns.svc.cluster.local:8443",
			wantAddress: "https://sklearn-transformer.parity-ns.svc.cluster.local:8443",
		},
		"transformer": {
			fixture:     statusFixture{transformer: true},
			wantURL:     "http://sklearn-transformer.parity-ns.svc.cluster.local",
			wantAddress: "http://sklearn-transformer.parity-ns.svc.cluster.local",
		},
		"custom predictor name with auth": {
			fixture:     statusFixture{predictorName: "blue", auth: ptr.To("true")},
			wantURL:     "https://sklearn-blue-predictor.parity-ns.svc.cluster.local:8443",
			wantAddress: "https://sklearn-blue-predictor.parity-ns.svc.cluster.local:8443",
		},
		"headless predictor": {
			fixture:     statusFixture{headless: true},
			wantURL:     "http://sklearn-predictor.parity-ns.svc.cluster.local",
			wantAddress: "http://sklearn-predictor.parity-ns.svc.cluster.local:8080",
		},
		"headless predictor with auth": {
			fixture:     statusFixture{headless: true, auth: ptr.To("true")},
			wantURL:     "https://sklearn-predictor.parity-ns.svc.cluster.local:8443",
			wantAddress: "https://sklearn-predictor.parity-ns.svc.cluster.local:8443",
		},
		"exposed predictor": {
			fixture:     statusFixture{visibility: "exposed", route: "admitted"},
			wantURL:     "https://sklearn-parity-ns.apps.example.com",
			wantAddress: "http://sklearn-predictor.parity-ns.svc.cluster.local",
		},
		"exposed predictor with auth": {
			fixture:     statusFixture{visibility: "exposed", auth: ptr.To("true"), route: "admitted"},
			wantURL:     "https://sklearn-parity-ns.apps.example.com",
			wantAddress: "https://sklearn-predictor.parity-ns.svc.cluster.local:8443",
		},
		"exposed transformer with auth": {
			fixture:     statusFixture{visibility: "exposed", transformer: true, auth: ptr.To("true"), route: "admitted"},
			wantURL:     "https://sklearn-parity-ns.apps.example.com",
			wantAddress: "https://sklearn-transformer.parity-ns.svc.cluster.local:8443",
		},
		"exposed custom predictor name": {
			fixture:     statusFixture{visibility: "exposed", predictorName: "blue", route: "admitted"},
			wantURL:     "https://sklearn-parity-ns.apps.example.com",
			wantAddress: "http://sklearn-blue-predictor.parity-ns.svc.cluster.local",
		},
		"exposed headless predictor": {
			fixture:     statusFixture{visibility: "exposed", headless: true, route: "admitted"},
			wantURL:     "https://sklearn-parity-ns.apps.example.com",
			wantAddress: "http://sklearn-predictor.parity-ns.svc.cluster.local:8080",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			g := NewGomegaWithT(t)
			isvc := tc.fixture.isvc()
			r, _ := newTestRouteReconciler(t, append([]client.Object{isvc}, tc.fixture.objects()...)...)

			result, err := r.Reconcile(t.Context(), isvc)

			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(result).To(Equal(ctrl.Result{}))
			g.Expect(isvc.Status.URL).NotTo(BeNil())
			g.Expect(isvc.Status.URL.String()).To(Equal(tc.wantURL))
			g.Expect(isvc.Status.Address).NotTo(BeNil())
			g.Expect(isvc.Status.Address.URL.String()).To(Equal(tc.wantAddress))
			g.Expect(isvc.Status.IsConditionReady(v1beta1.IngressReady)).To(BeTrue())
		})
	}
}

// TestRawRouteReconcilerRouteNotUsable covers the exposed InferenceServices midstream failed to
// reconcile at 53dcb1e29 ("route parity-ns/sklearn not found or not ready"): they now report why
// through IngressReady.
func TestRawRouteReconcilerRouteNotUsable(t *testing.T) {
	rejected := func(route *routev1.Route) {
		route.Status.Ingress = []routev1.RouteIngress{{
			Host: parityHost,
			Conditions: []routev1.RouteIngressCondition{{
				Type: routev1.RouteAdmitted, Status: corev1.ConditionFalse,
				Reason: "HostAlreadyClaimed", Message: "route sklearn already exposes sklearn-parity-ns.apps.example.com",
			}},
		}}
	}

	tests := map[string]struct {
		fixture     statusFixture
		mutateRoute func(*routev1.Route)
		wantReason  string
		wantMessage string
		wantResult  ctrl.Result
		wantAddress string
	}{
		"Route not created yet": {
			fixture:     statusFixture{visibility: "exposed"},
			wantReason:  RouteNotAdmittedReason,
			wantMessage: "Route parity-ns/sklearn has not been admitted by a router",
			wantResult:  ctrl.Result{RequeueAfter: routeAdmissionRequeueInterval},
			wantAddress: "http://sklearn-predictor.parity-ns.svc.cluster.local",
		},
		"Route pending admission": {
			fixture:     statusFixture{visibility: "exposed", auth: ptr.To("true"), route: "pending"},
			wantReason:  RouteNotAdmittedReason,
			wantMessage: "Route parity-ns/sklearn has not been admitted by a router",
			wantResult:  ctrl.Result{RequeueAfter: routeAdmissionRequeueInterval},
			wantAddress: "https://sklearn-predictor.parity-ns.svc.cluster.local:8443",
		},
		"Route rejected by the router": {
			fixture:     statusFixture{visibility: "exposed", route: "admitted"},
			mutateRoute: rejected,
			wantReason:  RouteNotAdmittedReason,
			wantMessage: "Route parity-ns/sklearn was not admitted: HostAlreadyClaimed route sklearn already exposes sklearn-parity-ns.apps.example.com",
			wantResult:  ctrl.Result{RequeueAfter: routeAdmissionRequeueInterval},
			wantAddress: "http://sklearn-predictor.parity-ns.svc.cluster.local",
		},
		// The Route watch maps by name, so a change to the foreign Route triggers the next
		// reconcile.
		"Route owned by another object": {
			fixture:     statusFixture{visibility: "exposed", route: "unowned"},
			wantReason:  RouteNotOwnedReason,
			wantMessage: "Route parity-ns/sklearn is not controlled by this InferenceService",
			wantAddress: "http://sklearn-predictor.parity-ns.svc.cluster.local",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			g := NewGomegaWithT(t)
			isvc := tc.fixture.isvc()
			objs := append([]client.Object{isvc}, tc.fixture.objects()...)
			if tc.mutateRoute != nil {
				tc.mutateRoute(objs[len(objs)-1].(*routev1.Route))
			}
			r, _ := newTestRouteReconciler(t, objs...)

			result, err := r.Reconcile(t.Context(), isvc)

			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(result).To(Equal(tc.wantResult))
			g.Expect(isvc.Status.URL).To(BeNil())
			g.Expect(isvc.Status.Address.URL.String()).To(Equal(tc.wantAddress))
			cond := isvc.Status.GetCondition(v1beta1.IngressReady)
			g.Expect(cond).NotTo(BeNil())
			g.Expect(cond.Status).To(Equal(corev1.ConditionFalse))
			g.Expect(cond.Reason).To(Equal(tc.wantReason))
			g.Expect(cond.Message).To(Equal(tc.wantMessage))
		})
	}
}

// TestRawRouteReconcilerRouteErrors checks that a failed Route write never leaves the URL
// RawIngressReconciler derives from the ingress domain, or IngressReady True, in the status.
func TestRawRouteReconcilerRouteErrors(t *testing.T) {
	previousURL := "https://sklearn-parity-ns.apps.example.com"
	drifted := parityRoute("5s", routev1.RouteSpec{
		Host:           parityHost,
		To:             routev1.RouteTargetReference{Kind: "Service", Name: "sklearn-predictor", Weight: ptr.To[int32](100)},
		Port:           &routev1.RoutePort{TargetPort: intstr.FromString("sklearn-predictor")},
		TLS:            &routev1.TLSConfig{Termination: routev1.TLSTerminationEdge, InsecureEdgeTerminationPolicy: routev1.InsecureEdgeTerminationPolicyRedirect},
		WildcardPolicy: routev1.WildcardPolicyNone,
	})
	conflict := apierr.NewConflict(routev1.Resource("routes"), parityName, errors.New("the object has been modified"))
	alreadyExists := apierr.NewAlreadyExists(routev1.Resource("routes"), parityName)
	forbidden := apierr.NewForbidden(routev1.Resource("routes"), parityName, errors.New("denied"))

	tests := map[string]struct {
		fixture     routeFixture
		existing    *routev1.Route
		funcs       interceptor.Funcs
		wantResult  ctrl.Result
		wantErr     bool
		wantURL     string // "" means nil
		wantReason  string // "" means IngressReady untouched
		wantMessage string
	}{
		"update conflict requeues and keeps the status": {
			fixture:  routeFixture{visibility: "exposed"},
			existing: drifted,
			funcs: interceptor.Funcs{Update: func(context.Context, client.WithWatch, client.Object, ...client.UpdateOption) error {
				return conflict
			}},
			wantResult: ctrl.Result{RequeueAfter: routeConflictRequeueInterval},
			wantURL:    previousURL,
		},
		"concurrent create requeues and keeps the status": {
			fixture: routeFixture{visibility: "exposed"},
			funcs: interceptor.Funcs{Create: func(context.Context, client.WithWatch, client.Object, ...client.CreateOption) error {
				return alreadyExists
			}},
			wantResult: ctrl.Result{RequeueAfter: routeConflictRequeueInterval},
			wantURL:    previousURL,
		},
		"failed update clears the URL": {
			fixture:  routeFixture{visibility: "exposed"},
			existing: drifted,
			funcs: interceptor.Funcs{Update: func(context.Context, client.WithWatch, client.Object, ...client.UpdateOption) error {
				return forbidden
			}},
			wantErr:     true,
			wantReason:  RouteReconcileFailedReason,
			wantMessage: `failed to update Route parity-ns/sklearn: routes.route.openshift.io "sklearn" is forbidden: denied`,
		},
		"missing Services clear the URL": {
			fixture:     routeFixture{visibility: "exposed", skipPredictorSvc: true},
			wantErr:     true,
			wantReason:  RouteReconcileFailedReason,
			wantMessage: `no services found for InferenceService "sklearn"; the backing Service may not have been created yet`,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			g := NewGomegaWithT(t)
			isvc := tc.fixture.isvc()
			isvc.Status.URL, _ = apis.ParseURL(previousURL)
			isvc.Status.SetCondition(v1beta1.IngressReady, &apis.Condition{Type: v1beta1.IngressReady, Status: corev1.ConditionTrue})
			objs := append([]client.Object{isvc}, tc.fixture.services()...)
			if tc.existing != nil {
				objs = append(objs, tc.existing.DeepCopy())
			}
			r, _ := newTestRouteReconcilerWithInterceptor(t, tc.funcs, objs...)

			result, err := r.Reconcile(t.Context(), isvc)

			g.Expect(err != nil).To(Equal(tc.wantErr), "unexpected error: %v", err)
			g.Expect(result).To(Equal(tc.wantResult))
			if tc.wantURL == "" {
				g.Expect(isvc.Status.URL).To(BeNil())
			} else {
				g.Expect(isvc.Status.URL.String()).To(Equal(tc.wantURL))
			}
			cond := isvc.Status.GetCondition(v1beta1.IngressReady)
			if tc.wantReason == "" {
				g.Expect(cond.Status).To(Equal(corev1.ConditionTrue))
				return
			}
			g.Expect(cond.Status).To(Equal(corev1.ConditionFalse))
			g.Expect(cond.Reason).To(Equal(tc.wantReason))
			g.Expect(cond.Message).To(Equal(tc.wantMessage))
		})
	}
}

func TestRawRouteReconcilerRouteLifecycle(t *testing.T) {
	exposed := routeFixture{visibility: "exposed", auth: ptr.To("true")}
	desired := parityRoute("30s", routev1.RouteSpec{
		To:             routev1.RouteTargetReference{Kind: "Service", Name: "sklearn-predictor", Weight: ptr.To[int32](100)},
		Port:           &routev1.RoutePort{TargetPort: intstr.FromString("https")},
		TLS:            &routev1.TLSConfig{Termination: routev1.TLSTerminationReencrypt, InsecureEdgeTerminationPolicy: routev1.InsecureEdgeTerminationPolicyRedirect},
		WildcardPolicy: routev1.WildcardPolicyNone,
	})
	admitted := func(route *routev1.Route) *routev1.Route {
		route = route.DeepCopy()
		route.Spec.Host = parityHost
		route.Status.Ingress = []routev1.RouteIngress{{
			Host:       parityHost,
			Conditions: []routev1.RouteIngressCondition{{Type: routev1.RouteAdmitted, Status: corev1.ConditionTrue}},
		}}
		return route
	}
	unowned := func(route *routev1.Route) *routev1.Route {
		route = route.DeepCopy()
		route.OwnerReferences = nil
		return route
	}
	stopped := func(isvc *v1beta1.InferenceService) {
		isvc.Annotations[constants.StopAnnotationKey] = "true"
	}

	tests := map[string]struct {
		fixture     routeFixture
		mutateISVC  func(*v1beta1.InferenceService)
		existing    *routev1.Route
		wantRoute   *routev1.Route // nil: no Route with the InferenceService name
		wantWritten bool           // the existing Route was updated
	}{
		"creates the Route": {
			fixture:   exposed,
			wantRoute: desired,
		},
		"adopts a Route odh-model-controller created without writing it": {
			fixture:   exposed,
			existing:  admitted(desired),
			wantRoute: admitted(desired),
		},
		"restores a drifted Route and keeps its host": {
			fixture: exposed,
			existing: func() *routev1.Route {
				route := admitted(desired)
				route.Labels["extra"] = "label"
				route.Annotations[routeTimeoutAnnotation] = "5s"
				route.Spec.Port.TargetPort = intstr.FromInt32(8080)
				route.Spec.Path = "/v1"
				return route
			}(),
			wantRoute:   admitted(desired),
			wantWritten: true,
		},
		"restores a drifted timeout annotation": {
			fixture: exposed,
			existing: func() *routev1.Route {
				route := admitted(desired)
				route.Annotations[routeTimeoutAnnotation] = "5s"
				return route
			}(),
			wantRoute:   admitted(desired),
			wantWritten: true,
		},
		"restores drifted labels": {
			fixture: exposed,
			existing: func() *routev1.Route {
				route := admitted(desired)
				route.Labels["extra"] = "label"
				return route
			}(),
			wantRoute:   admitted(desired),
			wantWritten: true,
		},
		"restores a drifted wildcard policy": {
			fixture: exposed,
			existing: func() *routev1.Route {
				route := admitted(desired)
				route.Spec.WildcardPolicy = routev1.WildcardPolicySubdomain
				return route
			}(),
			wantRoute:   admitted(desired),
			wantWritten: true,
		},
		"leaves a Route it does not control untouched": {
			fixture: exposed,
			existing: func() *routev1.Route {
				route := unowned(admitted(desired))
				route.Spec.Port.TargetPort = intstr.FromInt32(8080)
				return route
			}(),
			wantRoute: func() *routev1.Route {
				route := unowned(admitted(desired))
				route.Spec.Port.TargetPort = intstr.FromInt32(8080)
				return route
			}(),
		},
		"deletes its Route once the InferenceService is no longer exposed": {
			fixture:  routeFixture{auth: ptr.To("true")},
			existing: admitted(desired),
		},
		"keeps a Route it does not control when the InferenceService is not exposed": {
			fixture:   routeFixture{auth: ptr.To("true")},
			existing:  unowned(admitted(desired)),
			wantRoute: unowned(admitted(desired)),
		},
		"leaves the Route alone while the InferenceService is stopped": {
			fixture:    routeFixture{auth: ptr.To("true")},
			mutateISVC: stopped,
			existing:   admitted(desired),
			wantRoute:  admitted(desired),
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			g := NewGomegaWithT(t)
			isvc := tc.fixture.isvc()
			if tc.mutateISVC != nil {
				tc.mutateISVC(isvc)
			}
			objs := append([]client.Object{isvc}, tc.fixture.services()...)
			if tc.existing != nil {
				objs = append(objs, tc.existing.DeepCopy())
			}
			r, cl := newTestRouteReconciler(t, objs...)
			var before routev1.Route
			if tc.existing != nil {
				g.Expect(cl.Get(t.Context(), client.ObjectKeyFromObject(tc.existing), &before)).To(Succeed())
			}

			_, err := r.Reconcile(t.Context(), isvc)
			g.Expect(err).NotTo(HaveOccurred())

			got := &routev1.Route{}
			err = cl.Get(t.Context(), types.NamespacedName{Name: parityName, Namespace: parityNS}, got)
			if tc.wantRoute == nil {
				g.Expect(apierr.IsNotFound(err)).To(BeTrue(), "unexpected error: %v", err)
				return
			}
			g.Expect(err).NotTo(HaveOccurred())
			if diff := cmp.Diff(ownedRouteFields(tc.wantRoute), ownedRouteFields(got)); diff != "" {
				t.Errorf("Route (-want +got):\n%s", diff)
			}
			if tc.existing != nil {
				g.Expect(got.Status).To(Equal(before.Status))
				if tc.wantWritten {
					g.Expect(got.ResourceVersion).NotTo(Equal(before.ResourceVersion))
				} else {
					g.Expect(got.ResourceVersion).To(Equal(before.ResourceVersion))
				}
			}
		})
	}
}

// With Kubernetes Ingress creation enabled, RawIngressReconciler waits for the predictor before
// it reports a URL. The Route does not wait: odh-model-controller created it as soon as the
// Services existed.
func TestRawRouteReconcilerCreatesRouteBeforeIngressIsReady(t *testing.T) {
	g := NewGomegaWithT(t)
	fixture := routeFixture{visibility: "exposed"}
	isvc := fixture.isvc()
	s := routeTestScheme()
	cl := fake.NewClientBuilder().WithScheme(s).WithObjects(append([]client.Object{isvc}, fixture.services()...)...).Build()
	r, err := NewRawRouteReconciler(cl, s, &v1beta1.IngressConfig{
		UrlScheme:      "http",
		IngressDomain:  "example.com",
		DomainTemplate: "{{.Name}}-{{.Namespace}}.{{.IngressDomain}}",
	}, &v1beta1.InferenceServicesConfig{})
	g.Expect(err).NotTo(HaveOccurred())

	result, err := r.Reconcile(t.Context(), isvc)

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result).To(Equal(ctrl.Result{}))
	g.Expect(cl.Get(t.Context(), types.NamespacedName{Name: parityName, Namespace: parityNS}, &routev1.Route{})).To(Succeed())
	g.Expect(isvc.Status.URL).To(BeNil())
	cond := isvc.Status.GetCondition(v1beta1.IngressReady)
	g.Expect(cond).NotTo(BeNil())
	g.Expect(cond.Status).To(Equal(corev1.ConditionFalse))
	g.Expect(cond.Reason).To(Equal("Predictor ingress not created"))
}
