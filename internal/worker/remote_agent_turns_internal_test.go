package worker

import (
	"context"
	"testing"

	"github.com/flokkit/apptown/internal/console"
	"github.com/flokkit/apptown/internal/console/consoletest"
)

// A handed-back turn triggers one save however many times its record is
// copied: the turn is reported once, and the next turn is reported again.
func TestAHandedBackTurnIsReportedOncePerTurn(t *testing.T) {
	t.Parallel()
	agent := consoletest.NewAgent()
	in := console.Launch{ID: "c1", Dir: t.TempDir(), AgentSession: "s1"}
	if err := agent.Start(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	tr := &console.Transcript{Dir: in.Dir}
	var turns handedBackTurns
	if turns.next(tr) {
		t.Fatal("a session with no turn handed back was reported")
	}
	agent.Reply("c1", "done")
	if !turns.next(tr) {
		t.Fatal("a handed-back turn was not reported")
	}
	if turns.next(tr) || turns.next(&console.Transcript{Dir: in.Dir}) {
		t.Fatal("the same turn, copied again, was reported again")
	}
	if err := agent.Deliver(context.Background(), in, "and again"); err != nil {
		t.Fatal(err)
	}
	agent.Reply("c1", "done again")
	if !turns.next(tr) {
		t.Fatal("the next handed-back turn was not reported")
	}
}
