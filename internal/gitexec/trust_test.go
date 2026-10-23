package gitexec

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/trust"
)

// Git over HTTPS reads one file of roots in place of its own, so when the
// AppTown use has extra certificates (a company's TLS-inspecting proxy)
// every git AppTown starts is pointed at the system's roots and those
// together; without them git keeps its own.
//
// Not parallel: it installs the process's trust.
func TestGitTrustsTheAppTownUsesBundle(t *testing.T) {
	if slices.ContainsFunc(Environ(), func(kv string) bool { return strings.HasPrefix(kv, "GIT_SSL_CAINFO=") }) {
		t.Fatal("git was given a bundle with none configured")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "company proxy CA"}, NotBefore: time.Unix(0, 0), NotAfter: time.Unix(1<<33, 0), IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	b, err := trust.Parse(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	if err != nil {
		t.Fatal(err)
	}
	set, err := trust.New(t.TempDir(), map[trust.Use]trust.Bundle{trust.AppTown: b}, trust.Bundle{})
	if err != nil {
		t.Fatal(err)
	}
	before := http.DefaultTransport.(*http.Transport).TLSClientConfig
	t.Cleanup(func() { http.DefaultTransport.(*http.Transport).TLSClientConfig = before; trust.Install(nil) })
	trust.Install(set)
	want := "GIT_SSL_CAINFO=" + set.GitFile()
	if !slices.Contains(Environ(), want) {
		t.Fatalf("git's environment lacks %s", want)
	}
	cmd, done, err := remoteCommand(t.Context(), "", Remote{URL: "https://example.com/x.git", Pin: "example.com:443:127.0.0.1"}, []string{"ls-remote"})
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	if !slices.Contains(cmd.Env, want) {
		t.Fatalf("a remote fetch's environment lacks %s", want)
	}
}
