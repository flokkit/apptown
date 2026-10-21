package workflow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
	"flokkit.io/forge/internal/ids"
)

// A yes given in advance holds only where every step passed outright: a
// pass with no override, and every check receipt a pass. Anything a person
// would want to look at (a failure even if already on main, an excused or
// skipped check, an unclear or unassessed one, a review finding, a skip or
// an override) is not outright.
func TestPassedOutrightIsEveryCheckPassing(t *testing.T) {
	t.Parallel()
	check := func(verdict string, extra string) *Step {
		return &Step{Name: "guidance", Type: "guidance/conformance", Status: StatusPass, Evidence: json.RawMessage(`{"checks":[{"check":"health","verdict":"pass"},{"check":"c","verdict":"` + verdict + `"` + extra + `}]}`)}
	}
	for _, tc := range []struct {
		name     string
		step     *Step
		outright bool
	}{
		{"every check passed", check("pass", ""), true},
		{"tests passed", &Step{Name: "tests", Type: "tests", Status: StatusPass, Evidence: json.RawMessage(`{"reason":"all passed"}`)}, true},
		{"an approval is not judged", &Step{Name: "accept", Type: "approval", Status: StatusPending}, true},
		{"a clean review", &Step{Name: "review", Type: "agent/review", Status: StatusPass, Evidence: json.RawMessage(`{"findings":[]}`)}, true},
		{"a failure already on main", check("fail", `,"preexisting":true`), false},
		{"an excused finding", check("excepted", ""), false},
		{"a skipped check", check("skipped", ""), false},
		{"an unclear answer", check("unclear", ""), false},
		{"a check AppTown could not run", check("cannot-assess", `,"apptownFault":true`), false},
		{"check errors", &Step{Name: "guidance", Type: "guidance/conformance", Status: StatusPass, Evidence: json.RawMessage(`{"checkErrors":1}`)}, false},
		{"unreadable check evidence", &Step{Name: "guidance", Type: "guidance/conformance", Status: StatusPass, Evidence: json.RawMessage(`{"checks":[{"check":"c","verdict":"maybe"}]}`)}, false},
		{"a review finding", &Step{Name: "review", Type: "agent/review", Status: StatusPass, Evidence: json.RawMessage(`{"findings":["stale"]}`)}, false},
		{"overridden", &Step{Name: "tests", Type: "tests", Status: StatusPass, OverrideBy: "bls"}, false},
		{"skipped by a person", &Step{Name: "scan", Type: "scan", Status: StatusSkipped}, false},
		{"failed", &Step{Name: "scan", Type: "scan", Status: StatusFail}, false},
	} {
		if got := passedOutright([]*Step{tc.step}) == ""; got != tc.outright {
			t.Errorf("%s: outright %v, want %v (%q)", tc.name, got, tc.outright, passedOutright([]*Step{tc.step}))
		}
	}
}

// A yes in advance is the person's own: given signed in or from their own
// command line it is recorded with their principal and way; through an
// agent's token, an OAuth client, a chat or with no verified caller it is
// refused.
func TestAYesInAdvanceIsOnlyAPersonsOwn(t *testing.T) {
	t.Parallel()
	q, _ := admitWorkflowSpec(t, schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "work"}}})
	person := ids.New(time.Now())
	caller := func(pr access.Principal) context.Context {
		pr.ID, pr.Kind, pr.Name = person, "user", "alice"
		return access.WithCaller(context.Background(), access.Caller{Principal: pr, Verified: true, Receipt: "alice"})
	}
	for _, tc := range []struct {
		name  string
		ctx   context.Context
		given bool
	}{
		{"signed in", caller(access.Principal{Via: "session"}), true},
		{"their command line", caller(access.Principal{Via: "token", Credential: "token:laptop"}), true},
		{"an agent's token", caller(access.Principal{Via: "token", Purpose: access.TokenAgent, Credential: "token:helper"}), false},
		{"an OAuth client", caller(access.Principal{Via: "oauth", DelegationID: ids.New(time.Now())}), false},
		{"a chat", caller(access.Principal{Via: "session", Instrument: "console", ConsoleID: "c1"}), false},
		{"no verified caller", context.Background(), false},
	} {
		yes, err := advanceYesFrom(tc.ctx, q.db, "alice", "quick-change", []string{"I accept it in advance"}, "2026-10-21T00:00:00Z")
		if tc.given != (err == nil && yes != nil) {
			t.Errorf("%s: yes %+v, err %v", tc.name, yes, err)
			continue
		}
		if tc.given && (yes.Principal != person || yes.Via == "" || yes.Workflow != "quick-change") {
			t.Errorf("%s: recorded %+v", tc.name, yes)
		}
	}
}

const landingInAdvance = "I answer for this landing before it runs, if it merges no code and every gate passes"

