package v1alpha1

import (
	"testing"
)

// Helpers

func assertCondExists(t *testing.T, got interface{}, name string) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s condition not found", name)
	}
}

// Workload readiness aggregation

func TestDetermineWorkloadReadiness_DefaultsToTrueWhenSubconditionsUnset(t *testing.T) {
	svc := &LLMInferenceService{}
	svc.DetermineWorkloadReadiness()

	cond := svc.GetStatus().GetCondition(WorkloadReady)
	assertCondExists(t, cond, "WorkloadReady")
	if !cond.IsTrue() {
		t.Fatalf("WorkloadReady expected True when no subconditions set; got Status=%v Reason=%q Message=%q", cond.Status, cond.Reason, cond.Message)
	}
}

func TestDetermineWorkloadReadiness_PropagatesFailureReasonAndMessage(t *testing.T) {
	svc := &LLMInferenceService{}
	// Set one subcondition to False with formatted message; others True
	svc.MarkMainWorkloadNotReady("InitError", "bad %s", "config")
	svc.MarkWorkerWorkloadReady()
	svc.MarkPrefillWorkloadReady()
	svc.MarkPrefillWorkerWorkloadReady()

	svc.DetermineWorkloadReadiness()

	cond := svc.GetStatus().GetCondition(WorkloadReady)
	assertCondExists(t, cond, "WorkloadReady")
	if !cond.IsFalse() {
		t.Fatalf("WorkloadReady expected False; got Status=%v", cond.Status)
	}
	if cond.Reason != "InitError" {
		t.Errorf("expected WorkloadReady.Reason %q, got %q", "InitError", cond.Reason)
	}
	if cond.Message != "bad config" {
		t.Errorf("expected WorkloadReady.Message %q, got %q", "bad config", cond.Message)
	}
}

func TestDetermineWorkloadReadiness_AllTrueWhenAllSubconditionsTrue(t *testing.T) {
	svc := &LLMInferenceService{}
	svc.MarkMainWorkloadReady()
	svc.MarkWorkerWorkloadReady()
	svc.MarkPrefillWorkloadReady()
	svc.MarkPrefillWorkerWorkloadReady()

	svc.DetermineWorkloadReadiness()

	cond := svc.GetStatus().GetCondition(WorkloadReady)
	assertCondExists(t, cond, "WorkloadReady")
	if !cond.IsTrue() {
		t.Fatalf("WorkloadReady expected True; got Status=%v Reason=%q Message=%q", cond.Status, cond.Reason, cond.Message)
	}
}

func TestDetermineWorkloadReadiness_OrderPriority_FirstFalseWins(t *testing.T) {
	// The order is: Main, Worker, Prefill, PrefillWorker.
	// Make Worker and Prefill false, ensure Worker (first false in order) drives the aggregate reason/message.
	svc := &LLMInferenceService{}
	svc.MarkMainWorkloadReady()
	svc.MarkWorkerWorkloadNotReady("WorkerDown", "workers offline")
	svc.MarkPrefillWorkloadNotReady("PrefillDown", "prefill offline")
	svc.MarkPrefillWorkerWorkloadReady()

	svc.DetermineWorkloadReadiness()

	cond := svc.GetStatus().GetCondition(WorkloadReady)
	assertCondExists(t, cond, "WorkloadReady")
	if !cond.IsFalse() {
		t.Fatalf("WorkloadReady expected False; got Status=%v", cond.Status)
	}
	if cond.Reason != "WorkerDown" || cond.Message != "workers offline" {
		t.Errorf("expected first-false reason/message from Worker: (%q, %q); got (%q, %q)",
			"WorkerDown", "workers offline", cond.Reason, cond.Message)
	}
}

// Router readiness aggregation

func TestDetermineRouterReadiness_DefaultsToTrueWhenSubconditionsUnset(t *testing.T) {
	svc := &LLMInferenceService{}
	svc.DetermineRouterReadiness()

	cond := svc.GetStatus().GetCondition(RouterReady)
	assertCondExists(t, cond, "RouterReady")
	if !cond.IsTrue() {
		t.Fatalf("RouterReady expected True when no subconditions set; got Status=%v Reason=%q Message=%q", cond.Status, cond.Reason, cond.Message)
	}
}

