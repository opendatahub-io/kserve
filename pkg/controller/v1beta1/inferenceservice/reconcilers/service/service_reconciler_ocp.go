//go:build distro

/*
Copyright 2021 The KServe Authors.

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
)

// buildServiceList is the distro (OCP) implementation: creates services and then
// applies OpenShift serving-cert annotations and OAuth proxy port rewrites.
func buildServiceList(componentMeta metav1.ObjectMeta, componentExt *v1beta1.ComponentExtensionSpec,
	podSpec *corev1.PodSpec, multiNodeEnabled bool, serviceConfig *v1beta1.ServiceConfig,
) []*corev1.Service {
	svcList := createService(componentMeta, componentExt, podSpec, multiNodeEnabled, serviceConfig)
	if len(svcList) == 0 {
		return svcList
	}

	// Apply OCP serving-cert annotation and port rewrites to the default (head) service.
	applyOCPServiceConfig(svcList[0], componentMeta)

	// Apply OCP serving-cert annotation to the headless head service (multinode only).
	for _, svc := range svcList[1:] {
		if svc.Labels[constants.MultiNodeRoleLabelKey] == constants.MultiNodeHead {
			if svc.Annotations == nil {
				svc.Annotations = make(map[string]string)
			}
			svc.Annotations[constants.OpenshiftServingCertAnnotation] = componentMeta.Name + constants.ServingCertSecretSuffix
		}
	}

	return svcList
}

// applyOCPServiceConfig stamps the OpenShift serving-cert annotation onto the
// service and rewrites ports for InferenceGraph (port 443) or OAuth-enabled
// InferenceServices (HTTPS port replacing the plain HTTP port).
func applyOCPServiceConfig(svc *corev1.Service, componentMeta metav1.ObjectMeta) {
	annotations := make(map[string]string, len(svc.Annotations)+1)
	for k, v := range svc.Annotations {
		annotations[k] = v
	}
	annotations[constants.OpenshiftServingCertAnnotation] = componentMeta.Name + constants.ServingCertSecretSuffix
	svc.Annotations = annotations

	if _, isIG := componentMeta.Labels[constants.InferenceGraphLabel]; isIG {
		if len(svc.Spec.Ports) > 0 {
			svc.Spec.Ports[0].Port = int32(443)
		}
	} else if val, ok := componentMeta.Annotations[constants.ODHKserveRawAuth]; ok && strings.EqualFold(val, "true") {
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
