package workflow

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
	"flokkit.io/forge/internal/ids"
)

// Stored-record reads observe committed workflow state through the owner's own
// record reads and generated queries rather than SQL literals, so a renamed
// column breaks compilation instead of silently changing an assertion.
//
// The whole-store reads are complete: every step and event references a run,
// and runs are enumerated without a project fence or limit. Totals taken from
// them therefore still prove that no hidden row was written anywhere.

func storedRun(t *testing.T, db database.Queryer, id ids.ID) *Run {
	t.Helper()
	run, err := readRun(context.Background(), db, id)
	if err != nil {
		t.Fatalf("read run %d: %v", id, err)
	}
	return run
}

// storedRunRow is the full stored run, for columns the Run record omits.
func storedRunRow(t *testing.T, db database.Queryer, id ids.ID) workflowsql.Run {
	t.Helper()
	row, err := workflowsql.New(db.SQL()).RunByID(context.Background(), workflowsql.RunByIDParams{ID: id})
	if err != nil {
		t.Fatalf("read run %d: %v", id, err)
	}
	return row
}

func storedStep(t *testing.T, db database.Queryer, id ids.ID) *Step {
	t.Helper()
	step, err := readStep(context.Background(), db, id)
	if err != nil {
		t.Fatalf("read step %d: %v", id, err)
	}
	return step
}

func storedChange(t *testing.T, db database.Queryer, id ids.ID) *Change {
	t.Helper()
	ch, err := readChange(context.Background(), db, id)
	if err != nil {
		t.Fatalf("read change %d: %v", id, err)
	}
	return ch
}

func storedRunStart(t *testing.T, db database.Queryer, id ids.ID) workflowsql.RunStart {
	t.Helper()
	start, err := workflowsql.New(db.SQL()).RunStartByID(context.Background(), id)
	if err != nil {
		t.Fatalf("read run start %d: %v", id, err)
	}
	return start
}

func storedInferenceOwner(t *testing.T, db database.Queryer, id ids.ID) string {
	t.Helper()
	owner, err := workflowsql.New(db.SQL()).RunInferenceOwnerByID(context.Background(), id)
	if err != nil {
		t.Fatalf("read inference owner of run %d: %v", id, err)
	}
	return owner.Actor
}

// storedRuns is every run in the store, across all projects.
func storedRuns(t *testing.T, db database.Queryer) []*Run {
	t.Helper()
	rows, err := workflowsql.New(db.SQL()).WorkflowSummaries(context.Background(), workflowsql.WorkflowSummariesParams{})
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	runs := make([]*Run, 0, len(rows))
	for _, row := range rows {
		runs = append(runs, storedRun(t, db, row.ID))
	}
	return runs
}

// storedSteps is every step in the store, across all runs.
func storedSteps(t *testing.T, db database.Queryer) []*Step {
	t.Helper()
	var steps []*Step
	for _, run := range storedRuns(t, db) {
		steps = append(steps, storedRunSteps(t, db, run.ID)...)
	}
	return steps
}

func storedRunSteps(t *testing.T, db database.Queryer, runID ids.ID) []*Step {
	t.Helper()
	steps, err := readSteps(context.Background(), db, runID)
	if err != nil {
		t.Fatalf("read steps of run %d: %v", runID, err)
	}
	return steps
}

// storedEvents is every run event in the store, across all runs.
func storedEvents(t *testing.T, db database.Queryer) []Event {
	t.Helper()
	var events []Event
	for _, run := range storedRuns(t, db) {
		events = append(events, storedRunEvents(t, db, run.ID)...)
	}
	return events
}

func storedRunEvents(t *testing.T, db database.Queryer, runID ids.ID) []Event {
	t.Helper()
	events, err := readWorkflowEvents(context.Background(), db, runID, TraceView)
	if err != nil {
		t.Fatalf("read events of run %d: %v", runID, err)
	}
	return events
}

