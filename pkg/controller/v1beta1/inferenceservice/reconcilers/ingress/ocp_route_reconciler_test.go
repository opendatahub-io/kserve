//go:build distro

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

package ingress

import (
	"strconv"
	"testing"

	. "github.com/onsi/gomega"
	routev1 "github.com/openshift/api/route/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/kserve/kserve/pkg/apis/serving/v1beta1"
	"github.com/kserve/kserve/pkg/constants"
)

func ocpTestScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = v1beta1.AddToScheme(s)
	_ = corev1.AddToScheme(s)
	_ = routev1.AddToScheme(s)
	return s
}

func makePredictorService(ports []corev1.ServicePort) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      constants.PredictorServiceName(testIsvcName),
			Namespace: testNamespace,
			Labels: map[string]string{
				constants.InferenceServicePodLabelKey: testIsvcName,
				constants.KServiceComponentLabel:      string(constants.Predictor),
			},
		},
		Spec: corev1.ServiceSpec{
			ClusterIP: "10.0.0.1",
			Ports:     ports,
		},
	}
}

func makeTransformerService(isvcName string, ports []corev1.ServicePort) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      constants.TransformerServiceName(isvcName),
			Namespace: testNamespace,
			Labels: map[string]string{
				constants.InferenceServicePodLabelKey: isvcName,
				constants.KServiceComponentLabel:      string(constants.Transformer),
			},
		},
		Spec: corev1.ServiceSpec{
			ClusterIP: "10.0.0.2",
			Ports:     ports,
		},
	}
}

func defaultPorts() []corev1.ServicePort {
	return []corev1.ServicePort{
		{Name: "http", Port: 80},
		{Name: "https", Port: 8443},
	}
}

func makeISVC(labels map[string]string, annotations map[string]string) *v1beta1.InferenceService {
	isvc := &v1beta1.InferenceService{
		ObjectMeta: metav1.ObjectMeta{
			Name:        testIsvcName,
			Namespace:   testNamespace,
			UID:         types.UID("test-uid"),
			Labels:      labels,
			Annotations: annotations,
		},
		Spec: v1beta1.InferenceServiceSpec{
			Predictor: v1beta1.PredictorSpec{},
		},
	}
	isvc.Status.InitializeConditions()
	return isvc
}

func admittedRoute(host string) *routev1.Route {
	return &routev1.Route{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testIsvcName,
			Namespace: testNamespace,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "serving.kserve.io/v1beta1",
					Kind:       "InferenceService",
					Name:       testIsvcName,
					UID:        "test-uid",
					Controller: ptr.To(true),
				},
			},
		},
		Spec: routev1.RouteSpec{
			To: routev1.RouteTargetReference{
				Kind:   "Service",
				Name:   constants.PredictorServiceName(testIsvcName),
				Weight: ptr.To(int32(100)),
			},
			TLS: &routev1.TLSConfig{
				Termination:                   routev1.TLSTerminationEdge,
				InsecureEdgeTerminationPolicy: routev1.InsecureEdgeTerminationPolicyRedirect,
			},
		},
		Status: routev1.RouteStatus{
			Ingress: []routev1.RouteIngress{
				{
					Host: host,
					Conditions: []routev1.RouteIngressCondition{
						{Type: routev1.RouteAdmitted, Status: corev1.ConditionTrue},
					},
				},
			},
		},
	}
}

func newOCPReconciler(scheme *runtime.Scheme, objs ...runtime.Object) *RawOCPRouteReconciler {
	cl := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objs...).Build()
	return NewRawOCPRouteReconciler(cl, scheme,
		&v1beta1.IngressConfig{},
		&v1beta1.InferenceServicesConfig{},
	)
}

func TestRawOCPRoute_ExposedNoAuth(t *testing.T) {
	g := NewGomegaWithT(t)
	s := ocpTestScheme()

	svc := makePredictorService(defaultPorts())
	isvc := makeISVC(
		map[string]string{constants.NetworkVisibility: constants.ODHRouteEnabled},
		nil,
	)
	rec := newOCPReconciler(s, svc, isvc)

	result, err := rec.Reconcile(t.Context(), isvc)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(result.RequeueAfter).To(BeNumerically(">", 0), "should requeue waiting for Route admission")

	createdRoute := &routev1.Route{}
	err = rec.client.Get(t.Context(), types.NamespacedName{Name: testIsvcName, Namespace: testNamespace}, createdRoute)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(createdRoute.Spec.TLS.Termination).To(Equal(routev1.TLSTerminationEdge))
	g.Expect(createdRoute.Spec.To.Name).To(Equal(constants.PredictorServiceName(testIsvcName)))
}

