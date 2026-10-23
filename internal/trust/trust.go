// Package trust is the certificates a process trusts beyond the system's
// own, by use. A bundle is a PEM file of extra certificate authorities
// (a company's TLS-inspecting proxy re-signs everything outbound with one;
// a desktop trial's local authority signs its own addresses). Each use has
// its own bundle, loaded on its own even when two hold the same
// certificates, and a use's trust is the system roots plus its bundle.
//
// Trust is fixed at start: Install is called once, in a binary's main,
// before anything makes a connection, and the rest of the process reads it
// (Current). It is a property of the process's environment, like the
// system store it extends, so it is not threaded through each owner: a
// client with no transport of its own trusts the AppTown use through
// http.DefaultTransport, and the narrower uses are named where they are
// used (Transport, Lazy).
//
// It touches no AppTown state.
package trust

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
)

// Use is what a bundle is trusted for.
type Use string

const (
	// Inference is model-provider traffic.
	Inference Use = "inference"
	// Deployments is what apps, their builds and steps trust, and
	// registries: the containers AppTown starts for a project, the daemons
	// and builders it starts, its probes of preview and deployment
	// addresses and its registry clients.
	Deployments Use = "deployments"
	// AppTown is everything else the process itself calls: Git hosts,
	// integration providers, cloud provider APIs.
	AppTown Use = "apptown"
)

// Uses are every use, in a fixed order.
var Uses = []Use{Inference, Deployments, AppTown}

// Bundle is a set of extra certificate authorities, as PEM. The zero
// Bundle is none.
type Bundle struct{ pem []byte }

// Parse reads a PEM bundle; one with no certificate in it is an error,
// never a bundle that silently adds nothing.
func Parse(data []byte) (Bundle, error) {
	var out []byte
	rest := data
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		if _, err := x509.ParseCertificate(block.Bytes); err != nil {
			return Bundle{}, fmt.Errorf("a certificate in the bundle does not parse: %w", err)
		}
		out = append(out, pem.EncodeToMemory(block)...)
	}
	if len(out) == 0 {
		return Bundle{}, errors.New("the bundle holds no PEM certificate")
	}
	return Bundle{pem: out}, nil
}

// Load reads the bundle at path; "" is none.
func Load(path string) (Bundle, error) {
	if path == "" {
		return Bundle{}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Bundle{}, err
	}
	b, err := Parse(data)
	if err != nil {
		return Bundle{}, fmt.Errorf("%s: %w", path, err)
	}
	return b, nil
}

// Empty says the bundle adds nothing.
func (b Bundle) Empty() bool { return len(b.pem) == 0 }

// With is b and o together.
func (b Bundle) With(o Bundle) Bundle {
	return Bundle{pem: append(append([]byte{}, b.pem...), o.pem...)}
}

// systemBundleFiles are where a system keeps its roots as one PEM file:
// Go's own Linux list, then macOS's.
var systemBundleFiles = []string{
	"/etc/ssl/certs/ca-certificates.crt",
	"/etc/pki/tls/certs/ca-bundle.crt",
	"/etc/ssl/ca-bundle.pem",
	"/etc/pki/tls/cacert.pem",
	"/etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem",
	"/etc/ssl/cert.pem",
}

// systemBundle is the system's roots as PEM, from the first file that holds
// any, or an error when none does.
func systemBundle() ([]byte, error) {
	for _, f := range systemBundleFiles {
		// Only that it holds a certificate: git reads the file, and a root
		// Go would not parse is no reason to give git none.
		if data, err := os.ReadFile(f); err == nil && strings.Contains(string(data), "-----BEGIN CERTIFICATE-----") {
			return data, nil
		}
	}
	return nil, errors.New("no system certificate bundle file was found, so git cannot be given the extra certificates with the system's roots")
}

// Set is a process's trust: each use's bundle and pool, and the AppTown
// use's bundle as one file with the system's roots, for git.
type Set struct {
	bundles map[Use]Bundle
	pools   map[Use]*x509.CertPool
	gitFile string
}

