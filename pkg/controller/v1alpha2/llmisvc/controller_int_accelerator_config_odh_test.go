//go:build distro

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

package llmisvc_test

import (
	"context"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/yaml"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
	. "github.com/kserve/kserve/pkg/controller/v1alpha2/llmisvc/fixture"
	kservetesting "github.com/kserve/kserve/pkg/testing"
)

var _ = Describe("LLMInferenceService CPU accelerator config ODH", func() {
	// Loads the shipped ODH CPU accelerator preset manifest so the test exercises
	// the exact content delivered by the overlay (modulo build-time image and
	// runtime-version substitution from params.env).
	loadCPUAcceleratorConfig := func(namespace string) *v1alpha2.LLMInferenceServiceConfig {
		path := filepath.Join(kservetesting.ProjectRoot(),
			"config", "overlays", "odh", "accelerators", "cpu-config-llm-template.yaml")
		data, err := os.ReadFile(filepath.Clean(path))
		Expect(err).ToNot(HaveOccurred())

		config := &v1alpha2.LLMInferenceServiceConfig{}
		Expect(yaml.Unmarshal(data, config)).To(Succeed())
		config.Namespace = namespace
		return config
	}

	Context("CPU accelerator preset via baseRefs", func() {
		It("should apply preset image, env defaults, and arch pin to the workload", func(ctx SpecContext) {
			// given
			svcName := "test-llm-cpu-accel-preset"
			testNs := NewTestNamespace(ctx, envTest)

			config := loadCPUAcceleratorConfig(testNs.Name)
			Expect(envTest.Client.Create(ctx, config)).To(Succeed())

			llmSvc := LLMInferenceService(svcName,
				InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
				WithModelURI("hf://facebook/opt-125m"),
				WithModelName("facebook/opt-125m"),
				WithBaseRefs(corev1.LocalObjectReference{Name: config.Name}),
			)

			// when
			Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
			defer func() {
				testNs.DeleteAndWait(ctx, llmSvc)
			}()

			// then - the workload deployment carries the preset content
			deployment := &appsv1.Deployment{}
			Eventually(func(g Gomega, ctx context.Context) error {
				return envTest.Get(ctx, types.NamespacedName{
					Name:      svcName + "-kserve",
					Namespace: testNs.Name,
				}, deployment)
			}).WithContext(ctx).Should(Succeed())

			presetImage := config.Spec.Template.Containers[0].Image
			Expect(deployment.Spec.Template.Spec.Containers).To(ContainElement(And(
				HaveField("Name", "main"),
				HaveField("Image", presetImage),
				HaveField("Env", ContainElements(
					And(HaveField("Name", "VLLM_CPU_KVCACHE_SPACE"), HaveField("Value", "4")),
					And(HaveField("Name", "OMP_NUM_THREADS"), HaveField("Value", "4")),
				)),
			)))
			Expect(deployment.Spec.Template.Spec.NodeSelector).To(
				HaveKeyWithValue("kubernetes.io/arch", "amd64"))
		})

		It("should let a user-specified image override the preset image", func(ctx SpecContext) {
			// given
			svcName := "test-llm-cpu-accel-precedence"
			testNs := NewTestNamespace(ctx, envTest)

			config := loadCPUAcceleratorConfig(testNs.Name)
			Expect(envTest.Client.Create(ctx, config)).To(Succeed())

			userImage := "quay.io/example/custom-vllm-cpu:v1"
			llmSvc := LLMInferenceService(svcName,
				InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
				WithModelURI("hf://facebook/opt-125m"),
				WithModelName("facebook/opt-125m"),
				WithBaseRefs(corev1.LocalObjectReference{Name: config.Name}),
				WithTemplate(&corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "main",
						Image: userImage,
					}},
				}),
			)

			// when
			Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
			defer func() {
				testNs.DeleteAndWait(ctx, llmSvc)
			}()

			// then - the user's image wins, preset env defaults still merge in
			deployment := &appsv1.Deployment{}
			Eventually(func(g Gomega, ctx context.Context) error {
				return envTest.Get(ctx, types.NamespacedName{
					Name:      svcName + "-kserve",
					Namespace: testNs.Name,
				}, deployment)
			}).WithContext(ctx).Should(Succeed())

			Expect(deployment.Spec.Template.Spec.Containers).To(ContainElement(And(
				HaveField("Name", "main"),
				HaveField("Image", userImage),
				HaveField("Env", ContainElements(
					And(HaveField("Name", "VLLM_CPU_KVCACHE_SPACE"), HaveField("Value", "4")),
					And(HaveField("Name", "OMP_NUM_THREADS"), HaveField("Value", "4")),
				)),
			)))
		})
	})
})
