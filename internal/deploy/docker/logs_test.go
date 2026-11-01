package docker

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// A container's recent log is one bounded read: the newest 200 lines with
// Docker's timestamps, no follow, within its deadline, and cut to the newest
// 64 KiB starting at a whole line. A read at either bound says older output
// may exist; a short log says nothing was cut.
func TestARecentLogIsOneBoundedRead(t *testing.T) {
	t.Parallel()
	var asked []Command
	out := ""
	f := &fakeClient{reply: func(req Command) ([]byte, error) {
		asked = append(asked, req)
		return []byte(out), nil
	}}
	dk := Scope{Client: f}

	out = "2026-10-16T01:00:00Z started\n2026-10-16T01:00:01Z listening\n"
	got, err := ReadRecentLog(t.Context(), dk, "apptown-svc-env-game-main-worker")
	if err != nil || got.Truncated || got.Text != "2026-10-16T01:00:00Z started\n2026-10-16T01:00:01Z listening" {
		t.Fatalf("a short log: %+v %v", got, err)
	}
	req := asked[0]
	if want := []string{"logs", "--tail", "200", "--timestamps", "apptown-svc-env-game-main-worker"}; !slices.Equal(req.Args, want) {
		t.Fatalf("asked %v, want %v", req.Args, want)
	}
	if req.Timeout != logReadWithin || req.Stdout != nil || req.Stderr != nil {
		t.Fatalf("the read is not bounded by its deadline: %+v", req)
	}

	var lines []string
	for i := range 300 {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	out = strings.Join(lines, "\n") + "\n"
	got, _ = ReadRecentLog(t.Context(), dk, "c")
	if shown := strings.Split(got.Text, "\n"); len(shown) != LogMaxLines || shown[0] != "line 100" || shown[len(shown)-1] != "line 299" || !got.Truncated {
		t.Fatalf("more lines than the bound: %d lines from %q, truncated %v", len(shown), shown[0], got.Truncated)
	}

	out = strings.Repeat(strings.Repeat("x", 1023)+"\n", 100) + "last\n"
	got, _ = ReadRecentLog(t.Context(), dk, "c")
	if len(got.Text) > LogMaxBytes || !got.Truncated || !strings.HasSuffix(got.Text, "\nlast") || !strings.HasPrefix(got.Text, strings.Repeat("x", 1023)+"\n") {
		t.Fatalf("a log over 64 KiB: %d bytes, truncated %v", len(got.Text), got.Truncated)
	}
}

// A log the host did not give is refused in AppTown's own words: what
// the Docker client wrote (which can name a host's address or credentials)
// never reaches the caller. A missing container is absent; a read past its
// deadline or cancelled says so.
func TestARecentLogFailureSaysNothingTheHostWrote(t *testing.T) {
	t.Parallel()
	const leak = "ssh://deploy:hunter2@10.0.0.7"
	for _, tc := range []struct {
		name   string
		out    string
		err    error
		absent bool
		says   string
	}{
		{"refused", "error during connect: " + leak + ": permission denied", errors.New("exit status 1"), false, ErrLogUnreadable.Error()},
		{"absent", "Error response from daemon: No such container: c", errors.New("exit status 1"), true, ""},
		{"deadline", leak, context.DeadlineExceeded, false, "did not answer within 10s"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dk := Scope{Client: &fakeClient{out: tc.out, err: tc.err}}
			_, err := ReadRecentLog(t.Context(), dk, "c")
			if err == nil || strings.Contains(err.Error(), "hunter2") || strings.Contains(err.Error(), "10.0.0.7") {
				t.Fatalf("the failure: %v", err)
			}
			if errors.Is(err, ErrContainerAbsent) != tc.absent || (!tc.absent && !errors.Is(err, ErrLogUnreadable)) || !strings.Contains(err.Error(), tc.says) {
				t.Fatalf("the failure is not classified: %v", err)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	dk := Scope{Client: &fakeClient{out: "partial line"}}
	if got, err := ReadRecentLog(ctx, dk, "c"); !errors.Is(err, ErrLogUnreadable) || got.Text != "" {
		t.Fatalf("a cancelled read: %+v %v", got, err)
	}
}
