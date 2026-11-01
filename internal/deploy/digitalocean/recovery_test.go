package digitalocean

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy"
)

func TestDORecoveryRequiresExactIdentityAndCancelsPolling(t *testing.T) {
	t.Parallel()
	for _, noted := range []map[string]any{{}, {"appId": "app"}} {
		d := NewDriver(Dependencies{Client: func(context.Context) (*Client, Config, error) {
			t.Fatal("missing identity caused provider access")
			return nil, Config{}, nil
		}}, Options{})
		if _, err := d.Resume(context.Background(), deploy.Placement{}, 1, noted, func(string, ...any) {}); err == nil || deploy.IsTerminal(err) {
			t.Fatalf("missing identity: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"deployment":{"id":"deployment","phase":"BUILDING"}}`))
	}))
	defer server.Close()
	d := NewDriver(Dependencies{}, Options{PollInterval: time.Minute})
	start := time.Now()
	_, err := d.waitDeployment(ctx, &Client{Base: server.URL, HTTP: server.Client()}, "app", "deployment", true, func(string, ...any) { cancel() })
	if !errors.Is(err, context.Canceled) || time.Since(start) > time.Second {
		t.Fatalf("poll ignored cancellation: %v after %s", err, time.Since(start))
	}
}
