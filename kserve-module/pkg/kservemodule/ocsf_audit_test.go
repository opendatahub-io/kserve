package kservemodule

import (
	"context"
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
	r, cli := newOCSFReconciler(t, kserve, testTelemetryPolicyCRD(true), testGatewayConfigMap(), authorino)

	g.Expect(r.reconcileOCSFAuditLogging(context.Background(), kserve)).To(Succeed())

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
	r, cli := newOCSFReconciler(t, kserve, testTelemetryPolicyCRD(true), testGatewayConfigMap())
	ctx := context.Background()

	g.Expect(r.reconcileOCSFAuditLogging(ctx, kserve)).To(Succeed())
	policy := &unstructured.Unstructured{}
	policy.SetGroupVersionKind(telemetryPolicyGVK)
	key := client.ObjectKey{Name: telemetryPolicyName, Namespace: "gateway-system"}
	g.Expect(cli.Get(ctx, key, policy)).To(Succeed())
	policy.Object["spec"].(map[string]any)["targetRef"].(map[string]any)["name"] = "drifted-gateway"
	g.Expect(cli.Update(ctx, policy)).To(Succeed())

	g.Expect(r.reconcileOCSFAuditLogging(ctx, kserve)).To(Succeed())
	g.Expect(cli.Get(ctx, key, policy)).To(Succeed())
	desired := desiredOCSFPolicy(kserve, "gateway-system", "shared-llm-gateway")
	g.Expect(policy.Object["spec"]).To(Equal(desired.Object["spec"]))
	stable := policy.DeepCopy()
	g.Expect(r.reconcileOCSFAuditLogging(ctx, kserve)).To(Succeed())
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
	r, cli := newOCSFReconciler(t, kserve, testTelemetryPolicyCRD(true), owned, user)

	g.Expect(r.reconcileOCSFAuditLogging(context.Background(), kserve)).To(Succeed())
	deleted := &unstructured.Unstructured{}
	deleted.SetGroupVersionKind(telemetryPolicyGVK)
	err := cli.Get(context.Background(), client.ObjectKeyFromObject(owned), deleted)
	g.Expect(apierrors.IsNotFound(err)).To(BeTrue())
	kept := &unstructured.Unstructured{}
	kept.SetGroupVersionKind(telemetryPolicyGVK)
	g.Expect(cli.Get(context.Background(), client.ObjectKeyFromObject(user), kept)).To(Succeed())
}

func TestReconcileOCSFAuditLoggingReportsMissingOrIncompatibleRHCL(t *testing.T) {
	for _, tt := range []struct {
		name string
		crd  *apiextensionsv1.CustomResourceDefinition
	}{
		{name: "missing", crd: nil},
		{name: "incompatible", crd: testTelemetryPolicyCRD(false)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)
			kserve := testKserve(platformv1alpha1.AuditProfileMetadata)
			objects := []client.Object{kserve, testGatewayConfigMap()}
			if tt.crd != nil {
				objects = append(objects, tt.crd)
			}
			r, _ := newOCSFReconciler(t, objects...)
			g.Expect(r.reconcileOCSFAuditLogging(context.Background(), kserve)).To(Succeed())
			g.Expect(r.ocsfAuditLoggingCondition).NotTo(BeNil())
			g.Expect(r.ocsfAuditLoggingCondition.reason).To(Equal(reasonRHCLDependencyUnavailable))
			g.Expect(r.ocsfAuditLoggingCondition.message).To(ContainSubstring("RHCL 1.5.1 or later"))
			condMgr := newConditionManager(kserve)
			applyOCSFAuditLoggingCondition(condMgr, kserve, r.ocsfAuditLoggingCondition)
			var conditionReason, conditionMessage string
			for _, condition := range kserve.Status.Conditions {
				if condition.Type == ConditionAuditLoggingReady {
					conditionReason = condition.Reason
					conditionMessage = condition.Message
				}
			}
			g.Expect(conditionReason).To(Equal(reasonRHCLDependencyUnavailable))
			g.Expect(conditionMessage).To(ContainSubstring("RHCL 1.5.1 or later"))
		})
	}
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

func testTelemetryPolicyCRD(withOCSF bool) *apiextensionsv1.CustomResourceDefinition {
	schemaRoot := &apiextensionsv1.JSONSchemaProps{Type: "object", Properties: map[string]apiextensionsv1.JSONSchemaProps{
		"spec": {Type: "object"},
	}}
	if withOCSF {
		schemaRoot.Properties["spec"] = jsonSchemaObject(map[string]apiextensionsv1.JSONSchemaProps{
			"logging": jsonSchemaObject(map[string]apiextensionsv1.JSONSchemaProps{
				"default": jsonSchemaObject(map[string]apiextensionsv1.JSONSchemaProps{
					"ocsf": jsonSchemaObject(map[string]apiextensionsv1.JSONSchemaProps{
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
					}),
				}),
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

func jsonSchemaObject(properties map[string]apiextensionsv1.JSONSchemaProps) apiextensionsv1.JSONSchemaProps {
	return apiextensionsv1.JSONSchemaProps{Type: "object", Properties: properties}
}

func jsonSchemaString() apiextensionsv1.JSONSchemaProps {
	return apiextensionsv1.JSONSchemaProps{Type: "string"}
}

func jsonSchemaInteger() apiextensionsv1.JSONSchemaProps {
	return apiextensionsv1.JSONSchemaProps{Type: "integer", Format: "int32"}
}
