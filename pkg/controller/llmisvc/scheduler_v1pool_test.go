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

package llmisvc

import (
	"testing"

	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	igwapi "sigs.k8s.io/gateway-api-inference-extension/api/v1alpha2"
	gatewayapi "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
	"github.com/kserve/kserve/pkg/constants"
)

func TestExpectedSchedulerInferencePoolV1(t *testing.T) {
	tests := []struct {
		name           string
		v1alpha2Pool   *igwapi.InferencePool
		eppGRPCPort    int32
		expectedName   string
		expectedNS     string
		expectedSpec   map[string]interface{}
		expectedLabels map[string]string
	}{
		{
			name: "basic conversion with all fields",
			v1alpha2Pool: &igwapi.InferencePool{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test-pool",
					Namespace:   "test-ns",
					Labels:      map[string]string{"app": "test"},
					Annotations: map[string]string{"note": "test-annotation"},
				},
				Spec: igwapi.InferencePoolSpec{
					TargetPortNumber: 8000,
					Selector: map[igwapi.LabelKey]igwapi.LabelValue{
						"app.kubernetes.io/name": "my-app",
					},
					EndpointPickerConfig: igwapi.EndpointPickerConfig{
						ExtensionRef: &igwapi.Extension{
							ExtensionReference: igwapi.ExtensionReference{
								Group: ptr.To(igwapi.Group("")),
								Kind:  ptr.To(igwapi.Kind("Service")),
								Name:  igwapi.ObjectName("my-extension"),
							},
						},
					},
				},
			},
			eppGRPCPort:  9002,
			expectedName: "test-pool",
			expectedNS:   "test-ns",
			expectedSpec: map[string]interface{}{
				"targetPorts": []interface{}{
					map[string]interface{}{
						"number": int64(8000),
					},
				},
				"selector": map[string]interface{}{
					"matchLabels": map[string]interface{}{
						"app.kubernetes.io/name": "my-app",
					},
				},
				"endpointPickerRef": map[string]interface{}{
					"name":  "my-extension",
					"group": "",
					"kind":  "Service",
					"port": map[string]interface{}{
						"number": int64(9002),
					},
				},
			},
			expectedLabels: map[string]string{
				"app": "test",
			},
		},
		{
			name: "conversion without extensionRef",
			v1alpha2Pool: &igwapi.InferencePool{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "simple-pool",
					Namespace: "default",
				},
				Spec: igwapi.InferencePoolSpec{
					TargetPortNumber: 9000,
					Selector: map[igwapi.LabelKey]igwapi.LabelValue{
						"component": "worker",
					},
					// No ExtensionRef
				},
			},
			eppGRPCPort:  9002,
			expectedName: "simple-pool",
			expectedNS:   "default",
			expectedSpec: map[string]interface{}{
				"targetPorts": []interface{}{
					map[string]interface{}{
						"number": int64(9000),
					},
				},
				"selector": map[string]interface{}{
					"matchLabels": map[string]interface{}{
						"component": "worker",
					},
				},
				// No endpointPickerRef expected
			},
			expectedLabels: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)

			result := expectedSchedulerInferencePoolV1(tt.v1alpha2Pool, tt.eppGRPCPort)

			// Verify GVK
			g.Expect(result.GetAPIVersion()).To(Equal(constants.InferencePoolV1Group + "/v1"))
			g.Expect(result.GetKind()).To(Equal("InferencePool"))

			// Verify metadata
			g.Expect(result.GetName()).To(Equal(tt.expectedName))
			g.Expect(result.GetNamespace()).To(Equal(tt.expectedNS))

			// Verify labels
			if tt.expectedLabels != nil {
				g.Expect(result.GetLabels()).To(Equal(tt.expectedLabels))
			}

			// Verify spec
			spec, found, err := unstructured.NestedMap(result.Object, "spec")
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(found).To(BeTrue())

			// Check targetPorts
			expectedTargetPorts := tt.expectedSpec["targetPorts"]
			actualTargetPorts, _, _ := unstructured.NestedSlice(result.Object, "spec", "targetPorts")
			g.Expect(actualTargetPorts).To(Equal(expectedTargetPorts))

			// Check selector.matchLabels
			if tt.expectedSpec["selector"] != nil {
				expectedSelector := tt.expectedSpec["selector"].(map[string]interface{})
				actualSelector, _, _ := unstructured.NestedMap(result.Object, "spec", "selector")
				g.Expect(actualSelector).To(Equal(expectedSelector))
			}

			// Check endpointPickerRef
			if tt.expectedSpec["endpointPickerRef"] != nil {
				expectedEPR := tt.expectedSpec["endpointPickerRef"].(map[string]interface{})
				actualEPR, found, _ := unstructured.NestedMap(result.Object, "spec", "endpointPickerRef")
				g.Expect(found).To(BeTrue(), "endpointPickerRef should be present")
				g.Expect(actualEPR["name"]).To(Equal(expectedEPR["name"]))
				g.Expect(actualEPR["port"]).To(Equal(expectedEPR["port"]))
				if expectedEPR["group"] != nil {
					g.Expect(actualEPR["group"]).To(Equal(expectedEPR["group"]))
				}
				if expectedEPR["kind"] != nil {
					g.Expect(actualEPR["kind"]).To(Equal(expectedEPR["kind"]))
				}
			} else {
				_, found, _ := unstructured.NestedMap(result.Object, "spec", "endpointPickerRef")
				g.Expect(found).To(BeFalse(), "endpointPickerRef should not be present")
			}

			// Verify spec is not nil
			g.Expect(spec).ToNot(BeNil())
		})
	}
}

