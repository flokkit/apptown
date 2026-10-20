package digitalocean

import (
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// The API client's connection is its own: closing http.DefaultTransport's
// idle connections, which every httptest.Server.Close does, does not close
// it under the client, so calls between such closes reuse one connection.
//
// Kills: NewClient's HTTP client back on http.DefaultTransport.
func TestAnAPIConnectionIsNotClosedByOtherServers(t *testing.T) {
	var opened atomic.Int64
	api := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	api.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			opened.Add(1)
		}
	}
	api.Start()
	t.Cleanup(api.Close)
	c := NewClient("token", api.URL)
	for range 3 {
		if err := c.Call(t.Context(), "GET", "/v2/account", nil, nil); err != nil {
			t.Fatal(err)
		}
		http.DefaultTransport.(*http.Transport).CloseIdleConnections()
	}
	if n := opened.Load(); n != 1 {
		t.Fatalf("three calls opened %d connections, want 1", n)
	}
}
