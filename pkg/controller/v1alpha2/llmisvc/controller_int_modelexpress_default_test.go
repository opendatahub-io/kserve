//go:build !distro

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

package llmisvc_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	"github.com/kserve/kserve/pkg/constants"
	. "github.com/kserve/kserve/pkg/controller/v1alpha2/llmisvc/fixture"
)

var _ = Describe("LLMInferenceService Controller - ModelExpress CA bundle (upstream)", func() {
	It("trusts system roots for an https server without a CA bundle annotation", func(ctx SpecContext) {
		svcName := "test-mx-ca-upstream"
		testNs := NewTestNamespace(ctx, envTest)

		llmSvc := modelExpressService(svcName, testNs.Name, "hf://org/model", map[string]string{
			constants.ModelExpressModeAnnotationKey:    "native",
			constants.ModelExpressAddressAnnotationKey: mxAddress,
		})
		Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
		defer func() { testNs.DeleteAndWait(ctx, llmSvc) }()

		expectModelExpressCondition(ctx, llmSvc, corev1.ConditionTrue, "")
		for _, name := range []string{svcName + "-kserve", svcName + "-kserve-prefill"} {
			d := getDeployment(ctx, name, testNs.Name)
			expectNoModelExpressCA(&d.Spec.Template.Spec, name)
		}
	})
})
