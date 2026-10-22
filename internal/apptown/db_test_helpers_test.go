package apptown

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/jobs"
	"github.com/flokkit/apptown/internal/apptown/model"
	"github.com/flokkit/apptown/internal/apptown/remediation"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

// mustRecords is a read a test depends on: a failure stops the test (a
// panic names the read) rather than reading as an empty list.
func mustRecords[T any](records []T, err error) []T {
	if err != nil {
		panic(fmt.Sprintf("read failed: %v", err))
	}
	return records
}

func laneOn(t *testing.T, s *adoption.Service, projectID ids.ID, name string) bool {
	t.Helper()
	on, err := s.LaneSelected(t.Context(), projectID, name)
	if err != nil {
		t.Fatalf("reading lane %s: %v", name, err)
	}
	return on
}

// These helpers ask the owning domain what a root test wants to know, so
// assertions read the product's answer instead of its tables.

// ownedRun is the run as Work's store reads it.
func ownedRun(t *testing.T, a *App, id ids.ID) *workflow.Run {
	t.Helper()
	run, err := workflow.ReadRunIn(context.Background(), a.DB, id)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

// requireRunStatus fails unless each run reads with the given status.
func requireRunStatus(t *testing.T, a *App, want workflow.RunState, runs ...ids.ID) {
	t.Helper()
	for _, id := range runs {
		if got := ownedRun(t, a, id).Status; got != want {
			t.Fatalf("run %d: status %q, want %q", id, got, want)
		}
	}
}

// ownedChange is the change as Work's store reads it.
func ownedChange(t *testing.T, a *App, id ids.ID) *workflow.Change {
	t.Helper()
	change, err := workflow.ReadChangeIn(context.Background(), a.DB, id)
	if err != nil {
		t.Fatal(err)
	}
	return change
}

// requireChangeState fails unless the change reads in the given state.
func requireChangeState(t *testing.T, a *App, id ids.ID, want workflow.ChangeState) *workflow.Change {
	t.Helper()
	change := ownedChange(t, a, id)
	if change.State != want {
		t.Fatalf("change %d: state %q, want %q", id, change.State, want)
	}
	return change
}

// runEventCount is how many of a run's events have the kind. It reads the
// whole trace: the detail view keeps only the newest events, so an absence
// there would not prove the event was never written.
func runEventCount(t *testing.T, a *App, runID ids.ID, kind string) int {
	t.Helper()
	snapshot, err := a.Work.Snapshot(a.asEngine(context.Background()), runID, workflow.TraceView)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, event := range snapshot.Events {
		if event.Kind == kind {
			n++
		}
	}
	return n
}

// ledgerEntries is every ledger line of exactly this kind, and of exactly this
// project when projectID is nonzero (the ledger read also returns instance
// lines for a project, which would hide a receipt filed under the wrong
// scope). It fails rather than return a list the read's cap could have cut
// short, so a count over it is a total.
func ledgerEntries(t *testing.T, a *App, projectID ids.ID, kind string) []audit.Entry {
	t.Helper()
	got, err := audit.Recent(context.Background(), a.DB, audit.Filter{ProjectID: projectID, KindPrefix: kind, Limit: audit.MaxLimit})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) >= audit.MaxLimit {
		t.Fatalf("ledger read for %q reached its cap; a count over it is not a total", kind)
	}
	var out []audit.Entry
	for _, e := range got {
		if e.Kind == kind && (projectID.IsZero() || e.ProjectID == projectID) {
			out = append(out, e)
		}
	}
	return out
}

// operationJobs is every queued or recent job of one operation, as the jobs
// page lists them, failing when the list's cap could have cut it short.
func operationJobs(t *testing.T, a *App, operation string) []jobs.Job {
	t.Helper()
	const cap = 500
	all, err := jobs.List(context.Background(), a.DB, cap)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) >= cap {
		t.Fatal("job list reached its cap; a count over it is not a total")
	}
	var out []jobs.Job
	for _, j := range all {
		if j.Kind == operation {
			out = append(out, j)
		}
	}
	return out
}

