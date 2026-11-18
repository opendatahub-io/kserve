package kservemodule

import (
	"context"
	"errors"
	"testing"

	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"

	platformv1alpha1 "github.com/opendatahub-io/kserve-module/pkg/apis/v1alpha1"
	"github.com/opendatahub-io/odh-platform-utilities/api/common"
)

func TestReconcileOCSFAuditLoggingMetadataCreatesPolicy(t *testing.T) {
	g := NewWithT(t)
	authorino := testAuthorino()
	authorinoBefore := authorino.DeepCopy()
	kserve := testKserve(platformv1alpha1.AuditProfileMetadata)
	r, cli := newOCSFReconciler(t, kserve, testTelemetryPolicyCRD(true, true), testGatewayConfigMap(), authorino)

	_, err := r.reconcileOCSFAuditLogging(context.Background(), kserve)
	g.Expect(err).To(Succeed())
	g.Expect(r.ocsfAuditLoggingCondition.reason).To(Equal(reasonTelemetryPolicyPending))

	policy := &unstructured.Unstructured{}
	policy.SetGroupVersionKind(telemetryPolicyGVK)
	g.Expect(cli.Get(context.Background(), client.ObjectKey{
		Name: telemetryPolicyName, Namespace: "gateway-system",
	}, policy)).To(Succeed())
	g.Expect(policy.GetLabels()[telemetryPolicyManagedLabel]).To(Equal("true"))
	g.Expect(policy.GetAnnotations()[telemetryPolicyOwnerAnnotation]).To(Equal(kserve.Name))
	g.Expect(policy.GetOwnerReferences()).To(HaveLen(1))
	target, found, err := unstructured.NestedMap(policy.Object, "spec", "targetRef")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeTrue())
	g.Expect(target).To(HaveKeyWithValue("name", "shared-llm-gateway"))
	g.Expect(target).To(HaveKeyWithValue("group", "gateway.networking.k8s.io"))
	ocsf, found, err := unstructured.NestedMap(policy.Object, "spec", "logging", "default", "ocsf")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeTrue())
	g.Expect(ocsf).To(HaveKeyWithValue("profile", "aiOperation"))
	actor, _, _ := unstructured.NestedMap(ocsf, "actor")
	g.Expect(actor).To(HaveKeyWithValue("identity", "auth.identity.user.username"))
	g.Expect(actor).To(HaveKeyWithValue("uid", "auth.identity.user.uid"))
	g.Expect(actor).To(HaveKeyWithValue("groups", "auth.identity.user.groups"))
	targetConfig, _, _ := unstructured.NestedMap(ocsf, "target")
	g.Expect(targetConfig).To(HaveKeyWithValue("resourceType", "LLMInferenceService"))
	g.Expect(targetConfig).To(HaveKeyWithValue("aiProvider", "KServe"))
	path, _, _ := unstructured.NestedMap(targetConfig, "requestPath")
	g.Expect(path).To(HaveKeyWithValue("namespaceSegment", int64(0)))
	g.Expect(path).To(HaveKeyWithValue("nameSegment", int64(1)))

	gotAuthorino := &unstructured.Unstructured{}
	gotAuthorino.SetGroupVersionKind(authorino.GroupVersionKind())
	g.Expect(cli.Get(context.Background(), client.ObjectKeyFromObject(authorino), gotAuthorino)).To(Succeed())
	g.Expect(gotAuthorino.Object["spec"]).To(Equal(authorinoBefore.Object["spec"]))
	g.Expect(gotAuthorino.GetName()).To(Equal(authorinoBefore.GetName()))
	g.Expect(gotAuthorino.GetNamespace()).To(Equal(authorinoBefore.GetNamespace()))
}

