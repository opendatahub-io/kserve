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
	"testing"

	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gatewayapi "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
	"github.com/kserve/kserve/pkg/controller/llmisvc"
	. "github.com/kserve/kserve/pkg/controller/llmisvc/fixture"
)

func TestGatewayConditionsEvaluation(t *testing.T) {
	tests := []struct {
		name                    string
		llmSvc                  *v1alpha1.LLMInferenceService
		gateways                []*gatewayapi.Gateway
		expectedRouterReady     bool
		expectedConditionReason string
		expectedErrorMsg        string
		expectConditionUnset    bool // true when condition should remain nil
	}{
		{
			name: "single ready gateway - router should be ready",
			llmSvc: LLMInferenceService("test-llm",
				InNamespace[*v1alpha1.LLMInferenceService]("test-ns"),
				WithModelURI("hf://test/model"),
				WithGatewayRefs(v1alpha1.UntypedObjectReference{
					Name:      "ready-gateway",
					Namespace: "test-ns",
				}),
			),
			gateways: []*gatewayapi.Gateway{
				Gateway("ready-gateway",
					InNamespace[*gatewayapi.Gateway]("test-ns"),
					WithListener(gatewayapi.HTTPProtocolType),
					WithAddresses("203.0.113.1"),
					WithProgrammedCondition(metav1.ConditionTrue, "Ready", "Gateway is ready"),
				),
			},
			expectedRouterReady:     true,
			expectedConditionReason: "",
		},
		{
			name: "single not ready gateway - router should not be ready",
			llmSvc: LLMInferenceService("test-llm",
				InNamespace[*v1alpha1.LLMInferenceService]("test-ns"),
				WithModelURI("hf://test/model"),
				WithGatewayRefs(v1alpha1.UntypedObjectReference{
					Name:      "not-ready-gateway",
					Namespace: "test-ns",
				}),
			),
			gateways: []*gatewayapi.Gateway{
				Gateway("not-ready-gateway",
					InNamespace[*gatewayapi.Gateway]("test-ns"),
					WithListener(gatewayapi.HTTPProtocolType),
					WithAddresses("203.0.113.1"),
					WithProgrammedCondition(metav1.ConditionFalse, "NotReady", "Gateway is not ready"),
				),
			},
			expectedRouterReady:     false,
			expectedConditionReason: "GatewaysNotReady",
		},
		{
			name: "multiple gateways - all ready",
			llmSvc: LLMInferenceService("test-llm",
				InNamespace[*v1alpha1.LLMInferenceService]("test-ns"),
				WithModelURI("hf://test/model"),
				WithGatewayRefs(
					v1alpha1.UntypedObjectReference{Name: "gateway-1", Namespace: "test-ns"},
					v1alpha1.UntypedObjectReference{Name: "gateway-2", Namespace: "test-ns"},
				),
			),
			gateways: []*gatewayapi.Gateway{
				Gateway("gateway-1",
					InNamespace[*gatewayapi.Gateway]("test-ns"),
					WithListener(gatewayapi.HTTPProtocolType),
					WithProgrammedCondition(metav1.ConditionTrue, "Ready", "Gateway 1 is ready"),
				),
				Gateway("gateway-2",
					InNamespace[*gatewayapi.Gateway]("test-ns"),
					WithListener(gatewayapi.HTTPProtocolType),
					WithProgrammedCondition(metav1.ConditionTrue, "Ready", "Gateway 2 is ready"),
				),
			},
			expectedRouterReady:     true,
			expectedConditionReason: "",
		},
		{
			name: "multiple gateways - some not ready",
			llmSvc: LLMInferenceService("test-llm",
				InNamespace[*v1alpha1.LLMInferenceService]("test-ns"),
				WithModelURI("hf://test/model"),
				WithGatewayRefs(
					v1alpha1.UntypedObjectReference{Name: "ready-gateway", Namespace: "test-ns"},
					v1alpha1.UntypedObjectReference{Name: "not-ready-gateway", Namespace: "test-ns"},
				),
			),
			gateways: []*gatewayapi.Gateway{
				Gateway("ready-gateway",
					InNamespace[*gatewayapi.Gateway]("test-ns"),
					WithListener(gatewayapi.HTTPProtocolType),
					WithProgrammedCondition(metav1.ConditionTrue, "Ready", "Gateway is ready"),
				),
				Gateway("not-ready-gateway",
					InNamespace[*gatewayapi.Gateway]("test-ns"),
					WithListener(gatewayapi.HTTPProtocolType),
					WithProgrammedCondition(metav1.ConditionFalse, "NotReady", "Gateway is not ready"),
				),
			},
			expectedRouterReady:     false,
			expectedConditionReason: "GatewaysNotReady",
		},
		{
			name: "gateway with no programmed condition - should be not ready",
			llmSvc: LLMInferenceService("test-llm",
				InNamespace[*v1alpha1.LLMInferenceService]("test-ns"),
				WithModelURI("hf://test/model"),
				WithGatewayRefs(v1alpha1.UntypedObjectReference{
					Name:      "no-condition-gateway",
					Namespace: "test-ns",
				}),
			),
			gateways: []*gatewayapi.Gateway{
				Gateway("no-condition-gateway",
					InNamespace[*gatewayapi.Gateway]("test-ns"),
					WithListener(gatewayapi.HTTPProtocolType),
					// No programmed condition set
				),
			},
			expectedRouterReady:     false,
			expectedConditionReason: "GatewaysNotReady",
		},
		{
			name: "gateway not found - should return error",
			llmSvc: LLMInferenceService("test-llm",
				InNamespace[*v1alpha1.LLMInferenceService]("test-ns"),
				WithModelURI("hf://test/model"),
				WithGatewayRefs(v1alpha1.UntypedObjectReference{
					Name:      "missing-gateway",
					Namespace: "test-ns",
				}),
			),
			gateways:            []*gatewayapi.Gateway{},
			expectedRouterReady: false,
			expectedErrorMsg:    "failed to get Gateway",
		},
		{
			name: "no gateway refs - should skip evaluation",
			llmSvc: LLMInferenceService("test-llm",
				InNamespace[*v1alpha1.LLMInferenceService]("test-ns"),
				WithModelURI("hf://test/model"),
				// No gateway refs
			),
			gateways:             []*gatewayapi.Gateway{},
			expectConditionUnset: true, // Should not set any router condition
		},
		{
			name: "gateway without namespace uses LLMInferenceService namespace",
			llmSvc: LLMInferenceService("test-llm",
				InNamespace[*v1alpha1.LLMInferenceService]("test-ns"),
				WithModelURI("hf://test/model"),
				WithGatewayRefs(v1alpha1.UntypedObjectReference{
					Name: "same-ns-gateway",
					// Namespace omitted - should use test-ns
				}),
			),
			gateways: []*gatewayapi.Gateway{
				Gateway("same-ns-gateway",
					InNamespace[*gatewayapi.Gateway]("test-ns"),
					WithListener(gatewayapi.HTTPProtocolType),
					WithProgrammedCondition(metav1.ConditionTrue, "Ready", "Gateway is ready"),
				),
			},
			expectedRouterReady: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewGomegaWithT(t)
			ctx := t.Context()

			// Setup scheme and fake client
			scheme := runtime.NewScheme()
			err := v1alpha1.AddToScheme(scheme)
			g.Expect(err).ToNot(HaveOccurred())
			err = gatewayapi.Install(scheme)
			g.Expect(err).ToNot(HaveOccurred())

			// Prepare objects for fake client
			var objects []client.Object
			objects = append(objects, tt.llmSvc)
			for _, gw := range tt.gateways {
				objects = append(objects, gw)
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(objects...).
				Build()

			// Create reconciler
			reconciler := &llmisvc.LLMInferenceServiceReconciler{
				Client: fakeClient,
			}

			// Execute the evaluation
			err = reconciler.EvaluateGatewayConditions(ctx, tt.llmSvc)

			if tt.expectedErrorMsg != "" {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring(tt.expectedErrorMsg))
				return
			}

			g.Expect(err).ToNot(HaveOccurred())

			// Aggregate gateway conditions into router readiness
			tt.llmSvc.DetermineRouterReadiness()

			// Check the router condition
			routerCondition := tt.llmSvc.GetStatus().GetCondition(v1alpha1.RouterReady)
			// Also check the gateway condition was set properly
			gatewayCondition := tt.llmSvc.GetStatus().GetCondition(v1alpha1.GatewaysReady)
			switch {
			case tt.expectConditionUnset:
				g.Expect(routerCondition.IsTrue()).To(BeTrue(), "Router should be ready")
				g.Expect(gatewayCondition).To(BeNil(), "Gateway condition should not be set when no gateway refs")
			case tt.expectedRouterReady:
				g.Expect(routerCondition).ToNot(BeNil(), "Router condition should be set")
				g.Expect(routerCondition.IsTrue()).To(BeTrue(), "Router should be ready")
				g.Expect(gatewayCondition).ToNot(BeNil(), "Gateway condition should be set")
				g.Expect(gatewayCondition.IsTrue()).To(BeTrue(), "Gateways should be ready")
				if tt.expectedConditionReason != "" {
					g.Expect(routerCondition.Reason).To(Equal(tt.expectedConditionReason))
				}
			default:
				g.Expect(routerCondition).ToNot(BeNil(), "Router condition should be set")
				g.Expect(routerCondition.IsFalse()).To(BeTrue(), "Router should not be ready")
				g.Expect(gatewayCondition).ToNot(BeNil(), "Gateway condition should be set")
				g.Expect(gatewayCondition.IsFalse()).To(BeTrue(), "Gateways should not be ready")
				if tt.expectedConditionReason != "" {
					// The router condition reason should propagate from gateway condition
					g.Expect(routerCondition.Reason).To(Equal(gatewayCondition.Reason))
				}
			}
		})
	}
}

