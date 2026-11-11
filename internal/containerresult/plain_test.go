package containerresult_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/flokkit/apptown/internal/containerresult"
	"github.com/flokkit/apptown/internal/containerresult/containerresulttest"
)

// plainDaemon is d behind a proxy that carries only plain requests
// (Portainer's): docker run -d and an attached exec never reach it, and
// every command is recorded. cp, when set, answers a copy first.
type plainDaemon struct {
	d        *containerresulttest.Daemon
	mu       sync.Mutex
	commands [][]string
	cp       func(ctx context.Context, args []string) ([]byte, error, bool)
}

func (p *plainDaemon) dk(ctx context.Context, env []string, args ...string) ([]byte, error) {
	p.mu.Lock()
	p.commands = append(p.commands, append([]string(nil), args...))
	p.mu.Unlock()
	switch {
	case args[0] == "run":
		return nil, errors.New("docker run waited on a container it had not started")
	case args[0] == "exec" && args[1] != "-d":
		return []byte("unable to upgrade to tcp, received 200"), errors.New("exit status 1")
	case args[0] == "cp" && p.cp != nil:
		if out, err, ok := p.cp(ctx, args); ok {
			return out, err
		}
	}
	out, err, ok := p.d.RunEnv(args, env)
	if !ok {
		return nil, fmt.Errorf("unexpected command %v", args)
	}
	return out, err
}

func plainSpec(script string) containerresult.Spec {
	return containerresult.Spec{Image: "busybox@sha256:plain", Options: []string{"--network", "none"}, Script: script, Plain: true}
}

// Through a plain proxy the daemon's standing reader is made with create
// and start and read with a detached exec, its status and answer copied
// out: an hour of readings still starts one container.
//
// Kills: a plain read made with docker run -d or read with an attached
// exec, or one through a container of its own.
func TestAPlainDaemonsReadingsShareOneStandingReader(t *testing.T) {
	t.Parallel()
	p := &plainDaemon{d: &containerresulttest.Daemon{Answers: func(c containerresulttest.Command) (containerresulttest.Answer, bool) {
		return containerresulttest.Answer{Answer: []byte("plain answer")}, c.Script == "echo plain"
	}}}
	for i := range 72 {
		got, err := containerresult.RunStanding(t.Context(), p.dk, "plain-reader", plainSpec("echo plain"))
		if err != nil || string(got) != "plain answer" {
			t.Fatalf("reading %d: %q %v", i, got, err)
		}
	}
	if p.d.Started != 1 || len(p.d.Readers()) != 1 {
		t.Fatalf("72 readings started %d containers, readers %v; want one standing reader", p.d.Started, p.d.Readers())
	}
	for _, c := range p.commands {
		if c[0] == "run" || c[0] == "exec" && c[1] != "-d" {
			t.Fatalf("a plain read sent %v", c)
		}
	}
}

// A script's non-zero status is its error, with what it wrote to stderr;
// a zero status is read only once the status says the script ended.
//
// Kills: a status read from the detached exec's own answer, or an answer
// taken without a status.
func TestAPlainReadsStatusIsTheScripts(t *testing.T) {
	t.Parallel()
	p := &plainDaemon{d: &containerresulttest.Daemon{Answers: func(c containerresulttest.Command) (containerresulttest.Answer, bool) {
		return containerresulttest.Answer{Code: 5, Said: "df: /d: no such file"}, c.Script == "df /d"
	}}}
	_, err := containerresult.RunStanding(t.Context(), p.dk, "plain-status", plainSpec("df /d"))
	var exit *containerresult.ExitError
	if !errors.As(err, &exit) || exit.Code != 5 || !strings.Contains(exit.Said, "no such file") {
		t.Fatalf("a failing script: %v", err)
	}
}

// A script that leaves no status is an error once the wait ends, never an
// answer, and a copy that does not answer is held to the same bound.
//
// Kills: an answer read without its status, or a wait whose copies are
// not bounded.
func TestAPlainReadWithoutAStatusIsAnError(t *testing.T) {
	t.Parallel()
	for _, blocks := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			p := &plainDaemon{d: &containerresulttest.Daemon{Answers: func(c containerresulttest.Command) (containerresulttest.Answer, bool) {
				return containerresulttest.Answer{Answer: []byte("an answer")}, c.Script == "echo lost"
			}}}
			p.cp = func(ctx context.Context, args []string) ([]byte, error, bool) {
				if !strings.HasSuffix(args[1], ".code") {
					return nil, nil, false
				}
				if blocks {
					<-ctx.Done()
					return nil, ctx.Err(), true
				}
				return []byte("Error response from daemon: Could not find the file " + args[1]), errors.New("exit status 1"), true
			}
			got, err := containerresult.RunStanding(t.Context(), p.dk, "plain-lost", plainSpec("echo lost"))
			if err == nil || got != nil || !strings.Contains(err.Error(), "no exit status") {
				t.Fatalf("blocking copy %v: %q %v", blocks, got, err)
			}
		})
	}
}

// Reads that overlap in one reader each read their own answer: each has
// its own files.
//
// Kills: plain reads sharing a result path.
func TestOverlappingPlainReadsKeepTheirOwnAnswers(t *testing.T) {
	t.Parallel()
	p := &plainDaemon{d: &containerresulttest.Daemon{Answers: func(c containerresulttest.Command) (containerresulttest.Answer, bool) {
		if !strings.HasPrefix(c.Script, "echo ") {
			return containerresulttest.Answer{}, false
		}
		return containerresulttest.Answer{Answer: []byte(strings.TrimPrefix(c.Script, "echo "))}, true
	}}}
	// The second read's exec runs before the first's status is copied.
	var started atomic.Bool
	second := make(chan error, 1)
	p.cp = func(ctx context.Context, args []string) ([]byte, error, bool) {
		if started.CompareAndSwap(false, true) {
			got, err := containerresult.RunStanding(ctx, p.dk, "plain-overlap", plainSpec("echo second"))
			if err == nil && string(got) != "second" {
				err = fmt.Errorf("the second read answered %q", got)
			}
			second <- err
		}
		return nil, nil, false
	}
	got, err := containerresult.RunStanding(t.Context(), p.dk, "plain-overlap", plainSpec("echo first"))
	if err != nil || string(got) != "first" {
		t.Fatalf("the first read: %q %v", got, err)
	}
	if err := <-second; err != nil {
		t.Fatal(err)
	}
}
