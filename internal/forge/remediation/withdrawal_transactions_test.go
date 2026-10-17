package remediation

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/ids"
)

func withdrawalFixture(t *testing.T) (*attemptScenario, Attempts, Item, *workflow.Run, ids.ID) {
	t.Helper()
	f, starts, item, def := attemptFixture(t, false)
	def.Run.Spec.Stages[0].Steps[0].With = map[string]any{"quorum": 2, "approvers": []string{"alice", "bob"}}
	run, err := starts.Start(context.Background(), item, def, "worker")
	if err != nil || run == nil {
		t.Fatalf("admission: %+v %v", run, err)
	}
	f.prepare(t, run.ID)
	item = f.item(t, item.ID)
	f.execute(t, item, workflow.StatusPass)
	snapshot, err := f.work.Snapshot(f.ctx, run.ID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	var approval ids.ID
	for _, step := range snapshot.Steps {
		if step.Type == "approval" {
			approval = step.ID
		}
	}
	if approval.IsZero() {
		t.Fatal("missing approval")
	}
	if _, err := f.work.Approve(f.ctx, workflow.Approval{StepID: approval, Decision: workflow.VoteApprove}); err != nil {
		t.Fatal(err)
	}
	store := NewReconciliation(f.db, f.work, f.ctx, nil)
	observed, err := store.Observe(context.Background(), item)
	if err != nil || observed == nil {
		t.Fatalf("observation: %+v %v", observed, err)
	}
	decision := Decide(*observed)
	if decision.State != ItemAccepting {
		t.Fatalf("acceptance decision: observation=%+v decision=%+v", observed, decision)
	}
	if applied, err := store.Apply(context.Background(), *observed, decision); err != nil || applied == nil {
		t.Fatalf("accepting: %+v %v", applied, err)
	}
	return f, starts, f.item(t, item.ID), run, approval
}
func TestRemediationWithdrawalScopeAndInvalidEvidenceCannotProduceSkip(t *testing.T) {
	t.Parallel()
	for _, mutation := range []string{
		`end run`,
		`move stage`,
		// sql-guarantee: Detach the existing run from its admitted Change; no Workflow mutation rewrites that immutable binding.
		`UPDATE runs SET change_id=NULL`,
		`wrong project`,
		// sql-guarantee: Remove only the active run binding while retaining the accepted attempt and its history; StartItem increments attempt and would introduce a separate missing-history refusal.
		`UPDATE remediation_items SET run_id=NULL`,
		// sql-guarantee: Erase the admitted approval while keeping the accepted item and run; no Workflow operation deletes only those steps.
		`DELETE FROM steps`,
		`complete quorum`,
		// sql-guarantee: Keep an already voted manual approval in impossible running state; approval commands cannot claim a manual step.
		`UPDATE steps SET status='running'`,
		// sql-guarantee: Move the same voted approval to another stage while retaining its identity and decisions; stage admission only inserts new identities.
		`UPDATE steps SET stage='earlier'`,
		// sql-guarantee: Retain the pending voted approval with override attribution; Override passes it and would test terminal-status refusal instead.
		`UPDATE steps SET override_by='alice'`,
		// sql-guarantee: Lower quorum beneath existing decisions while retaining a pending verdict; approval would settle it and retry would erase its voted evidence.
		`UPDATE steps SET config_json='{"quorum":1}'`,
		// sql-guarantee: Corrupt the quorum on the same already-voted approval; readmission cannot retain that step identity and its original decisions.
		`UPDATE steps SET config_json='{"quorum":-1}'`,
		// sql-guarantee: Replace existing approval decisions with an invalid JSON array; supported approval and withdrawal always write structured evidence.
		`UPDATE steps SET evidence_json='[]'`,
	} {
		t.Run(mutation, func(t *testing.T) {
			t.Parallel()
			f, store, item, run, approval := withdrawalFixture(t)
			db := f.db
			switch mutation {
			case "end run":
				moveRemediationRun(t, f, run.ID, workflow.RunDone, run.CurrentStage)
			case "move stage":
				moveRemediationRun(t, f, run.ID, workflow.RunRunning, "later")
			case "complete quorum":
				bob := access.WithCaller(f.ctx, access.Caller{Principal: access.Principal{Kind: "user", Name: "bob", Via: "session"}, Verified: true, Receipt: "bob"})
				if _, err := identity.New(db, identity.Config{}).EnsureUser(bob, "bob"); err != nil {
					t.Fatal(err)
				}
				if _, err := f.work.Approve(bob, workflow.Approval{StepID: approval, Decision: workflow.VoteApprove}); err != nil {
					t.Fatal(err)
				}
			case "wrong project":
				other := f.project(t, "other")
				// sql-guarantee: Rehome the same accepted run while preserving its Change and item bindings; no Workflow operation edits its immutable project identity.
				completionExec(t, db, `UPDATE runs SET project_id=?,number=number+100000`, other)
			default:
				completionExec(t, db, mutation)
			}
			before := f.snapshot(t)
			if err := store.Skip(context.Background(), item.RemediationID, item.ID, "operator", "no longer needed"); err == nil {
				t.Fatal("invalid acceptance skipped")
			}
			if after := f.snapshot(t); after != before {
				t.Fatalf("invalid withdrawal changed state\nbefore: %s\nafter:  %s", before, after)
			}
		})
	}
}

func TestRemediationWithdrawalRollsBackEveryRequiredRecord(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"read", "approval", "receipt", "attempt", "item", "audit", "suppressed approval", "suppressed receipt", "suppressed item", "commit", "second approval"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			f, store, item, run, id := withdrawalFixture(t)
			db := f.db
			before, err := workflow.ReadStepIn(context.Background(), db, id)
			if err != nil {
				t.Fatal(err)
			}
			switch failure {
			case "read":
				dbtest.FailNextStatement(t, db, "name: StepsOfTypeForUpdate")
			case "approval":
				dbtest.FailNextStatement(t, db, "name: RecordApproval")
			case "receipt":
				dbtest.FailNextStatement(t, db, "INSERT INTO run_events")
			case "attempt":
				dbtest.FailNextStatement(t, db, "-- name: FinishAttempt :execrows")
			case "item":
				dbtest.FailNextStatement(t, db, "UPDATE remediation_items SET outcome_revision = outcome_revision + 1")
			case "audit":
				dbtest.FailNextStatement(t, db, "INSERT INTO audit_log")
			case "second approval":
				enterRemediationStage(t, f, run.ID, "build", schema.Step{Name: "second", Uses: "approval", With: map[string]any{"quorum": -1}})
			default:
				table, event, body := "steps", "UPDATE", "RETURN NULL;"
				if failure == "suppressed receipt" {
					table, event = "run_events", "INSERT"
				}
				if failure == "suppressed item" {
					table = "remediation_items"
				}
				trigger := "CREATE TRIGGER veto_withdrawal BEFORE " + event + " ON " + table
				if failure == "commit" {
					body = "RAISE EXCEPTION 'commit refused';"
					trigger = "CREATE CONSTRAINT TRIGGER veto_withdrawal AFTER UPDATE ON steps DEFERRABLE INITIALLY DEFERRED"
				}
				completionExec(t, db, `CREATE FUNCTION veto_withdrawal() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN `+body+` END $$`)
				completionExec(t, db, trigger+" FOR EACH ROW EXECUTE FUNCTION veto_withdrawal()")
			}
			unchanged := f.snapshot(t)
			if err := store.Skip(context.Background(), item.RemediationID, item.ID, "operator", "not needed"); err == nil {
				t.Fatalf("%s skipped despite failure", failure)
			}
			after, err := workflow.ReadStepIn(context.Background(), db, id)
			if err != nil || !workflow.SameStep(before, after) {
				t.Fatalf("approval survived %s: %+v %v", failure, after, err)
			}
			if after := f.snapshot(t); after != unchanged {
				t.Fatalf("withdrawal failure %s changed state\nbefore: %s\nafter:  %s", failure, unchanged, after)
			}
		})
	}
}