func TestIsGatewayReady(t *testing.T) {
	tests := []struct {
		name     string
		gateway  *gatewayapi.Gateway
		expected bool
	}{
		{
			name: "gateway with programmed condition true - should be ready",
			gateway: Gateway("test-gateway",
				WithProgrammedCondition(metav1.ConditionTrue, "Ready", "Gateway is ready"),
			),
			expected: true,
		},
		{
			name: "gateway with programmed condition false - should not be ready",
			gateway: Gateway("test-gateway",
				WithProgrammedCondition(metav1.ConditionFalse, "NotReady", "Gateway is not ready"),
			),
			expected: false,
		},
		{
			name: "gateway with programmed condition unknown - should not be ready",
			gateway: Gateway("test-gateway",
				WithProgrammedCondition(metav1.ConditionUnknown, "Unknown", "Gateway status unknown"),
			),
			expected: false,
		},
		{
			name: "gateway with no conditions - should not be ready",
			gateway: Gateway("test-gateway",
				WithListener(gatewayapi.HTTPProtocolType),
			),
			expected: false,
		},
		{
			name: "gateway with other conditions but no programmed - should not be ready",
			gateway: Gateway("test-gateway",
				WithGatewayCondition("Accepted", metav1.ConditionTrue, "Accepted", "Gateway accepted"),
			),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewGomegaWithT(t)

			result := llmisvc.IsGatewayReady(tt.gateway)

			g.Expect(result).To(Equal(tt.expected))
		})
	}
}

