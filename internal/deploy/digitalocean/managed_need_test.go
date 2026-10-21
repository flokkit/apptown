package digitalocean

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
)

type failingNeedRecorder struct {
	intentErr   error
	identityErr error
	identityID  string
}

func (r *failingNeedRecorder) Recorded(context.Context, ids.ID, string) (map[string]any, error) {
	return nil, nil
}

func (r *failingNeedRecorder) RecordIntent(context.Context, ids.ID, string, map[string]any) error {
	return r.intentErr
}

func (r *failingNeedRecorder) RecordIdentity(ctx context.Context, _ ids.ID, _ string, _ map[string]any, id, _ string) error {
	r.identityID = id
	return r.identityErr
}

// The provider can be exercised with an HTTP server and a two-operation store;
// no App, database, secrets file, or workers are needed.
func TestDigitalOceanStopsWhenManagedNeedRecordingFails(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"intent", "identity", "identity held"} {
		t.Run(phase, func(t *testing.T) {
			failure := errors.New("record unavailable")
			recorder := &failingNeedRecorder{}
			switch phase {
			case "intent":
				recorder.intentErr = failure
			case "identity held":
				// The record refuses another identity: the release ends,
				// and asking again would change nothing.
				failure = fault.New(fault.Conflict, "a managed need's cluster, database and user do not change in place")
				recorder.intentErr = failure
			default:
				recorder.identityErr = failure
			}
			var created atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == "GET" && r.URL.Path == "/v2/databases":
					_, _ = w.Write([]byte(`{"databases":[]}`))
				case r.Method == "POST" && r.URL.Path == "/v2/databases":
					created.Store(true)
					_, _ = w.Write([]byte(`{"database":{"id":"db-new","name":"forge-demo-main","status":"creating"}}`))
				default:
					t.Errorf("provider continued after failed recording: %s %s", r.Method, r.URL.Path)
					http.Error(w, "unexpected", 500)
				}
			}))
			defer server.Close()
			driver := NewDriver(Dependencies{
				Client: func(context.Context) (*Client, Config, error) {
					return &Client{Base: server.URL, HTTP: server.Client()}, Config{Region: "syd"}, nil
				},
				Slug:  "demo",
				Needs: recorder,
			}, Options{})
			_, _, err := driver.FulfilNeed(context.Background(), deploy.Placement{ID: idstest.N(7), Name: "main"}, "postgres", nil, func(string, ...any) {})
			if err == nil || !strings.Contains(err.Error(), failure.Error()) || deploy.IsTerminal(err) != (phase == "identity held") {
				t.Fatalf("recording error was lost or misjudged: %v (terminal %v)", err, deploy.IsTerminal(err))
			}
			if created.Load() != (phase == "identity") || (created.Load() && recorder.identityID != "db-new") {
				t.Fatalf("creation crossed the intent boundary: created=%v identity=%s", created.Load(), recorder.identityID)
			}
		})
	}
}
