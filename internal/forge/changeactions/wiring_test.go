package changeactions

import (
	"context"
	"errors"
	"testing"
	"time"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/forge/adoption"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/forge/integration"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/forge/wow"
	"flokkit.io/forge/internal/ids"
)

func TestWiringModeCannotFallThroughToAdmission(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"", "chat", "workflow"} {
		mode, err := ParseWiringMode(raw)
		if err != nil || mode == "" {
			t.Fatalf("%q: %q %v", raw, mode, err)
		}
		if raw == "" && mode != WiringWorkflow {
			t.Fatal("omitted mode changed default")
		}
	}
	if _, err := ParseWiringMode("typo"); err == nil {
		t.Fatal("unknown mode parsed")
	}
	// No Work dependency: an invalid constructed value must stop before admission.
	s := &Service{}
	for _, mode := range []WiringMode{"", "typo"} {
		ch, run, err := s.OpenWiring(context.Background(), Wiring{Mode: mode, Workflow: "wire"})
		if err == nil || ch != nil || run != nil {
			t.Fatalf("invalid mode admitted work: %v %v %v", ch, run, err)
		}
	}
}

// Abandonment's cross-owner effects are not under test here; settlement only
// needs somewhere to record them.
type wiringAbandonments struct{}

func (wiringAbandonments) RecordAbandonIn(context.Context, *database.Tx, workflow.AbandonEffects) error {
	return nil
}

type wiringFixture struct {
	db      *database.DB
	work    *workflow.Service
	actions *Service
	project ids.ID
	ctx     context.Context
}

func newWiringFixture(t *testing.T) wiringFixture {
	t.Helper()
	db := databaseSuite.New(t)
	accounts := identity.New(db, identity.Config{Projects: project.NewCatalogue(db)})
	if err := accounts.SeedAdministrator(context.Background(), "operator"); err != nil {
		t.Fatal(err)
	}
	id, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "wire", Name: "Wire"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.InTx(context.Background(), func(tx *database.Tx) error {
		if err := adoption.InitializeIn(t.Context(), tx, id, 0, "", ""); err != nil {
			return err
		}
		return adoption.RecordIn(t.Context(), tx, id, 1, adoption.AdoptionCreate, "fixture", adoption.EmptyReceipt(), "operator", time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	projects := project.New(db, identity.Scoped(accounts, "project"))
	work := workflow.New(context.Background(), db, workflow.Dependencies{
		Authority:    access.Checked{Authority: identity.Scoped(accounts, "workflow"), Projects: projects},
		Abandonment:  wiringAbandonments{},
		Repositories: &scm.RepositoryLocks{},
		Resolve: func(_ context.Context, req workflow.StartRequest, src workflow.StartSource) (workflow.PreparedRun, error) {
			return workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: req.WorkflowName, Version: 1}, AdoptionID: src.AdoptionID,
				Spec: schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "review", Steps: []schema.Step{{Name: "accept", Uses: "approval"}}}}}}, nil
		},
	})
	t.Cleanup(work.Stop)
	if err := integration.NewAttachments(db).Attach(t.Context(), id, "mailer", "smtp", `{"domain":"wire.test"}`, integration.Entered, "operator", nil); err != nil {
		t.Fatal(err)
	}
	ctx := access.WithCaller(t.Context(), access.Caller{Principal: access.Principal{Kind: "user", Name: "operator", Via: "session"}, Receipt: "operator", Verified: true})
	return wiringFixture{db: db, work: work, actions: New(work, wow.New(db, wow.Dependencies{}), db), project: id, ctx: ctx}
}

func (f wiringFixture) open(mode WiringMode) (*workflow.Change, *workflow.Run, error) {
	return f.actions.OpenWiring(f.ctx, Wiring{Mode: mode, Capability: "mailer", Workflow: "adopt-integration", Who: "operator",
		Draft: workflow.ChangeDraft{ProjectID: f.project, Type: "feature", Title: "Connect Mail Relay", Body: "wire it"}})
}

// What the owners hold, read independently of any command's answer: the
// project's Changes, the attachment's linked Change and its wiring receipts.
func (f wiringFixture) persisted(t *testing.T) (changes []workflow.ChangeSummary, linked ids.ID, receipts int) {
	t.Helper()
	changes, err := f.work.Changes(f.ctx, f.project, workflow.ChangeList{})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := integration.AttachedRows(context.Background(), f.db, f.project)
	if err != nil || len(rows) != 1 {
		t.Fatalf("attachment: %+v %v", rows, err)
	}
	entries, err := audit.Recent(f.ctx, f.db, audit.Filter{ProjectID: f.project, KindPrefix: "integration.wire"})
	if err != nil {
		t.Fatal(err)
	}
	return changes, rows[0].WireChangeID, len(entries)
}

