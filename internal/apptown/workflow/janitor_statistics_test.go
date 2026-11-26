package workflow

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// Every outcome a closed attempt can carry has one column; an outcome the
// statistics do not know is refused, never counted in some column.
func TestJanitorStatisticsPlaceEveryClosedOutcomeOnce(t *testing.T) {
	ended := sql.NullTime{Time: time.Now(), Valid: true}
	for outcome, want := range map[string]string{
		ChangeShipped.String(): "shipped", "pass": "nothing",
		"held": janitorFailed, "faulted": janitorFailed, "retried": janitorFailed, StatusFail.String(): janitorFindingsLeft, StatusError.String(): janitorFailed,
		"skipped": "closed", ChangeAbandoned.String(): "closed",
	} {
		got, _, err := janitorColumn(workflowsql.JanitorAttemptStatsRow{Outcome: outcome, EndedAt: ended, Reason: "step: why"}, nil)
		if err != nil || got != want {
			t.Fatalf("%s: %q %v, want %q", outcome, got, err, want)
		}
	}
	if _, _, err := janitorColumn(workflowsql.JanitorAttemptStatsRow{Outcome: "", EndedAt: ended}, nil); err == nil {
		t.Fatal("a closed attempt with no outcome was placed")
	}
	if _, _, err := janitorColumn(workflowsql.JanitorAttemptStatsRow{Outcome: "mystery", EndedAt: ended}, nil); err == nil {
		t.Fatal("an unknown outcome was placed")
	}
}

// An open attempt's outcome is history; the recorded queue says whether a
// person is needed now, and only for the Change the queue is deciding.
func TestJanitorStatisticsOpenAttemptsFollowTheRecordedQueue(t *testing.T) {
	change, other := idstest.N(1), idstest.N(2)
	open := workflowsql.JanitorAttemptStatsRow{Queue: "updates", ActiveChangeID: change, Outcome: "held", Reason: "tests: failed"}
	for name, c := range map[string]struct {
		queues         []QueueStatus
		column, reason string
	}{
		"no record":          {nil, "running", ""},
		"updating":           {[]QueueStatus{{Queue: "updates", State: "updating", ChangeID: change}}, "running", ""},
		"another Change":     {[]QueueStatus{{Queue: "updates", State: "decide", ChangeID: other}}, "running", ""},
		"another queue":      {[]QueueStatus{{Queue: "packs", State: "decide", ChangeID: change}}, "running", ""},
		"check to accept":    {[]QueueStatus{{Queue: "updates", State: "decide", ChangeID: change, Decide: &queueDecision{Act: nextOverride}}}, janitorWaiting, "tests: failed"},
		"waiting acceptance": {[]QueueStatus{{Queue: "updates", State: "decide", ChangeID: change, Decide: &queueDecision{Act: NextApprove}}}, janitorWaiting, janitorWaitsAcceptance},
	} {
		column, reason, err := janitorColumn(open, c.queues)
		if err != nil || column != c.column || reason != c.reason {
			t.Fatalf("%s: %q %q %v", name, column, reason, err)
		}
	}
}

