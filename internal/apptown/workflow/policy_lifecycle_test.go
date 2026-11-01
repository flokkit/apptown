package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
)

type policyLawRules struct{}

func (policyLawRules) ViolationsIn(ctx context.Context, q database.Queryer, projectID ids.ID, spec *schema.Spec) ([]LawViolation, error) {
	policies, err := adoption.ReadProjectPolicies(ctx, q, projectID)
	if err != nil {
		return nil, err
	}
	evaluations, err := adoption.PrepareEvaluations(ctx, q, policies)
	if err != nil {
		return nil, err
	}
	var violations []LawViolation
	for _, p := range evaluations {
		if detail := p.Detail(spec); detail != "" {
			violations = append(violations, LawViolation{Policy: p.Name, Rule: p.Rule, Reading: p.Reading, Detail: detail})
		}
	}
	return violations, nil
}

func policyGateFixture(t *testing.T) (*database.DB, *Run) {
	t.Helper()
	spec := schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{{Name: "finish", Steps: []schema.Step{{Name: "deploy", Uses: "deploy"}}}, {Name: "later"}}}
	store, change := changeEditFixture(t, ChangeDraft{Title: "Policy gate"})
	run := insertStoredRun(t, store, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: change.ProjectID, ChangeID: change.ID, WorkflowName: "test", WorkflowVersion: 1, Kind: "pipeline", Status: RunRunning.String(), CurrentStage: "finish", DefinitionJson: json.RawMessage(mustJSON(spec))})
	installPolicy(t, store, run.ProjectID, "scan-first", "requireStepBefore", map[string]any{"uses": "deploy", "requires": "scan"})
	run, err := ReadRunIn(context.Background(), store, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	return store, run
}

func installPolicy(t *testing.T, db *database.DB, projectID ids.ID, name, rule string, with map[string]any) {
	t.Helper()
	if err := adoption.New(db, adoption.Dependencies{}).InstallPolicy(t.Context(), projectID, name, "", rule, with, "", "alice"); err != nil {
		t.Fatal(err)
	}
}

func removePolicies(t *testing.T, db *database.DB, projectID ids.ID) {
	t.Helper()
	policies, err := adoption.ReadProjectPolicies(context.Background(), db, projectID)
	if err != nil {
		t.Fatal(err)
	}
	s := adoption.New(db, adoption.Dependencies{})
	for _, p := range policies {
		if err := s.RemovePolicy(t.Context(), p.ID, "alice"); err != nil {
			t.Fatal(err)
		}
	}
}

// A standing gate as merge admission records it: the pinned rules were
// violated the last time this landing asked to merge.
func pausedByLaw(t *testing.T, db *database.DB, run *Run) ids.ID {
	t.Helper()
	id := insertStoredStep(t, db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: run.CurrentStage, Name: "current-rules", Type: "policy/current", Status: StatusPending.String(), ConfigJson: json.RawMessage(`{}`)})
	evidence := json.RawMessage(`{"violations":[{"policy":"scan-first","rule":"requireStepBefore","reading":"","detail":"old"}],"reason":"old"}`)
	if n, err := workflowsql.New(db.SQL()).RecordApproval(context.Background(), workflowsql.RecordApprovalParams{At: time.Now(), Status: StatusNeedsHuman.String(), EvidenceJson: evidence, ID: id, ExpectedStatus: StatusPending.String()}); err != nil || n != 1 {
		t.Fatalf("record standing policy gate: %d %v", n, err)
	}
	return id
}

func projectLaws(db *database.DB) lawSet { return newLaws(db, policyLawRules{}) }

