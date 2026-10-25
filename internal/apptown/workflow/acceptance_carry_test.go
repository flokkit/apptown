package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func denyStep(name string) approvalEligibility {
	return func(_ context.Context, _ *Run, step *Step, who access.Actor) error {
		if step.Name == name {
			return &DecisionDenied{Code: access.DenyNotEligible, Reason: who.Name + " is not an approver for this step (bob)"}
		}
		return nil
	}
}

// stepNamed reads a run's step through the owner's own read.
func stepNamed(t *testing.T, store landings, runID ids.ID, name string) *Step {
	t.Helper()
	steps, err := readSteps(context.Background(), store.db, runID)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range steps {
		if step.Name == name {
			return step
		}
	}
	t.Fatalf("run %d has no step %s", runID, name)
	return nil
}

// receipts reads a run's receipts of one kind, as their payloads.
func receipts(t *testing.T, store landings, runID ids.ID, kind string) []map[string]string {
	t.Helper()
	events, err := workflowsql.New(store.db.SQL()).ReadRunEvents(context.Background(), workflowsql.ReadRunEventsParams{RunID: runID, EventLimit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]string
	for _, e := range events {
		if e.Kind != kind {
			continue
		}
		payload := map[string]any{}
		if err := json.Unmarshal(e.PayloadJson, &payload); err != nil {
			t.Fatal(err)
		}
		flat := map[string]string{}
		for k, v := range payload {
			flat[k] = fmt.Sprint(v)
		}
		out = append(out, flat)
	}
	return out
}

func carryingAdvancer(store landings) *advancer {
	return newAdvancer(store.db, advanceRuntime{
		Stages:     NewStages(nil, func(string) bool { return false }),
		Checks:     advancementScopeChecks{refresh: func(context.Context, *Run) error { return nil }},
		Completion: completionRecorderFunc(noCompletionEffects),
		Carry:      store.carry,
	})
}

// Acceptance counts for a landing approval only where the accepter could
// have approved that step themselves. Before 28 September the yes was cast
// on every first-stage approval without asking.
func TestAcceptanceCountsOnlyWhereTheAccepterCouldApprove(t *testing.T) {
	t.Parallel()
	store, work, landing, _ := landingAcceptanceFixture(t)
	store.eligible = denyStep("second")
	if _, err := store.apply(context.Background(), work.ID, landing.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.carry(context.Background(), landing); err != nil {
		t.Fatal(err)
	}
	if first := stepNamed(t, store, landing.ID, "first"); first.Status != StatusPass {
		t.Fatalf("the eligible approval must carry: %s", first.Status)
	}
	if second := stepNamed(t, store, landing.ID, "second"); second.Status != StatusPending || string(second.Evidence) != "{}" {
		t.Fatalf("the ineligible approval must wait untouched: %s %s", second.Status, second.Evidence)
	}
	carried := receipts(t, store, landing.ID, "landing.acceptance.carried")
	withheld := receipts(t, store, landing.ID, "landing.acceptance.withheld")
	if len(carried) != 1 || carried[0]["step"] != "first" || len(withheld) != 1 || withheld[0]["step"] != "second" || !strings.Contains(withheld[0]["reason"], "(bob)") {
		t.Fatalf("receipts: carried %v, withheld %v", carried, withheld)
	}
}

// A landing whose approval has its own stage after the gates, as stock land
// has since 18 September, still receives the acceptance when that stage
// opens, and moves on without anyone else acting.
func TestAcceptanceCarriesIntoALaterApprovalStage(t *testing.T) {
	t.Parallel()
	store, work, landing, _ := landingAcceptanceFixture(t)
	storedReadyStart(t, store.db, landing.ID)
	if _, err := store.apply(context.Background(), work.ID, landing.ID); err != nil {
		t.Fatal(err)
	}
	res, err := carryingAdvancer(store).Advance(context.Background(), landing.ID, "system")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Advanced || res.RunStatus != RunDone {
		t.Fatalf("the carried approval must let the landing finish: %+v", res)
	}
	review := stepNamed(t, store, landing.ID, "review")
	if evidence, err := ReadApprovalEvidence(review.Evidence); err != nil || review.Status != StatusPass || evidence.Approver != "alice" {
		t.Fatalf("the later approval must carry alice's yes: %s %s %v", review.Status, review.Evidence, err)
	}
	if carried := receipts(t, store, landing.ID, "landing.acceptance.carried"); len(carried) != 3 || carried[2]["step"] != "review" {
		t.Fatalf("carried receipts: %v", carried)
	}
}

// Where the accepter may not approve the later stage, it waits for a person
// and the record says why.
func TestAcceptanceWithheldFromALaterStageLeavesItWaiting(t *testing.T) {
	t.Parallel()
	store, work, landing, _ := landingAcceptanceFixture(t)
	storedReadyStart(t, store.db, landing.ID)
	store.eligible = denyStep("review")
	if _, err := store.apply(context.Background(), work.ID, landing.ID); err != nil {
		t.Fatal(err)
	}
	res, err := carryingAdvancer(store).Advance(context.Background(), landing.ID, "system")
	if err != nil {
		t.Fatal(err)
	}
	if res.RunStatus != RunRunning || res.Stage != "later" {
		t.Fatalf("the landing must wait at its approval stage: %+v", res)
	}
	if review := stepNamed(t, store, landing.ID, "review"); review.Status != StatusPending {
		t.Fatalf("the later approval must wait: %s", review.Status)
	}
	if withheld := receipts(t, store, landing.ID, "landing.acceptance.withheld"); len(withheld) != 1 || withheld[0]["step"] != "review" {
		t.Fatalf("withheld receipts: %v", withheld)
	}
}

// The eligibility the carry uses is a manual approval's own check, asked
// about the accepter: named approvers with the approve role on this project,
// administrators, and nobody else.
func TestAcceptanceEligibilityIsTheManualApprovalCheck(t *testing.T) {
	t.Parallel()
	a := stepAuthorityFixture(t, ApprovalConfig{Approvers: []string{"owner", "outsider"}})
	ctx := context.Background()
	run, err := readRun(ctx, a.DB, a.RunID)
	if err != nil {
		t.Fatal(err)
	}
	step, err := ReadStepIn(ctx, a.DB, a.StepID)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		person string
		ok     bool
	}{
		{"owner", true},
		{"owner via console/c", true},
		{"boss", true},            // an administrator may approve any step
		{"outsider", false},       // named, but holds the approve role only elsewhere
		{"stranger", false},       // not named and no role
		{"apptown-system", false}, // nobody accepted
	} {
		who := access.Actor{Name: tc.person}
		base, _, _ := strings.Cut(tc.person, " via ")
		if found, err := identity.ActorIn(ctx, a.DB, base); err == nil {
			who.Principal = found.Principal
		}
		err := a.Work.landings.eligible(ctx, run, step, who)
		var denied *DecisionDenied
		if tc.ok && err != nil || !tc.ok && !errors.As(err, &denied) {
			t.Errorf("%s: want eligible=%v, got %v", tc.person, tc.ok, err)
		}
	}
	owner, err := identity.ActorIn(ctx, a.DB, "owner")
	if err != nil {
		t.Fatal(err)
	}
	// The owner's yes given through a chat is not theirs to carry, and a yes
	// naming owner but no principal (one the cutover could not place) is no
	// one's: the name alone could now be someone else's.
	for _, who := range []access.Actor{{Principal: owner.Principal, Name: "owner", Via: "chat:c"}, {Name: "owner"}} {
		var denied *DecisionDenied
		if err := a.Work.landings.eligible(ctx, run, step, who); !errors.As(err, &denied) {
			t.Errorf("%+v was carried: %v", who, err)
		}
	}
}

// A yes counts only for a sentence the person read when they gave it: an
// acceptance that showed a different landing sentence carries nothing.
func TestAcceptanceCountsOnlyForTheSentenceItShowed(t *testing.T) {
	t.Parallel()
	store, work, landing, _ := landingAcceptanceFixtureShowing(t, "an older sentence")
	if _, err := store.apply(context.Background(), work.ID, landing.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.carry(context.Background(), landing); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"first", "second"} {
		if step := stepNamed(t, store, landing.ID, name); step.Status != StatusPending {
			t.Fatalf("%s must wait: %s", name, step.Status)
		}
	}
	withheld := receipts(t, store, landing.ID, "landing.acceptance.withheld")
	if len(withheld) != 2 || !strings.HasPrefix(withheld[0]["reason"], "the acceptance did not show") {
		t.Fatalf("withheld receipts: %v", withheld)
	}
}

type landingMeansFunc func(context.Context, ids.ID) ([]string, error)

func (f landingMeansFunc) LandingMeans(ctx context.Context, projectID ids.ID) ([]string, error) {
	return f(ctx, projectID)
}

// Approving a change's acceptance records what its landing would ask, and
// the change view shows it while the acceptance waits. Other approvals, such
// as a plan's, record nothing.
func TestAcceptanceShowsAndRecordsWhatTheLandingAsks(t *testing.T) {
	t.Parallel()
	with := map[string]any{"approvers": []string{"owner"}, "means": "I read it"}
	spec := schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{
		{Name: "plan", Steps: []schema.Step{{Name: "approve-plan", Uses: "approval", With: with}}},
		{Name: "review", Steps: []schema.Step{{Name: "accept", Uses: "approval", With: with}}},
	}}
	a := stepAuthoritySpecFixture(t, spec)
	a.Work.means = landingMeansFunc(func(context.Context, ids.ID) ([]string, error) { return []string{landingMeans}, nil })
	ctx := stepVerifiedContext(context.Background(), access.Principal{Kind: "user", Name: "owner", Via: "token"})

	shown := func() []string {
		snapshot, err := a.Work.Snapshot(ctx, a.RunID, DetailView)
		if err != nil {
			t.Fatal(err)
		}
		return snapshot.LandingMeans
	}
	if got := shown(); len(got) != 0 {
		t.Fatalf("a plan approval must not show the landing: %q", got)
	}
	plan, err := a.Work.Approve(ctx, Approval{StepID: a.StepID, Decision: VoteApprove})
	if err != nil {
		t.Fatal(err)
	}
	if evidence, _ := ReadApprovalEvidence(plan.Evidence); len(evidence.Decisions) != 1 || evidence.Decisions[0].AlsoApproves != nil {
		t.Fatalf("a plan approval must record no landing sentence: %s", plan.Evidence)
	}
	run, err := readRun(context.Background(), a.DB, a.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (advanceStore{db: a.DB, stages: a.Work.stages}).transition(context.Background(), run, "review", "test"); err != nil {
		t.Fatal(err)
	}
	if got := shown(); len(got) != 1 || got[0] != landingMeans {
		t.Fatalf("the waiting acceptance must show the landing: %q", got)
	}
	acceptID := admittedStepID(t, queries{db: a.DB}, &Run{ID: a.RunID, CurrentStage: "review"}, "accept")
	if q, err := a.Work.ApprovalQuestion(ctx, acceptID); err != nil || q.Means != "I read it" || len(q.LandingMeans) != 1 || q.LandingMeans[0] != landingMeans {
		t.Fatalf("the question a client shows: %+v %v", q, err)
	}
	if q, err := a.Work.ApprovalQuestion(ctx, a.StepID); err != nil || len(q.LandingMeans) != 0 {
		t.Fatalf("a decided plan approval asks nothing of the landing: %+v %v", q, err)
	}
	accepted, err := a.Work.Approve(ctx, Approval{StepID: acceptID, Decision: VoteApprove, Shown: []string{landingMeans}})
	if err != nil {
		t.Fatal(err)
	}
	if evidence, _ := ReadApprovalEvidence(accepted.Evidence); len(evidence.Decisions) != 1 || len(evidence.Decisions[0].AlsoApproves) != 1 || evidence.Decisions[0].AlsoApproves[0] != landingMeans {
		t.Fatalf("the acceptance must record what it showed: %s", accepted.Evidence)
	}
}

// News is what this run's checks said that nobody knew at acceptance; what is
// true of every landing is not news, or every landing would ask twice.
func TestCheckNewsIsWhatTheRunSaid(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		step Step
		news bool
	}{
		{"clean pass", Step{Name: "tests", Type: "tests", Status: StatusPass, Evidence: json.RawMessage(`{"reason":"all passed"}`)}, false},
		{"no scanner attached", Step{Name: "scan", Type: "scan", Status: StatusPass, Evidence: json.RawMessage(`{"note":"no scanner attached for source"}`)}, false},
		{"AppTown could not run a runtime check", Step{Name: "guidance", Type: "guidance/conformance", Status: StatusPass, Evidence: json.RawMessage(`{"checks":[{"check":"readiness-redirect-safe","verdict":"cannot-assess","apptownFault":true},{"check":"contract","verdict":"skipped"}]}`)}, false},
		{"an approval is never news", Step{Name: "approve", Type: "approval", Status: StatusFail}, false},
		{"overridden", Step{Name: "tests", Type: "tests", Status: StatusPass, OverrideBy: "bls"}, true},
		{"did not pass", Step{Name: "scan", Type: "scan", Status: StatusNeedsHuman}, true},
		{"findings already failing on main", Step{Name: "guidance", Type: "guidance/conformance", Status: StatusPass, Evidence: json.RawMessage(`{"undecided":["a finding"],"preexisting":["another"]}`)}, false},
		{"check errors", Step{Name: "guidance", Type: "guidance/conformance", Status: StatusPass, Evidence: json.RawMessage(`{"checkErrors":1}`)}, true},
		{"could not judge the change", Step{Name: "guidance", Type: "guidance/conformance", Status: StatusPass, Evidence: json.RawMessage(`{"checks":[{"check":"health","verdict":"cannot-assess"}]}`)}, true},
		{"unreadable check evidence", Step{Name: "guidance", Type: "guidance/conformance", Status: StatusPass, Evidence: json.RawMessage(`{"checks":[{"check":"health","verdict":"maybe"}]}`)}, true},
	} {
		step := tc.step
		if got := checkNews([]*Step{&step}); (len(got) > 0) != tc.news {
			t.Errorf("%s: news %q, want news=%v", tc.name, got, tc.news)
		}
	}
}

