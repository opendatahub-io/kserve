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

package main

import (
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/kserve/kserve/pkg/constants"
	"github.com/kserve/kserve/pkg/controller/v1alpha1/localmodel/distro"
	controllerutils "github.com/kserve/kserve/pkg/controller/v1alpha1/utils"
)

func setupDistroControllers(mgr ctrl.Manager) error {
	return (&distro.CABundleReconciler{
		Client:          mgr.GetClient(),
		Reader:          mgr.GetAPIReader(),
		Scheme:          mgr.GetScheme(),
		SourceNamespace: controllerutils.InferenceServiceConfigNamespace(),
		TargetNamespace: constants.KServeNamespace,
	}).SetupWithManager(mgr)
}
