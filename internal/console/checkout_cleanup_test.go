package console_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/console"
	"github.com/flokkit/apptown/internal/console/consoletest"
)

func TestFailedCheckoutCleanupRetainsAnEndedRetryAcrossRestart(t *testing.T) {
	ctx := context.Background()
	data, checkout := t.TempDir(), t.TempDir()
	settled := 0
	agent := consoletest.NewAgent()
	chats := console.New(console.Dependencies{Agent: agent, Config: console.Config{DataDir: data}, Settle: func(context.Context, *console.Session) error { settled++; return nil }})
	// A final dot is rejected by RemoveAll even as root. This models a real
	// removal error without relying on permissions or injecting filesystem IO.
	sess, err := chats.Open(ctx, console.OpenRequest{Scope: "project:cleanup", Workspace: console.Workspace{Path: checkout + string(os.PathSeparator) + ".", Owned: true}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := chats.End(ctx, sess.ID); err == nil {
		t.Fatal("failed owned checkout removal reported successful End")
	}
	if _, err := chats.Get(ctx, sess.ID); !errors.Is(err, console.ErrNoSession) {
		t.Fatalf("ended chat stayed live: %v", err)
	}
	if agent.Exists(sess.ID) || settled != 1 {
		t.Fatalf("end did not stop and settle once: exists=%v settled=%d", agent.Exists(sess.ID), settled)
	}
	archive := filepath.Join(sess.Dir, console.EndedFile)
	if _, err := os.Stat(archive); err != nil {
		t.Fatalf("cleanup obligation lost: %v", err)
	}
	restarted := console.New(console.Dependencies{Agent: agent, Config: console.Config{DataDir: data}, Settle: func(context.Context, *console.Session) error { t.Error("archive retry settled again"); return nil }})
	if err := restarted.Remove(ctx, sess.ID); err == nil {
		t.Fatal("failed checkout cleanup allowed archive purge")
	}
	if _, err := os.Stat(archive); err != nil {
		t.Fatalf("failed purge lost cleanup obligation: %v", err)
	}
	// Explicitly repair this synthetic fixture. Removing the directory alone
	// cannot cure the final-dot error, which precedes the missing-path check.
	meta := sess.Meta
	meta.Checkout = checkout
	writeCleanupArchive(t, archive, meta)
	if _, err := restarted.End(ctx, sess.ID); err != nil {
		t.Fatalf("retry from persisted ownership: %v", err)
	}
	if _, err := os.Stat(checkout); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retry retained owned checkout: %v", err)
	}
	if agent.Exists(sess.ID) {
		t.Fatal("archive retry restarted runtime")
	}
	if err := restarted.Remove(ctx, sess.ID); err != nil {
		t.Fatalf("purge already cleaned archive: %v", err)
	}
	if _, err := os.Stat(sess.Dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cleaned archive remains: %v", err)
	}
}

func TestBulkPurgeRetainsFailedCheckoutOwnership(t *testing.T) {
	ctx := context.Background()
	data, checkout := t.TempDir(), t.TempDir()
	chats := console.New(console.Dependencies{Agent: consoletest.NewAgent(), Config: console.Config{DataDir: data}})
	sess, err := chats.Open(ctx, console.OpenRequest{Scope: "project:cleanup", Workspace: console.Workspace{Path: checkout, Owned: false}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := chats.End(ctx, sess.ID); err != nil {
		t.Fatal(err)
	}
	// Seed an older ended record with an outstanding removal obligation.
	meta := sess.Meta
	meta.OwnsCheckout = true
	meta.Checkout = checkout + string(os.PathSeparator) + "."
	archive := filepath.Join(sess.Dir, console.EndedFile)
	writeCleanupArchive(t, archive, meta)
	if err := chats.RemoveEnded(console.Match{Scope: meta.Scope}); err == nil {
		t.Fatal("bulk purge discarded failed owned checkout cleanup")
	}
	if _, err := os.Stat(archive); err != nil {
		t.Fatalf("bulk purge lost cleanup obligation: %v", err)
	}
	meta.Checkout = checkout
	writeCleanupArchive(t, archive, meta)
	if err := chats.RemoveEnded(console.Match{Scope: meta.Scope}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{checkout, sess.Dir} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("successful bulk purge retained %s: %v", p, err)
		}
	}
}

func writeCleanupArchive(t *testing.T, path string, meta console.Meta) {
	t.Helper()
	b, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestArchiveCleanupRequiresMatchingEndedOwnership(t *testing.T) {
	for _, kind := range []string{"borrowed", "missing checkout", "mismatched identity", "live record", "unreadable archive"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			data, checkout := t.TempDir(), t.TempDir()
			agent := consoletest.NewAgent()
			chats := console.New(console.Dependencies{Agent: agent, Config: console.Config{DataDir: data}})
			sess, err := chats.Open(ctx, console.OpenRequest{Scope: "project:cleanup", Workspace: console.Workspace{Path: checkout}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := chats.End(ctx, sess.ID); err != nil {
				t.Fatal(err)
			}
			archive := filepath.Join(sess.Dir, console.EndedFile)
			meta := sess.Meta
			meta.OwnsCheckout = kind != "borrowed"
			if kind == "mismatched identity" {
				meta.ID = chats.NewSessionID()
			}
			writeCleanupArchive(t, archive, meta)
			if kind == "live record" {
				writeCleanupArchive(t, filepath.Join(sess.Dir, console.MetaFile), meta)
			}
			if kind == "unreadable archive" {
				if err := os.WriteFile(archive, []byte("{"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "missing checkout" {
				if err := os.RemoveAll(checkout); err != nil {
					t.Fatal(err)
				}
			}
			restarted := console.New(console.Dependencies{Agent: agent, Config: console.Config{DataDir: data}})
			err = restarted.Remove(ctx, sess.ID)
			safe := kind == "borrowed" || kind == "missing checkout"
			if safe && err != nil {
				t.Fatalf("safe cleanup refused: %v", err)
			}
			if !safe && err == nil {
				t.Fatal("cleanup accepted unproven ended ownership")
			}
			if kind != "missing checkout" {
				if _, err := os.Stat(checkout); err != nil {
					t.Fatalf("removed unowned checkout: %v", err)
				}
			}
			if !safe {
				if _, err := os.Stat(archive); err != nil {
					t.Fatalf("removed unproven archive: %v", err)
				}
				// Bulk scanning must not turn a mismatched ID into a different path,
				// or erase malformed/live records while direct removal refuses them.
				_ = restarted.RemoveEnded(console.Match{Scope: meta.Scope})
				if _, err := os.Stat(archive); err != nil {
					t.Fatalf("bulk purge removed unproven archive: %v", err)
				}
				if _, err := os.Stat(checkout); err != nil {
					t.Fatalf("bulk purge removed unproven checkout: %v", err)
				}
			}
		})
	}
}

func TestPurgeCannotOvertakeAnEndingChat(t *testing.T) {
	ctx := context.Background()
	agent := endingAgent{Agent: consoletest.NewAgent(), started: make(chan struct{}), release: make(chan struct{})}
	chats := console.New(console.Dependencies{Agent: agent, Config: console.Config{DataDir: t.TempDir()}})
	checkout := t.TempDir()
	sess, err := chats.Open(ctx, console.OpenRequest{Scope: "project:cleanup", Workspace: console.Workspace{Path: checkout, Owned: true}})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := chats.End(ctx, sess.ID); done <- err }()
	select {
	case <-agent.started:
	case <-time.After(5 * time.Second):
		close(agent.release)
		t.Fatal("end did not reach runtime")
	}
	removeErr := chats.Remove(ctx, sess.ID)
	bulkErr := chats.RemoveEnded(console.Match{Scope: sess.Scope})
	_, recordErr := os.Stat(filepath.Join(sess.Dir, console.MetaFile))
	_, checkoutErr := os.Stat(checkout)
	close(agent.release)
	endErr := <-done
	if removeErr == nil || bulkErr != nil || recordErr != nil || checkoutErr != nil {
		t.Fatalf("purge overtook end: remove=%v bulk=%v record=%v checkout=%v", removeErr, bulkErr, recordErr, checkoutErr)
	}
	if endErr != nil {
		t.Fatal(endErr)
	}
	if err := chats.Remove(ctx, sess.ID); err != nil {
		t.Fatalf("settled archive cannot be purged: %v", err)
	}
}
