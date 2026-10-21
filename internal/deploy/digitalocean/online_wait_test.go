package digitalocean

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
)

type cancellingRecorder struct{ cancel context.CancelFunc }

func (cancellingRecorder) RecordIntent(context.Context, ids.ID, string, map[string]any) error {
	return nil
}

func (r cancellingRecorder) Recorded(context.Context, ids.ID, string) (map[string]any, error) {
	return nil, nil
}

func (r cancellingRecorder) RecordIdentity(context.Context, ids.ID, string, map[string]any, string, string) error {
	r.cancel()
	return nil
}

// Waiting for a new cluster to come online ends when the request does: a
// cancelled fulfilment does not sit out the poll interval.
func TestWaitingForAClusterEndsWithItsContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && r.URL.Path == "/v2/databases":
			_, _ = w.Write([]byte(`{"databases":[]}`))
		case r.Method == "POST" && r.URL.Path == "/v2/databases":
			_, _ = w.Write([]byte(`{"database":{"id":"db-new","name":"forge-demo-main","status":"creating"}}`))
		default:
			t.Errorf("asked after the fulfilment was cancelled: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", 500)
		}
	}))
	defer server.Close()
	driver := NewDriver(Dependencies{
		Client: func(context.Context) (*Client, Config, error) {
			return &Client{Base: server.URL, HTTP: server.Client()}, Config{Region: "syd"}, nil
		},
		Slug: "demo",
		// Created, not yet online, and the fulfilment is cancelled once
		// the cluster's identity is recorded: its wait begins.
		Needs: cancellingRecorder{cancel},
	}, Options{DBPoll: time.Hour})
	done := make(chan error, 1)
	go func() {
		_, _, err := driver.FulfilNeed(ctx, deploy.Placement{ID: idstest.N(7), Name: "main"}, "postgres", nil, func(string, ...any) {})
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("a cancelled wait for the cluster answered %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the wait for the cluster outlived its context")
	}
}
