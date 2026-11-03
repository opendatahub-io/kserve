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

package llmisvc

import (
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"knative.dev/pkg/apis"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
	"github.com/kserve/kserve/pkg/constants"
	"github.com/kserve/kserve/pkg/credentials"
)

func TestDeploymentPodTemplatesSatisfySelectors(t *testing.T) {
	modelURI, err := apis.ParseURL("hf://facebook/opt-125m")
	if err != nil {
		t.Fatalf("failed to parse test model URI: %v", err)
	}
	service := func() *v1alpha2.LLMInferenceService {
		return &v1alpha2.LLMInferenceService{
			ObjectMeta: metav1.ObjectMeta{Name: "selector-labels", Namespace: "team-a"},
			Spec: v1alpha2.LLMInferenceServiceSpec{
				Model:              v1alpha2.LLMModelSpec{URI: *modelURI},
				WorkloadSpec:       v1alpha2.WorkloadSpec{Template: &corev1.PodSpec{}},
				StorageInitializer: &v1alpha2.StorageInitializerSpec{Enabled: ptr.To(false)},
			},
		}
	}

	client := selectorTestClient(t)
	r := &LLMISVCReconciler{Client: client}

	// Keep an existing main Deployment present to exercise the upgrade path that
	// preserves its selector.
	main := service()
	existing := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: mainDeploymentName(main), Namespace: main.Namespace},
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{
			constants.LLMDRoleLabelKey: constants.LLMDRoleBoth,
		}}}},
	}
	if err := client.Create(t.Context(), existing); err != nil {
		t.Fatalf("failed to seed existing Deployment: %v", err)
	}

	tests := []struct {
		name  string
		build func(*v1alpha2.LLMInferenceService) (*appsv1.Deployment, error)
	}{
		{
			name: "single-node main with existing deployment",
			build: func(svc *v1alpha2.LLMInferenceService) (*appsv1.Deployment, error) {
				return r.expectedSingleNodeMainDeployment(t.Context(), svc, &Config{})
			},
		},
		{
			name: "single-node prefill",
			build: func(svc *v1alpha2.LLMInferenceService) (*appsv1.Deployment, error) {
				svc.Spec.Prefill = &v1alpha2.WorkloadSpec{}
				return r.expectedPrefillMainDeployment(t.Context(), svc, &Config{})
			},
		},
		{
			name: "scheduler",
			build: func(svc *v1alpha2.LLMInferenceService) (*appsv1.Deployment, error) {
				svc.Spec.Router = &v1alpha2.RouterSpec{Scheduler: &v1alpha2.SchedulerSpec{}}
				return r.expectedSchedulerDeployment(t.Context(), svc)
			},
		},
		{
			name: "tokenizer",
			build: func(svc *v1alpha2.LLMInferenceService) (*appsv1.Deployment, error) {
				return r.expectedTokenizerDeployment(t.Context(), svc)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, err := tt.build(service())
			if err != nil {
				t.Fatalf("failed to build deployment: %v", err)
			}
			for key, value := range d.Spec.Selector.MatchLabels {
				if d.Spec.Template.Labels[key] != value {
					t.Fatalf("PodTemplate does not satisfy selector key %q=%q: %#v", key, value, d.Spec.Template.Labels)
				}
			}
		})
	}
}

