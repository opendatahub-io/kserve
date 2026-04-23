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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/kserve/kserve/pkg/constants"
)

func TestShouldInjectInferenceServiceName(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)

	log := ctrl.Log.WithName("test")
	ns := "default"
	isvcName := "my-isvc"
	deploymentName := types.NamespacedName{Name: constants.PredictorServiceName(isvcName), Namespace: ns}
	containerName := constants.InferenceServiceContainerName

	makeDeployment := func(envVars ...corev1.EnvVar) *appsv1.Deployment {
		return &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      deploymentName.Name,
				Namespace: ns,
			},
			Spec: appsv1.DeploymentSpec{
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Name: containerName, Env: envVars},
						},
					},
				},
			},
		}
	}

	tests := []struct {
		name             string
		existingDeployment *appsv1.Deployment
		wantInject       bool
		wantErr          bool
	}{
		{
			name:             "new ISVC — no existing deployment, should inject",
			existingDeployment: nil,
			wantInject:       true,
		},
		{
			name: "pre-upgrade deployment — env var absent, should skip to avoid restart",
			existingDeployment: makeDeployment(),
			wantInject:         false,
		},
		{
			name: "post-upgrade deployment — env var already present, should inject (idempotent)",
			existingDeployment: makeDeployment(corev1.EnvVar{
				Name:  constants.InferenceServiceNameEnvVarKey,
				Value: isvcName,
			}),
			wantInject: true,
		},
		{
			name: "existing deployment without matching container — treat as new, should inject",
			existingDeployment: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Name: deploymentName.Name, Namespace: ns},
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{Name: "some-other-container"},
							},
						},
					},
				},
			},
			wantInject: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			builder := fake.NewClientBuilder().WithScheme(scheme)
			if tt.existingDeployment != nil {
				builder = builder.WithObjects(tt.existingDeployment)
			}
			c := builder.Build()

			got, err := shouldInjectInferenceServiceName(context.Background(), c, deploymentName, containerName, log)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantInject, got)
		})
	}
}
