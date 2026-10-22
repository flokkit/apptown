package httpapi

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A run's folder is written by agents and project code. Browsing it never
// reads outside it, and nothing in it is served as a page that runs on the
// AppTown's origin.
//
// Kills: http.FileServer(http.Dir(ws)), which followed links out of the
// folder and served an agent's index.html as text/html.
func TestABrowsedWorkspaceStaysInItsFolderAndNeverRuns(t *testing.T) {
	t.Parallel()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secrets.env"), []byte("KEY=secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	ws := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(ws, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(ws, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("index.html", "<script>fetch('/api/v1/me')</script>")
	write("src/main.go", "package main\n")
	write("logo.png", "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	write("blob.bin", "\x00\x01\x02\x03\xff")
	write("<img src=x onerror=alert(1)>", "x")
	write("javascript:alert(1)", "x")
	for name, target := range map[string]string{"leak": filepath.Join(outside, "secrets.env"), "out": outside} {
		if err := os.Symlink(target, filepath.Join(ws, name)); err != nil {
			t.Fatal(err)
		}
	}
	get := func(urlPath, rel string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		serveConfinedTree(w, httptest.NewRequest("GET", urlPath, nil), ws, rel)
		return w
	}
	for _, rel := range []string{"leak", "out/secrets.env", "out/", "../" + filepath.Base(outside) + "/secrets.env"} {
		if w := get("/x/"+rel, rel); w.Code != 404 || strings.Contains(w.Body.String(), "secret") {
			t.Errorf("%s resolves outside the folder: %d %q", rel, w.Code, w.Body.String())
		}
	}
	w := get("/x/index.html", "index.html")
	if w.Code != 200 || w.Header().Get("Content-Type") != "text/plain; charset=utf-8" ||
		!strings.HasPrefix(w.Header().Get("Content-Security-Policy"), "sandbox") || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("an agent's HTML must show as source in a sandbox: %d %v", w.Code, w.Header())
	}
	if w := get("/x/src/main.go", "src/main.go"); w.Code != 200 || w.Body.String() != "package main\n" || !strings.HasPrefix(w.Header().Get("Content-Disposition"), "inline") {
		t.Fatalf("source reads in the browser: %d %v", w.Code, w.Header())
	}
	if w := get("/x/logo.png", "logo.png"); w.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("a picture shows inline: %v", w.Header())
	}
	if w := get("/x/blob.bin", "blob.bin"); w.Header().Get("Content-Type") != "application/octet-stream" || !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("other bytes download: %v", w.Header())
	}
	if w := get("/x/src", "src"); w.Code != 301 || w.Header().Get("Location") != "/x/src/" {
		t.Fatalf("a directory is addressed with its slash: %d %v", w.Code, w.Header())
	}
	list := get("/x/", "")
	body := list.Body.String()
	if list.Code != 200 || strings.Contains(body, "<img") || !strings.Contains(body, "&lt;img src=x onerror=alert(1)&gt;") ||
		strings.Contains(body, `href="javascript:`) || !strings.Contains(body, `href="./src/"`) ||
		!strings.Contains(list.Header().Get("Content-Security-Policy"), "default-src 'none'") {
		t.Fatalf("the listing escapes names and keeps links as paths: %d %v\n%s", list.Code, list.Header(), body)
	}
}
