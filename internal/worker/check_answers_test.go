package worker_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/worker"
)

func TestAnIncompleteAnswerIsNoAnswer(t *testing.T) {
	for _, reply := range []string{`{"termination":"exited"}`, `{"termination":"exited","exitCode":null}`, `{"termination":"exited","exitCode":-1}`, `{"termination":"exited","exitCode":0} trailing junk`} {
		t.Run(reply, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.Copy(io.Discard, r.Body); io.WriteString(w, reply) }))
			defer s.Close()
			out := (worker.Checks{URL: s.URL, Client: s.Client()}).Run(context.Background(), execution.Request{Workspace: t.TempDir(), Image: "x", Argv: []string{"true"}, Timeout: time.Second, Network: "none"})
			if out.Termination == execution.Exited {
				t.Fatalf("malformed answer accepted: %+v", out)
			}
		})
	}
}

func TestAnImageCannotBeReadAsAnOption(t *testing.T) {
	binary, calls := fakeCheckDocker(t)
	checks, _ := checkWorker(t, binary)
	out := checks.Run(context.Background(), execution.Request{Workspace: t.TempDir(), Image: "--privileged", Argv: []string{"--volume=/should-not-be-mounted:/outside", "alpine:3", "true"}, Timeout: time.Second, Network: "none"})
	b, _ := os.ReadFile(calls)
	if strings.Contains(string(b), "--privileged") || out.Exited0() {
		t.Fatalf("image supplied a Docker option: outcome=%+v; argv=%s", out, b)
	}
}

type reviewS2Transport func(*http.Request) (*http.Response, error)

func (f reviewS2Transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestACheckRequestHasADeadline(t *testing.T) {
	client := &http.Client{Transport: reviewS2Transport(func(r *http.Request) (*http.Response, error) {
		defer r.Body.Close()
		io.Copy(io.Discard, r.Body)
		if _, ok := r.Context().Deadline(); !ok {
			t.Error("check request has no deadline: a stalled worker can hold it indefinitely")
		}
		return nil, context.DeadlineExceeded
	})}
	(worker.Checks{URL: "http://worker.invalid", Client: client}).Run(context.Background(), execution.Request{Workspace: t.TempDir(), Image: "x", Argv: []string{"true"}, Timeout: time.Second, Network: "none"})
}
