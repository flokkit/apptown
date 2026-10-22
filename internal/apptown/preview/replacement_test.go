package preview

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// A preview whose removal fails is never answered as the new incarnation:
// the old container is not taken for what was just prepared.
func TestAPreviewThatCannotBeRemovedIsNeverRelabelled(t *testing.T) {
	t.Parallel()
	s, _, client, dir, projectID := previewBootWorld(t, "")
	ws := filepath.Join(dir, "workspaces", "change-00000000-0009-7000-8000-000000000000")
	writePreviewBootFixture(t, ws)
	name := docker.PreviewChangeName(idstest.N(9))
	client.SetRunning(name, true)
	port, err := docker.ContainerHostPort(context.Background(), s.hosts.Local(), name, 8080)
	if err != nil {
		t.Fatal(err)
	}
	client.ResetCommands()
	client.SetReply(func(args []string) ([]byte, error, bool) {
		if args[0] == "port" && args[1] == name {
			return []byte(fmt.Sprintf("127.0.0.1:%d\n", port)), nil, true
		}
		if args[0] == "inspect" && args[len(args)-1] == name && strings.Contains(strings.Join(args, " "), "State.Running") {
			return []byte("true\n"), nil, true
		}
		if args[0] == "inspect" && args[len(args)-1] == name && strings.Contains(strings.Join(args, " "), "forge.incarnation") {
			return []byte("old-incarnation\n"), nil, true
		}
		if args[0] == "rm" && args[len(args)-1] == name {
			return []byte("Error response from daemon: removal refused"), errors.New("exit status 1"), true
		}
		return nil, nil, false
	})
	for _, restart := range []bool{false, true} {
		out, err := s.RehearseChange(context.Background(), Change{ID: idstest.N(9), ProjectID: projectID}, restart)
		if err == nil {
			t.Fatalf("restart %v: old container survived failed removal, but preview returned success with new identity %q; runs: %v", restart, out.Incarnation, client.Matching("run "))
		}
	}
}

// A restart whose removal fails stops: the running preview, even of the
// same incarnation, is not handed back as started again.
func TestARestartThatCannotRemoveThePreviewStops(t *testing.T) {
	t.Parallel()
	s, _, client, dir, projectID := previewBootWorld(t, "")
	writePreviewBootFixture(t, filepath.Join(dir, "workspaces", "change-00000000-0009-7000-8000-000000000000"))
	if _, err := s.RehearseChange(context.Background(), Change{ID: idstest.N(9), ProjectID: projectID}, false); err != nil {
		t.Fatal(err)
	}
	name := docker.PreviewChangeName(idstest.N(9))
	client.SetReply(func(args []string) ([]byte, error, bool) {
		if args[0] == "rm" && args[len(args)-1] == name {
			return []byte("Error response from daemon: removal refused"), errors.New("exit status 1"), true
		}
		return nil, nil, false
	})
	client.ResetCommands()
	if out, err := s.RehearseChange(context.Background(), Change{ID: idstest.N(9), ProjectID: projectID}, true); err == nil {
		t.Fatalf("a restart that could not remove the preview answered %+v; runs: %v", out, client.Matching("run "))
	}
}

// Preparing another historical commit, and failing, leaves the tree the
// running preview serves as it was.
func TestAFailedPreparationLeavesTheServedSnapshotAlone(t *testing.T) {
	t.Parallel()
	s, _, client, dir, projectID := previewBootWorld(t, "")
	repo := filepath.Join(dir, "workspaces", "project-p")
	if err := os.MkdirAll(filepath.Join(repo, ".apptown"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, path), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git := func(args ...string) string {
		t.Helper()
		out, err := gitexec.Output(t.Context(), repo, args...)
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(out)
	}
	contract := "version: 1\napp:\n  web:\n    static: .\n    public: true\n"
	write(".apptown/app.yaml", contract)
	write("index.html", "original site")
	git("init", "-q")
	git("add", ".")
	git("-c", "user.name=review", "-c", "user.email=review@example.invalid", "commit", "-qm", "original")
	original := git("rev-parse", "HEAD")
	if _, err := s.RehearseChange(context.Background(), Change{ID: idstest.N(9), ProjectID: projectID, SourceRevision: original}, false); err != nil {
		t.Fatal(err)
	}
	client.ResetCommands()
	write(".apptown/app.yaml", contract+"needs:\n  no-such-thing: {}\n")
	write("index.html", "replacement site")
	git("add", ".")
	git("-c", "user.name=review", "-c", "user.email=review@example.invalid", "commit", "-qm", "invalid replacement")
	replacement := git("rev-parse", "HEAD")
	if _, err := s.RehearseChange(context.Background(), Change{ID: idstest.N(9), ProjectID: projectID, SourceRevision: replacement}, true); err == nil {
		t.Fatal("unknown need should refuse replacement")
	}
	body, err := os.ReadFile(filepath.Join(dir, "workspaces", "asof-change-00000000-0009-7000-8000-000000000000", original, "index.html"))
	if err != nil || string(body) != "original site" {
		t.Fatalf("failed preparation replaced the old static preview's serving tree: %q, %v; removals: %v", body, err, client.Matching("rm "))
	}
}