func TestRemediationWithdrawalAndQuorumVoteHaveOneWinner(t *testing.T) {
	t.Parallel()
	f, store, item, run, id := withdrawalFixture(t)
	db := f.db
	// This cross-domain race isolates the two transaction owners from runtime scheduling.
	work := workflow.New(context.Background(), db, workflow.Dependencies{Authority: reconcileActor{}})
	work.Stop()
	bob := access.WithCaller(context.Background(), access.Caller{Principal: access.Principal{Kind: "user", Name: "bob", Via: "session"}, Verified: true, Receipt: "bob"})
	outcomes := race(2, func(i int) int {
		if i == 0 {
			if err := store.Skip(context.Background(), item.RemediationID, item.ID, "operator", "not needed"); err == nil {
				return 1
			}
			return 0
		}
		if _, err := work.Approve(bob, workflow.Approval{StepID: id, Decision: workflow.VoteApprove}); err == nil {
			return 1
		}
		return 0
	})
	if withdrawalWinners(outcomes, 1) != 1 {
		t.Fatal(outcomes)
	}
	// The fixture admits one run, so its trace holds every approval event.
	step := f.step(t, id)
	if outcomes[0] == 1 {
		if step.Status != workflow.StatusFail || jsonText(t, step.Evidence, "withdrawal", "by") != "operator" {
			t.Fatalf("withdrawn approval: %+v", step)
		}
		if n := f.eventCount(t, run.ID, "approval"); n != 1 {
			t.Fatalf("approval events: %d", n)
		}
	} else {
		if step.Status != workflow.StatusPass || jsonText(t, step.Evidence, "decision") != "approve" {
			t.Fatalf("approved step: %+v", step)
		}
		if current := f.item(t, item.ID); current.State != ItemAccepting || current.OutcomeRevision != item.OutcomeRevision {
			t.Fatalf("item after approval won: %+v", current)
		}
		if n := f.eventCount(t, run.ID, "approval"); n != 2 {
			t.Fatalf("approval events: %d", n)
		}
		if n := f.eventCount(t, run.ID, "approval.withdrawn"); n != 0 {
			t.Fatalf("withdrawal events: %d", n)
		}
	}
}

