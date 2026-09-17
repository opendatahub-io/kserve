package kservemodule

import (
	"context"
	"testing"

	. "github.com/onsi/gomega"
	nodev1 "k8s.io/api/node/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/opendatahub-io/kserve-module/pkg/apis/v1alpha1"
	"github.com/opendatahub-io/odh-platform-utilities/api/common"
	"github.com/opendatahub-io/odh-platform-utilities/pkg/cluster"
)

func TestKserveDependencies_Defined(t *testing.T) {
	g := NewWithT(t)

	g.Expect(kserveDependencies).ShouldNot(BeEmpty())

	for _, dep := range kserveDependencies {
		assertDependencyValid(g, dep)
	}
}

func TestModelControllerDependencies_Defined(t *testing.T) {
	g := NewWithT(t)

	g.Expect(modelControllerDependencies).ShouldNot(BeEmpty())

	for _, dep := range modelControllerDependencies {
		assertDependencyValid(g, dep)
	}
}

func assertDependencyValid(g Gomega, dep dependencyCheck) {
	g.Expect(dep.name).ShouldNot(BeEmpty(), "dependency must have a name")
	g.Expect(dep.checkType).ShouldNot(BeEmpty(), "dependency %s must have a checkType", dep.name)
	g.Expect(dep.platform).Should(BeElementOf("", "ocp", "xks"),
		"dependency %s has invalid platform %q", dep.name, dep.platform)

	switch dep.checkType {
	case checkCRD:
		g.Expect(dep.crdName).ShouldNot(BeEmpty(),
			"CRD dependency %s must have crdName", dep.name)
	case checkSubscription:
		g.Expect(dep.subscriptionName).ShouldNot(BeEmpty(),
			"subscription dependency %s must have subscriptionName", dep.name)
		g.Expect(dep.conditionGroup).ShouldNot(BeEmpty(),
			"subscription dependency %s must have conditionGroup", dep.name)
	case checkOperator:
		g.Expect(dep.operatorGVK.Kind).ShouldNot(BeEmpty(),
			"operator dependency %s must have operatorGVK.Kind", dep.name)
		g.Expect(dep.conditionFilter).ShouldNot(BeNil(),
			"operator dependency %s must have conditionFilter", dep.name)
	case checkOLMOperator:
		g.Expect(dep.operatorPrefix).ShouldNot(BeEmpty(),
			"OLM operator dependency %s must have operatorPrefix", dep.name)
		g.Expect(dep.conditionGroup).ShouldNot(BeEmpty(),
			"OLM operator dependency %s must have conditionGroup", dep.name)
	case checkRuntimeClass:
		g.Expect(dep.runtimeClassPrefixes).ShouldNot(BeEmpty(),
			"RuntimeClass dependency %s must have runtimeClassPrefixes", dep.name)
		g.Expect(dep.conditionGroup).ShouldNot(BeEmpty(),
			"RuntimeClass dependency %s must have conditionGroup", dep.name)
	}
}

func dependencyTestClient(objects ...client.Object) client.Client {
	scheme := runtime.NewScheme()
	_ = nodev1.AddToScheme(scheme)
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
}

func makeOperatorCondition(name string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "operators.coreos.com", Version: "v2", Kind: "OperatorCondition",
	})
	obj.SetName(name)
	obj.SetNamespace("openshift-operators")
	return obj
}

func makeRuntimeClass(name, handler string) *nodev1.RuntimeClass {
	return &nodev1.RuntimeClass{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Handler:    handler,
	}
}

func TestCheckOLMOperator(t *testing.T) {
	dep := olmOperatorDep(
		"Red Hat build of Trustee operator",
		trusteeOperatorPrefix,
		conditionConfidentialContainerDeps,
		"ocp",
		availSeverityNone,
	)

	tests := []struct {
		name    string
		objects []client.Object
		want    []string
	}{
		{
			name:    "matching operator condition",
			objects: []client.Object{makeOperatorCondition("trustee-operator.v1.0.0")},
		},
		{
			name:    "different operator",
			objects: []client.Object{makeOperatorCondition("other-operator.v1.0.0")},
			want:    []string{"Red Hat build of Trustee operator not installed"},
		},
		{
			name: "operator prefix must include separator",
			objects: []client.Object{
				makeOperatorCondition("trustee-operator-extra.v1.0.0"),
			},
			want: []string{"Red Hat build of Trustee operator not installed"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := &KserveModuleReconciler{Client: dependencyTestClient(tc.objects...)}
			g := NewWithT(t)
			g.Expect(r.checkOLMOperator(context.Background(), dep)).To(Equal(tc.want))
		})
	}
}

func TestCheckOLMOperator_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	r := &KserveModuleReconciler{Client: dependencyTestClient()}
	dep := olmOperatorDep("Trustee", trusteeOperatorPrefix, conditionConfidentialContainerDeps, "ocp", availSeverityNone)

	g := NewWithT(t)
	g.Expect(r.checkOLMOperator(ctx, dep)).To(BeEmpty())
}

