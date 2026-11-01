package trust

import (
	"encoding/pem"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/localtls"
)

// signed is an HTTPS server whose certificate a fresh local authority
// issued, asked by name, and that authority as a bundle.
func signed(t *testing.T) (url string, b Bundle) {
	t.Helper()
	ca, err := localtls.Load(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	srv.TLS = ca.TLSConfig()
	srv.StartTLS()
	t.Cleanup(srv.Close)
	b, err = Parse(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Certificate().Raw}))
	if err != nil {
		t.Fatal(err)
	}
	// By name: the authority issues for the name the client asks for.
	return strings.Replace(srv.URL, "127.0.0.1", "localhost", 1), b
}

func get(rt http.RoundTripper, url string) error {
	resp, err := (&http.Client{Transport: rt}).Get(url)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

// A bundle that names no certificate, or a file that is not there, is
// refused, never a bundle that silently adds nothing.
func TestABundleWithNoCertificateIsRefused(t *testing.T) {
	if _, err := Parse([]byte("-----BEGIN CERTIFICATE-----\n")); err == nil {
		t.Error("a PEM with no certificate parsed")
	}
	if _, err := load(filepath.Join(t.TempDir(), "missing.pem")); err == nil {
		t.Error("a missing file loaded")
	}
	if b, err := load(""); err != nil || !b.empty() {
		t.Errorf("no path: %v %v", b, err)
	}
	if _, err := Read(map[Use]string{Inference: filepath.Join(t.TempDir(), "x.pem")}); err == nil || !strings.Contains(err.Error(), "-trust-inference") {
		t.Errorf("the error does not name the flag: %v", err)
	}
}

// Installed trust reaches every kind of client by its use: the AppTown use
// through http.DefaultTransport (and a clone made after), a narrower use
// through its own transport, including one a package made before Install;
// and a use never trusts another use's bundle.
//
// Not parallel: it installs the process's trust.
func TestInstalledTrustReachesEachUsesClientsAndNoOther(t *testing.T) {
	appURL, app := signed(t)
	depURL, dep := signed(t)
	infURL, inf := signed(t)
	early := Lazy(Deployments) // a package's transport, made before Install
	set, err := New(t.TempDir(), map[Use]Bundle{AppTown: app, Deployments: dep, Inference: inf}, Bundle{})
	if err != nil {
		t.Fatal(err)
	}
	before := http.DefaultTransport.(*http.Transport).TLSClientConfig
	t.Cleanup(func() { http.DefaultTransport.(*http.Transport).TLSClientConfig = before; current.Store(nil) })
	Install(set)

	if err := get(http.DefaultTransport, appURL); err != nil {
		t.Errorf("the default transport does not trust the AppTown use: %v", err)
	}
	if err := get(http.DefaultTransport.(*http.Transport).Clone(), appURL); err != nil {
		t.Errorf("a clone made after Install does not trust the AppTown use: %v", err)
	}
	if err := get(early, depURL); err != nil {
		t.Errorf("a Lazy made before Install does not trust its use: %v", err)
	}
	if err := get(Transport(Deployments), depURL); err != nil {
		t.Errorf("the deployments transport: %v", err)
	}
	if err := get(Transport(Inference), infURL); err != nil {
		t.Errorf("the inference transport: %v", err)
	}
	for name, try := range map[string]func() error{
		"deployments → AppTown's": func() error { return get(Transport(Deployments), appURL) },
		"inference → AppTown's":   func() error { return get(Transport(Inference), appURL) },
		"inference → deployments": func() error { return get(Transport(Inference), depURL) },
		"deployments → inference": func() error { return get(Transport(Deployments), infURL) },
		"default → deployments":   func() error { return get(http.DefaultTransport, depURL) },
		"default → inference":     func() error { return get(http.DefaultTransport, infURL) },
	} {
		if err := try(); err == nil || !strings.Contains(err.Error(), "unknown authority") {
			t.Errorf("%s: trusted another use's bundle (%v)", name, err)
		}
	}
	// A use with no bundle of its own trusts the system's roots alone, not
	// the AppTown use's that the default transport now carries.
	only, err := New(t.TempDir(), map[Use]Bundle{AppTown: app}, Bundle{})
	if err != nil {
		t.Fatal(err)
	}
	Install(only)
	for _, u := range []Use{Inference, Deployments} {
		if err := get(Transport(u), appURL); err == nil || !strings.Contains(err.Error(), "unknown authority") {
			t.Errorf("%s, with no bundle, trusted the AppTown use's (%v)", u, err)
		}
	}
	git, err := os.ReadFile(set.GitFile())
	if err != nil || !strings.Contains(string(git), string(app.pem)) || len(git) <= len(app.pem)+1 {
		t.Errorf("git's file lacks the system's roots or the bundle (%d bytes): %v", len(git), err)
	}
}

// A package-level transport cloned from http.DefaultTransport is made
// before main installs the process's trust, so it would never trust a
// bundle: such transports are trust.Lazy. Ratchet at zero.
func TestNoPackageClonesTheDefaultTransportAtStart(t *testing.T) {
	root := filepath.Join("..", "..")
	var found []string
	for _, top := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				return err
			}
			for _, decl := range f.Decls {
				g, ok := decl.(*ast.GenDecl)
				if !ok || g.Tok != token.VAR {
					continue
				}
				ast.Inspect(g, func(n ast.Node) bool {
					if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "DefaultTransport" {
						found = append(found, path)
					}
					return true
				})
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(found) > 0 {
		t.Fatalf("package-level transports built from http.DefaultTransport at start (use trust.Lazy):\n%s", strings.Join(found, "\n"))
	}
}
