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
// counts captured events, not bytes: the live log also carries stderr
// and cumulative tool updates that the file omits.
func TestTheCatchUpShowsTheEventsTheLiveFeedMissed(t *testing.T) {
	t.Parallel()
	log := "installing the toolchain\n{\"type\":\"turn_start\"}\nmise WARN something\n{\"type\":\"tool_execution_update\",\"partialResult\":{}}\n{\"type\":\"turn_end\"}\n"
	file := "{\"type\":\"turn_start\"}\n{\"type\":\"turn_end\"}\n{\"type\":\"agent_end\"}\n"
	var seen int64
	for _, line := range strings.SplitAfter(log, "\n") {
		if piharness.IsCapturedEvent([]byte(line)) {
			seen++
		}
	}
	if got := string(piharness.EventsAfter([]byte(file), seen)); got != "{\"type\":\"agent_end\"}\n" {
		t.Fatalf("caught up with %q", got)
	}
}

// A broken capture cannot report Pi's successful exit as its own success.
func TestCaptureFailureFailsTheInvocation(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	argv := piharness.CapturedArgv([]string{"sh", "-c", "echo '{\"type\":\"agent_end\"}'"}, "missing/events", "status")
	out := execution.Runner{}.Run(t.Context(), execution.Local, execution.Request{Workspace: dir, Argv: argv, Timeout: time.Minute})
	if out.ExitCode == 0 {
		t.Fatalf("failed capture exited successfully: %+v", out)
	}
}

func TestToolCutsCountBytesAndKeepSmallResults(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	original := "START" + strings.Repeat("x", 20000) + "END"
	input := `{"type":"tool_execution_end","result":{"content":[{"type":"text","text":"` + original + `"},{"type":"text","text":"small result"}],"details":{"usage":{"input":31}}}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "input"), []byte(input), 0o644); err != nil {
		t.Fatal(err)
	}
	argv := piharness.CapturedArgv([]string{"cat", "input"}, "events", "status")
	out := execution.Runner{}.Run(t.Context(), execution.Local, execution.Request{Workspace: dir, Argv: argv, Timeout: time.Minute})
	captured, err := os.ReadFile(filepath.Join(dir, "events"))
	if err != nil {
		t.Fatal(err)
	}
	expected := strings.Repeat("x", 8192-5) + `\n[AppTown cut 3624 bytes of tool output]\n` + strings.Repeat("x", 8192-3)
	if out.ExitCode != 0 || string(out.Output) != string(captured) || !strings.Contains(string(captured), expected) || !strings.Contains(string(captured), `"text":"small result"`) || !strings.Contains(string(captured), `"input":31`) {
		t.Fatalf("capture changed a small result, lost usage or miscounted the cut: %.200s", captured)
	}
}
