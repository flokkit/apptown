package diskspace_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/diskspace"
	"github.com/flokkit/apptown/internal/diskspace/diskspacetest"
	"github.com/flokkit/apptown/stock"
)

const roomy = "Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/x 104857600 10485760 94371840 10% /d\n"

// inspected is what a daemon shows of a probe container: its image and
// command, and an environment the evidence must leave out.
const inspected = `{"Image":"sha256:30ec","Path":"sh","Args":["-c","df"],"LogPath":"/var/lib/docker/containers/x/x-json.log",` +
	`"Config":{"Image":"busybox@sha256:bdf5","Env":["SECRET=hunter2"],"Cmd":["sh","-c","df"]},` +
	`"Mounts":[{"Source":"/var/lib/docker","Destination":"/d","RW":false}],"HostConfig":{"LogConfig":{"Type":"json-file"}},` +
	`"State":{"Status":"exited","ExitCode":0,"StartedAt":"s","FinishedAt":"f"}}`

// scripted is a daemon for ReadThroughDocker: it records each call,
// answers a step from answer (by diskspacetest.Step), and otherwise
// answers as a daemon with room whose sweep finds nothing: wait 0, the
// result files of a df with room, the same on the log.
type scripted struct {
	mu     sync.Mutex
	calls  [][]string
	answer func(ctx context.Context, step string, args []string) (string, string, error, bool)
}

func (s *scripted) docker(ctx context.Context, args ...string) (string, string, error) {
	s.mu.Lock()
	s.calls = append(s.calls, args)
	s.mu.Unlock()
	step := diskspacetest.Step(args)
	if s.answer != nil {
		if stdout, stderr, err, ok := s.answer(ctx, step, args); ok {
			return stdout, stderr, err
		}
	}
	switch {
	case step == "wait":
		return "0\n", "", nil
	case step == "cp":
		_, err := diskspacetest.WriteResult(args, roomy, "", "0")
		return "", "", err
	case step == "logs":
		return roomy, "", nil
	case step == "inspect":
		return inspected, "", nil
	case step == "image":
		return `{"Id":"sha256:30ec","RepoDigests":["busybox@sha256:bdf5"],"Os":"linux","Architecture":"amd64"}`, "", nil
	case step == "version":
		return `{"Client":{"Version":"29.8.1"},"Server":{"Version":"28.0.4","Components":[{"Name":"containerd","Version":"1.7.27"},{"Name":"runc","Version":"1.2.5"}]}}`, "", nil
	}
	return "", "", nil
}

// steps is each call's step; the sweep's removals as "sweep-rm <id>".
func (s *scripted) steps() []string {
	var out []string
	for _, c := range s.calls {
		step := diskspacetest.Step(c)
		if step == "" && c[0] == "rm" {
			step = "sweep-rm " + c[len(c)-1]
		}
		out = append(out, step)
	}
	return out
}

func fixedNow() time.Time { return time.Unix(1_800_000_000, 0) }

// A read through Docker takes its answer from the result file it copies
// out of the exited container, never from the container's output: it
// creates a named, labelled container with /d read-only, starts it, waits
// for it, copies its result folder, reads its log only to compare, and
// removes it.
//
// Kills: the answer read from the log; the container left unnamed or
// unlabelled; /d writable; the removal dropped.
func TestADiskReadTakesItsAnswerFromTheResultFile(t *testing.T) {
	t.Parallel()
	s := &scripted{}
	var reports []string
	space, err := diskspace.ReadThroughDocker(t.Context(), s.docker, testDF, "/var/lib/docker", fixedNow, func(r string) { reports = append(reports, r) })
	if err != nil || space.TotalBytes != 104857600<<10 || space.FreeBytes != 94371840<<10 {
		t.Fatalf("read %+v, %v", space, err)
	}
	if got := strings.Join(s.steps(), " "); got != "sweep create start wait cp logs rm" {
		t.Fatalf("steps: %s", got)
	}
	if len(reports) != 0 {
		t.Fatalf("a read whose log agrees with its file reported %q", reports)
	}
	create := strings.Join(s.calls[1], " ")
	for _, w := range []string{"--name apptown-disk-probe-", "--label apptown.class=disk-probe", "--label apptown.deadline=1800000600",
		"--network none", "-v /var/lib/docker:/d:ro", testDF + " sh -c", "df -Pk /d >/probe/df.out 2>/probe/df.err"} {
		if !strings.Contains(create, w) {
			t.Errorf("create does not say %q: %s", w, create)
		}
	}
	name := s.calls[1][2]
	for _, c := range s.calls[2:] {
		if !strings.Contains(strings.Join(c, " "), name) {
			t.Errorf("%v is not about the container created, %s", c, name)
		}
	}
}

