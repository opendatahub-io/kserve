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
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
)

func TestSchedulerMetricsServiceProtocol(t *testing.T) {
	tests := []struct {
		name    string
		command []string
		args    []string
		want    string
	}{
		{name: "plaintext default", command: []string{"/app/epp"}, want: "http"},
		{name: "gRPC TLS alone", command: []string{"/app/epp", "--secure-serving=true", "--cert-path=/tls"}, want: "http"},
		{name: "preset TLS flag", command: []string{"/app/epp", "--metrics-cert-dir=/tls"}, want: "https"},
		{name: "separate argument", command: []string{"/app/epp"}, args: []string{"--metrics-cert-dir", "/tls"}, want: "https"},
		{name: "args override command", command: []string{"/app/epp", "--metrics-cert-dir=/tls"}, args: []string{"--metrics-cert-dir="}, want: "http"},
		{name: "empty directory", command: []string{"/app/epp", "--metrics-cert-dir", ""}, want: "http"},
		{name: "gRPC disabled with metrics TLS", command: []string{"/app/epp", "--secure-serving=false"}, args: []string{"--metrics-cert-dir=/tls"}, want: "https"},
		{name: "positional argument is not a flag", command: []string{"/app/epp", "--", "--metrics-cert-dir=/tls"}, want: "http"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model := &v1alpha2.LLMInferenceService{
				ObjectMeta: metav1.ObjectMeta{Name: "model", Namespace: "models"},
				Spec: v1alpha2.LLMInferenceServiceSpec{Router: &v1alpha2.RouterSpec{
					Scheduler: &v1alpha2.SchedulerSpec{Template: &corev1.PodSpec{
						Containers: []corev1.Container{{
							Name: "main", Command: tt.command, Args: tt.args,
							Ports: []corev1.ContainerPort{{Name: "metrics", ContainerPort: 9090}, {Name: "grpc", ContainerPort: 9002}},
						}},
					}},
				}},
			}
			before := model.DeepCopy()
			service := (&LLMISVCReconciler{}).expectedSchedulerService(context.Background(), model)
			found := false
			for _, port := range service.Spec.Ports {
				if port.Name == "metrics" {
					found = true
					if port.AppProtocol == nil || *port.AppProtocol != tt.want {
						t.Errorf("metrics appProtocol = %v, want %q", port.AppProtocol, tt.want)
					}
				} else if port.AppProtocol != nil {
					t.Errorf("unexpected appProtocol on %s: %s", port.Name, *port.AppProtocol)
				}
			}
			monitor := (&LLMISVCReconciler{}).expectedSchedulerMonitor(model)
			endpoint := monitor.Spec.Endpoints[0]
			if endpoint.Scheme == nil || string(*endpoint.Scheme) != tt.want {
				t.Errorf("monitor scheme = %v, want %q", endpoint.Scheme, tt.want)
			}
			if (endpoint.TLSConfig != nil) != (tt.want == "https") {
				t.Errorf("monitor TLS config does not match metrics protocol %q", tt.want)
			}
			if !found {
				t.Fatal("scheduler Service has no metrics port")
			}
			if !reflect.DeepEqual(model, before) {
				t.Fatal("protocol discovery mutated the scheduler spec")
			}
		})
	}
}

func TestSchedulerMonitorMissingTemplate(t *testing.T) {
	for _, router := range []*v1alpha2.RouterSpec{nil, {}, {Scheduler: &v1alpha2.SchedulerSpec{}}} {
		model := &v1alpha2.LLMInferenceService{Spec: v1alpha2.LLMInferenceServiceSpec{Router: router}}
		monitor := (&LLMISVCReconciler{}).expectedSchedulerMonitor(model)
		endpoint := monitor.Spec.Endpoints[0]
		if endpoint.Scheme == nil || *endpoint.Scheme != "http" || endpoint.TLSConfig != nil {
			t.Fatal("missing scheduler template must not enable metrics TLS")
		}
	}
}