func TestSemanticUnstructuredInferencePoolIsEqual(t *testing.T) {
	tests := []struct {
		name     string
		expected *unstructured.Unstructured
		current  *unstructured.Unstructured
		equal    bool
	}{
		{
			name: "identical pools are equal",
			expected: func() *unstructured.Unstructured {
				u := &unstructured.Unstructured{}
				u.SetName("test-pool")
				u.SetNamespace("test-ns")
				u.SetLabels(map[string]string{"app": "test"})
				u.Object["spec"] = map[string]interface{}{
					"targetPorts": []interface{}{
						map[string]interface{}{"number": int64(8000)},
					},
				}
				return u
			}(),
			current: func() *unstructured.Unstructured {
				u := &unstructured.Unstructured{}
				u.SetName("test-pool")
				u.SetNamespace("test-ns")
				u.SetLabels(map[string]string{"app": "test"})
				u.Object["spec"] = map[string]interface{}{
					"targetPorts": []interface{}{
						map[string]interface{}{"number": int64(8000)},
					},
				}
				return u
			}(),
			equal: true,
		},
		{
			name: "different spec is not equal",
			expected: func() *unstructured.Unstructured {
				u := &unstructured.Unstructured{}
				u.SetName("test-pool")
				u.Object["spec"] = map[string]interface{}{
					"targetPorts": []interface{}{
						map[string]interface{}{"number": int64(8000)},
					},
				}
				return u
			}(),
			current: func() *unstructured.Unstructured {
				u := &unstructured.Unstructured{}
				u.SetName("test-pool")
				u.Object["spec"] = map[string]interface{}{
					"targetPorts": []interface{}{
						map[string]interface{}{"number": int64(9000)}, // Different port
					},
				}
				return u
			}(),
			equal: false,
		},
		{
			name: "different labels is not equal",
			expected: func() *unstructured.Unstructured {
				u := &unstructured.Unstructured{}
				u.SetName("test-pool")
				u.SetLabels(map[string]string{"app": "test"})
				u.Object["spec"] = map[string]interface{}{}
				return u
			}(),
			current: func() *unstructured.Unstructured {
				u := &unstructured.Unstructured{}
				u.SetName("test-pool")
				u.SetLabels(map[string]string{"app": "other"})
				u.Object["spec"] = map[string]interface{}{}
				return u
			}(),
			equal: false,
		},
		{
			name: "current has extra fields - still equal (DeepDerivative)",
			expected: func() *unstructured.Unstructured {
				u := &unstructured.Unstructured{}
				u.SetName("test-pool")
				u.Object["spec"] = map[string]interface{}{
					"targetPorts": []interface{}{
						map[string]interface{}{"number": int64(8000)},
					},
				}
				return u
			}(),
			current: func() *unstructured.Unstructured {
				u := &unstructured.Unstructured{}
				u.SetName("test-pool")
				u.Object["spec"] = map[string]interface{}{
					"targetPorts": []interface{}{
						map[string]interface{}{"number": int64(8000)},
					},
					"extraField": "extra-value", // Extra field in current
				}
				return u
			}(),
			equal: true, // DeepDerivative allows extra fields in current
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)
			result := semanticUnstructuredInferencePoolIsEqual(tt.expected, tt.current)
			g.Expect(result).To(Equal(tt.equal))
		})
	}
}

