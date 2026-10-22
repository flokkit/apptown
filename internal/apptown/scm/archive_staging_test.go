package scm

import (
	"archive/tar"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/execfile"

	"github.com/flokkit/apptown/internal/gitexec/gittest"
)

func archiveRepository(t *testing.T) string {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "repo $HOME")
	if err := os.Mkdir(repo, 0755); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		t.Helper()
		cmd := gittest.Command(args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run("init", "-b", "main")
	run("config", "user.name", "fixture")
	run("config", "user.email", "fixture@example.invalid")
	if err := os.WriteFile(filepath.Join(repo, "source.txt"), []byte("committed"), 0644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "fixture")
	if err := os.WriteFile(filepath.Join(repo, "source.txt"), []byte("dirty"), 0644); err != nil {
		t.Fatal(err)
	}
	return repo
}

func TestStageTreeAtUsesCommittedRevisionAndCleansScratch(t *testing.T) {
	t.Parallel()
	dir, cleanup, err := StageTreeAt(context.Background(), archiveRepository(t), "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	b, err := os.ReadFile(filepath.Join(dir, "source.txt"))
	if err != nil || string(b) != "committed" {
		t.Fatalf("selected source: %q, %v", b, err)
	}
	cleanup()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("scratch survived cleanup: %v", err)
	}
}

func TestStageTreeAtRejectsEitherProcessFailureAndRemovesPartialOutput(t *testing.T) {
	repo := archiveRepository(t)
	// A valid archive followed by a producer error used to look successful when
	// tar consumed it successfully. Preserve that failure, not only invalid refs.
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	if err := tw.WriteHeader(&tar.Header{Name: "source.txt", Mode: 0644, Size: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("ok")); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	fixture := filepath.Join(t.TempDir(), "valid.tar")
	if err := os.WriteFile(fixture, archive.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STAGING_TEST_TAR", fixture)
	for _, tc := range []struct{ name, command, script, want string }{
		{"producer after valid output", "git", "#!/bin/sh\n/bin/cat \"$STAGING_TEST_TAR\"\nexit 7\n", "archive"},
		{"extractor after partial output", "tar", "#!/bin/sh\nprintf partial > \"$5/partial\"\necho extractor-failed >&2\nexit 8\n", "extract"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scratch := t.TempDir()
			t.Setenv("TMPDIR", scratch)
			bin := t.TempDir()
			if err := execfile.Write(filepath.Join(bin, tc.command), []byte(tc.script), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			dir, cleanup, err := StageTreeAt(context.Background(), repo, "HEAD")
			defer cleanup()
			if err == nil || dir != "" || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("failure accepted: dir=%q err=%v", dir, err)
			}
			entries, readErr := os.ReadDir(scratch)
			if readErr != nil || len(entries) != 0 {
				t.Fatalf("partial scratch leaked: %v %v", entries, readErr)
			}
		})
	}
}