// The acceptance is judged once the stage's other steps have answered, and
// is withheld when they raised something.
func TestAcceptanceWaitsForTheStageChecksAndTheirNews(t *testing.T) {
	t.Parallel()
	store, work, landing, _ := landingAcceptanceFixture(t)
	check := insertStoredStep(t, store.db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: landing.ID, Stage: "start", Name: "guidance", Type: "guidance/conformance", Status: StatusRunning.String()})
	if _, err := store.apply(context.Background(), work.ID, landing.ID); err != nil {
		t.Fatal(err)
	}
	if cast, err := store.carry(context.Background(), landing); err != nil || cast {
		t.Fatalf("judged before the checks answered: %v %v", cast, err)
	}
	if got := receipts(t, store, landing.ID, "landing.acceptance.withheld"); len(got) != 0 {
		t.Fatalf("judged before the checks answered: %v", got)
	}
	if changed, err := workflowsql.New(store.db.SQL()).RecordApproval(context.Background(), workflowsql.RecordApprovalParams{At: time.Now(), ID: check, ExpectedStatus: StatusRunning.String(), Status: StatusPass.String(), EvidenceJson: json.RawMessage(`{"checkErrors":1}`)}); err != nil || changed != 1 {
		t.Fatalf("settle the check: %d %v", changed, err)
	}
	if cast, err := store.carry(context.Background(), landing); err != nil || cast {
		t.Fatalf("carried past news: %v %v", cast, err)
	}
	withheld := receipts(t, store, landing.ID, "landing.acceptance.withheld")
	if len(withheld) != 2 || !strings.Contains(withheld[0]["reason"], "guidance had 1 check error(s)") {
		t.Fatalf("withheld receipts: %v", withheld)
	}
	// Judged once: a later advance does not judge the same step again.
	if _, err := store.carry(context.Background(), landing); err != nil {
		t.Fatal(err)
	}
	if again := receipts(t, store, landing.ID, "landing.acceptance.withheld"); len(again) != 2 {
		t.Fatalf("judged twice: %v", again)
	}
}

