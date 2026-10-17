package console_test

import (
	"context"
	"testing"
	"time"

	"flokkit.io/forge/internal/console"
	"flokkit.io/forge/internal/console/consoletest"
)

// Pause a wake after its ended check but before the runtime starts.
// End/Gone must not prove cessation while that admitted launch can run.
type delayedStart struct {
	*consoletest.Agent
	entered chan struct{}
	release chan struct{}
}

func (a *delayedStart) Start(ctx context.Context, l console.Launch) error {
	if a.entered != nil {
		close(a.entered)
		<-a.release
	}
	return a.Agent.Start(ctx, l)
}
func TestAChatCannotBeEndedWhileItsAgentIsStarting(t *testing.T) {
	ctx := context.Background()
	agent := &delayedStart{Agent: consoletest.NewAgent()}
	chats := console.New(console.Dependencies{Agent: agent, Config: console.Config{DataDir: t.TempDir()}})
	s, err := chats.Open(ctx, console.OpenRequest{Scope: "change:1", Workspace: console.Workspace{Path: t.TempDir(), Writable: true}})
	if err != nil {
		t.Fatal(err)
	}
	agent.entered = make(chan struct{})
	agent.release = make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- chats.Warm(ctx, s.ID) }()
	select {
	case <-agent.entered:
	case <-time.After(10 * time.Second):
		close(agent.release)
		t.Fatal("wake did not reach launch")
	}
	_, endErr := chats.End(ctx, s.ID)
	gone, goneErr := chats.Gone(ctx, s.ID)
	close(agent.release)
	wakeErr := <-done
	if endErr == nil && goneErr == nil && gone && agent.Exists(s.ID) {
		t.Fatalf("End and Gone confirmed cessation, then admitted wake recreated writer: warm=%v", wakeErr)
	}
}
