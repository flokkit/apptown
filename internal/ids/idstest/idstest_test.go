package idstest_test

import (
	"testing"

	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
)

func TestNIsAStableOrderedID(t *testing.T) {
	if got := idstest.N(7).String(); got != "00000000-0007-7000-8000-000000000000" {
		t.Fatalf("N(7) = %s", got)
	}
	if back, err := ids.Parse(idstest.N(7).String()); err != nil || back != idstest.N(7) {
		t.Fatalf("N(7) does not round-trip: %v %v", back, err)
	}
	if idstest.N(7).Compare(idstest.N(8)) >= 0 || idstest.N(1).IsZero() {
		t.Fatal("N does not order by n, or N(1) is Nil")
	}
}