// A failure after the Change and Run are written but before (or while) the
// attachment link and its receipt are, rolls the whole admission back: no
// Change, no Run to dispatch, no link, no receipt. The retry admits exactly
// one piece of work, linked.
func TestWiringLinkFailureRollsBackAdmissionBeforeDispatch(t *testing.T) {
	t.Parallel()
	for _, mode := range []WiringMode{WiringWorkflow, WiringChat} {
		for _, statement := range []string{"-- name: AttachmentWireForUpdate", "-- name: LinkIntegrationWire", "INSERT INTO audit_log"} {
			t.Run(string(mode)+statement, func(t *testing.T) {
				t.Parallel()
				f := newWiringFixture(t)
				clear := f.db.FailNextStatement(statement)
				ch, run, err := f.open(mode)
				clear()
				if err == nil || ch != nil || run != nil {
					t.Fatalf("failed link answered: %+v %+v %v", ch, run, err)
				}
				if changes, linked, receipts := f.persisted(t); len(changes) != 0 || !linked.IsZero() || receipts != 0 {
					t.Fatalf("partial admission survived: %+v link %d receipts %d", changes, linked, receipts)
				}
				ch, run, err = f.open(mode)
				if err != nil || (mode == WiringWorkflow) != (run != nil) {
					t.Fatalf("retry: %+v %+v %v", ch, run, err)
				}
				changes, linked, receipts := f.persisted(t)
				if len(changes) != 1 || changes[0].ID != ch.ID || linked != ch.ID || receipts != 1 {
					t.Fatalf("retry admitted %+v link %d receipts %d", changes, linked, receipts)
				}
				if run != nil {
					snapshot, err := f.work.Snapshot(f.ctx, run.ID, workflow.DetailView)
					if err != nil || snapshot.Run.ChangeID != ch.ID || len(snapshot.Steps) == 0 {
						t.Fatalf("retried run was not admitted whole: %+v %v", snapshot, err)
					}
				}
			})
		}
	}
}

// Once admission commits, the Change is linked whatever happens next. A retry
// while it is live is refused with a conflict naming it, never a second
// Change; only an abandoned wiring (a failed preparation settles to that) may
// be replaced.
func TestWiringRetryNeverAdmitsSecondLiveChange(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"none", "preparation", "dispatch"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			f := newWiringFixture(t)
			switch failure {
			case "preparation":
				t.Cleanup(f.db.FailNextStatement("UPDATE run_starts SET ready"))
			case "dispatch":
				f.work.Stop()
			}
			first, _, err := f.open(WiringWorkflow)
			if (failure == "none") != (err == nil) || first == nil {
				t.Fatalf("first wiring: %+v %v", first, err)
			}
			changes, linked, receipts := f.persisted(t)
			if len(changes) != 1 || linked != first.ID || receipts != 1 {
				t.Fatalf("committed wiring not linked: %+v link %d receipts %d", changes, linked, receipts)
			}
			if failure == "preparation" {
				// Settlement abandoned the first; one retry replaces it with one Change.
				second, _, err := f.open(WiringChat)
				if err != nil || second.ID == first.ID {
					t.Fatalf("replacing abandoned wiring: %+v %v", second, err)
				}
				changes, linked, receipts = f.persisted(t)
				states := map[ids.ID]workflow.ChangeState{}
				for _, c := range changes {
					states[c.ID] = c.State
				}
				if len(changes) != 2 || states[first.ID] != workflow.ChangeAbandoned || states[second.ID] != workflow.ChangeInProgress || linked != second.ID || receipts != 2 {
					t.Fatalf("replaced wiring: %v link %d receipts %d", states, linked, receipts)
				}
				return
			}
			for _, mode := range []WiringMode{WiringWorkflow, WiringChat} {
				ch, run, err := f.open(mode)
				var open *integration.WireOpenError
				if !errors.As(err, &open) || open.ChangeID != first.ID || fault.KindOf(err) != fault.Conflict || ch != nil || run != nil {
					t.Fatalf("retry over live wiring: %+v %+v %v", ch, run, err)
				}
			}
			if changes, linked, receipts = f.persisted(t); len(changes) != 1 || linked != first.ID || receipts != 1 {
				t.Fatalf("refused retry changed the record: %+v link %d receipts %d", changes, linked, receipts)
			}
		})
	}
}
