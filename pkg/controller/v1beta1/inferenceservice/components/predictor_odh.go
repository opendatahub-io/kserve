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
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/kserve/kserve/pkg/apis/serving/v1beta1"
	"github.com/kserve/kserve/pkg/constants"
	"github.com/kserve/kserve/pkg/hwprofile"
)

// applyHardwareProfile resolves the HardwareProfile referenced by the InferenceService annotations
// and applies the scheduling stanzas to podSpec and objectMeta in-memory without mutating the IS.
//
// Implements a frozen-state mode: when the HWP annotation on the IS matches the annotation already
// recorded on the existing Deployment, the stanzas are copied from the existing Deployment rather
// than re-fetched from the HWP CR. This prevents HWP CR content changes from propagating to running
// Deployments until the user explicitly updates the IS annotation.
//
// Parameters:
//   - ctx: Request context
//   - isvc: InferenceService being reconciled
//   - podSpec: Pod spec to modify in-place
//   - objectMeta: Deployment ObjectMeta to modify in-place
func (p *Predictor) applyHardwareProfile(ctx context.Context, isvc *v1beta1.InferenceService, podSpec *corev1.PodSpec, objectMeta *metav1.ObjectMeta) error {
	// Fetch the existing Deployment to determine whether the HWP annotation has changed.
	// The controller-runtime client serves this from its informer cache — no extra API-server round-trip.
	existing := &appsv1.Deployment{}
	if err := p.client.Get(ctx, types.NamespacedName{
		Name: objectMeta.Name, Namespace: objectMeta.Namespace,
	}, existing); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed fetching Deployment %s/%s for HWP annotation check: %w", objectMeta.Namespace, objectMeta.Name, err)
		}
		existing = nil
	}

	name, namespace := hwprofile.HardwareProfileRef(isvc.GetAnnotations(), isvc.Namespace)

	// Frozen mode: annotation unchanged and Deployment already exists.
	// Copy stanzas from the existing Deployment rather than re-fetching from the HWP CR,
	// preserving the last-applied values until the user changes the annotation.
	if existing != nil && !hwprofile.AnnotationChanged(isvc.GetAnnotations(), isvc.Namespace, existing.GetAnnotations()) {
		if name == "" {
			return nil
		}
		hwprofile.CopyContainerResources(ctx, constants.InferenceServiceContainerName, &existing.Spec.Template.Spec, podSpec)
		hwprofile.CopyNodeScheduling(&existing.Spec.Template.Spec, podSpec)
		hwprofile.CopyKueueLabel(&existing.ObjectMeta, objectMeta)
		return nil
	}

	// Fresh mode: annotation changed or Deployment does not yet exist — resolve from the HWP CR.
	if name == "" {
		return nil
	}

	profile, err := hwprofile.Resolve(ctx, p.client, name, namespace)
	if err != nil {
		return err
	}
	if profile == nil {
		return nil
	}

	hwprofile.ApplyToContainerResources(ctx, profile, constants.InferenceServiceContainerName, podSpec)
	hwprofile.ApplyNodeScheduling(profile, podSpec)
	hwprofile.ApplyKueueLabel(profile, objectMeta)

	log.FromContext(ctx).Info("Applied HardwareProfile to InferenceService predictor",
		"hardwareProfile", fmt.Sprintf("%s/%s", namespace, name),
		"inferenceService", fmt.Sprintf("%s/%s", isvc.Namespace, isvc.Name),
	)

	return nil
}

// extendRawDeploymentSpec applies distribution-specific scheduling stanzas to
// the pod spec and object meta of a raw Deployment by resolving the referenced HardwareProfile.
func extendRawDeploymentSpec(ctx context.Context, p *Predictor, isvc *v1beta1.InferenceService, podSpec *corev1.PodSpec, objectMeta *metav1.ObjectMeta) error {
	return p.applyHardwareProfile(ctx, isvc, podSpec, objectMeta)
}