func TestReconcileOCSFAuditLoggingRepairsDriftAndIsIdempotent(t *testing.T) {
	g := NewWithT(t)
	kserve := testKserve(platformv1alpha1.AuditProfileMetadata)
	r, cli := newOCSFReconciler(t, kserve, testTelemetryPolicyCRD(true, true), testGatewayConfigMap())
	ctx := context.Background()

	_, err := r.reconcileOCSFAuditLogging(ctx, kserve)
	g.Expect(err).To(Succeed())
	policy := &unstructured.Unstructured{}
	policy.SetGroupVersionKind(telemetryPolicyGVK)
	key := client.ObjectKey{Name: telemetryPolicyName, Namespace: "gateway-system"}
	g.Expect(cli.Get(ctx, key, policy)).To(Succeed())
	policy.Object["spec"].(map[string]any)["targetRef"].(map[string]any)["name"] = "drifted-gateway"
	g.Expect(cli.Update(ctx, policy)).To(Succeed())

	_, err = r.reconcileOCSFAuditLogging(ctx, kserve)
	g.Expect(err).To(Succeed())
	g.Expect(cli.Get(ctx, key, policy)).To(Succeed())
	desired := desiredOCSFPolicy(kserve, "gateway-system", "shared-llm-gateway")
	g.Expect(policy.Object["spec"]).To(Equal(desired.Object["spec"]))
	stable := policy.DeepCopy()
	_, err = r.reconcileOCSFAuditLogging(ctx, kserve)
	g.Expect(err).To(Succeed())
	g.Expect(cli.Get(ctx, key, policy)).To(Succeed())
	g.Expect(policy.Object["spec"]).To(Equal(stable.Object["spec"]))
}

func TestReconcileOCSFAuditLoggingRemovedDeletesOnlyOwnedPolicy(t *testing.T) {
	g := NewWithT(t)
	kserve := testKserve(platformv1alpha1.AuditProfileRemoved)
	owned := desiredOCSFPolicy(kserve, "old-gateway-system", "old-gateway")
	user := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": telemetryPolicyGVK.GroupVersion().String(),
		"kind":       telemetryPolicyGVK.Kind,
		"metadata": map[string]any{
			"name": "user-policy", "namespace": "old-gateway-system",
		},
		"spec": map[string]any{"targetRef": map[string]any{"name": "user-gateway"}},
	}}
	user.SetGroupVersionKind(telemetryPolicyGVK)
	r, cli := newOCSFReconciler(t, kserve, testTelemetryPolicyCRD(true, true), owned, user)

	_, err := r.reconcileOCSFAuditLogging(context.Background(), kserve)
	g.Expect(err).To(Succeed())
	deleted := &unstructured.Unstructured{}
	deleted.SetGroupVersionKind(telemetryPolicyGVK)
	err = cli.Get(context.Background(), client.ObjectKeyFromObject(owned), deleted)
	g.Expect(apierrors.IsNotFound(err)).To(BeTrue())
	kept := &unstructured.Unstructured{}
	kept.SetGroupVersionKind(telemetryPolicyGVK)
	g.Expect(cli.Get(context.Background(), client.ObjectKeyFromObject(user), kept)).To(Succeed())
}

func TestReconcileOCSFAuditLoggingCleanupFailureMarksNotReady(t *testing.T) {
	g := NewWithT(t)
	kserve := testKserve(platformv1alpha1.AuditProfileMetadata)
	r, cli := newOCSFReconciler(t, kserve, testTelemetryPolicyCRD(true, true), testGatewayConfigMap())
	r.Client = &failingListClient{Client: cli, err: errors.New("cleanup list failed")}

	retry, err := r.reconcileOCSFAuditLogging(context.Background(), kserve)
	g.Expect(retry).To(BeFalse())
	g.Expect(err).To(MatchError(ContainSubstring("cleanup list failed")))
	g.Expect(r.ocsfAuditLoggingCondition).NotTo(BeNil())
	g.Expect(r.ocsfAuditLoggingCondition.reason).To(Equal(reasonAuditLoggingReconcileFailed))

	condMgr := newConditionManager(kserve)
	applyOCSFAuditLoggingCondition(condMgr, kserve, r.ocsfAuditLoggingCondition)
	condition := condMgr.GetCondition(ConditionLLMISVCOCSFAuditLoggingReady)
	g.Expect(condition.Status).To(Equal(metav1.ConditionFalse))
	g.Expect(condition.Reason).To(Equal(reasonAuditLoggingReconcileFailed))

	policy := &unstructured.Unstructured{}
	policy.SetGroupVersionKind(telemetryPolicyGVK)
	g.Expect(cli.Get(context.Background(), client.ObjectKey{Name: telemetryPolicyName, Namespace: "gateway-system"}, policy)).To(Succeed())
}

