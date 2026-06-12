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
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"
	lwsapi "sigs.k8s.io/lws/api/leaderworkerset/v1"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
	"github.com/kserve/kserve/pkg/constants"
	"github.com/kserve/kserve/pkg/hwprofile"
)

// setHWPTrackingAnnotation records the resolved HWP name (and cross-namespace namespace, if
// applicable) on the Deployment or LWS ObjectMeta so that the next reconcile can detect
// whether the annotation has changed without re-fetching the HWP CR.
//
// The namespace annotation is omitted when it equals the workload namespace, mirroring the
// HardwareProfileRef convention so that HardwareProfileRef(meta.Annotations, workloadNamespace)
// round-trips cleanly.
func setHWPTrackingAnnotation(meta *metav1.ObjectMeta, name, namespace, workloadNamespace string) {
	if meta.Annotations == nil {
		meta.Annotations = make(map[string]string)
	}
	meta.Annotations[constants.HardwareProfileAnnotationName] = name
	if namespace != workloadNamespace {
		meta.Annotations[constants.HardwareProfileAnnotationNamespace] = namespace
	} else {
		delete(meta.Annotations, constants.HardwareProfileAnnotationNamespace)
	}
}

// applyHardwareProfileToDeployment resolves the HardwareProfile referenced by the LLMInferenceService
// annotations and applies the scheduling stanzas to the given pod spec and object metas in-memory,
// without mutating the LLMInferenceService itself.
//
// Implements a frozen-state mode: when the HWP annotation on the LLMis matches the tracking annotation
// already recorded on the existing Deployment, the stanzas are copied from the existing Deployment
// rather than re-fetched from the HWP CR. This prevents HWP CR content changes from propagating to
// running Deployments until the user explicitly updates the LLMis annotation.
//
// Parameters:
//   - ctx: Request context
//   - llmSvc: LLMInferenceService being reconciled
//   - podSpec: Pod spec to modify in-place
//   - deploymentMeta: Deployment ObjectMeta to modify in-place
//   - podTemplateMeta: Pod template ObjectMeta to modify in-place
//   - curr: Existing Deployment from the cluster, or nil when the Deployment does not yet exist
func (r *LLMISVCReconciler) applyHardwareProfileToDeployment(
	ctx context.Context,
	llmSvc *v1alpha2.LLMInferenceService,
	podSpec *corev1.PodSpec,
	deploymentMeta *metav1.ObjectMeta,
	podTemplateMeta *metav1.ObjectMeta,
	curr *appsv1.Deployment,
) error {
	name, namespace := hwprofile.HardwareProfileRef(llmSvc.GetAnnotations(), llmSvc.GetNamespace())

	// Frozen mode: annotation unchanged and Deployment already exists.
	// Copy stanzas from the existing Deployment rather than re-fetching from the HWP CR,
	// preserving the last-applied values until the user changes the annotation.
	if curr != nil && !hwprofile.AnnotationChanged(llmSvc.GetAnnotations(), llmSvc.GetNamespace(), curr.GetAnnotations()) {
		if name == "" {
			return nil
		}
		hwprofile.CopyContainerResources(ctx, constants.LLMInferenceServiceMainContainerName, &curr.Spec.Template.Spec, podSpec)
		hwprofile.CopyNodeScheduling(&curr.Spec.Template.Spec, podSpec)
		hwprofile.CopyKueueLabel(&curr.ObjectMeta, deploymentMeta)
		hwprofile.CopyKueueLabel(&curr.Spec.Template.ObjectMeta, podTemplateMeta)
		setHWPTrackingAnnotation(deploymentMeta, name, namespace, llmSvc.GetNamespace())
		return nil
	}

	// Fresh mode: annotation changed or Deployment does not yet exist — resolve from the HWP CR.
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
	setHWPTrackingAnnotation(deploymentMeta, name, namespace, llmSvc.GetNamespace())

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
// Implements the same frozen-state mode as applyHardwareProfileToDeployment: when the HWP
// annotation is unchanged, stanzas are copied from the existing LWS rather than re-fetched.
//
// Parameters:
//   - ctx: Request context
//   - llmSvc: LLMInferenceService being reconciled
//   - lws: LeaderWorkerSet to modify in-place
//   - curr: Existing LWS from the cluster, or nil when the LWS does not yet exist
func (r *LLMISVCReconciler) applyHardwareProfileToLWS(
	ctx context.Context,
	llmSvc *v1alpha2.LLMInferenceService,
	lws *lwsapi.LeaderWorkerSet,
	curr *lwsapi.LeaderWorkerSet,
) error {
	name, namespace := hwprofile.HardwareProfileRef(llmSvc.GetAnnotations(), llmSvc.GetNamespace())

	// Frozen mode: annotation unchanged and LWS already exists.
	if curr != nil && !hwprofile.AnnotationChanged(llmSvc.GetAnnotations(), llmSvc.GetNamespace(), curr.GetAnnotations()) {
		if name == "" {
			return nil
		}
		if curr.Spec.LeaderWorkerTemplate.LeaderTemplate != nil && lws.Spec.LeaderWorkerTemplate.LeaderTemplate != nil {
			hwprofile.CopyContainerResources(ctx, constants.LLMInferenceServiceMainContainerName,
				&curr.Spec.LeaderWorkerTemplate.LeaderTemplate.Spec,
				&lws.Spec.LeaderWorkerTemplate.LeaderTemplate.Spec)
			hwprofile.CopyNodeScheduling(
				&curr.Spec.LeaderWorkerTemplate.LeaderTemplate.Spec,
				&lws.Spec.LeaderWorkerTemplate.LeaderTemplate.Spec)
			hwprofile.CopyKueueLabel(
				&curr.Spec.LeaderWorkerTemplate.LeaderTemplate.ObjectMeta,
				&lws.Spec.LeaderWorkerTemplate.LeaderTemplate.ObjectMeta)
		}
		hwprofile.CopyContainerResources(ctx, constants.LLMInferenceServiceMainContainerName,
			&curr.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec,
			&lws.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec)
		hwprofile.CopyNodeScheduling(
			&curr.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec,
			&lws.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec)
		hwprofile.CopyKueueLabel(
			&curr.Spec.LeaderWorkerTemplate.WorkerTemplate.ObjectMeta,
			&lws.Spec.LeaderWorkerTemplate.WorkerTemplate.ObjectMeta)
		hwprofile.CopyKueueLabel(&curr.ObjectMeta, &lws.ObjectMeta)
		setHWPTrackingAnnotation(&lws.ObjectMeta, name, namespace, llmSvc.GetNamespace())
		return nil
	}

	// Fresh mode: annotation changed or LWS does not yet exist — resolve from the HWP CR.
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
	setHWPTrackingAnnotation(&lws.ObjectMeta, name, namespace, llmSvc.GetNamespace())

	log.FromContext(ctx).Info("Applied HardwareProfile to LLMInferenceService LWS",
		"hardwareProfile", fmt.Sprintf("%s/%s", namespace, name),
		"llmInferenceService", fmt.Sprintf("%s/%s", llmSvc.GetNamespace(), llmSvc.GetName()),
	)

	return nil
}

// extendExpectedDeployment applies distribution-specific scheduling stanzas to the
// expected single-node Deployment by resolving the referenced HardwareProfile.
//
// curr is the existing Deployment fetched from the cluster; pass nil when the Deployment
// does not yet exist.
func extendExpectedDeployment(ctx context.Context, r *LLMISVCReconciler, llmSvc *v1alpha2.LLMInferenceService, d *appsv1.Deployment, curr *appsv1.Deployment) error {
	return r.applyHardwareProfileToDeployment(ctx, llmSvc, &d.Spec.Template.Spec, &d.ObjectMeta, &d.Spec.Template.ObjectMeta, curr)
}

// extendExpectedLWS applies distribution-specific scheduling stanzas to an expected
// LeaderWorkerSet by resolving the referenced HardwareProfile.
//
// curr is the existing LWS fetched from the cluster; pass nil when the LWS does not yet exist.
func extendExpectedLWS(ctx context.Context, r *LLMISVCReconciler, llmSvc *v1alpha2.LLMInferenceService, lws *lwsapi.LeaderWorkerSet, curr *lwsapi.LeaderWorkerSet) error {
	return r.applyHardwareProfileToLWS(ctx, llmSvc, lws, curr)
}
