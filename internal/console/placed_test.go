package console_test

import (
	"context"
	"errors"
	"testing"

	"flokkit.io/forge/internal/console"
	"flokkit.io/forge/internal/console/consoletest"
)

// Each session's runtime is the one its record places it on, for every
// call, ended sessions included; a record that cannot be read fails the call
// rather than asking the wrong runtime (which could answer "gone").
func TestAPlacedSessionIsAskedWhereItRuns(t *testing.T) {
	ctx := context.Background()
	local, remote := consoletest.NewAgent(), consoletest.NewAgent()
	unreadable := errors.New("record unreadable")
	p := console.Placed{Local: local, Remote: remote, OnRemote: func(id string) (bool, error) {
		switch id {
		case "remote1":
			return true, nil
		case "broken1":
			return false, unreadable
		}
		return false, nil
	}}
	for _, id := range []string{"local1", "remote1"} {
		if err := p.Start(ctx, console.Launch{ID: id}); err != nil {
			t.Fatal(err)
		}
	}
	if !local.Exists("local1") || local.Exists("remote1") || !remote.Exists("remote1") || remote.Exists("local1") {
		t.Fatal("a session started on the wrong runtime")
	}
	if err := p.End(ctx, "remote1"); err != nil || remote.Exists("remote1") {
		t.Fatalf("ending: %v", err)
	}
	if st, err := p.Status(ctx, "remote1"); err != nil || st.Exists {
		t.Fatalf("an ended remote session: %+v %v", st, err)
	}
	if _, err := p.Status(ctx, "broken1"); !errors.Is(err, unreadable) {
		t.Fatalf("an unreadable record: %v", err)
	}
}

// reapRecorder remembers which sessions its runtime was told are alive.
type reapRecorder struct {
	*consoletest.Agent
	told map[string]bool
}

func (r *reapRecorder) Reap(ctx context.Context, alive map[string]bool) error {
	r.told = alive
	return r.Agent.Reap(ctx, alive)
}

// The remote runtime resumes watching every live session it is told of, so
// it is told only of those placed on it, or whose placement cannot be read
// (asking is harmless: it answers that it has no such session).
func TestTheRemoteRuntimeIsToldOnlyOfItsOwnSessions(t *testing.T) {
	local, remote := &reapRecorder{Agent: consoletest.NewAgent()}, &reapRecorder{Agent: consoletest.NewAgent()}
	p := console.Placed{Local: local, Remote: remote, OnRemote: func(id string) (bool, error) {
		switch id {
		case "remote1":
			return true, nil
		case "broken1":
			return false, errors.New("record unreadable")
		}
		return false, nil
	}}
	alive := map[string]bool{"local1": true, "remote1": true, "broken1": true}
	if err := p.Reap(context.Background(), alive); err != nil {
		t.Fatal(err)
	}
	if len(local.told) != 3 {
		t.Fatalf("the local runtime was told of %v", local.told)
	}
	if len(remote.told) != 2 || !remote.told["remote1"] || !remote.told["broken1"] {
		t.Fatalf("the remote runtime was told of %v", remote.told)
	}
}

// An AppTown that runs no chats (-no-local-docker) refuses waking a chat it
// once ran, by name, while its record stays readable; ending it and judging
// it gone ask nothing of a runtime there is not.
func TestAForgeWithNoLocalChatsRefusesWakingOne(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	before := console.New(console.Dependencies{Agent: consoletest.NewAgent(), Config: console.Config{DataDir: dir}})
	s, err := before.Open(ctx, console.OpenRequest{Scope: "change:1", Workspace: console.Workspace{Path: t.TempDir()}, Actor: "bls"})
	if err != nil {
		t.Fatal(err)
	}
	after := console.New(console.Dependencies{Agent: console.NoLocal{}, Config: console.Config{DataDir: dir}})
	if err := after.Resume(ctx); err != nil {
		t.Fatal(err)
	}
	if live, err := after.Unended(); err != nil || len(live) != 1 || live[0].ID != s.ID {
		t.Fatalf("the unended chats: %+v %v", live, err)
	}
	_, attachErr := after.Attach(ctx, s.ID, console.Size{})
	for what, err := range map[string]error{"warm": after.Warm(ctx, s.ID), "send": after.Send(ctx, s.ID, "hello"), "attach": attachErr} {
		if !errors.Is(err, console.ErrNoLocalChats) {
			t.Fatalf("%s: %v", what, err)
		}
	}
	if meta, err := after.Recorded(s.ID); err != nil || meta.Scope != "change:1" {
		t.Fatalf("the chat's record: %+v %v", meta, err)
	}
	if _, err := after.End(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	if gone, err := after.Gone(ctx, s.ID); err != nil || !gone {
		t.Fatalf("an ended chat on an AppTown with none: gone=%v %v", gone, err)
	}
	if live, err := after.Unended(); err != nil || len(live) != 0 {
		t.Fatalf("after its end: %+v %v", live, err)
	}
}
