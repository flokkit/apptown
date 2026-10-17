package usage

import (
	"encoding/json"
	"testing"

	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
)

// Spend moves with what it is about: a row of a run (or its root) or a
// chat that stays stays, counted. A row with no run or chat is the
// project's own and moves, and that includes a change's narrative spend,
// which records no change: a skipped change's narrative spend moves as
// project spend (the accepted rule; no row says which change it was for).
// nilOr is the run a row names: 0 for none.
func nilOr(n uint64) ids.ID {
	if n == 0 {
		return ids.Nil
	}
	return idstest.N(n)
}

func TestSpendMovesWithWhatItIsAbout(t *testing.T) {
	row := func(kind string, run, root uint64, session string) json.RawMessage {
		b, _ := json.Marshal(map[string]any{"kind": kind, "run_id": nilOr(run), "root_run_id": nilOr(root), "session_id": session, "cost_micros": 5})
		return b
	}
	stays := []json.RawMessage{row("step", 11, 11, ""), row("step", 12, 11, ""), row("approval-brief", 11, 0, ""), row("console", 0, 0, "abc123abc123")}
	moves := []json.RawMessage{row("step", 20, 20, ""), row("console", 0, 0, "def456def456"), row("narrative", 0, 0, ""), row("simple-change", 0, 0, "")}
	got, stayed, err := Moving(append(append([]json.RawMessage{}, stays...), moves...), []ids.ID{idstest.N(11)}, []string{"abc123abc123"})
	if err != nil {
		t.Fatal(err)
	}
	if stayed != len(stays) || len(got) != len(moves) {
		t.Fatalf("%d stayed, %d moved: %s", stayed, len(got), got)
	}
	for i := range moves {
		if string(got[i]) != string(moves[i]) {
			t.Fatalf("row %d: %s", i, got[i])
		}
	}
}
