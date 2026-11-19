package execution_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/flokkit/apptown/internal/execution"
)

// A result is read whole or not at all: a reply without its length, of a
// length other than the captured size, or cut short is the worker being
// unavailable, never a shorter result; and Results, which reads every
// present result, fails rather than answer one without its bytes (a
// capture the worker no longer keeps a copy of).
//
// Kills: the stated length not checked against the captured size; a body
// cut short read as the end of the result; Results answering a present
// result whose bytes could not be read.
func TestAResultIsReadWholeOrFails(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/tasks/chunked/results/dump":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("part"))
			w.(http.Flusher).Flush()
		case "/v1/tasks/other/results/dump":
			w.Header().Set("Content-Length", "4")
			_, _ = w.Write([]byte("part"))
		case "/v1/tasks/short/results/dump":
			// Ten bytes promised, four sent, and the connection closed.
			conn, buf, _ := w.(http.Hijacker).Hijack()
			_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 10\r\n\r\npart")
			_ = buf.Flush()
			_ = conn.Close()
		case "/v1/tasks/whole/results/dump":
			w.Header().Set("Content-Length", "10")
			_, _ = w.Write([]byte("0123456789"))
		case "/v1/tasks/uncopied/results":
			_, _ = w.Write([]byte(`{"dump":{"state":"present","size":10}}`))
		case "/v1/tasks/uncopied/results/dump":
			http.Error(w, "the worker keeps no copy of the result dump", http.StatusNotFound)
		default:
			http.Error(w, "unexpected "+r.URL.String(), http.StatusTeapot)
		}
	}))
	t.Cleanup(srv.Close)
	driver := execution.WorkerTasks{URL: srv.URL, Client: srv.Client()}
	read := func(key string) (string, error) {
		var out bytes.Buffer
		err := driver.ReadResult(context.Background(), execution.TaskRef{Key: key}, "dump", 10, &out)
		return out.String(), err
	}
	for _, key := range []string{"chunked", "other", "short"} {
		if got, err := read(key); !errors.Is(err, execution.ErrWorkerUnavailable) {
			t.Errorf("%s: read as %q, %v", key, got, err)
		}
	}
	if got, err := read("whole"); err != nil || got != "0123456789" {
		t.Errorf("a whole reply: %q %v", got, err)
	}
	if files, err := driver.Results(context.Background(), execution.TaskRef{Key: "uncopied"}); err == nil {
		t.Errorf("a present result without its copy: %+v", files)
	}
}
