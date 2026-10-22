package mcpapi

import (
	"testing"

	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestHumanBytes(t *testing.T) {
	t.Parallel()
	for n, want := range map[int]string{812: "812 B", 4300: "4.2k", 21504: "21k"} {
		if got := humanBytes(n); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", n, got, want)
		}
	}
}

// MCP's waiting answer is Work's reading: the approval Work offers, and
// "ready to advance" only when Work offers to advance. A stage whose gate is
// missing evidence, with a pending approval and nothing offered, is blocked,
// not ready.
func TestWaitingReadsWorksOffer(t *testing.T) {
	t.Parallel()
	run := &workflow.Run{Status: workflow.RunRunning, CurrentStage: "finish"}
	steps := []*workflow.Step{
		{ID: idstest.N(1), Stage: "finish", Name: "work", Type: "agent/pi", Status: workflow.StatusPass},
		{ID: idstest.N(2), Stage: "finish", Name: "accept", Type: "approval", Status: workflow.StatusPending, Config: []byte(`{"means":"I read it"}`)},
		{ID: idstest.N(3), Stage: "finish", Name: "sign-off", Type: "approval", Status: workflow.StatusPending, Config: []byte(`{"means":"I sign"}`)},
	}
	offered := mcpWaiting(&workflow.RunSnapshot{Run: run, Steps: steps, State: workflow.ActivityWaiting,
		Next: []workflow.NextAct{{Act: workflow.NextApprove, StepID: idstest.N(3), Step: "sign-off"}}})
	if offered["on"] != "a person" || offered["stepId"] != idstest.N(3) || offered["means"] != "I sign" {
		t.Fatalf("offered approval: %v", offered)
	}
	blocked := mcpWaiting(&workflow.RunSnapshot{Run: run, Steps: steps, State: workflow.ActivityBlocked, StateDetail: "the stage is missing evidence", Next: []workflow.NextAct{}})
	if blocked["on"] != "a fix" || blocked["why"] == "ready to advance" {
		t.Fatalf("missing evidence: %v", blocked)
	}
	ready := mcpWaiting(&workflow.RunSnapshot{Run: run, Steps: steps[:1], State: workflow.ActivityReady, Next: []workflow.NextAct{{Act: workflow.NextAdvance}}})
	if ready["on"] != "nothing" || ready["why"] != "ready to advance" {
		t.Fatalf("ready: %v", ready)
	}
	if unknown := mcpWaiting(&workflow.RunSnapshot{Run: run, Steps: steps[:1], Next: []workflow.NextAct{}}); unknown["on"] != "not known" {
		t.Fatalf("unknown: %v", unknown)
	}
}
