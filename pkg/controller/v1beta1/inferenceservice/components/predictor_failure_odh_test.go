//go:build distro

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

package components

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"knative.dev/pkg/apis"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
	"github.com/kserve/kserve/pkg/apis/serving/v1beta1"
)

func TestPredictorEarlyFailureConditions(t *testing.T) {
	disabled := true
	runtimeName := "test-runtime"
	storageURI := "s3://model"
	badStorageURI := "invalid://model"
	badStoragePercentURI := "custom://bucket/model%20name"
	tests := []struct {
		name            string
		runtime         *string
		storageURI      *string
		objects         []client.Object
		existingFailure *v1beta1.FailureInfo
		expectedReason  string
		expectedMsg     string
		expectedURI     string
	}{
		{
			name:           "no supporting runtime",
			expectedReason: string(v1beta1.NoSupportingRuntime),
			expectedMsg:    "No runtime found to support specified framework/version",
		},
		{
			name:            "runtime not recognized again",
			runtime:         &runtimeName,
			existingFailure: &v1beta1.FailureInfo{Reason: v1beta1.RuntimeNotRecognized, Message: "Waiting for runtime to become available"},
			expectedReason:  string(v1beta1.RuntimeNotRecognized),
			expectedMsg:     "Waiting for runtime to become available",
		},
		{
			name:    "runtime disabled",
			runtime: &runtimeName,
			objects: []client.Object{&v1alpha1.ServingRuntime{
				ObjectMeta: metav1.ObjectMeta{Name: runtimeName, Namespace: "default"},
				Spec:       v1alpha1.ServingRuntimeSpec{Disabled: &disabled},
			}},
			expectedReason: string(v1beta1.RuntimeDisabled),
			expectedMsg:    "Specified runtime is disabled",
		},
		{
			name:           "predictor spec build error",
			storageURI:     &badStorageURI,
			expectedReason: "PredictorSpecBuildError",
		},
		{
			name:           "predictor spec build error preserves encoded storage URI",
			storageURI:     &badStoragePercentURI,
			expectedReason: "PredictorSpecBuildError",
			expectedURI:    badStoragePercentURI,
		},
		{
			name:            "predictor spec build error after model load failure",
			storageURI:      &badStorageURI,
			existingFailure: &v1beta1.FailureInfo{Reason: v1beta1.ModelLoadFailed, Message: "old pod failure"},
			expectedReason:  "PredictorSpecBuildError",
		},
		{
			name:            "predictor spec build error after runtime failure",
			storageURI:      &badStorageURI,
			existingFailure: &v1beta1.FailureInfo{Reason: v1beta1.RuntimeNotRecognized, Message: "old runtime failure"},
			expectedReason:  "PredictorSpecBuildError",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, v1alpha1.AddToScheme(scheme))
			cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tc.objects...).Build()
			predictor := &Predictor{
				client:                 cl,
				scheme:                 scheme,
				inferenceServiceConfig: &v1beta1.InferenceServicesConfig{},
				Log:                    logr.Discard(),
			}
			uri := &storageURI
			if tc.storageURI != nil {
				uri = tc.storageURI
			}
			isvc := &v1beta1.InferenceService{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
				Spec: v1beta1.InferenceServiceSpec{Predictor: v1beta1.PredictorSpec{
					Model: &v1beta1.ModelSpec{
						ModelFormat:            v1beta1.ModelFormat{Name: "sklearn"},
						Runtime:                tc.runtime,
						PredictorExtensionSpec: v1beta1.PredictorExtensionSpec{StorageURI: uri},
					},
				}},
			}
			isvc.Status.InitializeConditions()
			isvc.Status.ModelStatus.LastFailureInfo = tc.existingFailure

			_, err := predictor.Reconcile(context.Background(), isvc)
			require.Error(t, err)
			if tc.expectedURI != "" {
				require.Contains(t, err.Error(), tc.expectedURI)
			}
			for _, conditionType := range []apis.ConditionType{v1beta1.PredictorReady, apis.ConditionReady} {
				condition := isvc.Status.GetCondition(conditionType)
				require.NotNil(t, condition)
				assert.Equal(t, corev1.ConditionFalse, condition.Status)
				assert.Equal(t, tc.expectedReason, condition.Reason)
				if tc.expectedMsg == "" {
					assert.Equal(t, err.Error(), condition.Message)
				} else {
					assert.Equal(t, tc.expectedMsg, condition.Message)
				}
			}
			assert.Equal(t, corev1.ConditionUnknown, isvc.Status.GetCondition(v1beta1.IngressReady).Status)
		})
	}
}
