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

package llmisvc_test

import (
	"context"

	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
)

func sharedTestFixture(ctx context.Context, c client.Client) {
	Expect(envTest.Client.Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "kserve",
		},
	})).To(Succeed())

	templateConfig := &v1alpha1.LLMInferenceServiceConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kserve-config-llm-template",
			Namespace: "kserve",
		},
		Spec: v1alpha1.LLMInferenceServiceSpec{
			WorkloadSpec: v1alpha1.WorkloadSpec{
				Template: &corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "model",
							Image: "test-model:latest",
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									"nvidia.com/gpu": resource.MustParse("1"),
								},
							},
						},
					},
				},
			},
		},
	}
	Expect(c.Create(ctx, templateConfig)).To(Succeed())
}
