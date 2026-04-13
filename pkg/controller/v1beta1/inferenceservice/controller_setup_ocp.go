//go:build distro

/*
Copyright 2023 The KServe Authors.

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

package inferenceservice

import (
	"errors"
	"fmt"

	routev1 "github.com/openshift/api/route/v1"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/kserve/kserve/pkg/utils"
)

var setupLog = logf.Log.WithName("ControllerSetup")

func extendControllerSetup(mgr manager.Manager, b *builder.Builder) error {
	if err := routev1.AddToScheme(mgr.GetScheme()); err != nil {
		return fmt.Errorf("failed to add OpenShift Route API to scheme: %w", err)
	}
	routeGV := routev1.GroupVersion
	ok, err := utils.IsCrdAvailable(mgr.GetConfig(), routeGV.String(), "Route")
	if err != nil {
		setupLog.Error(err, "Failed to check Route CRD availability")
		return fmt.Errorf("failed to check Route CRD availability: %w", err)
	}
	if !ok {
		setupLog.Error(nil, "Route CRD not found — distro build requires OpenShift Route API")
		return errors.New("Route CRD not available: distro build requires the OpenShift Route API")
	}
	b.Owns(&routev1.Route{})
	return nil
}
