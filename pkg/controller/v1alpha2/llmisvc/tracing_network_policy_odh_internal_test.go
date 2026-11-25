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
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"testing"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/utils/ptr"
	duckv1 "knative.dev/pkg/apis/duck/v1"
	"knative.dev/pkg/kmeta"
	"knative.dev/pkg/network"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
	"github.com/kserve/kserve/pkg/constants"
)

func TestParseOTLPServiceEndpoint(t *testing.T) {
	tests := []struct {
		name          string
		endpoint      string
		namespace     string
		wantService   string
		wantNamespace string
		wantPort      int32
		wantValid     bool
	}{
		{name: "same namespace", endpoint: "http://otel-collector:4317", namespace: "team-a", wantService: "otel-collector", wantNamespace: "team-a", wantPort: 4317, wantValid: true},
		{name: "cross namespace", endpoint: "http://jaeger.observability.svc." + network.GetClusterDomainName() + ":4317", namespace: "team-a", wantService: "jaeger", wantNamespace: "observability", wantPort: 4317, wantValid: true},
		{name: "cross namespace with search path", endpoint: "http://jaeger.observability:4317", namespace: "team-a", wantService: "jaeger", wantNamespace: "observability", wantPort: 4317, wantValid: true},
		{name: "custom port", endpoint: "http://jaeger.observability.svc:4318", namespace: "team-a", wantService: "jaeger", wantNamespace: "observability", wantPort: 4318, wantValid: true},
		{name: "trailing dot", endpoint: "http://jaeger.observability.svc." + network.GetClusterDomainName() + ".:4317", namespace: "team-a", wantService: "jaeger", wantNamespace: "observability", wantPort: 4317, wantValid: true},
		{name: "unsupported scheme", endpoint: "ftp://jaeger.observability.svc:4317", namespace: "team-a"},
		{name: "invalid namespace prefix", endpoint: "http://jaeger.-prod.svc:4317", namespace: "team-a"},
		{name: "invalid namespace suffix", endpoint: "http://jaeger.prod-.svc:4317", namespace: "team-a"},
		{name: "localhost", endpoint: "http://localhost:4317", namespace: "team-a"},
		{name: "external host", endpoint: "http://otel.example.com:4317", namespace: "team-a"},
		{name: "IP address", endpoint: "http://10.0.0.10:4317", namespace: "team-a"},
		{name: "host and port without scheme", endpoint: "jaeger.observability:4317", namespace: "team-a"},
		{name: "unparseable", endpoint: "not a URL", namespace: "team-a"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed, valid := parseOTLPServiceEndpoint(tt.endpoint, tt.namespace)
			if valid != tt.wantValid || parsed.port != tt.wantPort {
				t.Fatalf("got valid=%v port=%d, want valid=%v port=%d", valid, parsed.port, tt.wantValid, tt.wantPort)
			}
			if valid && (parsed.serviceName != tt.wantService || parsed.namespace != tt.wantNamespace) {
				t.Fatalf("got service=%q namespace=%q, want service=%q namespace=%q", parsed.serviceName, parsed.namespace, tt.wantService, tt.wantNamespace)
			}
		})
	}
}

func TestOTLPPeerForService(t *testing.T) {
	endpoint, valid := parseOTLPServiceEndpoint("http://jaeger.observability.svc:4318", "team-a")
	if !valid {
		t.Fatal("expected endpoint to be valid")
	}

	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "jaeger", Namespace: "observability"},
		Spec:       corev1.ServiceSpec{Selector: map[string]string{"app": "jaeger"}},
	}
	peer, port, hasPeer, supported := otlpPeerForService(endpoint, "team-a", service)
	if !supported || !hasPeer || port != 4318 {
		t.Fatalf("got supported=%v peer=%v port=%d", supported, hasPeer, port)
	}
	if got := peer.NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"]; got != "observability" {
		t.Fatalf("namespace got %q", got)
	}
	if got := peer.PodSelector.MatchLabels; !reflect.DeepEqual(got, service.Spec.Selector) {
		t.Fatalf("pod selector got %v want %v", got, service.Spec.Selector)
	}

	_, _, hasPeer, supported = otlpPeerForService(endpoint, "team-a", nil)
	if supported || hasPeer {
		t.Fatal("expected a missing Service to be unsupported")
	}

	sameNamespaceEndpoint, valid := parseOTLPServiceEndpoint("http://jaeger:4318", "team-a")
	if !valid {
		t.Fatal("expected same-namespace endpoint to be valid")
	}
	selectorlessService := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "jaeger", Namespace: "team-a"}}
	_, port, hasPeer, supported = otlpPeerForService(sameNamespaceEndpoint, "team-a", selectorlessService)
	if !supported || hasPeer || port != 4318 {
		t.Fatalf("got supported=%v peer=%v port=%d for same-namespace Service", supported, hasPeer, port)
	}
}

