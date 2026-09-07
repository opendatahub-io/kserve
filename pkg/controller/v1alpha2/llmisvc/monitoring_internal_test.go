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
	"testing"

	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
)

func TestExpectedVLLMEngineMonitorTLS(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := monitoringv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := v1alpha2.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	r := &LLMISVCReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).Build(),
	}
	llmSvc := &v1alpha2.LLMInferenceService{
		ObjectMeta: metav1.ObjectMeta{Name: "test-llm", Namespace: "test"},
	}

	tests := []struct {
		name      string
		enableTLS bool
		scheme    monitoringv1.Scheme
		hasTLS    bool
	}{
		{name: "TLS enabled", enableTLS: true, scheme: monitoringv1.Scheme("https"), hasTLS: true},
		{name: "TLS disabled", enableTLS: false, scheme: monitoringv1.Scheme("http"), hasTLS: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			monitor, err := r.expectedVLLMEngineMonitor(llmSvc, tt.enableTLS)
			if err != nil {
				t.Fatal(err)
			}
			endpoint := monitor.Spec.PodMetricsEndpoints[0]
			if endpoint.Scheme == nil || *endpoint.Scheme != tt.scheme {
				t.Fatalf("scheme = %v, want %q", endpoint.Scheme, tt.scheme)
			}
			if got := endpoint.TLSConfig != nil; got != tt.hasTLS {
				t.Fatalf("TLS config present = %v, want %v", got, tt.hasTLS)
			}
		})
	}
}
