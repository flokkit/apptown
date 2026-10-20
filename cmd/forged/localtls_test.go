package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// -local-tls needs an https public origin, so the cookie, app origins and
// forwarded scheme agree with the listener; with one it makes the authority
// under the data directory and tells chats where the public port really is.
func TestLocalTLSNeedsAnHTTPSOriginAndMakesTheAuthority(t *testing.T) {
	t.Parallel()
	if wrap, chats, err := localTLS(false, "", ":7777", t.TempDir(), nil); wrap != nil || chats != nil || err != nil {
		t.Fatalf("off: %v %v %v", wrap != nil, chats, err)
	}
	for _, host := range []string{"", "localhost", "http://localhost:7777"} {
		if _, _, err := localTLS(true, host, ":7777", t.TempDir(), nil); err == nil || !strings.Contains(err.Error(), "https://") {
			t.Errorf("public host %q: %v, want a refusal", host, err)
		}
	}
	data := t.TempDir()
	wrap, chats, err := localTLS(true, "https://localhost:7443", ":7777", data, []string{"preview=https://preview.localhost:7443,main=https://main.localhost:7443"})
	if err != nil || wrap == nil || chats == nil {
		t.Fatalf("on: %v", err)
	}
	if chats.PreviewDomain != "preview.localhost" {
		t.Fatalf("preview domain %q", chats.PreviewDomain)
	}
	if chats.Port != 7443 || chats.Target != "host.docker.internal:7777" {
		t.Fatalf("chats reach port %d at %s, want 7443 at host.docker.internal:7777", chats.Port, chats.Target)
	}
	ca, err := os.ReadFile(filepath.Join(data, localTLSDir, "ca.pem"))
	if err != nil || string(ca) != string(chats.CACert) || strings.Contains(string(chats.CACert), "PRIVATE KEY") {
		t.Fatalf("chats get %q, want the authority's certificate only (%v)", chats.CACert, err)
	}
}
