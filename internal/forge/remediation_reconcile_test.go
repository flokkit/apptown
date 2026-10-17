package forge

import (
	"context"
	"testing"

	"flokkit.io/forge/internal/forge/remediation"
	"flokkit.io/forge/internal/forge/workflow"
)

func observeRemediation(t *testing.T, store remediation.Reconciliation, item remediation.Item) remediation.Observation {
	t.Helper()
	o, err := store.Observe(context.Background(), item)
	if err != nil || o == nil {
		t.Fatalf("observation: %+v %v", o, err)
	}
	return *o
}

func TestRemediationLandingRecheckRejectsOldSnapshot(t *testing.T) {
	t.Parallel()
	a, run, _ := landingRecheckFixture(t)
	req, run := landingRemediationFixture(t, a, run)
	item, _ := remediation.ReadItem(context.Background(), a.DB, req.Remediation.ItemID)
	store := remediationReconciliationFor(a)
	o := observeRemediation(t, store, item)
	decision := remediation.Decide(o)
	if decision.Action != remediation.ActionRecheckLanding {
		t.Fatalf("decision: %+v", decision)
	}
	// A person's successful recheck invalidates the earlier worker decision.
	if _, err := a.Work.RecheckLanding(asPerson("alice"), workflow.LandingRecheck{Who: "alice", ChangeID: run.ChangeID}); err != nil {
		t.Fatal(err)
	}
	before := outcomeSnapshot(t, a.DB)
	if result, err := store.Apply(context.Background(), o, decision); err != nil || result != nil {
		t.Fatalf("stale landing recheck: %+v %v", result, err)
	}
	if outcomeSnapshot(t, a.DB) != before {
		t.Fatal("stale recheck consumed retry budget or renewed lease")
	}
	requireEvents(t, a, 3, eventKind("step.recheck"))
}
