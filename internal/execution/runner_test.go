//go:build unix

package execution

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"testing/iotest"
	"time"

	"flokkit.io/forge/internal/execfile"

	"flokkit.io/forge/internal/poll"
)

// hangGuard bounds a run whose duration is not what the test checks: by the
// test binary's own deadline, less a tenth to report the failure. The first
// start of a freshly written fake executable waits for the operating system's
// first-run check of a new file (macOS scans it), which under a busy suite
// took more than ten seconds; a fixed ten-second bound failed these tests for
// that alone, with "signal: killed" on the fake's first call.
func hangGuard(t *testing.T) time.Duration {
	t.Helper()
	deadline, ok := t.Deadline()
	if !ok {
		return 10 * time.Minute
	}
	return time.Until(deadline) * 9 / 10
}

func shell(command string) Request {
	return Request{Argv: []string{"sh", "-c", command}, Timeout: 5 * time.Second}
}

func TestExecutionOutcomes(t *testing.T) {
	runner := Runner{}
	req := shell(`printf '%s\n' "$GREETING"; printf 'problem\n' >&2; exit 7`)
	req.Env = []string{"GREETING=hello world"}
	var lines []string
	req.OnLine = func(s string) { lines = append(lines, s) }
	out := runner.Run(context.Background(), Local, req)
	if out.Termination != Exited || out.ExitCode != 7 || out.RunErr == nil || string(out.Output) != "hello world\nproblem\n" || len(lines) != 2 {
		t.Fatalf("exit and combined streaming output: %+v, lines=%q", out, lines)
	}
	req.Argv = []string{filepath.Join(t.TempDir(), "missing-command")}
	out = runner.Run(context.Background(), Local, req)
	if out.Termination != LaunchFailed || out.ExitCode != -1 {
		t.Fatalf("launch failure: %+v", out)
	}
	out = runner.Run(context.Background(), "dokcer", shell("exit 0"))
	if out.Termination != LaunchFailed || !strings.Contains(out.RunErr.Error(), "unknown execution backend") {
		t.Fatalf("unknown backend: %+v", out)
	}
	out = runner.Run(context.Background(), Local, Request{Timeout: time.Second})
	if out.Termination != LaunchFailed {
		t.Fatalf("empty command: %+v", out)
	}
}

func TestCancellationStopsLocalChildren(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The shell leads a group of its own; it starts a child that would
	// outlive any test, prints its pid (the group's id) and waits.
	req := shell(`sleep 300 & printf '%s\n' $$; wait`)
	var group atomic.Int64
	var led atomic.Bool
	req.OnLine = func(line string) {
		if pid, err := strconv.Atoi(strings.TrimSpace(line)); err == nil {
			group.Store(int64(pid))
			// The group is the command's own, so its end below says
			// something.
			led.Store(syscall.Kill(-pid, 0) == nil)
		}
		cancel()
	}
	out := (Runner{}).Run(ctx, Local, req)
	if out.Termination != Canceled || out.TimedOut {
		t.Fatalf("canceled outcome: %+v", out)
	}
	pgid := int(group.Load())
	if pgid <= 0 || !led.Load() {
		t.Fatalf("the shell never said its pid, or does not lead a group of its own: %d %+v", pgid, out)
	}
	t.Cleanup(func() { _ = syscall.Kill(-pgid, syscall.SIGKILL) })
	// Every process the command started is gone: its group no longer
	// exists, once init has reaped what was killed.
	if !poll.Until(context.Background(), 5*time.Second, 10*time.Millisecond, func() bool {
		return errors.Is(syscall.Kill(-pgid, 0), syscall.ESRCH)
	}) {
		t.Fatal("the canceled command's process group outlived it")
	}
	req = shell("sleep 10")
	req.Timeout = 30 * time.Millisecond
	out = (Runner{}).Run(context.Background(), Local, req)
	if out.Termination != TimedOut || !out.TimedOut {
		t.Fatalf("deadline outcome: %+v", out)
	}
}

func TestCaptureIsBoundedWithoutNewlines(t *testing.T) {
	req := shell("head -c 18000000 /dev/zero")
	streamed := 0
	req.OnLine = func(s string) {
		if len(s) > maxStreamLine {
			t.Errorf("unbounded stream fragment: %d", len(s))
		}
		streamed += len(s)
	}
	out := (Runner{}).Run(context.Background(), Local, req)
	if out.RunErr != nil || len(out.Output) != maxCapturedOutput || streamed != 18000000 {
		t.Fatalf("capture=%d, streamed=%d, error=%v", len(out.Output), streamed, out.RunErr)
	}
}

