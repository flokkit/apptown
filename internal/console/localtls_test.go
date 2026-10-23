package console

import (
	"strings"
	"testing"
)

// The local-TLS prelude leads the loopback's public port to AppTown and goes
// on when socat is missing. It trusts nothing itself: the authority is in the
// extra certificates every container is given (internal/trust).
func TestTheLocalTLSPreludeForwardsThePort(t *testing.T) {
	t.Parallel()
	tls := LocalTLS{Port: 7777, Target: "host.docker.internal:7777"}
	text := LocalTLSPrelude(tls)
	// No socat on this PATH: the prelude must go on without it.
	out, err := command([]string{"sh", "-c", text + "\necho went-on"}, []string{"PATH=/usr/bin:/bin"}).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "went-on") {
		t.Fatalf("%v: %s", err, out)
	}
	for _, want := range []string{
		"socat TCP4-LISTEN:7777,bind=127.0.0.1,fork,reuseaddr TCP:host.docker.internal:7777",
		"socat TCP6-LISTEN:7777,bind=[::1],fork,reuseaddr TCP:host.docker.internal:7777",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("prelude missing %q", want)
		}
	}
	if strings.Contains(text, "certutil") || strings.Contains(text, "SSL_CERT_FILE") {
		t.Errorf("the prelude still installs a certificate: %s", text)
	}
}

// A change chat names its own preview in its hosts file, for tools that ask
// the system resolver; with no preview domain it writes nothing there.
func TestTheLocalTLSPreludeNamesTheChatsPreview(t *testing.T) {
	t.Parallel()
	with := LocalTLSPrelude(LocalTLS{Port: 7777, Target: "host.docker.internal:7777", PreviewDomain: "preview.localhost"})
	if !strings.Contains(with, `printf '127.0.0.1 %s\n::1 %s\n' "change-$APPTOWN_CHANGE.preview.localhost"`) || !strings.Contains(with, ">>/etc/hosts") {
		t.Fatalf("no hosts entry for the chat's preview: %s", with)
	}
	if without := LocalTLSPrelude(LocalTLS{Port: 7777, Target: "host.docker.internal:7777"}); strings.Contains(without, "/etc/hosts") {
		t.Fatalf("a hosts entry with no preview domain: %s", without)
	}
}
