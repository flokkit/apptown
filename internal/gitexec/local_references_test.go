package gitexec

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLocalGitReferenceRelocationPreservesExternalIdentity(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "snapshot")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(repo, ".git", "config")
	raw := []byte("[remote \"origin\"]\n url = /old/workspaces/project-address\n[remote \"upstream\"]\n url = https://example.invalid/team/repository.git\n[remote \"ssh\"]\n url = user@example.invalid:team/repository.git\n[future]\n value = preserve\n")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	refs, err := LocalURLs(t.Context(), repo)
	if err != nil || len(refs) != 1 || refs[0].Path != "/old/workspaces/project-address" {
		t.Fatalf("local references: %+v %v", refs, err)
	}
	edit, err := PlanConfigRelocation(t.Context(), repo, []URLChange{{Key: refs[0].Key, Before: refs[0].Value, After: "/new/workspaces/project-id"}})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := edit.Apply(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := readConfig(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, entry := range entries {
		got[entry.key] = entry.value
	}
	if got["remote.origin.url"] != "/new/workspaces/project-id" || got["remote.upstream.url"] != "https://example.invalid/team/repository.git" || got["remote.ssh.url"] != "user@example.invalid:team/repository.git" || got["future.value"] != "preserve" {
		t.Fatalf("changed unrelated Git identity: %+v", got)
	}
	if _, err := PlanConfigRelocation(t.Context(), repo, []URLChange{{Key: "remote.upstream.url", Before: got["remote.upstream.url"], After: "/local"}}); err == nil {
		t.Fatal("converted external origin to local path")
	}
}
func TestLocalGitReferenceRefusesConcurrentConfigChange(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(repo, ".git", "config")
	raw := []byte("[remote \"origin\"]\n url = /old/hub\n")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	edit, err := PlanConfigRelocation(t.Context(), repo, []URLChange{{Key: "remote.origin.url", Before: "/old/hub", After: "/new/hub"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(raw, []byte("# changed\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := edit.Apply(t.Context()); err == nil {
		t.Fatal("overwrote independently changed config")
	}
}
