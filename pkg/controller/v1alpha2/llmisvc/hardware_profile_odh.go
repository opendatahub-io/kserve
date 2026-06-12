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
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	lwsapi "sigs.k8s.io/lws/api/leaderworkerset/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
	"github.com/kserve/kserve/pkg/constants"
	"github.com/kserve/kserve/pkg/hwprofile"
)

// applyHardwareProfileToDeployment resolves the HardwareProfile referenced by the LLMInferenceService
// annotations and applies the scheduling stanzas to the given pod spec and object metas in-memory,
// without mutating the LLMInferenceService itself.
//
// Parameters:
//   - ctx: Request context
//   - llmSvc: LLMInferenceService being reconciled
//   - podSpec: Pod spec to modify in-place
//   - deploymentMeta: Deployment ObjectMeta to modify in-place
//   - podTemplateMeta: Pod template ObjectMeta to modify in-place
func (r *LLMISVCReconciler) applyHardwareProfileToDeployment(
	ctx context.Context,
	llmSvc *v1alpha2.LLMInferenceService,
	podSpec *corev1.PodSpec,
	deploymentMeta *metav1.ObjectMeta,
	podTemplateMeta *metav1.ObjectMeta,
) error {
	name, namespace := hwprofile.HardwareProfileRef(llmSvc.GetAnnotations(), llmSvc.GetNamespace())
	if name == "" {
		return nil
	}

	profile, err := hwprofile.Resolve(ctx, r.Client, name, namespace)
	if err != nil {
		return err
	}
	if profile == nil {
		return nil
	}

	hwprofile.ApplyToContainerResources(ctx, profile, constants.LLMInferenceServiceMainContainerName, podSpec)
	hwprofile.ApplyNodeScheduling(profile, podSpec)
	hwprofile.ApplyKueueLabel(profile, deploymentMeta)
	hwprofile.ApplyKueueLabel(profile, podTemplateMeta)

	log.FromContext(ctx).Info("Applied HardwareProfile to LLMInferenceService deployment",
		"hardwareProfile", fmt.Sprintf("%s/%s", namespace, name),
		"llmInferenceService", fmt.Sprintf("%s/%s", llmSvc.GetNamespace(), llmSvc.GetName()),
	)

	return nil
}

// applyHardwareProfileToLWS resolves the HardwareProfile referenced by the LLMInferenceService
// annotations and applies the scheduling stanzas to the LeaderWorkerSet leader and worker pod
// templates and the LWS ObjectMeta in-memory.
//
// Parameters:
//   - ctx: Request context
//   - llmSvc: LLMInferenceService being reconciled
//   - lws: LeaderWorkerSet to modify in-place
func (r *LLMISVCReconciler) applyHardwareProfileToLWS(
	ctx context.Context,
	llmSvc *v1alpha2.LLMInferenceService,
	lws *lwsapi.LeaderWorkerSet,
) error {
	name, namespace := hwprofile.HardwareProfileRef(llmSvc.GetAnnotations(), llmSvc.GetNamespace())
	if name == "" {
		return nil
	}

	profile, err := hwprofile.Resolve(ctx, r.Client, name, namespace)
	if err != nil {
		return err
	}
	if profile == nil {
		return nil
	}

	// Apply resources and node scheduling to leader pod spec if present
	if lws.Spec.LeaderWorkerTemplate.LeaderTemplate != nil {
		hwprofile.ApplyToContainerResources(ctx, profile, constants.LLMInferenceServiceMainContainerName, &lws.Spec.LeaderWorkerTemplate.LeaderTemplate.Spec)
		hwprofile.ApplyNodeScheduling(profile, &lws.Spec.LeaderWorkerTemplate.LeaderTemplate.Spec)
		hwprofile.ApplyKueueLabel(profile, &lws.Spec.LeaderWorkerTemplate.LeaderTemplate.ObjectMeta)
	}

	// Apply resources and node scheduling to worker pod spec
	hwprofile.ApplyToContainerResources(ctx, profile, constants.LLMInferenceServiceMainContainerName, &lws.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec)
	hwprofile.ApplyNodeScheduling(profile, &lws.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec)
	hwprofile.ApplyKueueLabel(profile, &lws.Spec.LeaderWorkerTemplate.WorkerTemplate.ObjectMeta)

	// Apply Kueue label to LWS top-level metadata
	hwprofile.ApplyKueueLabel(profile, &lws.ObjectMeta)

	log.FromContext(ctx).Info("Applied HardwareProfile to LLMInferenceService LWS",
		"hardwareProfile", fmt.Sprintf("%s/%s", namespace, name),
		"llmInferenceService", fmt.Sprintf("%s/%s", llmSvc.GetNamespace(), llmSvc.GetName()),
	)

	return nil
}