func TestReconcileOCSFAuditLoggingReportsMissingOrIncompatibleRHCL(t *testing.T) {
	for _, tt := range []struct {
		name string
		crd  *apiextensionsv1.CustomResourceDefinition
	}{
		{name: "missing", crd: nil},
		{name: "incompatible", crd: testTelemetryPolicyCRD(false, false)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)
			kserve := testKserve(platformv1alpha1.AuditProfileMetadata)
			objects := []client.Object{kserve, testGatewayConfigMap()}
			if tt.crd != nil {
				objects = append(objects, tt.crd)
			}
			r, _ := newOCSFReconciler(t, objects...)
			_, err := r.reconcileOCSFAuditLogging(context.Background(), kserve)
			g.Expect(err).To(Succeed())
			g.Expect(r.ocsfAuditLoggingCondition).NotTo(BeNil())
			g.Expect(r.ocsfAuditLoggingCondition.reason).To(Equal(reasonRHCLDependencyUnavailable))
			g.Expect(r.ocsfAuditLoggingCondition.message).To(ContainSubstring("OCSF audit logging is unavailable"))
			condMgr := newConditionManager(kserve)
			applyOCSFAuditLoggingCondition(condMgr, kserve, r.ocsfAuditLoggingCondition)
			var conditionReason, conditionMessage string
			for _, condition := range kserve.Status.Conditions {
				if condition.Type == ConditionLLMISVCOCSFAuditLoggingReady {
					conditionReason = condition.Reason
					conditionMessage = condition.Message
				}
			}
			g.Expect(conditionReason).To(Equal(reasonRHCLDependencyUnavailable))
			g.Expect(conditionMessage).To(ContainSubstring("OCSF audit logging is unavailable"))
		})
	}
}

func TestAuditLoggingCRDCapabilities(t *testing.T) {
	for _, tt := range []struct {
		name                 string
		telemetryOCSF        bool
		telemetryFields      bool
		authorinoFields      bool
		wantOCSF, wantCustom bool
	}{
		{name: "neither schema supports logging"},
		{name: "custom fields only", telemetryFields: true, authorinoFields: true, wantCustom: true},
		{name: "OCSF and custom fields", telemetryOCSF: true, telemetryFields: true, authorinoFields: true, wantOCSF: true, wantCustom: true},
		{name: "Authorino field missing", telemetryOCSF: true, telemetryFields: true, wantOCSF: true},
		{name: "TelemetryPolicy fields missing", telemetryOCSF: true, authorinoFields: true, wantOCSF: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)
			kserve := testKserve(platformv1alpha1.AuditProfileMetadata)
			r, _ := newOCSFReconciler(t, kserve, testTelemetryPolicyCRD(tt.telemetryOCSF, tt.telemetryFields), testAuthorinoCRD(tt.authorinoFields))
			capabilities := r.auditLoggingCRDCapabilities(context.Background())
			g.Expect(capabilities.ocsfErr == nil).To(Equal(tt.wantOCSF))
			g.Expect(capabilities.customFieldsErr == nil).To(Equal(tt.wantCustom))
			condMgr := newConditionManager(kserve)
			applyAuditLoggingCapabilityConditions(condMgr, kserve, capabilities)
			g.Expect(condMgr.GetCondition(ConditionISVCAuditLoggingAvailable).Status).To(Equal(metav1.ConditionTrue))
			g.Expect(condMgr.GetCondition(ConditionLLMISVCOCSFAuditLoggingAvailable).Status == metav1.ConditionTrue).To(Equal(tt.wantOCSF))
			g.Expect(condMgr.GetCondition(ConditionLLMISVCCustomAuditFieldsAvailable).Status == metav1.ConditionTrue).To(Equal(tt.wantCustom))
			g.Expect(condMgr.GetCondition(ConditionLLMISVCAuditLoggingAvailable).Status == metav1.ConditionTrue).To(Equal(tt.wantOCSF || tt.wantCustom))
			if tt.wantCustom && !tt.wantOCSF {
				g.Expect(condMgr.GetCondition(ConditionLLMISVCAuditLoggingAvailable).Reason).To(Equal("CustomFieldsOnly"))
			}
		})
	}
}

