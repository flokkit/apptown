package localtls

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var start = time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)

func fixed(t *time.Time) func() time.Time { return func() time.Time { return *t } }

func roots(a *Authority) *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(a.Certificate())
	return p
}

// The CA can vouch only for localhost names and loopback addresses, and
// says so in a critical extension a verifier must honour.
func TestTheAuthorityIsLimitedToLocalhostNames(t *testing.T) {
	t.Parallel()
	now := start
	a, err := Load(t.TempDir(), fixed(&now))
	if err != nil {
		t.Fatal(err)
	}
	ca := a.Certificate()
	if !ca.PermittedDNSDomainsCritical || strings.Join(ca.PermittedDNSDomains, ",") != "localhost" {
		t.Fatalf("permitted names %v (critical %v), want only localhost, critical", ca.PermittedDNSDomains, ca.PermittedDNSDomainsCritical)
	}
	var ranges []string
	for _, r := range ca.PermittedIPRanges {
		ranges = append(ranges, r.String())
	}
	if strings.Join(ranges, ",") != "127.0.0.0/8,::1/128" {
		t.Fatalf("permitted addresses %v, want the loopback ranges", ranges)
	}

	// A certificate the CA's key signs for a real website fails
	// verification on the constraints, as it would in a browser.
	evil := &x509.Certificate{SerialNumber: big.NewInt(7), Subject: pkix.Name{CommonName: "example.com"}, DNSNames: []string{"example.com"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, err := x509.CreateCertificate(rand.Reader, evil, ca, key.Public(), a.key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots(a), DNSName: "example.com", CurrentTime: now}); err == nil {
		t.Fatal("a certificate for example.com signed by the local CA verified")
	}
}

// A preview's name, localhost and an IP address each get a certificate
// that verifies against the CA; a name outside localhost gets none.
func TestLeavesVerifyForLocalhostNamesAndNothingElse(t *testing.T) {
	t.Parallel()
	now := start
	a, err := Load(t.TempDir(), fixed(&now))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ sni, verifyAs string }{
		{"change-1.preview.localhost", "change-1.preview.localhost"},
		{"web.shop.main.localhost", "web.shop.main.localhost"},
		{"localhost", "localhost"},
		{"", "127.0.0.1"},
		{"", "::1"},
	} {
		cert, err := a.Leaf(c.sni)
		if err != nil {
			t.Fatalf("%q: %v", c.sni, err)
		}
		inter := x509.NewCertPool()
		if _, err := cert.Leaf.Verify(x509.VerifyOptions{Roots: roots(a), Intermediates: inter, DNSName: c.verifyAs, CurrentTime: now}); err != nil {
			t.Errorf("%q as %s: %v", c.sni, c.verifyAs, err)
		}
	}
	if _, err := a.Leaf("example.com"); err == nil {
		t.Fatal("a certificate was issued for example.com")
	}
}

// Loading the folder again gives the same CA; its key file is private.
func TestLoadKeepsTheAuthority(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	first, err := Load(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	again, err := Load(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !again.Certificate().Equal(first.Certificate()) {
		t.Fatal("loading the folder again made a different CA")
	}
	info, err := os.Stat(filepath.Join(dir, KeyFile))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("key file mode %v (%v), want 0600", info.Mode().Perm(), err)
	}
}

// A lone certificate or a lone key is reported, never replaced: the
// person may already trust the certificate.
func TestAHalfWrittenAuthorityIsAnError(t *testing.T) {
	t.Parallel()
	for _, keep := range []string{CertFile, KeyFile} {
		dir := t.TempDir()
		if _, err := Load(dir, nil); err != nil {
			t.Fatal(err)
		}
		other := map[string]string{CertFile: KeyFile, KeyFile: CertFile}[keep]
		if err := os.Remove(filepath.Join(dir, other)); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(dir, nil); err == nil || !strings.Contains(err.Error(), dir) {
			t.Errorf("with only %s: %v, want an error naming the folder", keep, err)
		}
		if _, err := os.Stat(filepath.Join(dir, other)); err == nil {
			t.Errorf("with only %s: the missing file was made again", keep)
		}
	}
}

// A leaf is kept until a day before it ends, then issued again.
func TestALeafIsReissuedBeforeItEnds(t *testing.T) {
	t.Parallel()
	now := start
	a, err := Load(t.TempDir(), fixed(&now))
	if err != nil {
		t.Fatal(err)
	}
	first, _ := a.Leaf("localhost")
	now = first.Leaf.NotAfter.Add(-48 * time.Hour)
	if kept, _ := a.Leaf("localhost"); kept != first {
		t.Fatal("a leaf with two days left was reissued")
	}
	now = first.Leaf.NotAfter.Add(-time.Hour)
	renewed, _ := a.Leaf("localhost")
	if renewed == first || !renewed.Leaf.NotAfter.After(first.Leaf.NotAfter) {
		t.Fatal("a leaf with an hour left was not reissued")
	}
}

// One port: a TLS client is served over TLS (and net/http sees a
// *tls.Conn), and a plain HTTP request is sent to the same address over
// https.
func TestOnePortServesTLSAndRedirectsPlainHTTP(t *testing.T) {
	t.Parallel()
	a, err := Load(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	l := Listener(inner, a.TLSConfig())
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil {
			http.Error(w, "not TLS", 500)
			return
		}
		_, _ = io.WriteString(w, "secure "+r.Host)
	}), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
	port := inner.Addr().(*net.TCPAddr).Port

	// A silent connection does not hold up the next one.
	silent, err := net.Dial("tcp", inner.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer silent.Close()

	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: roots(a), ServerName: "change-1.preview.localhost"},
		DialContext:     (&net.Dialer{}).DialContext,
	}, Timeout: 5 * time.Second}
	resp, err := client.Get("https://127.0.0.1:" + itoa(port) + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.HasPrefix(string(body), "secure ") {
		t.Fatalf("TLS request: %d %q", resp.StatusCode, body)
	}

	plain, err := net.Dial("tcp", inner.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer plain.Close()
	_, _ = io.WriteString(plain, "GET /x?y=1 HTTP/1.1\r\nHost: change-1.preview.localhost:"+itoa(port)+"\r\n\r\n")
	got, err := http.ReadResponse(bufio.NewReader(plain), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := "https://change-1.preview.localhost:" + itoa(port) + "/x?y=1"
	if got.StatusCode != http.StatusPermanentRedirect || got.Header.Get("Location") != want {
		t.Fatalf("plain request: %d to %q, want 308 to %q", got.StatusCode, got.Header.Get("Location"), want)
	}
}

func itoa(n int) string { return big.NewInt(int64(n)).String() }
