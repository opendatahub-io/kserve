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
