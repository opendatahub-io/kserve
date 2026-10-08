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
	"fmt"
	"testing"

	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/kserve/kserve/pkg/apis/serving/v1beta1"
	"github.com/kserve/kserve/pkg/constants"
)

// TestRawIngressReconciler_URLAndAddress_TransformerAuth verifies that when auth is
// enabled and the transformer is the entry point (cluster-local / no Route), both
// Status.URL and Status.Address advertise the transformer's native HTTPS endpoint
// (https on TransformerHTTPSPort), matching the pod/Service which serve HTTPS/8443.
func TestRawIngressReconciler_URLAndAddress_TransformerAuth(t *testing.T) {
	g := NewGomegaWithT(t)
	s := testScheme()

	tHost := transformerHost()
	svcName := constants.TransformerServiceName(testIsvcName)
	svc := makeService(svcName, false)
	cl := fake.NewClientBuilder().WithScheme(s).WithObjects(svc).Build()

	isvc := &v1beta1.InferenceService{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testIsvcName,
			Namespace: testNamespace,
			Annotations: map[string]string{
				constants.ODHKserveRawAuth: "true",
			},
		},
		Spec: v1beta1.InferenceServiceSpec{
			Predictor:   v1beta1.PredictorSpec{},
			Transformer: &v1beta1.TransformerSpec{},
		},
		Status: v1beta1.InferenceServiceStatus{},
	}

	ingressConfig := &v1beta1.IngressConfig{
		DisableIngressCreation: true,
		UrlScheme:              "http",
		IngressDomain:          "example.com",
		DomainTemplate:         "{{.Name}}-{{.Namespace}}.{{.IngressDomain}}",
	}
	isvcConfig := &v1beta1.InferenceServicesConfig{}

	reconciler, err := NewRawIngressReconciler(cl, s, ingressConfig, isvcConfig)
	g.Expect(err).ToNot(HaveOccurred())

	result, err := reconciler.Reconcile(t.Context(), isvc)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(result).To(Equal(ctrl.Result{}))

	wantHostPort := fmt.Sprintf("%s:%d", tHost, constants.TransformerHTTPSPort)

	g.Expect(isvc.Status.URL).ToNot(BeNil())
	g.Expect(isvc.Status.URL.Scheme).To(Equal("https"), "authenticated transformer URL should be https")
	g.Expect(isvc.Status.URL.Host).To(Equal(wantHostPort), "authenticated transformer URL should advertise the HTTPS port")

	g.Expect(isvc.Status.Address).ToNot(BeNil())
	g.Expect(isvc.Status.Address.URL.Scheme).To(Equal("https"), "authenticated transformer address should be https")
	g.Expect(isvc.Status.Address.URL.Host).To(Equal(wantHostPort), "authenticated transformer address should advertise the HTTPS port")
}