// Advances race: every one may try to carry, and each approval still gets
// one vote and one receipt.
func TestConcurrentCarriesCastOneVotePerApproval(t *testing.T) {
	t.Parallel()
	store, work, landing, _ := landingAcceptanceFixture(t)
	if _, err := store.apply(context.Background(), work.ID, landing.ID); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := store.carry(context.Background(), landing); err != nil && !errors.Is(err, ErrRunMoved) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if carried := receipts(t, store, landing.ID, "landing.acceptance.carried"); len(carried) != 2 {
		t.Fatalf("carried receipts: %v", carried)
	}
	if approvals := receipts(t, store, landing.ID, "approval"); len(approvals) != 2 {
		t.Fatalf("votes: %v", approvals)
	}
}

// A check overridden while the acceptance was being judged is seen under the
// vote's locks: the acceptance is withheld, not carried on the earlier,
// clean reading.
func TestAcceptanceSeesAnOverrideMadeWhileItWasJudged(t *testing.T) {
	t.Parallel()
	store, work, landing, _ := landingAcceptanceFixture(t)
	ctx := context.Background()
	check := insertStoredStep(t, store.db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: landing.ID, Stage: "start", Name: "check", Type: "tests", Status: StatusSkipped.String()})
	if _, err := store.apply(ctx, work.ID, landing.ID); err != nil {
		t.Fatal(err)
	}
	overridden := false
	store.eligible = func(ctx context.Context, _ *Run, _ *Step, _ access.Actor) error {
		if !overridden {
			overridden = true
			decisions := stepDecisionStore{store.db, clocked{}}
			snapshot, err := decisions.prepare(ctx, check)
			if err != nil {
				return err
			}
			if _, err := decisions.override(ctx, snapshot, "bob", "overridden while the acceptance was judged"); err != nil {
				return err
			}
		}
		return nil
	}
	if _, err := store.carry(ctx, landing); err != nil {
		t.Fatal(err)
	}
	if first := stepNamed(t, store, landing.ID, "first"); first.Status == StatusPass {
		t.Fatal("the acceptance carried past an override made while it was judged")
	}
	withheld := receipts(t, store, landing.ID, "landing.acceptance.withheld")
	if len(withheld) != 2 || !strings.Contains(withheld[0]["reason"], "check was overridden by bob") {
		t.Fatalf("withheld receipts: %v", withheld)
	}
}

