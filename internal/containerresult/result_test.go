package containerresult_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/containerresult"
	"github.com/flokkit/apptown/internal/containerresult/containerresulttest"
)

// daemon is a stand-in Docker whose result commands answer as a says; its
// containers' stdout reaches nobody.
func daemon(a containerresulttest.Answer) (*containerresulttest.Daemon, containerresult.Docker) {
	d := &containerresulttest.Daemon{Answers: func(containerresulttest.Command) (containerresulttest.Answer, bool) { return a, true }}
	return d, func(_ context.Context, _ []string, args ...string) ([]byte, error) {
		out, err, ok := d.Run(args)
		if !ok {
			return nil, fmt.Errorf("unexpected docker %s", strings.Join(args, " "))
		}
		return out, err
	}
}

func TestRunReadsTheAnswerFromItsFileThoughNothingItPrintedArrived(t *testing.T) {
	t.Parallel()
	d, dk := daemon(containerresulttest.Answer{Answer: []byte("1000\n1000\n")})
	got, err := containerresult.Run(context.Background(), dk, containerresult.Spec{Image: "busybox", Script: "id -u app && id -g app"})
	if err != nil || string(got) != "1000\n1000\n" {
		t.Fatalf("got %q, %v; want the file's answer", got, err)
	}
	if d.Removed != 1 {
		t.Fatalf("removed %d containers, want the one it made", d.Removed)
	}
}

func TestRunWithNoAnswerIsUnknownNotEmpty(t *testing.T) {
	t.Parallel()
	d, dk := daemon(containerresulttest.Answer{})
	got, err := containerresult.Run(context.Background(), dk, containerresult.Spec{Image: "busybox", Script: "dockerd --version"})
	if !errors.Is(err, containerresult.ErrNoResult) || got != nil {
		t.Fatalf("got %q, %v; want ErrNoResult", got, err)
	}
	if d.Removed != 1 {
		t.Fatalf("removed %d containers, want the one it made", d.Removed)
	}
}

func TestRunAnEmptyAnswerIsTheCallersToJudge(t *testing.T) {
	t.Parallel()
	_, dk := daemon(containerresulttest.Answer{Answer: []byte{}})
	got, err := containerresult.Run(context.Background(), dk, containerresult.Spec{Image: "busybox", Script: "ls /cache"})
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("got %q, %v; want an empty answer that is there", got, err)
	}
}

func TestRunReportsANonZeroExitAndKeepsNoAnswer(t *testing.T) {
	t.Parallel()
	d, dk := daemon(containerresulttest.Answer{Code: 3, Answer: []byte("partial")})
	got, err := containerresult.Run(context.Background(), dk, containerresult.Spec{Image: "busybox", Script: "exit 3"})
	var exit *containerresult.ExitError
	if !errors.As(err, &exit) || exit.Code != 3 || got != nil {
		t.Fatalf("got %q, %v; want exit status 3", got, err)
	}
	if d.Removed != 1 {
		t.Fatalf("removed %d containers, want the one it made", d.Removed)
	}
}

func TestRunRefusesAnAnswerOverItsBound(t *testing.T) {
	t.Parallel()
	_, dk := daemon(containerresulttest.Answer{Answer: bytes.Repeat([]byte("x"), 11)})
	if _, err := containerresult.Run(context.Background(), dk, containerresult.Spec{Image: "busybox", Script: "ls", Max: 10}); err == nil || !strings.Contains(err.Error(), "over 10 bytes") {
		t.Fatalf("got %v; want the answer refused, never cut short", err)
	}
}

func TestRunRemovesItsContainerWhenTheCallerStops(t *testing.T) {
	t.Parallel()
	for _, read := range []struct {
		name string
		run  func(context.Context, containerresult.Docker, containerresult.Spec) ([]byte, error)
	}{{"regular", containerresult.Run}, {"owned diagnostic", containerresult.RunOwned}} {
		t.Run(read.name, func(t *testing.T) {
			d, dk := daemon(containerresulttest.Answer{Answer: []byte("x")})
			ctx, cancel := context.WithCancel(context.Background())
			stopping := func(c context.Context, env []string, args ...string) ([]byte, error) {
				if args[0] == "wait" {
					cancel()
					return nil, context.Canceled
				}
				if c.Err() != nil {
					return nil, c.Err() // a removal that inherited the stop would fail here
				}
				return dk(c, env, args...)
			}
			if _, err := read.run(ctx, stopping, containerresult.Spec{Image: "busybox", Script: "sleep 9"}); !errors.Is(err, context.Canceled) {
				t.Fatalf("got %v; want the stop", err)
			}
			if d.Removed != 1 {
				t.Fatalf("removed %d containers after the stop, want the one it made", d.Removed)
			}
		})
	}
}

func TestRunSweepsOnlyResultContainersPastTheirDeadline(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_000_000, 0)
	var removed []string
	_, inner := daemon(containerresulttest.Answer{Answer: []byte("x")})
	dk := func(ctx context.Context, env []string, args ...string) ([]byte, error) {
		switch {
		case args[0] == "ps":
			class := ""
			for _, arg := range args {
				if strings.HasPrefix(arg, "label=apptown.class=") {
					class = strings.TrimPrefix(arg, "label=apptown.class=")
				}
			}
			if class != containerresult.Class && class != "disk-probe" {
				t.Fatalf("sweeping unrelated class %q", class)
			}
			return []byte(fmt.Sprintf("%s-old %d\n%s-fresh %d\n%s-unlabelled \n", class, now.Unix()-1, class, now.Unix()+60, class)), nil
		case args[0] == "rm" && !strings.HasPrefix(args[len(args)-1], containerresult.Prefix):
			removed = append(removed, args[len(args)-1])
			return nil, nil
		}
		return inner(ctx, env, args...)
	}
	if _, err := containerresult.Run(context.Background(), dk, containerresult.Spec{Image: "busybox", Script: "true", Now: func() time.Time { return now }}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(removed, " ") != "container-result-old container-result-unlabelled disk-probe-old disk-probe-unlabelled" {
		t.Fatalf("swept %v; want the one past its deadline and the one with none, never one in use", removed)
	}
}

