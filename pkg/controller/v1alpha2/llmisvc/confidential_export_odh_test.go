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
	"context"

	appsv1 "k8s.io/api/apps/v1"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
)

func (r *LLMISVCReconciler) CombineConfidentialForTest(ctx context.Context, svc *v1alpha2.LLMInferenceService, cfg *Config) error {
	combined, err := r.combineBaseRefsConfig(ctx, svc, cfg)
	if err != nil {
		return err
	}
	svc.Spec = combined.Config.Spec
	svc.Status.AppliedConfigRefs = combined.AppliedConfigRefs
	return nil
}

func (r *LLMISVCReconciler) PreparePlatformWorkloadForTest(ctx context.Context, svc *v1alpha2.LLMInferenceService, cfg *Config) error {
	return r.preparePlatformWorkload(ctx, svc, cfg)
}

func (r *LLMISVCReconciler) ConfidentialDeploymentForTest(ctx context.Context, svc *v1alpha2.LLMInferenceService, cfg *Config) (*appsv1.Deployment, error) {
	return r.expectedSingleNodeMainDeployment(ctx, svc, cfg)
}