// A result file with a valid answer is accepted though the container's
// log lacks it or cannot be read (the loss the file is for), and the
// disagreement is reported with what Docker shows of the container, its
// image and versions, and never its environment.
//
// Kills: the log required to agree; the disagreement not reported; the
// container's Env kept in the evidence.
func TestAResultFileIsTheAnswerWhenTheLogLacksIt(t *testing.T) {
	t.Parallel()
	for _, logs := range []struct {
		name string
		err  error
	}{{"empty", nil}, {"unreadable", errors.New("exit status 1")}} {
		t.Run(logs.name, func(t *testing.T) {
			t.Parallel()
			s := &scripted{answer: func(_ context.Context, step string, _ []string) (string, string, error, bool) {
				if step == "logs" {
					return "", "", logs.err, true
				}
				return "", "", nil, false
			}}
			var reports []string
			space, err := diskspace.ReadThroughDocker(t.Context(), s.docker, testDF, "/var/lib/docker", fixedNow, func(r string) { reports = append(reports, r) })
			if err != nil || space.FreeBytes != 94371840<<10 {
				t.Fatalf("a valid result file with a %s log: %+v %v", logs.name, space, err)
			}
			if len(reports) != 1 {
				t.Fatalf("reports: %q", reports)
			}
			for _, w := range []string{"answer was in its result file but not in what docker logs returned", `df.rc "0"`,
				"container image sha256:30ec (busybox@sha256:bdf5)", "log json-file at /var/lib/docker/containers/x/x-json.log",
				"mounts [/var/lib/docker:/d rw=false]", "state exited exit 0", "image sha256:30ec", "linux/amd64",
				"docker client 29.8.1, server 28.0.4 (containerd 1.7.27, runc 1.2.5)"} {
				if !strings.Contains(reports[0], w) {
					t.Errorf("the report does not say %q: %s", w, reports[0])
				}
			}
			if strings.Contains(reports[0], "hunter2") {
				t.Errorf("the report keeps the container's environment: %s", reports[0])
			}
		})
	}
}

// A read without a usable result refuses: the copy failed, the container
// or df did not exit 0, a result file is missing or its status empty, or
// df's output is empty or not df's. Each says why, what the file and the
// log held, and what Docker shows of the container; and the container is
// removed, as it is when the caller stops during the read, whose stop the
// removal does not inherit.
//
// Kills: a result accepted on the container's exit alone, without df's
// recorded status, or from a failed copy; the removal only on success, or
// on the caller's context; the evidence dropped from the refusal.
func TestADiskReadWithoutAUsableResultRefusesAndRemovesItsContainer(t *testing.T) {
	t.Parallel()
	failing := errors.New("exit status 1")
	for _, tc := range []struct {
		name   string
		answer func(cancel context.CancelFunc, step string, args []string) (string, string, error, bool)
		want   []string
	}{
		{"create refused", func(_ context.CancelFunc, step string, _ []string) (string, string, error, bool) {
			return "", "daemon says no", failing, step == "create"
		}, []string{"creating the probe container apptown-disk-probe-", `stderr "daemon says no"`}},
		{"start failed", func(_ context.CancelFunc, step string, _ []string) (string, string, error, bool) {
			return "", "daemon says no", failing, step == "start"
		}, []string{"starting the probe container", `stderr "daemon says no"`, "state exited"}},
		{"copy failed", func(_ context.CancelFunc, step string, args []string) (string, string, error, bool) {
			if step != "cp" {
				return "", "", nil, false
			}
			_, _ = diskspacetest.WriteResult(args, roomy, "", "0") // even with the files there
			return "", "Error: no such path", failing, true
		}, []string{"copying the probe's result", `stderr "Error: no such path"`}},
		{"container failed", func(_ context.CancelFunc, step string, _ []string) (string, string, error, bool) {
			return "137\n", "", nil, step == "wait"
		}, []string{`the probe container exited "137"`, "docker client 29.8.1"}},
		{"df failed", func(_ context.CancelFunc, step string, args []string) (string, string, error, bool) {
			if step != "cp" {
				return "", "", nil, false
			}
			_, err := diskspacetest.WriteResult(args, roomy, "df: /d: can't find mount point", "1")
			return "", "", err, true
		}, []string{`df exited "1"`, `df.err "df: /d: can't find mount point"`}},
		{"no status", func(_ context.CancelFunc, step string, args []string) (string, string, error, bool) {
			if step != "cp" {
				return "", "", nil, false
			}
			_, err := diskspacetest.WriteResult(args, roomy, "", "")
			return "", "", err, true
		}, []string{"the probe's result is incomplete: df.rc (empty)"}},
		{"no files", func(_ context.CancelFunc, step string, _ []string) (string, string, error, bool) {
			return "", "", nil, step == "cp"
		}, []string{"the probe's result is incomplete: df.out, df.err, df.rc"}},
		{"empty answer", func(_ context.CancelFunc, step string, args []string) (string, string, error, bool) {
			if step != "cp" {
				return "", "", nil, false
			}
			_, err := diskspacetest.WriteResult(args, "", "", "0")
			return "", "", err, true
		}, []string{`df answered ""`, `its log: stdout`}},
		{"not df's answer", func(_ context.CancelFunc, step string, args []string) (string, string, error, bool) {
			if step != "cp" {
				return "", "", nil, false
			}
			_, err := diskspacetest.WriteResult(args, "Filesystem\n/dev/x lots\n", "", "0")
			return "", "", err, true
		}, []string{`df answered "/dev/x lots"`}},
		{"caller stopped", func(cancel context.CancelFunc, step string, _ []string) (string, string, error, bool) {
			if step != "wait" {
				return "", "", nil, false
			}
			cancel()
			return "", "", context.Canceled, true
		}, []string{"waiting for the probe container", "context canceled"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			removedLive := false
			s := &scripted{}
			s.answer = func(ctx context.Context, step string, args []string) (string, string, error, bool) {
				if step == "rm" {
					removedLive = ctx.Err() == nil
					return "", "", nil, true
				}
				return tc.answer(cancel, step, args)
			}
			_, err := diskspace.ReadThroughDocker(ctx, s.docker, testDF, "/var/lib/docker", fixedNow, func(r string) { t.Errorf("a failed read reported %q", r) })
			if err == nil {
				t.Fatal("a read without a usable result answered")
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("the error does not say %q: %v", w, err)
				}
			}
			if strings.Contains(err.Error(), "hunter2") {
				t.Errorf("the error keeps the container's environment: %v", err)
			}
			steps := s.steps()
			if steps[len(steps)-1] != "rm" || !removedLive {
				t.Fatalf("the container was not removed (steps %v, removal live %v)", steps, removedLive)
			}
			if rm := s.calls[len(s.calls)-1]; rm[len(rm)-1] != s.calls[1][2] {
				t.Fatalf("removed %v, not the container created %s", rm, s.calls[1][2])
			}
		})
	}
}

