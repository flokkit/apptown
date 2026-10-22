package apptown

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// Each apptown start with a worker takes a session greater than the last,
// even when the last was taken while the clock ran ahead; a start whose
// session count cannot be kept does not run worker previews.
func TestTwoStartsNeverShareASession(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	dir := t.TempDir()
	ahead := time.Now().Add(time.Hour).UnixNano()
	counter := filepath.Join(dir, "worker-session")
	if err := os.WriteFile(counter, []byte(strconv.FormatInt(ahead, 10)), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := withPreviewWorker(t, &fakePreviewWorker{}, Config{DataDir: dir, DockerClient: newFakeDocker()})
	var previous int64
	for i := 0; i < 2; i++ {
		app, err := New(context.Background(), db, cfg)
		if err != nil {
			t.Fatal(err)
		}
		if err := app.ShutdownBackground(context.Background()); err != nil {
			t.Fatal(err)
		}
		session, err := strconv.ParseInt(app.workerPreviews.Session, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		if session <= previous {
			t.Fatalf("two starts enabled worker previews with the same unpersisted session: %d then %d", previous, session)
		}
		previous = session
	}
	// A count that cannot be kept (here, a folder where it belongs) refuses
	// the start: it never runs with a session it could not keep.
	if err := os.Remove(counter); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(counter, "in-the-way"), 0o700); err != nil {
		t.Fatal(err)
	}
	if app, err := New(context.Background(), db, cfg); err == nil {
		_ = app.ShutdownBackground(context.Background())
		if app.workerPreviews != nil {
			t.Fatalf("a start whose session count could not be kept ran worker previews with session %s", app.workerPreviews.Session)
		}
	}
}
