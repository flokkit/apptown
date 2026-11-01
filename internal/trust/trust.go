// Package trust is the certificates a process trusts beyond the system's
// own, by use. A bundle is a PEM file of extra certificate authorities
// (a company's TLS-inspecting proxy re-signs everything outbound with one;
// a desktop trial's local authority signs its own addresses). Each use has
// its own bundle, loaded on its own even when two hold the same
// certificates, and a use's trust is the system roots plus its bundle.
//
// What AppTown starts (containers, builds) is given the deployments use as
// one script, the installer (install.sh) with the bundle in it, which the
// container's or build's own command runs: the bundle is added beside the
// image's own roots, in the image's own stores (Script, Run, EnvVar).
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
	_ "embed"

	"crypto/sha256"
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
	// registries: the containers and builds AppTown starts (through
	// Script), the daemons and builders it starts, its probes of preview
	// and deployment addresses and its registry clients.
	Deployments Use = "deployments"
	// AppTown is everything else the process itself calls: Git hosts,
	// integration providers, cloud provider APIs.
	AppTown Use = "apptown"
)

// uses are every use, in a fixed order.
var uses = []Use{Inference, Deployments, AppTown}

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

// load reads the bundle at path; "" is none.
func load(path string) (Bundle, error) {
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

// empty says the bundle adds nothing.
func (b Bundle) empty() bool { return len(b.pem) == 0 }

// with is b and o together.
func (b Bundle) with(o Bundle) Bundle {
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

// Set is a process's trust: each use's bundle and pool, the AppTown use's
// bundle as one file with the system's roots, for git, and the deployments
// use's bundle as the installer containers run (Script).
type Set struct {
	bundles map[Use]Bundle
	pools   map[Use]*x509.CertPool
	gitFile string
	// here is the installer for this machine's containers: the deployments
	// bundle and the local authority; elsewhere is for containers on other
	// machines: the bundle alone.
	here, elsewhere string
}

// Read loads each use's bundle from the file named for it ("" is none),
// naming the use's flag in an error.
func Read(paths map[Use]string) (map[Use]Bundle, error) {
	bundles := map[Use]Bundle{}
	for _, u := range uses {
		b, err := load(paths[u])
		if err != nil {
			return nil, fmt.Errorf("-trust-%s: %w", u, err)
		}
		bundles[u] = b
	}
	return bundles, nil
}

// New prepares bundles. Each use with a bundle gets a pool of the system's
// roots and its bundle. local is a local certificate authority that signs
// only this machine's own addresses (apptownd -local-tls): it is trusted for
// deployments, and given to containers on this machine only. Git over HTTPS replaces its roots with a file
// rather than adding to them, so when the AppTown use has a bundle it is
// also written under dir with the system's roots before it; a system that
// keeps its roots in no such file (Go's Linux list, macOS's
// /etc/ssl/cert.pem) cannot give git both, and that refuses to start.
func New(dir string, bundles map[Use]Bundle, local Bundle) (*Set, error) {
	s := &Set{bundles: map[Use]Bundle{}, pools: map[Use]*x509.CertPool{}}
	for _, u := range uses {
		b := bundles[u]
		if u == Deployments {
			b = b.with(local)
		}
		if b.empty() {
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
	s.here, s.elsewhere = script(s.bundles[Deployments]), script(bundles[Deployments])
	if b := s.bundles[AppTown]; !b.empty() {
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

// The container side: one shell script, the installer (install.sh) with a
// bundle in it, which a container runs before its command and a build in
// its RUN lines. Every toolchain's detail (the variables, Java's store,
// NSS) is in the script, not here.
const (
	// EnvVar is the variable a container is given the script in.
	EnvVar = "APPTOWN_TRUST"
	// SecretID is the build secret a build is given the script as; a RUN
	// line mounts it at /run/secrets/apptown-trust.
	SecretID = "apptown-trust"
	// SumArg is the build argument a build is given the script's SHA-256 in
	// (BuildArgs).
	SumArg = "APPTOWN_TRUST_SUM"
	// Run runs the script in a shell; with no script it does nothing.
	Run = `eval "${APPTOWN_TRUST:-}"`
)

//go:embed install.sh
var installer string

// script is the installer with b in it, without its comments; "" when b
// is empty. A parsed bundle is base64 between PEM lines, so it holds no
// quote that could end the shell string it is put in.
func script(b Bundle) string {
	if b.empty() {
		return ""
	}
	var out strings.Builder
	for _, line := range strings.Split(installer, "\n") {
		line = strings.TrimLeft(line, " \t")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out.WriteString(line)
		out.WriteByte('\n')
	}
	return strings.Replace(out.String(), "@APPTOWN_TRUST_PEM@", strings.TrimSpace(string(b.pem)), 1)
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
//
// It also sets this process's EnvVar to the script for this machine's
// containers (or removes one it inherited), so the Docker client it starts
// passes it on by name: a container is given it with "-e APPTOWN_TRUST"
// (ContainerArgs), a build with "--secret id=apptown-trust,env=APPTOWN_TRUST"
// (BuildArgs), and the script itself never appears on a command line.
func Install(s *Set) {
	current.Store(s)
	if sc := s.Script(true); sc != "" {
		os.Setenv(EnvVar, sc)
	} else {
		os.Unsetenv(EnvVar)
	}
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

// Script is the installer a container runs (EnvVar, Run): the deployments
// bundle, with the local authority when here (the container runs on this
// machine), or "" when that is nothing.
func (s *Set) Script(here bool) string {
	if s == nil {
		return ""
	}
	if here {
		return s.here
	}
	return s.elsewhere
}

// ContainerArgs are the docker run/create arguments that give a container
// its script by name: here, from this process's environment (Install); for
// a container elsewhere (an attached Docker host) the command that runs
// the client sets the variable to Script(false) itself. None when that
// script is empty.
func ContainerArgs(here bool) []string {
	if Current().Script(here) == "" {
		return nil
	}
	return []string{"-e", EnvVar}
}

// BuildArgs are the docker build arguments that give a build the script as
// the secret SecretID, by name as ContainerArgs does, and its SHA-256 as the
// build argument SumArg; none when it is empty. A Dockerfile's RUN line that
// mounts the secret finds no file without them and runs on, so the secret is
// never a required input.
//
// A secret's content is not part of a build's cache key, so a RUN that writes
// the certificates into the image (an image with no shell to run the script)
// would keep what an earlier build cached. Such a stage declares ARG SumArg
// before that line, which makes the line build again when they change.
func BuildArgs(here bool) []string {
	sc := Current().Script(here)
	if sc == "" {
		return nil
	}
	return []string{"--secret", "id=" + SecretID + ",env=" + EnvVar, "--build-arg", SumArg + "=" + fmt.Sprintf("%x", sha256.Sum256([]byte(sc)))}
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
