package s3bucket

import (
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// A store's connection is the client's own: closing http.DefaultTransport's
// idle connections, which every httptest.Server.Close does, does not close
// it under the client, so requests between such closes reuse one connection.
// A client on the shared pool reconnects each time, and a request caught by
// a close fails with "connection broken" though the store never answered.
//
// Kills: the request client back on http.DefaultTransport.
func TestAStoreConnectionIsNotClosedByOtherServers(t *testing.T) {
	var opened atomic.Int64
	store := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	store.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			opened.Add(1)
		}
	}
	store.Start()
	t.Cleanup(store.Close)
	c := &Client{Endpoint: store.URL, Region: "syd1", AccessKey: "AKID", SecretKey: "x"}
	for range 3 {
		if err := c.CreateBucket(t.Context(), "b"); err != nil {
			t.Fatal(err)
		}
		http.DefaultTransport.(*http.Transport).CloseIdleConnections()
	}
	if n := opened.Load(); n != 1 {
		t.Fatalf("three requests opened %d connections, want 1", n)
	}
}