func TestRawOCPRoute_ExposedWithAuth(t *testing.T) {
	g := NewGomegaWithT(t)
	s := ocpTestScheme()

	svc := makePredictorService(defaultPorts())
	routeHost := testIsvcName + ".apps.example.com"
	route := admittedRoute(routeHost)
	route.Spec.TLS.Termination = routev1.TLSTerminationReencrypt

	isvc := makeISVC(
		map[string]string{constants.NetworkVisibility: constants.ODHRouteEnabled},
		map[string]string{constants.ODHKserveRawAuth: "true"},
	)
	rec := newOCPReconciler(s, svc, isvc, route)

	result, err := rec.Reconcile(t.Context(), isvc)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(result.Requeue).To(BeFalse())

	g.Expect(isvc.Status.URL).ToNot(BeNil())
	g.Expect(isvc.Status.URL.Scheme).To(Equal("https"))
	g.Expect(isvc.Status.URL.Host).To(Equal(routeHost))

	g.Expect(isvc.Status.Address).ToNot(BeNil())
	g.Expect(isvc.Status.Address.URL.Scheme).To(Equal("https"))
	g.Expect(isvc.Status.Address.URL.Host).To(ContainSubstring(strconv.Itoa(constants.OauthProxyPort)))
}

func TestRawOCPRoute_ClusterLocalNoAuth(t *testing.T) {
	g := NewGomegaWithT(t)
	s := ocpTestScheme()

	svc := makePredictorService(defaultPorts())
	isvc := makeISVC(nil, nil)
	rec := newOCPReconciler(s, svc, isvc)

	result, err := rec.Reconcile(t.Context(), isvc)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(result.Requeue).To(BeFalse())

	g.Expect(isvc.Status.URL).ToNot(BeNil())
	g.Expect(isvc.Status.URL.Scheme).To(Equal("http"))
	g.Expect(isvc.Status.URL.Host).To(Equal(predictorHost()))

	noRoute := &routev1.Route{}
	err = rec.client.Get(t.Context(), types.NamespacedName{Name: testIsvcName, Namespace: testNamespace}, noRoute)
	g.Expect(err).To(HaveOccurred(), "no Route should exist for cluster-local ISVC")
}

func TestRawOCPRoute_ClusterLocalWithAuth(t *testing.T) {
	g := NewGomegaWithT(t)
	s := ocpTestScheme()

	svc := makePredictorService(defaultPorts())
	isvc := makeISVC(nil,
		map[string]string{constants.ODHKserveRawAuth: "true"},
	)
	rec := newOCPReconciler(s, svc, isvc)

	result, err := rec.Reconcile(t.Context(), isvc)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(result.Requeue).To(BeFalse())

	g.Expect(isvc.Status.URL).ToNot(BeNil())
	g.Expect(isvc.Status.URL.Scheme).To(Equal("https"))
	expectedHost := predictorHost() + ":" + strconv.Itoa(constants.OauthProxyPort)
	g.Expect(isvc.Status.URL.Host).To(Equal(expectedHost))

	g.Expect(isvc.Status.Address).ToNot(BeNil())
	g.Expect(isvc.Status.Address.URL.Scheme).To(Equal("https"))
	g.Expect(isvc.Status.Address.URL.Host).To(Equal(expectedHost))
}

func TestRawOCPRoute_StoppedISVC(t *testing.T) {
	g := NewGomegaWithT(t)
	s := ocpTestScheme()

	svc := makePredictorService(defaultPorts())
	route := admittedRoute("host.example.com")

	isvc := makeISVC(
		map[string]string{constants.NetworkVisibility: constants.ODHRouteEnabled},
		map[string]string{constants.StopAnnotationKey: "true"},
	)
	rec := newOCPReconciler(s, svc, isvc, route)

	result, err := rec.Reconcile(t.Context(), isvc)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(result.Requeue).To(BeFalse())

	deletedRoute := &routev1.Route{}
	err = rec.client.Get(t.Context(), types.NamespacedName{Name: testIsvcName, Namespace: testNamespace}, deletedRoute)
	g.Expect(err).To(HaveOccurred(), "Route should be deleted for stopped ISVC")

	cond := isvc.Status.GetCondition(v1beta1.IngressReady)
	g.Expect(cond).ToNot(BeNil())
	g.Expect(cond.Status).To(Equal(corev1.ConditionFalse))
}