func TestRemediationConcurrentWithdrawalHasOneReceipt(t *testing.T) {
	t.Parallel()
	f, store, item, run, _ := withdrawalFixture(t)
	db := f.db
	outcomes := race(8, func(int) int {
		if err := store.Skip(context.Background(), item.RemediationID, item.ID, "operator", "not needed"); err == nil {
			return 1
		}
		return 0
	})
	if withdrawalWinners(outcomes, 1) != 1 {
		t.Fatal(outcomes)
	}
	if n := f.eventCount(t, run.ID, "approval.withdrawn"); n != 1 {
		t.Fatalf("withdrawal events: %d", n)
	}
	receiptCount(t, db, 1, "remediation.item.skip")
}

func TestRemediationSkipWithdrawsOnlyCurrentAcceptance(t *testing.T) {
	t.Parallel()
	f, store, item, run, id := withdrawalFixture(t)
	db := f.db
	old := enterRemediationStage(t, f, run.ID, "earlier", schema.Step{Name: "old-accept", Uses: "approval"})["old-accept"]
	passed := enterRemediationStage(t, f, run.ID, "finish", schema.Step{Name: "security", Uses: "approval"})["security"]
	moveRemediationRun(t, f, run.ID, workflow.RunRunning, "finish")
	if _, err := f.work.Override(f.ctx, workflow.Override{StepID: passed, Reason: "historical security exception"}); err != nil {
		t.Fatal(err)
	}
	moveRemediationRun(t, f, run.ID, workflow.RunRunning, "build")
	if err := store.Skip(context.Background(), item.RemediationID, item.ID, "operator", "  not needed  "); err != nil {
		t.Fatal(err)
	}
	withdrawn := f.step(t, id)
	var fields map[string]json.RawMessage
	var decisions []json.RawMessage
	if err := json.Unmarshal(withdrawn.Evidence, &fields); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(fields["decisions"], &decisions); err != nil {
		t.Fatalf("decisions is not an array: %s", withdrawn.Evidence)
	}
	if withdrawn.Status != workflow.StatusFail || jsonText(t, withdrawn.Evidence, "withdrawal", "by") != "operator" || jsonText(t, withdrawn.Evidence, "withdrawal", "reason") != "not needed" || len(decisions) != 1 {
		t.Fatalf("withdrawn approval: %+v %s", withdrawn, withdrawn.Evidence)
	}
	if s := f.step(t, old); s.Status != workflow.StatusPending || string(s.Evidence) != "{}" {
		t.Fatalf("earlier stage approval: %+v %s", s, s.Evidence)
	}
	if s := f.step(t, passed); s.Status != workflow.StatusPass || string(s.Evidence) != "{}" {
		t.Fatalf("passed approval: %+v %s", s, s.Evidence)
	}
	if current := f.item(t, item.ID); current.State != ItemSkipped || current.OutcomeRevision != item.OutcomeRevision+1 || current.LeaseWorker != "" || current.LeaseUntil.Valid {
		t.Fatalf("skipped item: %+v", current)
	}
	if n := attemptsWith(t, db, func(a Attempt) bool { return a.ItemID == item.ID && a.Outcome == "skipped" && a.FinishedAt.Valid }); n != 1 {
		t.Fatalf("skipped attempts: %d", n)
	}
	// The fixture admits one run, so its trace holds every event.
	matching := 0
	for _, e := range f.trace(t, run.ID).Events {
		if e.Kind == "approval.withdrawn" && jsonText(t, e.Payload, "stepId") == fmt.Sprint(id) && jsonText(t, e.Payload, "step") == "accept" && jsonText(t, e.Payload, "stage") == "build" && jsonText(t, e.Payload, "by") == "operator" {
			matching++
		}
	}
	if matching != 1 {
		t.Fatalf("withdrawal events: %d", matching)
	}
	if n := f.eventCount(t, run.ID, "step.rejected"); n != 0 {
		t.Fatalf("rejection events: %d", n)
	}
	receiptCount(t, db, 1, "remediation.item.skip")
	if status := f.runStatus(t, run.ID); status != workflow.RunRunning {
		t.Fatalf("run after skip: %s", status)
	}
}

