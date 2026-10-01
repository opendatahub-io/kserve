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

package inferencegraph

import (
	osv1 "github.com/openshift/api/route/v1"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/kserve/kserve/pkg/utils"
)

// extendControllerSetup watches the OpenShift Routes that expose raw deployment graphs,
// when the Route CRD is installed.
func (r *InferenceGraphReconciler) extendControllerSetup(_ manager.Manager, b *builder.Builder) error {
	routeFound, err := utils.IsCrdAvailable(r.ClientConfig, osv1.GroupVersion.String(), "Route")
	if err != nil {
		return err
	}

	if routeFound {
		b.Owns(&osv1.Route{})
	} else {
		r.Log.Info("The InferenceGraph controller won't watch route.openshift.io/v1/Route resources because the CRD is not available.")
	}

	return nil
}
