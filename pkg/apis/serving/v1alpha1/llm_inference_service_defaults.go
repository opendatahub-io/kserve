package v1alpha1

import (
	"context"

	"k8s.io/utils/ptr"
	"knative.dev/pkg/apis"
)

var (
	_ apis.Defaultable = &LLMInferenceService{}
)

func (in *LLMInferenceService) SetDefaults(_ context.Context) {
	if in.Spec.Model.Name == nil || *in.Spec.Model.Name == "" {
		in.Spec.Model.Name = ptr.To(in.GetName())
	}
}
