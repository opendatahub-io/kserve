/*
Copyright 2023 The KServe Authors.

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

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/kserve/kserve/pkg/apis/serving/v1beta1"
	"github.com/kserve/kserve/pkg/constants"
	"github.com/kserve/kserve/pkg/hwprofile"
)

// +kubebuilder:rbac:groups=infrastructure.opendatahub.io,resources=hardwareprofiles,verbs=get;list;watch

// applyHardwareProfile resolves the HardwareProfile referenced by the InferenceService annotations
// and applies the scheduling stanzas to podSpec and objectMeta in-memory without mutating the IS.
//
// Parameters:
//   - ctx: Request context
//   - isvc: InferenceService being reconciled
//   - podSpec: Pod spec to modify in-place
//   - objectMeta: Deployment ObjectMeta to modify in-place
func (p *Predictor) applyHardwareProfile(ctx context.Context, isvc *v1beta1.InferenceService, podSpec *corev1.PodSpec, objectMeta *metav1.ObjectMeta) error {
	name, namespace := hwprofile.HardwareProfileRef(isvc.GetAnnotations(), isvc.Namespace)
	if name == "" {
		return nil
	}

	profile, err := hwprofile.Resolve(ctx, p.client, name, namespace)
	if err != nil {
		return fmt.Errorf("HardwareProfile %s/%s: %w", namespace, name, err)
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
