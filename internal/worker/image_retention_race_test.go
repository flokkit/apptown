package worker_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/worker"
	"flokkit.io/forge/internal/worker/workertest"
)

// Preparation may overlap a sweep. Pause Docker's removal after the sweep
// read the old timestamp, then prepare the image before allowing deletion.
func TestAPreparationNeverReturnsAnImageBeingSwept(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var removed atomic.Bool
	answer := func(args []string) ([]byte, error) {
		switch args[0] {
		case "images":
			return []byte("sha256:app\n"), nil
		case "image":
			if removed.Load() {
				return []byte("No such image\n"), errExit
			}
			return []byte(`"sha256:app" {}` + "\n"), nil
		case "rmi":
			close(entered)
			<-release
			removed.Store(true)
		}
		return nil, nil
	}
	binary, _ := sweepDocker(t)
	binary.reply = func(args []string) ([]byte, error, bool) { out, err := answer(args); return out, err, true }
	ctx := t.Context()
	ca := workertest.NewCA(t)
	srv := &worker.Server{Installation: workertest.Installation, State: t.TempDir(), Checks: imageChecks{answer}}
	hs := workertest.StartServer(t, ca, srv)
	checks := worker.Checks{URL: hs.URL, Client: workertest.Client(t, ca, workertest.Installation)}
	uses := filepath.Join(srv.State, "check-images")
	if err := os.MkdirAll(uses, 0700); err != nil {
		t.Fatal(err)
	}
	used := filepath.Join(uses, "app")
	if err := os.WriteFile(used, nil, 0600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(used, old, old); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { srv.SweepChecks(ctx, binary, false); close(done) }()
	var released sync.Once
	resume := func() { released.Do(func() { close(release) }) }
	defer func() { resume(); <-done }()
	select {
	case <-entered:
	case <-done:
		t.Fatal("the sweep finished without removing the image")
	}
	// A preparation that overlaps the sweep waits for it: it cannot return
	// an image the sweep is removing.
	prepared := make(chan execution.ImageAnswer, 1)
	go func() { prepared <- checks.Image(ctx, execution.ImageRequest{Tag: "forge-conform:commit"}) }()
	select {
	case a := <-prepared:
		t.Fatalf("a preparation answered while the sweep was removing its image: %+v", a)
	case <-time.After(300 * time.Millisecond):
	}
	resume()
	<-done
	if a := <-prepared; a.ID != "" || a.Err != "" {
		t.Fatalf("a preparation after the sweep answered with the removed image: %+v", a)
	}
}

// imageChecks prepares check images on an in-process Docker that answer
// gives, and runs no check.
type imageChecks struct {
	answer func(args []string) ([]byte, error)
}

func (c imageChecks) Run(context.Context, string, execution.Request) execution.Outcome {
	return execution.Outcome{RunErr: errors.New("this worker runs no check"), Termination: execution.LaunchFailed, ExitCode: -1}
}

func (c imageChecks) PrepareImage(ctx context.Context, req execution.ImageRequest) execution.ImageAnswer {
	return execution.PrepareImage(ctx, func(_ context.Context, _ io.Reader, args ...string) ([]byte, error) { return c.answer(args) }, req)
}
