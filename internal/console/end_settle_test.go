package console_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/console"
	"github.com/flokkit/apptown/internal/console/consoletest"
)

// lastWords is a runtime whose end writes the record one last time, as a
// worker's end copies its record back after stopping the agent.
type lastWords struct {
	*consoletest.Agent
	dir string
}

func (a *lastWords) End(ctx context.Context, id string) error {
	if err := a.Agent.End(ctx, id); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(a.dir, id, "sessions", "2026-10-24T00-00-00.jsonl"), []byte("the last turn\n"), 0o600)
}

// An end settles the session after its runtime's last record is in, and
// before the record is archived or removed: a settle that fails keeps the
// session registered and its live record, and removes nothing.
func TestAnEndSettlesTheLastRecordBeforeArchiving(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	data := t.TempDir()
	agent := &lastWords{Agent: consoletest.NewAgent(), dir: filepath.Join(data, "console")}
	var seen string
	failing := errors.New("spend not recorded")
	var settleErr error
	chats := console.New(console.Dependencies{Agent: agent, Config: console.Config{DataDir: data},
		Settle: func(_ context.Context, s *console.Session) error {
			files, err := s.Records()
			if err != nil {
				return err
			}
			for _, f := range files {
				b, err := os.ReadFile(f)
				if err != nil {
					return err
				}
				seen += string(b)
			}
			return settleErr
		}})
	s, err := chats.Open(ctx, console.OpenRequest{Scope: "change:1", Workspace: console.Workspace{Path: t.TempDir(), Writable: true}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(s.Dir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	settleErr = failing
	if _, err := chats.End(ctx, s.ID); !errors.Is(err, failing) {
		t.Fatalf("a failed settle ended the chat: %v", err)
	}
	if !strings.Contains(seen, "the last turn") {
		t.Fatalf("settled before the runtime's last record: %q", seen)
	}
	if _, err := chats.Get(ctx, s.ID); err != nil {
		t.Fatalf("a chat whose settle failed is no longer registered: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.Dir, console.MetaFile)); err != nil {
		t.Fatalf("a chat whose settle failed was archived: %v", err)
	}
	if err := chats.Remove(ctx, s.ID); !errors.Is(err, failing) {
		t.Fatalf("a failed settle let the chat be removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.Dir, "sessions")); err != nil {
		t.Fatalf("a failed settle removed the records: %v", err)
	}
	settleErr = nil
	if _, err := chats.End(ctx, s.ID); err != nil {
		t.Fatalf("ending it again: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.Dir, console.EndedFile)); err != nil {
		t.Fatalf("a settled end is archived: %v", err)
	}
}
