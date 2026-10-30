package docker

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/execfile"
	"github.com/flokkit/apptown/internal/poll/polltest"
)

func TestDockerCLISelectionStreamsAndCancellation(t *testing.T) {
	t.Parallel()
	script := filepath.Join(t.TempDir(), "docker-fixture")
	if err := execfile.Write(script, []byte("#!/bin/sh\nif [ \"$1\" = wait ]; then sleep 30; exit; fi\nprintf '%s\\n' \"$@\"\ncat\nprintf diagnostic >&2\n"), 0700); err != nil {
		t.Fatal(err)
	}
	client := CLI{Binary: script}
	var stdout bytes.Buffer
	out, err := client.Run(context.Background(), "ssh://selected", Command{Args: []string{"copy", "two words"}, Stdin: strings.NewReader("payload"), Stdout: &stdout})
	if err != nil || string(out) != "diagnostic" || stdout.String() != "--host\nssh://selected\ncopy\ntwo words\npayload" {
		t.Fatalf("stream routing: stdout=%q stderr=%q err=%v", stdout.String(), out, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := client.Run(ctx, "", Command{Args: []string{"wait"}, Timeout: 30 * time.Second}); err == nil || deploy.IsUnreachable(err) {
		t.Fatalf("caller cancellation classified as connectivity failure: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("cancellation took %v", elapsed)
	}
	// The command's own timeout is a provider connectivity failure; a
	// caller's cancellation above does not establish that the host is down.
	t.Run("command timeout is unreachable", func(t *testing.T) {
		_, err := client.Run(t.Context(), "", Command{Args: []string{"wait"}, Timeout: 20 * time.Millisecond})
		if !deploy.IsUnreachable(err) {
			t.Fatalf("timeout was not typed: %v", err)
		}
	})

}

// An AppTown with no local Docker refuses every call to its own daemon before
// any process starts; an admitted target's scope keeps the Docker CLI, with
// that target's --host on every call. A scope given no host stays refused.
func TestAppTownsOwnDockerRefusesWhileAnAttachedTargetKeepsItsHost(t *testing.T) {
	t.Parallel()
	// The CLI's own --host argument is TestDockerCLISelectionStreamsAndCancellation's; here
	// the attached client is asked with the target's host.
	own := Scope{Client: Refusing{Attached: hostEcho{}}}
	for _, dk := range []Scope{own, own.WithHost("", "", "")} {
		if out, err := dk.Run(context.Background(), "ps"); !errors.Is(err, ErrNoLocalBackend) || len(out) != 0 || !dk.Absent() {
			t.Fatalf("AppTown's own Docker answered %q %v", out, err)
		}
	}
	target := own.WithHost("ssh://apptown-target-lab", "lab.example", "")
	if out, err := target.Run(context.Background(), "info"); err != nil || string(out) != "ssh://apptown-target-lab info" || target.Absent() {
		t.Fatalf("the attached target: %q %v", out, err)
	}
}

// The daemon AppTown selected builds its images: a builder named in the
// AppTown's own environment (BUILDX_BUILDER) would take a plain docker build
// elsewhere, with the project's source, so no command inherits it.
func TestNoDockerCommandInheritsABuilderFromAppTownsEnvironment(t *testing.T) {
	t.Setenv("BUILDX_BUILDER", "some-remote-builder")
	t.Setenv("BUILDX_CONFIG", "/ambient/buildx")
	t.Setenv("APPTOWN_PROBE_KEPT", "kept")
	script := filepath.Join(t.TempDir(), "docker-fixture")
	if err := execfile.Write(script, []byte("#!/bin/sh\necho \"builder=[${BUILDX_BUILDER-unset}${BUILDX_CONFIG-}] kept=[$APPTOWN_PROBE_KEPT]\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	client := CLI{Binary: script}
	// The observation's bound is the test binary's deadline, not ten
	// seconds: the fixture's first start waits for the operating system's
	// first-run check of a new file (macOS scans it), which under a busy
	// suite took longer.
	bound := polltest.HangGuard(t)
	for name, req := range map[string]Command{
		"a command":             {Args: []string{"build", "."}},
		"a streamed command":    {Args: []string{"build", "."}, Stdout: &bytes.Buffer{}},
		"a bounded observation": {Args: []string{"info"}, Timeout: bound},
	} {
		out, err := client.Run(context.Background(), "", req)
		if req.Stdout != nil {
			out = req.Stdout.(*bytes.Buffer).Bytes()
		}
		got := strings.TrimSpace(string(out))
		if err != nil || (got != "builder=[unset] kept=[kept]" && got != "builder=[] kept=[kept]") {
			t.Errorf("%s: %q %v", name, got, err)
		}
	}
}

// hostEcho answers each command with the host it was sent to and its
// arguments.
type hostEcho struct{}

func (hostEcho) Run(_ context.Context, host string, c Command) ([]byte, error) {
	return []byte(host + " " + strings.Join(c.Args, " ")), nil
}
