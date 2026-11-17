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

package validation

import (
	"context"
	"net/http"

	admissionv1 "k8s.io/api/admission/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/util/json"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

const RemovedMessage = "llm-d Technology Preview has been removed; new LLMInferenceServices and LLMInferenceServiceConfigs cannot be created"

const RemovedSpecChangeMessage = "llm-d Technology Preview has been removed; existing LLMInferenceService and LLMInferenceServiceConfig specs cannot be changed"

// RemovedFeatureValidator serves the existing fail-closed webhooks without
// invoking provisioning-era validation or reading configs.
type RemovedFeatureValidator struct{}

func (*RemovedFeatureValidator) Handle(_ context.Context, req admission.Request) admission.Response {
	if req.Operation == admissionv1.Create {
		return admission.Denied(RemovedMessage)
	}
	if req.Operation == admissionv1.Update && req.SubResource == "" {
		var oldObject, newObject map[string]interface{}
		if err := json.Unmarshal(req.OldObject.Raw, &oldObject); err != nil {
			return admission.Errored(http.StatusBadRequest, err)
		}
		if err := json.Unmarshal(req.Object.Raw, &newObject); err != nil {
			return admission.Errored(http.StatusBadRequest, err)
		}
		if !equality.Semantic.DeepEqual(oldObject["spec"], newObject["spec"]) {
			return admission.Denied(RemovedSpecChangeMessage)
		}
	}
	return admission.Allowed("")
}