// Each read first removes the probe containers that earlier reads left
// (past their deadline, or with none readable), and leaves alone those
// of a read still in progress.
//
// Kills: sweeping by class alone (a concurrent read's container removed
// under it); no sweep (a failed removal leaks for good).
func TestADiskReadSweepsOnlyExpiredProbeContainers(t *testing.T) {
	t.Parallel()
	now := fixedNow().Unix()
	s := &scripted{answer: func(_ context.Context, step string, _ []string) (string, string, error, bool) {
		if step == "sweep" {
			return fmt.Sprintf("old %d\nlive %d\nunlabelled \n", now-1, now+600), "", nil, true
		}
		return "", "", nil, false
	}}
	if _, err := diskspace.ReadThroughDocker(t.Context(), s.docker, testDF, "/var/lib/docker", fixedNow, nil); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(s.steps(), ",")
	if got != "sweep,sweep-rm old,sweep-rm unlabelled,create,start,wait,cp,logs,rm" {
		t.Fatalf("steps: %s", got)
	}
}

// Rejected inspect output must not bypass the evidence field allowlist.
func TestRejectedInspectDoesNotKeepEnvironment(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"decode", "command"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			s := &scripted{answer: func(_ context.Context, step string, _ []string) (string, string, error, bool) {
				if step == "logs" {
					return "", "", nil, true
				}
				if step == "inspect" {
					out := `{"Config":{"Env":["SECRET=hunter2"]},"Image":42}`
					if mode == "command" {
						return out, "connection lost", errors.New("exit status 1"), true
					}
					return out, "", nil, true
				}
				return "", "", nil, false
			}}
			var reports []string
			_, err := diskspace.ReadThroughDocker(t.Context(), s.docker, testDF, "/var/lib/docker", fixedNow, func(r string) { reports = append(reports, r) })
			if err != nil {
				t.Fatal(err)
			}
			if len(reports) != 1 {
				t.Fatalf("reports: %q", reports)
			}
			if strings.Contains(reports[0], "hunter2") {
				t.Errorf("rejected inspect retained the environment: %s", reports[0])
			}
		})
	}
}

// testDF is the df image the probe tests read with, the stock tool
// versions' as a caller's snapshot would give it.
var testDF = stock.Images().DF