func TestCheckRuntimeClass(t *testing.T) {
	dep := runtimeClassDep(
		"Confidential container RuntimeClass",
		cocoRuntimeClassPrefixes,
		conditionConfidentialContainerDeps,
		"ocp",
		availSeverityNone,
	)

	tests := []struct {
		name    string
		objects []client.Object
		want    []string
	}{
		{
			name:    "matching kata class with handler",
			objects: []client.Object{makeRuntimeClass("kata", "kata")},
		},
		{
			name:    "matching ccruntime class with handler",
			objects: []client.Object{makeRuntimeClass("ccruntime-foo", "ccruntime-foo")},
		},
		{
			name:    "matching enclave class with handler",
			objects: []client.Object{makeRuntimeClass("enclave-cc", "enclave-cc")},
		},
		{
			name:    "matching class with whitespace handler",
			objects: []client.Object{makeRuntimeClass("kata-qemu-tdx", "  \t")},
			want: []string{
				`Confidential container RuntimeClass not ready (no RuntimeClass matching any of ["kata" "ccruntime" "enclave-cc"] and a runtime handler)`,
			},
		},
		{
			name:    "different class",
			objects: []client.Object{makeRuntimeClass("katalyst", "katalyst")},
			want: []string{
				`Confidential container RuntimeClass not ready (no RuntimeClass matching any of ["kata" "ccruntime" "enclave-cc"] and a runtime handler)`,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := &KserveModuleReconciler{Client: dependencyTestClient(tc.objects...)}
			g := NewWithT(t)
			g.Expect(r.checkRuntimeClass(context.Background(), dep)).To(Equal(tc.want))
		})
	}
}

func TestCheckRuntimeClass_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	r := &KserveModuleReconciler{Client: dependencyTestClient()}
	dep := runtimeClassDep("RuntimeClass", cocoRuntimeClassPrefixes, conditionConfidentialContainerDeps, "ocp", availSeverityNone)

	g := NewWithT(t)
	g.Expect(r.checkRuntimeClass(ctx, dep)).To(BeEmpty())
}

func TestMissingConfidentialContainerDependenciesAreOptional(t *testing.T) {
	// An unrelated OperatorCondition makes the fake client exercise the same
	// successful list path used when OLM is installed, without satisfying either
	// of the CoCo operator checks.
	r := &KserveModuleReconciler{
		Client: dependencyTestClient(makeOperatorCondition("other-operator.v1.0.0")),
	}
	r.SetClusterType(cluster.ClusterTypeOpenShift)

	result := r.checkDependencies(context.Background(), &platformv1alpha1.Kserve{})
	g := NewWithT(t)
	g.Expect(result.groupReasons[conditionConfidentialContainerDeps]).To(ConsistOf(
		"Red Hat build of Trustee operator not installed",
		`Confidential container RuntimeClass not ready (no RuntimeClass matching any of ["kata" "ccruntime" "enclave-cc"] and a runtime handler)`,
	))
	g.Expect(result.availReasons).To(BeEmpty())
	g.Expect(hasCriticalFailure(result)).To(BeFalse())

	condMgr := newConditionManager(&platformv1alpha1.Kserve{})
	applyDependencyConditions(condMgr, result)
	g.Expect(condMgr.GetCondition(ConditionDependenciesAvailable).Status).To(Equal(metav1.ConditionTrue))
	g.Expect(condMgr.GetCondition(conditionConfidentialContainerDeps).Status).To(Equal(metav1.ConditionFalse))
	g.Expect(condMgr.GetCondition(string(common.ConditionTypeDegraded)).Status).To(Equal(metav1.ConditionFalse))
}

func TestLwsConditionFilter_Healthy(t *testing.T) {
	g := NewWithT(t)

	g.Expect(lwsConditionFilter("Available", "True")).Should(BeFalse())
	g.Expect(lwsConditionFilter("Degraded", "False")).Should(BeFalse())
}

func TestLwsConditionFilter_Degraded(t *testing.T) {
	g := NewWithT(t)

	g.Expect(lwsConditionFilter("Degraded", "True")).Should(BeTrue())
	g.Expect(lwsConditionFilter("Available", "False")).Should(BeTrue())
}

func TestLwsConditionFilter_TargetConfigDegraded(t *testing.T) {
	g := NewWithT(t)

	g.Expect(lwsConditionFilter("TargetConfigControllerDegraded", "True")).Should(BeTrue())
}

func TestLwsConditionFilter_Unknown(t *testing.T) {
	g := NewWithT(t)

	g.Expect(lwsConditionFilter("SomeOther", "True")).Should(BeFalse())
	g.Expect(lwsConditionFilter("", "")).Should(BeFalse())
}
