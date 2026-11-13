package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"github.com/flokkit/apptown/internal/ids"
)

func janitorReadFixture(t *testing.T) (*Service, *StandingRules, context.Context, string) {
	t.Helper()
	s, _, repo, _, _ := automatedFixture(t)
	directory := identity.New(s.db, identity.Config{Projects: project.NewCatalogue(s.db)})
	if err := directory.SeedAdministrator(t.Context(), "boss"); err != nil {
		t.Fatal(err)
	}
	projects := project.New(s.db, identity.Scoped(directory, "project"))
	auth := access.Checked{Authority: directory, Projects: projects}
	s.checked = auth
	rules := NewStandingRules(s.db, StandingDependencies{Authority: auth, Identity: directory, Procedures: &standingProcedureFixture{means: "accept qualified changes"}, Portfolio: &standingPortfolioFixture{}})
	s.standing = &rules
	principal := testAdmittedPrincipal(t, directory, "boss")
	ctx := stepVerifiedContext(t.Context(), principal)
	return s, &rules, ctx, repo
}

func janitorTry(t *testing.T, s *Service, pid ids.ID, rule StandingRevision, at time.Time, fail bool) *Change {
	t.Helper()
	ctx := access.WithCaller(t.Context(), access.Caller{Verified: true, Principal: access.Principal{Kind: "instrument", Name: "system", Instrument: access.SystemInstrument, Via: "schedule", ProjectID: pid}})
	created, err := database.Transact(ctx, s.db, func(tx *database.Tx) (CreatedChange, error) {
		return CreateChangeIn(ctx, tx, ChangeDraft{ProjectID: pid, Type: "scheduled", Title: "Update dependencies", Queue: "updates", Schedule: "janitor-update", RuleID: rule.RuleID, RuleRevision: rule.Revision}, "apptown-system", at)
	})
	if err != nil {
		t.Fatal(err)
	}
	ch := created.Change
	if fail {
		run := admitAutomatedRun(t, s, ch, "fix")
		steps, err := s.RunSteps(t.Context(), run.ID)
		if err != nil {
			t.Fatal(err)
		}
		e := &Execution{service: s, ctx: t.Context(), step: steps[0], run: run}
		claim, err := newStepAttempts(s.db, func(string) bool { return true }).start(t.Context(), steps[0], "fix", pid)
		if err != nil {
			t.Fatal(err)
		}
		e.claim = claim
		if err := e.StartFix(nil); err != nil {
			t.Fatal(err)
		}
		outcome, err := EncodeOutcome(StatusFail, map[string]any{"reason": "package registry unavailable"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := (reworkStore{db: s.db}).fail(t.Context(), e.claim, outcome); err != nil {
			t.Fatal(err)
		}
	}
	return ch
}

func TestJanitorInstanceReadKeepsQueuesChoicesAndReaderAuthority(t *testing.T) {
	s, rules, ctx, _ := janitorReadFixture(t)
	idsVisible, err := s.authority.VisibleProjects(ctx, actionReadProject)
	if err != nil {
		t.Fatal(err)
	}
	pid := idsVisible[0]
	in := StandingDefinition{Scope: "instance", Schedules: []standingSchedule{{Workflow: "janitor-update", Crons: map[string]string{"routine": "0 6 * * 1", "aggressive": "0 6 * * *"}}, {Workflow: "janitor-scan", Crons: map[string]string{"routine": "0 5 * * *", "aggressive": "0 5 * * *"}}}, Categories: StandingCategories{Dependencies: "routine", SecurityFixes: true}, Delivery: "full"}
	rule := enableStanding(t, *rules, ctx, in)
	if _, err := rules.deps.Identity.EnsurePerson(t.Context(), "reader"); err != nil {
		t.Fatal(err)
	}
	if _, err := rules.deps.Identity.Grant(t.Context(), identity.Spec{SubjectType: "user", Subject: "reader", Capability: string(access.CapProjectRead), Scope: access.ScopeInstance, By: "boss"}, ""); err != nil {
		t.Fatal(err)
	}
	reader := stepVerifiedContext(t.Context(), testAdmittedPrincipal(t, rules.deps.Identity, "reader"))
	readable, err := rules.JanitorRules(reader)
	if err != nil || len(readable) != 1 || readable[0].RuleID != rule.RuleID {
		t.Fatalf("instance reader cannot read grant: %+v %v", readable, err)
	}

	second := workflowTestProject(t, s.db, "off-project", "Off project", "")
	// The fixture's authority models an instance reader; each operation still
	// goes through the production owner's permission check.
	s.authority = aggregateAuthority{operationAuthority: s.authority, read: func(context.Context, commandAction) ([]ids.ID, error) { return []ids.ID{pid, second}, nil }}
	if err := rules.SetProjectChoice(ctx, second, "off", "Waiting for the next release"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for i := 0; i < 3; i++ {
		janitorTry(t, s, pid, rule, now.Add(time.Duration(i)*time.Minute), true)
	}
	rows, err := s.JanitorProjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows=%+v", rows)
	}
	byID := map[ids.ID]janitorProject{}
	for _, p := range rows {
		byID[p.ProjectID] = p
	}
	off := byID[second]
	if off.Level != "off" || off.Choice == nil || off.Choice.Person.Name != "boss" || off.Choice.At == "" || off.Choice.Reason != "Waiting for the next release" {
		t.Fatalf("off=%+v", off)
	}
	stopped := byID[pid]
	if stopped.Queues[1].State != "stopped" || stopped.Queues[0].Stopped() {
		t.Fatalf("queues=%+v", stopped.Queues)
	}
	if !strings.Contains(strings.Join(stopped.QueueSentences, " "), "Weekly updates stopped after 3 failed tries. Security fixes can still run.") {
		t.Fatal(stopped.QueueSentences)
	}
	dbtest.FailNextStatement(t, s.db, "-- name: JanitorProjectChoices")
	if _, err := s.JanitorProjects(ctx); err == nil {
		t.Fatal("database failure became an empty answer")
	}
}

func TestJanitorFailedUpdateKeepsOriginalGrantAfterRevisionAndPurge(t *testing.T) {
	s, rules, ctx, repo := janitorReadFixture(t)
	visible, _ := s.authority.VisibleProjects(ctx, actionReadProject)
	pid := visible[0]
	definition := StandingDefinition{Projects: []StandingProject{{ProjectID: pid, Workflow: "janitor-update"}}, Categories: StandingCategories{Dependencies: "routine", SecurityFixes: true}, Delivery: "land-only"}
	original := enableStanding(t, *rules, ctx, definition)
	ch := janitorTry(t, s, pid, original, time.Now().UTC(), true)
	gittest.Run(t, repo, "branch", ch.Branch)
	definition.Categories.Dependencies = "aggressive"
	q, err := rules.Preview(ctx, definition)
	if err != nil {
		t.Fatal(err)
	}
	revised, err := rules.Enable(ctx, StandingCommand{RuleID: original.RuleID, ExpectedRevision: original.Revision, Definition: definition, ShownDigest: q.Digest})
	if err != nil {
		t.Fatal(err)
	}
	if revised.Revision == original.Revision {
		t.Fatal("rule did not change")
	}
	read, err := s.Change(t.Context(), ch.ID)
	if err != nil || read.Janitor == nil || read.Janitor.Rule == nil || read.Janitor.Rule.Kind != "Dependency updates" || read.Janitor.Rule.Scope == "" || read.Janitor.RuleID != original.RuleID || read.Janitor.RuleRevision != original.Revision || read.Janitor.Issuer.Principal != original.Issuer.Principal {
		t.Fatalf("Change lost its original firing authority: %+v %v", read, err)
	}

	if _, err := s.Abandon(t.Context(), StopWork{ChangeID: ch.ID, Reason: "replaced"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PurgeAutomatedChanges(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Change(t.Context(), ch.ID); err == nil {
		t.Fatal("untouched Change was not purged")
	}
	attempts, err := s.Attempts(t.Context(), pid, "updates", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 || attempts[0].Rule == nil || attempts[0].Rule.Kind != "Dependency updates" || attempts[0].Rule.Scope != read.Janitor.Rule.Scope || attempts[0].RuleID != original.RuleID || attempts[0].RuleRevision != original.Revision || attempts[0].RuleIssuer == nil || attempts[0].RuleIssuer.Principal != original.Issuer.Principal || attempts[0].RuleGrantedAt == nil || !attempts[0].RuleGrantedAt.Equal(original.EnabledAt) {
		t.Fatalf("original grant lost: %+v", attempts)
	}
}

func TestJanitorLandedHistoryWindowsPagingAndVisibility(t *testing.T) {
	s, _, _, _ := janitorReadFixture(t)
	visible, _ := s.authority.VisibleProjects(t.Context(), actionReadProject)
	pid := visible[0]
	now := time.Now().UTC()
	week := now.Add(-7 * 24 * time.Hour)
	for i, at := range []time.Time{now.Add(-time.Hour), now.Add(-time.Hour), now.Add(-8 * 24 * time.Hour), now.Add(-time.Minute)} {
		ch := janitorTry(t, s, pid, StandingRevision{}, at, false)
		if err := s.db.InTx(t.Context(), func(tx *database.Tx) error {
			_, err := TransitionChangeIn(t.Context(), tx, pid, ch.ID, ChangeMove{Action: ShipChange}, at)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if i < 3 {
			// sql-guarantee: This read fixture supplies landed metadata at fixed
			// historical times. The cross-owner pack test exercises actual SCM
			// and adoption settlement before reading the same history.
			if _, err := workflowsql.New(s.db.SQL()).SetChangeLandedSHA(t.Context(), workflowsql.SetChangeLandedSHAParams{ChangeID: ch.ID, ProjectID: pid, LandedSha: "recorded-landing"}); err != nil {
				t.Fatal(err)
			}
		}
		t.Logf("landed %d: %s", i, ch.ID)
	}
	first, err := s.LandedJanitorChanges(t.Context(), week, now, 0, 1)
	if err != nil || len(first.Changes) != 1 || !first.More {
		t.Fatalf("first=%+v %v", first, err)
	}
	next, err := s.LandedJanitorChanges(t.Context(), week, now, 1, 1)
	if err != nil || len(next.Changes) != 1 || next.More || first.Changes[0].ChangeID == next.Changes[0].ChangeID {
		t.Fatalf("next=%+v %v", next, err)
	}
	older, err := s.LandedJanitorChanges(t.Context(), time.Unix(0, 0), week, 0, 20)
	if err != nil || len(older.Changes) != 1 || older.Changes[0].Schedule != "janitor-update" {
		t.Fatalf("older=%+v %v", older, err)
	}
	s.authority = aggregateAuthority{operationAuthority: s.authority, read: func(context.Context, commandAction) ([]ids.ID, error) { return []ids.ID{}, nil }}
	hidden, err := s.LandedJanitorChanges(t.Context(), time.Unix(0, 0), now, 0, 20)
	if err != nil || len(hidden.Changes) != 0 {
		t.Fatalf("hidden=%+v %v", hidden, err)
	}
}

func TestJanitorRepairSentenceReadsDefinition(t *testing.T) {
	for _, rounds := range []int{1, 4} {
		doc, err := schema.ParseWorkflowStored([]byte(fmt.Sprintf("apiVersion: apptown.dev/v1\nkind: Workflow\nmetadata: {name: janitor-update}\nspec:\n  kind: pipeline\n  stages:\n    - name: fix\n      steps:\n        - name: repair\n          uses: agent/pi\n          with: {repairRounds: %d}\n", rounds)))
		if err != nil {
			t.Fatal(err)
		}
		got := repairProcedure("janitor-update", 9, doc)
		if got.RepairRounds != rounds || !strings.Contains(got.Sentence, fmt.Sprintf("up to %d repair sessions", rounds)) || !strings.Contains(got.Sentence, "asks a person") {
			t.Fatal(got)
		}
	}
}

func TestJanitorNeverSaysSecurityCanRunWhenItIsOffOrStopped(t *testing.T) {
	queues := []QueueStatus{{Queue: "updates", Failed: queueStopAfter}, {Queue: "security", Failed: queueStopAfter}}
	if got := strings.Join(janitorQueueSentences(queues, "routine", true), " "); strings.Contains(got, "can still run") {
		t.Fatal(got)
	}
	queues[1].Failed = 0
	if got := strings.Join(janitorQueueSentences(queues, "off", false), " "); strings.Contains(got, "can still run") {
		t.Fatal(got)
	}
}

func TestJanitorQueueStopRulesUseAdmissionBound(t *testing.T) {
	for _, tc := range []struct{ workflow, level, want string }{
		{"janitor-fix", "routine", "Security fixes stop after %d failed tries."},
		{"janitor-update", "routine", "Weekly updates stop after %d failed tries; security fixes can still run."},
		{"janitor-update", "aggressive", "Nightly updates stop after %d failed tries; security fixes can still run."},
		{"janitor-adopt", "routine", "Pack updates stop after %d failed tries."},
	} {
		if got, want := janitorStopRule(tc.workflow, tc.level), fmt.Sprintf(tc.want, queueStopAfter); got != want {
			t.Errorf("%s/%s: %q, want %q", tc.workflow, tc.level, got, want)
		}
	}
}

// Votes retain only the rule reference; labels must come from that revision's
// existing history, independently of the firing and the current rule head.
func TestJanitorVoteReadsRecordedRuleKindAndDisplayScope(t *testing.T) {
	s, rules, ctx, _ := janitorReadFixture(t)
	visible, _ := s.authority.VisibleProjects(ctx, actionReadProject)
	pid := visible[0]
	definition := StandingDefinition{Projects: []StandingProject{{ProjectID: pid, Workflow: "janitor-update"}}, Categories: StandingCategories{Dependencies: "routine"}, Delivery: "land-only"}
	original := enableStanding(t, *rules, ctx, definition)
	definition.Categories = StandingCategories{WaysOfWorking: true}
	packs := enableStanding(t, *rules, ctx, definition)
	question, err := rules.Preview(ctx, definition)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rules.Enable(ctx, StandingCommand{RuleID: original.RuleID, ExpectedRevision: original.Revision, Definition: definition, ShownDigest: question.Digest}); err != nil {
		t.Fatal(err)
	}
	p, err := project.ByIDIn(ctx, s.db, pid)
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range []StandingRevision{original, packs} {
		raw := []byte(fmt.Sprintf(`{"decisions":[{"standing":{"ruleId":%q,"revision":%d}}],"other":"retained"}`, rule.RuleID, rule.Revision))
		step := &Step{Type: "approval", Evidence: rawJSON(string(raw))}
		if err := standingEvidenceIdentityIn(ctx, s.db, s.checked, step); err != nil {
			t.Fatal(err)
		}
		var got struct {
			Decisions []struct {
				Standing struct {
					Rule standingRuleIdentity `json:"rule"`
				} `json:"standing"`
			} `json:"decisions"`
			Other string `json:"other"`
		}
		if err := json.Unmarshal(step.Evidence, &got); err != nil {
			t.Fatal(err)
		}
		kind := "Dependency updates"
		if rule.RuleID == packs.RuleID {
			kind = "Keep packs current"
		}
		if got.Decisions[0].Standing.Rule.Kind != kind || got.Decisions[0].Standing.Rule.Scope != p.Name || got.Other != "retained" {
			t.Fatalf("wrong vote identity: %s", step.Evidence)
		}
	}
	instance, err := standingRuleIdentityIn(ctx, s.db, s.checked, StandingRevision{Question: StandingQuestion{Definition: StandingDefinition{Scope: "instance"}}})
	if err != nil || instance.Scope != "all projects" {
		t.Fatalf("instance identity: %+v %v", instance, err)
	}
}

func TestJanitorVoteIdentityOnChangeEvidenceReads(t *testing.T) {
	rig, rules, _, ctx, definition := standingFixture(t)
	rule := enableStanding(t, rules, ctx, definition)
	run, err := readRun(ctx, rig.DB, rig.RunID)
	if err != nil {
		t.Fatal(err)
	}
	step, err := readStep(ctx, rig.DB, rig.StepID)
	if err != nil {
		t.Fatal(err)
	}
	vote := ApprovalVote{Approver: "the janitor", Principal: rule.Issuer.Principal, Via: "standing", Decision: VoteApprove, Standing: &standingVote{RuleID: rule.RuleID, Revision: rule.Revision, Issuer: rule.Issuer}}
	if _, err := database.Transact(ctx, rig.DB, func(tx *database.Tx) (reworkedResult, error) {
		return (reworkStore{db: rig.DB}).approveIn(ctx, tx, run, step, vote)
	}); err != nil {
		t.Fatal(err)
	}
	check := func(step *Step) {
		t.Helper()
		evidence, err := ReadApprovalEvidence(step.Evidence)
		if err != nil {
			t.Fatal(err)
		}
		if len(evidence.Decisions) != 1 || evidence.Decisions[0].Standing.Rule == nil || evidence.Decisions[0].Standing.Rule.Kind != "Dependency updates" || evidence.Decisions[0].Standing.Rule.Scope == "" {
			t.Fatalf("missing recorded rule identity: %s", step.Evidence)
		}
	}
	observed, err := rig.Work.Step(ctx, rig.StepID)
	if err != nil {
		t.Fatal(err)
	}
	check(observed.Step)
	steps, err := rig.Work.RunSteps(ctx, rig.RunID)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range steps {
		if step.ID == rig.StepID {
			check(step)
		}
	}
	snapshot, err := rig.Work.Snapshot(ctx, rig.RunID, DetailView)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range snapshot.Steps {
		if step.ID == rig.StepID {
			check(step)
		}
	}
}
