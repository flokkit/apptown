package digitalocean

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/flokkit/apptown/internal/deploy"
)

func TestRestartUsesDocumentedOperationWithoutUpdatingOrRebuilding(t *testing.T) {
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch r.Method + " " + r.URL.Path {
		case "POST /v2/apps/app/restart":
			_, _ = w.Write([]byte(`{"deployment":{"id":"restart","phase":"DEPLOYING"}}`))
		case "GET /v2/apps/app/deployments/restart":
			_, _ = w.Write([]byte(`{"deployment":{"id":"restart","phase":"ACTIVE"}}`))
		default:
			t.Errorf("unexpected effect: %s %s", r.Method, r.URL)
			http.Error(w, "unexpected", 500)
		}
	}))
	defer server.Close()
	d := NewDriver(Dependencies{Client: func(context.Context) (*Client, Config, error) { return NewClient("token", server.URL), Config{}, nil }}, Options{})
	p := deploy.Placement{Handle: map[string]any{"appId": "app"}}
	for _, act := range []string{"start", "stop"} {
		if err := d.Runtime(t.Context(), p, act); err == nil {
			t.Fatalf("supported %s", act)
		}
	}
	if len(calls) != 0 {
		t.Fatal(calls)
	}
	if err := d.Runtime(t.Context(), p, "restart"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, []string{"POST /v2/apps/app/restart", "GET /v2/apps/app/deployments/restart"}) {
		t.Fatal(calls)
	}
}
