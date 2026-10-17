package stamp_test

import (
	"encoding/json"
	"testing"
	"time"

	"flokkit.io/forge/internal/stamp"
)

func TestStampWireFormat(t *testing.T) {
	t.Parallel()
	type out struct {
		At     stamp.Time `json:"at"`
		Closed stamp.Time `json:"closedAt,omitzero"`
	}
	b, _ := json.Marshal(out{At: stamp.Parse("2026-09-05T05:40:13Z")})
	if string(b) != `{"at":"2026-09-05T05:40:13Z"}` {
		t.Fatalf("the wire form is the RFC3339 string, absence omitted: %s", b)
	}
	b, _ = json.Marshal(out{})
	if string(b) != `{"at":""}` {
		t.Fatalf("absence without omitzero is the empty string the API always sent: %s", b)
	}
	var in out
	if err := json.Unmarshal([]byte(`{"at":"2026-09-05T05:40:13.25Z","closedAt":""}`), &in); err != nil {
		t.Fatal(err)
	}
	if !in.At.Valid || in.At.Time.Nanosecond() != 250_000_000 || in.Closed.Valid {
		t.Fatalf("parsed %+v", in)
	}
	if stamp.Parse("2026-08-28 09:32:36").Valid {
		t.Fatal("ambiguous timestamps must not be interpreted as UTC")
	}
}

// A moment has passed by the clock asked, not the real one: absence never.
func TestAMomentHasPassedByTheClockAsked(t *testing.T) {
	t.Parallel()
	at := stamp.Of(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	if at.PassedAt(time.Date(2029, 12, 31, 0, 0, 0, 0, time.UTC)) || !at.PassedAt(time.Date(2030, 1, 2, 0, 0, 0, 0, time.UTC)) {
		t.Fatal("PassedAt did not judge by the clock asked")
	}
	if (stamp.Time{}).PassedAt(time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatal("absence has passed")
	}
}