func TestAuditLoggingCRDCapabilitiesRetriesTransientRead(t *testing.T) {
	g := NewWithT(t)
	kserve := testKserve(platformv1alpha1.AuditProfileMetadata)
	r, cli := newOCSFReconciler(t, kserve, testTelemetryPolicyCRD(true, true), testAuthorinoCRD(true))
	r.apiReader = &failOnceReader{
		Reader: cli,
		key:    client.ObjectKey{Name: telemetryPolicyGVKName},
		err:    errors.New("temporary API failure"),
	}

	capabilities := r.auditLoggingCRDCapabilities(context.Background())
	g.Expect(capabilities.retry).To(BeTrue())
	g.Expect(capabilities.ocsfErr).To(HaveOccurred())

	capabilities = r.auditLoggingCRDCapabilities(context.Background())
	g.Expect(capabilities.retry).To(BeFalse())
	g.Expect(capabilities.ocsfErr).NotTo(HaveOccurred())
	g.Expect(capabilities.customFieldsErr).NotTo(HaveOccurred())
}

func TestAuditLoggingCRDCapabilitiesDoesNotRetryUnavailableCRD(t *testing.T) {
	g := NewWithT(t)
	kserve := testKserve(platformv1alpha1.AuditProfileMetadata)
	r, _ := newOCSFReconciler(t, kserve, testAuthorinoCRD(true))

	capabilities := r.auditLoggingCRDCapabilities(context.Background())
	g.Expect(capabilities.retry).To(BeFalse())
	g.Expect(capabilities.ocsfErr).To(HaveOccurred())
}

func TestInferenceServiceAuditLoggingUnavailableWhenKServeRemoved(t *testing.T) {
	g := NewWithT(t)
	kserve := testKserve(platformv1alpha1.AuditProfileMetadata)
	kserve.Spec.ManagementState = common.Removed
	condMgr := newConditionManager(kserve)
	applyAuditLoggingCapabilityConditions(condMgr, kserve, auditLoggingCapabilities{})
	condition := condMgr.GetCondition(ConditionISVCAuditLoggingAvailable)
	g.Expect(condition.Status).To(Equal(metav1.ConditionFalse))
	g.Expect(condition.Reason).To(Equal("KServeRemoved"))
}

func TestOCSFPolicyStatusRequiresCurrentEnforcement(t *testing.T) {
	g := NewWithT(t)
	policy := desiredOCSFPolicy(testKserve(platformv1alpha1.AuditProfileMetadata), "gateway-system", "shared-llm-gateway")
	policy.SetGeneration(2)
	g.Expect(evaluateOCSFPolicyStatus(policy).reason).To(Equal(reasonTelemetryPolicyPending))
	policy.Object["status"] = map[string]any{"conditions": []any{
		map[string]any{"type": "Accepted", "status": "True", "observedGeneration": int64(2)},
		map[string]any{"type": "Enforced", "status": "False", "observedGeneration": int64(2), "message": "unsupported OCSF configuration"},
	}}
	condition := evaluateOCSFPolicyStatus(policy)
	g.Expect(condition.reason).To(Equal(reasonTelemetryPolicyNotEnforced))
	g.Expect(condition.message).To(ContainSubstring("unsupported OCSF configuration"))
	policy.Object["status"].(map[string]any)["conditions"].([]any)[1].(map[string]any)["status"] = "True"
	g.Expect(evaluateOCSFPolicyStatus(policy)).To(BeNil())
	policy.SetGeneration(3)
	g.Expect(evaluateOCSFPolicyStatus(policy).reason).To(Equal(reasonTelemetryPolicyPending))
}

