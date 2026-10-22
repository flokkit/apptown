package apptown

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
)

// Fixes as records (design/subsystems/remediation.md §9.1): a verdict that flips from
// failing to conforming is a fix, kept by the project and attributed to the
// Changes whose branches landed between the two judged commits.

// A fix is written by the review when a verdict flips, attributed to the
// Changes whose branches landed between the two judged commits.
func TestFixRecordedByReviewWithAttribution(t *testing.T) {
	t.Parallel()
	a := conformanceTestApp(t)
	tgLocalSh(t, a)
	pid, sha1, _ := fleetRepo(t, a, "fx", map[string]string{"a.txt": "// FIXME"})
	adopt(t, a, pid, "hygiene", 1, checksJSON(shCheck("no-fixme", noFixmeSh, "high")))
	if _, _, err := fleetReviewsFor(a).RunFleetReviewWith(testConformanceContext("t"), "manual", conformance.ReviewOptions{}); err != nil {
		t.Fatal(err)
	}
	defineWorkflows(t, a, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "fix-land", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{{Name: "land"}}}})
	a.Work.Stop()
	ctx := a.asAppTown(context.Background(), pid, "fix fixture")
	change, err := a.Work.Create(ctx, workflow.ChangeDraft{ProjectID: pid, Type: "remediation", Title: "fix"})
	if err != nil {
		t.Fatal(err)
	}
	// The fix is committed before its landing is admitted: the landing pins
	// and merges that commit.
	ws := (workflow.Layout{DataDir: a.cfg.dataDir}).ChangeWorkspace(change.ID)
	if err := os.WriteFile(filepath.Join(ws, "a.txt"), []byte("clean"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, ws, "add", "-A")
	gittest.Run(t, ws, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-qm", "fix")
	run, err := a.Work.StartRun(asLander(t, a), workflow.StartChangeRun{ChangeID: change.ID, Workflow: "fix-land"})
	if !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatal(err)
	}
	if out, err := a.Work.Advance(ctx, run.ID); err != nil || !out.Advanced {
		t.Fatalf("land fix: %+v %v", out, err)
	}
	chID := change.ID
	change, err = a.Work.Change(ctx, chID)
	if err != nil || change.State != workflow.ChangeShipped {
		t.Fatalf("fix not shipped: %+v %v", change, err)
	}
	if _, _, err := fleetReviewsFor(a).RunFleetReviewWith(testConformanceContext("t"), "manual", conformance.ReviewOptions{}); err != nil {
		t.Fatal(err)
	}
	fixes, err := a.conformance.
		ProjectFixes(testConformanceContext("t"), pid, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(fixes) != 1 || fixes[0].Check != "no-fixme" || len(fixes[0].ChangeIDs) != 1 || fixes[0].ChangeIDs[0] != chID {
		t.Fatalf("the review should record one fix attributed to change %s: %+v", chID, fixes)
	}
	if fixes[0].BeforeSHA != sha1 || fixes[0].BeforeState != conformance.DoesNotConform {
		t.Fatalf("the fix keeps the failing verdict underneath: %+v", fixes[0])
	}
	// The fix names its change and review by the numbers people read.
	if len(fixes[0].ChangeNumbers) != 1 || fixes[0].ChangeNumbers[0] != change.Number || fixes[0].ReviewNumber <= 0 {
		t.Fatalf("the fix names change %v and review %d, want change %d: %+v", fixes[0].ChangeNumbers, fixes[0].ReviewNumber, change.Number, fixes[0])
	}
	if s := conformance.FixSentence(fixes[0]); !strings.Contains(s, fmt.Sprintf("by change %d,", change.Number)) || !strings.Contains(s, fmt.Sprintf("verified by conformance review %d", fixes[0].ReviewNumber)) {
		t.Fatalf("sentence: %q", s)
	}
	// A third review with nothing flipped writes nothing new.
	if _, _, err := fleetReviewsFor(a).RunFleetReviewWith(testConformanceContext("t"), "manual", conformance.ReviewOptions{}); err != nil {
		t.Fatal(err)
	}
	fixes, err = a.conformance.
		ProjectFixes(testConformanceContext("t"), pid, 10)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(fixes); n != 1 {
		t.Fatalf("no flip, no new fix: %d", n)
	}
}

// A landing blocked on a failed gate is stuck with the gate's words — an item
// never sits in "landing" forever.
