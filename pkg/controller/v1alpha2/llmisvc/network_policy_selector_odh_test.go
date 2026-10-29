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

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"knative.dev/pkg/apis"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
	"github.com/kserve/kserve/pkg/constants"
)

func TestNetworkPolicyIdentityLabelsStayOutOfDeploymentSelectors(t *testing.T) {
	modelURI, err := apis.ParseURL("hf://facebook/opt-125m")
	if err != nil {
		t.Fatalf("failed to parse test model URI: %v", err)
	}
	service := func() *v1alpha2.LLMInferenceService {
		return &v1alpha2.LLMInferenceService{
			ObjectMeta: metav1.ObjectMeta{Name: "selector-identity", Namespace: "team-a"},
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
	// preserves its selector while the expected PodTemplate gains NP labels.
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
		want  string
	}{
		{
			name: "single-node main with existing deployment",
			build: func(svc *v1alpha2.LLMInferenceService) (*appsv1.Deployment, error) {
				return r.expectedSingleNodeMainDeployment(t.Context(), svc, &Config{})
			},
			want: networkPolicyRoleWorkload,
		},
		{
			name: "single-node prefill",
			build: func(svc *v1alpha2.LLMInferenceService) (*appsv1.Deployment, error) {
				svc.Spec.Prefill = &v1alpha2.WorkloadSpec{}
				return r.expectedPrefillMainDeployment(t.Context(), svc, &Config{})
			},
			want: networkPolicyRoleWorkload,
		},
		{
			name: "scheduler",
			build: func(svc *v1alpha2.LLMInferenceService) (*appsv1.Deployment, error) {
				svc.Spec.Router = &v1alpha2.RouterSpec{Scheduler: &v1alpha2.SchedulerSpec{}}
				return r.expectedSchedulerDeployment(t.Context(), svc)
			},
			want: networkPolicyRoleScheduler,
		},
		{
			name: "tokenizer",
			build: func(svc *v1alpha2.LLMInferenceService) (*appsv1.Deployment, error) {
				return r.expectedTokenizerDeployment(t.Context(), svc)
			},
			want: networkPolicyRoleTokenizer,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, err := tt.build(service())
			if err != nil {
				t.Fatalf("failed to build deployment: %v", err)
			}
			if _, ok := d.Spec.Selector.MatchLabels[networkPolicyServiceIDLabel]; ok {
				t.Fatalf("NetworkPolicy service identity leaked into immutable selector: %#v", d.Spec.Selector.MatchLabels)
			}
			if _, ok := d.Spec.Selector.MatchLabels[networkPolicyRoleLabel]; ok {
				t.Fatalf("NetworkPolicy role leaked into immutable selector: %#v", d.Spec.Selector.MatchLabels)
			}
			if got := d.Spec.Template.Labels[networkPolicyServiceIDLabel]; got != networkPolicyServiceID(service()) {
				t.Fatalf("PodTemplate service identity got %q, want %q", got, networkPolicyServiceID(service()))
			}
			if got := d.Spec.Template.Labels[networkPolicyRoleLabel]; got != tt.want {
				t.Fatalf("PodTemplate role got %q, want %q", got, tt.want)
			}
			for key, value := range d.Spec.Selector.MatchLabels {
				if d.Spec.Template.Labels[key] != value {
					t.Fatalf("PodTemplate does not satisfy selector key %q=%q: %#v", key, value, d.Spec.Template.Labels)
				}
			}
		})
	}
}
