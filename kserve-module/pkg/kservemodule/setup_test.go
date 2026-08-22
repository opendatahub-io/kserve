package kservemodule

import (
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	. "github.com/onsi/gomega"
)

// The preset watch is scoped by namespace and nothing else. Narrowing it by the
// well-known annotation would filter on a field a user can remove, and removing
// it is the edit that has to be reverted: the event would be dropped for
// carrying the very change it was needed for.
//
// Driving presetWatchFilter rather than a copy declared here is what makes this
// a regression test - the annotation creeping back into the filter fails it.
func TestPresetWatchFilter_SurvivesAnnotationRemoval(t *testing.T) {
	g := NewWithT(t)
	pred := predicate.NewPredicateFuncs(presetWatchFilter("opendatahub"))

	wellKnown := map[string]any{wellKnownAnnotationKey: wellKnownAnnotationValue}
	shipped := presetObject("opendatahub", wellKnown)
	stripped := presetObject("opendatahub", nil)
	elsewhere := presetObject("user-models", wellKnown)

	g.Expect(pred.Update(event.UpdateEvent{ObjectOld: shipped, ObjectNew: stripped})).
		Should(BeTrue(), "stripping the annotation must still enqueue a reconcile")
	g.Expect(pred.Update(event.UpdateEvent{ObjectOld: shipped, ObjectNew: shipped})).
		Should(BeTrue(), "an edit that keeps the annotation must enqueue a reconcile")
	g.Expect(pred.Create(event.CreateEvent{Object: shipped})).Should(BeTrue(), "create event")
	g.Expect(pred.Delete(event.DeleteEvent{Object: shipped})).Should(BeTrue(), "delete event")

	g.Expect(pred.Create(event.CreateEvent{Object: elsewhere})).
		Should(BeFalse(), "a copy outside the applications namespace must not enqueue a reconcile")
	g.Expect(pred.Delete(event.DeleteEvent{Object: elsewhere})).
		Should(BeFalse(), "deleting a copy elsewhere must not enqueue a reconcile")
}

// A watch with no filter registers no predicate at all, so it sees every event
// for its kind.
func TestDynamicWatchPredicates_NoFilterRegistersNone(t *testing.T) {
	NewWithT(t).Expect((&dynamicWatch{}).predicates()).Should(BeNil())
}

func TestDynamicWatchPredicates_FilterRegistersOne(t *testing.T) {
	dw := &dynamicWatch{filterFn: presetWatchFilter("opendatahub")}
	NewWithT(t).Expect(dw.predicates()).Should(HaveLen(1))
}