func TestFetchReferencedGateways(t *testing.T) {
	tests := []struct {
		name          string
		llmSvc        *v1alpha1.LLMInferenceService
		gateways      []*gatewayapi.Gateway
		expectedCount int
		expectedError string
	}{
		{
			name: "fetch single gateway successfully",
			llmSvc: LLMInferenceService("test-llm",
				InNamespace[*v1alpha1.LLMInferenceService]("test-ns"),
				WithGatewayRefs(v1alpha1.UntypedObjectReference{
					Name:      "test-gateway",
					Namespace: "test-ns",
				}),
			),
			gateways: []*gatewayapi.Gateway{
				Gateway("test-gateway", InNamespace[*gatewayapi.Gateway]("test-ns")),
			},
			expectedCount: 1,
		},
		{
			name: "fetch multiple gateways successfully",
			llmSvc: LLMInferenceService("test-llm",
				InNamespace[*v1alpha1.LLMInferenceService]("test-ns"),
				WithGatewayRefs(
					v1alpha1.UntypedObjectReference{Name: "gateway-1", Namespace: "test-ns"},
					v1alpha1.UntypedObjectReference{Name: "gateway-2", Namespace: "other-ns"},
				),
			),
			gateways: []*gatewayapi.Gateway{
				Gateway("gateway-1", InNamespace[*gatewayapi.Gateway]("test-ns")),
				Gateway("gateway-2", InNamespace[*gatewayapi.Gateway]("other-ns")),
			},
			expectedCount: 2,
		},
		{
			name: "gateway not found - should return error",
			llmSvc: LLMInferenceService("test-llm",
				InNamespace[*v1alpha1.LLMInferenceService]("test-ns"),
				WithGatewayRefs(v1alpha1.UntypedObjectReference{
					Name:      "missing-gateway",
					Namespace: "test-ns",
				}),
			),
			gateways:      []*gatewayapi.Gateway{},
			expectedCount: 0,
			expectedError: "failed to get Gateway",
		},
		{
			name: "no gateway refs - should return empty slice",
			llmSvc: LLMInferenceService("test-llm",
				InNamespace[*v1alpha1.LLMInferenceService]("test-ns"),
				// No gateway refs
			),
			gateways:      []*gatewayapi.Gateway{},
			expectedCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewGomegaWithT(t)
			ctx := t.Context()

			// Setup scheme and fake client
			scheme := runtime.NewScheme()
			err := v1alpha1.AddToScheme(scheme)
			g.Expect(err).ToNot(HaveOccurred())
			err = gatewayapi.Install(scheme)
			g.Expect(err).ToNot(HaveOccurred())

			// Prepare objects for fake client
			var objects []client.Object
			objects = append(objects, tt.llmSvc)
			for _, gw := range tt.gateways {
				objects = append(objects, gw)
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(objects...).
				Build()

			// Create reconciler
			reconciler := &llmisvc.LLMInferenceServiceReconciler{
				Client: fakeClient,
			}

			// Execute the fetch
			gateways, err := reconciler.CollectReferencedGateways(ctx, tt.llmSvc)

			if tt.expectedError != "" {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring(tt.expectedError))
				return
			}

			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(gateways).To(HaveLen(tt.expectedCount))
		})
	}
}