func TestPolicyRefreshFollowsCurrentRulesWithoutInventingGates(t *testing.T) {
	t.Parallel()
	db, run := policyGateFixture(t)
	laws, ctx := projectLaws(db), context.Background()
	// Without a standing gate a refresh observes nothing: enforcement belongs to merge admission.
	if err := laws.refresh(ctx, run); err != nil {
		t.Fatal(err)
	}
	wantStepCount(t, db, 0, nil)
	gate := pausedByLaw(t, db, run)
	if err := laws.refresh(ctx, run); err != nil {
		t.Fatal(err)
	}
	wantStepState(t, db, gate, StatusNeedsHuman)
	wantEventCount(t, db, 0, anyEvent)
	// An unchanged decision does not rewrite its history.
	completionExec(t, db, `CREATE FUNCTION reject_policy_rewrite() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'unexpected rewrite'; END $$`)
	completionExec(t, db, `CREATE TRIGGER reject_policy_rewrite BEFORE UPDATE ON steps FOR EACH ROW EXECUTE FUNCTION reject_policy_rewrite()`)
	if err := laws.refresh(ctx, run); err != nil {
		t.Fatalf("unchanged decision rewritten: %v", err)
	}
	completionExec(t, db, `DROP TRIGGER reject_policy_rewrite ON steps`)
	// Rules withdrawn: the pause clears with its receipt, as an observation rather than an override.
	removePolicies(t, db, run.ProjectID)
	if err := laws.refresh(ctx, run); err != nil {
		t.Fatal(err)
	}
	wantEqual(t, "override", wantStepState(t, db, gate, StatusPass).OverrideBy, "")
	wantEventCount(t, db, 1, ofKind("law.satisfied"))
	// An automatic pass does not exempt the landing from a later amendment.
	installPolicy(t, db, run.ProjectID, "new-amendment", "requireStepBefore", map[string]any{"uses": "deploy", "requires": "tests"})
	if err := laws.refresh(ctx, run); err != nil {
		t.Fatal(err)
	}
	wantStepCount(t, db, 1, func(s *Step) bool { return s.Type == "policy/current" })
	wantStepState(t, db, gate, StatusNeedsHuman)
	wantEventCount(t, db, 1, ofKind("law.changed"))
	wantReceiptCount(t, db, 1, receiptKind("landing.paused-by-law"))
}

func TestPolicyRefreshKeepsStandingOverridesAndEarlierStageHistory(t *testing.T) {
	t.Parallel()
	t.Run("override", func(t *testing.T) {
		t.Parallel()
		db, run := policyGateFixture(t)
		gate := pausedByLaw(t, db, run)
		overrideScopeStep(t, db, workflowsql.OverrideStepParams{At: time.Now(), ID: gate, OverrideBy: "alice", OverrideReason: "shipping under the pinned rules"})
		if err := projectLaws(db).refresh(context.Background(), run); err != nil {
			t.Fatal(err)
		}
		wantEqual(t, "override", wantStepState(t, db, gate, StatusPass).OverrideBy, "alice")
		wantEventCount(t, db, 0, anyEvent)
	})
	t.Run("earlier stage", func(t *testing.T) {
		t.Parallel()
		db, run := policyGateFixture(t)
		gate := pausedByLaw(t, db, run)
		recordScopeStep(t, db, workflowsql.RecordApprovalParams{At: time.Now(), ID: gate, Status: "pass"})
		transitionScopeRun(t, db, workflowsql.TransitionRunParams{At: time.Now(), ID: run.ID, NextStage: "later"})
		run, err := ReadRunIn(context.Background(), db, run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := projectLaws(db).refresh(context.Background(), run); err != nil {
			t.Fatal(err)
		}
		wantStepCount(t, db, 1, nil)
		wantEqual(t, "stage", wantStepState(t, db, gate, StatusPass).Stage, "finish")
		wantEventCount(t, db, 0, anyEvent)
	})
}

func TestPolicyReadFailuresAndInvalidDeclarationsLeaveThePauseStanding(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"policies", "binding", "gates", "invalid declaration"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			db, run := policyGateFixture(t)
			gate := pausedByLaw(t, db, run)
			laws := projectLaws(db)
			switch failure {
			case "policies":
				dbtest.FailNextStatement(t, db, "FROM policies")
			case "binding":
				installPolicy(t, db, run.ProjectID, "scan-first", "requireCapabilityBound", map[string]any{"capability": "scanner"})
				dbtest.FailNextStatement(t, db, "FROM integrations")
			case "gates":
				dbtest.FailNextStatement(t, db, "name: StepsOfTypeForUpdate")
			case "invalid declaration":
				laws = newLaws(db, lawRulesFunc(func(context.Context, database.Queryer, ids.ID, *schema.Spec) ([]LawViolation, error) {
					return nil, wow.ErrPolicyDeclaration
				}))
			}
			err := laws.refresh(context.Background(), run)
			if err == nil {
				t.Fatalf("%s cleared the pause", failure)
			}
			if failure == "invalid declaration" && !errors.Is(err, wow.ErrPolicyDeclaration) {
				t.Fatalf("wrong declaration error: %v", err)
			}
			wantJSONField(t, "evidence", wantStepState(t, db, gate, StatusNeedsHuman).Evidence, "reason", "old")
			wantEventCount(t, db, 0, anyEvent)
			wantReceiptCount(t, db, 0, func(e audit.Entry) bool { return strings.HasPrefix(e.Kind, "landing.") })
		})
	}
}

