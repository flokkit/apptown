package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
)

func invocationFixture(t *testing.T) (*Service, InvokeChange) {
	t.Helper()
	return invocationRunnerFixture(t, nil)
}

func invocationRunnerFixture(t *testing.T, runner Runner) (*Service, InvokeChange) {
	t.Helper()
	db := testDB(t)
	id := workflowTestProject(t, db, "invoke", "Invoke", "")
	if err := db.InTx(context.Background(), func(tx *database.Tx) error {
		if err := adoption.InitializeIn(t.Context(), tx, id, 0, ""); err != nil {
			return err
		}
		return adoption.RecordIn(t.Context(), tx, id, 1, adoption.AdoptionCreate, "fixture", adoption.EmptyReceipt(), "operator", time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	deps := Dependencies{
		Git: GitWorkspace{DataDir: t.TempDir()}, Repositories: &scm.RepositoryLocks{},
		Abandonment: abandonRecorderFunc(noAbandonEffects),
		Resolve: func(_ context.Context, req StartRequest, src StartSource) (PreparedRun, error) {
			if req.WorkflowName == "missing" {
				return PreparedRun{}, errors.New("unknown workflow")
			}
			return PreparedRun{Workflow: DefinitionRef{Name: req.WorkflowName, Version: 1}, AdoptionID: src.AdoptionID,
				Spec: schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "review", Steps: []schema.Step{{Name: "accept", Uses: "approval"}}}}}}, nil
		},
	}
	if runner != nil {
		deps.Automatic = []string{"script"}
		deps.Runners = map[string]Runner{"script": runner}
		resolve := deps.Resolve
		deps.Resolve = func(ctx context.Context, req StartRequest, source StartSource) (PreparedRun, error) {
			def, err := resolve(ctx, req, source)
			if err == nil {
				def.Spec.Stages[0].OnEnter = []schema.Step{{Name: "work", Uses: "script"}}
			}
			return def, err
		}
	}
	s := New(context.Background(), db, deps)
	s.authority = allowProject(id)
	t.Cleanup(s.Stop)
	return s, InvokeChange{Change: ChangeDraft{ProjectID: id, Title: "Atomic work"}, Workflow: "test", Inputs: []byte(`{"precise":9007199254740993}`)}
}

// failingParticipant writes through the admission transaction and then fails,
// so a surviving receipt would show that the participant was not rolled back.
func failingParticipant(t *testing.T) AdmissionParticipant {
	return func(ctx context.Context, tx *database.Tx, ch *Change) error {
		if ch == nil || ch.ID.IsZero() {
			t.Error("participant ran without the admitted Change")
		}
		if err := audit.At(time.Now()).RecordIn(ctx, tx, "operator", ch.ProjectID, "change.created", "participant write", nil); err != nil {
			return err
		}
		return errors.New("participant refused")
	}
}

// A participant's failure rolls back a plain Change admission too, before any
// workspace is prepared.
func TestCreateWithParticipantFailureLeavesNoChange(t *testing.T) {
	t.Parallel()
	s, in := invocationFixture(t)
	s.creator.prepare = func(context.Context, CreatedChange) error {
		t.Error("rejected Change prepared a workspace")
		return nil
	}
	if ch, err := s.CreateWith(t.Context(), in.Change, failingParticipant(t)); err == nil || ch != nil {
		t.Fatalf("participant failure admitted a Change: %+v %v", ch, err)
	}
	assertNoInvocation(t, s)
}

func assertNoInvocation(t *testing.T, s *Service) {
	t.Helper()
	// Independent persistence checks: a rolled-back invocation leaves none of
	// its records, even if a domain reader would hide a partial admission.
	// sql-guarantee: a whole-store total; no workflow read enumerates Changes across every project, and this proves no hidden Change row was written anywhere.
	completionCount(t, s.db, 0, `SELECT count(*) FROM changes`)
	wantRunCount(t, s.db, 0, nil)
	wantStepCount(t, s.db, 0, nil)
	wantEventCount(t, s.db, 0, anyEvent)
	wantReceiptCount(t, s.db, 0, receiptKind("change.created"))
}

