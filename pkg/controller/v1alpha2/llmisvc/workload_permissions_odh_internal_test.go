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

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
)

func TestAddsCapabilityRejectedByRestrictedV2(t *testing.T) {
	t.Parallel()

	adding := func(capabilities ...corev1.Capability) corev1.Container {
		return corev1.Container{
			Name:            "c",
			SecurityContext: &corev1.SecurityContext{Capabilities: &corev1.Capabilities{Add: capabilities}},
		}
	}

	tests := []struct {
		name string
		pod  corev1.PodSpec
		want bool
	}{
		{name: "empty pod", pod: corev1.PodSpec{}, want: false},
		{name: "no security context", pod: corev1.PodSpec{Containers: []corev1.Container{{Name: "c"}}}, want: false},
		{
			name: "no capabilities",
			pod:  corev1.PodSpec{Containers: []corev1.Container{{Name: "c", SecurityContext: &corev1.SecurityContext{}}}},
			want: false,
		},
		{
			name: "drop only",
			pod: corev1.PodSpec{Containers: []corev1.Container{{
				Name:            "c",
				SecurityContext: &corev1.SecurityContext{Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
			}}},
			want: false,
		},
		{name: "NET_BIND_SERVICE only", pod: corev1.PodSpec{Containers: []corev1.Container{adding("NET_BIND_SERVICE")}}, want: false},
		{name: "IPC_LOCK", pod: corev1.PodSpec{Containers: []corev1.Container{adding("IPC_LOCK")}}, want: true},
		{name: "NET_BIND_SERVICE with NET_RAW", pod: corev1.PodSpec{Containers: []corev1.Container{adding("NET_BIND_SERVICE", "NET_RAW")}}, want: true},
		{
			name: "second container adds SYS_RAWIO",
			pod:  corev1.PodSpec{Containers: []corev1.Container{{Name: "a"}, adding("SYS_RAWIO")}},
			want: true,
		},
		{
			name: "init container adds IPC_LOCK",
			pod:  corev1.PodSpec{InitContainers: []corev1.Container{adding("IPC_LOCK")}, Containers: []corev1.Container{{Name: "c"}}},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := addsCapabilityRejectedByRestrictedV2(&tt.pod); got != tt.want {
				t.Fatalf("addsCapabilityRejectedByRestrictedV2() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSemanticSCCRoleBindingIsEqualDetectsRemovedSubject(t *testing.T) {
	t.Parallel()

	main := rbacv1.Subject{Kind: rbacv1.ServiceAccountKind, Name: "svc-kserve", Namespace: "ns"}
	prefill := rbacv1.Subject{Kind: rbacv1.ServiceAccountKind, Name: "default", Namespace: "ns"}
	binding := func(subjects ...rbacv1.Subject) *rbacv1.RoleBinding {
		return &rbacv1.RoleBinding{
			RoleRef:  rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: llmInferenceServiceSCCClusterRole},
			Subjects: subjects,
		}
	}

	if !semanticSCCRoleBindingIsEqual(binding(main, prefill), binding(main, prefill)) {
		t.Fatal("identical bindings must be equal")
	}
	if semanticSCCRoleBindingIsEqual(binding(main), binding(main, prefill)) {
		t.Fatal("a binding that drops a subject must not compare equal to the stored one")
	}
	if semanticSCCRoleBindingIsEqual(binding(main, prefill), binding(main)) {
		t.Fatal("a binding that adds a subject must not compare equal to the stored one")
	}
}

func TestSingleNodeTemplatesRequireDedicatedServiceAccount(t *testing.T) {
	t.Parallel()

	adding := func(serviceAccount string, capabilities ...corev1.Capability) *corev1.PodSpec {
		return &corev1.PodSpec{
			ServiceAccountName: serviceAccount,
			Containers: []corev1.Container{{
				Name:            "main",
				SecurityContext: &corev1.SecurityContext{Capabilities: &corev1.Capabilities{Add: capabilities}},
			}},
		}
	}
	worker := &corev1.PodSpec{}

	tests := []struct {
		name string
		spec v1alpha2.LLMInferenceServiceSpec
		want bool
	}{
		{name: "no templates", want: false},
		{
			name: "main adds IPC_LOCK without a ServiceAccount",
			spec: v1alpha2.LLMInferenceServiceSpec{WorkloadSpec: v1alpha2.WorkloadSpec{Template: adding("", "IPC_LOCK")}},
			want: true,
		},
		{
			name: "main adds IPC_LOCK with a ServiceAccount",
			spec: v1alpha2.LLMInferenceServiceSpec{WorkloadSpec: v1alpha2.WorkloadSpec{Template: adding("engine", "IPC_LOCK")}},
			want: false,
		},
		{
			name: "main adds only NET_BIND_SERVICE",
			spec: v1alpha2.LLMInferenceServiceSpec{WorkloadSpec: v1alpha2.WorkloadSpec{Template: adding("", "NET_BIND_SERVICE")}},
			want: false,
		},
		{
			name: "multi-node main adds IPC_LOCK",
			spec: v1alpha2.LLMInferenceServiceSpec{WorkloadSpec: v1alpha2.WorkloadSpec{Template: adding("", "IPC_LOCK"), Worker: worker}},
			want: false,
		},
		{
			name: "single-node prefill adds IPC_LOCK without a ServiceAccount",
			spec: v1alpha2.LLMInferenceServiceSpec{Prefill: &v1alpha2.WorkloadSpec{Template: adding("", "IPC_LOCK")}},
			want: true,
		},
		{
			name: "single-node prefill adds IPC_LOCK with a ServiceAccount",
			spec: v1alpha2.LLMInferenceServiceSpec{Prefill: &v1alpha2.WorkloadSpec{Template: adding("prefill", "IPC_LOCK")}},
			want: false,
		},
		{
			name: "multi-node prefill adds IPC_LOCK",
			spec: v1alpha2.LLMInferenceServiceSpec{Prefill: &v1alpha2.WorkloadSpec{Template: adding("", "IPC_LOCK"), Worker: worker}},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			llmSvc := &v1alpha2.LLMInferenceService{Spec: tt.spec}
			if got := singleNodeTemplatesRequireDedicatedServiceAccount(llmSvc); got != tt.want {
				t.Fatalf("singleNodeTemplatesRequireDedicatedServiceAccount() = %v, want %v", got, tt.want)
			}
		})
	}
}
