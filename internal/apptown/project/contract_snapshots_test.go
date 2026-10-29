package project

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy/docker"
)

// A repo-backed project whose history carries each contract form, so the
// evidence writer can be checked against real commits.
func snapshotWorld(t *testing.T) (*Contracts, string, ids.ID, map[string]string) {
	t.Helper()
	db := testDB(t)
	c := NewContracts(db)
	dataDir := t.TempDir()
	projectID, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return CreateAt(t.Context(), tx, time.Now(), Spec{Slug: "p", Name: "P"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := New(db, nil).SetDefaultBranch(context.Background(), projectID, "main"); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(dataDir, "workspaces", "project-"+projectID.String())
	_ = os.MkdirAll(filepath.Join(repo, ".apptown"), 0o755)
	git := func(args ...string) string {
		c := gittest.Command(args...)
		c.Dir = repo
		c.Env = append(c.Env, "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return string(out)
	}
	git("init", "-q")
	sha := map[string]string{}
	commit := func(tag string) { sha[tag] = strings.TrimSpace(git("rev-parse", "HEAD")) }
	write := func(name, body string) { _ = os.WriteFile(filepath.Join(repo, ".apptown", name), []byte(body), 0o644) }

	// era 1: legacy compose.yaml (static site)
	write("compose.yaml", "services:\n  site:\n    x-forge-app: true\n    x-forge-static: site\n")
	git("add", "-A")
	git("commit", "-q", "-m", "compose era")
	commit("compose")
	// era 2: native app.yaml with a need + init
	_ = os.Remove(filepath.Join(repo, ".apptown", "compose.yaml"))
	write("app.yaml", "version: 1\napp:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\ninit:\n  - {name: migrate, build: ., command: [\"x\"]}\nneeds:\n  postgres: {version: \"16\"}\n")
	git("add", "-A")
	git("commit", "-q", "-m", "app.yaml era")
	commit("appyaml")
	// era 3: no contract at all (a non-application change)
	_ = os.Remove(filepath.Join(repo, ".apptown", "app.yaml"))
	_ = os.WriteFile(filepath.Join(repo, "README.md"), []byte("no app here\n"), 0o644)
	git("add", "-A")
	git("commit", "-q", "-m", "no contract")
	commit("none")
	return c, dataDir, projectID, sha
}

func TestContractSnapshotNativeOnly(t *testing.T) {
	t.Parallel()
	c, dataDir, pid, sha := snapshotWorld(t)

	// Snapshot each era directly.
	c.RecordCommit(context.Background(), dataDir, pid, sha["compose"])
	c.RecordCommit(context.Background(), dataDir, pid, sha["appyaml"])
	c.RecordCommit(context.Background(), dataDir, pid, sha["none"])

	// Retired forms never supply a native declaration.
	assertStatus(t, c, pid, sha["compose"], "absent", "none")

	// Evidence retains the exact native bytes, including need and init metadata.
	original, err := docker.ContractFileAt(context.Background(), filepath.Join(dataDir, "workspaces", "project-"+pid.String()), sha["appyaml"])
	if err != nil {
		t.Fatal(err)
	}
	var stored string
	// sql-guarantee: The stored snapshot bytes are the evidence guarantee, and no production read returns contract_snapshots rows; adding one would be test-only API.
	if err := c.db.SQL().QueryRowContext(context.Background(), `SELECT app_yaml FROM contract_snapshots WHERE project_id=$1 AND sha=$2`, pid, sha["appyaml"]).Scan(&stored); err != nil || stored != string(original) {
		t.Fatalf("snapshot changed native evidence: %v", err)
	}
	assertStatus(t, c, pid, sha["appyaml"], "present", "app-yaml")

	// no-contract era: an explicit ABSENT, not a missing row.
	assertStatus(t, c, pid, sha["none"], "absent", "none")

	// Removing source does not erase or reinterpret the original evidence.
	if err := os.RemoveAll(filepath.Join(dataDir, "workspaces", "project-"+pid.String())); err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: The stored snapshot bytes are the evidence guarantee, and no production read returns contract_snapshots rows; adding one would be test-only API.
	if err := c.db.SQL().QueryRowContext(context.Background(), `SELECT app_yaml FROM contract_snapshots WHERE project_id=$1 AND sha=$2`, pid, sha["appyaml"]).Scan(&stored); err != nil || stored != string(original) {
		t.Fatalf("original evidence unavailable after source removal: %v", err)
	}
}

// Re-observing a commit preserves its evidence even if the source disappears.
func TestContractSnapshotIdempotentPerSHA(t *testing.T) {
	t.Parallel()
	c, dataDir, pid, sha := snapshotWorld(t)
	c.RecordCommit(context.Background(), dataDir, pid, sha["appyaml"])
	// A missing repository would resolve as invalid if AppTown read it again.
	if err := os.RemoveAll(filepath.Join(dataDir, "workspaces", "project-"+pid.String())); err != nil {
		t.Fatal(err)
	}
	c.RecordCommit(context.Background(), dataDir, pid, sha["appyaml"])
	assertStatus(t, c, pid, sha["appyaml"], "present", "app-yaml")
	var n int
	// sql-guarantee: The stored snapshot bytes are the evidence guarantee, and no production read returns contract_snapshots rows; adding one would be test-only API.
	if err := c.db.SQL().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM contract_snapshots WHERE project_id=$1 AND sha=$2`, pid, sha["appyaml"]).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("one row per (project, sha), got %d", n)
	}
}

func assertStatus(t *testing.T, c *Contracts, pid ids.ID, sha, wantStatus, wantFormat string) {
	t.Helper()
	var status, format string
	// sql-guarantee: The stored snapshot bytes are the evidence guarantee, and no production read returns contract_snapshots rows; adding one would be test-only API.
	if c.db.SQL().QueryRowContext(context.Background(), `SELECT status, source_format FROM contract_snapshots WHERE project_id=$1 AND sha=$2`, pid, sha).Scan(&status, &format) != nil {
		t.Fatalf("no snapshot row for %s", sha)
	}
	if status != wantStatus || format != wantFormat {
		t.Fatalf("snapshot %s: status=%s format=%s, want %s/%s", sha, status, format, wantStatus, wantFormat)
	}
}
