// Package localtls is a certificate authority for one computer's own
// addresses: it makes and keeps a CA whose name constraints permit only
// localhost names and loopback addresses, and issues a certificate for
// whichever of those a client asks for. The desktop trial serves itself and
// every app it runs over HTTPS with it, so apps see on a laptop what they
// see on a server; the person trusts the CA once. Even if its key leaked,
// the CA could vouch for no real website.
//
// It touches no AppTown state. The CA's files live in the folder it is given;
// issued certificates are kept in memory only.
package localtls

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/flokkit/apptown/internal/clock"
)

// File names in the CA's folder. CertFile is the public certificate a
// person trusts; KeyFile never leaves the folder.
const (
	CertFile = "ca.pem"
	KeyFile  = "ca-key.pem"
)

const (
	caLife   = 10 * 365 * 24 * time.Hour
	leafLife = 397 * 24 * time.Hour // the longest browsers accept
	renewAt  = 24 * time.Hour       // a leaf is reissued this long before it ends
)

// The only names and addresses the CA may vouch for. A DNS constraint of
// "localhost" permits localhost and every name under it.
var (
	permittedDNS = []string{"localhost"}
	permittedIPs = []*net.IPNet{
		{IP: net.IPv4(127, 0, 0, 0).To4(), Mask: net.CIDRMask(8, 32)},
		{IP: net.IPv6loopback, Mask: net.CIDRMask(128, 128)},
	}
)

// Authority is a loaded CA.
type Authority struct {
	cert *x509.Certificate
	der  []byte
	key  crypto.Signer
	now  func() time.Time

	mu     sync.Mutex
	leaves map[string]*tls.Certificate
}

// Load reads the CA in dir, or makes one when dir holds neither of its
// files. One file without the other, or a key that does not match the
// certificate, is an error naming the folder: the person may already trust
// that certificate, so it is never replaced silently.
func Load(dir string, now func() time.Time) (*Authority, error) {
	now = clock.Or(now)
	certPath, keyPath := filepath.Join(dir, CertFile), filepath.Join(dir, KeyFile)
	certPEM, certErr := os.ReadFile(certPath)
	keyPEM, keyErr := os.ReadFile(keyPath)
	switch {
	case errors.Is(certErr, os.ErrNotExist) && errors.Is(keyErr, os.ErrNotExist):
		return create(dir, now)
	case certErr != nil:
		return nil, fmt.Errorf("the local certificate authority in %s: %w (remove the folder to make a new one, then trust it again)", dir, certErr)
	case keyErr != nil:
		return nil, fmt.Errorf("the local certificate authority in %s: %w (remove the folder to make a new one, then trust it again)", dir, keyErr)
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("the local certificate authority in %s does not load: %w (remove the folder to make a new one, then trust it again)", dir, err)
	}
	cert, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("the local certificate authority in %s: %w", dir, err)
	}
	signer, ok := pair.PrivateKey.(crypto.Signer)
	if !ok || !cert.IsCA {
		return nil, fmt.Errorf("the local certificate authority in %s is not a CA key pair", dir)
	}
	return &Authority{cert: cert, der: pair.Certificate[0], key: signer, now: now, leaves: map[string]*tls.Certificate{}}, nil
}

func create(dir string, now func() time.Time) (*Authority, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := serialNumber()
	if err != nil {
		return nil, err
	}
	t := now()
	tmpl := &x509.Certificate{
		SerialNumber:                serial,
		Subject:                     pkix.Name{CommonName: "AppTown local CA " + serial.Text(16)[:8], Organization: []string{"AppTown on this computer"}},
		NotBefore:                   t.Add(-time.Hour),
		NotAfter:                    t.Add(caLife),
		IsCA:                        true,
		BasicConstraintsValid:       true,
		MaxPathLenZero:              true,
		KeyUsage:                    x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		PermittedDNSDomainsCritical: true,
		PermittedDNSDomains:         permittedDNS,
		PermittedIPRanges:           permittedIPs,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		return nil, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	// The key first: a crash between the writes leaves a lone key, which
	// Load reports instead of making a second CA behind the person's back.
	if err := writeFile(filepath.Join(dir, KeyFile), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		return nil, err
	}
	if err := writeFile(filepath.Join(dir, CertFile), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &Authority{cert: cert, der: der, key: key, now: now, leaves: map[string]*tls.Certificate{}}, nil
}

// writeFile writes whole or not at all.
func writeFile(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func serialNumber() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
}

// Certificate is the CA certificate, the one a person trusts.
func (a *Authority) Certificate() *x509.Certificate { return a.cert }

// TLSConfig serves each connection a certificate for the name it asked
// for, issued on first use and kept in memory. A connection that names no
// server (an IP address) gets one for localhost and the loopback addresses.
// Any name outside localhost is refused.
func (a *Authority) TLSConfig() *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"h2", "http/1.1"},
		GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			return a.Leaf(hello.ServerName)
		},
	}
}

// Leaf is the certificate for name ("" for an IP address), issued or
// reissued when it has none that lasts more than a day.
func (a *Authority) Leaf(name string) (*tls.Certificate, error) {
	name = strings.ToLower(strings.TrimSuffix(name, "."))
	if name != "" && name != "localhost" && !strings.HasSuffix(name, ".localhost") {
		return nil, fmt.Errorf("this AppTown serves only localhost names over HTTPS, not %q", name)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if leaf, ok := a.leaves[name]; ok && a.now().Add(renewAt).Before(leaf.Leaf.NotAfter) {
		return leaf, nil
	}
	leaf, err := a.issue(name)
	if err != nil {
		return nil, err
	}
	a.leaves[name] = leaf
	return leaf, nil
}

func (a *Authority) issue(name string) (*tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := serialNumber()
	if err != nil {
		return nil, err
	}
	t := a.now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: name},
		NotBefore:    t.Add(-time.Hour),
		NotAfter:     t.Add(leafLife),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{name},
	}
	if name == "" {
		tmpl.Subject.CommonName = "localhost"
		tmpl.DNSNames = []string{"localhost"}
		tmpl.IPAddresses = []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, a.cert, key.Public(), a.key)
	if err != nil {
		return nil, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &tls.Certificate{Certificate: [][]byte{der, a.der}, PrivateKey: key, Leaf: leaf}, nil
}
