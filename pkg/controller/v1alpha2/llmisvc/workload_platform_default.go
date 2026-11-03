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

package llmisvc

import (
	"context"
	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

func (r *LLMISVCReconciler) getPlatformConfig(context.Context, *v1alpha2.LLMInferenceService, string) (*v1alpha2.LLMInferenceServiceConfig, bool, error) {
	return nil, false, nil
}
func (r *LLMISVCReconciler) preparePlatformWorkload(context.Context, *v1alpha2.LLMInferenceService, *Config) error {
	return nil
}
func (r *LLMISVCReconciler) attachPlatformModelArtifacts(context.Context, *corev1.ServiceAccount, *v1alpha2.LLMInferenceService, *corev1.PodSpec, *Config) (bool, error) {
	return false, nil
}
func applyPlatformDeploymentStrategy(*v1alpha2.LLMInferenceService, *appsv1.Deployment) {}

func platformConfigRefName(_ *v1alpha2.LLMInferenceService, _ *v1alpha2.LLMInferenceServiceConfig, name string) string {
	return name
}
