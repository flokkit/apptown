package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestImageNewestPrintsThePinAndStaleReading(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/image-lines" || r.URL.Query().Get("ref") != "caddy:2.10.2-alpine@sha256:a" || r.URL.Query().Get("scope") != "minor" {
			t.Errorf("request %s", r.URL)
		}
		_, _ = io.WriteString(w, `{"tag":"caddy:2.11.9-alpine","digest":"sha256:abc","registry":"mirror.test","readAt":"2026-11-17T00:00:00Z","stale":true,"error":"registry offline"}`)
	}))
	defer srv.Close()
	prev := api
	api = &client{base: srv.URL, http: srv.Client(), getenv: func(string) string { return "" }, wait: func(time.Duration) {}}
	defer func() { api = prev }()
	out := capture(t, func() {
		if err := cmdImage([]string{"newest", "caddy:2.10.2-alpine@sha256:a", "--scope", "minor"}); err != nil {
			t.Fatal(err)
		}
	})
	for _, want := range []string{"caddy:2.11.9-alpine@sha256:abc\n", "registry: mirror.test", "registry unreachable; used the reading from 2026-11-17T00:00:00Z: registry offline"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q: %s", want, out)
		}
	}
}
