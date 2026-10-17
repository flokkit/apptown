package worker_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"flokkit.io/forge/internal/execfile"

	"flokkit.io/forge/internal/console"
	"flokkit.io/forge/internal/console/pi"
	"flokkit.io/forge/internal/worker"
	"flokkit.io/forge/internal/worker/workertest"
)

// The chat may change links under its
// writable folders at any time; replacing its files must not let such a link
// carry a removal (or a write) outside the chat's directory.
func TestReplacingAChatsFilesCannotFollowTheChatsLinks(t *testing.T) {
	c := startRemoteChats(t, console.Static{})
	s := c.open(t)
	ctx := context.Background()
	if err := c.service.Warm(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	dir := c.runtime.Started(s.ID).Dir
	if _, err := c.client.PutFiles(ctx, s.ID, bytes.NewReader(tarOf(t, entry{name: "pi-home/retired/victim", body: "staged"}))); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	victim := filepath.Join(outside, "victim")
	if err := os.WriteFile(victim, []byte("worker-owned"), 0o600); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(dir, "pi-home", "retired")
	if err := os.RemoveAll(parent); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, parent); err != nil { // what the chat may do
		t.Fatal(err)
	}
	if _, err := c.client.PutFiles(ctx, s.ID, bytes.NewReader(tarOf(t))); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(victim); err != nil || string(got) != "worker-owned" {
		t.Fatalf("a file outside the chat was removed: %q %v", got, err)
	}
	if _, err := c.client.PutFiles(ctx, s.ID, bytes.NewReader(tarOf(t, entry{name: "pi-home/retired/planted", body: "x"}))); err == nil {
		t.Fatal("a file was written through the chat's link")
	}
	if _, err := os.Stat(filepath.Join(outside, "planted")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a file was written outside the chat: %v", err)
	}
}

