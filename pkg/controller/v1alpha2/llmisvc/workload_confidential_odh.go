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
	"errors"
	"fmt"
	"slices"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
	"github.com/kserve/kserve/pkg/constants"
	"github.com/kserve/kserve/pkg/credentials"
	"github.com/kserve/kserve/pkg/utils"
)

func (r *LLMISVCReconciler) prepareConfidentialWorkload(ctx context.Context, svc *v1alpha2.LLMInferenceService) error {
	if !usesConfidentialBareMetal(svc) || utils.GetForceStopRuntime(svc) {
		return nil
	}
	// A single filesystem cannot be written concurrently by independent guests.
	if (svc.Spec.Replicas != nil && *svc.Spec.Replicas > 1) || svc.Spec.Worker != nil || svc.Spec.Prefill != nil ||
		(svc.Spec.Scaling != nil && svc.Spec.Scaling.MaxReplicas > 1) {
		return errors.New("the confidential bare-metal preset supports one model pod per encrypted PVC; extra replicas, workers, and prefill/decode storage are not implemented")
	}
	if svc.Spec.Model.LoRA != nil || svc.Spec.KVCacheOffloading != nil {
		return errors.New("adapter loading and configured cache offloading are unsupported by the confidential bare-metal preset because their storage is not connected to the encrypted filesystem")
	}
	model := svc.Spec.Model
	if model.URI.String() == "" || model.Confidential == nil || !model.Confidential.Enabled || model.Confidential.ResourceId == nil || *model.Confidential.ResourceId == "" {
		return errors.New("the confidential bare-metal preset requires model.uri, model.confidential.enabled=true, and model.confidential.resourceId")
	}
	pod := svc.Spec.Template
	if pod == nil {
		return errors.New("the confidential bare-metal preset requires template")
	}
	// Validate the merged template before granting access to the privileged SCC.
	if pod.RuntimeClassName == nil || *pod.RuntimeClassName != "kata-cc-nvidia-gpu" {
		return errors.New("the confidential bare-metal preset requires template.runtimeClassName: kata-cc-nvidia-gpu")
	}
	formatter := utils.GetInitContainerWithName(pod, "format-disk")
	initializer := utils.GetInitContainerWithName(pod, constants.StorageInitializerContainerName)
	if formatter == nil || initializer == nil {
		return errors.New("the confidential bare-metal preset requires format-disk and storage-initializer init containers")
	}
	if err := r.validateConfidentialPVC(ctx, svc); err != nil {
		return err
	}
	cfg, err := r.loadConfidentialDefaults(ctx)
	if err != nil {
		return err
	}
	if err := configureConfidentialDiskKey(formatter, cfg.LUKSResourceID); err != nil {
		return err
	}
	if err := r.reconcileConfidentialHelpers(ctx, svc.Namespace); err != nil {
		return err
	}

	// The wrapper, image, resources, and mounts remain the preset's responsibility.
	// Credential resolution runs later on the actual workload pod, as for ordinary services.
	initializer.Args = []string{model.URI.String(), constants.DefaultModelLocalMountPath}
	utils.AddEnvVars(initializer, []corev1.EnvVar{
		{Name: constants.ConfidentialEnabledEnvVar, Value: "true"},
		{Name: constants.ConfidentialResourceIdEnvVar, Value: *model.Confidential.ResourceId},
	})
	if svc.Spec.Annotations == nil {
		svc.Spec.Annotations = map[string]string{}
	}
	if svc.Spec.Annotations[confidentialInitDataAnnotation] == "" && cfg.InitData != "" {
		svc.Spec.Annotations[confidentialInitDataAnnotation] = cfg.InitData
	}
	return nil
}

func (r *LLMISVCReconciler) attachPlatformModelArtifacts(ctx context.Context, sa *corev1.ServiceAccount, svc *v1alpha2.LLMInferenceService, pod *corev1.PodSpec, cfg *Config) (bool, error) {
	if !usesConfidentialBareMetal(svc) {
		return false, nil
	}
	initializer := utils.GetInitContainerWithName(pod, constants.StorageInitializerContainerName)
	if initializer == nil {
		return true, errors.New("missing confidential storage-initializer")
	}
	if sa == nil {
		sa = &corev1.ServiceAccount{}
		if err := r.Get(ctx, client.ObjectKey{Namespace: svc.Namespace, Name: pod.ServiceAccountName}, sa); err != nil {
			return true, err
		}
	}
	builder := credentials.NewCredentialBuilderFromConfig(r.Client, r.Clientset, *cfg.CredentialConfig)
	if err := builder.CreateSecretVolumeAndEnvFromServiceAccount(ctx, sa, svc.Annotations, initializer, &pod.Volumes); err != nil {
		return true, err
	}
	injectCaBundle(svc.Namespace, pod, initializer, cfg.StorageConfig)
	return true, nil
}

func applyPlatformDeploymentStrategy(svc *v1alpha2.LLMInferenceService, deployment *appsv1.Deployment) {
	if usesConfidentialBareMetal(svc) {
		deployment.Spec.Strategy = appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType}
	}
}

// The customer prepares storage; Kubernetes handles binding and scheduling.
func (r *LLMISVCReconciler) validateConfidentialPVC(ctx context.Context, svc *v1alpha2.LLMInferenceService) error {
	volume := confidentialBlockVolume(svc.Spec.Template)
	if volume == nil || volume.ClaimName == "" {
		return fmt.Errorf("set spec.template.volumes[name=luks-block].persistentVolumeClaim.claimName to an existing PVC in namespace %s; create the storage before deploying this service", svc.Namespace)
	}
	if volume.ReadOnly {
		return errors.New("the luks-block PVC must allow writes")
	}
	claim := &corev1.PersistentVolumeClaim{}
	key := client.ObjectKey{Namespace: svc.Namespace, Name: volume.ClaimName}
	if err := r.Get(ctx, key, claim); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("PVC %s does not exist; create the storage before deploying this service: %w", key, err)
		}
		return fmt.Errorf("read encrypted PVC %s: %w", key, err)
	}
	if claim.Spec.VolumeMode == nil || *claim.Spec.VolumeMode != corev1.PersistentVolumeBlock {
		return fmt.Errorf("PVC %s must use volumeMode: Block", key)
	}
	return nil
}

// Index by name rather than array position so unrelated user containers are allowed.
func confidentialBlockVolume(pod *corev1.PodSpec) *corev1.PersistentVolumeClaimVolumeSource {
	if pod == nil {
		return nil
	}
	i := slices.IndexFunc(pod.Volumes, func(v corev1.Volume) bool { return v.Name == "luks-block" })
	if i < 0 {
		return nil
	}
	return pod.Volumes[i].PersistentVolumeClaim
}