// inAdvanceLandingFixture is an adoption's working run accepted by a yes in
// advance on commit c1, and its landing at an approval stage that declares
// what such a yes means, after the gates in gates.
func inAdvanceLandingFixture(t *testing.T, declared string, gates ...*Step) (landings, *Run, *Run) {
	t.Helper()
	ctx := context.Background()
	store, work, _ := completionFixture(t, "working")
	if err := (advanceStore{db: store, completion: completionRecorderFunc(noCompletionEffects)}).complete(ctx, work); err != nil {
		t.Fatal(err)
	}
	accepted := insertStoredStep(t, store, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: work.ID, Stage: "finish", Name: "accept", Type: "approval", Status: StatusPending.String()})
	vote := mustJSON(map[string]any{"approver": "alice", "decisions": []map[string]any{{"approver": "alice", "decision": "approve", "alsoApproves": []string{landingInAdvance}, "candidate": "c1", "inAdvance": "I accept it in advance"}}})
	if changed, err := workflowsql.New(store.SQL()).RecordApproval(ctx, workflowsql.RecordApprovalParams{At: time.Now(), Status: StatusPass.String(), EvidenceJson: json.RawMessage(vote), ID: accepted, ExpectedStatus: StatusPending.String()}); err != nil || changed != 1 {
		t.Fatalf("stored acceptance: changed=%d %v", changed, err)
	}
	with := map[string]any{"means": landingMeans}
	if declared != "" {
		with["inAdvance"] = declared
	}
	def := schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{{Name: "approval", Steps: []schema.Step{{Name: "approve-landing", Uses: "approval", With: with}}}}}
	landingID := insertStoredRun(t, store, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: work.ProjectID, ChangeID: work.ChangeID, WorkflowName: "land", WorkflowVersion: 1, Kind: "pipeline", CurrentStage: "approval", DefinitionJson: json.RawMessage(mustJSON(def)), Status: RunRunning.String()}).ID
	// The gates the stage before ran, as the landing's own steps.
	for _, g := range gates {
		id := insertStoredStep(t, store, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: landingID, Stage: "gates", Name: g.Name, Type: g.Type, Status: StatusRunning.String()})
		if changed, err := workflowsql.New(store.SQL()).RecordApproval(ctx, workflowsql.RecordApprovalParams{At: time.Now(), ID: id, ExpectedStatus: StatusRunning.String(), Status: g.Status.String(), EvidenceJson: g.Evidence}); err != nil || changed != 1 {
			t.Fatalf("settle gate %s: %d %v", g.Name, changed, err)
		}
	}
	insertStoredStep(t, store, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: landingID, Stage: "approval", Name: "approve-landing", Type: "approval", ConfigJson: json.RawMessage(mustJSON(with)), Status: StatusPending.String()})
	if changed, err := workflowsql.New(store.SQL()).AttachAcceptedLanding(ctx, workflowsql.AttachAcceptedLandingParams{LandingRunID: landingID, WorkRunID: work.ID}); err != nil || changed != 1 {
		t.Fatalf("attach accepted landing: changed=%d %v", changed, err)
	}
	if _, err := database.Transact(ctx, store, func(tx *database.Tx) (struct{}, error) {
		return struct{}{}, recordReceiptIn(ctx, tx, landingID, landingCandidateEvent, LandingCandidate{Candidate: "c1", Target: "m1", By: "admission"}, time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	landing, err := readRun(ctx, store, landingID)
	if err != nil {
		t.Fatal(err)
	}
	return landings{db: store, eligible: anyoneMayApprove}, work, landing
}

// A yes given in advance counts for the landing's approval only where that
// approval says what such a yes means and every gate passed outright; the
// carried vote keeps that meaning.
func TestAYesInAdvanceCarriesOnlyToAnOutrightLanding(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	passed := &Step{Name: "guidance", Type: "guidance/conformance", Status: StatusPass, Evidence: json.RawMessage(`{"checks":[{"check":"health","verdict":"pass"}]}`)}
	onMain := &Step{Name: "guidance", Type: "guidance/conformance", Status: StatusPass, Evidence: json.RawMessage(`{"checks":[{"check":"health","verdict":"fail","preexisting":true}]}`)}
	unassessed := &Step{Name: "guidance", Type: "guidance/conformance", Status: StatusPass, Evidence: json.RawMessage(`{"checks":[{"check":"front-page","verdict":"cannot-assess","apptownFault":true}]}`)}
	for _, tc := range []struct {
		name     string
		declared string
		gate     *Step
		carried  bool
		reason   string
	}{
		{"every gate passed outright", landingInAdvance, passed, true, ""},
		{"a failure already on main", landingInAdvance, onMain, false, "health was fail"},
		{"a check AppTown could not run", landingInAdvance, unassessed, false, "front-page was cannot-assess"},
		{"the approval says nothing of a yes in advance", "", passed, false, "did not show what this approval means"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store, work, landing := inAdvanceLandingFixture(t, tc.declared, tc.gate)
			if _, err := store.apply(ctx, work.ID, landing.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := store.carry(ctx, landing); err != nil {
				t.Fatal(err)
			}
			step := stepNamed(t, store, landing.ID, "approve-landing")
			if (step.Status == StatusPass) != tc.carried {
				t.Fatalf("approve-landing is %s, want carried=%v; withheld %v", step.Status, tc.carried, receipts(t, store, landing.ID, "landing.acceptance.withheld"))
			}
			if !tc.carried {
				withheld := receipts(t, store, landing.ID, "landing.acceptance.withheld")
				if len(withheld) != 1 || !strings.Contains(withheld[0]["reason"], tc.reason) {
					t.Fatalf("withheld %v, want a reason saying %q", withheld, tc.reason)
				}
				return
			}
			evidence, err := ReadApprovalEvidence(step.Evidence)
			if err != nil || len(evidence.Decisions) != 1 || evidence.Decisions[0].InAdvance != landingInAdvance || evidence.Decisions[0].Approver != "alice" {
				t.Fatalf("the carried vote does not keep its meaning: %s %v", step.Evidence, err)
			}
		})
	}
}
