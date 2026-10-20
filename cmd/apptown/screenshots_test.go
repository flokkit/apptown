package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// show says how many pictures AppTown took and from which commit, compared
// with the change's current one as same, different or not compared, and
// reads only the run's own newest finished boot.
func TestShowSaysWhatTheScreenshotsShow(t *testing.T) {
	ev := func(s string) json.RawMessage { return json.RawMessage(s) }
	steps := []cockpitStep{
		{Type: "preview", Status: "pass", Evidence: ev(`{"screenshots":{"commit":"aaaaaaa111","shots":[{"path":"/","view":"desktop"}]}}`)},
		{Type: "preview", Status: "pass", Evidence: ev(`{"screenshots":{"commit":"bbbbbbb222","shots":[{"path":"/","view":"desktop"},{"path":"/","view":"phone"}]}}`)},
		{Type: "preview", Status: "running", Evidence: ev(`{}`)},
	}
	s := latestScreenshots(steps)
	if s == nil || s.Commit != "bbbbbbb222" {
		t.Fatalf("not the newest finished boot: %+v", s)
	}
	for head, want := range map[string]string{
		"bbbbbbb222": "2 taken from bbbbbbb (the change's current commit): / desktop, / phone — https://x/changes/1",
		"ccccccc333": "a different commit: the change is now at ccccccc",
		"":           "not compared with the change's current commit",
	} {
		if got := screenshotsLine(s, head, "https://x/changes/1"); !strings.Contains(got, want) {
			t.Fatalf("head %q: %q, want %q", head, got, want)
		}
	}
	if got := screenshotsLine(&bootScreenshots{Unavailable: "the browser did not finish"}, "x", "l"); got != "screenshots could not be taken: the browser did not finish" {
		t.Fatalf("unavailable: %q", got)
	}
	if latestScreenshots([]cockpitStep{{Type: "tests", Status: "pass", Evidence: ev(`{"screenshots":{}}`)}}) != nil {
		t.Fatal("another step's evidence was read as a boot's")
	}
}
