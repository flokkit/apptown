package deployment

import (
	"context"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/clock/clocktest"
	"github.com/flokkit/apptown/internal/database"
)

func newestReceiptAt(t *testing.T, db *database.DB, kind string) time.Time {
	t.Helper()
	got, err := audit.Recent(context.Background(), db, audit.Filter{KindPrefix: kind})
	if err != nil || len(got) == 0 {
		t.Fatalf("%s receipts: %+v %v", kind, got, err)
	}
	at, err := time.Parse(time.RFC3339Nano, got[0].At)
	if err != nil {
		t.Fatal(err)
	}
	return at
}

// A deployment's binding takes one reading of AppTown's clock for the
// deployment it records and for its receipt. On a clock that moves on a
// second at every reading, a separate reading for the receipt would say a
// later second.
func TestADeploymentsBindingReceiptCarriesItsReading(t *testing.T) {
	t.Parallel()
	s, _, in, _ := configurationFixture(t)
	clk := clocktest.NewStepping(time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC), time.Second)
	s.clock = clk.Now
	reading := clk.Next()
	configureForTest(t, s, in)
	if at := newestReceiptAt(t, s.db, "deployment.bound"); !at.Equal(reading) {
		t.Fatalf("deployment.bound at %v, the binding's reading %v", at, reading)
	}
}