func TestPolicyRefreshDecisionAndReceiptCommitTogether(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"write", "receipt", "suppressed write", "suppressed receipt", "commit"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			db, run := policyGateFixture(t)
			gate := pausedByLaw(t, db, run)
			removePolicies(t, db, run.ProjectID)
			switch failure {
			case "write":
				dbtest.FailNextStatement(t, db, "name: RecordStepEvaluation")
			case "receipt":
				dbtest.FailNextStatement(t, db, "INSERT INTO run_events")
			default:
				table, event, body := "steps", "UPDATE", "RETURN NULL;"
				if failure == "suppressed receipt" {
					table, event = "run_events", "INSERT"
				}
				trigger := "CREATE TRIGGER veto_policy BEFORE " + event + " ON " + table
				if failure == "commit" {
					body = "RAISE EXCEPTION 'commit refused';"
					trigger = "CREATE CONSTRAINT TRIGGER veto_policy AFTER " + event + " ON " + table + " DEFERRABLE INITIALLY DEFERRED"
				}
				completionExec(t, db, `CREATE FUNCTION veto_policy() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN `+body+` END $$`)
				completionExec(t, db, trigger+" FOR EACH ROW EXECUTE FUNCTION veto_policy()")
			}
			if err := projectLaws(db).refresh(context.Background(), run); err == nil {
				t.Fatalf("%s survived", failure)
			}
			wantStepState(t, db, gate, StatusNeedsHuman)
			wantStepCount(t, db, 0, func(s *Step) bool { return s.Status == StatusPass })
			wantEventCount(t, db, 0, anyEvent)
		})
	}
}

func TestPolicyRefreshScopeAndCancellationStopTheDecision(t *testing.T) {
	t.Parallel()
	for _, mutation := range []string{"done", "later", "revision", "definition"} {
		t.Run(mutation, func(t *testing.T) {
			t.Parallel()
			db, run := policyGateFixture(t)
			gate := pausedByLaw(t, db, run)
			removePolicies(t, db, run.ProjectID)
			switch mutation {
			case "done":
				// sql-guarantee: lockRunSnapshotIn compares the whole Run, including UpdatedAt; TransitionRun also stamps updated_at and could reject the snapshot without checking status, so change only status at this original interference point.
				completionExec(t, db, `UPDATE runs SET status='done' WHERE id=$1`, run.ID)
			case "later":
				// sql-guarantee: lockRunSnapshotIn compares the whole Run, including UpdatedAt; TransitionRun also stamps updated_at and could reject the snapshot without checking current_stage, so change only current_stage at this original interference point.
				completionExec(t, db, `UPDATE runs SET current_stage='later' WHERE id=$1`, run.ID)
			case "revision":
				// sql-guarantee: lockRunSnapshotIn compares the whole Run, including UpdatedAt; InvalidateRunDecision also stamps updated_at and could reject the snapshot without checking retry_revision, so change only retry_revision at this original interference point.
				completionExec(t, db, `UPDATE runs SET retry_revision=1 WHERE id=$1`, run.ID)
			case "definition":
				// sql-guarantee: Invalidate the law refresh's detached pinned definition without changing status, stage or revision; admission fixes definition_json and no generated run mutation rewrites it.
				completionExec(t, db, `UPDATE runs SET definition_json='{}' WHERE id=$1`, run.ID)
			}
			if err := projectLaws(db).refresh(context.Background(), run); !errors.Is(err, ErrRunMoved) {
				t.Fatalf("stale refresh: %v", err)
			}
			wantStepState(t, db, gate, StatusNeedsHuman)
			wantEventCount(t, db, 0, anyEvent)
		})
	}
	t.Run("cancelled lock", func(t *testing.T) {
		t.Parallel()
		db, run := policyGateFixture(t)
		gate := pausedByLaw(t, db, run)
		removePolicies(t, db, run.ProjectID)
		lock, err := db.Begin(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer lock.Rollback()
		holdProjectLock(t, lock, run.ProjectID)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- projectLaws(db).refresh(ctx, run) }()
		dbtest.WaitForLock(t, db, "-- name: ProjectForUpdate")
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancel: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("cancelled refresh kept waiting")
		}
		if err := lock.Rollback(); err != nil {
			t.Fatal(err)
		}
		wantStepState(t, db, gate, StatusNeedsHuman)
	})
}

func TestPolicyCapabilityLookupUsesItsTransaction(t *testing.T) {
	t.Parallel()
	db, run := policyGateFixture(t)
	gate := pausedByLaw(t, db, run)
	installPolicy(t, db, run.ProjectID, "scan-first", "requireCapabilityBound", map[string]any{"capability": "scanner"})
	db.Pool().SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := projectLaws(db).refresh(ctx, run); err != nil {
		t.Fatalf("capability evaluation: %v", err)
	}
	wantJSONField(t, "evidence", wantStepState(t, db, gate, StatusNeedsHuman).Evidence, "violations.0.policy", "scan-first")
}
