package console_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/flokkit/apptown/internal/console"
	"github.com/flokkit/apptown/internal/console/consoletest"
)

// A chat continuing a lane's agent from the record its owner kept writes
// those bytes as that session's file and resumes that session; a session
// name that could leave the directory is refused.
func TestAChatResumesAKeptRecordAsItsSession(t *testing.T) {
	ctx := context.Background()
	chats := console.New(console.Dependencies{Agent: consoletest.NewAgent(), Config: console.Config{DataDir: t.TempDir()}})
	kept := []byte(`{"type":"session","version":3,"id":"step-4-a1"}` + "\n" + `{"type":"message"}` + "\n")
	open := console.OpenRequest{Scope: "change:1", Workspace: console.Workspace{Path: t.TempDir()}, Actor: "bls",
		Resume: &console.Resumption{Record: kept, Session: "step-4-a1", Label: "the lane's agent"}}
	s, err := chats.Open(ctx, open)
	if err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(s.Dir, "sessions", "*_step-4-a1.jsonl"))
	if len(files) != 1 || s.AgentSession != "step-4-a1" {
		t.Fatalf("resumed %v as %q", files, s.AgentSession)
	}
	if got, err := os.ReadFile(files[0]); err != nil || string(got) != string(kept) {
		t.Fatalf("record %q %v", got, err)
	}
	open.Resume = &console.Resumption{Record: kept, Session: "../escape"}
	if _, err := chats.Open(ctx, open); err == nil {
		t.Fatal("a session name leaving the directory was accepted")
	}
}