// Read loads each use's bundle from the file named for it ("" is none),
// naming the use's flag in an error.
func Read(paths map[Use]string) (map[Use]Bundle, error) {
	bundles := map[Use]Bundle{}
	for _, u := range Uses {
		b, err := Load(paths[u])
		if err != nil {
			return nil, fmt.Errorf("-trust-%s: %w", u, err)
		}
		bundles[u] = b
	}
	return bundles, nil
}

// New prepares bundles. Each use with a bundle gets a pool of the system's
// roots and its bundle. Git over HTTPS replaces its roots with a file
// rather than adding to them, so when the AppTown use has a bundle it is
// also written under dir with the system's roots before it; a system that
// keeps its roots in no such file (Go's Linux list, macOS's
// /etc/ssl/cert.pem) cannot give git both, and that refuses to start.
func New(dir string, bundles map[Use]Bundle) (*Set, error) {
	s := &Set{bundles: map[Use]Bundle{}, pools: map[Use]*x509.CertPool{}}
	for _, u := range Uses {
		b := bundles[u]
		if b.Empty() {
			continue
		}
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(b.pem) {
			return nil, fmt.Errorf("the %s bundle: no certificate could be added", u)
		}
		s.bundles[u], s.pools[u] = b, pool
	}
	if b := s.bundles[AppTown]; !b.Empty() {
		system, err := systemBundle()
		if err != nil {
			return nil, fmt.Errorf("-trust-%s: %w", AppTown, err)
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
		s.gitFile = filepath.Join(dir, "git-ca-bundle.pem")
		if err := writeFile(s.gitFile, append(append(append([]byte{}, system...), '\n'), b.pem...)); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func writeFile(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

var current atomic.Pointer[Set]

// Install makes s the process's trust, and, when the AppTown use has a
// bundle, its roots http.DefaultTransport's. It is called once, before
// anything makes a connection: a transport cloned earlier keeps the roots
// it was cloned with, which is why package-level transports are Lazy.
func Install(s *Set) {
	current.Store(s)
	if pool := s.Pool(AppTown); pool != nil {
		http.DefaultTransport.(*http.Transport).TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
}

// Current is the process's trust; nil (no extras anywhere) when nothing
// was installed. A nil Set answers as one with no bundles.
func Current() *Set { return current.Load() }

// Pool is the use's roots: the system's and its bundle; nil (the system's
// alone) when it has none.
func (s *Set) Pool(u Use) *x509.CertPool {
	if s == nil {
		return nil
	}
	return s.pools[u]
}

// GitFile is the AppTown use's bundle with the system's roots, as one file
// git reads (GIT_SSL_CAINFO), or "" when the use has no bundle.
func (s *Set) GitFile() string {
	if s == nil {
		return ""
	}
	return s.gitFile
}

// Transport is a transport of its own (its own connection pool), on
// http.DefaultTransport's settings, trusting exactly the use: the system
// roots and the use's bundle, never another use's.
func Transport(u Use) *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.TLSClientConfig = &tls.Config{RootCAs: Current().Pool(u), MinVersion: tls.VersionTLS12}
	return t
}

// Lazy is Transport(u) built at its first use, for a transport a package
// keeps from its start: a package's variables are made before main can
// Install the process's trust.
func Lazy(u Use) http.RoundTripper { return &lazy{use: u} }

type lazy struct {
	use  Use
	once sync.Once
	t    *http.Transport
}

func (l *lazy) transport() *http.Transport {
	l.once.Do(func() { l.t = Transport(l.use) })
	return l.t
}

func (l *lazy) RoundTrip(r *http.Request) (*http.Response, error) {
	return l.transport().RoundTrip(r)
}

// CloseIdleConnections closes the pool's idle connections, as
// http.Client.CloseIdleConnections expects of a transport.
func (l *lazy) CloseIdleConnections() { l.transport().CloseIdleConnections() }
