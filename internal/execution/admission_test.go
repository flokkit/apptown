package execution_test

import (
	"context"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/execution"
)

func TestAdmissionLineKeepsOrderAndExpiresOnlyUnusedCapacity(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 25, 6, 0, 0, 0, time.UTC)
	line := execution.NewAdmissionLine(1, 40*time.Second, func() time.Time { return now }, nil)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	ask := func(key string, wantAhead int, wantAdmitted bool) {
		t.Helper()
		ahead, admitted, err := line.Admit(ctx, key, func() error { return nil })
		if err != nil || ahead != wantAhead || admitted != wantAdmitted {
			t.Fatalf("%s: %d %t %v", key, ahead, admitted, err)
		}
	}
	ask("holder", 0, true)
	// Only a consumed grant is a holder whose slot work may share.
	if line.Holds("holder") {
		t.Fatal("an unused grant holds a slot")
	}
	if err := line.Consume("holder"); err != nil {
		t.Fatal(err)
	}
	if !line.Holds("holder") || line.Holds("first") {
		t.Fatal("holders misread")
	}
	ask("first", 0, false)
	ask("second", 1, false)
	ask("third", 2, false)
	line.Release("holder")
	if line.Holds("holder") {
		t.Fatal("a released key still holds its slot")
	}
	ask("third", 2, false)
	ask("first", 0, true)
	if err := line.Consume("first"); err != nil {
		t.Fatal(err)
	}
	line.Release("first")
	ask("second", 0, true)
	// Its unused grant expires. Renew the third waiter's place first.
	now = now.Add(30 * time.Second)
	ask("third", 0, false)
	now = now.Add(11 * time.Second)
	ask("third", 0, true)
	if err := line.Consume("second"); err != execution.ErrBusy {
		t.Fatalf("expired grant consumed: %v", err)
	}
	if err := line.Consume("third"); err != nil {
		t.Fatal(err)
	}
	ask("abandoned", 0, false)
	now = now.Add(41 * time.Second)
	ask("next", 0, false) // a live holder has no expiry
	line.Release("third")
	ask("next", 0, true)
}
