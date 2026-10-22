package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
)

// evalCopyForTest records projectID as a copy made for evalID. The copy
// record belongs to the evals owner, which imports Work, so Work's own
// tests cannot reach its operation.
func evalCopyForTest(t *testing.T, db *database.DB, projectID ids.ID, evalID ids.ID) {
	t.Helper()
	// sql-guarantee: the evals owner writes eval_copies and imports workflow; this package's tests cannot call it without an import cycle.
	if _, err := db.SQL().ExecContext(context.Background(), `INSERT INTO eval_copies (project_id, eval_id, arm_id, arm) VALUES ($1, $2, '00000000-0001-7000-8000-000000000000', 'baseline')`, projectID, evalID); err != nil {
		t.Fatal(err)
	}
}

// evalRunForTest stores a running root run in projectID admitted with
// authority, at a stage holding a passed step and a waiting approval.
func evalRunForTest(t *testing.T, db *database.DB, projectID, parent ids.ID, authority string) *Run {
	t.Helper()
	ch, err := NewCreator(db, nil).Create(context.Background(), ChangeDraft{ProjectID: projectID, Title: "work"}, "creator")
	if err != nil {
		t.Fatal(err)
	}
	run := insertStoredRun(t, db, workflowsql.InsertRunParams{ID: ids.New(time.Now()),
		ProjectID: projectID, ChangeID: ch.ID, ParentRunID: parent, WorkflowName: "quick-change", WorkflowVersion: 1,
		Kind: "pipeline", Status: RunRunning.String(), CurrentStage: "review",
		DefinitionJson: []byte(`{"kind":"pipeline","stages":[{"name":"review"}]}`),
	})
	insertStoredStep(t, db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "review", Name: "review-change", Type: "script", Status: StatusPass.String()})
	insertStoredStep(t, db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "review", Name: "accept", Type: "approval",
		ConfigJson: []byte(`{"quorum":1,"means":"I have looked at it."}`), Status: StatusPending.String()})
	if authority != "" {
		if err := db.InTx(context.Background(), func(tx *database.Tx) error {
			return markLandingAuthorityIn(context.Background(), tx, run.ID, authority)
		}); err != nil {
			t.Fatal(err)
		}
	}
	return run
}

// An eval's authority is a fact for a merge or deploy only where the
// effect's project is a copy of that same eval: in a real project, in a copy
// of another eval, or in a project a child run reaches from a copy, it is no
// authority at all. A person's landing authority is unchanged.
func TestAnEvalsAuthorityHoldsOnlyInACopyOfThatEval(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	ctx := context.Background()
	copy7 := changeTestProject(t, db, "copy-7")
	copy8 := changeTestProject(t, db, "copy-8")
	real := changeTestProject(t, db, "real")
	evalCopyForTest(t, db, copy7, idstest.N(7))
	evalCopyForTest(t, db, copy8, idstest.N(8))
	eval7 := evalAuthorityOf(idstest.N(7), "bls")

	for _, c := range []struct {
		name    string
		project ids.ID
		allowed bool
	}{{"its own copy", copy7, true}, {"another eval's copy", copy8, false}, {"a real project", real, false}} {
		run := evalRunForTest(t, db, c.project, ids.Nil, eval7)
		err := assentEvidenceIn(ctx, db, run.ID, c.project)
		if c.allowed != (err == nil) || (!c.allowed && !errors.Is(err, errNoAssent)) {
			t.Fatalf("%s: %v", c.name, err)
		}
	}
	// Inherited from the run that called it: judged at the effect's project.
	root := evalRunForTest(t, db, copy7, ids.Nil, eval7)
	child := evalRunForTest(t, db, copy7, root.ID, "")
	if err := assentEvidenceIn(ctx, db, child.ID, copy7); err != nil {
		t.Fatalf("a child in the eval's copy: %v", err)
	}
	if err := assentEvidenceIn(ctx, db, child.ID, real); !errors.Is(err, errNoAssent) {
		t.Fatalf("an effect outside the eval's copies: %v", err)
	}
	// A person's authority is no eval's and holds as before.
	person := evalRunForTest(t, db, real, ids.Nil, "lander")
	if err := assentEvidenceIn(ctx, db, person.ID, real); err != nil {
		t.Fatalf("a person's landing authority: %v", err)
	}
}

