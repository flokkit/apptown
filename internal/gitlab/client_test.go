package gitlab

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSCMProviderRequestUsesOperationCancellation(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	finished := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done(); close(finished) }))
	defer server.Close()
	client, err := New(server.URL, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := client.GetMR(ctx, "project", 1); done <- err }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("provider request did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("provider cancellation: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("provider request ignored cancellation")
	}
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("server did not observe cancellation")
	}
}
