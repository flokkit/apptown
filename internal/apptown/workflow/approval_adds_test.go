package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
)

func finished(t *testing.T, a ChangeAdds) ChangeAdds {
	t.Helper()
	out, err := a.finish()
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// "Nothing new" always names the rows it covers, and only rows that were
// read: a row that could not be read is listed as unknown, never folded
// into "nothing new".
func TestNothingNewNamesOnlyTheRowsItRead(t *testing.T) {
	all := finished(t, ChangeAdds{Head: "h", Rows: []AddsRow{{Name: "secrets"}, {Name: "services"}, {Name: "app parts"}}})
	if all.NothingNew != "Nothing new in secrets, services or app parts" || all.ReadFrom != "secrets, services and app parts" || len(all.Lines) != 0 {
		t.Fatalf("nothing added: %+v", all)
	}
	partly := finished(t, ChangeAdds{Head: "h", Rows: []AddsRow{{Name: "secrets", Unknown: "secretspec.toml at abc does not parse"}, {Name: "services"}, {Name: "app parts"}}})
	if partly.NothingNew != "Nothing new in services or app parts" || strings.Contains(partly.ReadFrom, "secrets") {
		t.Fatalf("an unknown row is counted as read: %+v", partly)
	}
	if len(partly.Lines) != 1 || partly.Lines[0].Kind != "unknown" || !strings.Contains(partly.Lines[0].Text, "Secrets: unknown") {
		t.Fatalf("the unknown row is not shown: %+v", partly.Lines)
	}
	none := finished(t, ChangeAdds{Head: "h", Rows: []AddsRow{{Name: "secrets", Unknown: "x"}, {Name: "services", Unknown: "x"}}})
	if none.NothingNew != "" || none.ReadFrom != "" {
		t.Fatalf("nothing read, yet: %+v", none)
	}
	added := finished(t, ChangeAdds{Head: "h", Rows: []AddsRow{{Name: "secrets", Added: []string{"STRIPE_KEY"}}, {Name: "services", Removed: []string{"redis"}}}})
	if added.NothingNew != "" || len(added.Lines) != 2 || added.Lines[0].Text != "Secrets: STRIPE_KEY" || added.Lines[1].Text != "Services removed: redis" {
		t.Fatalf("added: %+v", added)
	}
	// Removals alone add nothing: the section says so and lists them.
	removed := finished(t, ChangeAdds{Head: "h", Rows: []AddsRow{{Name: "services", Removed: []string{"redis"}}}})
	if removed.NothingNew != "Nothing new in services" || len(removed.Lines) != 1 || removed.Lines[0].Kind != "removed" {
		t.Fatalf("removals only: %+v", removed)
	}
}

// A row read only on the change's side lists what the change declares,
// says so, and says that what it removes is not known. A row that declares
// nothing adds nothing whatever main holds, so it counts toward "nothing
// new"; its removals are still unknown.
func TestARowReadOnlyOnTheChangesSideSaysSo(t *testing.T) {
	a := finished(t, ChangeAdds{Head: "h", Rows: []AddsRow{
		{Name: "app parts", Added: []string{"part game"}, Removed: []string{"part old"}, BaseUnread: ".apptown/app.yaml at b does not parse"},
		{Name: "services", BaseUnread: ".apptown/app.yaml at b does not parse"},
	}})
	if len(a.Rows[0].Removed) != 0 {
		t.Fatalf("removals kept against an unreadable main: %+v", a.Rows[0])
	}
	want := []AddsLine{
		{"added", "App parts: part game (everything the change declares; main's could not be read, so some may already be there)"},
		{"unknown", "App parts removed: unknown, .apptown/app.yaml at b does not parse"},
		{"unknown", "Services removed: unknown, .apptown/app.yaml at b does not parse"},
	}
	if len(a.Lines) != len(want) {
		t.Fatalf("lines %+v, want %+v", a.Lines, want)
	}
	for i := range want {
		if a.Lines[i] != want[i] {
			t.Fatalf("line %d %+v, want %+v", i, a.Lines[i], want[i])
		}
	}
	if a.ReadFrom != "app parts and services" || a.NothingNew != "" {
		t.Fatalf("read %q nothing-new %q", a.ReadFrom, a.NothingNew)
	}
	none := finished(t, ChangeAdds{Head: "h", Rows: []AddsRow{{Name: "services", BaseUnread: "x"}}})
	if none.NothingNew != "Nothing new in services" {
		t.Fatalf("a row adding nothing against an unreadable main: %+v", none)
	}
	if finished(t, ChangeAdds{Head: "h", Rows: []AddsRow{{Name: "services", BaseUnread: "x"}}}).Digest == finished(t, ChangeAdds{Head: "h", Rows: []AddsRow{{Name: "services"}}}).Digest {
		t.Fatal("main's side being unread is not part of the digest")
	}
}

// The digest names the commits and the rows: a different base, head or
// row is a different list. Rows that could not be read list nothing, so a
// reader cannot record items beside an unknown.
func TestTheDigestNamesCommitsAndRows(t *testing.T) {
	base := finished(t, ChangeAdds{Base: "b", Head: "h", Rows: []AddsRow{{Name: "secrets", Added: []string{"A"}}}})
	for name, other := range map[string]ChangeAdds{
		"base":    {Base: "b2", Head: "h", Rows: []AddsRow{{Name: "secrets", Added: []string{"A"}}}},
		"head":    {Base: "b", Head: "h2", Rows: []AddsRow{{Name: "secrets", Added: []string{"A"}}}},
		"row":     {Base: "b", Head: "h", Rows: []AddsRow{{Name: "secrets", Added: []string{"B"}}}},
		"unknown": {Base: "b", Head: "h", Rows: []AddsRow{{Name: "secrets", Unknown: "x"}}},
	} {
		if finished(t, other).Digest == base.Digest {
			t.Errorf("a different %s has the same digest", name)
		}
	}
	if again := finished(t, ChangeAdds{Base: "b", Head: "h", Rows: []AddsRow{{Name: "secrets", Added: []string{"A"}}}}); again.Digest != base.Digest {
		t.Fatal("the same list has another digest")
	}
	odd := finished(t, ChangeAdds{Head: "h", Rows: []AddsRow{{Name: "secrets", Unknown: "x", Added: []string{"A"}}}})
	if len(odd.Rows[0].Added) != 0 {
		t.Fatalf("an unknown row kept items: %+v", odd.Rows[0])
	}
}

// A landing's vote assents to its pinned candidate; a list read for any
// other commit cannot be recorded with it.
func TestAVoteIsRefusedWhenItsListWasReadForAnotherCandidate(t *testing.T) {
	ctx := context.Background()
	db, change := changeEditFixture(t, ChangeDraft{Title: "landing"})
	automatic := func(kind string) bool { return kind == "reconcile" }
	admissions := newRunAdmissions(db, NewStages(nil, automatic), nil)
	run, err := admissions.Create(ctx, StartRequest{ProjectID: change.ProjectID, ChangeID: change.ID, WorkflowName: "land", InputsJSON: "{}", RequestedBy: "operator"}, PreparedRun{
		Workflow: DefinitionRef{Name: "land", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{{Name: "gates", Steps: []schema.Step{
			{Name: "catch-up", Uses: "reconcile"}, {Name: "approve", Uses: "approval", With: map[string]any{"means": "I approve this source"}},
		}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := admissions.Prepared(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	attempts := newStepAttempts(db, automatic)
	snapshot, err := readStep(ctx, db, admittedStepID(t, queries{db: db}, run, "catch-up"))
	if err != nil {
		t.Fatal(err)
	}
	claim, err := attempts.Start(ctx, snapshot, snapshot.Type)
	if err != nil {
		t.Fatal(err)
	}
	e := &Execution{service: &Service{db: db}, ctx: ctx, step: claim, run: run, claim: claim}
	if _, err := e.PinLandingCandidate(LandingCandidate{Candidate: "candidate-X", Target: "main"}); err != nil {
		t.Fatal(err)
	}
	if err := attempts.Finish(ctx, claim, StepOutcome{Status: StatusPass, Evidence: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	approval := admittedStepID(t, queries{db: db}, run, "approve")
	vote := func(head string) error {
		_, err := database.Transact(ctx, db, func(tx *database.Tx) (reworkedResult, error) {
			current, step, err := lockStep(ctx, tx, approval)
			if err != nil {
				return reworkedResult{}, err
			}
			return reworkStore{db, clocked{}}.approveIn(ctx, tx, current, step, ApprovalVote{Approver: "bls", Decision: VoteApprove, Adds: &ChangeAdds{Head: head}})
		})
		return err
	}
	if err := vote("candidate-Y"); !errors.Is(err, errAddsChanged) {
		t.Fatalf("recorded a list read for another candidate: %v", err)
	}
	if err := vote("candidate-X"); err != nil {
		t.Fatal(err)
	}
}

// An approval that assents to a commit shows what that commit adds, and a
// vote is recorded only with the list the person was shown, for the commit
// it was read for: without the list's digest, with another digest, or with
// one read before the branch moved, the vote is refused. The vote records
// the list and the commit. The commit is the change's branch in its folder;
// what it adds is the reader's (AppTown's comparer is changeadds').
func TestAnApprovalOfACommitRecordsWhatItAddsAsShown(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a := stepAuthorityFixture(t, ApprovalConfig{Approvers: []string{"owner"}})
	if err := project.New(a.DB, nil).SetDefaultBranch(ctx, a.ProjectID, "main"); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	source, err := ReadSourceIn(ctx, a.DB, RunRef{ID: a.RunID, ProjectID: a.ProjectID})
	if err != nil {
		t.Fatal(err)
	}
	folder := source.Workspace(dir)
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, folder, "init", "-q", "-b", source.Branch)
	commit := func(message string) string {
		t.Helper()
		gittest.RunAsTester(t, folder, "commit", "-q", "--allow-empty", "-m", message)
		return gittest.Run(t, folder, "rev-parse", "HEAD")
	}
	first := commit("declarations")
	a.Work.pins = landingPins{db: a.DB, dataDir: dir}
	a.Work.adds = approvalAdds{reader: fixedAdds{{Name: "secrets", Added: []string{"STRIPE_KEY"}}}, pins: a.Work.pins}
	owner := stepVerifiedContext(ctx, access.Principal{Kind: "user", Name: "owner", Via: "token"})
	q, err := a.Work.ApprovalQuestion(owner, a.StepID)
	if err != nil || q.Adds == nil || q.Adds.Head != first {
		t.Fatalf("an acceptance of a commit shows nothing of what it adds: %+v %v", q.Adds, err)
	}
	if _, err := a.Work.Approve(owner, Approval{StepID: a.StepID, Decision: VoteApprove}); !errors.Is(err, errAddsNotShown) {
		t.Fatalf("approved without the list: %v", err)
	}
	if _, err := a.Work.Approve(owner, Approval{StepID: a.StepID, Decision: VoteApprove, Adds: "not-the-list"}); !errors.Is(err, errAddsChanged) {
		t.Fatalf("approved another list: %v", err)
	}
	head := commit("more declarations")
	if _, err := a.Work.Approve(owner, Approval{StepID: a.StepID, Decision: VoteApprove, Adds: q.Adds.Digest}); !errors.Is(err, errAddsChanged) {
		t.Fatalf("approved the list read before the branch moved: %v", err)
	}
	again, err := a.Work.ApprovalQuestion(owner, a.StepID)
	if err != nil || again.Adds == nil || again.Adds.Head != head {
		t.Fatalf("question after the branch moved: %+v %v", again.Adds, err)
	}
	if _, err := a.Work.Approve(owner, Approval{StepID: a.StepID, Decision: VoteApprove, Adds: again.Adds.Digest}); err != nil {
		t.Fatal(err)
	}
	step, err := readStep(ctx, a.DB, a.StepID)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := ReadApprovalEvidence(step.Evidence)
	if err != nil || len(evidence.Decisions) != 1 || evidence.Decisions[0].Adds == nil {
		t.Fatalf("the vote does not record what it was shown: %s %v", step.Evidence, err)
	}
	vote := evidence.Decisions[0]
	if vote.Adds.Digest != again.Adds.Digest || vote.Adds.Head != head || vote.Candidate != head {
		t.Fatalf("recorded %+v (candidate %s), shown %+v", vote.Adds, vote.Candidate, again.Adds)
	}
}