// An acceptance records only the landing sentences the person was shown, and
// only while the landing still asks them: drift between display and decision
// is refused with a code, and a client that showed nothing records nothing:
// no consent to unseen words.
func TestAcceptanceRecordsOnlyTheLandingSentencesItShowed(t *testing.T) {
	t.Parallel()
	with := map[string]any{"approvers": []string{"owner"}, "means": "I accept the work"}
	spec := schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "review", Steps: []schema.Step{{Name: "accept", Uses: "approval", With: with}}}}}
	ctx := stepVerifiedContext(context.Background(), access.Principal{Kind: "user", Name: "owner", Via: "token"})
	approve := func(shown []string) (*Step, error) {
		a := stepAuthoritySpecFixture(t, spec)
		sentence := "the old landing sentence"
		a.Work.means = landingMeansFunc(func(context.Context, ids.ID) ([]string, error) { return []string{sentence}, nil })
		q, err := a.Work.ApprovalQuestion(ctx, a.StepID)
		if err != nil || len(q.LandingMeans) != 1 || q.LandingMeans[0] != sentence {
			t.Fatalf("question: %+v %v", q, err)
		}
		sentence = "a new landing sentence nobody was shown"
		return a.Work.Approve(ctx, Approval{StepID: a.StepID, Decision: VoteApprove, Shown: shown})
	}
	if step, err := approve([]string{"the old landing sentence"}); !errors.Is(err, errLandingQuestionChanged) || step != nil {
		t.Fatalf("an acceptance of changed words: %+v %v", step, err)
	}
	step, err := approve(nil)
	if err != nil {
		t.Fatal(err)
	}
	if evidence, _ := ReadApprovalEvidence(step.Evidence); len(evidence.Decisions) != 1 || evidence.Decisions[0].AlsoApproves != nil {
		t.Fatalf("a client that showed nothing recorded landing words: %s", step.Evidence)
	}
}