// In an eval's copy, an approval waiting once its stage's other steps have
// answered is settled by the eval's start: passed, shown as eval-authorised
// with the stated meaning, with no approver and no vote, so it is not a
// person's approval and is not carried as acceptance. The same authority in
// a real project settles nothing.
func TestAnEvalsApprovalIsSettledButIsNoPersonsApproval(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	ctx := context.Background()
	copy7 := changeTestProject(t, db, "copy-7")
	real := changeTestProject(t, db, "real")
	evalCopyForTest(t, db, copy7, idstest.N(7))
	authority := evalAuthorityOf(idstest.N(7), "bls")
	s := newLandings(db, nil)

	run := evalRunForTest(t, db, copy7, ids.Nil, authority)
	settled, err := s.carryOrSettleForEval(ctx, run)
	if err != nil || !settled {
		t.Fatalf("settled in the eval's copy: %v %v", settled, err)
	}
	steps, err := readSteps(ctx, db, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var accept *Step
	for _, step := range steps {
		if step.Name == "accept" {
			accept = step
		}
	}
	evidence, err := ReadApprovalEvidence(accept.Evidence)
	if err != nil || accept.Status != StatusPass || evidence.EvalAuthorised != authority || evidence.Approver != "" || len(evidence.Decisions) != 0 || evidence.Means != "I have looked at it." {
		t.Fatalf("eval-authorised approval: %+v %+v %v", accept, evidence, err)
	}
	if approvedByAPerson(accept) {
		t.Fatal("an eval-authorised approval is not a person's")
	}
	if err := assentEvidenceIn(ctx, db, run.ID, copy7); err != nil {
		t.Fatalf("the eval's own authority still holds: %v", err)
	}
	accepted, err := database.Transact(ctx, db, func(tx *database.Tx) (acceptance, error) { return acceptanceIn(ctx, tx, run.ID) })
	if err != nil || isPerson(accepted.Accepter) {
		t.Fatalf("an eval-authorised acceptance names no accepter to carry: %+v %v", accepted, err)
	}

	outside := evalRunForTest(t, db, real, ids.Nil, authority)
	if settled, err := s.carryOrSettleForEval(ctx, outside); err != nil || settled {
		t.Fatalf("outside the eval's copies nothing is settled: %v %v", settled, err)
	}
}

// In a checked landing an eval's settled approval is assent to the pinned
// candidate, as a person's vote is: it names that commit, so the merge's own
// check of what every approval assented to passes.
func TestAnEvalsSettledApprovalNamesTheLandingsCandidate(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	ctx := context.Background()
	copy7 := changeTestProject(t, db, "copy-7")
	evalCopyForTest(t, db, copy7, idstest.N(7))
	run := evalRunForTest(t, db, copy7, ids.Nil, evalAuthorityOf(idstest.N(7), "bls"))
	const candidate = "0c54822890d8aaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	// The start events Work's admission records for a pinned landing; this
	// fixture stores its run directly, below admission.
	if err := db.InTx(ctx, func(tx *database.Tx) error {
		if err := recordReceiptIn(ctx, tx, run.ID, landingPinRequiredEvent, struct{}{}, time.Now()); err != nil {
			return err
		}
		return recordReceiptIn(ctx, tx, run.ID, landingCandidateEvent, LandingCandidate{Candidate: candidate, Target: "1111111111111111111111111111111111111111", By: "catch-up"}, time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	steps, err := readSteps(ctx, db, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range steps {
		if step.Name == "review-change" {
			// The stage's check passed on the candidate, as a landing gate records.
			recordScopeStep(t, db, workflowsql.RecordApprovalParams{At: time.Now(), ID: step.ID, Status: StatusPass.String(), EvidenceJson: json.RawMessage(`{"candidate":"` + candidate + `"}`)})
		}
	}
	if settled, err := newLandings(db, nil).carryOrSettleForEval(ctx, run); err != nil || !settled {
		t.Fatalf("settled: %v %v", settled, err)
	}
	if steps, err = readSteps(ctx, db, run.ID); err != nil {
		t.Fatal(err)
	}
	for _, step := range steps {
		if step.Name != "accept" {
			continue
		}
		evidence, err := ReadApprovalEvidence(step.Evidence)
		if err != nil || evidence.Candidate != candidate {
			t.Fatalf("the settled approval names %q, want the landing's candidate: %v", evidence.Candidate, err)
		}
	}
	c, _, err := LandingCandidateIn(ctx, db, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if refusal, err := landingCandidateRefusal(ctx, db, run.ID, c); err != nil || refusal != "" {
		t.Fatalf("the merge's check refuses: %q %v", refusal, err)
	}
}

// A landing of accepted work in an eval's copy carries the eval's authority
// its work run was admitted with; accepted work anywhere else carries none.
func TestALandingInAnEvalsCopyCarriesTheEvalsAuthority(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	ctx := context.Background()
	copy7 := changeTestProject(t, db, "copy-7")
	real := changeTestProject(t, db, "real")
	evalCopyForTest(t, db, copy7, idstest.N(7))
	authority := evalAuthorityOf(idstest.N(7), "bls")
	work := evalRunForTest(t, db, copy7, ids.Nil, authority)
	if got, err := acceptedEvalAuthorityIn(ctx, db, StartRequest{ProjectID: copy7, AcceptedRunID: work.ID}); err != nil || got != authority {
		t.Fatalf("in the copy: %q %v", got, err)
	}
	elsewhere := evalRunForTest(t, db, real, ids.Nil, authority)
	if got, err := acceptedEvalAuthorityIn(ctx, db, StartRequest{ProjectID: real, AcceptedRunID: elsewhere.ID}); err != nil || got != "" {
		t.Fatalf("outside the copies: %q %v", got, err)
	}
	person := evalRunForTest(t, db, copy7, ids.Nil, "lander")
	if got, err := acceptedEvalAuthorityIn(ctx, db, StartRequest{ProjectID: copy7, AcceptedRunID: person.ID}); err != nil || got != "" {
		t.Fatalf("a person's authority is not carried this way: %q %v", got, err)
	}
}