// projectExists is whether the project catalogue still holds the project.
func projectExists(t *testing.T, a *App, id ids.ID) bool {
	t.Helper()
	_, err := a.Projects.ByID(context.Background(), id)
	found, err := database.Lookup(err)
	if err != nil {
		t.Fatal(err)
	}
	return found
}

// slotOverrides is how many explicit project mappings of the slot the model
// catalogue lists for the project.
func slotOverrides(t *testing.T, a *App, projectSlug, slot string) int {
	t.Helper()
	all, err := model.New(a.DB).Overrides(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, o := range all {
		if o.ProjectSlug == projectSlug && o.Slot == slot {
			n++
		}
	}
	return n
}

// allRuns is every run in every project, roots and children, uncapped.
func allRuns(t *testing.T, a *App) []workflow.RunSummary {
	t.Helper()
	runs, err := a.Work.Runs(a.asEngine(context.Background()), workflow.RunQuery{})
	if err != nil {
		t.Fatal(err)
	}
	return runs
}

// runsWithStatus counts the runs in the given status.
func runsWithStatus(runs []workflow.RunSummary, want workflow.RunState) int {
	n := 0
	for _, run := range runs {
		if run.Status == want {
			n++
		}
	}
	return n
}

// changeRuns is every run of a change, roots and children, uncapped.
func changeRuns(t *testing.T, a *App, changeID ids.ID) []*workflow.Run {
	t.Helper()
	runs, err := a.Work.ChangeRuns(a.asEngine(context.Background()), changeID, workflow.ChangeRuns{})
	if err != nil {
		t.Fatal(err)
	}
	return runs
}

// projectChanges is every change of a project in any state, uncapped.
func projectChanges(t *testing.T, a *App, projectID ids.ID) []workflow.ChangeSummary {
	t.Helper()
	changes, err := a.Work.Changes(a.asEngine(context.Background()), projectID, workflow.ChangeList{})
	if err != nil {
		t.Fatal(err)
	}
	return changes
}

// runUnready is whether the run's admitted start still awaits preparation,
// as Work's execution observation reports it.
func runUnready(t *testing.T, a *App, projectID, changeID, runID ids.ID) bool {
	t.Helper()
	tx, err := a.DB.ReadSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	observed, err := workflow.ObserveExecutionIn(context.Background(), tx, workflow.ExecutionSelection{Change: workflow.ChangeRef{ID: changeID, ProjectID: projectID}, RunID: runID})
	if err != nil || observed == nil {
		t.Fatalf("observing run %d: %+v %v", runID, observed, err)
	}
	return observed.Unready
}

// remediationAttempts is every attempt of every item of one remediation.
func remediationAttempts(t *testing.T, a *App, remediationID ids.ID) []remediation.Attempt {
	t.Helper()
	byItem, err := a.Fixes.Attempts(context.Background(), remediationID)
	if err != nil {
		t.Fatal(err)
	}
	var out []remediation.Attempt
	for _, attempts := range byItem {
		out = append(out, attempts...)
	}
	return out
}

// accountExists is whether the directory knows the account.
func accountExists(t *testing.T, a *App, username string) bool {
	t.Helper()
	_, found, err := a.Access.Role(context.Background(), username)
	if err != nil {
		t.Fatal(err)
	}
	return found
}

// liveGrants is the live grants at a scope matching subject and role.
func liveGrants(t *testing.T, a *App, scope access.ScopeKind, scopeID, subjectType, subject, role string) []access.Grant {
	t.Helper()
	grants, err := a.Access.GrantsAt(context.Background(), scope, scopeID)
	if err != nil {
		t.Fatal(err)
	}
	var out []access.Grant
	for _, g := range grants {
		if g.SubjectType == subjectType && g.Subject == subject && g.RoleName == role {
			out = append(out, g)
		}
	}
	return out
}

// conformanceRecordCounts is how many reviews the fleet lists and how many
// dispositions the projects hold, read as an administrator.
func conformanceRecordCounts(t *testing.T, a *App, projects ...ids.ID) (reviews, dispositions int) {
	t.Helper()
	ctx := verifiedContext(context.Background(), access.Principal{Kind: "user", Name: "boss", Via: "token", Role: "admin"})
	const cap = 50
	listed, err := a.conformance.Reviews(ctx, cap)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) >= cap {
		t.Fatal("review list reached its cap")
	}
	for _, id := range projects {
		held, err := a.conformance.Dispositions(ctx, id, false)
		if err != nil {
			t.Fatal(err)
		}
		dispositions += len(held)
	}
	return len(listed), dispositions
}