func TestCreateAndRunRejectsBeforeEffectsAndRollsBackAdmission(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"create authority", "invoke authority", "unknown workflow", "required artifact", "stage insert", "adoption changed", "participant"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			s, in := invocationFixture(t)
			s.starter.runtime.Prepare = func(context.Context, *Run) error { t.Error("rejected invocation prepared a workspace"); return nil }
			s.starter.runtime.Dispatch = func(context.Context, *Run) error { t.Error("rejected invocation dispatched"); return nil }
			switch failure {
			case "create authority", "invoke authority":
				s.authority = authorityFunc(func(_ context.Context, p operationPermission) (string, error) {
					if p.Action == actionCreateChange && failure == "create authority" || p.Action == actionStartRun && failure == "invoke authority" {
						return "", &access.Denied{Reason: failure}
					}
					return "operator", nil
				})
			case "unknown workflow":
				in.Workflow = "missing"
			case "required artifact":
				s.starter.runtime.Missing = func(context.Context, ids.ID, *schema.Spec) (string, error) { return "spec-final", nil }
			case "stage insert":
				dbtest.FailNextStatement(t, s.db, "INSERT INTO steps")
			case "adoption changed":
				resolve := s.starter.runtime.Resolve
				s.starter.runtime.Resolve = func(ctx context.Context, req StartRequest, source StartSource) (PreparedRun, error) {
					def, err := resolve(ctx, req, source)
					if err != nil {
						return def, err
					}
					err = s.db.InTx(ctx, func(tx *database.Tx) error {
						return adoption.RecordIn(ctx, tx, req.ProjectID, 2, adoption.AdoptionCreate, "concurrent adoption", adoption.EmptyReceipt(), "operator", time.Now())
					})
					return def, err
				}
			case "participant":
				in.Join = failingParticipant(t)
			}
			ch, run, err := s.CreateAndRun(t.Context(), in)
			if err == nil || ch != nil || run != nil {
				t.Fatalf("failure admitted work: %+v %+v %v", ch, run, err)
			}
			assertNoInvocation(t, s)
		})
	}
}

func TestCreateAndRunCommitsBeforePreparationAndReadinessBeforeDispatch(t *testing.T) {
	t.Parallel()
	s, in := invocationFixture(t)
	prepared, dispatched := 0, 0
	s.starter.runtime.Prepare = func(ctx context.Context, run *Run) error {
		prepared++
		// These reads use another connection: admission really committed.
		wantEqual(t, "change run", storedChange(t, s.db, run.ChangeID).RunID, run.ID)
		wantStepCount(t, s.db, 1, func(s *Step) bool { return s.RunID == run.ID })
		wantRunReady(t, s.db, run.ID, false)
		return nil
	}
	s.starter.runtime.Dispatch = func(ctx context.Context, run *Run) error {
		dispatched++
		wantRunReady(t, s.db, run.ID, true)
		return nil
	}
	ch, run, err := s.CreateAndRun(t.Context(), in)
	if err != nil || prepared != 1 || dispatched != 1 || ch.RunID != run.ID {
		t.Fatalf("admission: %+v %+v %d/%d %v", ch, run, prepared, dispatched, err)
	}
	wantJSONField(t, "run inputs", json.RawMessage(storedRun(t, s.db, run.ID).InputsJSON), "precise", "9007199254740993")
	wantInferenceOwner(t, s.db, run.ID, "verified-person")
}

