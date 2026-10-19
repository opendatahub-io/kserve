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
	"errors"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"knative.dev/pkg/kmeta"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
	"github.com/kserve/kserve/pkg/constants"
)

func TestIngressNamespaceUsesConfigNotOpenShiftFallback(t *testing.T) {
	t.Parallel()

	if got, want := ingressNamespace(&Config{IngressGatewayNamespace: "opendatahub"}), "opendatahub"; got != want {
		t.Fatalf("xKS-style config: got %q want %q", got, want)
	}
	if got, want := ingressNamespace(&Config{IngressGatewayNamespace: "openshift-ingress"}), "openshift-ingress"; got != want {
		t.Fatalf("OCP-style config: got %q want %q", got, want)
	}
	if got, want := ingressNamespace(nil), constants.KServeNamespace; got != want {
		t.Fatalf("nil config: got %q want %q (must not be openshift-ingress)", got, want)
	}
	if got, want := ingressNamespace(&Config{}), constants.KServeNamespace; got != want {
		t.Fatalf("empty config: got %q want %q (must not be openshift-ingress)", got, want)
	}
}

func TestMonitoringNetworkPolicyNameIsPerService(t *testing.T) {
	t.Parallel()

	llmSvc := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{Name: "svc-a", Namespace: "team-a"}}
	got := monitoringNetworkPolicyName(llmSvc)
	want := kmeta.ChildName("svc-a", "-prometheus-scraping")
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestGatewayPeerNamespaces(t *testing.T) {
	t.Parallel()

	managed := "openshift-ingress"
	svcNs := "team-a"

	llmSvc := func(refs ...v1alpha2.GatewayObjectReference) *v1alpha2.LLMInferenceService {
		svc := &v1alpha2.LLMInferenceService{
			ObjectMeta: metav1.ObjectMeta{Name: "svc-a", Namespace: svcNs},
		}
		if len(refs) > 0 {
			svc.Spec.Router = &v1alpha2.RouterSpec{
				Gateway: &v1alpha2.GatewaySpec{Refs: refs},
			}
		}
		return svc
	}
	ref := func(name, ns string) v1alpha2.GatewayObjectReference {
		return v1alpha2.GatewayObjectReference{
			UntypedObjectReference: v1alpha2.UntypedObjectReference{
				Name:      gwapiv1.ObjectName(name),
				Namespace: gwapiv1.Namespace(ns),
			},
		}
	}

	tests := []struct {
		name string
		svc  *v1alpha2.LLMInferenceService
		want []string
	}{
		{
			name: "managed only",
			svc:  llmSvc(),
			want: []string{managed},
		},
		{
			name: "same-namespace custom ref is skipped",
			svc:  llmSvc(ref("my-gw", svcNs)),
			want: []string{managed},
		},
		{
			name: "empty ref namespace defaults to service namespace and is skipped",
			svc:  llmSvc(ref("my-gw", "")),
			want: []string{managed},
		},
		{
			name: "cross-namespace custom ref is added",
			svc:  llmSvc(ref("my-gw", "other-ns")),
			want: []string{managed, "other-ns"},
		},
		{
			name: "duplicate managed namespace is not repeated",
			svc:  llmSvc(ref("my-gw", managed)),
			want: []string{managed},
		},
		{
			name: "two cross-namespace refs",
			svc:  llmSvc(ref("gw-a", "ns-a"), ref("gw-b", "ns-b")),
			want: []string{managed, "ns-a", "ns-b"},
		},
		{
			name: "nil service still has managed peer",
			svc:  nil,
			want: []string{managed},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := gatewayPeerNamespaces(tt.svc, managed)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %v want %v", got, tt.want)
			}
		})
	}
}

func TestPrometheusPeerNamespacesIncludesRHOAIDefault(t *testing.T) {
	t.Setenv(monitoringNamespaceEnvVar, "")

	got := prometheusPeerNamespaces()
	want := []string{
		defaultMonitoringNamespace,
		defaultUserWorkloadMonitoringNamespace,
		defaultRHOAIMonitoringNamespace,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestPrometheusPeerNamespacesAddsCustomMonitoringNamespace(t *testing.T) {
	t.Setenv(monitoringNamespaceEnvVar, "custom-monitoring")

	got := prometheusPeerNamespaces()
	want := []string{
		defaultMonitoringNamespace,
		defaultUserWorkloadMonitoringNamespace,
		defaultRHOAIMonitoringNamespace,
		"custom-monitoring",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestPrometheusPeerNamespacesDoesNotDuplicateRHOAIDefault(t *testing.T) {
	t.Setenv(monitoringNamespaceEnvVar, defaultRHOAIMonitoringNamespace)

	got := prometheusPeerNamespaces()
	want := []string{
		defaultMonitoringNamespace,
		defaultUserWorkloadMonitoringNamespace,
		defaultRHOAIMonitoringNamespace,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestReconcileNetworkPoliciesPropagatesTracingServiceErrors(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add core scheme: %v", err)
	}
	if err := v1alpha2.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add serving scheme: %v", err)
	}

	wantErr := errors.New("transient OTLP Service read failure")
	fakeClient := clientfake.NewClientBuilder().
		WithScheme(scheme).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*corev1.Service); ok {
					return wantErr
				}
				return c.Get(ctx, key, obj, opts...)
			},
		}).
		Build()

	llmSvc := &v1alpha2.LLMInferenceService{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "svc-a",
			Namespace: "team-a",
			Annotations: map[string]string{
				constants.EnableTracingEgressNetworkPolicyAnnotationKey: "true",
			},
		},
		Spec: v1alpha2.LLMInferenceServiceSpec{Tracing: &v1alpha2.TracingSpec{
			ExporterEndpoint: ptr.To("http://jaeger.observability.svc:4317"),
		}},
	}
	reconciler := &LLMISVCReconciler{
		Client:        fakeClient,
		EventRecorder: record.NewFakeRecorder(10),
	}

	err := reconciler.reconcileNetworkPolicies(context.Background(), llmSvc, nil)
	if !errors.Is(err, wantErr) {
		t.Fatalf("reconcileNetworkPolicies error = %v, want wrapped %v", err, wantErr)
	}
}
