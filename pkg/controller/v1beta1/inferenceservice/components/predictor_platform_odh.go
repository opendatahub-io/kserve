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

package components

import (
	"knative.dev/pkg/apis"

	"github.com/kserve/kserve/pkg/apis/serving/v1beta1"
	"github.com/kserve/kserve/pkg/controller/v1beta1/inferenceservice/reconcilers"
	"github.com/kserve/kserve/pkg/controller/v1beta1/inferenceservice/reconcilers/deployment"
)

// propagatePlatformWorkloadStatus sets the conditions recorded by the platform
// customization of the predictor workload. When none is recorded, it clears an
// AuthProxyPreserved condition left by an earlier reconcile.
func propagatePlatformWorkloadStatus(isvc *v1beta1.InferenceService, workload reconcilers.WorkloadReconciler) {
	var conditions []apis.Condition
	if w, ok := workload.(interface{ PlatformConditions() []apis.Condition }); ok {
		conditions = w.PlatformConditions()
	}

	if len(conditions) == 0 {
		if existing := isvc.Status.GetCondition(v1beta1.LatestDeploymentReady); existing != nil && existing.Reason == deployment.AuthProxyPreservedReason {
			isvc.Status.ClearCondition(v1beta1.LatestDeploymentReady)
		}
		return
	}

	for i := range conditions {
		isvc.Status.SetCondition(conditions[i].Type, &conditions[i])
	}
}
