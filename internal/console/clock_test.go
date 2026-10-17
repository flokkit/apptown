package console_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"flokkit.io/forge/internal/console"
	"flokkit.io/forge/internal/console/consoletest"
)

// skewed is a clock a test moves: it starts far from the real one, so a
// rule that read the real clock instead would decide differently.
type skewed struct {
	mu sync.Mutex
	at time.Time
}

func (c *skewed) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *skewed) advance(d time.Duration) {
	c.mu.Lock()
	c.at = c.at.Add(d)
	c.mu.Unlock()
}

// A chat's activity is judged on the console's clock: it is stamped from
// it, a project with a chat used within ten minutes of it cannot be
// deleted, and a chat used within a window of it is busy. Only the
// console's clock moves here.
func TestAChatsActivityIsJudgedOnTheConsolesClock(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	clock := &skewed{at: time.Date(2031, 3, 1, 9, 0, 0, 0, time.UTC)}
	chats := console.New(console.Dependencies{Agent: consoletest.NewAgent(), Config: console.Config{DataDir: t.TempDir(), Now: clock.now}})
	s, err := chats.Open(ctx, console.OpenRequest{Scope: "project:p", Labels: map[string]string{"project": "7"}, Workspace: console.Workspace{Path: "/tmp/p"}, Actor: "bls"})
	if err != nil {
		t.Fatal(err)
	}
	if !s.CreatedAt.Equal(clock.now()) || !s.LastUsed().Equal(clock.now()) {
		t.Fatalf("created %v, last used %v; the console's clock says %v", s.CreatedAt, s.LastUsed(), clock.now())
	}
	match := console.Match{Scope: "project:p"}
	if !chats.Busy(ctx, match, 5*time.Minute) {
		t.Fatal("a chat just used is not busy")
	}
	if _, err := chats.Block("project", "7"); err == nil {
		t.Fatal("a project with a chat just used was deleted")
	}
	clock.advance(11 * time.Minute)
	if chats.Busy(ctx, match, 5*time.Minute) {
		t.Fatal("a chat unused for eleven minutes of the console's clock is busy")
	}
	release, err := chats.Block("project", "7")
	if err != nil {
		t.Fatalf("a chat unused for eleven minutes of the console's clock still blocks the deletion: %v", err)
	}
	release()
}
