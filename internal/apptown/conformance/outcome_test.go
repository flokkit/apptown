package conformance

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestOutcomesRejectObservationStates(t *testing.T) {
	t.Parallel()
	for _, word := range []string{"stale", "not-scanned", "", "running", "pass"} {
		if _, err := ParseOutcome(word); !errors.Is(err, errOutcome) {
			t.Errorf("accepted outcome %q: %v", word, err)
		}
		state := Conforms
		if err := state.Scan(word); !errors.Is(err, errOutcome) || state != Conforms {
			t.Errorf("scan %q changed outcome: %v %v", word, state, err)
		}
	}
	for _, input := range []string{`"stale"`, `"not-scanned"`, `null`, `0`, `""`} {
		state := Conforms
		if err := json.Unmarshal([]byte(input), &state); err == nil || state != Conforms {
			t.Errorf("decoded %s: %v %v", input, state, err)
		}
	}
	for _, state := range []Outcome{0, 99} {
		if _, err := state.Value(); !errors.Is(err, errOutcome) {
			t.Errorf("stored invalid outcome: %v", err)
		}
		if _, err := json.Marshal(state); !errors.Is(err, errOutcome) {
			t.Errorf("serialized invalid outcome: %v", err)
		}
	}
	for _, state := range []driver.Valuer{Stale, NotScanned} {
		if _, err := state.Value(); !errors.Is(err, errObservationStorage) {
			t.Errorf("stored observation: %v", err)
		}
	}
	for state := Conforms; state <= EvaluationError; state++ {
		value, err := state.Value()
		if err != nil {
			t.Fatal(err)
		}
		var decoded Outcome
		if err := decoded.Scan(value); err != nil || decoded != state {
			t.Fatalf("SQL round trip: %v %v", decoded, err)
		}
		b, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(b, &decoded); err != nil || decoded != state {
			t.Fatalf("JSON round trip: %v %v", decoded, err)
		}
	}
}

func TestObservationsPreserveEvidence(t *testing.T) {
	t.Parallel()
	original := Verdict{ID: idstest.N(31), ProjectID: idstest.N(7), ProjectSlug: "game", SHA: "then", Block: "quality", Version: 2, Check: "launch", State: Excepted, Detail: "original detail ", ExceptedBy: `{"id":9}`}
	for _, tc := range []struct {
		name, head string
		version    int
		active     bool
		want       ObservationState
		note       bool
	}{
		{"unchanged", "then", 2, true, Excepted, false},
		{"expired", "then", 2, false, DoesNotConform, true},
		{"new commit", "now", 2, true, Stale, false},
		{"new adoption", "then", 3, true, Stale, false},
		{"stale precedes expiry", "now", 2, false, Stale, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			observed := observe(original, currentContext{Check: CheckRef{ProjectID: original.ProjectID, ProjectSlug: original.ProjectSlug, Block: original.Block, Version: tc.version, Check: original.Check}, HeadSHA: tc.head, ExceptionActive: tc.active})
			if observed.State != tc.want || observed.Record == nil || !reflect.DeepEqual(*observed.Record, original) {
				t.Fatalf("observation altered evidence: %+v", observed)
			}
			if (observed.Detail() != original.Detail) != tc.note {
				t.Fatalf("unexpected detail: %q", observed.Detail())
			}
			observed.Record.Detail = "consumer edit"
			if original.Detail != "original detail " {
				t.Fatal("observation aliased input")
			}
		})
	}
}

func TestObservationJSONKeepsTheFlatContract(t *testing.T) {
	t.Parallel()
	record := Verdict{ID: idstest.N(31), ProjectID: idstest.N(7), ProjectSlug: "game", SHA: "then", Block: "quality", Version: 2, Check: "launch", State: Excepted, Detail: "saved", Mode: "posture"}
	for _, tc := range []struct {
		observation Observation
		wantState   string
		wantID      ids.ID
		wantDetail  string
	}{
		{Recorded(record), "excepted", idstest.N(31), "saved"},
		{observe(record, currentContext{Check: record.reference(), HeadSHA: "now", ExceptionActive: true}), "stale", idstest.N(31), "saved"},
		{observe(record, currentContext{Check: record.reference(), HeadSHA: "then", ExceptionActive: false}), "does-not-conform", idstest.N(31), "saved\n(the exception that covered this has expired or been removed)"},
		{Unassessed(record.reference()), "not-scanned", ids.Nil, ""},
	} {
		b, err := json.Marshal(tc.observation)
		if err != nil {
			t.Fatal(err)
		}
		var wire struct {
			ID                         ids.ID `json:"id"`
			ProjectID                  ids.ID `json:"projectId"`
			Check, State, Detail, Mode string
			Record                     json.RawMessage
		}
		if err := json.Unmarshal(b, &wire); err != nil {
			t.Fatal(err)
		}
		if wire.ID != tc.wantID || wire.ProjectID != idstest.N(7) || wire.Check != "launch" || wire.State != tc.wantState || wire.Detail != tc.wantDetail || wire.Mode != "posture" || wire.Record != nil {
			t.Fatalf("wire shape: %s", b)
		}
	}
	if o := Unassessed(record.reference()); o.Record != nil {
		t.Fatal("absence manufactured a verdict")
	}
	if _, err := json.Marshal(Observation{}); err == nil {
		t.Fatal("serialized missing observation state")
	}
}
