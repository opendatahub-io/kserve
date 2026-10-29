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
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
)

// configureSchedulerMetricsProtocol advertises the metrics listener's transport.
// EPP's gRPC secure-serving flag does not enable TLS on the metrics listener.
func configureSchedulerMetricsProtocol(service *corev1.Service, pod *corev1.PodSpec) {
	protocol := "http"
	if schedulerMetricsTLSEnabled(pod) {
		protocol = "https"
	}
	for i := range service.Spec.Ports {
		if service.Spec.Ports[i].Name == "metrics" {
			service.Spec.Ports[i].AppProtocol = ptr.To(protocol)
		}
	}
}

// schedulerMetricsTLSEnabled reads the effective listener flag from the metrics container.
func schedulerMetricsTLSEnabled(pod *corev1.PodSpec) bool {
	if pod == nil {
		return false
	}
	for _, container := range pod.Containers {
		if !slices.ContainsFunc(container.Ports, func(port corev1.ContainerPort) bool {
			return port.Name == "metrics"
		}) {
			continue
		}
		args := slices.Concat(container.Command, container.Args)
		directory := ""
		for i := 0; i < len(args); i++ {
			arg := args[i]
			if arg == "--" {
				break
			}
			if value, found := strings.CutPrefix(arg, "--metrics-cert-dir="); found {
				directory = value
			} else if arg == "--metrics-cert-dir" && i+1 < len(args) {
				i++
				directory = args[i]
			}
		}
		return directory != ""
	}
	return false
}
