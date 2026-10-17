package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// interruptible runs the CLI's git (through run) so that Ctrl-C still stops
// it. gitexec starts git in a process group of its own, out of the
// terminal's foreground group, so the terminal's interrupt reaches only the
// CLI; left alone, the CLI would die and its git (a push, say) would carry
// on. Here an interrupt, or a SIGTERM, cancels run's context instead, which
// kills git's group; once run has returned, with git gone, the CLI ends as
// the signal would have ended it.
func interruptible(parent context.Context, run func(context.Context) error) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	got := make(chan os.Signal, 1)
	go func() {
		select {
		case s := <-signals:
			got <- s
			cancel()
		case <-ctx.Done():
		}
	}()
	err := run(ctx)
	signal.Stop(signals)
	cancel()
	select {
	case s := <-got:
		code := 1
		if n, ok := s.(syscall.Signal); ok {
			code = 128 + int(n)
		}
		os.Exit(code)
	default:
	}
	return err
}
