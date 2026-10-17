//go:build distro

/*
Copyright 2025 The KServe Authors.

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

package service

import (
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/kserve/kserve/pkg/apis/serving/v1beta1"
	"github.com/kserve/kserve/pkg/constants"
	"github.com/kserve/kserve/pkg/utils"
)

// customizeService applies ODH-specific customizations to the default service:
//   - Adds the OpenShift serving certificate annotation for automatic TLS provisioning
//   - Overrides the service port to 443 for InferenceGraph resources (detected via
//     constants.InferenceGraphLabel set by the InferenceGraph controller in raw_ig.go)
//   - Replaces the default HTTP port with an HTTPS port when auth proxy is enabled
//
// podSpec is used to resolve the transformer's actual serving port so the Service
// TargetPort stays aligned with the pod's listener (see transformerServingPort).
func customizeService(svc *corev1.Service, componentMeta metav1.ObjectMeta, podSpec *corev1.PodSpec) {
	// Default unnamed ports to "http" - OpenShift Routes and Service Mesh require named ports.
	for i := range svc.Spec.Ports {
		if len(svc.Spec.Ports[i].Name) == 0 {
			svc.Spec.Ports[i].Name = "http"
		}
	}

	// Add OpenShift serving cert annotation for automatic TLS certificate provisioning.
	if svc.Annotations == nil {
		svc.Annotations = make(map[string]string)
	}
	svc.Annotations[constants.OpenshiftServingCertAnnotation] = componentMeta.Name + constants.ServingCertSecretSuffix

	// InferenceGraph services use port 443 for TLS termination.
	// Auth proxy port override is skipped for InferenceGraph - TLS is handled at the gateway level.
	if _, isIG := componentMeta.Labels[constants.InferenceGraphLabel]; isIG {
		if len(svc.Spec.Ports) > 0 {
			svc.Spec.Ports[0].Port = int32(443)
		}
		return
	}

	isTransformer := componentMeta.Labels[constants.KServiceComponentLabel] == string(v1beta1.TransformerComponent)
	authEnabled := false
	if val, ok := componentMeta.Annotations[constants.ODHKserveRawAuth]; ok && strings.EqualFold(val, "true") {
		authEnabled = true
	}

	// When auth proxy is enabled on the predictor, replace the default HTTP port
	// with the HTTPS proxy port (kube-rbac-proxy sidecar).
	if authEnabled && !isTransformer {
		httpsPort := corev1.ServicePort{
			Name: "https",
			Port: constants.OauthProxyPort,
			TargetPort: intstr.IntOrString{
				Type:   intstr.String,
				StrVal: "https",
			},
			Protocol: corev1.ProtocolTCP,
		}
		ports := svc.Spec.Ports
		replaced := false
		for i, port := range ports {
			if port.Port == constants.CommonDefaultHttpPort {
				ports[i] = httpsPort
				replaced = true
			}
		}
		if !replaced {
			ports = append(ports, httpsPort)
		}
		svc.Spec.Ports = ports
	}

	// When auth is enabled on the transformer, expose an "https" port so
	// odh-model-controller's setRouteTargetPort() can find it and configure reencrypt
	// termination. The Service .Port stays at the HTTPS port (8443) OCP expects, while
	// .TargetPort points to the transformer's actual serving port (honoring a user-set
	// --http_port), keeping the Service aligned with the pod's listener on the deployment side.
	if authEnabled && isTransformer {
		httpsPort := corev1.ServicePort{
			Name: "https",
			Port: constants.TransformerHTTPSPort,
			TargetPort: intstr.IntOrString{
				Type:   intstr.Int,
				IntVal: transformerServingPort(podSpec),
			},
			Protocol: corev1.ProtocolTCP,
		}
		ports := svc.Spec.Ports
		replaced := false
		for i, port := range ports {
			if port.Port == constants.CommonDefaultHttpPort {
				ports[i] = httpsPort
				replaced = true
			}
		}
		if !replaced {
			ports = append(ports, httpsPort)
		}
		svc.Spec.Ports = ports
	}
}

// transformerServingPort resolves the transformer container's actual serving
// port so the Service TargetPort stays aligned with the pod's listener.
//
// GetContainer() auto-injects "--http_port 8080" before the deployment hook runs;
// that hook overrides it to the HTTPS port (8443) unless the user set a non-default
// --http_port. This mirrors that resolution.
func transformerServingPort(podSpec *corev1.PodSpec) int32 {
	servingPort := constants.TransformerHTTPSPort
	if podSpec == nil {
		return servingPort
	}
	container := utils.GetContainerWithName(podSpec, constants.InferenceServiceContainerName)
	if container == nil {
		return servingPort
	}
	if userPort, ok := getArgValue(container.Args, constants.ArgumentHttpPort); ok {
		if userPort != constants.InferenceServiceDefaultHttpPort {
			if parsed, ok := utils.ParsePort(userPort); ok {
				servingPort = parsed
			}
		}
	}
	return servingPort
}

// getArgValue extracts the value for a CLI flag from an args slice, handling both
// "--flag value" and "--flag=value" forms.
//
// TODO(RHOAIENG-95631): duplicated from the unexported helper in the deployment
// package (deployment_reconciler.go). De-duplicate by promoting a shared exported
// helper (e.g. in pkg/utils) once upstream/downstream can be updated together.
func getArgValue(args []string, flag string) (string, bool) {
	var lastVal string
	found := false
	for i, arg := range args {
		if arg == flag && i+1 < len(args) {
			lastVal = args[i+1]
			found = true
		}
		if strings.HasPrefix(arg, flag+"=") {
			lastVal = strings.TrimPrefix(arg, flag+"=")
			found = true
		}
	}
	return lastVal, found
}

// customizeHeadSvc applies ODH-specific customizations to headless (head node) services:
// - Adds the OpenShift serving certificate annotation using the predictor service name
func customizeHeadSvc(svc *corev1.Service, predictorSvcName string) {
	if svc.Annotations == nil {
		svc.Annotations = make(map[string]string)
	}
	svc.Annotations[constants.OpenshiftServingCertAnnotation] = predictorSvcName + constants.ServingCertSecretSuffix
}