func TestTracingServiceIndexValues(t *testing.T) {
	llmSvc := &v1alpha2.LLMInferenceService{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "svc-a",
			Namespace:   "team-a",
			Annotations: map[string]string{constants.EnableTracingEgressNetworkPolicyAnnotationKey: "true"},
		},
	}
	llmSvc.Status.Annotations = map[string]string{
		constants.LLMTracingServiceStatusAnnotationKey: "observability/jaeger",
	}
	if got, want := tracingServiceIndexValues(llmSvc), []string{"observability/jaeger"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("resolved endpoint index got %v want %v", got, want)
	}

	setTracingServiceStatusAnnotation(llmSvc, "")
	if got := tracingServiceIndexValues(llmSvc); got != nil {
		t.Fatalf("cleared endpoint should not be indexed: %v", got)
	}

	setTracingServiceStatusAnnotation(llmSvc, "team-a/jaeger")
	delete(llmSvc.Annotations, constants.EnableTracingEgressNetworkPolicyAnnotationKey)
	if got := tracingServiceIndexValues(llmSvc); got != nil {
		t.Fatalf("unopted-in service should not be indexed: %v", got)
	}
}

func TestEnqueueOnOTLPServiceChangeBootstrapsAndRetriesList(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := v1alpha2.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add serving scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add core scheme: %v", err)
	}

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
	service := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "jaeger", Namespace: "observability"}}
	listCalls := 0
	fakeClient := clientfake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(llmSvc).
		WithIndex(&v1alpha2.LLMInferenceService{}, tracingServiceIndex, func(object client.Object) []string {
			return tracingServiceIndexValues(object.(*v1alpha2.LLMInferenceService))
		}).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, c client.WithWatch, object client.ObjectList, opts ...client.ListOption) error {
				listCalls++
				if listCalls == 1 {
					return errors.New("transient cache failure")
				}
				return c.List(ctx, object, opts...)
			},
		}).
		Build()

	queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
	defer queue.ShutDown()
	(&LLMISVCReconciler{Client: fakeClient}).enqueueOnOTLPServiceChange(logr.Discard()).Create(
		context.Background(), event.CreateEvent{Object: service}, queue)

	if got, want := listCalls, 2; got != want {
		t.Fatalf("list calls got %d want %d", got, want)
	}
	if queue.Len() != 1 {
		t.Fatalf("queue length got %d want 1", queue.Len())
	}
	request, shutdown := queue.Get()
	if shutdown {
		t.Fatal("queue shut down before request was enqueued")
	}
	queue.Done(request)
	queue.Forget(request)
	if got, want := request, (reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "team-a", Name: "svc-a"}}); got != want {
		t.Fatalf("request got %v want %v", got, want)
	}
}

