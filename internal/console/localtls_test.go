package console

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// With AppTown's local authority staged, the chat's launcher trusts it
// for every client (a bundle of the system roots plus it, the variables
// curl, Python, Go and Node read) and goes on when a tool it uses is
// missing; without it, the launcher changes nothing. It also adds the
// authority to both NSS databases headless Chromium reads and leads its
// loopback's public port to AppTown.
func TestTheLocalTLSPreludeTrustsTheAuthorityAndForwardsThePort(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ca := filepath.Join(dir, "local-ca.pem")
	tls := LocalTLS{Port: 7777, Target: "host.docker.internal:7777"}
	// No socat or certutil on this PATH: the prelude must go on without them.
	run := func() string {
		cmd := command([]string{"sh", "-c", localTLSPrelude(ca, dir, tls) + "\necho SSL=$SSL_CERT_FILE CURL=$CURL_CA_BUNDLE REQ=$REQUESTS_CA_BUNDLE NODE=$NODE_EXTRA_CA_CERTS"},
			[]string{"PATH=/usr/bin:/bin", "HOME=" + filepath.Join(dir, "home")})
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		return string(out)
	}

	if out := run(); !strings.Contains(out, "SSL= CURL= REQ= NODE=") {
		t.Fatalf("without the authority the prelude changed the environment: %s", out)
	}
	if err := os.WriteFile(ca, []byte("THE-LOCAL-CA\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := run()
	bundle := filepath.Join(dir, "forge-ca-bundle.pem")
	if want := "SSL=" + bundle + " CURL=" + bundle + " REQ=" + bundle + " NODE=" + ca; !strings.Contains(out, want) {
		t.Fatalf("environment: %s, want %s", out, want)
	}
	if b, _ := os.ReadFile(bundle); !strings.Contains(string(b), "THE-LOCAL-CA") {
		t.Fatalf("the bundle lacks the authority: %q", b)
	}
	text := localTLSPrelude(ca, dir, tls)
	for _, want := range []string{
		"socat TCP4-LISTEN:7777,bind=127.0.0.1,fork,reuseaddr TCP:host.docker.internal:7777",
		"socat TCP6-LISTEN:7777,bind=[::1],fork,reuseaddr TCP:host.docker.internal:7777",
		`for db in "$HOME/.pki/nssdb" "$HOME/.local/share/pki/nssdb"`,
		`certutil -d "sql:$db" -A -t C,, -n "AppTown local CA" -i ` + ca,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("prelude missing %q", want)
		}
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
