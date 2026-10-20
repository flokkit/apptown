package gitexec

import (
	"context"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A remote fetch goes to the address the caller checked, whatever DNS says:
// the host here does not resolve at all, so a connection to the listener
// can only come from the pin. The TLS handshake then fails (the listener
// speaks nothing), which is fine: the destination is the guarantee.
func TestARemoteFetchConnectsOnlyToThePinnedAddress(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var accepted atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			_ = c.Close()
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	host := "pinned.invalid"
	r := Remote{URL: "https://" + host + ":" + strconv.Itoa(port) + "/repo.git", Pin: host + ":" + strconv.Itoa(port) + ":127.0.0.1"}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := RemoteHead(ctx, r); err == nil {
		t.Fatal("a listener that speaks no TLS answered ls-remote")
	}
	if accepted.Load() == 0 {
		t.Fatal("git did not connect to the pinned address")
	}
}

// The password reaches git only through the askpass helper's environment:
// not the URL, not an argument. The helper answers "forge" for the user.
func TestARemotePasswordIsInNoArgument(t *testing.T) {
	t.Parallel()
	const secret = "s3cret-value"
	r := Remote{URL: "https://example.com/r.git", Pin: "example.com:443:93.184.215.14", Password: secret}
	cmd, done, err := remoteCommand(context.Background(), "", r, []string{"ls-remote", r.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	for _, a := range cmd.Args {
		if strings.Contains(a, secret) {
			t.Fatalf("the password is in an argument: %q", a)
		}
	}
	for _, want := range []string{"protocol.allow=never", "protocol.https.allow=always", "http.followRedirects=false", "http.curloptResolve=example.com:443:93.184.215.14", "credential.helper="} {
		if !contains(cmd.Args, want) {
			t.Fatalf("git runs without %s: %q", want, cmd.Args)
		}
	}
	helper := envValue(cmd.Env, "GIT_ASKPASS")
	ask := func(prompt string) string {
		c := exec.Command(helper, prompt)
		c.Env = cmd.Env
		out, err := c.Output()
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out))
	}
	if got := ask("Username for 'https://example.com': "); got != "forge" {
		t.Fatalf("user = %q", got)
	}
	if got := ask("Password for 'https://forge@example.com': "); got != secret {
		t.Fatalf("password = %q", got)
	}
	if envValue(cmd.Env, "GIT_TERMINAL_PROMPT") != "0" {
		t.Fatal("git may prompt")
	}
}

// Without a pinned address or an https URL there is no remote command.
func TestARemoteNeedsHTTPSAndAPin(t *testing.T) {
	t.Parallel()
	for _, r := range []Remote{{URL: "http://example.com/r", Pin: "example.com:80:1.2.3.4"}, {URL: "https://example.com/r"}, {URL: "file:///tmp/r", Pin: "x:443:1.2.3.4"}} {
		if _, _, err := remoteCommand(context.Background(), "", r, []string{"ls-remote"}); err == nil {
			t.Fatalf("%+v was allowed", r)
		}
	}
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// A server that echoes the credential it was offered in its error does not
// get it into AppTown's error: git's own diagnostics are replaced by a
// fixed sentence.
func TestARemoteErrorNeverCarriesTheServersText(t *testing.T) {
	t.Parallel()
	const secret = "review-secret-123456"
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, pass, ok := r.BasicAuth()
		if !ok {
			w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(403)
		_, _ = io.WriteString(w, "invalid token: "+pass+"\n")
	}))
	defer srv.Close()
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	host := strings.TrimPrefix(srv.URL, "https://")
	r := Remote{URL: srv.URL + "/repo.git", Pin: host + ":127.0.0.1", Password: secret}
	// Only trust in the fixture's certificate is added; the remote flags
	// are the production ones.
	_, err := runRemote(context.Background(), "", r, "-c", "http.sslCAInfo="+ca, "ls-remote", "--end-of-options", r.URL)
	if err == nil {
		t.Fatal("the refusing server answered")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "invalid token") {
		t.Fatalf("the server's text reached the error: %v", err)
	}
	if !strings.Contains(err.Error(), "refused access") {
		t.Fatalf("the refusal is not said plainly: %v", err)
	}
}