// wantRunState fails unless the stored run has the given status.
func wantRunState(t *testing.T, db database.Queryer, id ids.ID, want RunState) *Run {
	t.Helper()
	run := storedRun(t, db, id)
	if run.Status != want {
		t.Fatalf("run %d status = %s, want %s", id, run.Status, want)
	}
	return run
}

func wantRetryRevision(t *testing.T, db database.Queryer, id ids.ID, want int64) {
	t.Helper()
	if got := storedRun(t, db, id).RetryRevision; got != want {
		t.Fatalf("run %d retry revision = %d, want %d", id, got, want)
	}
}

func wantCurrentStage(t *testing.T, db database.Queryer, id ids.ID, want string) {
	t.Helper()
	if got := storedRun(t, db, id).CurrentStage; got != want {
		t.Fatalf("run %d stage = %q, want %q", id, got, want)
	}
}

func wantChangeState(t *testing.T, db database.Queryer, id ids.ID, want ChangeState) *Change {
	t.Helper()
	ch := storedChange(t, db, id)
	if ch.State != want {
		t.Fatalf("change %d state = %s, want %s", id, ch.State, want)
	}
	return ch
}

func wantStepState(t *testing.T, db database.Queryer, id ids.ID, want StepState) *Step {
	t.Helper()
	step := storedStep(t, db, id)
	if step.Status != want {
		t.Fatalf("step %d status = %s, want %s", id, step.Status, want)
	}
	return step
}

// wantUntouchedStep fails unless the step is in state with empty evidence and
// no finish time: nothing was recorded against it.
func wantUntouchedStep(t *testing.T, db database.Queryer, id ids.ID, want StepState) {
	t.Helper()
	step := wantStepState(t, db, id, want)
	if !jsonEqual(step.Evidence, `{}`) || step.FinishedAt.Valid {
		t.Fatalf("step %d evidence=%s finished=%v, want untouched", id, step.Evidence, step.FinishedAt.Valid)
	}
}

// wantRunCount counts every stored run that matches.
func wantRunCount(t *testing.T, db database.Queryer, want int, match func(*Run) bool) {
	t.Helper()
	got := 0
	for _, run := range storedRuns(t, db) {
		if match == nil || match(run) {
			got++
		}
	}
	if got != want {
		t.Fatalf("runs = %d, want %d", got, want)
	}
}

// wantStepCount counts every stored step that matches.
func wantStepCount(t *testing.T, db database.Queryer, want int, match func(*Step) bool) {
	t.Helper()
	got := 0
	for _, step := range storedSteps(t, db) {
		if match == nil || match(step) {
			got++
		}
	}
	if got != want {
		t.Fatalf("steps = %d, want %d", got, want)
	}
}

// wantEventCount counts every stored run event that matches.
func wantEventCount(t *testing.T, db database.Queryer, want int, match func(Event) bool) {
	t.Helper()
	if got := countEvents(storedEvents(t, db), match); got != want {
		t.Fatalf("run events = %d, want %d", got, want)
	}
}

// wantRunEventCount counts one run's events that match.
func wantRunEventCount(t *testing.T, db database.Queryer, runID ids.ID, want int, match func(Event) bool) {
	t.Helper()
	if got := countEvents(storedRunEvents(t, db, runID), match); got != want {
		t.Fatalf("run %d events = %d, want %d", runID, got, want)
	}
}

func countEvents(events []Event, match func(Event) bool) int {
	got := 0
	for _, event := range events {
		if match == nil || match(event) {
			got++
		}
	}
	return got
}

// anyEvent matches every event; it names a total in a call site.
var anyEvent func(Event) bool

// ofKind matches events of any of the kinds.
func ofKind(kinds ...string) func(Event) bool {
	return func(e Event) bool {
		for _, kind := range kinds {
			if e.Kind == kind {
				return true
			}
		}
		return false
	}
}

