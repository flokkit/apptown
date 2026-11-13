package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fix"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"github.com/flokkit/apptown/internal/ids"
)

type standingPortfolioFixture struct {
	uncovered bool
	projectID ids.ID
	hold      string
	// failFor names a project whose coverage cannot be read.
	failFor ids.ID
}

func (f *standingPortfolioFixture) StandingPortfolio(_ context.Context, _ database.Queryer, id ids.ID, _ StandingCategories) ([]StandingProjectPreview, error) {
	if id.IsZero() {
		id = f.projectID
	}
	if !f.failFor.IsZero() && id == f.failFor {
		return nil, errors.New("deployment requires a selected revision")
	}
	return []StandingProjectPreview{{ProjectID: id, Covered: !f.uncovered, OnlyChecksStarts: true, NeedsPackUpgrade: []string{"language"}, Deployments: []StandingEnvironment{{ProjectID: id, Deployment: "main", Setting: "automatic"}}}}, nil
}
func (f *standingPortfolioFixture) StandingHold(context.Context, database.Queryer, ids.ID) (string, error) {
	return f.hold, nil
}
func (f *standingProcedureFixture) InstanceStandingProcedure(ctx context.Context, q database.Queryer, name string) (StandingProcedure, error) {
	return f.StandingProcedure(ctx, q, ids.Nil, name)
}

// One instance consent applies to future projects, but each use reads current
// project choice, coverage, approval meaning, issuer grants and release health.
func TestInstanceStandingConsentRechecksEveryProjectAtUse(t *testing.T) {
	a, s, p, owner, _ := standingFixture(t)
	portfolio := &standingPortfolioFixture{}
	s.deps.Portfolio = portfolio
	in := StandingDefinition{Scope: "instance", Schedules: []standingSchedule{{Workflow: "test", Crons: map[string]string{"routine": "0 6 * * 1", "aggressive": "0 6 * * *"}}}, Categories: StandingCategories{Dependencies: "routine", SecurityFixes: true}, Delivery: "full"}
	if _, err := s.Preview(owner, in); err == nil {
		t.Fatal("a project owner enabled instance consent without instance administration")
	}
	boss := stepVerifiedContext(t.Context(), testAdmittedPrincipal(t, a.Access, "boss"))
	rule := enableStanding(t, s, boss, in)
	if !strings.Contains(rule.Question.Shown, "releases it to the running app") || !strings.Contains(rule.Question.Shown, "including ones created later") || !strings.Contains(rule.Question.Shown, "An agent writes any code") || len(rule.Question.Portfolio) != 1 || !rule.Question.Portfolio[0].OnlyChecksStarts {
		t.Fatalf("incomplete question: %+v", rule.Question)
	}
	later, err := database.Transact(t.Context(), a.DB, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "later", Name: "Later"})
	})
	if err != nil {
		t.Fatal(err)
	}
	use := func(want bool) {
		t.Helper()
		got, cron, err := s.StandingForWorkflow(t.Context(), later, "test")
		if err != nil || (!got.RuleID.IsZero()) != want || want && (got.RuleID != rule.RuleID || cron != "0 6 * * 1") {
			t.Fatalf("eligible=%t: %+v %s %v", want, got, cron, err)
		}
	}
	use(true)
	if err := s.SetProjectChoice(boss, later, "off", "working on recovery"); err != nil {
		t.Fatal(err)
	}
	use(false)
	state, err := s.ProjectState(boss, later)
	if err != nil || state.Level != "off" || state.Choice == nil || state.Choice.Person.Name != "boss" || state.Choice.Reason != "working on recovery" || state.Choice.At == "" {
		t.Fatalf("choice did not retain the person and reason: %+v %v", state, err)
	}
	if err := s.SetProjectChoice(boss, later, "routine", "recovery finished"); err != nil {
		t.Fatal(err)
	}
	use(true)
	portfolio.uncovered = true
	use(false)
	portfolio.uncovered = false
	portfolio.hold = "release 2 failed"
	use(false)
	portfolio.hold = ""
	p.means = "a changed approval meaning"
	use(false)
	p.means = rule.Question.Projects[0].Procedure.Acceptance.Means
	use(true)
	if _, err := s.Disable(boss, rule.RuleID, rule.Revision); err != nil {
		t.Fatal(err)
	}
	use(false)
}

func TestAnAgentCannotGrantInstanceStandingConsent(t *testing.T) {
	a, s, _, _, _ := standingFixture(t)
	s.deps.Portfolio = &standingPortfolioFixture{}
	person := testAdmittedPrincipal(t, a.Access, "boss")
	person.Via = "console/agent"
	person.Purpose = access.TokenAgent
	ctx := stepVerifiedContext(t.Context(), person)
	in := StandingDefinition{Scope: "instance", Schedules: []standingSchedule{{Workflow: "test", Crons: map[string]string{"routine": "0 6 * * 1", "aggressive": "0 6 * * *"}}}, Categories: StandingCategories{Dependencies: "routine"}, Delivery: "full"}
	if _, err := s.Preview(ctx, in); err == nil {
		t.Fatal("agent assent was accepted")
	}
}