func statisticsFor(t *testing.T, s *Service, ctx context.Context, days int) janitorStatistics {
	t.Helper()
	got, err := s.JanitorStatistics(ctx, days)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func kindCounts(t *testing.T, stats janitorStatistics, kind string) janitorCounts {
	t.Helper()
	for _, k := range stats.Kinds {
		if k.Kind == kind {
			return k.Counts
		}
	}
	t.Fatalf("no %s row: %+v", kind, stats.Kinds)
	return janitorCounts{}
}

func waitingSteps(stats janitorStatistics, kind string) []string {
	var out []string
	for _, r := range stats.Reasons {
		if r.Kind == kind && r.Column == janitorWaiting {
			out = append(out, r.Step)
		}
	}
	return out
}

// A held check waits on a person; after a person accepts it anyway and work
// runs again the attempt is running, not still waiting for the old reason;
// when only acceptance remains, it waits for acceptance.
func TestJanitorStatisticsFollowAnAttemptThroughOverrideToAcceptance(t *testing.T) {
	s, ch, _, _, _ := automatedFixture(t)
	run := admitAutomatedRun(t, s, ch, "routine", "tests", "approval")
	steps, err := s.RunSteps(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var routine, tests *Step
	for _, step := range steps {
		switch step.Type {
		case "routine":
			routine = step
		case "tests":
			tests = step
		}
	}
	claim, err := newStepAttempts(s.db, func(string) bool { return true }).start(t.Context(), routine, "routine", ch.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	e := &Execution{service: s, ctx: t.Context(), step: routine, run: run, claim: claim}
	if err := e.StartFix(nil); err != nil {
		t.Fatal(err)
	}
	outcome, err := EncodeOutcome(StatusFail, map[string]string{"reason": "dockerfile restructure"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (reworkStore{db: s.db}).fail(t.Context(), claim, outcome); err != nil {
		t.Fatal(err)
	}
	held := statisticsFor(t, s, t.Context(), 7)
	if c := kindCounts(t, held, "security"); c.Runs != 1 || c.Waiting != 1 {
		t.Fatalf("held: %+v", c)
	}
	if got := waitingSteps(held, "security"); len(got) != 1 || got[0] != routine.Name {
		t.Fatalf("held reasons: %v in %+v", got, held.Reasons)
	}
	if len(held.Projects) != 1 || held.Projects[0].Counts.Waiting != 1 || held.Projects[0].Slug != "scan-project" {
		t.Fatalf("project row: %+v", held.Projects)
	}

	ctx := access.WithCaller(t.Context(), access.Caller{Verified: true, Principal: access.Principal{Kind: "user", Name: "verified-person", Via: "session"}})
	if _, err := s.Override(ctx, Override{StepID: routine.ID, Reason: "Keep the Dockerfile restructure"}); err != nil {
		t.Fatal(err)
	}
	s.Wait()
	testClaim, err := newStepAttempts(s.db, func(string) bool { return true }).start(ctx, tests, "tests", ch.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	running := statisticsFor(t, s, ctx, 7)
	if c := kindCounts(t, running, "security"); c.Runs != 1 || c.Running != 1 || c.Waiting != 0 {
		t.Fatalf("running again: %+v", c)
	}
	if got := waitingSteps(running, "security"); len(got) != 0 {
		t.Fatalf("an old reason is still a current wait: %v", got)
	}

	if err := newStepAttempts(s.db, func(string) bool { return true }).finish(ctx, testClaim, StepOutcome{Status: StatusPass}); err != nil {
		t.Fatal(err)
	}
	accepting := statisticsFor(t, s, ctx, 7)
	if c := kindCounts(t, accepting, "security"); c.Waiting != 1 || c.Running != 0 {
		t.Fatalf("awaiting acceptance: %+v", c)
	}
	if got := waitingSteps(accepting, "security"); len(got) != 1 || got[0] != janitorWaitsAcceptance {
		t.Fatalf("acceptance reason: %v", got)
	}
}

// Counts cover only the period and the caller's projects; a failed attempt
// whose Change was purged still counts, with an unlinked example.
func TestJanitorStatisticsCountThePeriodForVisibleProjects(t *testing.T) {
	s, _, repo, _, _ := automatedFixture(t)
	visible, err := s.authority.VisibleProjects(t.Context(), actionReadProject)
	if err != nil {
		t.Fatal(err)
	}
	pid := visible[0]
	now := time.Now().UTC()
	for _, at := range []time.Time{now.Add(-time.Hour), now.Add(-10 * 24 * time.Hour), now.Add(-40 * 24 * time.Hour)} {
		ch := janitorTry(t, s, pid, StandingRevision{}, at, false)
		if err := s.db.InTx(t.Context(), func(tx *database.Tx) error {
			_, err := TransitionChangeIn(t.Context(), tx, pid, ch.ID, ChangeMove{Action: ShipChange}, at)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	failed := janitorTry(t, s, pid, StandingRevision{}, now.Add(-2*time.Hour), true)
	gittest.Run(t, repo, "branch", failed.Branch)
	if _, err := s.Abandon(t.Context(), StopWork{ChangeID: failed.ID, Reason: "replaced"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PurgeAutomatedChanges(t.Context()); err != nil {
		t.Fatal(err)
	}

	week := statisticsFor(t, s, t.Context(), 7)
	if c := kindCounts(t, week, "updates"); c.Runs != 2 || c.Shipped != 1 || c.Failed != 1 {
		t.Fatalf("week: %+v", c)
	}
	var reason *janitorReason
	for i, r := range week.Reasons {
		if r.Kind == "updates" && r.Column == janitorFailed {
			reason = &week.Reasons[i]
		}
	}
	if reason == nil || reason.Count != 1 || !strings.HasSuffix(reason.Latest, "package registry unavailable") || strings.Contains(reason.Step, ": ") || len(reason.Examples) != 1 || !reason.Examples[0].ChangeID.IsZero() || reason.Examples[0].Slug != "scan-project" {
		t.Fatalf("failed reason: %+v", week.Reasons)
	}
	if c := kindCounts(t, statisticsFor(t, s, t.Context(), 30), "updates"); c.Runs != 3 || c.Shipped != 2 {
		t.Fatalf("month: %+v", c)
	}
	if _, err := s.JanitorStatistics(t.Context(), 14); err == nil {
		t.Fatal("a 14-day period was read")
	}

	s.authority = aggregateAuthority{operationAuthority: s.authority, read: func(context.Context, commandAction) ([]ids.ID, error) { return []ids.ID{}, nil }}
	hidden := statisticsFor(t, s, t.Context(), 30)
	if c := kindCounts(t, hidden, "updates"); c.Runs != 0 || len(hidden.Projects) != 0 || len(hidden.Reasons) != 0 {
		t.Fatalf("hidden project counted: %+v", hidden)
	}
	dbtest.FailNextStatement(t, s.db, "-- name: JanitorAttemptStats")
	s.authority = allowProject(pid)
	if _, err := s.JanitorStatistics(t.Context(), 7); err == nil {
		t.Fatal("a failed read became empty statistics")
	}
}

// A security run that closes with nothing to change while its scan still has
// findings worked as intended: it counts as Findings left, not Failed, and is
// not a reason for not shipping. A scan that could not run stays Failed.
func TestJanitorStatisticsCountFindingsLeftApartFromFailures(t *testing.T) {
	for _, state := range []StepState{StatusFail, StatusError} {
		t.Run(state.String(), func(t *testing.T) {
			s, ch, _, _, _ := automatedFixture(t)
			run := admitAutomatedRun(t, s, ch, "scan")
			settleScanTestSteps(t, s, run, state)
			if err := s.endNothingToChange(t.Context(), run); err != nil {
				t.Fatal(err)
			}
			week := statisticsFor(t, s, t.Context(), 7)
			c := kindCounts(t, week, "security")
			want := janitorCounts{Runs: 1, FindingsLeft: 1}
			if state == StatusError {
				want = janitorCounts{Runs: 1, Failed: 1}
			}
			if c != want || len(week.Projects) != 1 || week.Projects[0].Counts != want {
				t.Fatalf("%s counted as %+v, projects %+v; want %+v", state, c, week.Projects, want)
			}
			if grouped := len(week.Reasons) != 0; grouped != (state == StatusError) {
				t.Fatalf("%s reasons: %+v", state, week.Reasons)
			}
		})
	}
}
