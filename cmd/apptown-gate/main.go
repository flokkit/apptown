// Command apptown-gate runs AppTown access gate in front of a restored copy.
// It is configured only through the environment (see internal/gate) and
// logs one line at startup, never anything about a request.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/flokkit/apptown/internal/gate"
)

func main() {
	cfg, err := gate.ConfigFromEnv(os.Getenv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "apptown-gate: refusing to start:\n%v\n", err)
		os.Exit(2)
	}

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           gate.New(cfg, gate.Options{}),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		// No write timeout: sessions carry streams and upgraded
		// connections, which the gate ends at the session's deadline.
		// The server's own error log can quote client input; discard it.
		ErrorLog: log.New(io.Discard, "", 0),
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()

	fmt.Fprintf(os.Stderr, "apptown-gate: listening on %s, forwarding to %s\n", cfg.Listen, cfg.Upstream.Redacted())
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintf(os.Stderr, "apptown-gate: %v\n", err)
		os.Exit(1)
	}
}