func TestStandingEvidenceIncludesEveryEarlierRequiredStage(t *testing.T) {
	for _, tc := range []struct {
		name    string
		verdict CheckVerdict
		detail  string
		okay    bool
	}{
		{"passed", VerdictPass, "", true}, {"not applicable with its reason", VerdictNotApplicable, "no Go part", true},
		{"not applicable without reason", VerdictNotApplicable, "", false}, {"preexisting failure", VerdictFail, "already on main", false},
		{"excepted", VerdictExcepted, "", false}, {"unknown", VerdictCannotAssess, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := func(task *Execution) {
				if _, err := task.Claim(); err != nil {
					t.Error(err)
					return
				}
				task.Settle((CheckEvidence{Checks: []CheckReceipt{{Check: "old-stage-check", Verdict: tc.verdict, Detail: tc.detail, Preexisting: true}}}).Outcome(StatusPass, nil))
			}
			rig := stepAuthorityRunnerFixture(t, schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{
				{Name: "first", Steps: []schema.Step{{Name: "earlier", Uses: "tests"}}},
				{Name: "final", Steps: []schema.Step{{Name: "accept", Uses: "approval"}}},
			}}, map[string]Runner{"tests": runner})
			rig.Work.advancer = newAdvancer(rig.DB, advanceRuntime{stages: rig.Work.stages, checks: advancementScopeChecks{refresh: func(context.Context, *Run) error { return nil }}})
			ctx := stepVerifiedContext(t.Context(), testAdmittedPrincipal(t, rig.Access, "owner"))
			if err := rig.Work.ExecuteStep(ctx, rig.StepID); err != nil {
				t.Fatal(err)
			}
			if _, err := rig.Work.Advance(ctx, rig.RunID); err != nil {
				t.Fatal(err)
			}
			run, err := rig.Work.Run(ctx, rig.RunID)
			if err != nil {
				t.Fatal(err)
			}
			why, err := standingEvidence(ctx, rig.DB, run, false, 0)
			if err != nil || (why == "") != tc.okay {
				t.Fatalf("standing evidence: %q %v; met=%t", why, err, tc.okay)
			}
		})
	}
}