func TestTracingNetworkPolicyNameIsPerService(t *testing.T) {
	llmSvc := &v1alpha2.LLMInferenceService{ObjectMeta: metav1.ObjectMeta{Name: "svc-a", Namespace: "team-a"}}
	if got, want := tracingNetworkPolicyName(llmSvc), kmeta.ChildName("svc-a", tracingNetworkPolicySuffix); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

// TestTracingEgressOptedIn pins the shared tracing opt-in predicate directly:
// the service opts in only with a tracing configuration in the reconciled
// spec and the serving.kserve.io/enable-tracing-egress-network-policy
// annotation carrying the exact value "true". Every other annotation value —
// including case and padding variants — opts out, which keeps the gate
// identical for reconcileTracingNetworkPolicy and the allow-all egress
// lifecycle.
func TestTracingEgressOptedIn(t *testing.T) {
	annotation := constants.EnableTracingEgressNetworkPolicyAnnotationKey
	endpoint := "http://jaeger.observability.svc:4317"
	tests := []struct {
		name        string
		tracing     *v1alpha2.TracingSpec
		annotations map[string]string
		want        bool
	}{
		{name: "no tracing without the annotation"},
		{name: "no tracing with the exact annotation opts out", annotations: map[string]string{annotation: "true"}},
		{name: "tracing without the annotation opts out", tracing: &v1alpha2.TracingSpec{}},
		{name: "tracing with an empty annotation value opts out", tracing: &v1alpha2.TracingSpec{}, annotations: map[string]string{annotation: ""}},
		{name: "tracing with annotation false opts out", tracing: &v1alpha2.TracingSpec{}, annotations: map[string]string{annotation: "false"}},
		{name: "tracing with annotation True is not the exact literal", tracing: &v1alpha2.TracingSpec{}, annotations: map[string]string{annotation: "True"}},
		{name: "tracing with annotation TRUE is not the exact literal", tracing: &v1alpha2.TracingSpec{}, annotations: map[string]string{annotation: "TRUE"}},
		{name: "tracing with a padded annotation value opts out", tracing: &v1alpha2.TracingSpec{}, annotations: map[string]string{annotation: " true"}},
		{name: "tracing with a suffixed annotation value opts out", tracing: &v1alpha2.TracingSpec{}, annotations: map[string]string{annotation: "true "}},
		{name: "tracing with annotation 1 opts out", tracing: &v1alpha2.TracingSpec{}, annotations: map[string]string{annotation: "1"}},
		{name: "tracing with annotation yes opts out", tracing: &v1alpha2.TracingSpec{}, annotations: map[string]string{annotation: "yes"}},
		{
			name:        "empty tracing spec with the exact annotation opts in",
			tracing:     &v1alpha2.TracingSpec{},
			annotations: map[string]string{annotation: "true"},
			want:        true,
		},
		{
			name:        "tracing with an exporter endpoint and the exact annotation opts in",
			tracing:     &v1alpha2.TracingSpec{ExporterEndpoint: &endpoint},
			annotations: map[string]string{annotation: "true"},
			want:        true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			llmSvc := &v1alpha2.LLMInferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "model",
					Namespace:   "team-a",
					Annotations: tt.annotations,
				},
				Spec: v1alpha2.LLMInferenceServiceSpec{Tracing: tt.tracing},
			}
			if got := tracingEgressOptedIn(llmSvc); got != tt.want {
				t.Fatalf("tracingEgressOptedIn got %v, want %v", got, tt.want)
			}
		})
	}
}