func TestRawOCPRoute_LabelRemoved(t *testing.T) {
	g := NewGomegaWithT(t)
	s := ocpTestScheme()

	svc := makePredictorService(defaultPorts())
	route := admittedRoute("host.example.com")

	isvc := makeISVC(nil, nil)
	rec := newOCPReconciler(s, svc, isvc, route)

	result, err := rec.Reconcile(t.Context(), isvc)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(result.Requeue).To(BeFalse())

	deletedRoute := &routev1.Route{}
	err = rec.client.Get(t.Context(), types.NamespacedName{Name: testIsvcName, Namespace: testNamespace}, deletedRoute)
	g.Expect(err).To(HaveOccurred(), "Route should be deleted when visibility label removed")
}

func TestRawOCPRoute_TransformerPresent(t *testing.T) {
	g := NewGomegaWithT(t)
	s := ocpTestScheme()

	predSvc := makePredictorService(defaultPorts())
	transSvc := makeTransformerService(testIsvcName, defaultPorts())

	isvc := makeISVC(
		map[string]string{constants.NetworkVisibility: constants.ODHRouteEnabled},
		nil,
	)
	isvc.Spec.Transformer = &v1beta1.TransformerSpec{}
	rec := newOCPReconciler(s, predSvc, transSvc, isvc)

	result, err := rec.Reconcile(t.Context(), isvc)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(result.RequeueAfter).To(BeNumerically(">", 0))

	createdRoute := &routev1.Route{}
	err = rec.client.Get(t.Context(), types.NamespacedName{Name: testIsvcName, Namespace: testNamespace}, createdRoute)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(createdRoute.Spec.To.Name).To(Equal(constants.TransformerServiceName(testIsvcName)))
}

func TestRawOCPRoute_NotAdmitted(t *testing.T) {
	g := NewGomegaWithT(t)
	s := ocpTestScheme()

	svc := makePredictorService(defaultPorts())
	route := admittedRoute("")
	route.Status.Ingress[0].Conditions[0].Status = corev1.ConditionFalse

	isvc := makeISVC(
		map[string]string{constants.NetworkVisibility: constants.ODHRouteEnabled},
		nil,
	)
	rec := newOCPReconciler(s, svc, isvc, route)

	result, err := rec.Reconcile(t.Context(), isvc)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(result.RequeueAfter).To(BeNumerically(">", 0))

	cond := isvc.Status.GetCondition(v1beta1.IngressReady)
	g.Expect(cond).ToNot(BeNil())
	g.Expect(cond.Status).To(Equal(corev1.ConditionFalse))
}

func TestRawOCPRoute_Timeout(t *testing.T) {
	g := NewGomegaWithT(t)
	s := ocpTestScheme()

	svc := makePredictorService(defaultPorts())
	isvc := makeISVC(
		map[string]string{constants.NetworkVisibility: constants.ODHRouteEnabled},
		nil,
	)
	isvc.Spec.Predictor.TimeoutSeconds = ptr.To(int64(120))

	rec := newOCPReconciler(s, svc, isvc)

	_, err := rec.Reconcile(t.Context(), isvc)
	g.Expect(err).ToNot(HaveOccurred())

	createdRoute := &routev1.Route{}
	err = rec.client.Get(t.Context(), types.NamespacedName{Name: testIsvcName, Namespace: testNamespace}, createdRoute)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(createdRoute.Annotations).To(HaveKeyWithValue("haproxy.router.openshift.io/timeout", "120s"))
}

func TestRawOCPRoute_Update(t *testing.T) {
	g := NewGomegaWithT(t)
	s := ocpTestScheme()

	svc := makePredictorService(defaultPorts())
	route := admittedRoute("host.example.com")
	route.Spec.TLS.Termination = routev1.TLSTerminationPassthrough

	isvc := makeISVC(
		map[string]string{constants.NetworkVisibility: constants.ODHRouteEnabled},
		nil,
	)
	rec := newOCPReconciler(s, svc, isvc, route)

	result, err := rec.Reconcile(t.Context(), isvc)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(result.Requeue).To(BeFalse())

	updatedRoute := &routev1.Route{}
	err = rec.client.Get(t.Context(), types.NamespacedName{Name: testIsvcName, Namespace: testNamespace}, updatedRoute)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(updatedRoute.Spec.TLS.Termination).To(Equal(routev1.TLSTerminationEdge), "Route should be updated to Edge TLS")
}
