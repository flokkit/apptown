package deploy

import (
	"strings"
	"testing"
)

func TestHostnameKeysNormalizeOnlyDNSNames(t *testing.T) {
	for raw, want := range map[string]string{" App.Example. ": "app.example", "internal": "internal", "XN--BCHER-KVA.example": "xn--bcher-kva.example", "app-123.internal": "app-123.internal"} {
		got, err := NormalizeHostname(raw)
		if err != nil || got != want {
			t.Errorf("%q => %q %v", raw, got, err)
		}
	}
	for _, raw := range []string{"", ".", "..", "a..b", "https://app.example", "user@app.example", "app.example/path", "app.example:443", "*.example", "app example", "app\nexample", "bücher.example", "127.0.0.1", "127.1", "123", "0x7f000001", "0x7f.1", "[::1]", "::1", "-app.example", "app-.example", strings.Repeat("a", 64) + ".example", strings.Repeat("a.", 127) + "a"} {
		if got, err := NormalizeHostname(raw); err == nil {
			t.Errorf("invalid hostname %q accepted as %q", raw, got)
		}
	}
}

func TestIngressSnapshotDoesNotShareMutableHostInput(t *testing.T) {
	source := Ingress{Hosts: []string{"generated.example", "customer.example"}, URL: "https://customer.example", CertificateDomain: "example"}
	snapshot := source.Clone()
	if !snapshot.Same(source) {
		t.Fatal("clone changed admitted input")
	}
	source.Hosts[0] = "other.example"
	if snapshot.Hosts[0] != "generated.example" || snapshot.Same(source) {
		t.Fatal("caller changed admitted host input")
	}
	reordered := snapshot.Clone()
	reordered.Hosts[0], reordered.Hosts[1] = reordered.Hosts[1], reordered.Hosts[0]
	if snapshot.Same(reordered) {
		t.Fatal("host order lost from immutable input")
	}
}
