package ids

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"
)

// IDs minted by one process are strictly increasing, even within one
// millisecond and when the clock steps back, and carry their millisecond.
func TestNewIsStrictlyIncreasing(t *testing.T) {
	at := time.UnixMilli(1_790_000_000_000)
	prev := New(at)
	for i, now := range []time.Time{at, at, at.Add(-time.Hour), at.Add(time.Millisecond)} {
		id := New(now)
		if id.String() <= prev.String() {
			t.Fatalf("mint %d (%v) is not after the previous: %s <= %s", i, now, id, prev)
		}
		prev = id
	}
	if id := New(at.Add(time.Hour)); !id.Time().Equal(at.Add(time.Hour)) || id[6]>>4 != 7 || id[8]>>6 != 2 {
		t.Fatalf("version 7, RFC variant, minted millisecond: %s %v", id, id.Time())
	}
}

// Text, JSON and SQL forms round-trip; Nil is "", null and NULL; anything
// that is not a canonical UUID, a bare number above all, is refused.
func TestFormsRoundTripAndNilIsAbsent(t *testing.T) {
	id := New(time.Now())
	if p, err := Parse(id.String()); err != nil || p != id {
		t.Fatalf("parse %s: %v %v", id, p, err)
	}
	b, _ := json.Marshal(struct{ A, B ID }{A: id})
	if string(b) != `{"A":"`+id.String()+`","B":null}` {
		t.Fatalf("json %s", b)
	}
	var back struct{ A, B ID }
	if err := json.Unmarshal(b, &back); err != nil || back.A != id || !back.B.IsZero() {
		t.Fatalf("json back %+v %v", back, err)
	}
	if v, _ := Nil.Value(); v != nil {
		t.Fatalf("Nil is NULL, got %v", v)
	}
	var s ID
	if err := s.Scan(nil); err != nil || !s.IsZero() {
		t.Fatal("NULL scans to Nil")
	}
	if err := s.Scan(id.String()); err != nil || s != id {
		t.Fatal("text scans")
	}
	if p, err := Parse(""); err != nil || !p.IsZero() {
		t.Fatal(`"" is Nil`)
	}
	for _, bad := range []string{"293", "0193f7a2-6b1c-7d3e-8f00-1234567890a", "0193f7a26b1c7d3e8f001234567890ab", "zz93f7a2-6b1c-7d3e-8f00-1234567890ab"} {
		if _, err := Parse(bad); !errors.Is(err, ErrNotAnID) {
			t.Errorf("%q: %v, want ErrNotAnID", bad, err)
		}
	}
	if err := json.Unmarshal([]byte(`{"A":293}`), &back); err == nil {
		t.Fatal("a JSON number is not an id")
	}
}

// Any verb prints the canonical form: a format written for an integer id
// cannot print sixteen numbers.
func TestFormatPrintsTheCanonicalFormForEveryVerb(t *testing.T) {
	id, err := Parse("0193f7a2-6b1c-7d3e-8f00-1234567890ab")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"%d", "%v", "%s"} {
		if got := fmt.Sprintf(f, id); got != id.String() {
			t.Errorf("%s: %q", f, got)
		}
	}
	if got := fmt.Sprintf("%q", id); got != `"`+id.String()+`"` {
		t.Errorf("%%q: %s", got)
	}
}