func TestRunGivesEnvByNameOnly(t *testing.T) {
	t.Parallel()
	_, inner := daemon(containerresulttest.Answer{Answer: []byte("200\n")})
	var created []string
	var env []string
	dk := func(ctx context.Context, e []string, args ...string) ([]byte, error) {
		if args[0] == "create" {
			created, env = args, e
		}
		return inner(ctx, e, args...)
	}
	if _, err := containerresult.Run(context.Background(), dk, containerresult.Spec{Image: "busybox", Script: "echo $SECRET", Env: []string{"SECRET=hunter2"}}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(created, " "), "hunter2") || !strings.Contains(strings.Join(created, " "), "-e SECRET") || strings.Join(env, " ") != "SECRET=hunter2" {
		t.Fatalf("created with %q and CLI env %q; want the value in the CLI's environment only", created, env)
	}
}

func TestRunInReadsTheAnswerFromItsFileAndRemovesIt(t *testing.T) {
	t.Parallel()
	d, dk := daemon(containerresulttest.Answer{Answer: []byte("rows 12\n")})
	got, err := containerresult.RunIn(context.Background(), dk, "db", containerresult.Spec{Script: "echo rows 12"})
	if err != nil || string(got) != "rows 12\n" {
		t.Fatalf("got %q, %v; want the file's answer", got, err)
	}
	if d.Cleared != 1 {
		t.Fatalf("cleared %d files, want the one it wrote", d.Cleared)
	}
}

func TestRunInWithNoAnswerIsUnknown(t *testing.T) {
	t.Parallel()
	d, dk := daemon(containerresulttest.Answer{})
	if _, err := containerresult.RunIn(context.Background(), dk, "db", containerresult.Spec{Script: "echo rows 12"}); !errors.Is(err, containerresult.ErrNoResult) {
		t.Fatalf("got %v; want ErrNoResult", err)
	}
	if d.Cleared != 1 {
		t.Fatalf("cleared %d files, want the removal tried", d.Cleared)
	}
}

func TestRunInReportsANonZeroExit(t *testing.T) {
	t.Parallel()
	_, dk := daemon(containerresulttest.Answer{Code: 2})
	var exit *containerresult.ExitError
	if _, err := containerresult.RunIn(context.Background(), dk, "db", containerresult.Spec{Script: "exit 2"}); !errors.As(err, &exit) || exit.Code != 2 {
		t.Fatalf("got %v; want exit status 2", err)
	}
}

// A read whose caller has stopped before its container is made asks Docker
// to remove nothing: the removal outlives the stop, and a container never
// made needs none.
func TestRunOfAStoppedCallerRemovesNothing(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var reached []string
	dk := func(c context.Context, _ []string, args ...string) ([]byte, error) {
		if c.Err() != nil {
			return nil, c.Err() // refused before it starts, as the CLI is
		}
		reached = append(reached, strings.Join(args, " "))
		return nil, nil
	}
	if _, err := containerresult.Run(ctx, dk, containerresult.Spec{Image: "busybox", Script: "ls"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v; want the stop", err)
	}
	if len(reached) != 0 {
		t.Fatalf("asked Docker %q after the stop", reached)
	}
}

func TestOwnedResultDoesNotSweepSiblingsAndReportsFailedRemoval(t *testing.T) {
	for _, failRemoval := range []bool{false, true} {
		t.Run(fmt.Sprintf("removal-fails=%v", failRemoval), func(t *testing.T) {
			d, base := daemon(containerresulttest.Answer{Answer: []byte("204\n")})
			made := ""
			removed := ""
			removalErr := errors.New("owned removal refused")
			dk := func(ctx context.Context, env []string, args ...string) ([]byte, error) {
				switch args[0] {
				case "ps":
					t.Fatal("owned result swept same-daemon sibling resources")
				case "create":
					for i, arg := range args {
						if arg == "--name" {
							made = args[i+1]
						}
					}
					if !strings.Contains(strings.Join(args, " "), "apptown.class=container-result-owned") {
						t.Error("owned diagnostic remains eligible for legacy broad sweep")
					}
				case "rm":
					removed = args[len(args)-1]
					if removed != made || made == "" {
						t.Fatalf("removed unowned resource %q, made %q", removed, made)
					}
					if failRemoval {
						return nil, removalErr
					}
				}
				return base(ctx, env, args...)
			}
			got, err := containerresult.RunOwned(t.Context(), dk, containerresult.Spec{Image: "probe", Script: "printf 204"})
			if failRemoval {
				if !errors.Is(err, removalErr) || !strings.Contains(err.Error(), made) || got != nil {
					t.Fatalf("lost cleanup evidence: %q %v", got, err)
				}
			} else if err != nil || string(got) != "204\n" || d.Removed != 1 {
				t.Fatalf("owned result: %q %v removed%d", got, err, d.Removed)
			}
			if made == "" || removed != made {
				t.Fatal("owned cleanup did not run")
			}
		})
	}
}