// TestReconcileTracingNetworkPolicyOptInGateBehavior pins the opt-in gate of
// the tracing egress NetworkPolicy through reconcileTracingNetworkPolicy: the
// policy and its resolved-service status annotation exist only while
// spec.tracing is present and the service carries the
// serving.kserve.io/enable-tracing-egress-network-policy annotation with the
// exact value "true". Every other combination opts out — a previously created
// tracing policy is removed and the status annotation is cleared. The gate
// evaluates the same shared predicate the allow-all egress lifecycle uses;
// these cases are the behavior it must keep.
func TestReconcileTracingNetworkPolicyOptInGateBehavior(t *testing.T) {
	crossNamespaceEndpoint := "http://jaeger.observability.svc:4317"
	annotation := constants.EnableTracingEgressNetworkPolicyAnnotationKey
	tests := []struct {
		name        string
		tracing     *v1alpha2.TracingSpec
		annotations map[string]string
		wantOptIn   bool
	}{
		{name: "tracing absent with the exact annotation opts out", annotations: map[string]string{annotation: "true"}},
		{name: "tracing absent without the annotation opts out"},
		{name: "tracing present without the annotation opts out", tracing: &v1alpha2.TracingSpec{}},
		{name: "tracing present with an empty annotation value opts out", tracing: &v1alpha2.TracingSpec{}, annotations: map[string]string{annotation: ""}},
		{name: "tracing present with annotation false opts out", tracing: &v1alpha2.TracingSpec{}, annotations: map[string]string{annotation: "false"}},
		{name: "tracing present with annotation True is not the exact literal", tracing: &v1alpha2.TracingSpec{}, annotations: map[string]string{annotation: "True"}},
		{name: "tracing present with annotation TRUE is not the exact literal", tracing: &v1alpha2.TracingSpec{}, annotations: map[string]string{annotation: "TRUE"}},
		{name: "tracing present with a padded annotation value opts out", tracing: &v1alpha2.TracingSpec{}, annotations: map[string]string{annotation: " true"}},
		{name: "tracing present with annotation 1 opts out", tracing: &v1alpha2.TracingSpec{}, annotations: map[string]string{annotation: "1"}},
		{
			name:        "empty tracing spec with the exact annotation opts in",
			tracing:     &v1alpha2.TracingSpec{},
			annotations: map[string]string{annotation: "true"},
			wantOptIn:   true,
		},
		{
			name:        "tracing with a cluster-local exporter endpoint and the exact annotation opts in",
			tracing:     &v1alpha2.TracingSpec{ExporterEndpoint: &crossNamespaceEndpoint},
			annotations: map[string]string{annotation: "true"},
			wantOptIn:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &v1alpha2.LLMInferenceService{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "model",
					Namespace:   "team-a",
					UID:         "model-uid",
					Annotations: tt.annotations,
				},
				Spec: v1alpha2.LLMInferenceServiceSpec{Tracing: tt.tracing},
				// A stale resolved-service key from an earlier opted-in reconcile:
				// the gate must clear it on opt-out.
				Status: v1alpha2.LLMInferenceServiceStatus{
					Status: duckv1.Status{
						Annotations: map[string]string{constants.LLMTracingServiceStatusAnnotationKey: "stale/jaeger"},
					},
				},
			}
			// Seed the tracing policy of an earlier opted-in reconcile so the
			// opt-out cases exercise the cleanup path.
			cl := newNetworkPolicyTestClient(t, expectedTracingNetworkPolicy(service, netv1.NetworkPolicyPeer{}, 0, false))
			r := &LLMISVCReconciler{Client: cl, EventRecorder: record.NewFakeRecorder(16)}

			if err := r.reconcileTracingNetworkPolicy(t.Context(), service); err != nil {
				t.Fatalf("reconcileTracingNetworkPolicy failed: %v", err)
			}

			stored := &netv1.NetworkPolicy{}
			err := cl.Get(t.Context(), types.NamespacedName{
				Name: tracingNetworkPolicyName(service), Namespace: service.Namespace,
			}, stored)
			if !tt.wantOptIn {
				if !apierrors.IsNotFound(err) {
					t.Fatalf("opted-out service must lose the tracing policy: %v", err)
				}
				if _, ok := service.Status.Annotations[constants.LLMTracingServiceStatusAnnotationKey]; ok {
					t.Fatalf("opted-out service must clear the resolved tracing service annotation: %v", service.Status.Annotations)
				}
				return
			}
			if err != nil {
				t.Fatalf("opted-in service must keep the tracing policy: %v", err)
			}
			if !metav1.IsControlledBy(stored, service) {
				t.Fatal("tracing policy is not controlled by the service")
			}
			want := expectedTracingNetworkPolicy(service, netv1.NetworkPolicyPeer{}, 0, false)
			if !reflect.DeepEqual(stored.Spec, want.Spec) {
				t.Fatalf("tracing policy spec got %#v, want the documented tracing permissions", stored.Spec)
			}
			wantStatusKey := ""
			if endpoint, valid := parseOTLPServiceEndpoint(ptr.Deref(tt.tracing.ExporterEndpoint, ""), service.Namespace); valid {
				wantStatusKey = tracingServiceKey(endpoint.namespace, endpoint.serviceName)
			}
			if got := service.Status.Annotations[constants.LLMTracingServiceStatusAnnotationKey]; got != wantStatusKey {
				t.Fatalf("resolved tracing service annotation got %q, want %q", got, wantStatusKey)
			}
		})
	}
}

