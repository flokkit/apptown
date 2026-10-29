package worker

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/stock"
)

// A declared result file is read as a check's report is: a regular file
// in the tree or the state folder, reached through no link. Each other case
// is its own state, never another one: nothing is absent, a link or a
// folder is refused with its reason, and a file over its maximum is too
// large, not cut short.
func TestAResultFileIsReadConfined(t *testing.T) {
	t.Parallel()
	s := &Server{ToolVersions: stock.Versions(), State: t.TempDir()}
	tree, state := s.workspacePath("ws"), s.statePath("ws")
	outside := t.TempDir()
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	link := func(target, path string) {
		t.Helper()
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(tree, ".apptown", "review.json"), `{"ok":true}`)
	write(filepath.Join(state, "sessions", "s.jsonl"), "{}\n")
	write(filepath.Join(tree, "big"), "0123456789abcdefghij")
	write(filepath.Join(tree, "empty"), "")
	write(filepath.Join(outside, "secret"), "secret")
	write(filepath.Join(tree, "inside"), "inside")
	link(filepath.Join(outside, "secret"), filepath.Join(tree, "to-outside"))
	link("inside", filepath.Join(tree, "to-inside"))
	link(outside, filepath.Join(tree, "out"))
	link(filepath.Join(tree, ".apptown"), filepath.Join(tree, "forge-link"))
	if err := os.Mkdir(filepath.Join(tree, "folder"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(tree, "file"), "a file, not a folder")

	cases := []struct {
		path string
		want execution.ResultFile
	}{
		{"/work/.apptown/review.json", execution.ResultFile{State: execution.ResultPresent, Content: []byte(`{"ok":true}`), Size: 11}},
		{"/apptown/state/sessions/s.jsonl", execution.ResultFile{State: execution.ResultPresent, Content: []byte("{}\n"), Size: 3}},
		{"/work/empty", execution.ResultFile{State: execution.ResultPresent, Size: 0}},
		{"/work/nothing", execution.ResultFile{State: execution.ResultAbsent}},
		{"/work/no-folder/nothing", execution.ResultFile{State: execution.ResultAbsent}},
		{"/work/file/under-a-file", execution.ResultFile{State: execution.ResultAbsent}},
		{"/apptown/state/nothing", execution.ResultFile{State: execution.ResultAbsent}},
		{"/work/big", execution.ResultFile{State: execution.ResultTooLarge, Size: 20}},
		{"/work/to-outside", execution.ResultFile{State: execution.ResultRefused, Reason: execution.RefusedSymlink}},
		{"/work/to-inside", execution.ResultFile{State: execution.ResultRefused, Reason: execution.RefusedSymlink}},
		{"/work/out/secret", execution.ResultFile{State: execution.ResultRefused, Reason: execution.RefusedSymlink}},
		{"/work/forge-link/review.json", execution.ResultFile{State: execution.ResultRefused, Reason: execution.RefusedSymlink}},
		{"/work/folder", execution.ResultFile{State: execution.ResultRefused, Reason: execution.RefusedNotRegular}},
	}
	for _, c := range cases {
		got, err := s.readResult(declaredResults{Workspace: "ws"}, execution.Result{Name: "r", Path: c.path, Max: 11})
		if err != nil {
			t.Errorf("%s: %v", c.path, err)
			continue
		}
		if got.State != c.want.State || got.Reason != c.want.Reason || got.Size != c.want.Size || string(got.Content) != string(c.want.Content) {
			t.Errorf("%s: %+v, want %+v", c.path, got, c.want)
		}
		if err := got.Valid(); err != nil {
			t.Errorf("%s: %v", c.path, err)
		}
	}
	// A workspace that is gone is not an absent result.
	if _, err := s.readResult(declaredResults{Workspace: "gone"}, execution.Result{Name: "r", Path: "/work/a", Max: 10}); err == nil {
		t.Error("a result in a workspace that is gone was read")
	}
}

// Fresh removal never follows a link: a file reached through a linked
// folder outside the tree, or a link's target, is left where it is, and
// the link itself goes.
func TestFreshRemovalFollowsNoLink(t *testing.T) {
	t.Parallel()
	s := &Server{ToolVersions: stock.Versions(), State: t.TempDir()}
	tree := s.workspacePath("ws")
	outside := t.TempDir()
	secret := filepath.Join(outside, "review.json")
	if err := os.WriteFile(secret, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(tree, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(tree, "out")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(tree, "link.json")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/work/out/review.json", "/work/link.json", "/work/nothing/at/all"} {
		if err := s.removeFresh(declaredResults{Workspace: "ws"}, execution.Result{Name: "r", Path: p, Max: 10, Fresh: true}); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
	}
	if body, err := os.ReadFile(secret); err != nil || string(body) != "keep" {
		t.Fatalf("a file outside the tree was removed: %q %v", body, err)
	}
	if _, err := os.Lstat(filepath.Join(tree, "link.json")); !os.IsNotExist(err) {
		t.Fatalf("the link at a Fresh path was kept: %v", err)
	}
}