func TestIsInferencePoolMigrated(t *testing.T) {
	tests := []struct {
		name           string
		llmSvc         *v1alpha1.LLMInferenceService
		existingRoute  *gatewayapi.HTTPRoute
		expectedResult bool
	}{
		{
			name: "no HTTPRoute exists - not migrated",
			llmSvc: &v1alpha1.LLMInferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-llm",
					Namespace: "test-ns",
				},
			},
			existingRoute:  nil,
			expectedResult: false,
		},
		{
			name: "HTTPRoute exists without migration annotation - not migrated",
			llmSvc: &v1alpha1.LLMInferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-llm",
					Namespace: "test-ns",
				},
			},
			existingRoute: &gatewayapi.HTTPRoute{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-llm-kserve-route",
					Namespace: "test-ns",
				},
			},
			expectedResult: false,
		},
		{
			name: "HTTPRoute exists with wrong migration annotation value - not migrated",
			llmSvc: &v1alpha1.LLMInferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-llm",
					Namespace: "test-ns",
				},
			},
			existingRoute: &gatewayapi.HTTPRoute{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-llm-kserve-route",
					Namespace: "test-ns",
					Annotations: map[string]string{
						constants.InferencePoolMigratedAnnotation: "false",
					},
				},
			},
			expectedResult: false,
		},
		{
			name: "HTTPRoute exists with v1 migration annotation - migrated",
			llmSvc: &v1alpha1.LLMInferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-llm",
					Namespace: "test-ns",
				},
			},
			existingRoute: &gatewayapi.HTTPRoute{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-llm-kserve-route",
					Namespace: "test-ns",
					Annotations: map[string]string{
						constants.InferencePoolMigratedAnnotation: "v1",
					},
				},
			},
			expectedResult: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)
			ctx := t.Context()

			scheme := runtime.NewScheme()
			_ = v1alpha1.AddToScheme(scheme)
			_ = gatewayapi.Install(scheme)

			var objects []client.Object
			if tt.existingRoute != nil {
				objects = append(objects, tt.existingRoute)
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(objects...).
				Build()

			reconciler := &LLMInferenceServiceReconciler{
				Client: fakeClient,
			}

			result := reconciler.isInferencePoolMigrated(ctx, tt.llmSvc)
			g.Expect(result).To(Equal(tt.expectedResult))
		})
	}
}

func TestExpectedSchedulerDeploymentPoolGroup(t *testing.T) {
	tests := []struct {
		name              string
		llmSvc            *v1alpha1.LLMInferenceService
		existingRoute     *gatewayapi.HTTPRoute
		expectedPoolGroup string
	}{
		{
			name: "not migrated - uses v1alpha2 pool group",
			llmSvc: &v1alpha1.LLMInferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-llm",
					Namespace: "test-ns",
				},
				Spec: v1alpha1.LLMInferenceServiceSpec{
					Router: &v1alpha1.RouterSpec{
						Scheduler: &v1alpha1.SchedulerSpec{
							Template: &corev1.PodSpec{
								Containers: []corev1.Container{
									{
										Name: "main",
										Args: []string{
											"--pool-group", "inference.networking.x-k8s.io",
											"--pool-name", "test-pool",
										},
									},
								},
							},
						},
					},
				},
			},
			existingRoute:     nil,
			expectedPoolGroup: constants.InferencePoolV1Alpha2Group,
		},
		{
			name: "migrated - uses v1 pool group",
			llmSvc: &v1alpha1.LLMInferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-llm",
					Namespace: "test-ns",
				},
				Spec: v1alpha1.LLMInferenceServiceSpec{
					Router: &v1alpha1.RouterSpec{
						Scheduler: &v1alpha1.SchedulerSpec{
							Template: &corev1.PodSpec{
								Containers: []corev1.Container{
									{
										Name: "main",
										Args: []string{
											"--pool-group", "inference.networking.x-k8s.io",
											"--pool-name", "test-pool",
										},
									},
								},
							},
						},
					},
				},
			},
			existingRoute: &gatewayapi.HTTPRoute{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-llm-kserve-route",
					Namespace: "test-ns",
					Annotations: map[string]string{
						constants.InferencePoolMigratedAnnotation: "v1",
					},
				},
			},
			expectedPoolGroup: constants.InferencePoolV1Group,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)
			ctx := t.Context()

			scheme := runtime.NewScheme()
			_ = v1alpha1.AddToScheme(scheme)
			_ = gatewayapi.Install(scheme)
			_ = corev1.AddToScheme(scheme)

			var objects []client.Object
			if tt.existingRoute != nil {
				objects = append(objects, tt.existingRoute)
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(objects...).
				Build()

			reconciler := &LLMInferenceServiceReconciler{
				Client: fakeClient,
			}

			deployment := reconciler.expectedSchedulerDeployment(ctx, tt.llmSvc)

			// Find the main container and check --pool-group arg
			var foundPoolGroup string
			for _, container := range deployment.Spec.Template.Spec.Containers {
				if container.Name == "main" {
					args := container.Args
					for j := range len(args) - 1 {
						if args[j] == "--pool-group" || args[j] == "-pool-group" {
							foundPoolGroup = args[j+1]
							break
						}
					}
					break
				}
			}

			g.Expect(foundPoolGroup).To(Equal(tt.expectedPoolGroup))
		})
	}
}
