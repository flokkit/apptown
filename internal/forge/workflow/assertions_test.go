package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/forge/adoption"
	"flokkit.io/forge/internal/forge/adrs"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
	"flokkit.io/forge/internal/ids"
)

func TestAssertionArtifactSelectionAndCommittedSnapshot(t *testing.T) {
	t.Parallel()
	s, run, id := assertionOwnerFixture(t, "artifact/require", `{"kind":"report","name":"final"}`)
	first, err := s.Evaluate(context.Background(), Assertion{StepID: id})
	if err != nil || first.Status != StatusFail {
		t.Fatalf("missing artifact: %+v %v", first, err)
	}
	artifact := storedTextArtifact(t, s.db, run.ID, "report", "final", "commit", "")
	storedTextArtifact(t, s.db, run.ID, "report", "other", "wrong", "")
	second, err := s.Evaluate(context.Background(), Assertion{StepID: id})
	if err != nil || second.Status != StatusPass || !second.FinishedAt.Valid {
		t.Fatalf("selected artifact: %+v %v", second, err)
	}
	var evidence struct {
		ArtifactID ids.ID `json:"artifactId"`
		Ref        string `json:"ref"`
	}
	if err := json.Unmarshal(second.Evidence, &evidence); err != nil || evidence.ArtifactID != artifact || evidence.Ref != "commit" {
		t.Fatalf("evidence: %+v %v", evidence, err)
	}
	if first.Status != StatusFail || first.OwnedAttempt() != 0 || second.OwnedAttempt() != 0 {
		t.Fatal("read snapshot was mutated or gained execution ownership")
	}
	if _, err := s.Evaluate(context.Background(), Assertion{StepID: id}); err != nil {
		t.Fatal(err)
	}
	wantEventCount(t, s.db, 2, ofKind("check.evaluated"))
}

func TestAssertionADRRequiresProjectChangeStatusAndEveryTag(t *testing.T) {
	t.Parallel()
	s, run, id := assertionOwnerFixture(t, "adr/require", `{"tags":["security","api"]}`)
	other := workflowTestProject(t, s.db, "other", "Other", "")
	for _, value := range []struct {
		project, change ids.ID
		status, tags    string
	}{
		{other, run.ChangeID, "accepted", "security,api"}, {run.ProjectID, ids.Nil, "accepted", "security,api"},
		{run.ProjectID, run.ChangeID, "draft", "security,api"}, {run.ProjectID, run.ChangeID, "accepted", "security"},
	} {
		if value.project == other {
			// sql-guarantee: The ADR belongs to the foreign project but references this project's change, isolating the project filter; ADR Create/Edit reject this inconsistent association and its internal generated writer is inaccessible here.
			completionInsert(t, s.db, `INSERT INTO adrs(id,project_id,change_id,number,title,status,tags,created_at,updated_at) VALUES (?,?,?,1,'Decision','accepted',?,'2026-10-01','2026-10-01')`, value.project, value.change, value.tags)
		} else {
			createScopeADR(t, s.db, adrs.Create{ProjectID: value.project, ChangeID: value.change, Title: "Decision", Tags: value.tags}, value.status == "accepted")
		}
	}
	result, err := s.Evaluate(context.Background(), Assertion{StepID: id})
	if err != nil || result.Status != StatusFail {
		t.Fatalf("unrelated ADR counted: %+v %v", result, err)
	}
	accepted := createScopeADR(t, s.db, adrs.Create{ProjectID: run.ProjectID, ChangeID: run.ChangeID, Title: "Decision", Tags: "security, api, extra"}, true)
	result, err = s.Evaluate(context.Background(), Assertion{StepID: id})
	if err != nil || result.Status != StatusPass {
		t.Fatalf("matching ADR missed: %+v %v", result, err)
	}
	var evidence struct {
		ADRs []struct {
			ID ids.ID `json:"id"`
		} `json:"adrs"`
	}
	if err := json.Unmarshal(result.Evidence, &evidence); err != nil || len(evidence.ADRs) != 1 || evidence.ADRs[0].ID != accepted {
		t.Fatalf("evidence: %+v %v", evidence, err)
	}
}

