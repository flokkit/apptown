package web

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"testing/fstest"
)

// A build as web/precompress.js leaves it: a hashed script with both
// precompressed variants, a hashed stylesheet with gzip only, a font with
// none, and the unhashed app shell.
func testBuild() fstest.MapFS {
	return fstest.MapFS{
		"index.html":              {Data: []byte("<!doctype html><title>Flokkit</title>")},
		"index.html.gz":           {Data: []byte("gz-index")},
		"assets/index-abc.js":     {Data: []byte("console.log('identity bytes of the script')")},
		"assets/index-abc.js.br":  {Data: []byte("br-js")},
		"assets/index-abc.js.gz":  {Data: []byte("gz-js!")},
		"assets/index-abc.css":    {Data: []byte("body{color:red}")},
		"assets/index-abc.css.gz": {Data: []byte("gz-css")},
		"assets/font-abc.woff2":   {Data: []byte("wOF2 font bytes")},
	}
}

func get(t *testing.T, h http.Handler, path string, header map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestHandlerServesTheBestEncodingTheClientAccepts(t *testing.T) {
	build := testBuild()
	h := Handler(build)
	cases := []struct {
		name, path, accept, coding, body string
	}{
		{"brotli preferred over gzip", "/assets/index-abc.js", "gzip, deflate, br", "br", "br-js"},
		{"gzip when brotli is not offered", "/assets/index-abc.js", "gzip", "gzip", "gz-js!"},
		{"gzip when brotli is refused", "/assets/index-abc.js", "br;q=0, gzip", "gzip", "gz-js!"},
		{"wildcard accepts brotli", "/assets/index-abc.js", "*", "br", "br-js"},
		{"identity without Accept-Encoding", "/assets/index-abc.js", "", "", "console.log('identity bytes of the script')"},
		{"gzip when that is the only variant", "/assets/index-abc.css", "br, gzip", "gzip", "gz-css"},
		{"identity when no variant exists", "/assets/font-abc.woff2", "br, gzip", "", "wOF2 font bytes"},
		{"app shell compressed too", "/", "gzip", "gzip", "gz-index"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := get(t, h, c.path, map[string]string{"Accept-Encoding": c.accept})
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d", rec.Code)
			}
			if got := rec.Header().Get("Content-Encoding"); got != c.coding {
				t.Errorf("Content-Encoding %q, want %q", got, c.coding)
			}
			if rec.Body.String() != c.body {
				t.Errorf("body %q, want %q", rec.Body.String(), c.body)
			}
			if got := rec.Header().Get("Content-Length"); got != strconv.Itoa(len(c.body)) {
				t.Errorf("Content-Length %q, want the length of the bytes sent (%d)", got, len(c.body))
			}
		})
	}
}

func TestHandlerLabelsEachRepresentation(t *testing.T) {
	h := Handler(testBuild())
	js := map[string]string{}
	for _, accept := range []string{"br", "gzip", ""} {
		rec := get(t, h, "/assets/index-abc.js", map[string]string{"Accept-Encoding": accept})
		if ct := rec.Header().Get("Content-Type"); ct != "text/javascript; charset=utf-8" {
			t.Errorf("%q: Content-Type %q is the original file's, not the compressed bytes'", accept, ct)
		}
		if v := rec.Header().Get("Vary"); v != "Accept-Encoding" {
			t.Errorf("%q: Vary %q; a shared cache must key on Accept-Encoding", accept, v)
		}
		etag := rec.Header().Get("ETag")
		if etag == "" {
			t.Fatalf("%q: no ETag", accept)
		}
		for other, tag := range js {
			if tag == etag {
				t.Errorf("%q and %q share ETag %s; each encoding is its own representation", accept, other, etag)
			}
		}
		js[accept] = etag
	}
	if v := get(t, h, "/assets/font-abc.woff2", nil).Header().Get("Vary"); v != "" {
		t.Errorf("Vary %q on a file with a single representation", v)
	}
}