func TestCreateAndRunSettlesPreparationFailureWithoutAbandonGrant(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"clone", "cancelled", "readiness", "settlement", "already ready"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			s, in := invocationFixture(t)
			s.authority = authorityFunc(func(_ context.Context, p operationPermission) (string, error) {
				if p.Action == actionCloseChange {
					return "", &access.Denied{Reason: "no abandon grant"}
				}
				return "operator", nil
			})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if failure == "clone" {
				// Use the real workspace path against a repository that does not exist.
				if err := project.New(s.db, nil).SetDefaultBranch(ctx, in.Change.ProjectID, "main"); err != nil {
					t.Fatal(err)
				}
			} else {
				s.starter.runtime.Prepare = func(ctx context.Context, run *Run) error {
					switch failure {
					case "cancelled":
						cancel()
						return ctx.Err()
					case "readiness":
						dbtest.FailNextStatement(t, s.db, "-- name: MarkRunReady")
					case "settlement":
						dbtest.FailNextStatement(t, s.db, "-- name: ChangeForUpdate")
						return errors.New("workspace failed")
					case "already ready":
						if err := s.runAdmissions.Prepared(ctx, run.ID); err != nil {
							return err
						}
						return errors.New("concurrent preparer finished")
					}
					return nil
				}
			}
			s.starter.runtime.Dispatch = func(context.Context, *Run) error { t.Error("failed preparation dispatched"); return nil }
			ch, run, err := s.CreateAndRun(ctx, in)
			if err == nil || ch == nil || run == nil || !strings.Contains(err.Error(), fmt.Sprintf("change %d run %d", ch.ID, run.ID)) {
				t.Fatalf("lost committed identities: %+v %+v %v", ch, run, err)
			}
			stored, readErr := readChange(context.Background(), s.db, ch.ID)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if failure == "settlement" || failure == "already ready" {
				if stored.State != ChangeInProgress || !strings.Contains(err.Error(), "settlement failed") {
					t.Fatalf("unsafe or concealed settlement: %+v %v", stored, err)
				}
			} else {
				if stored.State != ChangeAbandoned {
					t.Fatalf("preparation left active work: %+v %v", stored, err)
				}
				wantRunState(t, s.db, run.ID, RunAbandoned)
				wantReceiptCount(t, s.db, 1, func(e audit.Entry) bool {
					reason, _ := jsonField(e.Payload, "reason")
					return e.Kind == "change.abandoned" && reason != ""
				})
			}
		})
	}
}

func TestCreateAndRunSettlementStopsWithinFiveSeconds(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		parent, stop := context.WithCancel(access.WithCaller(context.Background(), access.Caller{Receipt: "operator", Verified: true}))
		stop()
		ctx, cancel := invocationSettlementContext(parent)
		defer cancel()
		start := time.Now()
		deadline, ok := ctx.Deadline()
		caller, _ := access.CallerFrom(ctx)
		if !ok || deadline.Sub(start) != 5*time.Second || ctx.Err() != nil || caller.Receipt != "operator" || !caller.Verified {
			t.Fatal("settlement lost its independent bounded context")
		}
		<-ctx.Done()
		if time.Since(start) != 5*time.Second || !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			t.Fatal("settlement deadline drifted")
		}
	})
}

func TestCreateAndRunReportsStoppedDispatcherWithoutAbandoningReadyRun(t *testing.T) {
	t.Parallel()
	s, in := invocationFixture(t)
	s.Stop()
	ch, run, err := s.CreateAndRun(t.Context(), in)
	if !errors.Is(err, ErrDispatchStopped) || ch == nil || run == nil {
		t.Fatalf("shutdown reported success: %+v %+v %v", ch, run, err)
	}
	wantRunReady(t, s.db, run.ID, true)
	wantRunState(t, s.db, run.ID, RunRunning)
}

func TestCreateAndRunEnforcesRealCredentialBounds(t *testing.T) {
	t.Parallel()
	f := stepAuthorityFixture(t, ApprovalConfig{})
	if _, err := f.Access.Grant(context.Background(), identity.Spec{SubjectType: "user", Subject: "outsider", Capability: string(access.CapChangeCreate), Scope: access.ScopeProject, ScopeID: fmt.Sprint(f.ProjectID), Origin: "fixture", By: "boss"}, ""); err != nil {
		t.Fatal(err)
	}
	for _, pr := range []access.Principal{
		{Kind: "user", Name: "outsider", Via: "session"}, // create granted, invoke refused
		{Kind: "instrument", Name: "owner via console/c", Person: "owner", Instrument: "console", Via: "console", ProjectID: f.OtherProjectID},
	} {
		ch, run, err := f.Work.CreateAndRun(stepVerifiedContext(t.Context(), pr), InvokeChange{Change: ChangeDraft{ProjectID: f.ProjectID, Title: "No sibling"}, Workflow: "test"})
		var denied *access.Denied
		if !errors.As(err, &denied) || ch != nil || run != nil {
			t.Fatalf("credential escaped scope: %+v %+v %v", ch, run, err)
		}
	}
	// sql-guarantee: a whole-store total; no workflow read enumerates Changes across every project, and this proves no hidden Change row was written anywhere.
	completionCount(t, f.DB, 1, `SELECT count(*) FROM changes`)
}
