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

package llmisvc

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"knative.dev/pkg/kmeta"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
	"github.com/kserve/kserve/pkg/constants"
	kservescheme "github.com/kserve/kserve/pkg/scheme"
	"github.com/kserve/kserve/pkg/utils"
)

// Not parallel: discovery results live in a process-global cache, which goes back
// to what the envtest suites in this package discover once the test is done.
func TestEnsureGatewayPreconditionsDeletesRouteWithoutAuthPolicy(t *testing.T) {
	groupVersion := authPolicyGVK.GroupVersion().String()
	utils.SetAvailableResourcesForApi(groupVersion, &metav1.APIResourceList{GroupVersion: groupVersion})
	t.Cleanup(func() {
		utils.SetAvailableResourcesForApi(groupVersion, &metav1.APIResourceList{
			GroupVersion: groupVersion,
			APIResources: []metav1.APIResource{{Kind: authPolicyGVK.Kind}},
		})
	})

	for _, tt := range []struct {
		name        string
		annotations map[string]string
	}{
		{name: "a route that renders"},
		{name: "a route that fails to render", annotations: map[string]string{AnnotationRouteRuleNaming: "unsupported"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// given a service with auth enabled and a live route, on a cluster without the AuthPolicy CRD
			scheme := runtime.NewScheme()
			require.NoError(t, kservescheme.AddKServeAPIs(scheme))
			require.NoError(t, kservescheme.AddCoreKubernetesAPIs(scheme))
			require.NoError(t, kservescheme.AddGatewayAPIs(scheme))

			svc := &v1alpha2.LLMInferenceService{
				ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "ns", UID: "svc-uid"},
				Spec: v1alpha2.LLMInferenceServiceSpec{
					WorkloadSpec: v1alpha2.WorkloadSpec{Annotations: tt.annotations},
					Router: &v1alpha2.RouterSpec{Route: &v1alpha2.GatewayRoutesSpec{HTTP: &v1alpha2.HTTPRouteSpec{
						Spec: &gwapiv1.HTTPRouteSpec{Rules: []gwapiv1.HTTPRouteRule{{Name: ptr.To(gwapiv1.SectionName("rule"))}}},
					}}},
				},
			}
			route := &gwapiv1.HTTPRoute{ObjectMeta: metav1.ObjectMeta{
				Name:            kmeta.ChildName(svc.Name, "-kserve-route"),
				Namespace:       svc.Namespace,
				OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(svc, v1alpha2.LLMInferenceServiceGVK)},
			}}
			config := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: constants.InferenceServiceConfigMapName, Namespace: constants.KServeNamespace},
				Data: map[string]string{
					"ingress":            `{"enableGatewayApi": true, "kserveIngressGateway": "kserve/kserve-ingress-gateway", "ingressGateway": "knative-serving/knative-ingress-gateway"}`,
					"storageInitializer": `{"memoryRequest": "100Mi", "memoryLimit": "1Gi", "cpuRequest": "100m", "cpuLimit": "1", "cpuModelcar": "10m", "memoryModelcar": "15Mi"}`,
					"credentials":        `{"s3": {"s3AccessKeyIDName": "AWS_ACCESS_KEY_ID", "s3SecretAccessKeyName": "AWS_SECRET_ACCESS_KEY"}}`,
				},
			}
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(svc, route, config).Build()
			r := &LLMISVCReconciler{Client: c, EventRecorder: record.NewFakeRecorder(10)}

			// when the preconditions are checked
			err := r.ensureGatewayPreconditions(t.Context(), svc)

			// then the check fails and the route is gone
			require.ErrorIs(t, err, ErrPreconditionNotMet)
			err = c.Get(t.Context(), client.ObjectKeyFromObject(route), &gwapiv1.HTTPRoute{})
			assert.True(t, apierrors.IsNotFound(err), "the route must not stay up without its AuthPolicy, got: %v", err)
		})
	}
}
