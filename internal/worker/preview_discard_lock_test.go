package worker

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy/docker"
)

// Hold the first deletion until a boot, absent from the initial directory
// listing, has acquired its lock and created its app/tree. The final label
// sweep must not remove that app while the boot still holds its lock.
func TestRemovingEveryPreviewWaitsForABootsLock(t *testing.T) {
	state := t.TempDir()
	entered, release := make(chan struct{}), make(chan struct{})
	var removedNew atomic.Bool
	binary := dockerFunc(func(args []string) ([]byte, error) {
		switch {
		case args[0] == "rm" && args[len(args)-1] == "apptown-preview-change-1":
			close(entered)
			<-release
		case args[0] == "rm":
			removedNew.Store(true)
		case args[0] == "ps" && strings.Contains(strings.Join(args, " "), "apptown.class=preview"):
			return []byte("apptown-preview-change-2\n"), nil
		}
		return nil, nil
	})
	s := &Server{State: state, Previews: binary}
	if err := os.MkdirAll(filepath.Join(s.previewsRoot(), "apptown-preview-change-1"), 0700); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		req := httptest.NewRequest("DELETE", "/v1/previews", nil)
		req.Header.Set(PreviewSessionHeader, "1")
		req.Header.Set(PreviewSeqHeader, "1")
		s.deletePreviews(httptest.NewRecorder(), req)
	}()
	// The deletion reaching its first removal is the synchronisation point.
	select {
	case <-entered:
	case <-done:
		t.Fatal("the deletion finished without removing the first preview")
	}
	unlock := s.previewLock("apptown-preview-change-2")
	if err := os.MkdirAll(filepath.Join(s.previewsRoot(), "apptown-preview-change-2", "tree"), 0700); err != nil {
		unlock()
		t.Fatal(err)
	}
	close(release)
	// Either the corrected removal waits for the lock or it returns without
	// touching a new boot. The current code removes the app while locked.
	select {
	case <-done:
	case <-time.After(300 * time.Millisecond):
	}
	removed := removedNew.Load()
	unlock()
	<-done // a deletion that never finishes meets the test binary's deadline
	if removed {
		t.Fatal("discard-all removed a new boot's app while that preview's lock was held")
	}
}

// dockerFunc is an in-process Docker answering each command's arguments.
type dockerFunc func(args []string) ([]byte, error)

func (f dockerFunc) Run(_ context.Context, _ string, c docker.Command) ([]byte, error) {
	out, err := f(c.Args)
	if c.Stdout != nil { // a command that asked for its standard output alone
		_, _ = c.Stdout.Write(out)
		return nil, err
	}
	return out, err
}
