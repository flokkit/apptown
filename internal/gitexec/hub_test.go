package gitexec

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"flokkit.io/forge/internal/execfile"
)

// Git in the forge's own repositories runs hardened: an override in the
// forge's own environment (GIT_DIR, GIT_OBJECT_DIRECTORY) does not redirect
// it, a hook does not run, and a ref transaction moves the named ref in the
// named repository only. Not parallel: it sets the process's environment.
func TestGitInTheHubIgnoresTheEnvironmentAndHooks(t *testing.T) {
	root := t.TempDir()
	hub, elsewhere := filepath.Join(root, "hub"), filepath.Join(root, "elsewhere")
	for _, dir := range []string{hub, elsewhere} {
		gitAt(t, root, "init", "-q", "-b", "main", dir)
		gitAt(t, dir, "commit", "-q", "--allow-empty", "-m", "base of "+filepath.Base(dir))
	}
	want := gitAt(t, hub, "rev-parse", "HEAD")
	fired := filepath.Join(root, "fired")
	hook := "#!/bin/sh\ntouch " + fired + "\n"
	if err := execfile.Write(filepath.Join(hub, ".git", "hooks", "reference-transaction"), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_DIR", filepath.Join(elsewhere, ".git"))
	t.Setenv("GIT_OBJECT_DIRECTORY", filepath.Join(elsewhere, ".git", "objects"))
	ctx := context.Background()
	tip := HubRef(ctx, hub, "refs/heads/main")
	if tip != want {
		t.Fatalf("the hub's tip was read elsewhere: %q", tip)
	}
	if read, err := HubReadRef(ctx, hub, "refs/heads/main"); err != nil || read != want {
		t.Fatalf("the hub's tip, read error-aware, = %q %v", read, err)
	}
	if out, err := HubRun(ctx, hub, "update-ref", "refs/apptown/candidates/x", tip); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	txn, err := PrepareUpdate(ctx, hub, "refs/apptown/candidates/y", tip, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := txn.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"refs/apptown/candidates/x", "refs/apptown/candidates/y"} {
		if got := HubRef(ctx, hub, ref); got != tip {
			t.Fatalf("%s in the hub = %q", ref, got)
		}
	}
	if _, err := os.Stat(filepath.Join(elsewhere, ".git", "refs", "forge")); !os.IsNotExist(err) {
		t.Fatalf("a ref was written in the repository the environment named: %v", err)
	}
	if _, err := os.Stat(fired); !os.IsNotExist(err) {
		t.Fatalf("the hub's hook ran: %v", err)
	}
}

// HubReadRef tells a ref that names nothing (no error) from a read that
// failed: a broken ref, or a folder that is no repository, is an error,
// never an absent ref.
func TestAHubRefReadTellsMissingFromFailed(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	hub := filepath.Join(root, "hub")
	gitAt(t, root, "init", "-q", "-b", "main", hub)
	gitAt(t, hub, "commit", "-q", "--allow-empty", "-m", "base")
	ctx := context.Background()
	if got, err := HubReadRef(ctx, hub, "refs/heads/absent"); err != nil || got != "" {
		t.Fatalf("a missing ref = %q %v; want none, no error", got, err)
	}
	if err := os.WriteFile(filepath.Join(hub, ".git", "refs", "heads", "broken"), []byte("not an object\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := HubReadRef(ctx, hub, "refs/heads/broken"); err == nil {
		t.Fatalf("a broken ref = %q with no error", got)
	}
	if got, err := HubReadRef(ctx, filepath.Join(root, "nowhere"), "refs/heads/main"); err == nil {
		t.Fatalf("a missing repository = %q with no error", got)
	}
	notRepo := t.TempDir()
	if got, err := HubReadRef(ctx, notRepo, "refs/heads/main"); err == nil {
		t.Fatalf("a folder that is no repository = %q with no error", got)
	}
}