func TestHandlerCachePolicy(t *testing.T) {
	h := Handler(testBuild())
	for path, want := range map[string]string{
		"/assets/index-abc.js":   "public, max-age=31536000, immutable",
		"/assets/font-abc.woff2": "public, max-age=31536000, immutable",
		"/":                      "no-cache",
		"/index.html":            "no-cache",
		"/projects/demo":         "no-cache", // SPA fallback is the app shell
	} {
		rec := get(t, h, path, nil)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status %d", path, rec.Code)
		}
		if got := rec.Header().Get("Cache-Control"); got != want {
			t.Errorf("%s: Cache-Control %q, want %q", path, got, want)
		}
	}
}

func TestHandlerRevalidatesWithETag(t *testing.T) {
	h := Handler(testBuild())
	for _, path := range []string{"/", "/projects/demo", "/assets/index-abc.js"} {
		first := get(t, h, path, map[string]string{"Accept-Encoding": "gzip"})
		etag := first.Header().Get("ETag")
		again := get(t, h, path, map[string]string{"Accept-Encoding": "gzip", "If-None-Match": etag})
		if again.Code != http.StatusNotModified || again.Body.Len() != 0 {
			t.Errorf("%s: matching ETag got %d with %d bytes, want 304 and no body", path, again.Code, again.Body.Len())
		}
		stale := get(t, h, path, map[string]string{"Accept-Encoding": "gzip", "If-None-Match": `"an-older-build"`})
		if stale.Code != http.StatusOK {
			t.Errorf("%s: an older ETag got %d, want 200", path, stale.Code)
		}
	}
	// A browser switching encodings must not be told its other copy is current.
	gz := get(t, h, "/assets/index-abc.js", map[string]string{"Accept-Encoding": "gzip"}).Header().Get("ETag")
	if rec := get(t, h, "/assets/index-abc.js", map[string]string{"Accept-Encoding": "br", "If-None-Match": gz}); rec.Code != http.StatusOK {
		t.Errorf("gzip ETag revalidated a brotli request: %d", rec.Code)
	}
}

// After a deploy an open tab may ask for a chunk of the previous build. It
// must get a plain, uncacheable 404 so its import fails and the page can
// reload, never the app shell posing as JavaScript under an immutable header.
func TestHandlerMissingAssetIsAnUncacheable404(t *testing.T) {
	h := Handler(testBuild())
	for _, path := range []string{"/assets/index-old.js", "/assets/index-abc.js.br", "/assets/"} {
		rec := get(t, h, path, map[string]string{"Accept-Encoding": "br, gzip"})
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", path, rec.Code)
		}
		if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
			t.Errorf("%s: Cache-Control %q, want no-store", path, cc)
		}
	}
}

// The real build embedded in apptownd has precompressed variants of the app's
// script; without them every visit downloads it uncompressed.
func TestEmbeddedBuildIsPrecompressed(t *testing.T) {
	sub, err := fs.Sub(Dist, "dist")
	if err != nil {
		t.Fatal(err)
	}
	scripts, _ := fs.Glob(sub, "assets/index-*.js")
	if len(scripts) == 0 {
		t.Skip("web/dist has no build; run make web")
	}
	h := DistHandler()
	for _, coding := range []string{"br", "gzip"} {
		rec := get(t, h, "/"+scripts[0], map[string]string{"Accept-Encoding": coding})
		if rec.Header().Get("Content-Encoding") != coding {
			t.Errorf("%s: served %q; the build did not precompress it", scripts[0], rec.Header().Get("Content-Encoding"))
		}
	}
}

// Indexing the embedded build reads and hashes every file in it. It happens
// once per process, not once per App: a test binary builds hundreds of Apps.
func TestEmbeddedBuildIsIndexedOncePerProcess(t *testing.T) {
	DistHandler()
	if allocs := testing.AllocsPerRun(5, func() { DistHandler() }); allocs != 0 {
		t.Fatalf("serving the embedded build again allocated %v times; it was indexed again", allocs)
	}
}
