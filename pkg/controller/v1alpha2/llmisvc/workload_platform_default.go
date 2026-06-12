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

	appsv1 "k8s.io/api/apps/v1"
	lwsapi "sigs.k8s.io/lws/api/leaderworkerset/v1"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
)

// extendExpectedDeployment is a hook for distribution-specific extensions to
// the expected single-node Deployment, such as injecting platform scheduling stanzas.
func extendExpectedDeployment(_ context.Context, _ *LLMISVCReconciler, _ *v1alpha2.LLMInferenceService, _ *appsv1.Deployment) error {
	return nil
}

// extendExpectedLWS is a hook for distribution-specific extensions to an
// expected LeaderWorkerSet, such as injecting platform scheduling stanzas.
func extendExpectedLWS(_ context.Context, _ *LLMISVCReconciler, _ *v1alpha2.LLMInferenceService, _ *lwsapi.LeaderWorkerSet) error {
	return nil
}
