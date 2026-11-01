package preview

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/ids/idstest"
	"github.com/flokkit/apptown/internal/worker"
	"github.com/flokkit/apptown/internal/worker/workertest"
)

// The real HTTP client stops waiting for a removal, while the worker's
// already-dispatched handler remains pending. A successful reuse then
// makes this preview wanted before the old DELETE resumes.
func TestADelayedRemovalNeverUndoesALaterReuse(t *testing.T) {
	s, _, _, dir, project := workerPreviewWorld(t)
	ch := Change{ID: idstest.N(9), ProjectID: project}
	writePreviewBootFixture(t, filepath.Join(dir, "workspaces", "change-00000000-0009-7000-8000-000000000000"))
	b, err := s.prepareChange(context.Background(), ch)
	if err != nil {
		t.Fatal(err)
	}
	name := docker.PreviewChangeName(ch.ID)
	state := t.TempDir()
	folder := filepath.Join(state, "previews", name)
	if err := os.MkdirAll(filepath.Join(folder, "tree"), 0700); err != nil {
		t.Fatal(err)
	}
	for path, body := range map[string]string{
		filepath.Join(folder, "booted"):    b.incarnation,
		filepath.Join(folder, "spec.json"): fmt.Sprintf(`{"name":%q,"stackId":"pv00000000-0001-7000-8000-000000000000"}`, name),
		// A healthy worker's fence: one with previews and none has lost them.
		filepath.Join(state, "previews", "fence.json"): `{"last":{}}`,
	} {
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// The preview's app is running until an rm; the daemon answers as the
	// worker's inspect and listing read it.
	var running atomic.Bool
	running.Store(true)
	binary := newDockerFake()
	binary.SetReply(func(args []string) ([]byte, error, bool) {
		switch args[0] {
		case "info":
			return nil, nil, false
		case "inspect":
			if running.Load() {
				return []byte("true id-old " + b.incarnation + "\n"), nil, true
			}
			return []byte("Error: No such object\n"), errors.New("exit status 1"), true
		case "ps":
			if running.Load() {
				return []byte(name + "\n"), nil, true
			}
		case "rm":
			running.Store(false)
		}
		return nil, nil, true
	})
	srv := &worker.Server{Installation: workertest.Installation, State: state, Previews: binary}
	entered, resume, removed := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var release sync.Once
	defer release.Do(func() { close(resume) })
	ca := workertest.NewCA(t)
	hs := workertest.StartWith(t, ca, state, nil, func(_ http.Handler) http.Handler {
		next := srv.Handler()
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodDelete {
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					panic(err)
				}
				conn.Close() // lose the HTTP reply, not the pending worker operation
				close(entered)
				<-resume
				next.ServeHTTP(httptest.NewRecorder(), r)
				close(removed)
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	p := worker.Previews{URL: hs.URL, Client: workertest.Client(t, ca, workertest.Installation)}
	s.worker = p
	done := make(chan error, 1)
	go func() { done <- s.ReconcileWorkerPreviews(context.Background()) }()
	select {
	case <-entered:
	case <-time.After(30 * time.Second): // a full suite can starve the reconciliation for seconds
		t.Fatal("DELETE never reached worker")
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("disconnected DELETE succeeded")
		}
	case <-time.After(30 * time.Second): // a full suite can starve the reconciliation for seconds
		t.Fatal("reconciliation did not release its hold")
	}
	out, err := s.RehearseChange(context.Background(), ch, false)
	if err != nil || out.URL == "" {
		t.Fatalf("reuse: %+v %v", out, err)
	}
	if _, ok := s.servedRecordOf(name); !ok {
		t.Fatal("successful reuse not recorded")
	}
	release.Do(func() { close(resume) })
	select {
	case <-removed:
	case <-time.After(5 * time.Second):
		t.Fatal("old DELETE did not finish")
	}
	st, err := p.State(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Running {
		t.Fatal("an earlier reconciliation DELETE removed the preview after its successful reuse and recording")
	}
}
