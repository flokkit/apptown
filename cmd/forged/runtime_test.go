package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func testDaemonServer(handler http.Handler) (*http.Server, <-chan string) {
	address := make(chan string, 1)
	return &http.Server{Addr: "127.0.0.1:0", Handler: handler, BaseContext: func(l net.Listener) context.Context {
		address <- "http://" + l.Addr().String()
		return context.Background()
	}}, address
}

func daemonAwait[T any](t *testing.T, result <-chan T) T {
	t.Helper()
	select {
	case value := <-result:
		return value
	case <-time.After(3 * time.Second):
		t.Fatal("daemon did not reach the expected boundary")
		var zero T
		return zero
	}
}

func TestDaemonShutdownDrainsBothListenersAndTasks(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) })
	api, apiAddress := testDaemonServer(handler)
	console, consoleAddress := testDaemonServer(handler)
	taskStopped := make(chan struct{})
	ownersStopped := make(chan struct{})
	d := daemonRuntime{servers: []*http.Server{api, console}, tasks: []daemonTask{{"jobs", func(ctx context.Context) error {
		<-ctx.Done()
		close(taskStopped)
		return nil
	}}}, stop: func(ctx context.Context) error {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("owners were given an unbounded shutdown context")
		}
		close(ownersStopped)
		return nil
	}}
	// Telemetry flushes last: after the task and the owners have stopped,
	// within the same shutdown budget.
	flushed := make(chan struct{})
	d.flush = func(ctx context.Context) error {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("flush was given an unbounded context")
		}
		for name, done := range map[string]chan struct{}{"task": taskStopped, "owners": ownersStopped} {
			select {
			case <-done:
			default:
				t.Errorf("flush ran before the %s stopped", name)
			}
		}
		close(flushed)
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	go func() { finished <- d.serve(ctx) }()
	urls := []string{daemonAwait(t, apiAddress), daemonAwait(t, consoleAddress)}
	client := &http.Client{Timeout: time.Second}
	for _, url := range urls {
		response, err := client.Get(url)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("listener returned %d", response.StatusCode)
		}
	}
	cancel()
	if err := daemonAwait(t, finished); err != nil {
		t.Fatal(err)
	}
	daemonAwait(t, taskStopped)
	daemonAwait(t, ownersStopped)
	daemonAwait(t, flushed)
	for _, url := range urls {
		if response, err := client.Get(url); err == nil {
			_ = response.Body.Close()
			t.Fatal("listener remained open after shutdown")
		}
	}
}

func TestDaemonShutdownHasOneBudgetForUncooperativeWork(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	entered := make(chan struct{}, 2)
	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		entered <- struct{}{}
		<-release
	})
	api, apiAddress := testDaemonServer(handler)
	console, consoleAddress := testDaemonServer(handler)
	cancelled := make(chan struct{}, 2)
	job := func(ctx context.Context) error { <-ctx.Done(); cancelled <- struct{}{}; <-release; return nil }
	stopDeadline := make(chan time.Time, 1)
	d := daemonRuntime{servers: []*http.Server{api, console}, grace: 250 * time.Millisecond,
		tasks: []daemonTask{{"long job", job}, {"slow probe", job}},
		stop: func(ctx context.Context) error {
			deadline, _ := ctx.Deadline()
			stopDeadline <- deadline
			<-release // deliberately ignores even the shutdown deadline
			return nil
		}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	go func() { finished <- d.serve(ctx) }()
	client := &http.Client{Timeout: 2 * time.Second}
	requests := make(chan struct{}, 2)
	for _, url := range []string{daemonAwait(t, apiAddress), daemonAwait(t, consoleAddress)} {
		go func() {
			response, err := client.Get(url)
			if err == nil {
				_ = response.Body.Close()
			}
			requests <- struct{}{}
		}()
	}
	daemonAwait(t, entered)
	daemonAwait(t, entered)
	start := time.Now()
	cancel()
	err := daemonAwait(t, finished)
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "long job") || !strings.Contains(err.Error(), "workflow, remediation and narrative") {
		t.Fatalf("unfinished work was not reported: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 750*time.Millisecond {
		t.Fatalf("component waits accumulated: %s for a %s shared budget", elapsed, d.grace)
	}
	deadline := daemonAwait(t, stopDeadline)
	if deadline.Sub(start) > d.grace+100*time.Millisecond {
		t.Fatal("execution owners received a fresh, later budget")
	}
	daemonAwait(t, cancelled)
	daemonAwait(t, cancelled)
	// Force-close releases HTTP clients even though their handlers ignore it.
	daemonAwait(t, requests)
	daemonAwait(t, requests)
	releaseOnce.Do(func() { close(release) })
}

