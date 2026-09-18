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

package deployment

import (
	"fmt"
	"strconv"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kserve/kserve/pkg/apis/serving/v1beta1"
	"github.com/kserve/kserve/pkg/constants"
)

// mountTransformerTLSInfrastructure injects the OpenShift service-ca bundle volume and
// TLS endpoint discovery env vars into the transformer deployment's kserve-container.
// This enables the transformer to verify the predictor's TLS certificate and discover
// the predictor's HTTPS endpoint when auth is enabled.
func mountTransformerTLSInfrastructure(deployment *appsv1.Deployment, componentMeta metav1.ObjectMeta) error {
	// Only inject TLS infrastructure when auth is enabled and this is the transformer component.
	authEnabled, ok := componentMeta.Annotations[constants.ODHKserveRawAuth]
	if !ok || !strings.EqualFold(authEnabled, "true") {
		return nil
	}
	componentLabel, ok := componentMeta.Labels[constants.KServiceComponentLabel]
	if !ok || componentLabel != string(v1beta1.TransformerComponent) {
		return nil
	}

	// Validate isvcName before any mutation to avoid orphaned volumes
	isvcName := componentMeta.Labels[constants.InferenceServicePodLabelKey]
	if isvcName == "" {
		return fmt.Errorf("InferenceServicePodLabelKey label missing on transformer deployment %q", componentMeta.Name)
	}

	podSpec := &deployment.Spec.Template.Spec

	// Add openshift-service-ca.crt ConfigMap volume
	podSpec.Volumes = append(podSpec.Volumes, corev1.Volume{
		Name: constants.ServiceCaBundleVolumeName,
		VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: constants.OpenShiftServiceCaConfigMapName,
				},
			},
		},
	})
	predictorHost := fmt.Sprintf("%s.%s.svc",
		constants.PredictorServiceName(isvcName), componentMeta.Namespace)

	// Add volume mount + env vars to kserve-container
	containerFound := false
	for i, container := range podSpec.Containers {
		if container.Name == constants.InferenceServiceContainerName {
			containerFound = true
			podSpec.Containers[i].VolumeMounts = append(
				podSpec.Containers[i].VolumeMounts,
				corev1.VolumeMount{
					Name:      constants.ServiceCaBundleVolumeName,
					MountPath: constants.ServiceCaBundleMountPath,
					ReadOnly:  true,
				},
			)
			podSpec.Containers[i].Env = append(podSpec.Containers[i].Env,
				corev1.EnvVar{
					Name:  "SSL_CERT_DIR",
					Value: constants.ServiceCaBundleMountPath,
				},
				corev1.EnvVar{
					Name:  "REQUESTS_CA_BUNDLE",
					Value: constants.ServiceCaBundleMountPath + "/" + constants.ServiceCaBundleCertFile,
				},
				corev1.EnvVar{
					Name:  constants.PredictorHostEnvVar,
					Value: predictorHost,
				},
				corev1.EnvVar{
					Name:  constants.PredictorPortEnvVar,
					Value: strconv.Itoa(constants.OauthProxyPort),
				},
				corev1.EnvVar{
					Name:  constants.PredictorProtocolEnvVar,
					Value: "https",
				},
			)
			// Inject --predictor_use_ssl=true so the Python SDK uses https:// for predictor_base_url
			podSpec.Containers[i].Args = append(podSpec.Containers[i].Args,
				constants.ArgumentPredictorUseSSL, "true",
			)
			break
		}
	}
	if !containerFound {
		return fmt.Errorf("container %q not found in transformer deployment %q", constants.InferenceServiceContainerName, componentMeta.Name)
	}
	return nil
}

// customizeAuthProxyArgs replaces only controller-managed audit arguments with
// the explicitly resolved effective profile.
func customizeAuthProxyArgs(profile constants.AuditLoggingProfile, manage bool, componentMeta metav1.ObjectMeta, generated []string, isvcName string) []string {
	if !manage {
		return generated
	}
	args := removeManagedAuditArgs(generated)
	return append(args, desiredAuditArgs(profile, componentMeta, isvcName)...)
}

// platformAuthProxyNeedsUpdate reports whether a kube-rbac-proxy's managed
// audit arguments differ from the resolved effective profile.
func platformAuthProxyNeedsUpdate(profile constants.AuditLoggingProfile, manage bool, existing *appsv1.Deployment, componentMeta metav1.ObjectMeta, isvcName string) bool {
	if existing == nil || !manage {
		return false
	}
	desired := desiredAuditArgs(profile, componentMeta, isvcName)
	for _, container := range existing.Spec.Template.Spec.Containers {
		if container.Name == constants.KubeRbacContainerName {
			return !sameArgs(managedAuditArgs(container.Args), desired)
		}
	}
	return false
}

// desiredAuditArgs builds controller-managed arguments for the effective profile.
func desiredAuditArgs(profile constants.AuditLoggingProfile, componentMeta metav1.ObjectMeta, isvcName string) []string {
	if profile != constants.AuditLoggingProfileMetadata {
		return nil
	}
	name := componentMeta.Labels[constants.InferenceServicePodLabelKey]
	if name == "" {
		name = isvcName
	}
	return []string{
		"--audit-log-profile=metadata",
		"--audit-resource-name=" + name,
		"--audit-resource-namespace=" + componentMeta.Namespace,
		"--audit-resource-type=InferenceService",
		"--audit-ai-provider=KServe",
	}
}

// removeManagedAuditArgs removes every kube-rbac-proxy argument in the
// controller-owned audit namespace while leaving unrelated arguments intact.
func removeManagedAuditArgs(args []string) []string {
	filtered := make([]string, 0, len(args))
	for _, arg := range args {
		if !isManagedAuditArg(arg) {
			filtered = append(filtered, arg)
		}
	}
	return filtered
}

// managedAuditArgs returns the controller-owned audit arguments in their
// original order for exact desired-state comparison.
func managedAuditArgs(args []string) []string {
	result := make([]string, 0)
	for _, arg := range args {
		if isManagedAuditArg(arg) {
			result = append(result, arg)
		}
	}
	return result
}

// sameArgs reports whether two ordered argument slices are identical.
func sameArgs(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

// isManagedAuditArg reports whether an argument belongs to the controller-owned
// kube-rbac-proxy audit namespace.
func isManagedAuditArg(arg string) bool {
	name, _, _ := strings.Cut(arg, "=")
	return strings.HasPrefix(name, "--audit-")
}