// TestReconcileTracingNetworkPolicyMigratesBranchCreatedPolicyToLegacySelector
// covers the upgrade path off the removed identity scheme: an earlier build of
// this controller selected the tracing policy's Pods through the
// serving.kserve.io/llmisvc-network-policy-id label and stamped the same label
// onto the policy's metadata. Reconciling such a stored policy must rewrite it
// in place onto the pre-existing application labels — part-of plus app-name —
// and drop the stale identity label, while its egress permissions stay the
// documented set.
func TestReconcileTracingNetworkPolicyMigratesBranchCreatedPolicyToLegacySelector(t *testing.T) {
	llmSvc := &v1alpha2.LLMInferenceService{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "svc-a",
			Namespace: "team-a",
			UID:       "svc-a-uid",
			Annotations: map[string]string{
				constants.EnableTracingEgressNetworkPolicyAnnotationKey: "true",
			},
		},
		Spec: v1alpha2.LLMInferenceServiceSpec{Tracing: &v1alpha2.TracingSpec{}},
	}
	// The identity value the branch builds derived from namespace/name.
	digest := sha256.Sum256([]byte(llmSvc.GetNamespace() + "/" + llmSvc.GetName()))
	branchServiceID := hex.EncodeToString(digest[:])[:16]
	const branchServiceIDLabel = "serving.kserve.io/llmisvc-network-policy-id"

	// Seed the stored main Deployment and the tracing policy exactly as the
	// branch build left it: same name and egress rules, but selected through
	// the identity label the current templates no longer stamp, with the same
	// label carried in the policy metadata.
	mainSelector := metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "svc-a"}}
	branchTracing := expectedTracingNetworkPolicy(llmSvc, netv1.NetworkPolicyPeer{}, 0, false)
	branchTracing.Labels[branchServiceIDLabel] = branchServiceID
	branchTracing.Spec.PodSelector = metav1.LabelSelector{MatchLabels: map[string]string{
		branchServiceIDLabel: branchServiceID,
	}}

	cl := newNetworkPolicyTestClient(t,
		ownedDeployment(llmSvc, mainDeploymentName(llmSvc), mainSelector.DeepCopy(), mainSelector.MatchLabels),
		branchTracing)
	r := &LLMISVCReconciler{Client: cl, EventRecorder: record.NewFakeRecorder(16)}

	if err := r.reconcileNetworkPolicies(t.Context(), llmSvc, nil); err != nil {
		t.Fatalf("reconcileNetworkPolicies failed: %v", err)
	}

	migrated := &netv1.NetworkPolicy{}
	if err := cl.Get(t.Context(), types.NamespacedName{
		Name: tracingNetworkPolicyName(llmSvc), Namespace: llmSvc.Namespace,
	}, migrated); err != nil {
		t.Fatalf("the tracing policy was not migrated in place: %v", err)
	}
	if got := migrated.Labels[branchServiceIDLabel]; got != "" {
		t.Fatalf("stale identity label survived the migration: %q in %v", got, migrated.Labels)
	}
	want := expectedTracingNetworkPolicy(llmSvc, netv1.NetworkPolicyPeer{}, 0, false)
	if !reflect.DeepEqual(migrated.Labels, want.Labels) {
		t.Fatalf("migrated labels got %v, want %v", migrated.Labels, want.Labels)
	}
	if !reflect.DeepEqual(migrated.Spec.PodSelector.MatchLabels, map[string]string{
		constants.KubernetesPartOfLabelKey:  constants.LLMInferenceServicePartOfValue,
		constants.KubernetesAppNameLabelKey: llmSvc.GetName(),
	}) {
		t.Fatalf("migrated selector got %#v, want the legacy application selector", migrated.Spec.PodSelector)
	}
	if !reflect.DeepEqual(migrated.Spec, want.Spec) {
		t.Fatalf("migrated spec got %#v, want the documented tracing permissions", migrated.Spec)
	}
}

