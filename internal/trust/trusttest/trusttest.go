// Package trusttest makes certificate authorities for tests of what trusts
// them (internal/trust): one signs an HTTPS server that a test reaches from
// this process or from a container, and its certificate is a bundle a test
// installs as the process's trust.
package trusttest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/trust"
)

// Authority is a test's certificate authority.
type Authority struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

// New makes an authority named name.
func New(t testing.TB, name string) *Authority {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		SubjectKeyId: []byte(name),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &Authority{cert: cert, key: key, pem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

// PEM is the authority's certificate.
func (a *Authority) PEM() []byte { return a.pem }

// Bundle is the authority's certificate as a bundle.
func (a *Authority) Bundle(t testing.TB) trust.Bundle {
	t.Helper()
	b, err := trust.Parse(a.pem)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Server is a certificate and key the authority signs for localhost,
// host.docker.internal (how a container reaches this machine) and
// 127.0.0.1, as PEM.
func (a *Authority) Server(t testing.TB) (certPEM, keyPEM []byte, cert tls.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: "localhost"},
		DNSNames: []string{"localhost", "host.docker.internal"}, IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, a.cert, &key.PublicKey, a.key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
		tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// Serve serves h over HTTPS on every address of this machine with the
// authority's Server certificate, and answers its port.
func (a *Authority) Serve(t testing.TB, h http.Handler) int {
	t.Helper()
	_, _, cert := a.Server(t)
	l, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(tls.NewListener(l, &tls.Config{Certificates: []tls.Certificate{cert}}))
	t.Cleanup(func() { srv.Close() })
	_, port, _ := net.SplitHostPort(l.Addr().String())
	n, _ := strconv.Atoi(port)
	return n
}

// Install makes deployments (and the local authority local, which may be
// empty) the process's trust until the test ends. Tests that call it are
// not parallel: the trust is the process's.
func Install(t testing.TB, deployments, local trust.Bundle) *trust.Set {
	t.Helper()
	set, err := trust.New(t.TempDir(), map[trust.Use]trust.Bundle{trust.Deployments: deployments}, local)
	if err != nil {
		t.Fatal(err)
	}
	trust.Install(set)
	t.Cleanup(func() { trust.Install(nil) })
	return set
}

// OK answers 200 "ok".
var OK = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok\n")) })
