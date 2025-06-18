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

package v1alpha1

import (
	"knative.dev/pkg/apis"
	duckv1 "knative.dev/pkg/apis/duck/v1"
)

const (
	WorkloadReady apis.ConditionType = "WorkloadsReady"
	RouterReady   apis.ConditionType = "RouterReady"
)

var llmInferenceServiceCondSet = apis.NewLivingConditionSet(
	WorkloadReady,
	RouterReady,
)

func (in *LLMInferenceService) GetStatus() *duckv1.Status {
	return &in.Status.Status
}

func (in *LLMInferenceService) GetConditionSet() apis.ConditionSet {
	return llmInferenceServiceCondSet
}

func (in *LLMInferenceService) MarkWorkloadNotReady(reason, messageFormat string, messageA ...interface{}) {
	in.GetConditionSet().Manage(in.GetStatus()).MarkFalse(WorkloadReady, reason, messageFormat, messageA...)
}

func (in *LLMInferenceService) MarkWorkloadReady() {
	in.GetConditionSet().Manage(in.GetStatus()).MarkTrue(WorkloadReady)
}