func TestAssertionSourceFailureAndMalformedConfigurationDoNotBecomeVerdicts(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, typ, config, fault string }{
		{"artifact read", "artifact/require", `{}`, "FROM artifacts"}, {"ADR read", "adr/require", `{}`, "FROM adrs"},
		{"artifact options", "artifact/require", `{"kind":[]}`, ""}, {"ADR options", "adr/require", `{"tags":"security"}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, _, id := assertionOwnerFixture(t, tc.typ, tc.config)
			if tc.fault != "" {
				dbtest.FailNextStatement(t, s.db, tc.fault)
			}
			got, err := s.Evaluate(context.Background(), Assertion{StepID: id})
			if got != nil || err == nil {
				t.Fatalf("invalid evaluation succeeded: %+v %v", got, err)
			}
			if tc.fault != "" {
				// QueryRow's fault is an actual failed PostgreSQL statement;
				// multi-row queries can return the injected error directly.
				var pgerr *pgconn.PgError
				if !errors.Is(err, database.ErrInjectedFailure) && !(errors.As(err, &pgerr) && pgerr.Code == "42883") {
					t.Fatalf("wrong read error: %v", err)
				}
			}
			wantUntouchedStep(t, s.db, id, StatusPending)
			wantEventCount(t, s.db, 3, anyEvent)
		})
	}
}

func TestAssertionResultAndReceiptAreAtomic(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"write", "receipt", "suppressed write", "suppressed receipt", "commit", "readback"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			s, _, id := assertionOwnerFixture(t, "artifact/require", `{}`)
			switch failure {
			case "write":
				dbtest.FailNextStatement(t, s.db, "name: RecordStepEvaluation")
			case "receipt":
				dbtest.FailNextStatement(t, s.db, "INSERT INTO run_events")
			case "readback":
				dbtest.FailNextStatement(t, s.db, "name: StepByID")
			default:
				table, event, body := "steps", "UPDATE", "RETURN NULL;"
				if failure == "suppressed receipt" {
					table, event = "run_events", "INSERT"
				}
				trigger := "CREATE TRIGGER veto_assertion BEFORE " + event + " ON " + table
				if failure == "commit" {
					body = "RAISE EXCEPTION 'commit refused';"
					trigger = "CREATE CONSTRAINT TRIGGER veto_assertion AFTER UPDATE ON steps DEFERRABLE INITIALLY DEFERRED"
				}
				completionExec(t, s.db, `CREATE FUNCTION veto_assertion() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN `+body+` END $$`)
				completionExec(t, s.db, trigger+" FOR EACH ROW EXECUTE FUNCTION veto_assertion()")
			}
			result, err := s.Evaluate(context.Background(), Assertion{StepID: id})
			if result != nil || err == nil {
				t.Fatalf("%s returned success: %+v %v", failure, result, err)
			}
			wantUntouchedStep(t, s.db, id, StatusPending)
			wantEventCount(t, s.db, 3, anyEvent)
		})
	}
}

func TestAssertionMergeResultRequiresItsPreparedScope(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"run ended", "stage", "revision", "definition", "attempt", "status", "configuration", "evidence", "finished", "override"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s, run, id := assertionOwnerFixture(t, "scm/merged", `{}`)
			s.merge = mergeObserverFunc(func(context.Context, MergeAssertion) (MergeObservation, error) {
				switch name {
				case "run ended":
					// sql-guarantee: assertionScope.matches compares the whole Run, including UpdatedAt; TransitionRun also stamps updated_at and could reject the snapshot without checking status, so change only status at this original interference point.
					completionExec(t, s.db, `UPDATE runs SET status='done' WHERE id=?`, run.ID)
				case "stage":
					// sql-guarantee: assertionScope.matches compares the whole Run, including UpdatedAt; TransitionRun also stamps updated_at and could reject the snapshot without checking current_stage, so change only current_stage at this original interference point.
					completionExec(t, s.db, `UPDATE runs SET current_stage='later' WHERE id=?`, run.ID)
				case "revision":
					// sql-guarantee: assertionScope.matches compares the whole Run, including UpdatedAt; InvalidateRunDecision also stamps updated_at and could reject the snapshot without checking retry_revision, so change only retry_revision at this original interference point.
					completionExec(t, s.db, `UPDATE runs SET retry_revision=retry_revision+1 WHERE id=?`, run.ID)
				case "definition":
					// sql-guarantee: Replace the pinned definition during the external merge observation so the prepared assertion must be rejected; admission pins definitions and no generated mutation rewrites one.
					completionExec(t, s.db, `UPDATE runs SET definition_json='{}' WHERE id=?`, run.ID)
				case "attempt":
					incrementScopeAttempt(t, s.db, id)
				case "status":
					// sql-guarantee: SameStep compares FinishedAt as well as Status; RecordApproval and RecordStepEvaluation stamp finished_at for fail, so isolate status after the merge snapshot without an independent timestamp mismatch.
					completionExec(t, s.db, `UPDATE steps SET status='fail' WHERE id=?`, id)
				case "configuration":
					retryScopeStep(t, s.db, id, json.RawMessage(`{"different":true}`))
				case "evidence":
					recordScopeStep(t, s.db, workflowsql.RecordApprovalParams{At: time.Now(), ID: id, Status: "pending", EvidenceJson: json.RawMessage(`{"newer":true}`)})
				case "finished":
					if n, err := workflowsql.New(s.db.SQL()).RecordStepEvaluation(context.Background(), workflowsql.RecordStepEvaluationParams{At: time.Now(), ID: id, Attempt: 0, ExpectedStatus: "pending", Status: "pending", EvidenceJson: json.RawMessage(`{}`)}); err != nil || n != 1 {
						t.Fatalf("stamp pending evaluation: %d %v", n, err)
					}
				case "override":
					// sql-guarantee: SameStep compares FinishedAt independently of the override fields; OverrideStep also stamps finished_at, so retain the exact status/actor/reason mutation without masking its snapshot fence.
					completionExec(t, s.db, `UPDATE steps SET status='pass',override_by='alice',override_reason='reviewed' WHERE id=?`, id)
				}
				return MergeObservation{Merged: true}, nil
			})
			result, err := s.Evaluate(context.Background(), Assertion{StepID: id})
			if result != nil || !errors.Is(err, ErrAssertionChanged) {
				t.Fatalf("stale %s accepted: %+v %v", name, result, err)
			}
			wantEventCount(t, s.db, 3, anyEvent)
		})
	}
}

func TestAssertionMergeDoesNotHoldAConnectionOrShareItsSnapshot(t *testing.T) {
	t.Parallel()
	s, run, id := assertionOwnerFixture(t, "scm/merged", `{}`)
	s.db.Pool().SetMaxOpenConns(1)
	s.merge = mergeObserverFunc(func(ctx context.Context, copy MergeAssertion) (MergeObservation, error) {
		if _, err := readRun(ctx, s.db, copy.Source.RunID); err != nil {
			return MergeObservation{}, err
		}
		copy.Source.Branch = "a collaborator's local edit"
		return MergeObservation{Merged: true}, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, err := s.Evaluate(ctx, Assertion{StepID: id})
	if err != nil || result.Status != StatusPass {
		t.Fatalf("external evaluation: %+v %v", result, err)
	}
	wantCurrentStage(t, s.db, run.ID, "finish")
}

func TestAssertionConcurrentMergeResultsHaveOneWinner(t *testing.T) {
	t.Parallel()
	s, _, id := assertionOwnerFixture(t, "scm/merged", `{}`)
	entered, release := make(chan struct{}, 2), make(chan struct{})
	s.merge = mergeObserverFunc(func(context.Context, MergeAssertion) (MergeObservation, error) {
		entered <- struct{}{}
		<-release
		return MergeObservation{Merged: true}, nil
	})
	results := make(chan error, 2)
	for range 2 {
		go func() { _, err := s.Evaluate(context.Background(), Assertion{StepID: id}); results <- err }()
	}
	for range 2 {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			close(release)
			t.Fatal("both evaluations did not prepare")
		}
	}
	close(release)
	winners := 0
	for range 2 {
		err := <-results
		if err == nil {
			winners++
		} else if !errors.Is(err, ErrAssertionChanged) {
			t.Fatal(err)
		}
	}
	if winners != 1 {
		t.Fatalf("winners: %d", winners)
	}
	wantEventCount(t, s.db, 1, ofKind("check.evaluated"))
}

func TestAssertionCancelledAdmissionReleasesItsLocks(t *testing.T) {
	t.Parallel()
	s, run, id := assertionOwnerFixture(t, "artifact/require", `{}`)
	gate, err := s.db.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	holdProjectLock(t, gate, run.ProjectID)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer gate.Rollback()
	done := make(chan error, 1)
	go func() { _, err := s.Evaluate(ctx, Assertion{StepID: id}); done <- err }()
	dbtest.WaitForLock(t, s.db, "-- name: ProjectForUpdate")
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled assertion kept waiting")
	}
	if err := gate.Rollback(); err != nil {
		t.Fatal(err)
	}
	wantEventCount(t, s.db, 3, anyEvent)
	if _, err := s.Evaluate(context.Background(), Assertion{StepID: id}); err != nil {
		t.Fatal(err)
	}
}

func TestAssertionAdmissionPreservesOverridesAndRejectsInactiveWork(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"overridden", "running", "done", "old stage", "unsupported"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			kind := "scm/merged"
			if scenario == "unsupported" {
				kind = "approval"
			}
			s, run, id := assertionOwnerFixture(t, kind, `{}`)
			switch scenario {
			case "overridden":
				recordScopeStep(t, s.db, workflowsql.RecordApprovalParams{At: time.Now(), ID: id, Status: "pending", EvidenceJson: json.RawMessage(`{"keep":true}`)})
				overrideScopeStep(t, s.db, workflowsql.OverrideStepParams{At: time.Now(), ID: id, OverrideBy: "alice", OverrideReason: "reviewed"})
			case "running":
				recordScopeStep(t, s.db, workflowsql.RecordApprovalParams{At: time.Now(), ID: id, Status: "running"})
			case "done":
				transitionScopeRun(t, s.db, workflowsql.TransitionRunParams{At: time.Now(), ID: run.ID, NextStatus: "done"})
			case "old stage":
				transitionScopeRun(t, s.db, workflowsql.TransitionRunParams{At: time.Now(), ID: run.ID, NextStage: "later"})
			}
			s.merge = mergeObserverFunc(func(context.Context, MergeAssertion) (MergeObservation, error) {
				t.Error("ineligible step performed external work")
				return MergeObservation{}, nil
			})
			got, err := s.Evaluate(context.Background(), Assertion{StepID: id})
			if scenario == "overridden" {
				if err != nil || got.OverrideBy != "alice" || got.Status != StatusPass || string(got.Evidence) != `{"keep":true}` {
					t.Fatalf("override changed: %+v %v", got, err)
				}
			} else if err == nil || got != nil {
				t.Fatalf("ineligible %s accepted: %+v %v", scenario, got, err)
			}
			wantEventCount(t, s.db, 3, anyEvent)
		})
	}
}

func TestAssertionCancelledExternalResultCannotCommit(t *testing.T) {
	t.Parallel()
	s, _, id := assertionOwnerFixture(t, "scm/merged", `{}`)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.merge = mergeObserverFunc(func(context.Context, MergeAssertion) (MergeObservation, error) {
		cancel()
		return MergeObservation{Merged: true}, nil
	})
	result, err := s.Evaluate(ctx, Assertion{StepID: id})
	if result != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled result committed: %+v %v", result, err)
	}
	wantUntouchedStep(t, s.db, id, StatusPending)
	wantEventCount(t, s.db, 3, anyEvent)
}

type mergeObserverFunc func(context.Context, MergeAssertion) (MergeObservation, error)

func (f mergeObserverFunc) ObserveMerge(ctx context.Context, in MergeAssertion) (MergeObservation, error) {
	return f(ctx, in)
}

func TestAssertionMergeRefusesChangedSourceMetadata(t *testing.T) {
	t.Parallel()
	for _, mutation := range []string{"default branch", "change branch", "revision"} {
		t.Run(mutation, func(t *testing.T) {
			t.Parallel()
			owner, run, id := assertionOwnerFixture(t, "scm/merged", `{}`)
			owner.merge = mergeObserverFunc(func(context.Context, MergeAssertion) (MergeObservation, error) {
				switch mutation {
				case "default branch":
					if err := project.New(owner.db, nil).SetDefaultBranch(context.Background(), run.ProjectID, "trunk"); err != nil {
						t.Fatal(err)
					}
				case "change branch":
					if n, err := workflowsql.New(owner.db.SQL()).SetChangeBranch(context.Background(), workflowsql.SetChangeBranchParams{ID: run.ChangeID, ProjectID: run.ProjectID, Branch: "changed"}); err != nil || n != 1 {
						t.Fatalf("change source branch: %d %v", n, err)
					}
				case "revision":
					if err := owner.db.InTx(context.Background(), func(tx *database.Tx) error {
						return adoption.RecordIn(t.Context(), tx, run.ProjectID, 1, adoption.AdoptionCreate, "source changed", adoption.EmptyReceipt(), "alice", time.Now())
					}); err != nil {
						t.Fatal(err)
					}
				}
				return MergeObservation{Merged: true}, nil
			})
			result, err := owner.Evaluate(context.Background(), Assertion{StepID: id})
			if result != nil || !errors.Is(err, ErrAssertionChanged) {
				t.Fatalf("stale source assertion: %+v %v", result, err)
			}
			wantStepCount(t, owner.db, 1, func(s *Step) bool { return s.Status == StatusPending && jsonEqual(s.Evidence, `{}`) })
			wantEventCount(t, owner.db, 3, anyEvent)
		})
	}
}

func TestAssertionCommandFencesAuthorizedSnapshot(t *testing.T) {
	t.Parallel()
	for _, changed := range []string{"project", "run", "attempt", "config"} {
		t.Run(changed, func(t *testing.T) {
			t.Parallel()
			owner, run, id := assertionOwnerFixture(t, "artifact/require", `{"kind":"report"}`)
			other := workflowTestProject(t, owner.db, "other", "Other", "")
			s := newService(owner.db, authorityFunc(func(_ context.Context, p operationPermission) (string, error) {
				if p.Action == actionRunStep {
					switch changed {
					case "project":
						// sql-guarantee: Reparent run.project_id inside authorization while preserving its step and attempt, isolating the authorized-project fence; admission and existing mutations do not reparent runs.
						completionExec(t, owner.db, `UPDATE runs SET project_id=?,number=number+100000 WHERE id=?`, other, run.ID)
					case "run":
						// sql-guarantee: Replace run.change_id with a missing change inside authorization to invalidate the authorized source; admission requires an owned change and no generated mutation changes this identity.
						completionExec(t, owner.db, `UPDATE runs SET change_id='00000000-03db-7000-8000-000000000000' WHERE id=?`, run.ID)
					case "attempt":
						incrementScopeAttempt(t, owner.db, id)
					case "config":
						retryScopeStep(t, owner.db, id, json.RawMessage(`{"kind":"other"}`))
					}
				}
				return "alice", nil
			}), Runtime{})
			if step, err := s.Evaluate(context.Background(), id); step != nil || !errors.Is(err, ErrAssertionChanged) {
				t.Fatalf("changed %s accepted: %+v %v", changed, step, err)
			}
			wantEventCount(t, owner.db, 0, ofKind("check.evaluated"))
		})
	}
}

func createScopeADR(t *testing.T, db *database.DB, in adrs.Create, accepted bool) ids.ID {
	t.Helper()
	testAccounts(t, db, "alice")
	owner := adrs.New(db, changeTransitionAuthority{}, nil, nil)
	ctx := changeTransitionContext(context.Background(), "alice")
	row, err := owner.Create(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if accepted {
		if _, err := owner.Edit(ctx, adrs.Edit{ID: row.ID, Decision: adrs.Accept}); err != nil {
			t.Fatal(err)
		}
	}
	return row.ID
}