// attemptStatus is a deployment attempt's state as recovery reads it.
func attemptStatus(t *testing.T, a *App, id ids.ID) deployment.AttemptState {
	t.Helper()
	_, status, _, err := a.Deployment.Outcomes.RecoveryAttempt(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return status
}

// attemptNotes is what the attempt recorded as observed, as recovery reads it.
func attemptNotes(t *testing.T, a *App, id ids.ID) map[string]any {
	t.Helper()
	_, _, notes, err := a.Deployment.Outcomes.RecoveryAttempt(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return notes
}

// openAttempts is every running or uncertain deployment attempt.
func openAttempts(t *testing.T, a *App) []deployment.Attempt {
	t.Helper()
	attempts, err := a.Deployment.Outcomes.InterruptedAttempts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return attempts
}

// deploymentsMatching counts the instance's deployments that satisfy match.
func deploymentsMatching(t *testing.T, a *App, match func(deployment.ProjectDeployment) bool) int {
	t.Helper()
	all, err := a.Deployment.Deployments.All(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, d := range all {
		if match(d) {
			n++
		}
	}
	return n
}

// stepReason is the reason a step's evidence gives, "" when it gives none.
func stepReason(t *testing.T, step *workflow.Step) string {
	t.Helper()
	return exactText(step.Evidence, "reason")
}

// exactKey is one top-level member of a JSON object, matched by its exact
// key the way jsonb's -> is. Decoding into a struct would not do: Go matches
// field names without regard to case, so a stored "Reason" would satisfy a
// check for "reason" that the stored record's readers would not.
func exactKey(raw json.RawMessage, key string) (json.RawMessage, bool) {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil {
		return nil, false
	}
	value, ok := object[key]
	return value, ok
}

// exactText is a member as jsonb's ->> gives it: a string unquoted, any other
// value as its JSON text, and "" for a missing key or JSON null.
func exactText(raw json.RawMessage, key string) string {
	value, ok := exactKey(raw, key)
	if !ok || string(value) == "null" {
		return ""
	}
	var text string
	if json.Unmarshal(value, &text) == nil {
		return text
	}
	return string(value)
}

// exactRequest is a queued job's request read by its exact argument keys, as
// the old args->>'operation' predicates did, not by case-blind struct decoding.
func exactRequest(raw json.RawMessage) jobs.Request {
	var values map[string]any
	if v, ok := exactKey(raw, "values"); ok {
		_ = json.Unmarshal(v, &values)
	}
	retry, _ := exactKey(raw, "retryOnInterrupt")
	return jobs.Request{Operation: exactText(raw, "operation"), Key: exactText(raw, "key"), Values: values, RetryOnInterrupt: string(retry) == "true"}
}

// jobsInState counts one operation's jobs in a queue state.
func jobsInState(t *testing.T, a *App, operation, key, state string) int {
	t.Helper()
	n := 0
	for _, j := range operationJobs(t, a, operation) {
		if (key == "" || j.Key == key) && j.State == state {
			n++
		}
	}
	return n
}
