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
	"context"
	"fmt"
	"strconv"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"

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

func customizeAuthProxyArgs(componentMeta metav1.ObjectMeta, generated []string, isvcName string) []string {
	args := removeManagedAuditArgs(generated)
	if strings.EqualFold(componentMeta.Annotations[constants.ODHKserveAuditLogging], "true") {
		args = append(args, desiredAuditArgs(componentMeta, isvcName)...)
	}
	return args
}

func resolvePlatformAuthProxyMetadata(ctx context.Context, client kclient.Client, componentMeta metav1.ObjectMeta, existing *appsv1.Deployment, isvcName string) (metav1.ObjectMeta, error) {
	isvc := &v1beta1.InferenceService{}
	if err := client.Get(ctx, types.NamespacedName{Namespace: componentMeta.Namespace, Name: isvcName}, isvc); err != nil {
		return componentMeta, fmt.Errorf("get InferenceService %s/%s: %w", componentMeta.Namespace, isvcName, err)
	}

	resolved := *componentMeta.DeepCopy()
	if resolved.Annotations == nil {
		resolved.Annotations = map[string]string{}
	}
	if auditValue, present := isvc.Annotations[constants.ODHKserveAuditLogging]; present {
		resolved.Annotations[constants.ODHKserveAuditLogging] = auditValue
		return resolved, nil
	}
	if !hasAuthProxy(existing) {
		resolved.Annotations[constants.ODHKserveAuditLogging] = "false"
		return resolved, nil
	}

	// Legacy services predate the persisted audit annotation. Snapshot only a complete,
	// correctly attributed legacy configuration, then use the parent ISVC as the source
	// of truth for this and all subsequent reconciliations.
	auditEnabled := validLegacyAuditConfiguration(existing, isvcName, componentMeta.Namespace)
	if auditEnabled && (!strings.EqualFold(isvc.Annotations[constants.ODHKserveRawAuth], "true") ||
		isvc.Annotations[constants.DeploymentMode] != string(constants.Standard)) {
		auditEnabled = false
	}
	auditValue := strconv.FormatBool(auditEnabled)
	original := isvc.DeepCopy()
	if isvc.Annotations == nil {
		isvc.Annotations = map[string]string{}
	}
	isvc.Annotations[constants.ODHKserveAuditLogging] = auditValue
	if err := client.Patch(ctx, isvc, kclient.MergeFrom(original)); err != nil {
		return componentMeta, fmt.Errorf("persist audit logging setting on InferenceService %s/%s: %w", componentMeta.Namespace, isvcName, err)
	}
	resolved.Annotations[constants.ODHKserveAuditLogging] = auditValue
	return resolved, nil
}

func hasAuthProxy(deployment *appsv1.Deployment) bool {
	if deployment == nil {
		return false
	}
	for _, container := range deployment.Spec.Template.Spec.Containers {
		if container.Name == constants.KubeRbacContainerName || container.Name == constants.OauthProxyContainerName {
			return true
		}
	}
	return false
}

func platformAuthProxyNeedsUpdate(componentMeta metav1.ObjectMeta, existing *appsv1.Deployment, isvcName string) bool {
	if existing == nil {
		return false
	}
	desired := desiredAuditArgs(componentMeta, isvcName)
	for _, container := range existing.Spec.Template.Spec.Containers {
		if container.Name == constants.KubeRbacContainerName || container.Name == constants.OauthProxyContainerName {
			return !sameArgs(managedAuditArgs(container.Args), desired)
		}
	}
	return len(desired) > 0
}

func desiredAuditArgs(componentMeta metav1.ObjectMeta, isvcName string) []string {
	if !strings.EqualFold(componentMeta.Annotations[constants.ODHKserveAuditLogging], "true") {
		return nil
	}
	name := componentMeta.Labels[constants.InferenceServicePodLabelKey]
	if name == "" {
		name = isvcName
	}
	return []string{
		"--audit-log-enabled",
		"--audit-isvc-name=" + name,
		"--audit-isvc-namespace=" + componentMeta.Namespace,
	}
}

func validLegacyAuditConfiguration(existing *appsv1.Deployment, isvcName, namespace string) bool {
	if existing == nil {
		return false
	}
	want := []string{
		"--audit-log-enabled",
		"--audit-isvc-name=" + isvcName,
		"--audit-isvc-namespace=" + namespace,
	}
	for _, container := range existing.Spec.Template.Spec.Containers {
		if container.Name == constants.KubeRbacContainerName || container.Name == constants.OauthProxyContainerName {
			legacyArgs := removeArgByName(managedAuditArgs(container.Args), "--audit-use-forwarded-for")
			return sameArgs(legacyArgs, want)
		}
	}
	return false
}

func removeArgByName(args []string, unwanted string) []string {
	filtered := make([]string, 0, len(args))
	for _, arg := range args {
		name, _, _ := strings.Cut(arg, "=")
		if name != unwanted {
			filtered = append(filtered, arg)
		}
	}
	return filtered
}

func removeManagedAuditArgs(args []string) []string {
	filtered := make([]string, 0, len(args))
	for _, arg := range args {
		if !isManagedAuditArg(arg) {
			filtered = append(filtered, arg)
		}
	}
	return filtered
}

func managedAuditArgs(args []string) []string {
	result := make([]string, 0)
	for _, arg := range args {
		if isManagedAuditArg(arg) {
			result = append(result, arg)
		}
	}
	return result
}

func sameArgs(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	counts := make(map[string]int, len(left))
	for _, arg := range left {
		counts[arg]++
	}
	for _, arg := range right {
		if counts[arg] == 0 {
			return false
		}
		counts[arg]--
	}
	return true
}

func isManagedAuditArg(arg string) bool {
	name, _, _ := strings.Cut(arg, "=")
	switch name {
	case "--audit-log-enabled", "--audit-isvc-name", "--audit-isvc-namespace", "--audit-use-forwarded-for":
		return true
	default:
		return false
	}
}
