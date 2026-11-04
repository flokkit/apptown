package briefs

import (
	"context"
	"encoding/json"
	"errors"

	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"

	"github.com/flokkit/apptown/internal/apptown/conversation"
	"github.com/flokkit/apptown/internal/apptown/modelcall"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/console"
	"github.com/flokkit/apptown/internal/console/consoletest"
)

// noBriefModel resolves every slot to no model, or fails with err.
type noBriefModel struct{ err error }

func (m noBriefModel) Resolve(context.Context, ids.ID, string) (string, string, string, error) {
	return "", "", "", m.err
}
func TestApprovalModelReceivesEvidence(t *testing.T) {
	t.Parallel()
	c := &briefCompletion{out: "Adds dark mode."}
	var material string
	c.check = func(call modelcall.Call) { material = call.User }
	h := Service{selection: briefModels{slot: "main-strong", pattern: "strong"}, completion: c}
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
	}, " settings.tsx | 12 ++++++++----\n 1 file changed, 8 insertions(+), 4 deletions(-)", "")
	if err != nil {
		t.Fatal(err)
	}

	if brief != c.out {
		t.Fatalf("brief = %q", brief)
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
		if !strings.Contains(material, want) {
			t.Fatalf("brief missing %q:\n%s", want, brief)
		}
	}
	// The approval step itself must not be listed among the machine checks.
	if strings.Contains(material, "- accept (approval)") {
		t.Fatalf("the gate step should not list itself:\n%s", brief)
	}
}

// A model selection that cannot be read is a failure, not "no model":
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
	}, "", "")
	if !errors.Is(err, unread) || brief != "" {
		t.Fatalf("an unread selection gave %q, %v", brief, err)
	}
}

func TestApprovalBriefDoesNotHideUnreadableEvidence(t *testing.T) {
	t.Parallel()
	h := Service{selection: noBriefModel{}}
	_, err := h.buildApprovalBrief(context.Background(), workflow.ApprovalBriefInputs{StepContext: workflow.StepContext{Step: &workflow.Step{ID: idstest.N(1)}, Run: &workflow.Run{}}, Steps: []*workflow.Step{{ID: idstest.N(2), Name: "tests", Evidence: json.RawMessage(`{broken`)}}}, "", "")
	if err == nil {
		t.Fatal("unreadable evidence became an empty machine result")
	}
}

// The writer must get the implementation, not infer it from file counts.
func TestApprovalWriterUsesStrongModelAndPatch(t *testing.T) {
	t.Parallel()
	f := evidenceForBriefs()
	f.in.Report = "Added ripples on touch."
	f.in.Asked = &workflow.PersonFeedback{Note: "Make dragging ripple too", By: "ana"}
	files, patch := "water.js | 100 +", strings.Repeat("// water\n", 200)+"+ ripple(pointer.x, pointer.y)"
	c := &briefCompletion{out: "Adds water that ripples where you tap or drag.", check: func(call modelcall.Call) {
		for _, want := range []string{files, patch, f.in.Report, f.in.Asked.Note} {
			if !strings.Contains(call.User, want) {
				t.Errorf("writer missing %q", want)
			}
		}
		for _, want := range []string{"under 120 words", "at most two short paragraphs", "Never list checks that passed", "Do not raise missing tests unless"} {
			if !strings.Contains(call.System, want) {
				t.Errorf("prompt missing %q", want)
			}
		}
	}}
	s := Service{selection: briefModels{slot: "main-strong", pattern: "strong"}, completion: c}
	got, err := s.buildApprovalBrief(t.Context(), f.in, files, patch)
	if err != nil || got != c.out || c.calls.Load() != 1 {
		t.Fatalf("brief %q: %v (%d calls)", got, err, c.calls.Load())
	}
}

func TestApprovalWriterReadsLatestChatRepliesWithoutReport(t *testing.T) {
	t.Parallel()
	f := evidenceForBriefs()
	f.in.Change.ID = idstest.N(4)
	agent := consoletest.NewAgent()
	chats := console.New(console.Dependencies{Agent: agent, Config: console.Config{DataDir: t.TempDir()}})
	p := &project.Project{ID: f.in.Run.ProjectID, Slug: "water"}
	open := func(change *workflow.Change) *console.Session {
		t.Helper()
		var changeID ids.ID
		if change != nil {
			changeID = change.ID
		}
		// Console admission uses these owner operations to file each session.
		session, err := chats.Open(t.Context(), console.OpenRequest{
			Scope: conversation.Key(p.ID, changeID), Labels: conversation.LabelsFor(p, change),
			Actor: "ana", Model: "strong", Workspace: console.Workspace{Path: t.TempDir()},
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if _, err := chats.End(context.Background(), session.ID); err != nil {
				t.Error(err)
			}
		})
		return session
	}
	frontDesk := open(nil)
	if err := conversation.RecordStarted(t.Context(), chats, frontDesk, f.in.Change.ID); err != nil {
		t.Fatal(err)
	}
	agent.Reply(frontDesk.ID, "Unrelated project reply")
	session := open(f.in.Change)
	agent.Reply(session.ID, "Old reply")
	agent.Reply(session.ID, "Added touch ripples")
	if err := chats.Send(t.Context(), session.ID, "Thanks"); err != nil {
		t.Fatal(err)
	}
	agent.Reply(session.ID, "Dragging now ripples too")
	c := &briefCompletion{out: "Adds touch ripples.", check: func(call modelcall.Call) {
		if !strings.Contains(call.User, "Added touch ripples") || !strings.Contains(call.User, "Dragging now ripples too") || strings.Contains(call.User, "Old reply") || strings.Contains(call.User, "Thanks") || strings.Contains(call.User, "Unrelated project reply") {
			t.Errorf("wrong chat evidence: %s", call.User)
		}
	}}
	s := Service{selection: briefModels{slot: "main-strong", pattern: "strong"}, completion: c, chats: chats}
	if _, err := s.buildApprovalBrief(t.Context(), f.in, "", ""); err != nil {
		t.Fatal(err)
	}

}
