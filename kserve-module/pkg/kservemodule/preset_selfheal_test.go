package kservemodule

import (
	"testing"

	. "github.com/onsi/gomega"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

// Deterministic regression guard for RHOAIENG-88471.
//
// kserve-module installs the serving.kserve.io CRDs itself and then ships
// unowned LLMInferenceServiceConfig presets. A dynamic watch is meant to
// recreate a deleted preset, but it registers only during a reconcile that runs
// while the CRD exists. The bug: the CustomResourceDefinition watch predicate
// matched only dependency CRDs, so creating a serving.kserve.io CRD this module
// installs itself enqueued no reconcile. Once the controller reached a
// no-requeue steady state the watch never registered and deleted presets were
// never recreated.
//
// The fix makes the predicate also match the CRDs backing the module's own
// dynamic watches. These tests pin that behavior at the unit level so they are
// immune to the reconcile-loop timing that makes an end-to-end envtest unable
// to isolate the change (unconditional status writes and CRD-not-yet-cached
// error backoff both keep reconciles firing, which registers the watch
// regardless of the predicate).

// reconcilerWithDynamicWatches builds a reconciler carrying the same dynamic
// watches SetupWithManager wires up, without needing a manager.
func reconcilerWithDynamicWatches() *KserveModuleReconciler {
	r := &KserveModuleReconciler{}
	r.dynamicWatches = []*dynamicWatch{
		{groupKind: schema.GroupKind{Group: "operator.openshift.io", Kind: "LeaderWorkerSetOperator"}},
		{groupKind: schema.GroupKind{Group: "serving.kserve.io", Kind: "LocalModelNodeGroup"}},
		{groupKind: llmISVCConfigGVK.GroupKind()},
	}
	return r
}

func crdMeta(name string) *apiextensionsv1.CustomResourceDefinition {
	return &apiextensionsv1.CustomResourceDefinition{ObjectMeta: metav1.ObjectMeta{Name: name}}
}

func TestDynamicWatchCRDNames_IncludesSelfInstalledServingCRDs(t *testing.T) {
	g := NewWithT(t)
	names := reconcilerWithDynamicWatches().dynamicWatchCRDNames()

	// Derivation must match cluster.CustomResourceDefinitionExists exactly, or
	// the predicate and the existence check would disagree.
	g.Expect(names).To(HaveKey("llminferenceserviceconfigs.serving.kserve.io"))
	g.Expect(names).To(HaveKey("localmodelnodegroups.serving.kserve.io"))
	g.Expect(names).To(HaveKey("leaderworkersetoperators.operator.openshift.io"))
}

func TestCRDResourceName_MatchesExistenceCheckDerivation(t *testing.T) {
	g := NewWithT(t)
	g.Expect(crdResourceName(llmISVCConfigGVK.GroupKind())).
		To(Equal("llminferenceserviceconfigs.serving.kserve.io"))
}

// TestCRDWatchPredicate_EnqueuesForSelfInstalledCRD is the core guard: the
// predicate the CRD watch actually uses must fire for a serving.kserve.io CRD
// this module installs itself. If the wiring regresses to crdNamePredicate(nil),
// this fails.
func TestCRDWatchPredicate_EnqueuesForSelfInstalledCRD(t *testing.T) {
	g := NewWithT(t)
	pred := reconcilerWithDynamicWatches().crdWatchPredicate()

	preset := crdMeta("llminferenceserviceconfigs.serving.kserve.io")
	g.Expect(pred.Create(event.CreateEvent{Object: preset})).To(BeTrue(),
		"installing the self-shipped LLMInferenceServiceConfig CRD must enqueue a reconcile")

	// Dependency CRDs (matched by exact name and by suffix) must still enqueue.
	g.Expect(pred.Create(event.CreateEvent{
		Object: crdMeta("subscriptions.operators.coreos.com"),
	})).To(BeTrue())
	g.Expect(pred.Create(event.CreateEvent{
		Object: crdMeta("certificates.cert-manager.io"),
	})).To(BeTrue())

	// Unrelated CRDs must not enqueue.
	g.Expect(pred.Create(event.CreateEvent{
		Object: crdMeta("widgets.example.com"),
	})).To(BeFalse())
}

// TestCRDNamePredicate_WithoutDynamicWatchNames_IsTheBug documents that the
// pre-fix predicate (no dynamic-watch names) filters out the self-installed
// serving.kserve.io CRD, which is exactly why the preset watch never registered.
func TestCRDNamePredicate_WithoutDynamicWatchNames_IsTheBug(t *testing.T) {
	g := NewWithT(t)
	buggy := crdNamePredicate(nil)

	g.Expect(buggy.Create(event.CreateEvent{
		Object: crdMeta("llminferenceserviceconfigs.serving.kserve.io"),
	})).To(BeFalse(), "reproduces the bug: self-installed serving CRDs were excluded")

	// The dependency-CRD behavior is unchanged by the fix.
	g.Expect(buggy.Create(event.CreateEvent{
		Object: crdMeta("subscriptions.operators.coreos.com"),
	})).To(BeTrue())
}
