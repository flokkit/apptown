package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"
)

const daemonShutdownGrace = 5 * time.Second

type daemonTask struct {
	name string
	run  func(context.Context) error
}

type daemonRuntime struct {
	servers []*http.Server
	// wrapMain, when set, wraps the first server's listener (-local-tls).
	wrapMain func(net.Listener) net.Listener
	tasks    []daemonTask
	start    func()
	stop     func(context.Context) error
	flush    func(context.Context) error // after every producer has drained: telemetry
	grace    time.Duration
}

// Install signal handling after synchronous boot. Before then the process keeps
// the OS's immediate-exit behaviour, so a stuck boot cannot swallow SIGTERM.
func (d daemonRuntime) serveWithSignals() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// A second signal can force exit during the bounded graceful wait.
	reset := context.AfterFunc(ctx, stop)
	defer reset()
	return d.serve(ctx)
}

func (d daemonRuntime) serve(ctx context.Context) error {
	// Bind all listeners before starting recurring work. Failure of either
	// listener must not leave a partially serving daemon or live worker loops.
	listeners := make([]net.Listener, 0, len(d.servers))
	for _, server := range d.servers {
		listener, err := net.Listen("tcp", server.Addr)
		if err != nil {
			for _, opened := range listeners {
				_ = opened.Close()
			}
			return fmt.Errorf("listening on %s: %w", server.Addr, err)
		}
		if len(listeners) == 0 && d.wrapMain != nil {
			listener = d.wrapMain(listener)
		}
		listeners = append(listeners, listener)
	}
	if ctx.Err() != nil {
		for _, listener := range listeners {
			_ = listener.Close()
		}
		return nil
	}
	// The servers and tasks outlive the signal: they stop when shutdown
	// cancels them, after the listeners close.
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	defer cancel()
	failed := make(chan error, len(d.servers)+len(d.tasks))
	taskDone := make([]chan error, len(d.tasks))
	if d.start != nil {
		d.start()
	}
	for i, task := range d.tasks {
		done := make(chan error, 1)
		taskDone[i] = done
		go func() {
			var err error
			defer func() {
				if p := recover(); p != nil {
					err = fmt.Errorf("panicked: %v", p)
				}
				if runCtx.Err() == nil {
					if err == nil {
						err = errors.New("worker loop exited unexpectedly")
					}
					failed <- fmt.Errorf("%s: %w", task.name, err)
				}
				done <- err
				close(done)
			}()
			err = task.run(runCtx)
		}()
	}
	for i, server := range d.servers {
		go func() { failed <- server.Serve(listeners[i]) }()
	}
	var cause error
	select {
	case <-ctx.Done():
	case cause = <-failed:
	}
	grace := d.grace
	if grace <= 0 || grace > daemonShutdownGrace {
		grace = daemonShutdownGrace
	}
	// Shutdown runs after the signal cancelled ctx.
	shutdown, deadline := context.WithTimeout(context.WithoutCancel(ctx), grace)
	defer deadline()
	cancel()
	log.Printf("apptownd: shutting down (maximum wait %s)", grace)

	type result struct {
		name string
		err  error
	}
	results := make(chan result, len(d.servers)+len(d.tasks)+1)
	pending := map[string]bool{}
	wait := func(name string, drain func() error) {
		pending[name] = true
		go func() { results <- result{name, drain()} }()
	}
	if d.stop != nil {
		wait("workflow, remediation and narrative workers", func() error { return d.stop(shutdown) })
	}
	for i, server := range d.servers {
		wait(fmt.Sprintf("HTTP listener %d (%s)", i+1, server.Addr), func() error {
			err := server.Shutdown(shutdown)
			if err != nil {
				// This happens in the drain goroutine: even force-closing a
				// connection cannot extend the coordinator's deadline.
				_ = server.Close()
			}
			return err
		})
	}
	for i, task := range d.tasks {
		wait(task.name, func() error { return <-taskDone[i] })
	}
	var failures []error
	if cause != nil && !errors.Is(cause, http.ErrServerClosed) {
		failures = append(failures, cause)
	}
	for len(pending) > 0 {
		select {
		case r := <-results:
			delete(pending, r.name)
			if r.err != nil {
				failures = append(failures, fmt.Errorf("%s: %w", r.name, r.err))
			}
		case <-shutdown.Done():
			var names []string
			for name := range pending {
				names = append(names, name)
			}
			slices.Sort(names)
			failures = append(failures, fmt.Errorf("shutdown deadline reached; still waiting for %s: %w", strings.Join(names, ", "), shutdown.Err()))
			return errors.Join(append(failures, d.flushAfterDrain(shutdown))...)
		}
	}
	return errors.Join(append(failures, d.flushAfterDrain(shutdown))...)
}

// Adapt existing loops while checked workers can return operational failures.
func daemonLoop(run func(context.Context)) func(context.Context) error {
	return func(ctx context.Context) error { run(ctx); return nil }
}

// flushAfterDrain runs the flush step once the producers have stopped or the
// deadline has passed, with whatever budget remains. Telemetry is the last
// thing to stop, so the final spans and log lines of a shutdown are exported
// rather than dropped with the exporter.
func (d daemonRuntime) flushAfterDrain(ctx context.Context) error {
	if d.flush == nil {
		return nil
	}
	if err := d.flush(ctx); err != nil {
		return fmt.Errorf("telemetry flush: %w", err)
	}
	return nil
}