// fixedAdds reads, for any commit, the rows it was given.
type fixedAdds []AddsRow

func (rows fixedAdds) ChangeAdds(_ context.Context, q AddsQuestion) (ChangeAdds, error) {
	return ChangeAdds{Head: q.Head, Rows: slices.Clone(rows)}, nil
}

// An acceptance counts for the landing's approval of the same commit only
// if the landing reads the same list of what the change adds as the
// accepter was shown; the carried vote records the accepter's list.
// Otherwise a person approves, and the landing says why.
func TestAnAcceptanceCarriesOnlyWithTheListItWasShown(t *testing.T) {
	t.Parallel()
	shown := []AddsRow{{Name: "services", Added: []string{"postgres"}}}
	for name, landingReads := range map[string][]AddsRow{
		"same list":      shown,
		"different list": {{Name: "services", Added: []string{"postgres", "kafka"}}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			store, work, landing, _ := landingAcceptanceFixture(t)
			const candidate = "1111111111111111111111111111111111111111"
			// The acceptance was given on the candidate the landing pinned,
			// with the list it was shown.
			accepted, err := ChangeAdds{Head: candidate, Rows: slices.Clone(shown)}.finish()
			if err != nil {
				t.Fatal(err)
			}
			recordScopeStep(t, store.db, workflowsql.RecordApprovalParams{At: time.Now(), ID: stepNamed(t, store, work.ID, "accept").ID, Status: StatusPass.String(),
				EvidenceJson: json.RawMessage(mustJSON(map[string]any{"approver": "alice", "decisions": []map[string]any{{"approver": "alice", "principal": idstest.N(21).String(), "via": "session", "decision": "approve", "alsoApproves": []string{landingMeans}, "candidate": candidate, "adds": accepted}}}))})
			if err := store.db.InTx(ctx, func(tx *database.Tx) error {
				return recordReceiptIn(ctx, tx, landing.ID, landingCandidateEvent, LandingCandidate{Candidate: candidate, Target: "2222222222222222222222222222222222222222", By: "catch-up"}, time.Now())
			}); err != nil {
				t.Fatal(err)
			}
			// The landing reads its list through the reader as New wires it
			// into Work's landing owner; only who may approve is the fixture's.
			assembled := New(context.Background(), store.db, Dependencies{ChangeAdds: fixedAdds(landingReads), Repositories: &scm.RepositoryLocks{}})
			t.Cleanup(assembled.Stop)
			store = assembled.landings
			store.eligible = anyoneMayApprove
			if _, err := store.apply(ctx, work.ID, landing.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := store.carry(ctx, landing); err != nil {
				t.Fatal(err)
			}
			first := stepNamed(t, store, landing.ID, "first")
			if name == "different list" {
				withheld := receipts(t, store, landing.ID, "landing.acceptance.withheld")
				if first.Status != StatusPending || len(withheld) == 0 || !strings.Contains(withheld[0]["reason"], "what this change adds has changed since it was accepted") {
					t.Fatalf("carried an acceptance whose list the landing does not read: %s %s, withheld %v", first.Status, first.Evidence, withheld)
				}
				return
			}
			evidence, err := ReadApprovalEvidence(first.Evidence)
			if err != nil || first.Status != StatusPass || len(evidence.Decisions) != 1 || evidence.Decisions[0].Adds == nil || evidence.Decisions[0].Adds.Digest != accepted.Digest {
				t.Fatalf("an acceptance with the landing's own list did not carry it: %s %s %v", first.Status, first.Evidence, err)
			}
		})
	}
}
