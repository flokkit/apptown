package workflow

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/ids"
)

// Personal work survives gaps between runs, and neither participation nor an old
// grant makes somebody else's or a now-private change part of that work.
func TestPersonalOpenChangesPreserveRequesterAndCurrentVisibility(t *testing.T) {
	t.Parallel()
	a := stepAuthorityFixture(t, ApprovalConfig{Approvers: []string{"boss", "group:reviewers"}})
	actor, err := identity.ActorIn(t.Context(), a.DB, "owner")
	if err != nil {
		t.Fatal(err)
	}
	ctx := stepVerifiedContext(t.Context(), access.Principal{ID: actor.Principal, Kind: "user", Name: "owner", Via: access.ViaSession})
	create := func(project ids.ID, who, title string) *Change {
		t.Helper()
		ch, err := NewCreator(a.DB, nil).Create(t.Context(), ChangeDraft{ProjectID: project, Title: title}, who)
		if err != nil {
			t.Fatal(err)
		}
		return ch
	}
	idle := create(a.ProjectID, "owner", "Idle request")
	create(a.ProjectID, "outsider", "Another person's request")
	landed := create(a.ProjectID, "owner", "Landed")
	abandoned := create(a.ProjectID, "owner", "Abandoned")
	for _, move := range []struct {
		id     ids.ID
		action ChangeAction
	}{{landed.ID, ShipChange}, {abandoned.ID, abandonChange}} {
		if err := a.DB.InTx(t.Context(), func(tx *database.Tx) error {
			_, err := TransitionChangeIn(t.Context(), tx, a.ProjectID, move.id, ChangeMove{Action: move.action}, time.Now())
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	grant, err := a.Access.Grant(t.Context(), identity.Spec{SubjectType: "user", Subject: "owner", Role: access.RoleProjectMaintainer, Scope: access.ScopeProject, ScopeID: a.OtherProjectID.String(), By: "boss"}, "")
	if err != nil {
		t.Fatal(err)
	}
	hidden := create(a.OtherProjectID, "owner", "Once readable")
	rows, total, err := a.Work.OpenChanges(ctx)
	if err != nil || total != 3 || len(rows) != 3 {
		t.Fatalf("before revocation: %+v %d %v", rows, total, err)
	}
	if err := a.DB.InTx(t.Context(), func(tx *database.Tx) error {
		_, err := identity.RevokeIn(t.Context(), tx, grant, "boss", time.Now())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	rows, total, err = a.Work.OpenChanges(ctx)
	if err != nil || total != 2 || len(rows) != 2 {
		t.Fatalf("personal changes: %+v %d %v", rows, total, err)
	}
	found := map[ids.ID]PersonalOpenChange{}
	for _, row := range rows {
		found[row.ID] = row
	}
	if v := found[idle.ID]; v.State.String() != "waiting on you" || v.IdleSince != "" || v.Reason != "ready to land" || v.Number != idle.Number || v.Title != idle.Title || v.ProjectSlug != "game" {
		t.Fatalf("idle: %+v", v)
	}
	if v := found[a.ChangeID]; v.State.String() != "waiting on someone else" || !strings.Contains(v.Reason, "boss, group:reviewers") {
		t.Fatalf("other approvers: %+v", v)
	}
	if _, ok := found[hidden.ID]; ok {
		t.Fatal("revoked project leaked")
	}
	// A different display name with the same principal retains the requester's
	// idle changes; the opener's label never substitutes for principal identity.
	renamed := stepVerifiedContext(t.Context(), access.Principal{ID: actor.Principal, Kind: "user", Name: "renamed", Via: access.ViaSession})
	rows, total, err = a.Work.OpenChanges(renamed)
	if err != nil || total != 2 {
		t.Fatalf("principal selection: %+v %d %v", rows, total, err)
	}
	dbtest.FailNextStatement(t, a.DB, "-- name: PersonalOpenChanges")
	if rows, _, err := a.Work.OpenChanges(ctx); rows != nil || !errors.Is(err, dbtest.ErrInjectedFailure) {
		t.Fatalf("read failure: %+v %v", rows, err)
	}
}

func TestPersonalOpenChangesClassifyRunningAndReaderApproval(t *testing.T) {
	for _, tc := range []struct {
		name          string
		spec          schema.Spec
		state, reason string
		start, fail   bool
	}{
		{name: "approval", spec: schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "review", Steps: []schema.Step{{Name: "accept", Uses: "approval", With: map[string]any{"approvers": []string{"owner"}}}}}}}, state: "waiting on you", reason: "Approve accept"},
		{name: "running", spec: schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "work", OnEnter: []schema.Step{{Name: "build", Uses: "script"}}, Steps: []schema.Step{{Name: "accept", Uses: "approval"}}}}}, state: "running", reason: "build", start: true},
		{name: "failed check", spec: schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "work", OnEnter: []schema.Step{{Name: "build", Uses: "script"}}, Steps: []schema.Step{{Name: "accept", Uses: "approval"}}}}}, state: "waiting on you", reason: "build (fail)", start: true, fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := stepAuthoritySpecFixture(t, tc.spec)
			actor, err := identity.ActorIn(t.Context(), a.DB, "owner")
			if err != nil {
				t.Fatal(err)
			}
			ctx := stepVerifiedContext(t.Context(), access.Principal{ID: actor.Principal, Kind: "user", Name: "owner", Via: access.ViaSession})
			if tc.start {
				run, err := readRun(t.Context(), a.DB, a.RunID)
				if err != nil {
					t.Fatal(err)
				}
				steps, err := ReadScopedStepsIn(t.Context(), a.DB, RunRef{ID: run.ID, ProjectID: run.ProjectID})
				if err != nil {
					t.Fatal(err)
				}
				for _, step := range steps {
					if step.Name == "build" {
						attempts := newStepAttempts(a.DB, func(kind string) bool { return kind == "script" })
						claim, err := attempts.start(t.Context(), step, "script", a.ProjectID)
						if err != nil {
							t.Fatal(err)
						}
						if tc.fail {
							if err := attempts.finish(t.Context(), claim, StepOutcome{Status: StatusFail}); err != nil {
								t.Fatal(err)
							}
						}
					}
				}
			}
			rows, total, err := a.Work.OpenChanges(ctx)
			if err != nil || total != 1 || len(rows) != 1 {
				t.Fatalf("read: %+v %d %v", rows, total, err)
			}
			if rows[0].State.String() != tc.state || rows[0].Reason != tc.reason || rows[0].IdleSince != "" {
				t.Fatalf("classification: %+v", rows[0])
			}
		})
	}
}