// Each line streams whole with its newline, a line too long for the buffer
// streams in maxStreamLine-byte fragments, and a last line without a newline
// still arrives, however the bytes are split across reads.
func TestStreamedFragmentsKeepLineBoundaries(t *testing.T) {
	x := func(n int) string { return strings.Repeat("x", n) }
	cases := []struct {
		name, input string
		want        []string
	}{
		{"empty", "", nil},
		{"lines", "a\nb\n", []string{"a\n", "b\n"}},
		{"blank lines", "\n\n", []string{"\n", "\n"}},
		{"unterminated last line", "a\nlast", []string{"a\n", "last"}},
		{"longest whole line", x(maxStreamLine-1) + "\n", []string{x(maxStreamLine-1) + "\n"}},
		{"one byte too long", x(maxStreamLine) + "\n", []string{x(maxStreamLine), "\n"}},
		{"several fragments", x(2*maxStreamLine+5) + "\ny", []string{x(maxStreamLine), x(maxStreamLine), x(5) + "\n", "y"}},
		{"long unterminated", x(maxStreamLine + 3), []string{x(maxStreamLine), x(3)}},
	}
	readers := map[string]func(string) io.Reader{
		"whole":    func(s string) io.Reader { return strings.NewReader(s) },
		"half":     func(s string) io.Reader { return iotest.HalfReader(strings.NewReader(s)) },
		"data+EOF": func(s string) io.Reader { return iotest.DataErrReader(strings.NewReader(s)) },
		"one byte": func(s string) io.Reader { return iotest.OneByteReader(strings.NewReader(s)) },
	}
	for _, c := range cases {
		for how, reader := range readers {
			if how == "one byte" && len(c.input) > 64 {
				continue // a byte per read over a megabyte is slow, not different
			}
			var got []string
			eachFragment(reader(c.input), func(b []byte) { got = append(got, string(b)) })
			if !slices.Equal(got, c.want) {
				t.Errorf("%s, %s reads: got %d fragments %v, want %d %v", c.name, how, len(got), lens(got), len(c.want), lens(c.want))
			}
		}
	}
}

// A process that prints short lines must not cost a maximum-line buffer: the
// suites start thousands of processes, and a fixed 1 MiB each was most of
// what they allocated.
func TestShortOutputDoesNotReserveTheLongestLine(t *testing.T) {
	const runs = 100
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for range runs {
		eachFragment(strings.NewReader("hello\nworld\n"), func([]byte) {})
	}
	runtime.ReadMemStats(&after)
	if perRun := (after.TotalAlloc - before.TotalAlloc) / runs; perRun > maxStreamLine/16 {
		t.Fatalf("streaming two short lines allocated %d bytes", perRun)
	}
}

func lens(fragments []string) []int {
	out := make([]int, len(fragments))
	for i, f := range fragments {
		out[i] = len(f)
	}
	return out
}

func TestDockerCancellationCleansOwnedContainerAndReportsFailure(t *testing.T) {
	for _, startup := range []struct{ name, delay string }{{"ready", ""}, {"slow-start", "1.2"}} {
		t.Run(startup.name, func(t *testing.T) {
			dir := t.TempDir()
			fake := filepath.Join(dir, "docker")
			log := filepath.Join(dir, "calls")
			// This executable is a local recording stand-in; no Docker daemon is used.
			source := `#!/bin/sh
printf '%s\n' "$@" >> "$CALLS"
if [ "$1" = run ]; then
  if [ -n "$START_DELAY" ]; then sleep "$START_DELAY"; fi
  printf 'started\n'
  exec sleep 30
fi
printf 'cleanup denied\n' >&2
exit 1
`
			writeFakeDocker(t, fake, source)
			t.Setenv("CALLS", log)
			t.Setenv("START_DELAY", startup.delay)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			started := false
			// Cancellation is triggered by readiness, not elapsed time; the
			// timeout is only a hang guard. The slow-start case reproduces the
			// former one-second deadline race.
			req := Request{Workspace: dir, WorkspaceRO: true, Argv: []string{"echo", "literal $(command)"},
				Env: []string{"VALUE=two words"}, Image: "pinned:v1", Name: "owned-test", Network: "none",
				NoEntrypoint: true, Labels: []string{"owner=example"}, Timeout: hangGuard(t),
				OnLine: func(line string) {
					if line == "started" {
						started = true
						cancel()
					}
				}}
			out := (Runner{DockerBinary: fake}).Run(ctx, Docker, req)
			if !started || out.Termination != Canceled || out.TimedOut || out.CleanupErr == nil || !strings.Contains(out.CleanupErr.Error(), "cleanup denied") {
				t.Fatalf("started=%v, cleanup outcome: %+v", started, out)
			}
			calls, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{dir + ":/work:ro", "VALUE=two words", "literal $(command)", "--entrypoint\n\n", "rm\n-f\nowned-test\n"} {
				if !strings.Contains(string(calls), want) {
					t.Errorf("missing %q in arguments: %s", want, calls)
				}
			}
		})
	}
}

