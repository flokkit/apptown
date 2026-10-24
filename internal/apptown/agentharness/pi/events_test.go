package pi_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/agentharness/pi/pitest"

	piharness "github.com/flokkit/apptown/internal/apptown/agentharness/pi"
	"github.com/flokkit/apptown/internal/execution"
)

// The capture, run by a real sh under set -e as the worker's entry runs it,
// keeps pi's own exit status, writes its stdout (not its stderr) to the
// events file, and passes stdout on for the live feed.
//
// Kills: a status lost to errexit, or a capture that cannot fail.
func TestTheCaptureKeepsPisStatusAndItsEvents(t *testing.T) {
	t.Parallel()
	for _, code := range []int{0, 7} {
		dir := t.TempDir()
		pi := `echo '{"type":"turn_start"}'; echo 'toolchain noise' >&2; echo '{"type":"agent_end"}'; exit ` + map[int]string{0: "0", 7: "7"}[code]
		argv := piharness.CapturedArgv([]string{"sh", "-c", pi}, "events.jsonl", "events.status")
		out := execution.Runner{}.Run(context.Background(), execution.Local, execution.Request{Workspace: dir, Argv: argv, Timeout: time.Minute})
		events, err := os.ReadFile(filepath.Join(dir, "events.jsonl"))
		if err != nil || string(events) != "{\"type\":\"turn_start\"}\n{\"type\":\"agent_end\"}\n" {
			t.Fatalf("exit %d: events %q, %v (%+v)", code, events, err, out)
		}
		if out.ExitCode != code || !strings.Contains(string(out.Output), "toolchain noise") || !strings.Contains(string(out.Output), "agent_end") {
			t.Fatalf("exit %d: the capture exited %d with %q", code, out.ExitCode, out.Output)
		}
		if got, inner, ok := pitest.CapturedEvents(argv); !ok || got != "events.jsonl" || strings.Join(inner, " ") != "sh -c "+pi {
			t.Fatalf("the capture's file %q, argv %q, %v", got, inner, ok)
		}
	}
}

// The catch-up after exit shows the events the live feed did not: it
// counts events, not bytes, since the live log carries stderr the file
// does not.
func TestTheCatchUpShowsTheEventsTheLiveFeedMissed(t *testing.T) {
	t.Parallel()
	log := "installing the toolchain\n{\"type\":\"turn_start\"}\nmise WARN something\n{\"type\":\"turn_end\"}\n"
	file := "{\"type\":\"turn_start\"}\n{\"type\":\"turn_end\"}\n{\"type\":\"agent_end\"}\n"
	var seen int64
	for _, line := range strings.SplitAfter(log, "\n") {
		if piharness.IsEvent([]byte(line)) {
			seen++
		}
	}
	if got := string(piharness.EventsAfter([]byte(file), seen)); got != "{\"type\":\"agent_end\"}\n" {
		t.Fatalf("caught up with %q", got)
	}
}
