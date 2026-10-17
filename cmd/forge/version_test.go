package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/version"
)

// The CLI states its protocol on every request, `forge version` names this
// CLI and the forge it talks to, and a forge that refuses the CLI's
// protocol is an error carrying the forge's own sentence.
//
// Kills: a CLI that states no protocol, so a forge cannot refuse it by name.
func TestTheCLIStatesItsProtocolAndNamesBothVersions(t *testing.T) {
	refuse := false
	var protocols []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		protocols = append(protocols, r.Header.Get(version.CLIProtocolHeader))
		w.Header().Set(version.Header, "dev-forgeforge01")
		if refuse {
			w.WriteHeader(http.StatusUpgradeRequired)
			_, _ = io.WriteString(w, `{"error":"this forge (dev-forgeforge01) accepts CLI protocols 2 to 2; this CLI speaks \"1\": use the forge CLI from the same release as the forge"}`)
			return
		}
		_, _ = io.WriteString(w, `{}`)
	}))
	defer srv.Close()
	prev := api
	api = &client{base: srv.URL, actor: "tester", http: srv.Client(), wait: func(time.Duration) {}, getenv: func(string) string { return "" }}
	defer func() { api = prev }()

	out := capture(t, func() {
		if err := cmdVersion(nil); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "forge CLI "+version.String()+" (CLI protocol 1)") || !strings.Contains(out, "dev-forgeforge01") {
		t.Fatalf("version names both sides:\n%s", out)
	}
	if len(protocols) != 1 || protocols[0] != "1" {
		t.Fatalf("the CLI stated %q", protocols)
	}
	refuse = true
	var err error
	_ = capture(t, func() { err = cmdVersion(nil) })
	if err == nil || !strings.Contains(err.Error(), "accepts CLI protocols 2 to 2") {
		t.Fatalf("a refusal carries the forge's sentence: %v", err)
	}
	if err := api.get("/projects", nil); err == nil || !strings.Contains(err.Error(), "same release") || protocols[len(protocols)-1] != "1" {
		t.Fatalf("any command is refused with the sentence: %v %q", err, protocols)
	}
}
