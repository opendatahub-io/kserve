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

package reconcilers

import (
	"testing"

	routev1 "github.com/openshift/api/route/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/kserve/kserve/pkg/apis/serving/v1beta1"
	"github.com/kserve/kserve/pkg/constants"
	"github.com/kserve/kserve/pkg/controller/v1beta1/inferenceservice/reconcilers/ingress"
)

func TestCreateIngressReconcilerSelectsRoutes(t *testing.T) {
	tests := map[string]struct {
		mode             constants.DeploymentModeType
		enableGatewayAPI bool
		routeAPI         bool
		want             IngressReconciler
	}{
		"Routes when the cluster serves them": {
			mode:     constants.Standard,
			routeAPI: true,
			want:     &ingress.RawRouteReconciler{},
		},
		"Routes in legacy raw deployment mode": {
			mode:     constants.LegacyRawDeployment,
			routeAPI: true,
			want:     &ingress.RawRouteReconciler{},
		},
		"Gateway API takes precedence over Routes": {
			mode:             constants.Standard,
			enableGatewayAPI: true,
			routeAPI:         true,
			want:             &ingress.RawHTTPRouteReconciler{},
		},
		"Kubernetes Ingress without the Route API": {
			mode: constants.Standard,
			want: &ingress.RawIngressReconciler{},
		},
		"Knative mode is left to upstream": {
			mode:     constants.Knative,
			routeAPI: true,
			want:     &ingress.IngressReconciler{},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, v1beta1.AddToScheme(scheme))
			require.NoError(t, corev1.AddToScheme(scheme))
			require.NoError(t, routev1.AddToScheme(scheme))
			// The RESTMapper stands in for API discovery.
			mapper := meta.NewDefaultRESTMapper(nil)
			if tc.routeAPI {
				mapper.Add(routev1.GroupVersion.WithKind("Route"), meta.RESTScopeNamespace)
			}
			params := IngressReconcilerParams{
				Client:        fakeclient.NewClientBuilder().WithScheme(scheme).WithRESTMapper(mapper).Build(),
				Clientset:     fake.NewSimpleClientset(),
				Scheme:        scheme,
				IngressConfig: &v1beta1.IngressConfig{EnableGatewayAPI: tc.enableGatewayAPI},
				IsvcConfig:    &v1beta1.InferenceServicesConfig{},
			}

			rec, err := NewReconcilerFactory().CreateIngressReconciler(tc.mode, params)

			require.NoError(t, err)
			assert.IsType(t, tc.want, rec)
		})
	}
}
