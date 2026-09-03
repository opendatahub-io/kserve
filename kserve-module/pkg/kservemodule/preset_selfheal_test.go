package kservemodule

import (
	"testing"

	. "github.com/onsi/gomega"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

// Deterministic regression guard for RHOAIENG-88471.
//
// kserve-module installs the serving.kserve.io CRDs itself and then ships
// unowned LLMInferenceServiceConfig presets. A dynamic watch recreates a deleted
// preset, but it registers only during a reconcile that runs while the CRD
// exists and is seen by the client. Two things broke that:
//
//  1. The self-installed CRD is not in the cached client's store during the
//     reconcile that installs it, so a cached existence check misses it. The fix
//     reads through an uncached API reader.
//  2. Once the controller reached a no-requeue happy state the watch never
//     registered again. The fix gates the happy path: it requeues until every
//     selfInstalled watch is registered.
//
// A supporting change makes the CustomResourceDefinition watch predicate match
// the CRDs backing the module's own dynamic watches, so an externally recreated
// CRD also enqueues a reconcile.
//
// These tests pin the wiring at the unit level so they are immune to the
// reconcile-loop timing that makes an end-to-end envtest unable to isolate the
// change (unconditional status writes and CRD-not-yet-cached error backoff both
// keep reconciles firing, which registers the watch regardless).

// reconcilerWithDynamicWatches builds a reconciler carrying the same dynamic
// watches SetupWithManager wires up, without needing a manager. It uses the real
// buildDynamicWatches so the tests cannot drift from production wiring.
func reconcilerWithDynamicWatches() *KserveModuleReconciler {
	r := &KserveModuleReconciler{}
	r.dynamicWatches = r.buildDynamicWatches()
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

// TestDynamicWatches_SelfInstalledFlags pins which watches gate the happy path.
// The reconcile requeues until every selfInstalled watch registers; that must
// cover exactly the CRDs this module installs itself (serving.kserve.io) and not
// external/optional CRDs, which would otherwise requeue forever when absent.
func TestDynamicWatches_SelfInstalledFlags(t *testing.T) {
	g := NewWithT(t)
	selfInstalled := map[string]bool{}
	for _, dw := range reconcilerWithDynamicWatches().dynamicWatches {
		selfInstalled[crdResourceName(dw.groupKind)] = dw.selfInstalled
	}

	g.Expect(selfInstalled).To(HaveKeyWithValue("llminferenceserviceconfigs.serving.kserve.io", true))
	g.Expect(selfInstalled).To(HaveKeyWithValue("localmodelnodegroups.serving.kserve.io", true))
	// External operator CRD: must not hold up the happy path when absent.
	g.Expect(selfInstalled).To(HaveKeyWithValue("leaderworkersetoperators.operator.openshift.io", false))
}

// TestRegisterDynamicWatches_NilControllerNotPending guards that registration
// reports no pending work before the controller is built, so the happy path is
// not blocked during setup.
func TestRegisterDynamicWatches_NilControllerNotPending(t *testing.T) {
	g := NewWithT(t)
	r := reconcilerWithDynamicWatches() // r.controller is nil
	g.Expect(r.registerDynamicWatches(t.Context())).To(BeFalse())
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