func TestReconcileOCSFAuditLoggingRetriesPolicyStatusRead(t *testing.T) {
	g := NewWithT(t)
	kserve := testKserve(platformv1alpha1.AuditProfileMetadata)
	policy := desiredOCSFPolicy(kserve, "gateway-system", "shared-llm-gateway")
	policy.SetGeneration(1)
	policy.Object["status"] = map[string]any{"conditions": []any{
		map[string]any{"type": "Accepted", "status": "True", "observedGeneration": int64(1)},
		map[string]any{"type": "Enforced", "status": "True", "observedGeneration": int64(1)},
	}}
	r, cli := newOCSFReconciler(t, kserve, testTelemetryPolicyCRD(true, true), testGatewayConfigMap(), policy)
	r.apiReader = &failOnceReader{
		Reader: cli,
		key:    client.ObjectKeyFromObject(policy),
		err:    errors.New("temporary status read failure"),
	}

	retry, err := r.reconcileOCSFAuditLogging(context.Background(), kserve)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(retry).To(BeTrue())
	g.Expect(r.ocsfAuditLoggingCondition.reason).To(Equal(reasonTelemetryPolicyPending))

	// A retry reads the recovered status from the same policy without requiring
	// any intervening policy update.
	retry, err = r.reconcileOCSFAuditLogging(context.Background(), kserve)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(retry).To(BeFalse())
	g.Expect(r.ocsfAuditLoggingCondition).To(BeNil())
}

func TestOCSFAuditLoggingWatchesTelemetryPolicyCRD(t *testing.T) {
	g := NewWithT(t)
	r := &KserveModuleReconciler{}

	g.Expect(r.dynamicWatchCRDNames()).NotTo(HaveKey(telemetryPolicyGVKName))
	r.dynamicWatches = r.buildDynamicWatches()
	g.Expect(r.dynamicWatchCRDNames()).To(HaveKey(telemetryPolicyGVKName))

	var telemetryWatch *dynamicWatch
	for _, watch := range r.dynamicWatches {
		if watch.gvk == telemetryPolicyGVK {
			telemetryWatch = watch
			break
		}
	}
	g.Expect(telemetryWatch).NotTo(BeNil())
	g.Expect(telemetryWatch.crdName).To(Equal(telemetryPolicyGVKName))
}

func TestOCSFAuditLoggingTelemetryPolicyWatchFiltersEvents(t *testing.T) {
	g := NewWithT(t)
	r := &KserveModuleReconciler{}
	r.dynamicWatches = r.buildDynamicWatches()

	var telemetryWatch *dynamicWatch
	for _, watch := range r.dynamicWatches {
		if watch.gvk == telemetryPolicyGVK {
			telemetryWatch = watch
			break
		}
	}
	g.Expect(telemetryWatch).NotTo(BeNil())
	g.Expect(telemetryWatch.predicates).To(HaveLen(1))

	predicates := dynamicWatchPredicates(telemetryWatch)
	g.Expect(predicates).To(HaveLen(2))

	unrelated := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "user-policy"},
	}}
	owned := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{
			"name":   "user-policy",
			"labels": map[string]any{telemetryPolicyComponentLabel: telemetryPolicyComponentValue},
		},
	}}
	g.Expect(predicates[1].Update(event.UpdateEvent{ObjectNew: unrelated, ObjectOld: unrelated.DeepCopy()})).To(BeFalse())
	g.Expect(predicates[1].Update(event.UpdateEvent{ObjectNew: owned, ObjectOld: owned.DeepCopy()})).To(BeTrue())

	statusOnly := desiredOCSFPolicy(testKserve(platformv1alpha1.AuditProfileMetadata), "gateway-system", "shared-llm-gateway")
	g.Expect(predicates[0].Update(event.UpdateEvent{ObjectNew: statusOnly, ObjectOld: statusOnly.DeepCopy()})).To(BeFalse())
	statusChanged := statusOnly.DeepCopy()
	statusChanged.SetResourceVersion("2")
	g.Expect(predicates[0].Update(event.UpdateEvent{ObjectNew: statusChanged, ObjectOld: statusOnly})).To(BeTrue())
	generationChanged := statusOnly.DeepCopy()
	generationChanged.SetGeneration(statusOnly.GetGeneration() + 1)
	g.Expect(predicates[0].Update(event.UpdateEvent{ObjectNew: generationChanged, ObjectOld: statusOnly})).To(BeTrue())
}