// A chat's record read by the forge cannot be a worker file the chat links to.
func TestAChatsRecordCannotShowTheWorkersFiles(t *testing.T) {
	c := startRemoteChats(t, console.Static{})
	s := c.open(t)
	ctx := context.Background()
	if err := c.service.Warm(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(t.TempDir(), "worker-key")
	if err := os.WriteFile(secret, []byte("worker secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(c.runtime.Started(s.ID).Dir, "sessions", "x.jsonl")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatal(err)
	}
	if got, err := c.client.Record(ctx, s.ID, "x.jsonl", 0); err == nil || bytes.Contains(got, []byte("worker secret")) {
		t.Fatalf("a linked worker file was read as the chat's record: %q %v", got, err)
	}
	// A missing record file is not a missing session.
	if _, err := c.client.Record(ctx, s.ID, "absent.jsonl", 0); err == nil || errors.Is(err, worker.ErrNoSession) {
		t.Fatalf("a missing record file: %v", err)
	}
}

// From the reviews (F2, R2-F1, R2-F2): a rebuild is decided when it acts,
// by Docker's answer alone: refused while the chat runs, whatever its record
// says (a turn begun since a "stale" answer, a planted link to a worker file
// that looks handed back, input typed on a terminal since closed); allowed
// once it is stopped.
func TestARebuildIsRefusedWhileTheChatRuns(t *testing.T) {
	c := startRemoteChats(t, console.Static{})
	s := c.open(t)
	ctx := context.Background()
	if err := c.service.Warm(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	c.runtime.Reply(s.ID, "done")
	c.runtime.MarkStale(s.ID)
	c.runtime.Working(s.ID)
	if _, err := c.client.Rebuild(ctx, s.ID); !errors.Is(err, worker.ErrSessionBusy) || !c.runtime.Running(s.ID) {
		t.Fatalf("a rebuild over a turn begun since: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "worker-file")
	body := `{"type":"message","message":{"role":"assistant","content":[{"type":"text","text":"outside the chat"}]}}` + "\n"
	if err := os.WriteFile(outside, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Minute)
	_ = os.Chtimes(outside, old, old)
	if err := os.Symlink(outside, filepath.Join(c.runtime.Started(s.ID).Dir, "sessions", "zz-outside.jsonl")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.client.Rebuild(ctx, s.ID); !errors.Is(err, worker.ErrSessionBusy) {
		t.Fatalf("a rebuild read a worker file as the chat's record: %v", err)
	}
	term, err := c.client.Terminal(ctx, s.ID, console.Size{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := term.Write([]byte("start the next turn\r")); err != nil {
		t.Fatal(err)
	}
	term.Close()
	if _, err := c.client.Rebuild(ctx, s.ID); !errors.Is(err, worker.ErrSessionBusy) {
		t.Fatalf("a rebuild after input on a closed terminal: %v", err)
	}
	if _, err := c.client.Stop(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.client.Rebuild(ctx, s.ID); err != nil || c.runtime.Exists(s.ID) {
		t.Fatalf("a rebuild of a stopped chat: %v", err)
	}
}

// From the round-2 review (R2-F4), with the production pi runtime and its
// real PTY and viewers (only Docker's answers and the attached process are
// stand-ins): stopping the chat ends the terminal the forge has open on it,
// and the stopped chat can then be rebuilt.
func TestStoppingAChatEndsItsTerminalOnTheForge(t *testing.T) {
	bin := t.TempDir()
	if err := execfile.Write(filepath.Join(bin, "docker"), []byte("#!/bin/sh\nexec /bin/cat\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	var mu sync.Mutex
	exists, running := true, true
	rt := pi.New(pi.Config{Image: func() string { return "agent:test" }, Run: func(ctx context.Context, args ...string) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		if strings.Contains(strings.Join(args, " "), "forge-dind-console-") {
			return []byte("Error response from daemon: No such container: x"), errors.New("absent")
		}
		switch args[0] {
		case "inspect":
			if !exists {
				return []byte("Error response from daemon: No such container: x"), errors.New("absent")
			}
			return []byte(fmt.Sprintf(`%t 2026-09-30T00:00:00Z image0 {"forge.console.dind":"0"}`, running)), nil
		case "image":
			return []byte("image0"), nil
		case "stop":
			running = false
		case "rm":
			exists, running = false, false
		}
		return nil, nil
	}})
	defer rt.Close()
	ca := workertest.NewCA(t)
	srv := workertest.StartServer(t, ca, &worker.Server{State: t.TempDir(), Sessions: rt})
	c := worker.SessionClient{URL: srv.URL, Client: workertest.Client(t, ca, workertest.Installation)}
	ctx := context.Background()
	const id = "stopterminal"
	if _, err := c.Put(ctx, id, worker.SessionSpec{Image: "agent:test", AgentSession: "console"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Start(ctx, id); err != nil {
		t.Fatal(err)
	}
	defer rt.Stop(ctx, id)
	term, err := c.Terminal(ctx, id, console.Size{})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	if st, err := c.Stop(ctx, id); err != nil || !st.Stopped() {
		t.Fatalf("stop: %+v %v", st, err)
	}
	read := make(chan error, 1)
	go func() {
		var p [1]byte
		for {
			if _, err := term.Read(p[:]); err != nil {
				read <- err
				return
			}
		}
	}()
	select {
	case err := <-read:
		if err != io.EOF {
			t.Fatalf("the terminal ended with %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stopping the chat left the forge's terminal open")
	}
	if _, err := c.Rebuild(ctx, id); err != nil {
		t.Fatalf("a stopped chat could not be rebuilt: %v", err)
	}
}

type reviewTransport func(*http.Request) (*http.Response, error)

func (f reviewTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// From the review (F3): a record file that shrinks or vanishes while it is
// copied makes the copy incomplete, and an incomplete copy never ends the
// chat or refreshes its "as of" time; a complete one does. A "no such
// session" answer to the copy (from anything on the way, a proxy say) is
// checked against the worker's own status before the copy is skipped.
func TestAnIncompleteRecordCopyDoesNotEndTheChat(t *testing.T) {
	for _, scenario := range []string{"short-read", "missing-file", "no-session-answer", "complete"} {
		t.Run(scenario, func(t *testing.T) {
			c := startRemoteChats(t, console.Static{})
			s := c.open(t)
			ctx := context.Background()
			if err := c.service.Warm(ctx, s.ID); err != nil {
				t.Fatal(err)
			}
			c.agent.Close()
			c.runtime.Reply(s.ID, "the final answer must be copied")
			recordDir := filepath.Join(c.runtime.Started(s.ID).Dir, "sessions")
			if err := os.WriteFile(filepath.Join(recordDir, "z-remaining.jsonl"), []byte("remaining record\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			base := c.client.Client.Transport
			hc := *c.client.Client
			hc.Transport = reviewTransport(func(r *http.Request) (*http.Response, error) {
				if strings.Contains(r.URL.Path, "/records/") && !strings.Contains(r.URL.Path, "z-remaining") {
					path := filepath.Join(recordDir, filepath.Base(r.URL.Path))
					switch scenario {
					case "short-read":
						_ = os.Truncate(path, 0)
					case "missing-file":
						_ = os.Remove(path)
					case "no-session-answer":
						return &http.Response{StatusCode: http.StatusNotFound, Status: "404 Not Found", Body: io.NopCloser(strings.NewReader("no such session")), Request: r}, nil
					}
				}
				return base.RoundTrip(r)
			})
			a := &worker.RemoteAgent{Sessions: worker.SessionClient{URL: c.client.URL, Client: &hc}, Root: t.TempDir()}
			defer a.Close()
			err := a.End(ctx, s.ID)
			st, serr := c.client.Status(ctx, s.ID)
			if serr != nil {
				t.Fatal(serr)
			}
			if scenario == "complete" {
				if err != nil || !st.Removed || recordAsOf(filepath.Join(a.Root, s.ID)).IsZero() {
					t.Fatalf("a complete copy: %v, removed %v", err, st.Removed)
				}
				return
			}
			if err == nil || st.Removed {
				t.Fatalf("an incomplete copy ended the chat: %v, removed %v", err, st.Removed)
			}
			if !recordAsOf(filepath.Join(a.Root, s.ID)).IsZero() {
				t.Fatal("an incomplete copy recorded its time")
			}
		})
	}
}

// From the review (F4): a terminal that ends on the worker ends for the forge.
func TestATerminalThatEndsOnTheWorkerEndsForTheForge(t *testing.T) {
	c := startRemoteChats(t, console.Static{})
	s := c.open(t)
	if err := c.service.Warm(context.Background(), s.ID); err != nil {
		t.Fatal(err)
	}
	term, err := c.client.Terminal(context.Background(), s.ID, console.Size{})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	c.runtime.EndScreen(s.ID)
	done := make(chan error, 1)
	go func() {
		var p [1]byte
		_, err := term.Read(p[:])
		done <- err
	}()
	select {
	case err := <-done:
		if err != io.EOF {
			t.Fatalf("the terminal ended with %v", err)
		}
	case <-time.After(5 * time.Second):
		term.Close()
		<-done
		t.Fatal("the chat's screen ended, but the forge's terminal stayed open")
	}
}
