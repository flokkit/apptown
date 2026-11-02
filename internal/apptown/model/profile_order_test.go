package model

import (
	"testing"

	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
)

// Every profile gets its whole chain before borrowing any standard cell.
// The returned coordinates also describe eval arm selections and receipts.
func TestProfileChainBeforeStandardChain(t *testing.T) {
	t.Parallel()
	s := New(testDB(t))
	if err := s.SeedFallbacks(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, c := range [][3]string{
		{"main-strong", "standard", "vendor/standard"},
		{"review", "standard", "vendor/review"},
		{"main-fast", "standard", "vendor/quick"},
		{"main-strong", "high", "vendor/high"},
		{"main-fast", "luna", "vendor/luna-quick"},
	} {
		if err := s.Set(t.Context(), c[0], c[1], c[2], "fixture"); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range [][5]string{
		{"high", "security", "vendor/high", "main-strong", "high"},
		{"high", "fast", "vendor/high", "main-strong", "high"},
		{"high", "", "vendor/high", "main-strong", "high"},
		{"luna", "scout", "vendor/luna-quick", "main-fast", "luna"},
		{"luna", "security", "vendor/review", "review", "standard"},
		{"standard", "security", "vendor/review", "review", "standard"},
		{"standard", "fast", "vendor/quick", "main-fast", "standard"},
	} {
		p, slot, profile, err := s.resolve(t.Context(), c[0], c[1])
		if err != nil || p != c[2] || slot != c[3] || profile != c[4] {
			t.Errorf("%s/%s: %q %q %q (%v), want %v", c[0], c[1], p, slot, profile, err, c[2:])
		}
	}
	arms, err := NewSelection(s).ArmSlots(t.Context(), ids.Nil, "high")
	if err != nil || arms["review"] != "vendor/high" || arms["fast"] != "vendor/high" {
		t.Fatalf("eval arm: %v %v", arms, err)
	}
}

func TestCyclesStillStopEachProfileWalk(t *testing.T) {
	t.Parallel()
	s := New(testDB(t))
	// sql-guarantee: operator-created cycles have no mutation operation.
	if _, err := s.db.SQL().ExecContext(t.Context(), `INSERT INTO slot_fallbacks(slot,fallback) VALUES ('a','b'),('b','a')`); err != nil {
		t.Fatal(err)
	}
	if err := s.Set(t.Context(), "b", "standard", "vendor/standard", "fixture"); err != nil {
		t.Fatal(err)
	}
	if p, slot, profile, err := s.resolve(t.Context(), "high", "a"); err != nil || p != "vendor/standard" || slot != "b" || profile != "standard" {
		t.Fatalf("cycle then standard: %q %q %q %v", p, slot, profile, err)
	}
	if err := s.Set(t.Context(), "b", "standard", "", "fixture"); err != nil {
		t.Fatal(err)
	}
	if p, slot, profile, err := s.resolve(t.Context(), "high", "a"); err != nil || p != "" || slot != "main-strong" || profile != "" {
		t.Fatalf("unmapped cycle: %q %q %q %v", p, slot, profile, err)
	}
}

// A broken profile fallback read cannot borrow a standard task cell early.
func TestProfileFallbackReadFailureDoesNotBorrowStandard(t *testing.T) {
	t.Parallel()
	s := New(testDB(t))
	if err := s.Set(t.Context(), "review", "standard", "vendor/review", "fixture"); err != nil {
		t.Fatal(err)
	}
	restore := dbtest.NextStatementFailure(s.db, "FROM slot_fallbacks")
	defer restore()
	if p, _, _, err := s.resolve(t.Context(), "high", "review"); err == nil {
		t.Fatalf("failed profile walk borrowed %q", p)
	}
}
