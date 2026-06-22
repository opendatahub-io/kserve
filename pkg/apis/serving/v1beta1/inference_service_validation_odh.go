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

package v1beta1

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
)

// validatePodSpecSecurity validates security-sensitive PodSpec fields in all components
// of an InferenceService. This prevents the InferenceService from acting as a confused
// deputy by blocking fields that could be used to escalate privileges.
func validatePodSpecSecurity(isvc *InferenceService) error {
	// Validate predictor PodSpec
	if err := validatePodSpecSecurityFields(&isvc.Spec.Predictor.PodSpec, "predictor"); err != nil {
		return fmt.Errorf("the InferenceService %q is invalid: %w", isvc.Name, err)
	}

	// Validate predictor WorkerSpec PodSpec
	if isvc.Spec.Predictor.WorkerSpec != nil {
		if err := validatePodSpecSecurityFields(&isvc.Spec.Predictor.WorkerSpec.PodSpec, "predictor.workerSpec"); err != nil {
			return fmt.Errorf("the InferenceService %q is invalid: %w", isvc.Name, err)
		}
	}

	// Validate transformer PodSpec
	if isvc.Spec.Transformer != nil {
		if err := validatePodSpecSecurityFields(&isvc.Spec.Transformer.PodSpec, "transformer"); err != nil {
			return fmt.Errorf("the InferenceService %q is invalid: %w", isvc.Name, err)
		}
	}

	// Validate explainer PodSpec
	if isvc.Spec.Explainer != nil {
		if err := validatePodSpecSecurityFields(&isvc.Spec.Explainer.PodSpec, "explainer"); err != nil {
			return fmt.Errorf("the InferenceService %q is invalid: %w", isvc.Name, err)
		}
	}

	return nil
}

// validatePodSpecSecurityFields checks a single PodSpec for disallowed security-sensitive fields.
func validatePodSpecSecurityFields(podSpec *PodSpec, component string) error {
	if len(podSpec.HostAliases) > 0 {
		return fmt.Errorf("hostAliases are not allowed in %s", component)
	}

	if podSpec.HostNetwork {
		return fmt.Errorf("hostNetwork is not allowed in %s", component)
	}

	if podSpec.HostPID {
		return fmt.Errorf("hostPID is not allowed in %s", component)
	}

	if podSpec.HostIPC {
		return fmt.Errorf("hostIPC is not allowed in %s", component)
	}

	if podSpec.ServiceAccountName != "" {
		return fmt.Errorf("serviceAccountName is not allowed in %s", component)
	}

	if podSpec.DeprecatedServiceAccount != "" {
		return fmt.Errorf("serviceAccount is not allowed in %s", component)
	}

	if len(podSpec.InitContainers) > 0 {
		return fmt.Errorf("initContainers are not allowed in %s", component)
	}

	if err := validateNoProjectedSATokenVolumes(podSpec.Volumes, component); err != nil {
		return err
	}

	if err := validateNoHostPathVolumes(podSpec.Volumes, component); err != nil {
		return err
	}

	return nil
}

// validateNoHostPathVolumes rejects volumes with hostPath sources to prevent
// container breakout via host filesystem access.
func validateNoHostPathVolumes(volumes []corev1.Volume, component string) error {
	for _, vol := range volumes {
		if vol.HostPath != nil {
			return fmt.Errorf(
				"hostPath volume %q in %s is not allowed",
				vol.Name, component,
			)
		}
	}
	return nil
}

// validateNoProjectedSATokenVolumes checks that no user-specified volumes contain
// projected serviceAccountToken sources. This prevents bypassing the
// automountServiceAccountToken: false security control set by KServe.
func validateNoProjectedSATokenVolumes(volumes []corev1.Volume, component string) error {
	for _, vol := range volumes {
		if vol.Projected == nil {
			continue
		}
		for _, src := range vol.Projected.Sources {
			if src.ServiceAccountToken != nil {
				return fmt.Errorf(
					"projected volume %q in %s contains a serviceAccountToken source, which is not allowed",
					vol.Name, component,
				)
			}
		}
	}
	return nil
}
