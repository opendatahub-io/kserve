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
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
	"github.com/kserve/kserve/pkg/controller/llmisvc/fixture"
)

var _ = Describe("LLMInferenceService API validation", func() {
	var (
		ns     *corev1.Namespace
		nsName string
	)
	BeforeEach(func(ctx SpecContext) {
		nsName = fmt.Sprintf("test-llmisvc-api-validation-%d", time.Now().UnixNano())

		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: nsName,
			},
		}
		Expect(envTest.Client.Create(ctx, ns)).To(Succeed())

		DeferCleanup(func() {
			ns := ns
			envTest.DeleteAll(ns)
		})
	})
	Context("Integer value validation", func() {
		It("should reject LLMInferenceService with negative workload replicas", func(ctx SpecContext) {
			// given
			llmSvc := fixture.LLMInferenceService("test-negative-replicas",
				fixture.InNamespace[*v1alpha1.LLMInferenceService](nsName),
				fixture.WithModelURI("hf://facebook/opt-125m"),
				fixture.WithDeploymentReplicas(-1),
			)

			// when
			errValidation := envTest.Client.Create(ctx, llmSvc)

			// then
			Expect(errValidation).To(HaveOccurred(), "Expected the Create call to fail due to a validation error, but it succeeded")
			Expect(apierrors.IsInvalid(errValidation)).To(BeTrue(), fmt.Sprintf("Expected an invalid API error, but got a different type: %v", errValidation))
			Expect(errValidation.Error()).To(ContainSubstring("spec.replicas in body should be greater than or equal to 0"))
		})

		It("should reject LLMInferenceService with zero tensor parallelism", func(ctx SpecContext) {
			// given
			llmSvc := fixture.LLMInferenceService("test-zero-int-parallelism",
				fixture.InNamespace[*v1alpha1.LLMInferenceService](nsName),
				fixture.WithModelURI("hf://facebook/opt-125m"),
				fixture.WithParallelism(fixture.ParallelismSpec(
					fixture.WithTensorParallelism(0),
				)),
			)

			// when
			errValidation := envTest.Client.Create(ctx, llmSvc)

			// then
			Expect(errValidation).To(HaveOccurred(), "Expected the Create call to fail due to a validation error, but it succeeded")
			Expect(apierrors.IsInvalid(errValidation)).To(BeTrue(), fmt.Sprintf("Expected an invalid API error, but got a different type: %v", errValidation))
			Expect(errValidation.Error()).To(ContainSubstring("spec.parallelism.tensor in body should be greater than or equal to 1"))
		})

		It("should reject LLMInferenceService with zero pipeline parallelism", func(ctx SpecContext) {
			// given
			llmSvc := fixture.LLMInferenceService("test-zero-int-pipeline",
				fixture.InNamespace[*v1alpha1.LLMInferenceService](nsName),
				fixture.WithModelURI("hf://facebook/opt-125m"),
				fixture.WithParallelism(fixture.ParallelismSpec(
					fixture.WithPipelineParallelism(0),
				)),
			)

			// when
			errValidation := envTest.Client.Create(ctx, llmSvc)

			// then
			Expect(errValidation).To(HaveOccurred(), "Expected the Create call to fail due to a validation error, but it succeeded")
			Expect(apierrors.IsInvalid(errValidation)).To(BeTrue(), fmt.Sprintf("Expected an invalid API error, but got a different type: %v", errValidation))
			Expect(errValidation.Error()).To(ContainSubstring("spec.parallelism.pipeline in body should be greater than or equal to 1"))
		})

		It("should reject LLMInferenceService with zero data parallelism", func(ctx SpecContext) {
			// given
			llmSvc := fixture.LLMInferenceService("test-zero-data",
				fixture.InNamespace[*v1alpha1.LLMInferenceService](nsName),
				fixture.WithModelURI("hf://facebook/opt-125m"),
				fixture.WithParallelism(fixture.ParallelismSpec(
					fixture.WithDataParallelism(0),
					fixture.WithDataLocalParallelism(1),
				)),
			)

			// when
			errValidation := envTest.Client.Create(ctx, llmSvc)

			// then
			Expect(errValidation).To(HaveOccurred(), "Expected the Create call to fail due to a validation error, but it succeeded")
			Expect(apierrors.IsInvalid(errValidation)).To(BeTrue(), fmt.Sprintf("Expected an invalid API error, but got a different type: %v", errValidation))
			Expect(errValidation.Error()).To(ContainSubstring("spec.parallelism.data in body should be greater than or equal to 1"))
		})

		It("should reject LLMInferenceService with zero dataLocal parallelism", func(ctx SpecContext) {
			// given
			llmSvc := fixture.LLMInferenceService("test-zero-datalocal",
				fixture.InNamespace[*v1alpha1.LLMInferenceService](nsName),
				fixture.WithModelURI("hf://facebook/opt-125m"),
				fixture.WithParallelism(fixture.ParallelismSpec(
					fixture.WithDataParallelism(4),
					fixture.WithDataLocalParallelism(0),
				)),
			)

			// when
			errValidation := envTest.Client.Create(ctx, llmSvc)

			// then
			Expect(errValidation).To(HaveOccurred(), "Expected the Create call to fail due to a validation error, but it succeeded")
			Expect(apierrors.IsInvalid(errValidation)).To(BeTrue(), fmt.Sprintf("Expected an invalid API error, but got a different type: %v", errValidation))
			Expect(errValidation.Error()).To(ContainSubstring("spec.parallelism.dataLocal in body should be greater than or equal to 1"))
		})

		It("should reject LLMInferenceService with zero data parallelism RPC Port", func(ctx SpecContext) {
			// given
			llmSvc := fixture.LLMInferenceService("test-zero-data-rpc-port",
				fixture.InNamespace[*v1alpha1.LLMInferenceService](nsName),
				fixture.WithModelURI("hf://facebook/opt-125m"),
				fixture.WithParallelism(fixture.ParallelismSpec(
					fixture.WithDataRPCPort(0),
				)),
			)

			// when
			errValidation := envTest.Client.Create(ctx, llmSvc)

			// then
			Expect(errValidation).To(HaveOccurred(), "Expected the Create call to fail due to a validation error, but it succeeded")
			Expect(apierrors.IsInvalid(errValidation)).To(BeTrue(), fmt.Sprintf("Expected an invalid API error, but got a different type: %v", errValidation))
			Expect(errValidation.Error()).To(ContainSubstring("spec.parallelism.dataRPCPort in body should be greater than or equal to 1"))
		})

		It("should reject LLMInferenceService with too large data parallelism RPC Port", func(ctx SpecContext) {
			// given
			llmSvc := fixture.LLMInferenceService("test-max-data-rpc-port-exceeded",
				fixture.InNamespace[*v1alpha1.LLMInferenceService](nsName),
				fixture.WithModelURI("hf://facebook/opt-125m"),
				fixture.WithParallelism(fixture.ParallelismSpec(
					fixture.WithDataRPCPort(99999),
				)),
			)

			// when
			errValidation := envTest.Client.Create(ctx, llmSvc)

			// then
			Expect(errValidation).To(HaveOccurred(), "Expected the Create call to fail due to a validation error, but it succeeded")
			Expect(apierrors.IsInvalid(errValidation)).To(BeTrue(), fmt.Sprintf("Expected an invalid API error, but got a different type: %v", errValidation))
			Expect(errValidation.Error()).To(ContainSubstring("spec.parallelism.dataRPCPort in body should be less than or equal to 65535"))
		})
	})
})
