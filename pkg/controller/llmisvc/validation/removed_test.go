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
	"testing"

	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

func TestRemovedFeatureValidator(t *testing.T) {
	validator := &RemovedFeatureValidator{}
	for _, resource := range []string{"llminferenceservices", "llminferenceserviceconfigs"} {
		for _, namespace := range []string{"models", "another-namespace"} {
			for _, test := range []struct {
				name        string
				operation   admissionv1.Operation
				subresource string
				oldObject   string
				newObject   string
				allowed     bool
				message     string
			}{
				{name: "create", operation: admissionv1.Create, message: RemovedMessage},
				{name: "delete", operation: admissionv1.Delete, allowed: true},
				{name: "status", operation: admissionv1.Update, subresource: "status", allowed: true},
				{
					name: "unchanged-spec", operation: admissionv1.Update, allowed: true,
					oldObject: `{"spec":{"replicas":1,"model":{"uri":"hf://existing"}}}`,
					newObject: `{"spec":{"model":{"uri":"hf://existing"},"replicas":1}}`,
				},
				{
					name: "metadata-and-finalizers", operation: admissionv1.Update, allowed: true,
					oldObject: `{"metadata":{"finalizers":["serving.kserve.io/llmisvc-finalizer"]},"spec":{"replicas":1}}`,
					newObject: `{"metadata":{"labels":{"customer":"value"},"annotations":{"note":"retired"},"finalizers":[]},"spec":{"replicas":1}}`,
				},
				{name: "empty-spec-unchanged", operation: admissionv1.Update, allowed: true, oldObject: `{}`, newObject: `{}`},
				{
					name: "replicas-changed", operation: admissionv1.Update, message: RemovedSpecChangeMessage,
					oldObject: `{"spec":{"replicas":1}}`, newObject: `{"spec":{"replicas":2}}`,
				},
				{
					name: "model-changed", operation: admissionv1.Update, message: RemovedSpecChangeMessage,
					oldObject: `{"spec":{"model":{"uri":"hf://existing"}}}`, newObject: `{"spec":{"model":{"uri":"hf://edited"}}}`,
				},
				{
					name: "spec-removed", operation: admissionv1.Update, message: RemovedSpecChangeMessage,
					oldObject: `{"spec":{"replicas":1}}`, newObject: `{}`,
				},
				{
					name: "unknown-field-changed", operation: admissionv1.Update, message: RemovedSpecChangeMessage,
					oldObject: `{"spec":{"futureField":"old"}}`, newObject: `{"spec":{"futureField":"new"}}`,
				},
				{
					name: "large-integer-changed", operation: admissionv1.Update, message: RemovedSpecChangeMessage,
					oldObject: `{"spec":{"futureField":9007199254740992}}`, newObject: `{"spec":{"futureField":9007199254740993}}`,
				},
				{name: "invalid-old-object", operation: admissionv1.Update, oldObject: `{`, newObject: `{"spec":{}}`},
				{name: "invalid-new-object", operation: admissionv1.Update, oldObject: `{"spec":{}}`, newObject: `{`},
			} {
				t.Run(resource+"/"+namespace+"/"+test.name, func(t *testing.T) {
					response := validator.Handle(t.Context(), admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
						Operation: test.operation, Namespace: namespace, SubResource: test.subresource,
						Resource:  metav1.GroupVersionResource{Group: "serving.kserve.io", Version: "v1alpha1", Resource: resource},
						Object:    runtime.RawExtension{Raw: []byte(test.newObject)},
						OldObject: runtime.RawExtension{Raw: []byte(test.oldObject)},
					}})
					require.Equal(t, test.allowed, response.Allowed)
					if test.message != "" {
						require.Equal(t, test.message, response.Result.Message)
					}
					require.Empty(t, response.Warnings)
				})
			}
		}
	}
}
