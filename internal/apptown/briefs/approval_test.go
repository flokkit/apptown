package briefs

import (
	"context"
	"encoding/json"
	"errors"

	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"

	"github.com/flokkit/apptown/internal/apptown/workflow"
)

// noBriefModel resolves every slot to no model, or fails with err.
type noBriefModel struct{ err error }

func (m noBriefModel) Resolve(context.Context, ids.ID, string) (string, string, string, error) {
	return "", "", "", m.err
}
func TestBuildApprovalBriefSkeleton(t *testing.T) {
	t.Parallel()
	h := Service{selection: noBriefModel{}}
	approval := &workflow.Step{ID: idstest.N(4), Name: "accept", Type: "approval", Status: workflow.StatusPending,
		Config: json.RawMessage(`{"means":"you are accepting the visual design","instructions":"look at the screenshots"}`)}
	brief, err := h.buildApprovalBrief(context.Background(), workflow.ApprovalBriefInputs{
		StepContext: workflow.StepContext{Step: approval, Run: &workflow.Run{ProjectID: idstest.N(1)}},
		Change:      &workflow.Change{Title: "Add dark mode", Body: "A toggle in settings.", Summary: "Implemented the toggle and wired the theme."},
		Steps: []*workflow.Step{
			{ID: idstest.N(1), Name: "build", Type: "build", Status: workflow.StatusPass},
			{ID: idstest.N(2), Name: "review", Type: "agent/review", Status: workflow.StatusPass, Evidence: json.RawMessage(`{"summary":"reads cleanly, no concerns"}`)},
			{ID: idstest.N(3), Name: "scan", Type: "scan", Status: workflow.StatusPass, OverrideBy: "bls", OverrideReason: "known false positive"},
			{ID: idstest.N(5), Name: "second-review", Type: "agent/review", Status: workflow.StatusPass, OverrideBy: "ana", OverrideReason: "the reviewer misread the spec", Evidence: json.RawMessage(`{"verdict":"fail","summary":"the toggle is missing"}`)},
			approval,
		},
		Report: "I added the toggle and tested both themes.",
		Asked:  &workflow.PersonFeedback{Note: "make the toggle remember the choice", By: "ana"},
	}, " settings.tsx | 12 ++++++++----\n 1 file changed, 8 insertions(+), 4 deletions(-)")
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"THE ASK: Add dark mode",
		"A toggle in settings.",
		"WHAT WAS ASKED SINCE (a person sent the work back; this is part of the ask):\nmake the toggle remember the choice (— ana)",
		"STORY SO FAR:",
		"Implemented the toggle and wired the theme.",
		"MACHINES ALREADY VERIFIED:",
		"reads cleanly, no concerns",                             // the review summary
		"- scan (scan): OVERRIDDEN by bls: known false positive", // the overridden scan
		// An overridden review is an override first, never a pass with a summary.
		"- second-review (agent/review): OVERRIDDEN by ana: the reviewer misread the spec (the check itself did not pass) — the toggle is missing",
		"THE DIFF (the change's whole branch against its base):\nsettings.tsx | 12 ++++++++----", // what is accepted
		"IMPLEMENTER'S REPORT",
		"I added the toggle and tested both themes.",
		"WHAT YOUR OK MEANS HERE: you are accepting the visual design",
		"GATE INSTRUCTIONS: look at the screenshots",
	} {
		if !strings.Contains(brief, want) {
			t.Fatalf("brief missing %q:\n%s", want, brief)
		}
	}
	// The approval step itself must not be listed among the machine checks.
	if strings.Contains(brief, "- accept (approval)") {
		t.Fatalf("the gate step should not list itself:\n%s", brief)
	}
}

// A fast-model selection that cannot be read is a failure, not "no model":
// the bare skeleton is not returned to be saved as the approval's brief.
func TestApprovalBriefRefusesAnUnreadModelSelection(t *testing.T) {
	t.Parallel()
	unread := errors.New("model slots unreadable")
	h := Service{selection: noBriefModel{err: unread}}
	approval := &workflow.Step{ID: idstest.N(4), Name: "accept", Type: "approval", Status: workflow.StatusPending}
	brief, err := h.buildApprovalBrief(context.Background(), workflow.ApprovalBriefInputs{
		StepContext: workflow.StepContext{Step: approval, Run: &workflow.Run{ProjectID: idstest.N(1)}},
		Change:      &workflow.Change{Title: "Add dark mode"},
		Steps:       []*workflow.Step{approval},
	}, "")
	if !errors.Is(err, unread) || brief != "" {
		t.Fatalf("an unread selection gave %q, %v", brief, err)
	}
}

func TestApprovalBriefDoesNotHideUnreadableEvidence(t *testing.T) {
	t.Parallel()
	h := Service{selection: noBriefModel{}}
	_, err := h.buildApprovalBrief(context.Background(), workflow.ApprovalBriefInputs{StepContext: workflow.StepContext{Step: &workflow.Step{ID: idstest.N(1)}, Run: &workflow.Run{}}, Steps: []*workflow.Step{{ID: idstest.N(2), Name: "tests", Evidence: json.RawMessage(`{broken`)}}}, "")
	if err == nil {
		t.Fatal("unreadable evidence became an empty machine result")
	}
}