func TestOCSFAuditLoggingDoesNotChangeInferenceServiceConfigMapAuditProfile(t *testing.T) {
	g := NewWithT(t)
	cm := &corev1.ConfigMap{Data: map[string]string{
		openshiftConfigKeyName: `{"auditLoggingProfile":"metadata"}`,
	}}
	kserve := testKserve(platformv1alpha1.AuditProfileMetadata)
	kserve.Spec.AuditLoggingProfile = platformv1alpha1.AuditProfileMetadata
	g.Expect(updateInferenceCM(cm, kserve)).To(Succeed())
	g.Expect(cm.Data[openshiftConfigKeyName]).To(ContainSubstring(`"auditLoggingProfile": "metadata"`))
	g.Expect(cm.Data[openshiftConfigKeyName]).NotTo(ContainSubstring("enableAuditLogging"))
}

func newOCSFReconciler(t *testing.T, objects ...client.Object) (*KserveModuleReconciler, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	ExpectNoError(t, corev1.AddToScheme(scheme))
	ExpectNoError(t, apiextensionsv1.AddToScheme(scheme))
	ExpectNoError(t, platformv1alpha1.AddToScheme(scheme))
	scheme.AddKnownTypeWithName(telemetryPolicyGVK, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(telemetryPolicyListGVK, &unstructured.UnstructuredList{})
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	return &KserveModuleReconciler{
		Client:                cli,
		apiReader:             cli,
		applicationsNamespace: "opendatahub",
	}, cli
}

type failingListClient struct {
	client.Client
	err error
}

func (c *failingListClient) List(context.Context, client.ObjectList, ...client.ListOption) error {
	return c.err
}

type failOnceReader struct {
	client.Reader
	key    client.ObjectKey
	err    error
	failed bool
}

func (r *failOnceReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if key == r.key && !r.failed {
		r.failed = true
		return r.err
	}
	return r.Reader.Get(ctx, key, obj, opts...)
}

func ExpectNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func testKserve(profile platformv1alpha1.AuditProfile) *platformv1alpha1.Kserve {
	return &platformv1alpha1.Kserve{
		ObjectMeta: metav1.ObjectMeta{Name: platformv1alpha1.KserveInstanceName, UID: types.UID("kserve-uid")},
		Spec: platformv1alpha1.KserveSpec{
			ManagementSpec:      common.ManagementSpec{ManagementState: common.Managed},
			AuditLoggingProfile: profile,
		},
	}
}

func testGatewayConfigMap() *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: kserveConfigMapName, Namespace: "opendatahub"},
		Data:       map[string]string{ingressConfigKeyName: `{"kserveIngressGateway":"gateway-system/shared-llm-gateway"}`},
	}
}

func testAuthorino() *unstructured.Unstructured {
	authorino := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "authorino.kuadrant.io/v1beta1",
		"kind":       "Authorino",
		"metadata":   map[string]any{"name": "authorino", "namespace": "kuadrant-system"},
		"spec":       map[string]any{"authenticators": []any{map[string]any{"name": "user"}}},
	}}
	authorino.SetGroupVersionKind(schema.GroupVersionKind{Group: "authorino.kuadrant.io", Version: "v1beta1", Kind: "Authorino"})
	return authorino
}

