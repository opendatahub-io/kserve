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

	"github.com/kserve/kserve/pkg/constants"
)

// customizeService applies ODH-specific customizations to the default service:
//   - Adds the OpenShift serving certificate annotation for automatic TLS provisioning
//   - Overrides the service port to 443 for InferenceGraph resources (detected via
//     constants.InferenceGraphLabel set by the InferenceGraph controller in raw_ig.go)
//   - Replaces the default HTTP port with an HTTPS port when auth proxy is enabled
func customizeService(svc *corev1.Service, componentMeta metav1.ObjectMeta) {
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

	// When auth proxy is enabled, replace the default HTTP port with the HTTPS proxy port.
	if val, ok := componentMeta.Annotations[constants.ODHKserveRawAuth]; ok && strings.EqualFold(val, "true") {
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
}

// customizeHeadSvc applies ODH-specific customizations to headless (head node) services:
// - Adds the OpenShift serving certificate annotation using the predictor service name
func customizeHeadSvc(svc *corev1.Service, predictorSvcName string) {
	if svc.Annotations == nil {
		svc.Annotations = make(map[string]string)
	}
	svc.Annotations[constants.OpenshiftServingCertAnnotation] = predictorSvcName + constants.ServingCertSecretSuffix
}