// kindWith matches one kind whose payload has the path/value pairs, as in
// kindWith("step.retried", "by", "owner", "previousEvidence.n", "7").
func kindWith(kind string, fields ...string) func(Event) bool {
	return func(e Event) bool {
		return e.Kind == kind && hasFields(e.Payload, fields...)
	}
}

// hasFields reports whether each path/value pair matches jsonField.
func hasFields(raw json.RawMessage, fields ...string) bool {
	for i := 0; i+1 < len(fields); i += 2 {
		got, ok := jsonField(raw, fields[i])
		if !ok || got != fields[i+1] {
			return false
		}
	}
	return true
}

// jsonField reads a dotted path the way Postgres #>> does: strings unquoted,
// numbers and booleans as their exact JSON text, and null or absent as missing.
func jsonField(raw json.RawMessage, path string) (string, bool) {
	value, ok := jsonValue(raw, path)
	if !ok {
		return "", false
	}
	text := strings.TrimSpace(string(value))
	if text == "null" {
		return "", false
	}
	if strings.HasPrefix(text, `"`) {
		var s string
		if err := json.Unmarshal(value, &s); err != nil {
			return "", false
		}
		return s, true
	}
	return text, true
}

// jsonValue is the raw JSON at a dotted path; array elements are numbered.
func jsonValue(raw json.RawMessage, path string) (json.RawMessage, bool) {
	value := raw
	for _, key := range strings.Split(path, ".") {
		var object map[string]json.RawMessage
		if err := json.Unmarshal(value, &object); err != nil {
			var array []json.RawMessage
			if err := json.Unmarshal(value, &array); err != nil {
				return nil, false
			}
			index, err := strconv.Atoi(key)
			if err != nil || index < 0 || index >= len(array) {
				return nil, false
			}
			value = array[index]
			continue
		}
		next, ok := object[key]
		if !ok {
			return nil, false
		}
		value = next
	}
	return value, true
}

// jsonBigint reads a field the way Postgres casts (field->>'x')::bigint: a
// JSON integer (or a string holding one); anything else is not a number.
func jsonBigint(raw json.RawMessage, path string) (int64, bool) {
	value, ok := jsonValue(raw, path)
	if !ok {
		return 0, false
	}
	text := string(value)
	var quoted string
	if json.Unmarshal(value, &quoted) == nil {
		text = quoted
	}
	n, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64)
	return n, err == nil
}

// hasKey reports whether a JSON object has a top-level key, like jsonb_exists.
func hasKey(raw json.RawMessage, key string) bool {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return false
	}
	_, ok := object[key]
	return ok
}

// jsonEqual compares JSON values the way jsonb equality does.
func jsonEqual(raw json.RawMessage, want string) bool {
	decode := func(text []byte) (any, error) {
		var value any
		decoder := json.NewDecoder(bytes.NewReader(text))
		decoder.UseNumber()
		return value, decoder.Decode(&value)
	}
	got, err := decode(raw)
	if err != nil {
		return false
	}
	expected, err := decode([]byte(want))
	return err == nil && reflect.DeepEqual(got, expected)
}

func wantRunReady(t *testing.T, db database.Queryer, id ids.ID, ready bool) {
	t.Helper()
	if got := storedRunStart(t, db, id).Ready; got != ready {
		t.Fatalf("run %d ready = %v, want %v", id, got, ready)
	}
}

func wantInferenceOwner(t *testing.T, db database.Queryer, id ids.ID, actor string) {
	t.Helper()
	if got := storedInferenceOwner(t, db, id); got != actor {
		t.Fatalf("run %d inference owner = %q, want %q", id, got, actor)
	}
}

// wantEqual fails when a stored value differs from the expected one.
func wantEqual[T comparable](t *testing.T, what string, got, want T) {
	t.Helper()
	if got != want {
		t.Fatalf("%s = %v, want %v", what, got, want)
	}
}

