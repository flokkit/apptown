package workflow

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"github.com/flokkit/apptown/internal/ids"
)

// A change accepted under the standing rule lands without a person after its
// landing caught up with main: the rule judges the new candidate again and
// carry applies the acceptance. A candidate the rule refuses, or one that is
// more than main merged into the accepted commit, waits for a person with
// the reason recorded. (A person's yes keeps its exact-commit rule:
// TestAcceptanceCarriesOnlyForTheAcceptedWork.)
func TestStandingAcceptanceIsAssessedAgainAfterCatchUp(t *testing.T) {
	for _, mode := range []string{"clean catch-up", "rule refuses the candidate", "merge carries an edit", "work published after acceptance"} {
		t.Run(mode, func(t *testing.T) {
			const meaning = "declared fix in range and every check met"
			rig := stepAuthoritySpecFixture(t, schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "checks", Steps: []schema.Step{{Name: "tests", Uses: "tests"}}}, {Name: "final", Steps: []schema.Step{{Name: "accept", Uses: "approval", With: map[string]any{"standing": map[string]any{"class": "routine-update", "means": meaning}}}}}}})
			ctx := stepVerifiedContext(t.Context(), testAdmittedPrincipal(t, rig.Access, "boss"))
			if err := project.New(rig.DB, nil).SetDefaultBranch(ctx, rig.ProjectID, "main"); err != nil {
				t.Fatal(err)
			}
			source, err := ReadSourceIn(ctx, rig.DB, RunRef{ID: rig.RunID, ProjectID: rig.ProjectID})
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			folder, err := source.Workspace(dir)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.MkdirAll(folder, 0755); err != nil {
				t.Fatal(err)
			}
			commit := func(repo string, files map[string]string, message string) string {
				t.Helper()
				for name, body := range files {
					if err := os.WriteFile(filepath.Join(repo, name), []byte(body), 0644); err != nil {
						t.Fatal(err)
					}
				}
				gittest.Run(t, repo, "add", "-A")
				gittest.RunAsTester(t, repo, "commit", "-q", "-m", message)
				return gittest.Run(t, repo, "rev-parse", "HEAD")
			}
			gittest.Run(t, folder, "init", "-q", "-b", source.Branch)
			gittest.RunAsTester(t, folder, "commit", "-q", "--allow-empty", "-m", "base")
			gittest.Run(t, folder, "branch", "main")
			accepted := commit(folder, map[string]string{"go.mod": "module app\nrequire dep v1.2.4\n"}, "update dep")
			if _, err := database.Transact(ctx, rig.DB, func(tx *database.Tx) (scm.Authority, error) {
				return scm.AdvanceAuthorityIn(ctx, tx, rig.ProjectID, source.Branch, scm.RunHolder(rig.RunID))
			}); err != nil {
				t.Fatal(err)
			}
			attempts := newStepAttempts(rig.DB, func(string) bool { return true })
			attempt, err := attempts.start(ctx, mustStandingStep(t, rig.DB, rig.StepID), "tests", ids.Nil)
			if err != nil {
				t.Fatal(err)
			}
			outcome, err := (CheckEvidence{Checks: []CheckReceipt{{Check: "routine", Evaluator: "routine", Verdict: VerdictPass, Preexisting: true}}}).Outcome(StatusPass, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := attempts.finish(ctx, attempt, outcome); err != nil {
				t.Fatal(err)
			}
			if _, err := (advanceStore{db: rig.DB, stages: rig.Work.stages}).transition(ctx, rigRun(t, rig), "final", "apptown-system"); err != nil {
				t.Fatal(err)
			}
			for _, step := range mustSteps(t, rig.DB, rig.RunID) {
				if step.Name == "accept" {
					rig.StepID = step.ID
				}
			}

			// The hub holds the published branch; the inspector reads what
			// the run it inspects has on it, as the real one does.
			hub, err := source.Repository(dir)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(hub), 0755); err != nil {
				t.Fatal(err)
			}
			gittest.Run(t, dir, "clone", "-q", folder, hub)
			gittest.Run(t, hub, "checkout", "-q", "-b", "main", "origin/main")
			gittest.Run(t, hub, "config", "user.name", "Test")
			gittest.Run(t, hub, "config", "user.email", "test@example.com")
			inspected := []string{}
			procedures := &standingProcedureFixture{means: meaning}
			rules := NewStandingRules(rig.DB, StandingDependencies{Identity: rig.Access, Authority: rig.Authorizer, Procedures: procedures, Portfolio: &standingPortfolioFixture{}, Presence: standingPresenceFixture{clear: true}, Inspector: standingInspectorFunc(func(_ context.Context, inspected_ Source, _ JanitorChoices) (StandingInspection, error) {
				tip := gittest.Run(t, hub, "rev-parse", "refs/heads/"+source.Branch)
				inspected = append(inspected, inspected_.RootRole)
				if inspected_.RootRole == "landing" && mode == "rule refuses the candidate" {
					return StandingInspection{Classification: "requires-person", Reason: "the merged manifest moves a major version", Candidate: tip}, nil
				}
				return StandingInspection{Classification: "routine-dependency", Candidate: tip, ChangedFiles: []string{"go.mod"}}, nil
			})})
			enableStanding(t, rules, ctx, JanitorChoices{Dependencies: "routine"})
			rig.Work.standing = &rules
			rig.Work.landings.standing = &rules
			rig.Work.landings.eligible = anyoneMayApprove
			rig.Work.abandonments.locks = &scm.RepositoryLocks{}
			rig.Work.pins = landingPins{db: rig.DB, dataDir: dir}
			queue := &standingQueuedFixture{}
			rig.Work.standingRequests = queue
			if _, err := rig.Work.answerFromAcceptance(t.Context(), rigRun(t, rig)); err != nil || len(queue.keys) != 1 {
				t.Fatalf("working acceptance was not queued: %v %+v", err, queue.keys)
			}
			if result, err := rig.Work.RunStandingAcceptance(t.Context(), queue.keys[0], nil); err != nil || result["cast"] != true {
				t.Fatalf("the working run was not accepted under the standing rule: %+v %v", result, err)
			}

			// Another change lands on main; the landing's admission merges
			// main into the branch, as workcommands.catchUp does.
			commit(hub, map[string]string{"other.txt": "another change landed\n"}, "another change")
			candidate := ""
			switch mode {
			case "merge carries an edit":
				gittest.Run(t, hub, "checkout", "-q", "--detach", accepted)
				gittest.RunAsTester(t, hub, "merge", "-q", "--no-commit", "--no-ff", "main")
				candidate = commit(hub, map[string]string{"app.go": "package app // nobody accepted this\n"}, "Merge main into change")
				gittest.Run(t, hub, "checkout", "-q", "main")
			default:
				merge, err := scm.MergeMain(ctx, hub, accepted, "main", "Merge main into change\n", time.Now())
				if err != nil || merge.Commit == "" {
					t.Fatalf("catch-up: %+v %v", merge, err)
				}
				candidate = merge.Commit
				if mode == "work published after acceptance" {
					gittest.Run(t, hub, "checkout", "-q", "--detach", candidate)
					candidate = commit(hub, map[string]string{"app.go": "package app // published later\n"}, "more work")
					gittest.Run(t, hub, "checkout", "-q", "main")
				}
			}
			gittest.Run(t, hub, "update-ref", "refs/heads/"+source.Branch, candidate)

			work := rigRun(t, rig)
			completed := completionRecorderFunc(func(context.Context, *database.Tx, CompletedRun) error { return nil })
			if err := (advanceStore{db: rig.DB, stages: rig.Work.stages, completion: completed}).complete(ctx, work); err != nil {
				t.Fatal(err)
			}
			spec := schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{
				{Name: "approval", Steps: []schema.Step{{Name: "accept", Uses: "approval", With: map[string]any{"means": "I approve the landing", "standing": map[string]any{"class": "routine-update", "means": "land this qualified candidate"}}}}},
				{Name: "ship", Steps: []schema.Step{{Name: "build-exists", Uses: "script"}}, Gate: &schema.Gate{Require: []string{"build-exists"}}},
			}}
			admissions := newRunAdmissions(rig.DB, NewStages(nil, func(string) bool { return false }), nil)
			landing, err := admissions.records.create(ctx, StartRequest{ProjectID: rig.ProjectID, ChangeID: rig.ChangeID, WorkflowName: "land", InputsJSON: "{}", acceptedRunID: work.ID, landingBy: "boss", requestedBy: "apptown-system"}, PreparedRun{Workflow: DefinitionRef{Name: "land", Version: 1}, Spec: spec, Events: []StartEvent{LandingPinRequired()}})
			if err != nil {
				t.Fatal(err)
			}
			if err := admissions.records.prepared(ctx, landing.ID); err != nil {
				t.Fatal(err)
			}
			if err := (landingPins{db: rig.DB, dataDir: dir}).pinPrepared(ctx, landing); err != nil {
				t.Fatal(err)
			}
			if pinned, ok, err := LandingCandidateIn(ctx, rig.DB, landing.ID); err != nil || !ok || pinned.Candidate != candidate || pinned.Candidate == accepted {
				t.Fatalf("landing pinned %+v %t %v, want the catch-up %s", pinned, ok, err, candidate)
			}
			if change, err := readChange(ctx, rig.DB, rig.ChangeID); err != nil || change.State != ChangeLanding {
				t.Fatalf("the change is not landing: %+v %v", change, err)
			}
			owner := landings{db: rig.DB, standing: &rules, eligible: anyoneMayApprove}
			if _, err := owner.apply(ctx, work.ID, landing.ID); err != nil {
				t.Fatal(err)
			}
			landing, err = readRun(ctx, rig.DB, landing.ID)
			if err != nil {
				t.Fatal(err)
			}

			// carry waits for the rule; the standing job judges the candidate.
			if cast, err := owner.carry(ctx, landing); err != nil || cast {
				t.Fatalf("carry decided before the rule judged the new candidate: %t %v", cast, err)
			}
			if kinds := landingEventKinds(t, rig, landing.ID); strings.Contains(kinds, "landing.acceptance.") {
				t.Fatalf("carry judged before the rule: %s", kinds)
			}
			queue.keys = nil
			if _, err := rig.Work.answerFromAcceptance(t.Context(), landing); err != nil || len(queue.keys) != 1 {
				t.Fatalf("the landing's standing decision was not queued: %v %+v", err, queue.keys)
			}
			if _, err := rig.Work.RunStandingAcceptance(t.Context(), queue.keys[0], nil); err != nil {
				t.Fatal(err)
			}
			landing, err = readRun(ctx, rig.DB, landing.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := owner.carry(ctx, landing); err != nil {
				t.Fatal(err)
			}
			var decision standingDecision
			applied := landingEvent(t, rig, landing.ID, "acceptance.standing.applied", &decision)
			var withheld acceptanceWithheld
			refused := landingEvent(t, rig, landing.ID, "landing.acceptance.withheld", &withheld)
			step := mustLandingStep(t, rig.DB, landing.ID, "accept")
			if mode != "clean catch-up" {
				if applied || !refused || step.Status != StatusPending {
					t.Fatalf("the candidate was accepted without a person: applied=%t refused=%t %s", applied, refused, step.Status)
				}
				want := map[string]string{"rule refuses the candidate": "the merged manifest moves a major version", "merge carries an edit": "changes more than merging main in", "work published after acceptance": "is not a merge of main"}[mode]
				if !strings.Contains(withheld.Reason, "assessed again on the landing's candidate") || !strings.Contains(withheld.Reason, want) {
					t.Fatalf("the person is not told why: %q", withheld.Reason)
				}
				return
			}
			if !applied || refused || decision.Assessment == nil || decision.Assessment.Inspection.Candidate != candidate {
				t.Fatalf("the rule did not judge the new candidate: applied=%t refused=%t %+v (%s)", applied, refused, decision, withheld.Reason)
			}
			if inspected[len(inspected)-1] != "landing" {
				t.Fatalf("the landing's candidate was not inspected: %v", inspected)
			}
			if !landingEvent(t, rig, landing.ID, "landing.acceptance.carried", nil) || step.Status != StatusPass {
				t.Fatalf("the standing acceptance was not carried: %s %s", step.Status, landingEventKinds(t, rig, landing.ID))
			}
			ev, err := ReadApprovalEvidence(step.Evidence)
			if err != nil || len(ev.Decisions) != 1 || ev.Decisions[0].Standing == nil || ev.Decisions[0].Approver != "the janitor" || ev.Candidate != candidate {
				t.Fatalf("landing vote: %s %v", step.Evidence, err)
			}
			landing, err = readRun(ctx, rig.DB, landing.ID)
			if err != nil {
				t.Fatal(err)
			}
			service := scm.New(rig.DB, NewSCMWork(rig.DB, dir, policyLawRules{}, nil), &scm.RepositoryLocks{}, nil)
			driver := standingClaimDriver{Driver: (scm.GitDrivers{DataDir: dir}).Driver(), prepared: func() {}}
			merges := NewMerges(rig.DB, dir, reviewClaimLaws{before: func() {}}, service, mergeEffectsTest{driver: driver, observed: func(scm.Result) {}})
			merges.standing = &rules
			if result, err := merges.Merge(ctx, landing); err != nil || !result.Allowed {
				t.Fatalf("the caught-up candidate did not land: %+v %v", result, err)
			}
			gittest.Run(t, hub, "merge-base", "--is-ancestor", candidate, "main")
			if got, want := gittest.Run(t, hub, "rev-parse", "main^{tree}"), gittest.Run(t, hub, "rev-parse", candidate+"^{tree}"); got != want {
				t.Fatalf("main's tree is %s, want the candidate's %s", got, want)
			}
		})
	}
}