func TestDetermineRouterReadiness_PropagatesFailureReasonAndMessage(t *testing.T) {
	svc := &LLMInferenceService{}
	// Set one subcondition to False with a message; others True
	svc.MarkGatewaysReady()
	svc.MarkHTTPRoutesNotReady("HTTPNotReady", "route missing")
	svc.MarkInferencePoolsReady()
	svc.MarkSchedulerWorkloadReady()

	svc.DetermineRouterReadiness()

	cond := svc.GetStatus().GetCondition(RouterReady)
	assertCondExists(t, cond, "RouterReady")
	if !cond.IsFalse() {
		t.Fatalf("RouterReady expected False; got Status=%v", cond.Status)
	}
	if cond.Reason != "HTTPNotReady" {
		t.Errorf("expected RouterReady.Reason %q, got %q", "HTTPNotReady", cond.Reason)
	}
	if cond.Message != "route missing" {
		t.Errorf("expected RouterReady.Message %q, got %q", "route missing", cond.Message)
	}
}

func TestDetermineRouterReadiness_AllTrueWhenAllSubconditionsTrue(t *testing.T) {
	svc := &LLMInferenceService{}
	svc.MarkGatewaysReady()
	svc.MarkHTTPRoutesReady()
	svc.MarkInferencePoolsReady()
	svc.MarkSchedulerWorkloadReady()

	svc.DetermineRouterReadiness()

	cond := svc.GetStatus().GetCondition(RouterReady)
	assertCondExists(t, cond, "RouterReady")
	if !cond.IsTrue() {
		t.Fatalf("RouterReady expected True; got Status=%v Reason=%q Message=%q", cond.Status, cond.Reason, cond.Message)
	}
}

func TestDetermineRouterReadiness_OrderPriority_FirstFalseWins(t *testing.T) {
	// Order: Gateways, HTTPRoutes, InferencePools, SchedulerWorkload.
	// Make InferencePools and Scheduler false; ensure InferencePools drives aggregate.
	svc := &LLMInferenceService{}
	svc.MarkGatewaysReady()
	svc.MarkHTTPRoutesReady()
	svc.MarkInferencePoolsNotReady("PoolsDown", "inference pools offline")
	svc.MarkSchedulerWorkloadNotReady("SchedDown", "scheduler offline")

	svc.DetermineRouterReadiness()

	cond := svc.GetStatus().GetCondition(RouterReady)
	assertCondExists(t, cond, "RouterReady")
	if !cond.IsFalse() {
		t.Fatalf("RouterReady expected False; got Status=%v", cond.Status)
	}
	if cond.Reason != "PoolsDown" || cond.Message != "inference pools offline" {
		t.Errorf("expected first-false reason/message from InferencePools: (%q, %q); got (%q, %q)",
			"PoolsDown", "inference pools offline", cond.Reason, cond.Message)
	}
}

// Individual condition marking (example on PresetsCombined) and ConditionSet plumbing

func TestMarkPresetsCombined_Transitions(t *testing.T) {
	svc := &LLMInferenceService{}

	svc.MarkPresetsCombinedReady()
	cond := svc.GetStatus().GetCondition(PresetsCombined)
	assertCondExists(t, cond, "PresetsCombined")
	if !cond.IsTrue() {
		t.Fatalf("PresetsCombined expected True; got Status=%v", cond.Status)
	}

	svc.MarkPresetsCombinedNotReady("PresetError", "invalid preset")
	cond = svc.GetStatus().GetCondition(PresetsCombined)
	assertCondExists(t, cond, "PresetsCombined")
	if !cond.IsFalse() {
		t.Fatalf("PresetsCombined expected False; got Status=%v", cond.Status)
	}
	if cond.Reason != "PresetError" || cond.Message != "invalid preset" {
		t.Errorf("expected PresetsCombined reason/message (%q, %q); got (%q, %q)",
			"PresetError", "invalid preset", cond.Reason, cond.Message)
	}
}

func TestGetConditionSet_ManageCanSetWorkloadReady(t *testing.T) {
	svc := &LLMInferenceService{}
	svc.GetConditionSet().Manage(svc.GetStatus()).MarkTrue(WorkloadReady)

	cond := svc.GetStatus().GetCondition(WorkloadReady)
	assertCondExists(t, cond, "WorkloadReady")
	if !cond.IsTrue() {
		t.Fatalf("WorkloadReady expected True after direct Manage().MarkTrue; got Status=%v", cond.Status)
	}
}