func TestPersonalOpenChangesBoundAndOrderAfterVisibility(t *testing.T) {
	t.Parallel()
	a := stepAuthorityFixture(t, ApprovalConfig{Approvers: []string{"owner"}})
	actor, err := identity.ActorIn(t.Context(), a.DB, "owner")
	if err != nil {
		t.Fatal(err)
	}
	ctx := stepVerifiedContext(t.Context(), access.Principal{ID: actor.Principal, Kind: "user", Name: "owner", Via: access.ViaSession})
	var first, latest ids.ID
	for i := 0; i < 25; i++ {
		ch, err := NewCreator(a.DB, nil).Create(t.Context(), ChangeDraft{ProjectID: a.ProjectID, Title: "Idle"}, "owner")
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = ch.ID
		}
		latest = ch.ID
	}
	rows, total, err := a.Work.OpenChanges(ctx)
	if err != nil || total != 26 || len(rows) != 20 || rows[0].ID != latest {
		t.Fatalf("bounds/order: %+v %d %v", rows, total, err)
	}
	for i := 1; i < len(rows); i++ {
		if rows[i-1].UpdatedAt < rows[i].UpdatedAt {
			t.Fatalf("activity order: %+v", rows)
		}
	}

	// A new run moves an older idle request ahead of newer requests, even when
	// the older request was outside the bounded selection.
	admissions := newRunAdmissions(a.DB, NewStages(nil, func(string) bool { return false }), nil)
	_, err = admissions.records.create(t.Context(), StartRequest{ProjectID: a.ProjectID, ChangeID: first, WorkflowName: "again", InputsJSON: "{}", requestedBy: "owner"}, PreparedRun{Workflow: DefinitionRef{Name: "again", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "review", Steps: []schema.Step{{Name: "accept", Uses: "approval", With: map[string]any{"approvers": []string{"owner"}}}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	rows, total, err = a.Work.OpenChanges(ctx)
	if err != nil || total != 26 || len(rows) != 20 || rows[0].ID != first {
		t.Fatalf("run activity order: %+v %d %v", rows, total, err)
	}
	// No stable principal supplies no personal identity, even with a known name.
	rows, total, err = a.Work.OpenChanges(stepVerifiedContext(context.Background(), access.Principal{Kind: "user", Name: "owner", Via: access.ViaSession}))
	if err != nil || total != 0 || len(rows) != 0 {
		t.Fatalf("name-only attribution: %+v %d %v", rows, total, err)
	}
}

// No running run does not mean no work or no next act. A queued landing is
// the remaining idle case: no workflow is running, and Land is not offered.
func TestPersonalOpenChangesWithoutRunningRun(t *testing.T) {
	for _, name := range []string{"landing", "ready to land", "chat editing", "idle"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a := stepAuthorityFixture(t, ApprovalConfig{Approvers: []string{"owner"}})
			actor, err := identity.ActorIn(t.Context(), a.DB, "owner")
			if err != nil {
				t.Fatal(err)
			}
			ctx := stepVerifiedContext(t.Context(), access.Principal{ID: actor.Principal, Kind: "user", Name: "owner", Via: access.ViaSession})
			ch, err := NewCreator(a.DB, nil).Create(t.Context(), ChangeDraft{ProjectID: a.ProjectID, Title: name}, "owner")
			if err != nil {
				t.Fatal(err)
			}
			wantState, wantReason := "waiting on you", "ready to land"
			switch name {
			case "landing":
				ch.ID = a.ChangeID
				// sql-guarantee: hold a settled run before any follow-up job starts; engine completion dispatches it.
				completionExec(t, a.DB, `UPDATE runs SET status='done' WHERE id=$1`, a.RunID)
				err = a.DB.InTx(t.Context(), func(tx *database.Tx) error {
					_, err := TransitionChangeIn(t.Context(), tx, a.ProjectID, ch.ID, ChangeMove{Action: LandChange, RunID: a.RunID}, time.Now())
					return err
				})
				if err != nil {
					t.Fatal(err)
				}
				wantState, wantReason = "running", "landing"
			case "chat editing":
				a.Work.dataDir = t.TempDir()
				ws := (Layout{DataDir: a.Work.dataDir}).ChangeWorkspace(ch.ID)
				if err := os.MkdirAll(ws, 0755); err != nil {
					t.Fatal(err)
				}
				if out, err := gitexec.Run(ctx, ws, "init", "--template=", "-b", "main"); err != nil {
					t.Fatalf("%s: %v", out, err)
				}
				if err := os.WriteFile(filepath.Join(ws, "work.txt"), []byte("editing"), 0644); err != nil {
					t.Fatal(err)
				}
				wantState, wantReason = "running", "being edited in a chat"
			case "idle":
				ch.ID = a.ChangeID
				// sql-guarantee: completing through the engine dispatches the landing too; hold the settled run before that job starts.
				completionExec(t, a.DB, `UPDATE runs SET status='done' WHERE id=$1`, a.RunID)
				// sql-guarantee: hold the completion reservation before its landing job starts.
				completionExec(t, a.DB, `INSERT INTO workflow_landings(work_run_id) VALUES ($1)`, a.RunID)
				wantState, wantReason = "idle", "No workflow is running"
			}
			rows, _, err := a.Work.OpenChanges(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range rows {
				if row.ID != ch.ID {
					continue
				}
				if row.State.String() != wantState || row.Reason != wantReason || (row.IdleSince != "") != (name == "idle") {
					t.Fatalf("classification: %+v", row)
				}
				if name == "idle" && row.IdleSince != row.UpdatedAt {
					t.Fatalf("idle since: %+v", row)
				}
				if name == "chat editing" {
					overview, err := a.Work.OpenChangesWithoutRuns(ctx, a.ProjectID, 10)
					if err != nil {
						t.Fatal(err)
					}
					for _, item := range overview {
						if item.ID == ch.ID && item.Editing != 1 {
							t.Fatalf("overview: %+v", item)
						}
					}
				}
				return
			}
			t.Fatal("change missing")
		})
	}
}

// Eligibility includes current named-group membership; a previous vote leaves
// the outstanding quorum to others and says why the reader is not asked again.
func TestPersonalOpenChangesGroupApprovalAndPreviousVote(t *testing.T) {
	for _, voted := range []bool{false, true} {
		t.Run(map[bool]string{false: "group member", true: "already voted"}[voted], func(t *testing.T) {
			t.Parallel()
			a := stepAuthorityFixture(t, ApprovalConfig{Quorum: 2, Approvers: []string{"group:reviewers"}})
			if err := a.Access.SaveGroup(t.Context(), identity.GroupChange{Name: "reviewers", Members: []string{"owner", "boss"}}, testAdmittedPrincipal(t, a.Access, "boss")); err != nil {
				t.Fatal(err)
			}
			actor, err := identity.ActorIn(t.Context(), a.DB, "owner")
			if err != nil {
				t.Fatal(err)
			}
			ctx := stepVerifiedContext(t.Context(), access.Principal{ID: actor.Principal, Kind: "user", Name: "owner", Via: access.ViaSession})
			wantState, wantReason := "waiting on you", "Approve accept"
			if voted {
				if _, err := a.Work.Approve(ctx, Approval{StepID: a.StepID, Decision: VoteApprove}); err != nil {
					t.Fatal(err)
				}
				wantState, wantReason = "waiting on someone else", "Approval needed for accept from group:reviewers; you have already voted"
			}
			rows, total, err := a.Work.OpenChanges(ctx)
			if err != nil || total != 1 || len(rows) != 1 {
				t.Fatalf("read: %+v %d %v", rows, total, err)
			}
			if rows[0].State.String() != wantState || rows[0].Reason != wantReason || rows[0].IdleSince != "" {
				t.Fatalf("approval: %+v", rows[0])
			}
		})
	}
}
