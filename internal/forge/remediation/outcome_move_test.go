package remediation

import (
	"context"
	"strings"
	"testing"

	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/workflow"
)

// A remediation attempt's outcome receipt names its change, so when the
// project moves without that change (it never landed), the receipt stays
// with it: its reason never leaves.
func TestARemediationOutcomeStaysWithAChangeThatDoesNotMove(t *testing.T) {
	f, outcomes, item, _ := outcomeFixture(t)
	if _, err := outcomes.Record(context.Background(), item, Outcome{Kind: "stuck", Reason: "private-skipped-attempt-diagnostic"}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	work, err := workflow.ExportProjectIn(ctx, f.db, item.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	changes, err := work.ChangeIDs()
	if err != nil {
		t.Fatal(err)
	}
	runs, err := work.RunIDs()
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := audit.ExportProjectIn(ctx, f.db, item.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := audit.Select(ledger, audit.Moving{Changes: changes, Runs: runs})
	if err != nil {
		t.Fatal(err)
	}
	if got.Stayed["remediation.item.stuck"] != 1 {
		t.Fatalf("the outcome receipt did not stay: %v", got.Stayed)
	}
	for _, line := range got.Lines {
		if strings.Contains(string(line), "private-skipped-attempt-diagnostic") {
			t.Fatalf("the skipped change's outcome moved: %s", line)
		}
	}
}
