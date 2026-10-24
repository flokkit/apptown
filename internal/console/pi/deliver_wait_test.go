package pi

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/console"
	"github.com/flokkit/apptown/internal/containerresult/containerresulttest"
)

// paneAnswers is a chat container whose tmux names the pane's command
// pane() through its answer file, its stdout lost; ok is false for any
// other call.
func paneAnswers(pane func() string) func(args []string) ([]byte, error, bool) {
	d := &containerresulttest.Daemon{Answers: func(c containerresulttest.Command) (containerresulttest.Answer, bool) {
		if !strings.Contains(c.Script, "display-message") {
			return containerresulttest.Answer{}, false
		}
		return containerresulttest.Answer{Answer: []byte(pane() + "\n")}, true
	}}
	return d.Run
}

// Delivering words presses Enter until the session's record shows them;
// the wait between presses ends with the request that asked.
func TestDeliveringWordsStopsWaitingWhenItsRequestEnds(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	pane := paneAnswers(func() string { return "pi" })
	r := New(Config{Run: func(_ context.Context, args ...string) ([]byte, error) {
		// pi runs; the words are typed and Enter is pressed; the request then
		// ends, and the record (an empty session folder) never shows them.
		if out, err, ok := pane(args); ok {
			return out, err
		}
		if slices.Contains(args, "Enter") {
			cancel()
		}
		return nil, nil
	}})
	defer r.Close()
	done := make(chan error, 1)
	go func() { done <- r.Deliver(ctx, console.Launch{ID: "c1", Dir: t.TempDir()}, "hello") }()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("a delivery whose request ended answered %v", err)
		}
	case <-time.After(10 * time.Second): // the presses take 17 s
		t.Fatal("the wait between presses outlived the request")
	}
}

// Words wait for pi itself: while the chat's terminal still runs the shell
// that installs the project's toolchain, nothing is typed, because words and
// Enter presses typed then reach pi as one pasted block with a blank line per
// press, and are never sent. Once tmux names the pane's command "pi", the
// words are typed and Enter is pressed.
//
// Kills: Deliver typing before awaitPi, or awaitPi accepting any command.
func TestWordsWaitForPiToStart(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var mu sync.Mutex
	var seen []string
	pane := paneAnswers(func() string {
		seen = append(seen, "ask")
		if len(seen) < 3 {
			return "sh"
		}
		return "pi"
	})
	r := New(Config{Run: func(_ context.Context, args ...string) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		if out, err, ok := pane(args); ok {
			return out, err
		}
		switch {
		case slices.Contains(args, "-l"):
			seen = append(seen, "type")
		case slices.Contains(args, "Enter"):
			seen = append(seen, "enter")
			cancel() // the record is not the point here
		}
		return nil, nil
	}})
	defer r.Close()
	_ = r.Deliver(ctx, console.Launch{ID: "c1", Dir: t.TempDir()}, "hello")
	mu.Lock()
	defer mu.Unlock()
	if want := []string{"ask", "ask", "ask", "type", "enter"}; !slices.Equal(seen, want) {
		t.Fatalf("the terminal saw %v, want %v", seen, want)
	}
}
