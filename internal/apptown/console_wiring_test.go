package apptown

import (
	"context"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/console"
	"github.com/flokkit/apptown/internal/console/consoletest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// The rehearse loop and the five-minute round are stoppable: ending a
// session returns its loop at once, and StopConsoles returns every loop and
// the round without touching the sessions themselves.
func TestConsoleLoopsStop(t *testing.T) {
	t.Parallel()
	a := testApp(t, Config{ConsoleAgent: consoletest.NewAgent()})
	s := a.Console.Restore(consoleMetaFor("loop1", idstest.N(1), idstest.N(5)))
	finished := make(chan struct{})
	go func() {
		a.changeConsole.Rehearse(context.Background(), s)
		close(finished)
	}()
	if _, err := a.Console.End(context.Background(), s.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("rehearseLoop must return when the session ends")
	}

	roundDone := make(chan struct{})
	go func() {
		a.consoleRounds(context.Background(), a.consoleStop)
		close(roundDone)
	}()
	s2 := a.Console.Restore(consoleMetaFor("loop2", idstest.N(1), idstest.N(6)))
	loop2 := make(chan struct{})
	go func() {
		a.changeConsole.Rehearse(context.Background(), s2)
		close(loop2)
	}()
	a.StopConsoles(context.Background())
	for name, ch := range map[string]chan struct{}{"round": roundDone, "rehearse loop": loop2} {
		select {
		case <-ch:
		case <-time.After(3 * time.Second):
			t.Fatalf("%s must return on StopConsoles", name)
		}
	}
	// Sessions survive a stop: the registry is untouched for the next recovery.
	if _, err := a.Console.Get(context.Background(), "loop2"); err != nil {
		t.Error("StopConsoles must not remove sessions")
	}
}

// An idle session is stopped by the round; an ended one is not touched.
func TestConsoleIdleStopSkipsEndedSessions(t *testing.T) {
	t.Parallel()
	agent := consoletest.NewAgent()
	a := testApp(t, Config{ConsoleAgent: agent})
	m := consoleMetaFor("idle1", idstest.N(1), ids.Nil)
	m.Checkout = t.TempDir()
	s := a.Console.Restore(m)
	if err := agent.Start(context.Background(), console.Launch{ID: s.ID, Dir: s.Dir}); err != nil {
		t.Fatal(err)
	}
	s.SetLastUsed(time.Now().Add(-time.Hour))
	a.consoleIdleStop(context.Background())
	if agent.Running(s.ID) {
		t.Fatal("the idle round did not stop the agent")
	}
	if _, err := a.Console.Get(context.Background(), s.ID); err != nil {
		t.Fatal("the idle round forgot the session")
	}
}