// storedReceipts is every audit receipt, through the ledger's own reader. That
// read is bounded, so it refuses to stand in for a total it cannot see whole.
func storedReceipts(t *testing.T, db database.Queryer) []audit.Entry {
	t.Helper()
	entries, err := audit.Recent(context.Background(), db, audit.Filter{Limit: audit.MaxLimit})
	if err != nil {
		t.Fatalf("read audit ledger: %v", err)
	}
	if len(entries) >= audit.MaxLimit {
		t.Fatalf("audit ledger read is bounded at %d receipts and cannot prove a count", audit.MaxLimit)
	}
	return entries
}

// wantReceiptCount counts every audit receipt that matches.
func wantReceiptCount(t *testing.T, db database.Queryer, want int, match func(audit.Entry) bool) {
	t.Helper()
	got := 0
	for _, entry := range storedReceipts(t, db) {
		if match == nil || match(entry) {
			got++
		}
	}
	if got != want {
		t.Fatalf("audit receipts = %d, want %d", got, want)
	}
}

func receiptKind(kinds ...string) func(audit.Entry) bool {
	return func(e audit.Entry) bool {
		for _, kind := range kinds {
			if e.Kind == kind {
				return true
			}
		}
		return false
	}
}

// holdProjectLock takes the project row lock that workflow commands queue on.
func holdProjectLock(t *testing.T, tx database.Queryer, id ids.ID) {
	t.Helper()
	if _, err := workflowsql.New(tx.SQL()).ProjectForUpdate(context.Background(), id); err != nil {
		t.Fatal(err)
	}
}

// receiptWith matches one kind, by an actor unless actor is empty, whose
// payload has the path/value pairs.
func receiptWith(kind, actor string, fields ...string) func(audit.Entry) bool {
	return func(e audit.Entry) bool {
		return e.Kind == kind && (actor == "" || e.Actor == actor) && hasFields(e.Payload, fields...)
	}
}

// inProject narrows a receipt match to one project's lines.
func inProject(project ids.ID, match func(audit.Entry) bool) func(audit.Entry) bool {
	return func(e audit.Entry) bool { return e.ProjectID == project && (match == nil || match(e)) }
}

// wantJSONField fails unless each path/value pair matches jsonField.
func wantJSONField(t *testing.T, what string, raw json.RawMessage, fields ...string) {
	t.Helper()
	for i := 0; i+1 < len(fields); i += 2 {
		if got, ok := jsonField(raw, fields[i]); !ok || got != fields[i+1] {
			t.Fatalf("%s %s = %q (present %v), want %q in %s", what, fields[i], got, ok, fields[i+1], raw)
		}
	}
}

// jsonPathEqual compares the JSON value at a dotted path, like jsonb -> equality.
func jsonPathEqual(raw json.RawMessage, path, want string) bool {
	value, ok := jsonValue(raw, path)
	return ok && jsonEqual(value, want)
}

// untouchedIn matches steps in state with empty evidence and no finish time.
func untouchedIn(state StepState) func(*Step) bool {
	return func(s *Step) bool { return s.Status == state && jsonEqual(s.Evidence, `{}`) && !s.FinishedAt.Valid }
}

func changeSource(c *Change) [2]string { return [2]string{c.BaseSHA, c.Branch} }

// wantReadyCount counts runs whose start record has the given readiness; a
// run without a start record counts as neither.
func wantReadyCount(t *testing.T, db database.Queryer, ready bool, want int) {
	t.Helper()
	got := 0
	for _, run := range storedRuns(t, db) {
		start, err := workflowsql.New(db.SQL()).RunStartByID(context.Background(), run.ID)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			t.Fatalf("read run start %d: %v", run.ID, err)
		}
		if start.Ready == ready {
			got++
		}
	}
	if got != want {
		t.Fatalf("run starts with ready=%v = %d, want %d", ready, got, want)
	}
}