func TestDaemonSecondListenerFailureDoesNotStartWorkers(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	d := daemonRuntime{servers: []*http.Server{{Addr: "127.0.0.1:0"}, {Addr: occupied.Addr().String()}},
		start: func() { t.Error("started workers before all listeners bound") },
		tasks: []daemonTask{{"jobs", func(context.Context) error { t.Error("started jobs on a failed boot"); return nil }}}}
	if err := d.serve(context.Background()); err == nil || !strings.Contains(err.Error(), "listening on") {
		t.Fatalf("listener failure hidden: %v", err)
	}
}

func TestDaemonListenerExitStopsOtherComponents(t *testing.T) {
	server, address := testDaemonServer(http.NotFoundHandler())
	stopped := make(chan struct{})
	d := daemonRuntime{servers: []*http.Server{server}, tasks: []daemonTask{{"jobs", func(ctx context.Context) error {
		<-ctx.Done()
		close(stopped)
		return nil
	}}}}
	finished := make(chan error, 1)
	go func() { finished <- d.serve(context.Background()) }()
	daemonAwait(t, address)
	_ = server.Close()
	if err := daemonAwait(t, finished); err != nil {
		t.Fatal(err)
	}
	daemonAwait(t, stopped)
}

func TestDaemonWorkerFailureStopsOtherComponents(t *testing.T) {
	for _, failure := range []string{"startup", "unexpected exit", "panic"} {
		t.Run(failure, func(t *testing.T) {
			stopped := make(chan struct{})
			d := daemonRuntime{tasks: []daemonTask{
				{"jobs", func(context.Context) error {
					switch failure {
					case "startup":
						return errors.New("queue unavailable")
					case "panic":
						panic("queue panic")
					}
					return nil
				}},
				{"scheduler", func(ctx context.Context) error { <-ctx.Done(); close(stopped); return nil }},
			}}
			finished := make(chan error, 1)
			go func() { finished <- d.serve(context.Background()) }()
			if err := daemonAwait(t, finished); err == nil || !strings.Contains(err.Error(), "jobs:") {
				t.Fatalf("worker failure hidden: %v", err)
			}
			daemonAwait(t, stopped)
		})
	}
}

func TestDaemonReportsWorkerShutdownError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	want := errors.New("queue could not flush")
	d := daemonRuntime{tasks: []daemonTask{{"jobs", func(ctx context.Context) error {
		cancel()
		<-ctx.Done()
		return want
	}}}}
	if err := d.serve(ctx); !errors.Is(err, want) || !strings.Contains(err.Error(), "jobs:") {
		t.Fatalf("shutdown error hidden: %v", err)
	}
}

func TestDaemonSIGTERMExitsWithBlockedJob(t *testing.T) {
	if os.Getenv("FORGE_TEST_SHUTDOWN_CHILD") == "1" {
		server, _ := testDaemonServer(http.NotFoundHandler())
		server.BaseContext = func(net.Listener) context.Context {
			fmt.Println("ready")
			return context.Background()
		}
		d := daemonRuntime{servers: []*http.Server{server}, grace: 100 * time.Millisecond,
			tasks: []daemonTask{{"blocked job", func(context.Context) error { select {} }}}}
		if err := d.serveWithSignals(); !errors.Is(err, context.DeadlineExceeded) {
			fmt.Fprintln(os.Stderr, "unexpected shutdown result:", err)
			os.Exit(2)
		}
		os.Exit(0) // the actual process boundary must not wait for the job
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDaemonSIGTERMExitsWithBlockedJob$")
	cmd.Env = append(os.Environ(), "FORGE_TEST_SHUTDOWN_CHILD=1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill() }()
	ready := make(chan string, 1)
	go func() { scanner := bufio.NewScanner(out); scanner.Scan(); ready <- scanner.Text() }()
	if line := daemonAwait(t, ready); line != "ready" {
		t.Fatalf("child did not start: %q", line)
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { finished <- cmd.Wait() }()
	if err := daemonAwait(t, finished); err != nil {
		t.Fatalf("SIGTERM shutdown: %v\n%s", err, stderr.String())
	}
}
