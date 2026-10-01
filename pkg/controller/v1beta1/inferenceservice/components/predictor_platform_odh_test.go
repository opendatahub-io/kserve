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
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	"knative.dev/pkg/apis"

	"github.com/kserve/kserve/pkg/apis/serving/v1beta1"
	"github.com/kserve/kserve/pkg/controller/v1beta1/inferenceservice/reconcilers"
	"github.com/kserve/kserve/pkg/controller/v1beta1/inferenceservice/reconcilers/deployment"
)

type conditionsWorkload struct {
	reconcilers.WorkloadReconciler
	conditions []apis.Condition
}

func (w *conditionsWorkload) PlatformConditions() []apis.Condition {
	return w.conditions
}

func TestPropagatePlatformWorkloadStatus(t *testing.T) {
	authProxyPreserved := apis.Condition{
		Type:    v1beta1.LatestDeploymentReady,
		Status:  corev1.ConditionFalse,
		Reason:  deployment.AuthProxyPreservedReason,
		Message: "Preserving existing auth proxy container to avoid pod restart",
	}
	otherLatestDeploymentReady := apis.Condition{
		Type:   v1beta1.LatestDeploymentReady,
		Status: corev1.ConditionFalse,
		Reason: "ProgressDeadlineExceeded",
	}

	tests := []struct {
		name     string
		existing *apis.Condition
		workload reconcilers.WorkloadReconciler
		want     *apis.Condition
	}{
		{
			name:     "recorded condition is set",
			workload: &conditionsWorkload{conditions: []apis.Condition{authProxyPreserved}},
			want:     &authProxyPreserved,
		},
		{
			name:     "stale AuthProxyPreserved condition is cleared",
			existing: &authProxyPreserved,
			workload: &conditionsWorkload{},
		},
		{
			name:     "LatestDeploymentReady with another reason is kept",
			existing: &otherLatestDeploymentReady,
			workload: &conditionsWorkload{},
			want:     &otherLatestDeploymentReady,
		},
		{
			name:     "workload without platform conditions clears a stale AuthProxyPreserved condition",
			existing: &authProxyPreserved,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// given
			isvc := &v1beta1.InferenceService{}
			if tt.existing != nil {
				isvc.Status.SetCondition(tt.existing.Type, tt.existing.DeepCopy())
			}

			// when
			propagatePlatformWorkloadStatus(isvc, tt.workload)

			// then
			got := isvc.Status.GetCondition(v1beta1.LatestDeploymentReady)
			if tt.want == nil {
				assert.Nil(t, got)
				return
			}
			if assert.NotNil(t, got) {
				assert.Equal(t, tt.want.Status, got.Status)
				assert.Equal(t, tt.want.Reason, got.Reason)
				assert.Equal(t, tt.want.Message, got.Message)
			}
		})
	}
}
