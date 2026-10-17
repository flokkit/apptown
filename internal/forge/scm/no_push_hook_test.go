package scm

import (
	"os"
	"path/filepath"
	"testing"
)

// The hub's no-push hook is what keeps the gated landing the only way to
// main: a hub whose hook cannot be written is an error, never a silent
// success, both for a new repository and for the backfill at boot.
func TestAHubWhoseNoPushHookCannotBeWrittenIsAnError(t *testing.T) {
	t.Parallel()
	hub := filepath.Join(t.TempDir(), "hub")
	if err := InitializeRepository(t.Context(), hub, "main"); err != nil {
		t.Fatal(err)
	}
	hook := filepath.Join(hub, ".git", "hooks", "pre-receive")
	if _, err := os.Stat(hook); err != nil {
		t.Fatalf("a new hub has no pre-receive hook: %v", err)
	}
	// Fixture: the hooks folder replaced by a file, so no hook can be
	// written under it.
	hooks := filepath.Join(hub, ".git", "hooks")
	if err := os.RemoveAll(hooks); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hooks, []byte("not a folder"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := InstallNoPushHook(hub); err == nil {
		t.Fatal("installing the hook under an unwritable hooks folder reported success")
	}
	if err := InitializeRepository(t.Context(), hub, "main"); err == nil {
		t.Fatal("initializing a hub whose hook cannot be written reported success")
	}
}
