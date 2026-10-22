package console_test

import (
	"context"
	"errors"
	"testing"

	"github.com/flokkit/apptown/internal/console"
	"github.com/flokkit/apptown/internal/console/consoletest"
)

// startCancelling is the fake agent whose start fails because its caller
// went away; it records the context its removal was asked with.
type startCancelling struct {
	*consoletest.Agent
	cancel    context.CancelFunc
	removedOn error
	removed   bool
}

func (a *startCancelling) Start(ctx context.Context, in console.Launch) error {
	if err := a.Agent.Start(ctx, in); err != nil {
		return err
	}
	a.cancel()
	return context.Canceled
}

func (a *startCancelling) Remove(ctx context.Context, id string) error {
	a.removed, a.removedOn = true, ctx.Err()
	return a.Agent.Remove(ctx, id)
}

// A chat whose start fails because its caller went away removes what it
// started all the same: the removal is not cancelled with the caller.
func TestAFailedOpenRemovesWhatItStartedThoughTheCallerHasGone(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	agent := &startCancelling{Agent: consoletest.NewAgent(), cancel: cancel}
	chats := console.New(console.Dependencies{Agent: agent, Config: console.Config{DataDir: t.TempDir()}})
	_, err := chats.Open(ctx, console.OpenRequest{Scope: "project:p", Workspace: console.Workspace{Path: t.TempDir(), Owned: true}, Actor: "bls", Model: "m"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("open: %v", err)
	}
	if !agent.removed || agent.removedOn != nil {
		t.Fatalf("removed %v, on a context that was %v", agent.removed, agent.removedOn)
	}
}
