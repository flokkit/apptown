package console_test

import (
	"context"
	"errors"
	"testing"

	"github.com/flokkit/apptown/internal/console"
	"github.com/flokkit/apptown/internal/console/consoletest"
)

// Watching a chat's terminal shows its screen and changes nothing: it does
// not wake a stopped agent, takes no keystrokes, and is not a viewer that
// keeps the chat from idling.
func TestWatchingATerminalShowsItAndChangesNothing(t *testing.T) {
	ctx := context.Background()
	agent := consoletest.NewAgent()
	chats := console.New(console.Dependencies{Agent: agent, Config: console.Config{DataDir: t.TempDir()}})
	s, err := chats.Open(ctx, console.OpenRequest{Scope: "change:1", Workspace: console.Workspace{Path: t.TempDir(), Writable: true}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = chats.End(context.Background(), s.ID) })
	if err := chats.Stop(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := chats.Watch(ctx, s.ID); !errors.Is(err, console.ErrAsleep) || agent.Running(s.ID) {
		t.Fatalf("watching a stopped chat: %v, running %v", err, agent.Running(s.ID))
	}
	if err := chats.Warm(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	agent.Screen(s.ID, []byte("hello"))
	term, err := chats.Watch(ctx, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	buf := make([]byte, 16)
	if n, err := term.Read(buf); err != nil || string(buf[:n]) != "hello" {
		t.Fatalf("the watcher reads the screen: %q %v", buf[:n], err)
	}
	if _, err := term.Write([]byte("rm -rf /\r")); !errors.Is(err, console.ErrWatching) {
		t.Fatalf("a watcher's keystrokes: %v", err)
	}
	if s.Viewers() != 0 {
		t.Fatalf("a watcher counts as a viewer: %d", s.Viewers())
	}
	// Only the screen's own bytes: nothing the watcher typed reached it.
	typing, err := chats.Attach(ctx, s.ID, console.Size{})
	if err != nil {
		t.Fatal(err)
	}
	defer typing.Close()
	if n, _ := typing.Read(buf); string(buf[:n]) != "hello" {
		t.Fatalf("the screen holds %q", buf[:n])
	}
}
