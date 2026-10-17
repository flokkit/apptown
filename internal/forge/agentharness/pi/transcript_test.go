package pi

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func header(id string) string {
	return `{"type":"session","version":3,"id":"` + id + `","timestamp":"2026-10-01T00:00:00.000Z","cwd":"/work"}` + "\n"
}

const turn = `{"type":"message","message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}` + "\n"

func sessionsDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), ".forge", "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A transcript is kept only when its first line is Pi's header naming the
// admitted session and it holds a turn; otherwise the reason says which.
func TestCheckTranscriptNamesWhyATranscriptCannotBeKept(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		raw  []byte
		want string
	}{
		"kept":          {[]byte(header("s") + turn), ""},
		"empty":         {nil, "empty"},
		"too large":     {append([]byte(header("s")), bytes.Repeat([]byte("x"), MaxTranscriptBytes)...), "larger than"},
		"no header":     {[]byte(turn + header("s")), "does not start with Pi's session header"},
		"not JSON":      {[]byte("hello\n" + turn), "does not start with Pi's session header"},
		"wrong session": {[]byte(header("other") + turn), `names session "other", not "s"`},
		"header only":   {[]byte(header("s")), "only its header"},
		"blank after":   {[]byte(header("s") + "\n\n"), "only its header"},
	} {
		if got := CheckTranscript(c.raw, "s"); (c.want == "") != (got == "") || !strings.Contains(got, c.want) {
			t.Errorf("%s: %q, want %q", name, got, c.want)
		}
	}
}

// The transcript is found by its header, never by name or recency: a newer
// file for another session is ignored, a second file naming the session is
// ambiguous, and links are not followed.
func TestStepTranscriptFindsTheOneFileNamingTheSession(t *testing.T) {
	t.Parallel()
	dir := sessionsDir(t)
	write(t, filepath.Join(dir, "2026-01_step-7-a1.jsonl"), header("step-7-a1")+turn)
	write(t, filepath.Join(dir, "2026-09_step-7-a1.jsonl"), header("step-7-a2")+turn) // newer, misnamed
	raw, reason := StepTranscript(dir, "step-7-a1")
	if reason != "" || string(raw) != header("step-7-a1")+turn {
		t.Fatalf("found %q (%s)", raw, reason)
	}
	if _, reason := StepTranscript(dir, "step-7-a3"); !strings.Contains(reason, "no transcript names") {
		t.Fatalf("missing: %s", reason)
	}
	write(t, filepath.Join(dir, "2026-05_competing.jsonl"), header("step-7-a1")+turn+turn)
	if _, reason := StepTranscript(dir, "step-7-a1"); !strings.Contains(reason, "2 transcripts name") {
		t.Fatalf("ambiguous: %s", reason)
	}
	outside := filepath.Join(t.TempDir(), "secret.jsonl")
	write(t, outside, header("linked")+turn)
	if err := os.Symlink(outside, filepath.Join(dir, "2026-06_linked.jsonl")); err != nil {
		t.Fatal(err)
	}
	if _, reason := StepTranscript(dir, "linked"); !strings.Contains(reason, "no transcript names") {
		t.Fatalf("a link was followed: %s", reason)
	}
	write(t, filepath.Join(dir, "2026-07_header.jsonl"), header("bare"))
	if _, reason := StepTranscript(dir, "bare"); !strings.Contains(reason, "only its header") {
		t.Fatalf("header only: %s", reason)
	}
}

// A session directory reached through a link is not read.
func TestStepTranscriptRefusesALinkedSessionDirectory(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	elsewhere := t.TempDir()
	write(t, filepath.Join(elsewhere, "x.jsonl"), header("s")+turn)
	if err := os.MkdirAll(filepath.Join(ws, ".forge"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(ws, ".forge", "sessions")); err != nil {
		t.Fatal(err)
	}
	if raw, reason := StepTranscript(filepath.Join(ws, ".forge", "sessions"), "s"); raw != nil || !strings.Contains(reason, "not a directory") {
		t.Fatalf("read through a link: %q %s", raw, reason)
	}
}

// Resuming writes the kept bytes as the session's one file: the file naming
// the session is replaced, a new one is named as Pi names its files, and a
// directory with two files naming the session is refused.
func TestResumeTranscriptMakesTheKeptBytesTheSessionsFile(t *testing.T) {
	t.Parallel()
	dir := sessionsDir(t)
	kept := []byte(header("s") + turn)
	now := time.Date(2026, 10, 1, 1, 2, 3, 0, time.UTC)
	if err := ResumeTranscript(dir, "s", kept, now); err != nil {
		t.Fatal(err)
	}
	created := filepath.Join(dir, "2026-10-01T01-02-03-000Z_s.jsonl")
	if got, err := os.ReadFile(created); err != nil || !bytes.Equal(got, kept) {
		t.Fatalf("created: %q %v", got, err)
	}
	write(t, created, string(kept)+`{"type":"message","note":"not kept"}`+"\n")
	if err := ResumeTranscript(dir, "s", kept, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(created); !bytes.Equal(got, kept) {
		t.Fatalf("replaced: %q", got)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("files: %v", entries)
	}
	write(t, filepath.Join(dir, "2026-11_other.jsonl"), header("s")+turn)
	if err := ResumeTranscript(dir, "s", kept, now); err == nil || !strings.Contains(err.Error(), "2 transcripts") {
		t.Fatalf("ambiguous: %v", err)
	}
	if err := ResumeTranscript(dir, "t", kept, now); err == nil {
		t.Fatal("bytes of another session resumed")
	}
}