func TestStandingVotePreservesAuthorityWithoutClaimingHumanAssent(t *testing.T) {
	a, _, _, ctx, _ := standingFixture(t)
	who := testAdmittedPrincipal(t, a.Access, "owner").Actor()
	vote := ApprovalVote{Approver: "the janitor", Principal: who.Principal, Via: "standing", Decision: VoteApprove, Standing: &standingVote{Level: "routine", RuleID: ids.New(time.Now()), Revision: 3, Issuer: who, Means: "bounded fix, checks met", AssessmentDigest: "independent-digest"}}
	if votedInPerson(vote) {
		t.Fatal("standing was relabelled human assent")
	}
	raw, err := json.Marshal(vote)
	if err != nil {
		t.Fatal(err)
	}
	var read ApprovalVote
	if err := json.Unmarshal(raw, &read); err != nil {
		t.Fatal(err)
	}
	if read.Standing == nil || *read.Standing != *vote.Standing {
		t.Fatalf("lost standing authority: %s", raw)
	}
	// The owner's approval transaction, rather than a storage write, records it.
	run, err := readRun(ctx, a.DB, a.RunID)
	if err != nil {
		t.Fatal(err)
	}
	step, err := readStep(ctx, a.DB, a.StepID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.Transact(ctx, a.DB, func(tx *database.Tx) (reworkedResult, error) {
		return (reworkStore{db: a.DB}).approveIn(ctx, tx, run, step, vote)
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := database.Transact(ctx, a.DB, func(tx *database.Tx) (string, error) { return approvedByIn(ctx, tx, run) })
	if err != nil || got != "the janitor, under standing approval "+vote.Standing.RuleID.String()+" (rev 3), granted by owner" {
		t.Fatalf("landing attribution: %q %v", got, err)
	}
}

func TestStandingProcessingAcceptsCheckedCodeAndRefusesCurrentHold(t *testing.T) {
	for _, mode := range []string{"revoke within claim", "checked code", "aggressive major", "routine major", "routine during assessment", "routine at merge", "held back", "hold during assessment", "off during assessment", "purpose changed", "candidate moved", "rule disabled", "rule revised", "failed check", "off at merge", "hold at merge", "disabled at merge"} {
		t.Run(mode, func(t *testing.T) {
			held := mode != "revoke within claim" && mode != "checked code" && mode != "aggressive major" && !strings.HasSuffix(mode, "at merge")
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
			gittest.Run(t, folder, "init", "-q", "-b", source.Branch)
			gittest.RunAsTester(t, folder, "commit", "-q", "--allow-empty", "-m", "base")
			gittest.Run(t, folder, "branch", "main")
			if err = os.WriteFile(filepath.Join(folder, "app.go"), []byte("package app\n// repaired for the declared dependency update\n"), 0644); err != nil {
				t.Fatal(err)
			}
			gittest.Run(t, folder, "add", "app.go")
			gittest.RunAsTester(t, folder, "commit", "-q", "-m", "repair application code")
			tip := gittest.Run(t, folder, "rev-parse", "HEAD")
			if _, err := database.Transact(ctx, rig.DB, func(tx *database.Tx) (scm.Authority, error) {
				return scm.AdvanceAuthorityIn(ctx, tx, rig.ProjectID, source.Branch, scm.RunHolder(rig.RunID))
			}); err != nil {
				t.Fatal(err)
			}
			attempt, err := newStepAttempts(rig.DB, func(string) bool { return true }).start(ctx, mustStandingStep(t, rig.DB, rig.StepID), "tests", ids.Nil)
			if err != nil {
				t.Fatal(err)
			}
			verdict := VerdictPass
			if mode == "failed check" {
				verdict = VerdictFail
			}
			outcome, err := (CheckEvidence{Checks: []CheckReceipt{{Check: "tests", Verdict: verdict, Preexisting: true}}}).Outcome(StatusPass, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := newStepAttempts(rig.DB, func(string) bool { return true }).finish(ctx, attempt, outcome); err != nil {
				t.Fatal(err)
			}
			if _, err := (advanceStore{db: rig.DB, stages: rig.Work.stages}).transition(ctx, rigRun(t, rig), "final", "apptown-system"); err != nil {
				t.Fatal(err)
			}
			steps, err := readSteps(ctx, rig.DB, rig.RunID)
			if err != nil {
				t.Fatal(err)
			}
			for _, step := range steps {
				if step.Name == "accept" {
					rig.StepID = step.ID
				}
			}

			portfolio := &standingPortfolioFixture{}
			if mode == "held back" {
				portfolio.hold = "release 2 failed"
			}
			procedures := &standingProcedureFixture{means: meaning}
			var rules StandingRules
			rules = NewStandingRules(rig.DB, StandingDependencies{Identity: rig.Access, Authority: rig.Authorizer, Procedures: procedures, Portfolio: portfolio, Presence: standingPresenceFixture{clear: true}, Inspector: standingInspectorFunc(func(_ context.Context, _ Source, categories StandingCategories) (StandingInspection, error) {
				if strings.Contains(mode, "major") || strings.Contains(mode, "routine during") {
					before := []fix.Listing{{Ecosystem: "npm", Package: "example", Version: "1.0.0", Source: "registry", Attributes: map[string]any{}}}
					after := []fix.Listing{{Ecosystem: "npm", Package: "example", Version: "2.0.0", Source: "registry", Attributes: map[string]any{}}}
					if _, err := fix.CompareUpdate(before, after, categories.Dependencies, nil); err != nil {
						return StandingInspection{Classification: "requires-person", Reason: err.Error()}, nil
					}
				}
				switch mode {
				case "routine during assessment":
					if err := rules.SetProjectChoice(ctx, rig.ProjectID, "routine", "reduce update level"); err != nil {
						t.Fatal(err)
					}
				case "hold during assessment":
					portfolio.hold = "release 2 failed"
				case "off during assessment":
					if err := rules.SetProjectChoice(ctx, rig.ProjectID, "off", "pause during inspection"); err != nil {
						t.Fatal(err)
					}
				case "purpose changed":
					procedures.means = "a different approval purpose"
				case "rule revised":
					rows, err := rules.List(ctx, rig.ProjectID)
					if err != nil || len(rows) != 1 {
						t.Fatalf("rules: %+v %v", rows, err)
					}
					old := rows[0].Rule
					q, err := rules.Preview(ctx, old.Question.Definition)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := rules.Enable(ctx, StandingCommand{RuleID: old.RuleID, ExpectedRevision: old.Revision, Definition: q.Definition, ShownDigest: q.Digest}); err != nil {
						t.Fatal(err)
					}
				case "rule disabled":
					rows, err := rules.List(ctx, rig.ProjectID)
					if err != nil || len(rows) != 1 {
						t.Fatalf("rules: %+v %v", rows, err)
					}
					if _, err := rules.Disable(ctx, rows[0].Rule.RuleID, rows[0].Rule.Revision); err != nil {
						t.Fatal(err)
					}
				}
				candidate := tip
				if mode == "candidate moved" {
					candidate = "different-candidate"
				}
				return StandingInspection{Classification: "routine-dependency", Candidate: candidate, ChangedFiles: []string{"app.go"}}, nil
			})})
			in := StandingDefinition{Scope: "instance", Schedules: []standingSchedule{{Workflow: "test", Crons: map[string]string{"routine": "0 6 * * 1", "aggressive": "0 6 * * *"}}}, Categories: StandingCategories{Dependencies: "routine"}, Delivery: "full"}
			if mode == "aggressive major" || mode == "routine during assessment" || mode == "routine at merge" {
				in.Categories.Dependencies = "aggressive"
			}
			rule := enableStanding(t, rules, ctx, in)
			rig.Work.standing = &rules
			rig.Work.abandonments.locks = &scm.RepositoryLocks{}
			rig.Work.pins = landingPins{db: rig.DB, dataDir: dir}
			queue := &standingQueuedFixture{}
			rig.Work.standingRequests = queue
			if cast, err := rig.Work.answerFromAcceptance(t.Context(), rigRun(t, rig)); err != nil || cast || len(queue.keys) != 1 {
				t.Fatalf("standing acceptance was not queued: %t %v %+v", cast, err, queue.keys)
			}
			if queue.keys[0] != fmt.Sprintf("%s/%s/0", rig.RunID, rig.StepID) {
				t.Fatalf("wrong decision key: %s", queue.keys[0])
			}
			result, err := rig.Work.RunStandingAcceptance(t.Context(), queue.keys[0], nil)
			cast, _ := result["cast"].(bool)
			if err != nil || cast == held {
				t.Fatalf("cast=%t held=%t: %v", cast, held, err)
			}
			read, err := rig.Work.Step(ctx, rig.StepID)
			if err != nil {
				t.Fatal(err)
			}
			if held {
				if read.Step.Status != StatusPending {
					t.Fatal("held project got the vote")
				}
			} else {
				ev, err := ReadApprovalEvidence(read.Step.Evidence)
				if err != nil || len(ev.Decisions) != 1 || ev.Decisions[0].Standing == nil || ev.Decisions[0].Standing.RuleID != rule.RuleID || ev.Decisions[0].Candidate != tip {
					t.Fatalf("typed standing vote missing: %s %v", read.Step.Evidence, err)
				}
				standingCheckedCodeLanding(t, rig, rules, ctx, source, dir, folder, tip, mode)
			}
			// A crash/redelivery neither repeats the vote nor writes another refusal.
			if again, err := rig.Work.processStanding(t.Context(), rig.RunID, rig.StepID, 0); err != nil || again {
				t.Fatalf("decision repeated: %t %v", again, err)
			}
		})
	}
}

func TestStandingCarryRequiresEveryLandingCheckAndCurrentProjectConsent(t *testing.T) {
	for _, tc := range []struct {
		name      string
		verdict   CheckVerdict
		hold, off bool
		want      bool
	}{
		{"all checks met", VerdictPass, false, false, true},
		{"already failing on main", VerdictFail, false, false, false},
		{"waived landing check", VerdictExcepted, false, false, false},
		{"held after acceptance", VerdictPass, true, false, false},
		{"turned off after acceptance", VerdictPass, false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, rules, _, _, _ := standingFixture(t)
			ctx := stepVerifiedContext(t.Context(), testAdmittedPrincipal(t, a.Access, "boss"))
			portfolio := &standingPortfolioFixture{}
			rules.deps.Portfolio = portfolio
			in := StandingDefinition{Scope: "instance", Schedules: []standingSchedule{{Workflow: "test", Crons: map[string]string{"routine": "0 6 * * 1", "aggressive": "0 6 * * *"}}}, Categories: StandingCategories{Dependencies: "routine"}, Delivery: "full"}
			rule := enableStanding(t, rules, ctx, in)
			authority := &standingVote{Level: "routine", RuleID: rule.RuleID, Revision: rule.Revision, Issuer: rule.Issuer, Means: rule.Question.Projects[0].Procedure.Acceptance.Means, AssessmentDigest: "checked-source"}
			work, err := readRun(ctx, a.DB, a.RunID)
			if err != nil {
				t.Fatal(err)
			}
			accept, err := readStep(ctx, a.DB, a.StepID)
			if err != nil {
				t.Fatal(err)
			}
			_, err = database.Transact(ctx, a.DB, func(tx *database.Tx) (reworkedResult, error) {
				return (reworkStore{db: a.DB}).approveIn(ctx, tx, work, accept, ApprovalVote{Approver: "the janitor", Principal: rule.Issuer.Principal, Via: "standing", Decision: VoteApprove, Standing: authority, AlsoApproves: []string{rule.Question.Projects[0].Procedure.Landing.Means}})
			})
			if err != nil {
				t.Fatal(err)
			}
			completion := completionRecorderFunc(func(context.Context, *database.Tx, CompletedRun) error { return nil })
			if err := (advanceStore{db: a.DB, stages: a.Work.stages, completion: completion}).complete(ctx, work); err != nil {
				t.Fatal(err)
			}
			spec := schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{
				{Name: "checks", Steps: []schema.Step{{Name: "tests", Uses: "tests"}}},
				{Name: "approval", Steps: []schema.Step{{Name: "accept", Uses: "approval", With: map[string]any{"means": "I approve the landing", "standing": map[string]any{"class": "routine-update", "means": rule.Question.Projects[0].Procedure.Landing.Means}}}}},
				{Name: "ship", Steps: []schema.Step{{Name: "record", Uses: "script"}}},
			}}
			admissions := newRunAdmissions(a.DB, NewStages(nil, func(string) bool { return false }), nil)
			landing, err := admissions.records.create(ctx, StartRequest{ProjectID: a.ProjectID, ChangeID: a.ChangeID, WorkflowName: "land", InputsJSON: "{}", acceptedRunID: work.ID, requestedBy: "apptown-system"}, PreparedRun{Workflow: DefinitionRef{Name: "land", Version: 1}, Spec: spec})
			if err != nil {
				t.Fatal(err)
			}
			if err := admissions.records.prepared(ctx, landing.ID); err != nil {
				t.Fatal(err)
			}
			steps, err := readSteps(ctx, a.DB, landing.ID)
			if err != nil {
				t.Fatal(err)
			}
			attempt, err := (newStepAttempts(a.DB, func(string) bool { return true })).start(ctx, steps[0], "tests", ids.Nil)
			if err != nil {
				t.Fatal(err)
			}
			outcome, err := (CheckEvidence{Checks: []CheckReceipt{{Check: "landing-check", Verdict: tc.verdict, Preexisting: true}}}).Outcome(StatusPass, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := (newStepAttempts(a.DB, func(string) bool { return true })).finish(ctx, attempt, outcome); err != nil {
				t.Fatal(err)
			}
			current, err := readRun(ctx, a.DB, landing.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := (advanceStore{db: a.DB, stages: NewStages(nil, func(string) bool { return false })}).transition(ctx, current, "approval", "apptown-system"); err != nil {
				t.Fatal(err)
			}
			current, err = readRun(ctx, a.DB, landing.ID)
			if err != nil {
				t.Fatal(err)
			}
			store := landings{db: a.DB, standing: &rules, eligible: anyoneMayApprove}
			if _, err := store.apply(ctx, work.ID, current.ID); err != nil {
				t.Fatal(err)
			}
			if tc.hold {
				portfolio.hold = "release failed"
			}
			if tc.off {
				if err := rules.SetProjectChoice(ctx, a.ProjectID, "off", "pause updates"); err != nil {
					t.Fatal(err)
				}
			}
			cast, err := store.carry(ctx, current)
			if err != nil || cast != tc.want {
				t.Fatalf("carried=%t want=%t: %v", cast, tc.want, err)
			}
			if tc.want {
				by, err := database.Transact(ctx, a.DB, func(tx *database.Tx) (string, error) { return approvedByIn(ctx, tx, current) })
				if err != nil || by != "the janitor, under standing approval "+authority.RuleID.String()+" (rev 1), granted by boss" {
					t.Fatalf("standing landing attribution: %q %v", by, err)
				}
				// The same current-rule read runs again at the merge claim.
				if err := rules.SetProjectChoice(ctx, a.ProjectID, "off", "pause before merge"); err != nil {
					t.Fatal(err)
				}
				why, err := standingMergeRefusalIn(ctx, a.DB, current, &rules)
				if err != nil || !strings.Contains(why, "turned off") {
					t.Fatalf("merge claim ignored the off level: %q %v", why, err)
				}
			}
		})
	}
}

func TestStandingEvidenceFollowsEarlierCallsAndAllTheirChecks(t *testing.T) {
	for _, tc := range []struct{ failed, required bool }{{false, true}, {true, true}, {false, false}, {true, false}} {
		failed := tc.failed
		t.Run(map[bool]string{false: "all met", true: "child check failed outside its gate"}[failed], func(t *testing.T) {
			required := []string{"checkpoint"}
			if tc.required {
				required = append(required, "call")
			}
			rig := stepAuthoritySpecFixture(t, schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{
				{Name: "first", Steps: []schema.Step{{Name: "call", Uses: "workflow/call"}, {Name: "checkpoint", Uses: "script"}}, Gate: &schema.Gate{Require: required}},
				{Name: "final", Steps: []schema.Step{{Name: "accept", Uses: "approval"}}},
			}})
			ctx := t.Context()
			parent, err := readRun(ctx, rig.DB, rig.RunID)
			if err != nil {
				t.Fatal(err)
			}
			rootSteps, err := readSteps(ctx, rig.DB, parent.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, step := range rootSteps {
				if step.Name == "checkpoint" {
					attempt, err := newStepAttempts(rig.DB, func(string) bool { return true }).start(ctx, step, step.Type, ids.Nil)
					if err != nil {
						t.Fatal(err)
					}
					outcome, err := (CheckEvidence{Checks: []CheckReceipt{{Check: "checkpoint", Verdict: VerdictPass}}}).Outcome(StatusPass, nil)
					if err != nil {
						t.Fatal(err)
					}
					if err := newStepAttempts(rig.DB, func(string) bool { return true }).finish(ctx, attempt, outcome); err != nil {
						t.Fatal(err)
					}
				}
			}
			admissions := newRunAdmissions(rig.DB, NewStages(nil, func(string) bool { return false }), nil)
			child, err := admissions.records.create(ctx, StartRequest{ProjectID: rig.ProjectID, ChangeID: rig.ChangeID, WorkflowName: "fix", InputsJSON: "{}", parentRunID: parent.ID, parentStepID: rig.StepID, requestedBy: "apptown-system"}, PreparedRun{Workflow: DefinitionRef{Name: "fix", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "check", Steps: []schema.Step{{Name: "observe", Uses: "tests"}, {Name: "required", Uses: "script"}}, Gate: &schema.Gate{Require: []string{"required"}}}}}})
			if err != nil {
				t.Fatal(err)
			}
			if err := admissions.records.prepared(ctx, child.ID); err != nil {
				t.Fatal(err)
			}
			steps, err := readSteps(ctx, rig.DB, child.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, step := range steps {
				attempt, err := (newStepAttempts(rig.DB, func(string) bool { return true })).start(ctx, step, step.Type, ids.Nil)
				if err != nil {
					t.Fatal(err)
				}
				status, verdict := StatusPass, VerdictPass
				if step.Name == "observe" && failed {
					status, verdict = StatusFail, VerdictFail
				}
				outcome, err := (CheckEvidence{Checks: []CheckReceipt{{Check: step.Name, Verdict: verdict}}}).Outcome(status, nil)
				if err != nil {
					t.Fatal(err)
				}
				if err := (newStepAttempts(rig.DB, func(string) bool { return true })).finish(ctx, attempt, outcome); err != nil {
					t.Fatal(err)
				}
			}
			completion := completionRecorderFunc(func(context.Context, *database.Tx, CompletedRun) error { return nil })
			if err := (advanceStore{db: rig.DB, completion: completion}).complete(ctx, child); err != nil {
				t.Fatal(err)
			}
			parent, err = readRun(ctx, rig.DB, parent.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := (advanceStore{db: rig.DB, stages: NewStages(nil, func(string) bool { return false })}).transition(ctx, parent, "final", "apptown-system"); err != nil {
				t.Fatal(err)
			}
			parent, err = readRun(ctx, rig.DB, parent.ID)
			if err != nil {
				t.Fatal(err)
			}
			why, err := standingEvidence(ctx, rig.DB, parent, false, 0)
			if err != nil || (why == "") == failed {
				t.Fatalf("child evidence missing: %q %v", why, err)
			}
		})
	}
}

type standingQueuedFixture struct{ keys []string }

func (q *standingQueuedFixture) EnqueueStandingAcceptanceIn(_ context.Context, _ *database.Tx, key string) error {
	q.keys = append(q.keys, key)
	return nil
}
func rigRun(t *testing.T, rig *stepAuthorityTestRig) *Run {
	t.Helper()
	run, err := readRun(t.Context(), rig.DB, rig.RunID)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func TestInstanceStandingRefusesAnIssuerWhoLostLandingAuthority(t *testing.T) {
	a, rules, _, owner, _ := standingFixture(t)
	rules.deps.Portfolio = &standingPortfolioFixture{}
	pid, err := database.Transact(t.Context(), a.DB, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "issuer-check", Name: "Issuer check"})
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, spec := range []identity.Spec{
		{SubjectType: "user", Subject: "owner", Capability: string(access.CapInstanceStandingApproval), Scope: access.ScopeInstance, By: "boss"},
		{SubjectType: "user", Subject: "owner", Role: access.RoleProjectApprover, Scope: access.ScopeProject, ScopeID: pid.String(), By: "boss"},
	} {
		if _, err := a.Access.Grant(t.Context(), spec, ""); err != nil {
			t.Fatal(err)
		}
	}
	grant, err := a.Access.Grant(t.Context(), identity.Spec{SubjectType: "user", Subject: "owner", Capability: string(access.CapChangeLand), Scope: access.ScopeProject, ScopeID: pid.String(), By: "boss"}, "")
	if err != nil {
		t.Fatal(err)
	}
	in := StandingDefinition{Scope: "instance", Schedules: []standingSchedule{{Workflow: "test", Crons: map[string]string{"routine": "0 6 * * 1", "aggressive": "0 6 * * *"}}}, Categories: StandingCategories{Dependencies: "routine"}, Delivery: "full"}
	enableStanding(t, rules, owner, in)
	if rule, _, err := rules.StandingForWorkflow(t.Context(), pid, "test"); err != nil || rule.RuleID.IsZero() {
		t.Fatalf("current issuer refused: %+v %v", rule, err)
	}
	if _, err := a.Access.Revoke(t.Context(), grant, "boss"); err != nil {
		t.Fatal(err)
	}
	if rule, _, err := rules.StandingForWorkflow(t.Context(), pid, "test"); err != nil || !rule.RuleID.IsZero() {
		t.Fatalf("revoked land authority was reused: %+v %v", rule, err)
	}
}

func mustStandingStep(t *testing.T, db *database.DB, id ids.ID) *Step {
	t.Helper()
	step, err := readStep(t.Context(), db, id)
	if err != nil {
		t.Fatal(err)
	}
	return step
}

// This path uses the real Git driver: a checked application repair receives a
// standing vote, carries to its landing, and merges with recorded attribution.
func standingCheckedCodeLanding(t *testing.T, rig *stepAuthorityTestRig, rules StandingRules, ctx context.Context, source Source, dir, folder, tip, mode string) {
	t.Helper()
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
	completed := completionRecorderFunc(func(context.Context, *database.Tx, CompletedRun) error { return nil })
	work := rigRun(t, rig)
	if err := (advanceStore{db: rig.DB, stages: rig.Work.stages, completion: completed}).complete(ctx, work); err != nil {
		t.Fatal(err)
	}
	proc, err := rules.deps.Procedures.StandingProcedure(ctx, rig.DB, rig.ProjectID, "test")
	if err != nil {
		t.Fatal(err)
	}
	spec := schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{
		{Name: "approval", Steps: []schema.Step{{Name: "accept", Uses: "approval", With: map[string]any{"standing": map[string]any{"class": "routine-update", "means": proc.Landing.Means}}}}},
		{Name: "ship", Steps: []schema.Step{{Name: "record", Uses: "script"}}},
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
	owner := landings{db: rig.DB, standing: &rules, eligible: anyoneMayApprove}
	if _, err := owner.apply(ctx, work.ID, landing.ID); err != nil {
		t.Fatal(err)
	}
	if cast, err := owner.carry(ctx, landing); err != nil || !cast {
		t.Fatalf("checked code landing did not inherit: %t %v", cast, err)
	}
	landing, err = readRun(ctx, rig.DB, landing.ID)
	if err != nil {
		t.Fatal(err)
	}
	locks := &scm.RepositoryLocks{}
	service := scm.New(rig.DB, NewSCMWork(rig.DB, dir, policyLawRules{}, nil), locks, nil)
	armed, revoked := false, false
	claimLaws := reviewClaimLaws{before: func() {
		if !armed {
			return
		}
		armed = false
		rows, err := rules.List(ctx, rig.ProjectID)
		if err != nil || len(rows) != 1 {
			t.Fatalf("rules: %+v %v", rows, err)
		}
		revokeCtx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		_, err = rules.Disable(revokeCtx, rows[0].Rule.RuleID, rows[0].Rule.Revision)
		revoked = err == nil
	}}
	driver := standingClaimDriver{Driver: (scm.GitDrivers{DataDir: dir}).Driver(), prepared: func() {
		armed = mode == "revoke within claim"
		switch mode {
		case "routine at merge":
			if err := rules.SetProjectChoice(ctx, rig.ProjectID, "routine", "reduce update level before effect"); err != nil {
				t.Fatal(err)
			}
		case "off at merge":
			if err := rules.SetProjectChoice(ctx, rig.ProjectID, "off", "pause before effect"); err != nil {
				t.Fatal(err)
			}
		case "hold at merge":
			rules.deps.Portfolio.(*standingPortfolioFixture).hold = "release failed during preparation"
		case "disabled at merge":
			rows, err := rules.List(ctx, rig.ProjectID)
			if err != nil || len(rows) != 1 {
				t.Fatalf("rules: %+v %v", rows, err)
			}
			if _, err := rules.Disable(ctx, rows[0].Rule.RuleID, rows[0].Rule.Revision); err != nil {
				t.Fatal(err)
			}
		}
	}}
	merges := NewMerges(rig.DB, dir, claimLaws, service, mergeEffectsTest{driver: driver, observed: func(scm.Result) {}})
	merges.standing = &rules
	result, err := merges.Merge(ctx, landing)
	if revoked && result.Allowed {
		rows, readErr := rules.List(ctx, rig.ProjectID)
		if readErr != nil {
			t.Fatal(readErr)
		}
		t.Fatalf("merge succeeded after revocation committed inside its claim: allowed=%t enabled=%t main=%s candidate=%s", result.Allowed, rows[0].Rule.Enabled, gittest.Run(t, hub, "rev-parse", "main"), tip)
	}
	if strings.HasSuffix(mode, "at merge") {
		if err != nil || result.Allowed || result.Blocking == nil {
			t.Fatalf("claim ignored changed consent or health: %+v %v", result, err)
		}
		if got := gittest.Run(t, hub, "rev-parse", "main"); got == tip {
			t.Fatal("refused repair landed")
		}
		if mode == "disabled at merge" || mode == "routine at merge" {
			return
		} // Re-enabling is a new revision and needs new acceptance.
		if mode == "off at merge" {
			if err := rules.SetProjectChoice(ctx, rig.ProjectID, "routine", "resume after recovery"); err != nil {
				t.Fatal(err)
			}
		} else {
			rules.deps.Portfolio.(*standingPortfolioFixture).hold = ""
		}
		landing, err = readRun(ctx, rig.DB, landing.ID)
		if err != nil {
			t.Fatal(err)
		}
		// The refused attempt left a merge-result gate. It cannot require its
		// own future success as a precondition for this new effect.
		driver.prepared = func() {}
		merges.effects = mergeEffectsTest{driver: driver, observed: func(scm.Result) {}}
		result, err = merges.Merge(ctx, landing)
	}
	if err != nil || !result.Allowed {
		t.Fatalf("checked code did not land: %+v %v", result, err)
	}
	merged := gittest.Run(t, hub, "log", "-1", "--format=%B", "main")
	if !strings.Contains(merged, "the janitor, under standing approval") || !strings.Contains(merged, "(rev 1), granted by boss") {
		t.Fatalf("merge lost standing attribution: %s", merged)
	}
	if got := gittest.Run(t, hub, "show", "main:app.go"); !strings.Contains(got, "repaired") {
		t.Fatalf("application repair not landed: %s", got)
	}
}

type standingClaimDriver struct {
	scm.Driver
	prepared func()
}

func (d standingClaimDriver) Prepare(ctx context.Context, source scm.Source, previous string) (scm.Prepared, error) {
	plan, err := d.Driver.Prepare(ctx, source, previous)
	if err == nil {
		d.prepared()
	}
	return plan, err
}

// The current-law read is after standing eligibility in the final claim.
// Revoke through the owner while that transaction still holds project/run locks.
type reviewClaimLaws struct{ before func() }

func (r reviewClaimLaws) ViolationsIn(ctx context.Context, q database.Queryer, pid ids.ID, spec *schema.Spec) ([]LawViolation, error) {
	r.before()
	return (policyLawRules{}).ViolationsIn(ctx, q, pid, spec)
}

// Pack consent is independent of dependency levels and declarations. Release
// health and the project's off choice still apply at every use.
func TestKeepPacksCurrentHasItsOwnCategoryAndEligibility(t *testing.T) {
	a, s, _, _, _ := standingFixture(t)
	ctx := stepVerifiedContext(t.Context(), testAdmittedPrincipal(t, a.Access, "boss"))
	portfolio := &standingPortfolioFixture{uncovered: true}
	s.deps.Portfolio = portfolio
	in := StandingDefinition{Scope: "instance", Delivery: "full", Categories: StandingCategories{WaysOfWorking: true}, Schedules: []standingSchedule{{Workflow: "test"}}}
	rule := enableStanding(t, s, ctx, in)
	if rule.Question.Definition.Categories.Dependencies != "" || !strings.Contains(rule.Question.Shown, "newly published") || !strings.Contains(rule.Question.Shown, "not applicable") {
		t.Fatalf("pack meaning: %+v", rule.Question)
	}
	eligible := func(want bool) {
		t.Helper()
		got, _, err := s.StandingForWorkflow(ctx, a.ProjectID, "test")
		if err != nil || (!got.RuleID.IsZero()) != want {
			t.Fatalf("eligible=%t: %+v %v", want, got, err)
		}
	}
	eligible(true)
	portfolio.hold = "release 2 failed"
	eligible(false)
	portfolio.hold = ""
	if err := s.SetProjectChoice(ctx, a.ProjectID, "off", "pause packs"); err != nil {
		t.Fatal(err)
	}
	eligible(false)
	if err := s.SetProjectChoice(ctx, a.ProjectID, "aggressive", "resume packs"); err != nil {
		t.Fatal(err)
	}
	eligible(true)
	mixed := in
	mixed.Categories.Dependencies = "routine"
	if _, err := s.Preview(ctx, mixed); err == nil {
		t.Fatal("packs became a dependency level")
	}
}

// A standing decision withheld over a receipt that is not met stays recorded
// for that retry revision; running the check step again through the
// ordinary retry moves the revision, and the fresh evidence gets a fresh
// decision under current authority. This is how a Change withheld over a
// skipped standing-only check recovers once that check records
// not-applicable with its reason: no reconciliation of its own.
func TestRetryingAWithheldCheckGetsAFreshStandingDecision(t *testing.T) {
	const meaning = "declared fix in range and every check met"
	memory := CheckReceipt{Block: "deploy/memory-declared", Version: 8, Check: "memory-within", Evaluator: "runtime", Verdict: VerdictSkipped, Detail: "judged on the standing deployment in reviews, not on a preview"}
	// The check's runner as it now is: the standing-only check does not
	// apply to a change's run, with its reason.
	fixed := memory
	fixed.Verdict, fixed.Detail = VerdictNotApplicable, "judges the standing main deployment's running use, in reviews; a change's run has no standing deployment to judge"
	runner := func(e *Execution) {
		if _, err := e.Claim(); err != nil {
			return
		}
		e.Settle((CheckEvidence{Checks: []CheckReceipt{fixed}}).Outcome(StatusPass, nil))
	}
	rig := stepAuthorityRunnerFixture(t, schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "adopt", Steps: []schema.Step{{Name: "guidance-conformance", Uses: "tests"}, {Name: "accept", Uses: "approval", With: map[string]any{"standing": map[string]any{"class": "routine-update", "means": meaning}}}}}}}, map[string]Runner{"tests": runner})
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
	gittest.Run(t, folder, "init", "-q", "-b", source.Branch)
	gittest.RunAsTester(t, folder, "commit", "-q", "--allow-empty", "-m", "base")
	gittest.Run(t, folder, "branch", "main")
	gittest.RunAsTester(t, folder, "commit", "-q", "--allow-empty", "-m", "adopt the new pack")
	tip := gittest.Run(t, folder, "rev-parse", "HEAD")
	if _, err := database.Transact(ctx, rig.DB, func(tx *database.Tx) (scm.Authority, error) {
		return scm.AdvanceAuthorityIn(ctx, tx, rig.ProjectID, source.Branch, scm.RunHolder(rig.RunID))
	}); err != nil {
		t.Fatal(err)
	}
	checkStep := rig.StepID
	settle := func(receipt CheckReceipt) {
		t.Helper()
		attempts := newStepAttempts(rig.DB, func(string) bool { return true })
		attempt, err := attempts.start(ctx, mustStandingStep(t, rig.DB, checkStep), "tests", ids.Nil)
		if err != nil {
			t.Fatal(err)
		}
		outcome, err := (CheckEvidence{Checks: []CheckReceipt{receipt}}).Outcome(StatusPass, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := attempts.finish(ctx, attempt, outcome); err != nil {
			t.Fatal(err)
		}
	}
	// The receipt the run recorded before the fix.
	settle(memory)
	var accept ids.ID
	steps, err := readSteps(ctx, rig.DB, rig.RunID)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range steps {
		if step.Name == "accept" {
			accept = step.ID
		}
	}
	procedures := &standingProcedureFixture{means: meaning}
	rules := NewStandingRules(rig.DB, StandingDependencies{Identity: rig.Access, Authority: rig.Authorizer, Procedures: procedures, Portfolio: &standingPortfolioFixture{}, Presence: standingPresenceFixture{clear: true}, Inspector: standingInspectorFunc(func(context.Context, Source, StandingCategories) (StandingInspection, error) {
		return StandingInspection{Classification: "routine-dependency", Candidate: tip}, nil
	})})
	enableStanding(t, rules, ctx, StandingDefinition{Scope: "instance", Schedules: []standingSchedule{{Workflow: "test", Crons: map[string]string{"routine": "0 6 * * 1", "aggressive": "0 6 * * *"}}}, Categories: StandingCategories{Dependencies: "routine"}, Delivery: "full"})
	rig.Work.standing = &rules
	rig.Work.abandonments.locks = &scm.RepositoryLocks{}
	rig.Work.pins = landingPins{db: rig.DB, dataDir: dir}
	queue := &standingQueuedFixture{}
	rig.Work.standingRequests = queue
	decide := func() bool {
		t.Helper()
		if _, err := rig.Work.answerFromAcceptance(t.Context(), rigRun(t, rig)); err != nil || len(queue.keys) == 0 {
			t.Fatalf("standing acceptance was not queued: %v %+v", err, queue.keys)
		}
		result, err := rig.Work.RunStandingAcceptance(t.Context(), queue.keys[len(queue.keys)-1], nil)
		if err != nil {
			t.Fatal(err)
		}
		cast, _ := result["cast"].(bool)
		return cast
	}
	if decide() {
		t.Fatal("a skipped check was accepted")
	}
	// The withheld decision stands for that revision: asking again changes nothing.
	if _, err := rig.Work.answerFromAcceptance(t.Context(), rigRun(t, rig)); err != nil || len(queue.keys) != 1 {
		t.Fatalf("a considered decision was queued again: %v %+v", err, queue.keys)
	}
	if _, err := rig.Work.RetryStep(ctx, RetryCommand{StepID: checkStep}); err != nil {
		t.Fatal(err)
	}
	rig.Work.Wait()
	if !decide() || queue.keys[len(queue.keys)-1] != fmt.Sprintf("%s/%s/1", rig.RunID, accept) {
		t.Fatalf("no fresh standing decision for the retried evidence: %+v", queue.keys)
	}
	read, err := rig.Work.Step(ctx, accept)
	if err != nil {
		t.Fatal(err)
	}
	if ev, err := ReadApprovalEvidence(read.Step.Evidence); err != nil || len(ev.Decisions) != 1 || ev.Decisions[0].Standing == nil {
		t.Fatalf("standing vote missing: %s %v", read.Step.Evidence, err)
	}
}