func TestApprovalWithdrawalUsesTheOwningTransaction(t *testing.T) {
	t.Parallel()
	f, _, _, run, id := withdrawalFixture(t)
	db := f.db
	db.Pool().SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := db.InTx(ctx, func(tx *database.Tx) error {
		return workflow.WithdrawApprovalsIn(ctx, tx, workflow.WithdrawApprovals{ProjectID: run.ProjectID, ChangeID: run.ChangeID, RunID: run.ID, Withdrawal: workflow.ApprovalWithdrawal{By: "operator", Reason: "not needed", At: "now"}}, time.Now())
	})
	if err != nil {
		t.Fatal(err)
	}
	if s := f.step(t, id); s.Status != workflow.StatusFail {
		t.Fatalf("withdrawn in the owning transaction: %+v", s)
	}
}

func TestRemediationSkipRequiresItsItemWriteWithoutAcceptance(t *testing.T) {
	t.Parallel()
	for _, state := range []string{ItemQueued, ItemStuck} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			var f *attemptScenario
			var store Attempts
			var item Item
			if state == ItemQueued {
				f, store, item, _ = attemptFixture(t, false)
			} else {
				f, _, item, _ = outcomeFixture(t)
				f.execute(t, item, workflow.StatusFail)
				item = f.outcome(t, item, Outcome{Kind: ItemStuck, Reason: "failed"})
				store = NewAttempts(f.db, f.work, f.ctx, nil)
			}
			db := f.db
			before := f.snapshot(t)
			completionExec(t, db, `CREATE FUNCTION veto_skip() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END $$`)
			completionExec(t, db, `CREATE TRIGGER veto_skip BEFORE UPDATE ON remediation_items FOR EACH ROW EXECUTE FUNCTION veto_skip()`)
			if err := store.Skip(context.Background(), item.RemediationID, item.ID, "operator", "not needed"); err == nil {
				t.Fatal("suppressed item write reported a skip")
			}
			oneItem(t, db, item.ID, func(i Item) bool { return i.State == state })
			if after := f.snapshot(t); after != before {
				t.Fatalf("suppressed item write changed state\nbefore: %s\nafter:  %s", before, after)
			}
		})
	}
}

func withdrawalWinners(values []int, want int) int {
	n := 0
	for _, v := range values {
		if v == want {
			n++
		}
	}
	return n
}
