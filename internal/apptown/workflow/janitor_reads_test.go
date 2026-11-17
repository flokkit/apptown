package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/stock"
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
	s.janitorDefinitions = janitorReadDefinitions{}
	visible, err := s.authority.VisibleProjects(ctx, actionReadProject)
	if err != nil {
		t.Fatal(err)
	}
	receipt := adoption.EmptyReceipt()
	for _, name := range []string{"janitor-scan", "janitor-update", "janitor-adopt", "janitor-fix"} {
		receipt.Workflows = append(receipt.Workflows, adoption.ReceiptWorkflow{Name: name, Version: 1})
	}
	if err := s.db.InTx(ctx, func(tx *database.Tx) error {
		return adoption.New(nil, adoption.Dependencies{}).RecordIn(ctx, tx, visible[0], 1, adoption.AdoptionCreate, "fixture", receipt, "boss", time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordJanitorProjects(ctx); err != nil {
		t.Fatal(err)
	}
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
	in := JanitorChoices{Dependencies: "routine", SecurityFixes: true}
	rule := enableStanding(t, *rules, ctx, in)
	if _, err := rules.deps.Identity.EnsurePerson(t.Context(), "reader"); err != nil {
		t.Fatal(err)
	}
	if _, err := rules.deps.Identity.Grant(t.Context(), identity.Spec{SubjectType: "user", Subject: "reader", Capability: string(access.CapProjectRead), Scope: access.ScopeInstance, By: "boss"}, ""); err != nil {
		t.Fatal(err)
	}
	reader := stepVerifiedContext(t.Context(), testAdmittedPrincipal(t, rules.deps.Identity, "reader"))
	readable, err := rules.janitorChoices(reader)
	if err != nil || readable == nil || readable.RuleID != rule.RuleID {
		t.Fatalf("instance reader cannot read grant: %+v %v", readable, err)
	}

	second := workflowTestProject(t, s.db, "off-project", "Off project", "")
	// The fixture's authority models an instance reader; each operation still
	// goes through the production owner's permission check.
	s.authority = aggregateAuthority{operationAuthority: s.authority, read: func(context.Context, commandAction) ([]ids.ID, error) { return []ids.ID{pid, second}, nil }}
	if err := rules.SetProjectChoice(ctx, second, "off"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for i := 0; i < 3; i++ {
		janitorTry(t, s, pid, rule, now.Add(time.Duration(i)*time.Minute), true)
	}
	rows, _, _, err := s.JanitorProjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows=%+v", rows)
	}
	byID := map[ids.ID]JanitorProject{}
	for _, p := range rows {
		byID[p.ProjectID] = p
	}
	off := byID[second]
	if off.Level != "off" || off.Choice == nil || off.Choice.Person.Name != "boss" || off.Choice.At == "" {
		t.Fatalf("off=%+v", off)
	}
	stopped := byID[pid]
	if stopped.Queues[1].State != "stopped" || stopped.Queues[0].Stopped() {
		t.Fatalf("queues=%+v", stopped.Queues)
	}
	if stopped.State.Word != "needs you" {
		t.Fatal(stopped.State)
	}
	dbtest.FailNextStatement(t, s.db, "-- name: JanitorProjectChoices")
	if _, _, _, err := s.JanitorProjects(ctx); err == nil {
		t.Fatal("database failure became an empty answer")
	}
}

func TestJanitorFailedUpdateKeepsOriginalGrantAfterRevisionAndPurge(t *testing.T) {
	s, rules, ctx, repo := janitorReadFixture(t)
	visible, _ := s.authority.VisibleProjects(ctx, actionReadProject)
	pid := visible[0]
	definition := JanitorChoices{Dependencies: "routine", SecurityFixes: true}
	original := enableStanding(t, *rules, ctx, definition)
	ch := janitorTry(t, s, pid, original, time.Now().UTC(), true)
	gittest.Run(t, repo, "branch", ch.Branch)
	definition.Dependencies = "aggressive"
	revised, err := rules.Save(ctx, JanitorChoicesCommand{ExpectedRevision: original.Revision, Choices: definition})
	if err != nil {
		t.Fatal(err)
	}
	if revised.Revision == original.Revision {
		t.Fatal("rule did not change")
	}
	read, err := s.Change(t.Context(), ch.ID)
	if err != nil || read.Janitor == nil || read.Janitor.Rule == nil || read.Janitor.Rule.Kind != "Dependencies patch and minor, security fixes" || read.Janitor.Rule.Scope == "" || read.Janitor.RuleID != original.RuleID || read.Janitor.RuleRevision != original.Revision || read.Janitor.Issuer.Principal != original.Issuer.Principal {
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
	if len(attempts) != 1 || attempts[0].Rule == nil || attempts[0].Rule.Kind != "Dependencies patch and minor, security fixes" || attempts[0].Rule.Scope != read.Janitor.Rule.Scope || attempts[0].RuleID != original.RuleID || attempts[0].RuleRevision != original.Revision || attempts[0].RuleIssuer == nil || attempts[0].RuleIssuer.Principal != original.Issuer.Principal || attempts[0].RuleGrantedAt == nil || !attempts[0].RuleGrantedAt.Equal(original.At) {
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
	if err != nil || len(older.Changes) != 1 || older.Changes[0].Queue != "updates" {
		t.Fatalf("older=%+v %v", older, err)
	}
	s.authority = aggregateAuthority{operationAuthority: s.authority, read: func(context.Context, commandAction) ([]ids.ID, error) { return []ids.ID{}, nil }}
	hidden, err := s.LandedJanitorChanges(t.Context(), time.Unix(0, 0), now, 0, 20)
	if err != nil || len(hidden.Changes) != 0 {
		t.Fatalf("hidden=%+v %v", hidden, err)
	}
}

// Votes retain only the rule reference; labels must come from that revision's
// existing history, independently of the firing and the current rule head.

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
		if len(evidence.Decisions) != 1 || evidence.Decisions[0].Standing.Rule == nil || evidence.Decisions[0].Standing.Rule.Kind != "Dependencies patch and minor, security fixes, base images, toolchain patches" || evidence.Decisions[0].Standing.Rule.Scope == "" {
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

// Eligibility belongs to the scheduler; this read still lists both projects
// when the eligibility observer would fail for one of them.
func TestJanitorInstanceReadLeavesEligibilityToScheduler(t *testing.T) {
	s, rules, ctx, _ := janitorReadFixture(t)
	visible, _ := s.authority.VisibleProjects(ctx, actionReadProject)
	broken := workflowTestProject(t, s.db, "broken-project", "Broken project", "")
	s.authority = aggregateAuthority{operationAuthority: s.authority, read: func(context.Context, commandAction) ([]ids.ID, error) { return append(visible, broken), nil }}
	rules.deps.Portfolio.(*standingPortfolioFixture).failFor = broken
	rows, _, _, err := s.JanitorProjects(ctx)
	if err != nil || len(rows) != 2 {
		t.Fatalf("%+v %v", rows, err)
	}
}

type janitorReadDefinitions struct{}

func (janitorReadDefinitions) AdoptedWorkflows(_ context.Context, source adoption.Source, _ ...string) ([]adoption.AdoptedWorkflow, error) {
	var out []adoption.AdoptedWorkflow
	for _, pin := range source.Revision.Receipt.Workflows {
		body, err := stock.FS.ReadFile(pin.Name + ".yaml")
		out = append(out, adoption.AdoptedWorkflow{Name: pin.Name, Version: pin.Version, YAML: string(body), Err: err})
	}
	return out, nil
}

func TestJanitorProjectsClassifiesCompletedFacts(t *testing.T) {
	for _, tc := range []struct {
		name, word   string
		enabled, off bool
		failures     int
		active       bool
	}{
		{name: "off for project", word: "off", enabled: true, off: true},
		{name: "off", word: "off"},
		{name: "unknown", word: "not known", enabled: true},
		{name: "no repository", word: "no repository", enabled: true},
		{name: "up to date", word: "up to date", enabled: true},
		{name: "waiting", word: "needs you", enabled: true},
		{name: "stuck", word: "needs you", enabled: true, failures: 1},
		{name: "stopped", word: "needs you", enabled: true, failures: 3},
		{name: "updating", word: "working", enabled: true, active: true},
		{name: "stopped and updating", word: "needs you", enabled: true, failures: 3, active: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, rules, ctx, repo := janitorReadFixture(t)
			visible, err := s.authority.VisibleProjects(ctx, actionReadProject)
			if err != nil {
				t.Fatal(err)
			}
			pid := visible[0]
			var rule StandingRevision
			if tc.enabled {
				rule = enableStanding(t, *rules, ctx, JanitorChoices{Dependencies: "routine", SecurityFixes: true})
			}
			if tc.off {
				if err := rules.SetProjectChoice(ctx, pid, "off"); err != nil {
					t.Fatal(err)
				}
			}
			for i := 0; i < tc.failures; i++ {
				janitorTry(t, s, pid, rule, time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), true)
			}
			if tc.name == "no repository" {
				if err := project.New(s.db, nil).SetDefaultBranch(ctx, pid, ""); err != nil {
					t.Fatal(err)
				}
			}
			if tc.name == "up to date" || tc.name == "waiting" {
				attempts, err := s.Attempts(ctx, pid, "security", 10)
				if err != nil {
					t.Fatal(err)
				}
				ch, err := s.Change(ctx, attempts[0].ChangeID)
				if err != nil {
					t.Fatal(err)
				}
				recordObservations(t, s, ch, []conformance.ScanObservation{{RunID: ids.New(time.Now()), Subject: conformance.ScanSubject{On: "source", Commit: gittest.Run(t, repo, "rev-parse", "HEAD"), Tree: gittest.Run(t, repo, "rev-parse", "HEAD^{tree}")}, Scanner: "osv", Verdict: "pass", ObservedAt: time.Now().UTC()}, {RunID: ids.New(time.Now()), Subject: conformance.ScanSubject{On: "running"}, Verdict: "not_applicable", Reason: "no running images", ObservedAt: time.Now().UTC()}})
				if tc.name == "waiting" {
					admitAutomatedRun(t, s, ch, "approval")
				}
			}
			if tc.active {
				// The fixture's initial security change is a separate queue from updates.
				changes, err := s.Attempts(ctx, pid, "security", 10)
				if err != nil {
					t.Fatal(err)
				}
				ch, err := s.Change(ctx, changes[0].ChangeID)
				if err != nil {
					t.Fatal(err)
				}
				run := admitAutomatedRun(t, s, ch, "script")
				steps, err := s.RunSteps(ctx, run.ID)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := newStepAttempts(s.db, func(string) bool { return true }).start(ctx, steps[0], "script", pid); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.RecordJanitorProjects(ctx); err != nil {
				t.Fatal(err)
			}
			rows, _, _, err := s.JanitorProjects(ctx)
			if err != nil || len(rows) != 1 {
				t.Fatalf("rows=%+v: %v", rows, err)
			}
			if rows[0].State.Word != tc.word || rows[0].State.Unknown != (tc.name == "unknown") {
				t.Fatalf("state=%+v, want %s", rows[0].State, tc.word)
			}
		})
	}
}

func (janitorReadDefinitions) ScheduledWorkflowNamesIn(context.Context, database.Queryer, ids.ID) (map[string]bool, error) {
	return map[string]bool{}, nil
}

func TestFleetScanAndJanitorReadsOmitProjectsOutsideTheirOwnVisibility(t *testing.T) {
	t.Parallel()
	s, _, ctx, _ := janitorReadFixture(t)
	visible, err := s.authority.VisibleProjects(ctx, actionReadProject)
	if err != nil {
		t.Fatal(err)
	}
	shown := visible[0]
	hidden := workflowTestProject(t, s.db, "unreadable", "Unreadable", "")
	s.authority = aggregateAuthority{operationAuthority: s.authority, read: func(context.Context, commandAction) ([]ids.ID, error) { return []ids.ID{shown}, nil }}
	rows, _, _, err := s.JanitorProjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ProjectID != shown {
		t.Fatalf("Janitor visibility: %+v", rows)
	}
	scans, err := s.ScanAttention(ctx, []ids.ID{hidden, shown})
	if err != nil {
		t.Fatal(err)
	}
	if len(scans) != 1 || scans[0].ProjectID != shown {
		t.Fatalf("scan visibility: %+v", scans)
	}
}

func TestScanAttentionSharedObservationFailureRemainsUnknownForEachProject(t *testing.T) {
	t.Parallel()
	s, _, ctx, _ := janitorReadFixture(t)
	visible, err := s.authority.VisibleProjects(ctx, actionReadProject)
	if err != nil {
		t.Fatal(err)
	}
	dbtest.FailNextStatement(t, s.db, "-- name: JanitorRecords")
	rows, err := s.ScanAttention(ctx, visible)
	if err != nil || len(rows) != len(visible) {
		t.Fatalf("lost source coverage: %+v %v", rows, err)
	}
	for _, row := range rows {
		if row.Err == nil {
			t.Fatalf("failed source became known: %+v", row)
		}
	}
}

func TestJanitorKeepsUnassessedApprovalsUnknown(t *testing.T) {
	service, rules, ctx, _ := janitorReadFixture(t)
	rule := enableStanding(t, *rules, ctx, JanitorChoices{Dependencies: "routine"})
	procedures := rules.deps.Procedures.(*standingProcedureFixture)
	procedures.means = "changed meaning"
	failure := errors.New("procedure observation unavailable")
	procedures.err = failure
	rows, saved, unknown, err := service.JanitorProjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if saved == nil || !saved.Enabled || !slices.Equal(unknown, []ids.ID{rule.RuleID}) {
		t.Fatalf("unassessed approval must stay unknown: saved=%+v unknown=%v", saved, unknown)
	}
	if rows[0].State.Word != "not known" || !rows[0].State.Unknown || rows[0].State.Help != failure.Error() {
		t.Fatalf("project lost its eligibility failure: %+v", rows[0].State)
	}
	for _, result := range rows[0].Eligibility {
		if !slices.Equal(result.Unassessed, []ids.ID{rule.RuleID}) || !errors.Is(result.Err, failure) {
			t.Fatalf("unassessed workflow: %+v", result)
		}
	}
}

// History compares against all landed adoptions, including a person's adoption
// outside the requested history window, rather than the preceding Janitor row.
func TestJanitorLandedHistoryNamesOnlyMovedPacks(t *testing.T) {
	s, _, ctx, _ := janitorReadFixture(t)
	visible, err := s.authority.VisibleProjects(ctx, actionReadProject)
	if err != nil {
		t.Fatal(err)
	}
	project := visible[0]
	now := time.Now().UTC()
	record := func(at time.Time, queue string, revision int64, packs []adoption.PackPick, shipped bool) ids.ID {
		t.Helper()
		recordCtx := ctx
		if queue != "" {
			recordCtx = access.WithCaller(ctx, access.Caller{Verified: true, Principal: access.Principal{Kind: "instrument", Name: "system", Instrument: access.SystemInstrument, Via: "schedule", ProjectID: project}})
		}
		created, err := database.Transact(ctx, s.db, func(tx *database.Tx) (CreatedChange, error) {
			ch, err := CreateChangeIn(recordCtx, tx, ChangeDraft{ProjectID: project, Type: "uplift", Title: "Adopt packs", Queue: queue}, "boss", at)
			if err != nil {
				return ch, err
			}
			target, err := (testTarget{digest: fmt.Sprint(ch.Change.ID)}).RecordTargetIn(ctx, tx, TargetChange{ProjectID: project, ChangeID: ch.Change.ID, Actor: "boss"})
			if err != nil {
				return ch, err
			}
			raw, err := json.Marshal(adoption.Candidate{Packs: packs})
			if err != nil {
				return ch, err
			}
			if _, err := workflowsql.New(tx.SQL()).AttachUpliftTarget(ctx, workflowsql.AttachUpliftTargetParams{ID: ch.Change.ID, ProjectID: project, TargetAdoptionID: target.ID, TargetCandidateJson: raw}); err != nil {
				return ch, err
			}
			action := ShipChange
			if !shipped {
				action = abandonChange
			}
			if _, err := TransitionChangeIn(ctx, tx, project, ch.Change.ID, ChangeMove{Action: action}, at); err != nil {
				return ch, err
			}
			if shipped {
				err = StampUpliftAdoptionIn(ctx, tx, UpliftAdoption{Change: ChangeRef{ID: ch.Change.ID, ProjectID: project}, Revision: revision})
			}
			return ch, err
		})
		if err != nil {
			t.Fatal(err)
		}
		return created.Change.ID
	}
	first := record(now.Add(-10*time.Hour), "packs", 1, []adoption.PackPick{{Pack: "apptown", Version: 1}, {Pack: "web", Version: 1}}, true)
	record(now.Add(-9*time.Hour), "", 2, []adoption.PackPick{{Pack: "apptown", Version: 2}, {Pack: "web", Version: 2}}, true)
	record(now.Add(-8*time.Hour), "packs", 3, []adoption.PackPick{{Pack: "apptown", Version: 99}, {Pack: "web", Version: 99}}, false)
	second := record(now.Add(-time.Hour), "packs", 3, []adoption.PackPick{{Pack: "apptown", Version: 3}, {Pack: "web", Version: 2}, {Pack: "added", Version: 1}}, true)
	history, err := s.LandedJanitorChanges(ctx, now.Add(-2*time.Hour), now, 0, 20)
	if err != nil || len(history.Changes) != 1 {
		t.Fatalf("history: %+v %v", history, err)
	}
	got := history.Changes[0]
	if got.ChangeID != second || len(got.PackVersions) != 2 || got.PackVersions[0].Pack != "apptown" || got.PackVersions[1].Pack != "added" {
		t.Fatalf("second: %+v", got)
	}
	dbtest.FailNextStatement(t, s.db, "-- name: PreviousLandedAdoption")
	if _, err := s.LandedJanitorChanges(ctx, now.Add(-2*time.Hour), now, 0, 20); err == nil {
		t.Fatal("previous-adoption failure was hidden")
	}

	history, err = s.LandedJanitorChanges(ctx, now.Add(-11*time.Hour), now.Add(-9*time.Hour), 0, 20)
	if err != nil || len(history.Changes) != 1 || history.Changes[0].ChangeID != first || len(history.Changes[0].PackVersions) != 2 {
		t.Fatalf("first: %+v %v", history, err)
	}
}

func TestJanitorProjectsFollowInstanceAfterProjectReturns(t *testing.T) {
	service, rules, ctx, _ := janitorReadFixture(t)
	visible, err := service.authority.VisibleProjects(ctx, actionReadProject)
	if err != nil {
		t.Fatal(err)
	}
	pid := visible[0]
	for _, level := range []string{"off", "instance"} {
		if err := rules.SetProjectChoice(ctx, pid, level); err != nil {
			t.Fatal(err)
		}
		for _, instance := range []string{"aggressive", "routine", "off"} {
			enableStanding(t, *rules, ctx, JanitorChoices{Dependencies: instance, Packs: true})
			projects, _, _, err := service.JanitorProjects(ctx)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, p := range projects {
				if p.ProjectID == pid {
					found = true
					want := instance
					if level == "off" {
						want = "off"
					}
					if p.Level != want || (p.Choice == nil) != (level == "instance") {
						t.Fatalf("%s / %s: %+v", level, instance, p)
					}
				}
			}
			if !found {
				t.Fatal("project missing")
			}
		}
	}
}
