package pi

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"testing"
	"time"
)

// Startup diagnostics name the phase and session, but never launch arguments
// (which contain credentials) or Docker output. A failed readiness probe must
// not claim the helper was ready.
func TestStartupTimingsNamePhasesWithoutLaunchSecrets(t *testing.T) {
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previous) })
	pane := paneAnswers(func() string { return "pi" })
	at := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	r := New(Config{Now: func() time.Time { at = at.Add(time.Second); return at }, Run: func(_ context.Context, args ...string) ([]byte, error) {
		if out, err, ok := pane(args); ok {
			return out, err
		}
		return []byte("private Docker output"), nil
	}})
	defer r.Close()
	for _, args := range [][]string{
		{"create", "--name", "chat-measure", "-e", "TOKEN=secret-value", "image"},
		{"start", "chat-measure"},
	} {
		if _, err := r.command(t.Context(), args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.awaitPi(t.Context(), "chat-measure"); err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{"container_create", "container_start", "helper_ready"} {
		if !strings.Contains(logs.String(), "phase="+phase+" duration=") {
			t.Fatalf("missing %s: %s", phase, &logs)
		}
	}
	if !strings.Contains(logs.String(), "phase=container_create duration=1s") {
		t.Fatalf("duration did not follow the runtime clock: %s", &logs)
	}
	if strings.Contains(logs.String(), "secret-value") || strings.Contains(logs.String(), "private Docker output") {
		t.Fatalf("startup log exposed launch data: %s", &logs)
	}
	logs.Reset()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := r.awaitPi(ctx, "chat-measure"); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want cancellation", err)
	}
	if !strings.Contains(logs.String(), "phase=helper_ready") || strings.Contains(logs.String(), "error=<nil>") {
		t.Fatalf("failed readiness logged as success: %s", &logs)
	}
}