func TestExpectedTracingNetworkPolicy(t *testing.T) {
	llmSvc := &v1alpha2.LLMInferenceService{
		ObjectMeta: metav1.ObjectMeta{Name: "svc-a", Namespace: "team-a"},
		Spec: v1alpha2.LLMInferenceServiceSpec{Tracing: &v1alpha2.TracingSpec{
			ExporterEndpoint: ptr.To("http://jaeger.observability.svc.cluster.local:4318"),
		}},
	}

	otlpPeer := namespaceSelectorPeer("observability")
	otlpPeer.PodSelector = &metav1.LabelSelector{MatchLabels: map[string]string{"app": "jaeger"}}
	np := expectedTracingNetworkPolicy(llmSvc, otlpPeer, 4318, true)
	if got, want := np.Name, "svc-a-otlp-egress"; got != want {
		t.Fatalf("name got %q want %q", got, want)
	}
	if got, want := np.Namespace, "team-a"; got != want {
		t.Fatalf("namespace got %q want %q", got, want)
	}
	if got, want := np.Labels[constants.KubernetesComponentLabelKey], tracingNPComponentLabel; got != want {
		t.Fatalf("component label got %q want %q", got, want)
	}
	if got := np.OwnerReferences; len(got) != 1 || got[0].Name != llmSvc.Name || got[0].Controller == nil || !*got[0].Controller {
		t.Fatalf("unexpected owner references: %#v", got)
	}
	if got, want := np.Spec.PodSelector.MatchLabels, map[string]string{
		constants.KubernetesPartOfLabelKey:  constants.LLMInferenceServicePartOfValue,
		constants.KubernetesAppNameLabelKey: llmSvc.Name,
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("pod selector got %v want %v", got, want)
	}
	// The removed identity scheme must not resurface in the policy metadata.
	if _, ok := np.Labels["serving.kserve.io/llmisvc-network-policy-id"]; ok {
		t.Fatalf("policy labels must not carry the removed identity label: %v", np.Labels)
	}
	if !reflect.DeepEqual(np.Spec.PolicyTypes, []netv1.PolicyType{netv1.PolicyTypeEgress}) {
		t.Fatalf("policy types got %v", np.Spec.PolicyTypes)
	}
	if got, want := len(np.Spec.Egress), 4; got != want {
		t.Fatalf("egress rule count got %d want %d", got, want)
	}

	if got, want := np.Spec.Egress[0].Ports, []netv1.NetworkPolicyPort{
		{Protocol: ptr.To(corev1.ProtocolUDP), Port: ptr.To(intstr.FromInt32(53))},
		{Protocol: ptr.To(corev1.ProtocolTCP), Port: ptr.To(intstr.FromInt32(53))},
		{Protocol: ptr.To(corev1.ProtocolUDP), Port: ptr.To(intstr.FromInt32(5353))},
		{Protocol: ptr.To(corev1.ProtocolTCP), Port: ptr.To(intstr.FromInt32(5353))},
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("DNS ports got %v want %v", got, want)
	}
	if got, want := np.Spec.Egress[1].Ports, []netv1.NetworkPolicyPort{
		{Protocol: ptr.To(corev1.ProtocolTCP), Port: ptr.To(intstr.FromInt32(443))},
		{Protocol: ptr.To(corev1.ProtocolTCP), Port: ptr.To(intstr.FromInt32(6443))},
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("API ports got %v want %v", got, want)
	}
	if got := np.Spec.Egress[1].To; len(got) != 0 {
		t.Fatalf("API peers got %v want no destination restriction", got)
	}
	if np.Spec.Egress[2].To[0].PodSelector == nil || len(np.Spec.Egress[2].To[0].PodSelector.MatchLabels) != 0 {
		t.Fatalf("same-namespace rule is not unrestricted")
	}
	if got, want := len(np.Spec.Egress[3].To), 1; got != want {
		t.Fatalf("OTLP peer count got %d want %d", got, want)
	}
	if got, want := np.Spec.Egress[3].Ports, []netv1.NetworkPolicyPort{{
		Protocol: ptr.To(corev1.ProtocolTCP),
		Port:     ptr.To(intstr.FromInt32(4318)),
	}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("OTLP ports got %v want %v", got, want)
	}
	for _, peer := range np.Spec.Egress[3].To {
		if peer.PodSelector == nil || peer.NamespaceSelector == nil {
			t.Fatalf("OTLP peer has unexpected selectors: %#v", peer)
		}
		if peer.NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] != "observability" {
			t.Fatalf("unexpected namespace selector: %v", peer.NamespaceSelector.MatchLabels)
		}
		if !reflect.DeepEqual(peer.PodSelector.MatchLabels, map[string]string{"app": "jaeger"}) {
			t.Fatalf("unexpected pod selector: %v", peer.PodSelector.MatchLabels)
		}
	}
}
