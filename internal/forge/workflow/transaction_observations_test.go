package workflow

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
	"flokkit.io/forge/internal/ids"
)

// These observations read committed state through the workflow store's own
// reads, so transaction tests assert what the owner answers rather than a
// hand-written column predicate. A renamed column breaks compilation here.

func seenSteps(t *testing.T, db database.Queryer, runID ids.ID) []*Step {
	t.Helper()
	steps, err := readSteps(context.Background(), db, runID)
	if err != nil {
		t.Fatalf("read steps of run %d: %v", runID, err)
	}
	return steps
}

// The stored change row, including columns the domain record does not carry
// (title ownership, the null closed_at).
func seenChange(t *testing.T, db database.Queryer, id ids.ID) workflowsql.Change {
	t.Helper()
	row, err := workflowsql.New(db.SQL()).ChangeByID(context.Background(), workflowsql.ChangeByIDParams{ID: id})
	if err != nil {
		t.Fatalf("read change %d: %v", id, err)
	}
	return row
}

func seenEvents(t *testing.T, db database.Queryer, runID ids.ID) []workflowsql.RunEvent {
	t.Helper()
	rows, err := workflowsql.New(db.SQL()).ReadRunEvents(context.Background(), workflowsql.ReadRunEventsParams{RunID: runID})
	if err != nil {
		t.Fatalf("read events of run %d: %v", runID, err)
	}
	return rows
}

// eventCount counts a run's events of the given kinds whose payload matches
// every path=value pair, compared as the ->> text form.
func eventCount(t *testing.T, db database.Queryer, runID ids.ID, kinds string, match ...string) int {
	t.Helper()
	n := 0
	for _, e := range seenEvents(t, db, runID) {
		if !listHas(kinds, e.Kind) || !jsonMatches(t, e.PayloadJson, match...) {
			continue
		}
		n++
	}
	return n
}

func listHas(list, item string) bool {
	for _, k := range strings.Split(list, ",") {
		if k == item {
			return true
		}
	}
	return false
}

func seenArtifacts(t *testing.T, db database.Queryer, runID ids.ID) []workflowsql.ReadArtifactsRow {
	t.Helper()
	rows, err := workflowsql.New(db.SQL()).ReadArtifacts(context.Background(), workflowsql.ReadArtifactsParams{RunID: runID, IncludeBinary: true})
	if err != nil {
		t.Fatalf("read artifacts of run %d: %v", runID, err)
	}
	return rows
}

// seenLanding is the landing reservation of a work run, or false when none exists.
func seenLanding(t *testing.T, db database.Queryer, workRunID ids.ID) (workflowsql.WorkflowLanding, bool) {
	t.Helper()
	row, err := workflowsql.New(db.SQL()).LandingForUpdate(context.Background(), workRunID)
	if errors.Is(err, sql.ErrNoRows) {
		return row, false
	}
	if err != nil {
		t.Fatalf("read landing of run %d: %v", workRunID, err)
	}
	return row, true
}

func seenRunStart(t *testing.T, db database.Queryer, runID ids.ID) (workflowsql.RunStart, bool) {
	t.Helper()
	row, err := workflowsql.New(db.SQL()).RunStartByID(context.Background(), runID)
	if errors.Is(err, sql.ErrNoRows) {
		return row, false
	}
	if err != nil {
		t.Fatalf("read start of run %d: %v", runID, err)
	}
	return row, true
}

// auditCount reads the whole ledger (proved complete by staying under the
// read limit) and counts receipts of kind (empty: every kind) that match.
func auditCount(t *testing.T, db database.Queryer, kind string, match func(audit.Entry) bool) int {
	t.Helper()
	entries, err := audit.Recent(context.Background(), db, audit.Filter{KindPrefix: kind, Limit: audit.MaxLimit})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) >= audit.MaxLimit {
		t.Fatalf("ledger read is bounded at %d entries and cannot prove a count", audit.MaxLimit)
	}
	n := 0
	for _, e := range entries {
		if (kind == "" || e.Kind == kind) && (match == nil || match(e)) {
			n++
		}
	}
	return n
}

// jsonText is the ->> reading of a path in a JSON document: strings unquoted,
// numbers in their exact literal form, objects and arrays compacted.
func jsonText(t *testing.T, raw json.RawMessage, path ...string) (string, bool) {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	for _, key := range path {
		switch node := v.(type) {
		case map[string]any:
			next, ok := node[key]
			if !ok {
				return "", false
			}
			v = next
		case []any:
			var i int
			if _, err := fmt.Sscan(key, &i); err != nil || i < 0 || i >= len(node) {
				return "", false
			}
			v = node[i]
		default:
			return "", false
		}
	}
	switch value := v.(type) {
	case nil:
		return "", false
	case string:
		return value, true
	case json.Number:
		return value.String(), true
	case bool:
		return fmt.Sprint(value), true
	default:
		b, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return string(b), true
	}
}

// jsonMatches reports whether every "a.b.c=value" pair holds in raw.
func jsonMatches(t *testing.T, raw json.RawMessage, pairs ...string) bool {
	t.Helper()
	for _, pair := range pairs {
		path, want, _ := strings.Cut(pair, "=")
		got, ok := jsonText(t, raw, strings.Split(path, ".")...)
		if !ok || got != want {
			return false
		}
	}
	return true
}

func emptyJSON(raw json.RawMessage) bool {
	var b bytes.Buffer
	return json.Compact(&b, raw) == nil && b.String() == "{}"
}

func stepCount(steps []*Step, keep func(*Step) bool) int {
	n := 0
	for _, s := range steps {
		if keep(s) {
			n++
		}
	}
	return n
}

// fresh is a step returned to pending with no attempt residue.
func freshStep(s *Step) bool {
	return s.Status == StatusPending && !s.StartedAt.Valid && !s.FinishedAt.Valid && emptyJSON(s.Evidence) && s.OverrideBy == ""
}

func binaryArtifacts(t *testing.T, db database.Queryer, runID ids.ID, body []byte) int {
	t.Helper()
	n := 0
	for _, a := range seenArtifacts(t, db, runID) {
		if a.Binary && bytes.Equal(a.RawBody, body) {
			n++
		}
	}
	return n
}

// expect fails the test with the observed value when a claim does not hold.
func expect(t *testing.T, ok bool, format string, args ...any) {
	t.Helper()
	if !ok {
		t.Fatalf(format, args...)
	}
}