func mustSteps(t *testing.T, db *database.DB, runID ids.ID) []*Step {
	t.Helper()
	steps, err := readSteps(t.Context(), db, runID)
	if err != nil {
		t.Fatal(err)
	}
	return steps
}

func mustLandingStep(t *testing.T, db *database.DB, runID ids.ID, name string) *Step {
	t.Helper()
	for _, step := range mustSteps(t, db, runID) {
		if step.Name == name {
			return step
		}
	}
	t.Fatalf("run %s has no step %s", runID, name)
	return nil
}

func landingEventKinds(t *testing.T, rig *stepAuthorityTestRig, runID ids.ID) string {
	t.Helper()
	snapshot, err := rig.Work.Snapshot(stepVerifiedContext(t.Context(), testAdmittedPrincipal(t, rig.Access, "boss")), runID, TraceView)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, event := range snapshot.Events {
		kinds = append(kinds, event.Kind)
	}
	return strings.Join(kinds, " ")
}

// landingEvent reports whether runID recorded an event of kind, decoding the
// latest one into into when it is not nil.
func landingEvent(t *testing.T, rig *stepAuthorityTestRig, runID ids.ID, kind string, into any) bool {
	t.Helper()
	snapshot, err := rig.Work.Snapshot(stepVerifiedContext(t.Context(), testAdmittedPrincipal(t, rig.Access, "boss")), runID, TraceView)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range snapshot.Events {
		if event.Kind != kind {
			continue
		}
		found = true
		if into != nil {
			if err := json.Unmarshal(event.Payload, into); err != nil {
				t.Fatal(err)
			}
		}
	}
	return found
}
