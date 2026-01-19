/*
Copyright 2021 The KServe Authors.

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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kserve/kserve/pkg/constants"
)

func NewCacheOptions() (cache.Options, error) {
	// Build a label selector that matches pods with the InferenceService label (any value).
	isvcPodLabelReq, err := labels.NewRequirement(constants.InferenceServicePodLabelKey, selection.Exists, nil)
	if err != nil {
		return cache.Options{}, err
	}
	isvcPodLabelSelector := labels.NewSelector().Add(*isvcPodLabelReq)

	return cache.Options{
		ByObject: map[client.Object]cache.ByObject{
			&corev1.Pod{}: {
				Label: isvcPodLabelSelector,
			},
		},
	}, nil
}

func NewCacheOptionsWithLLMSvc(llmSvcLabelSelector labels.Selector, caSecretNamespace, caSecretName string) (cache.Options, error) {
	// Start with base ISVC pod watch cache
	opts, err := NewCacheOptions()
	if err != nil {
		return cache.Options{}, err
	}

	// Add LLMInferenceService-specific secret cache
	opts.ByObject[&corev1.Secret{}] = cache.ByObject{
		Namespaces: map[string]cache.Config{
			cache.AllNamespaces: {
				LabelSelector: llmSvcLabelSelector,
			},
			caSecretNamespace: {
				FieldSelector: fields.SelectorFromSet(map[string]string{
					"metadata.name": caSecretName,
				}),
			},
		},
	}

	return opts, nil
}