// A declared report comes back in the outcome, read from inside the
// workspace only: a regular file within the size bound. A missing report, a
// symbolic link (even to a file inside), a path out of the workspace or an
// oversized file is an error, never an empty report.
func TestAReportComesBackOnlyFromInsideTheWorkspace(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret")
	for name, body := range map[string]string{"report.json": `{"ok":true}`, "inside.txt": "x"} {
		if err := os.WriteFile(filepath.Join(ws, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(outside, []byte("not the check's"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(ws, "escape.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("inside.txt", filepath.Join(ws, "link.json")); err != nil {
		t.Fatal(err)
	}
	// Sparse files: only the size matters, so no bytes need writing. A
	// full OS image's scan is about 27 MB (trivy over rust:1.84.1), and is
	// read; one past the bound is not.
	for name, size := range map[string]int64{"big.json": MaxReport + 1, "image-scan.sarif": 32 << 20} {
		if f, err := os.Create(filepath.Join(ws, name)); err != nil {
			t.Fatal(err)
		} else if err := errors.Join(f.Truncate(size), f.Close()); err != nil {
			t.Fatal(err)
		}
	}
	if b, err := ReadReport(ws, "image-scan.sarif"); err != nil || len(b) != 32<<20 {
		t.Fatalf("a full image's scan report: %d bytes, %v", len(b), err)
	}
	if b, err := ReadReport(ws, "report.json"); err != nil || string(b) != `{"ok":true}` {
		t.Fatalf("a report inside: %q %v", b, err)
	}
	for _, path := range []string{"missing.json", "escape.json", "link.json", "../secret", "big.json"} {
		if b, err := ReadReport(ws, path); err == nil {
			t.Errorf("%s was read (%d bytes)", path, len(b))
		}
	}
	out := Runner{}.Run(context.Background(), Local, Request{Workspace: ws, Argv: []string{"sh", "-c", "echo made > made.json"}, Timeout: 10 * time.Second, Report: "made.json"})
	if !out.Exited0() || out.ReportErr != nil || string(out.Report) != "made\n" {
		t.Fatalf("a run's report: %+v", out)
	}
}

// A local run, git or a command that runs git, starts without the GIT_*
// variables AppTown inherited (an AppTown started from a Git hook carries
// GIT_DIR and others), and with the ones the request sets. Not parallel: it
// sets the process's environment.
func TestALocalRunInheritsNoGitVariable(t *testing.T) {
	t.Setenv("GIT_DIR", "/elsewhere/.git")
	t.Setenv("GIT_INDEX_FILE", "/elsewhere/index")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("APPTOWN_ENV_MARK", "kept")
	req := shell(`env`)
	req.Env = []string{"GIT_TERMINAL_PROMPT=0"}
	out := Runner{}.Run(context.Background(), Local, req)
	if !out.Exited0() {
		t.Fatalf("%+v", out)
	}
	env := "\n" + string(out.Output)
	for _, gone := range []string{"\nGIT_DIR=", "\nGIT_INDEX_FILE=", "\nGIT_CONFIG_COUNT="} {
		if strings.Contains(env, gone) {
			t.Errorf("the inherited %s reached the run", strings.TrimPrefix(gone, "\n"))
		}
	}
	for _, kept := range []string{"\nGIT_TERMINAL_PROMPT=0\n", "\nAPPTOWN_ENV_MARK=kept\n"} {
		if !strings.Contains(env, kept) {
			t.Errorf("%s did not reach the run", strings.TrimSpace(kept))
		}
	}
}

// Every Docker run the runner makes is bounded by its limits, placed before
// the image so docker reads them as flags; a zero field adds nothing, and
// memory bounds swap too.
func TestADockerRunIsBoundedByTheRunnersLimits(t *testing.T) {
	t.Parallel()
	req := Request{Name: "forge-step-1", Image: "img", Argv: []string{"true"}}
	got := runArgs(req, Limits{CPUs: 1.5, MemoryBytes: 512 << 20, PIDs: 100})
	want := []string{"run", "--cpus", "1.5", "--memory", "536870912", "--memory-swap", "536870912", "--pids-limit", "100"}
	if !slices.Equal(got[:len(want)], want) {
		t.Fatalf("args %v, want them to start %v", got, want)
	}
	if !slices.Equal(runArgs(req, Limits{}), removeFlag(dockerArgs(req), "--rm")) {
		t.Fatal("no limits add no flags, and the runner removes the container itself")
	}
	if got := LimitFlags(Limits{PIDs: 7}); !slices.Equal(got, []string{"--pids-limit", "7"}) {
		t.Fatalf("a zero field adds nothing: %v", got)
	}
}

// A container Docker killed at its memory limit is no answer: its exit code
// is the kill's, not the command's, so a check or a tests step reads it as an
// error, never as a failing (or carried) result. An exit the runner cannot
// read is no answer either; an ordinary exit is.
//
// Kills: --rm with no inspection, which read an out-of-memory kill as exit 137.
func TestARunKilledForMemoryIsNoAnswer(t *testing.T) {
	t.Parallel()
	run := func(oom string) Outcome {
		t.Helper()
		dir := t.TempDir()
		fake := filepath.Join(dir, "docker")
		// A local stand-in: the run exits 137, inspect answers oom.
		script := "#!/bin/sh\ncase \"$1\" in\nrun) exit 137;;\ninspect) " + oom + ";;\nesac\nexit 0\n"
		writeFakeDocker(t, fake, script)
		return (Runner{DockerBinary: fake, Limits: Limits{MemoryBytes: 64 << 20}}).Run(context.Background(), Docker,
			Request{Argv: []string{"make", "test"}, Image: "agent:test", Name: "tests-1", Timeout: time.Minute})
	}
	if out := run("echo true"); out.Termination != OutOfMemory || out.Answered() || out.ExitCode != -1 || !strings.Contains(out.RunErr.Error(), "64 MiB") {
		t.Fatalf("an out-of-memory kill: %+v", out)
	}
	if out := run("exit 1"); out.Answered() || out.CleanupErr == nil {
		t.Fatalf("an ending that could not be read: %+v", out)
	}
	if out := run("echo false"); !out.Answered() || out.ExitCode != 137 {
		t.Fatalf("an ordinary exit: %+v", out)
	}
}

// A run Docker refused to make (exit 125, and no container afterwards) did
// not start, and says why in Docker's own words; a container that ran and
// exited 125 is its command's answer.
//
// Kills: the refusal read as a failed inspect, whose "no such object" hid
// "range of CPUs is from 0.01 to 2.00" (one-server test, 22 Oct).
func TestARunDockerRefusedSaysDockersReason(t *testing.T) {
	t.Parallel()
	run := func(inspect string) Outcome {
		t.Helper()
		fake := filepath.Join(t.TempDir(), "docker")
		script := "#!/bin/sh\ncase \"$1\" in\nrun) echo 'docker: Error response from daemon: range of CPUs is from 0.01 to 2.00, as there are only 2 CPUs available' >&2; echo \"Run 'docker run --help' for more information\" >&2; exit 125;;\ninspect) " + inspect + ";;\nesac\nexit 0\n"
		writeFakeDocker(t, fake, script)
		return (Runner{DockerBinary: fake, Limits: Limits{CPUs: 4}}).Run(context.Background(), Docker,
			Request{Argv: []string{"osv-scanner"}, Image: "osv:test", Name: "scan-1", Timeout: time.Minute})
	}
	out := run("echo 'Error: No such object: scan-1' >&2; exit 1")
	if out.Termination != LaunchFailed || out.Answered() || out.CleanupErr != nil || out.RunErr == nil ||
		!strings.Contains(out.RunErr.Error(), "range of CPUs is from 0.01 to 2.00") {
		t.Fatalf("a refused run: %+v", out)
	}
	if out := run("echo false"); !out.Answered() || out.ExitCode != 125 {
		t.Fatalf("a container that exited 125: %+v", out)
	}
}

// writeFakeDocker writes a local stand-in for the docker CLI: these tests'
// guarantee is the process boundary itself (an exit code, what inspect
// answers, cleanup after cancellation). No Docker daemon is used.
func writeFakeDocker(t *testing.T, path, source string) {
	t.Helper()
	if err := execfile.Write(path, []byte(source), 0o700); err != nil {
		t.Fatal(err)
	}
}
