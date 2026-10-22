package evals

import (
	"context"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/usage"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

// An eval's acknowledgement of unknown spend covers only the unknown
// charges the starter saw, read through the real usage ledger with the
// AppTown's own model calls opened and settled as the gateway does:
//
//  1. an unknown charge X holds the eval, and the starter continues;
//  2. call A, still pending during a pass, holds nothing;
//  3. A settles complete, and the acknowledged count does not drop;
//  4. a different call B settles unknown: the next arm holds until the
//     starter continues again;
//  5. call C's settlement never comes: once its time runs out it holds the
//     eval, and the continue that acknowledges it locks it in, so a late
//     settlement cannot make it known.
//
// Kills: a pending call holding every eval in flight; an acknowledgement
// that a settlement can later shrink, so it covers B unseen; an expired
// call never counted.
func TestAnAcknowledgementCoversOnlyTheUnknownSpendSeen(t *testing.T) {
	t.Parallel()
	w := buildEvalWorld(t, 5, func(db *database.DB) Spend { return usage.New(db) })
	requests := usage.NewRequests(w.db)
	e := w.start(t, "alice")
	pass := func() *Eval {
		t.Helper()
		// End the working arms, so the pass asks to start the next.
		for _, a := range w.eval(t, e.ID).Arms {
			if a.State == ArmWorking {
				w.work.runs[a.ChangeID] = []*workflow.Run{{ID: a.RunID, Status: workflow.RunAbandoned}}
			}
		}
		if err := w.service.Pass(context.Background()); err != nil {
			t.Fatal(err)
		}
		return w.eval(t, e.ID)
	}
	proceed := func() {
		t.Helper()
		if _, err := w.service.Continue(as("alice"), e.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.service.Pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	var copyID ids.ID
	for _, a := range w.eval(t, e.ID).Arms {
		if !a.ProjectID.IsZero() {
			copyID = a.ProjectID
		}
	}
	if copyID.IsZero() {
		t.Fatal("no copy was made")
	}
	open := func(pending time.Duration) ids.ID {
		t.Helper()
		id, err := requests.OpenCall(context.Background(), usage.Request{Kind: "forge", Actor: "apptown-system", ProjectID: copyID, Provider: "native", Model: "judge"},
			usage.Entry{Kind: "guidance-checklist", ProjectID: copyID, Actor: "apptown-system", Provider: "native", Model: "judge"}, pending)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	settle := func(id ids.ID, incomplete string) bool {
		t.Helper()
		settled, err := requests.SettleCall(context.Background(), id, 200, "complete", "{}", usage.Totals{Input: 1, Total: 1, CostMicros: 10}, incomplete)
		if err != nil {
			t.Fatal(err)
		}
		return settled
	}

	settle(open(time.Hour), "the provider reported no usage") // X
	if got := pass(); got.State != StateHeld {
		t.Fatalf("1: unknown spend holds the eval: %+v", got)
	}
	proceed()

	a := open(time.Hour)
	if got := pass(); got.State != StateRunning {
		t.Fatalf("2: a call in flight holds nothing: %+v", got)
	}
	settle(a, "")
	if got := pass(); got.State != StateRunning {
		t.Fatalf("3: a call settling complete holds nothing: %+v", got)
	}

	settle(open(time.Hour), "the provider reported no usage") // B
	if got := pass(); got.State != StateHeld {
		t.Fatalf("4: a later unknown charge holds the eval again: %+v", got)
	}
	proceed()

	// A time of zero has run out by the next statement on the database's
	// own clock: a settlement that never comes.
	c := open(0)
	if got := pass(); got.State != StateHeld {
		t.Fatalf("5: a call whose settlement never came holds the eval: %+v", got)
	}
	proceed()
	// The continue locked C in as unknown, so it can never become known.
	if seen, ok, err := usage.New(w.db).Observed(context.Background(), usage.CallKey(c)); err != nil || !ok || seen.Incomplete != usage.NeverSettled {
		t.Fatalf("5: the acknowledged call is locked in: %+v %v %v", seen, ok, err)
	}
	if settle(c, "") {
		t.Fatal("5: a settlement after the acknowledgement made acknowledged spend known")
	}
	if got := pass(); got.State != StateRunning {
		t.Fatalf("5: acknowledged, the eval runs on: %+v", got)
	}
}
