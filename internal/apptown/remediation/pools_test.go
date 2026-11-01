package remediation

import (
	"context"
	"errors"
	"testing"
)

// A pool never declared reports so; a declared size is kept with its
// receipt; the bounds hold.
func TestPools(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	p := newPools(db, nil)
	if n, set, err := p.count(context.Background(), "remediation"); err != nil || set || n != 0 {
		t.Fatalf("undeclared: %v %v %d", err, set, n)
	}
	if err := p.set(context.Background(), "remediation", maxWorkers+1, "bls"); !errors.Is(err, errPoolSize) {
		t.Fatalf("over the bound: %v", err)
	}
	if err := p.set(context.Background(), "remediation", -1, "bls"); !errors.Is(err, errPoolSize) {
		t.Fatalf("under the bound: %v", err)
	}
	if err := p.set(context.Background(), "remediation", 0, "bls"); err != nil {
		t.Fatal(err)
	}
	if n, set, _ := p.count(context.Background(), "remediation"); !set || n != 0 {
		t.Fatalf("zero is a declaration (paused): %v %d", set, n)
	}
	if err := p.set(context.Background(), "remediation", 3, "bls"); err != nil {
		t.Fatal(err)
	}
	if n, set, _ := p.count(context.Background(), "remediation"); !set || n != 3 {
		t.Fatalf("declared: %v %d", set, n)
	}
	set := 0
	for _, r := range receipts(t, db, "workers.set") {
		if r.Subject == "remediation" {
			set++
		}
	}
	if set != 2 {
		t.Fatalf("receipts: %d", set)
	}
}