// TestComponentPodTemplateApplicationLabels covers the application labels used
// by the component controllers across Deployment and LeaderWorkerSet templates.
func TestComponentPodTemplateApplicationLabels(t *testing.T) {
	t.Run("single node deployments", func(t *testing.T) {
		modelURI, err := apis.ParseURL("hf://facebook/opt-125m")
		require.NoError(t, err)
		r := &LLMISVCReconciler{Client: selectorTestClient(t)}
		svc := &v1alpha2.LLMInferenceService{
			ObjectMeta: metav1.ObjectMeta{Name: "template-labels", Namespace: "team-a"},
			Spec: v1alpha2.LLMInferenceServiceSpec{
				Model:              v1alpha2.LLMModelSpec{URI: *modelURI},
				WorkloadSpec:       v1alpha2.WorkloadSpec{Template: &corev1.PodSpec{}},
				StorageInitializer: &v1alpha2.StorageInitializerSpec{Enabled: ptr.To(false)},
				Prefill:            &v1alpha2.WorkloadSpec{},
			},
		}

		main, err := r.expectedSingleNodeMainDeployment(t.Context(), svc, &Config{})
		require.NoError(t, err)
		prefill, err := r.expectedPrefillMainDeployment(t.Context(), svc, &Config{})
		require.NoError(t, err)

		require.Contains(t, main.Spec.Template.Labels, constants.KubernetesAppNameLabelKey)
		require.Contains(t, prefill.Spec.Template.Labels, constants.KubernetesAppNameLabelKey)
	})

	t.Run("multi node leader worker sets", func(t *testing.T) {
		r := &LLMISVCReconciler{Client: newFakeClientWithLWSNoMatch(t)}
		svc := newLLMInferenceServiceWithMultiNode("template-labels", "default")
		prefill := svc.Spec.WorkloadSpec
		prefill.Template = svc.Spec.Template.DeepCopy()
		prefill.Worker = svc.Spec.Worker.DeepCopy()
		svc.Spec.Prefill = &prefill
		cfg := &Config{CredentialConfig: &credentials.CredentialConfig{}}

		decode, err := r.expectedMainMultiNodeLWS(t.Context(), svc, cfg)
		require.NoError(t, err)
		prefillLWS, err := r.expectedPrefillMultiNodeLWS(t.Context(), svc, cfg)
		require.NoError(t, err)

		// The LWS object shares its label map with the worker template, so the
		// object labels are checked as well.
		require.NotNil(t, decode.Spec.LeaderWorkerTemplate.LeaderTemplate)
		require.Contains(t, decode.Spec.LeaderWorkerTemplate.LeaderTemplate.Labels, constants.KubernetesAppNameLabelKey)
		require.Contains(t, decode.Spec.LeaderWorkerTemplate.WorkerTemplate.Labels, constants.KubernetesAppNameLabelKey)
		require.Contains(t, decode.Labels, constants.KubernetesAppNameLabelKey)

		require.NotNil(t, prefillLWS.Spec.LeaderWorkerTemplate.LeaderTemplate)
		require.Contains(t, prefillLWS.Spec.LeaderWorkerTemplate.LeaderTemplate.Labels, constants.KubernetesAppNameLabelKey)
		require.Contains(t, prefillLWS.Spec.LeaderWorkerTemplate.WorkerTemplate.Labels, constants.KubernetesAppNameLabelKey)
		require.Contains(t, prefillLWS.Labels, constants.KubernetesAppNameLabelKey)
	})

	t.Run("scheduler and tokenizer deployments", func(t *testing.T) {
		modelURI, err := apis.ParseURL("hf://facebook/opt-125m")
		require.NoError(t, err)
		r := &LLMISVCReconciler{Client: selectorTestClient(t)}
		svc := &v1alpha2.LLMInferenceService{
			ObjectMeta: metav1.ObjectMeta{Name: "template-labels", Namespace: "team-a"},
			Spec: v1alpha2.LLMInferenceServiceSpec{
				Model:  v1alpha2.LLMModelSpec{URI: *modelURI},
				Router: &v1alpha2.RouterSpec{Scheduler: &v1alpha2.SchedulerSpec{}},
			},
		}

		scheduler, err := r.expectedSchedulerDeployment(t.Context(), svc)
		require.NoError(t, err)
		tokenizer, err := r.expectedTokenizerDeployment(t.Context(), svc)
		require.NoError(t, err)

		require.Contains(t, scheduler.Spec.Template.Labels, constants.KubernetesAppNameLabelKey)
		require.Contains(t, tokenizer.Spec.Template.Labels, constants.KubernetesAppNameLabelKey)
	})
}