func testTelemetryPolicyCRD(withOCSF, withFields bool) *apiextensionsv1.CustomResourceDefinition {
	schemaRoot := &apiextensionsv1.JSONSchemaProps{Type: "object", Properties: map[string]apiextensionsv1.JSONSchemaProps{
		"spec": {Type: "object"},
	}}
	defaultProperties := map[string]apiextensionsv1.JSONSchemaProps{}
	if withFields {
		defaultProperties["fields"] = jsonSchemaObject(nil)
	}
	if withOCSF {
		defaultProperties["ocsf"] = jsonSchemaObject(map[string]apiextensionsv1.JSONSchemaProps{
			"profile": jsonSchemaString(),
			"actor": jsonSchemaObject(map[string]apiextensionsv1.JSONSchemaProps{
				"identity": jsonSchemaString(), "uid": jsonSchemaString(), "groups": jsonSchemaString(),
			}),
			"target": jsonSchemaObject(map[string]apiextensionsv1.JSONSchemaProps{
				"resourceType": jsonSchemaString(), "aiProvider": jsonSchemaString(),
				"requestPath": jsonSchemaObject(map[string]apiextensionsv1.JSONSchemaProps{
					"namespaceSegment": jsonSchemaInteger(), "nameSegment": jsonSchemaInteger(),
				}),
			}),
		})
	}
	if len(defaultProperties) > 0 {
		schemaRoot.Properties["spec"] = jsonSchemaObject(map[string]apiextensionsv1.JSONSchemaProps{
			"logging": jsonSchemaObject(map[string]apiextensionsv1.JSONSchemaProps{
				"default": jsonSchemaObject(defaultProperties),
			}),
		})
	}
	return &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: telemetryPolicyGVKName},
		Status: apiextensionsv1.CustomResourceDefinitionStatus{Conditions: []apiextensionsv1.CustomResourceDefinitionCondition{{
			Type: apiextensionsv1.Established, Status: apiextensionsv1.ConditionTrue,
		}}},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group:    "extensions.kuadrant.io",
			Names:    apiextensionsv1.CustomResourceDefinitionNames{Plural: "telemetrypolicies", Singular: "telemetrypolicy", Kind: "TelemetryPolicy"},
			Scope:    apiextensionsv1.NamespaceScoped,
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{Name: "v1alpha1", Served: true, Storage: true, Schema: &apiextensionsv1.CustomResourceValidation{OpenAPIV3Schema: schemaRoot}}},
		},
	}
}

func testAuthorinoCRD(withFields bool) *apiextensionsv1.CustomResourceDefinition {
	specProperties := map[string]apiextensionsv1.JSONSchemaProps{}
	if withFields {
		specProperties["enableLoggingFields"] = apiextensionsv1.JSONSchemaProps{Type: "boolean"}
	}
	return &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: authorinoCRDName},
		Status: apiextensionsv1.CustomResourceDefinitionStatus{Conditions: []apiextensionsv1.CustomResourceDefinitionCondition{{
			Type: apiextensionsv1.Established, Status: apiextensionsv1.ConditionTrue,
		}}},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group:    "operator.authorino.kuadrant.io",
			Names:    apiextensionsv1.CustomResourceDefinitionNames{Plural: "authorinos", Singular: "authorino", Kind: "Authorino"},
			Scope:    apiextensionsv1.NamespaceScoped,
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{Name: "v1beta1", Served: true, Storage: true, Schema: &apiextensionsv1.CustomResourceValidation{OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{Type: "object", Properties: map[string]apiextensionsv1.JSONSchemaProps{"spec": jsonSchemaObject(specProperties)}}}}},
		},
	}
}

func jsonSchemaObject(properties map[string]apiextensionsv1.JSONSchemaProps) apiextensionsv1.JSONSchemaProps {
	return apiextensionsv1.JSONSchemaProps{Type: "object", Properties: properties}
}

func jsonSchemaString() apiextensionsv1.JSONSchemaProps {
	return apiextensionsv1.JSONSchemaProps{Type: "string"}
}

func jsonSchemaInteger() apiextensionsv1.JSONSchemaProps {
	return apiextensionsv1.JSONSchemaProps{Type: "integer", Format: "int32"}
}
