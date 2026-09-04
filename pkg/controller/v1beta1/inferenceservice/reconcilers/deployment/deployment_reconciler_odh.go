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

	"github.com/kserve/kserve/pkg/apis/serving/v1beta1"
	"github.com/kserve/kserve/pkg/constants"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr" //nolint:typecheck // build-tag guarded
)

// mountTransformerTLSInfrastructure injects TLS volumes and env vars into the
// transformer deployment's kserve-container. It adds:
//  1. The OpenShift service-ca bundle (CA trust for outbound TLS to the predictor)
//  2. The transformer's own serving certificate (for native HTTPS on port 8443)
//  3. Env vars for predictor endpoint discovery and serving-cert paths
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

	// Add openshift-service-ca.crt ConfigMap volume (CA trust for outbound TLS to predictor)
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

	// Add transformer serving-cert volume (for the transformer's own HTTPS endpoint)
	podSpec.Volumes = append(podSpec.Volumes, corev1.Volume{
		Name: constants.TransformerTLSVolumeName,
		VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{
				SecretName: componentMeta.Name + constants.ServingCertSecretSuffix,
			},
		},
	})

	predictorHost := fmt.Sprintf("%s.%s.svc",
		constants.PredictorServiceName(isvcName), componentMeta.Namespace)

	// Add volume mounts + env vars to kserve-container
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
				corev1.VolumeMount{
					Name:      constants.TransformerTLSVolumeName,
					MountPath: constants.TransformerTLSMountPath,
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
				corev1.EnvVar{
					Name:  constants.TransformerTLSCertEnvVar,
					Value: constants.TransformerTLSMountPath + "/tls.crt",
				},
				corev1.EnvVar{
					Name:  constants.TransformerTLSKeyEnvVar,
					Value: constants.TransformerTLSMountPath + "/tls.key",
				},
			)
			// Inject --predictor_use_ssl=true so the Python SDK uses https:// for predictor_base_url
			podSpec.Containers[i].Args = append(podSpec.Containers[i].Args,
				constants.ArgumentPredictorUseSSL, "true",
			)

			// Determine the serving port. GetContainer() auto-injects
			// "--http_port 8080" before this hook runs, so the framework
			// default is indistinguishable from "absent". Override it to
			// the HTTPS port; if the user explicitly set a non-default
			// port, respect their choice.
			servingPort := constants.TransformerHTTPSPort
			if userPort, ok := getArgValue(podSpec.Containers[i].Args, constants.ArgumentHttpPort); ok {
				if userPort != constants.InferenceServiceDefaultHttpPort {
					if parsed, err := strconv.ParseInt(userPort, 10, 32); err == nil {
						servingPort = int32(parsed)
					}
				}
			}
			podSpec.Containers[i].Args = setArgValue(podSpec.Containers[i].Args,
				constants.ArgumentHttpPort, strconv.Itoa(int(servingPort)))

			podSpec.Containers[i].Ports = append(podSpec.Containers[i].Ports, corev1.ContainerPort{
				ContainerPort: servingPort,
				Protocol:      corev1.ProtocolTCP,
			})

			// Patch existing probes to target the serving port.
			// setDefaultPodSpec() already created a readiness probe on the --http_port
			// value (8080 by default) before this hook runs; update it to match the
			// actual listening port after the override above.
			portVal := intstr.IntOrString{IntVal: servingPort}
			if podSpec.Containers[i].ReadinessProbe != nil && podSpec.Containers[i].ReadinessProbe.TCPSocket != nil {
				podSpec.Containers[i].ReadinessProbe.TCPSocket.Port = portVal
			}
			if podSpec.Containers[i].LivenessProbe != nil && podSpec.Containers[i].LivenessProbe.TCPSocket != nil {
				podSpec.Containers[i].LivenessProbe.TCPSocket.Port = portVal
			}
			break
		}
	}
	if !containerFound {
		return fmt.Errorf("container %q not found in transformer deployment %q", constants.InferenceServiceContainerName, componentMeta.Name)
	}
	return nil
}
