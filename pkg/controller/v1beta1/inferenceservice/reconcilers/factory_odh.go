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
	"fmt"

	routev1 "github.com/openshift/api/route/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kserve/kserve/pkg/controller/v1beta1/inferenceservice/reconcilers/ingress"
)

// resolvePlatformIngressReconciler exposes Standard InferenceServices through OpenShift Routes
// unless the Gateway API is enabled or the cluster does not serve Routes.
func resolvePlatformIngressReconciler(params IngressReconcilerParams) (IngressReconciler, error) {
	if params.IngressConfig.EnableGatewayAPI {
		return nil, nil
	}
	available, err := routeAPIAvailable(params.Client)
	if err != nil {
		return nil, fmt.Errorf("failed to discover the OpenShift Route API: %w", err)
	}
	if !available {
		return nil, nil
	}
	routeReconciler, err := ingress.NewRawRouteReconciler(params.Client, params.Scheme, params.IngressConfig, params.IsvcConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create route reconciler: %w", err)
	}
	return routeReconciler, nil
}

// routeAPIAvailable reports whether the cluster serves route.openshift.io/v1 Routes. The client
// RESTMapper caches the mapping once found; while the API is missing every call runs discovery.
func routeAPIAvailable(c client.Client) (bool, error) {
	_, err := c.RESTMapper().RESTMapping(schema.GroupKind{Group: routev1.GroupName, Kind: "Route"}, routev1.GroupVersion.Version)
	if meta.IsNoMatchError(err) {
		return false, nil
	}
	return err == nil, err
}
