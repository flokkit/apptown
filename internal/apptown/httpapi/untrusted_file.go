package httpapi

import (
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// serveUntrustedFile is the only raw user-file response. Never infer active
// content types from an extension. Recognized raster bytes may display inline;
// everything else is an opaque download (including SVG, HTML and PDF).
func ServeUntrustedFile(dataDir string, w http.ResponseWriter, r *http.Request, path string) {
	rel, err := filepath.Rel(dataDir, path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	root, err := os.OpenRoot(dataDir)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer root.Close()
	// An upload may contain symlinks created by its runtime. Confining reads
	// merely to DataDir would still expose sibling sessions and secrets.env.
	uploads, err := root.OpenRoot(filepath.Dir(rel))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer uploads.Close()
	f, err := uploads.Open(filepath.Base(rel))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	serveUntrusted(w, r, f, filepath.Base(path), false)
}

// serveUntrusted answers with one opened file that something untrusted
// wrote. Recognized raster bytes display inline; with textInline, text
// displays as plain text (HTML among it, so it shows as source and never
// runs); everything else is an opaque download. A type is never inferred
// from a name, and anything but a regular file is not found.
func serveUntrusted(w http.ResponseWriter, r *http.Request, f *os.File, name string, textInline bool) {
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	var head [512]byte
	n, err := f.Read(head[:])
	if err != nil && err != io.EOF {
		http.NotFound(w, r)
		return
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		http.NotFound(w, r)
		return
	}
	typ, disposition := "application/octet-stream", "attachment"
	switch detected := http.DetectContentType(head[:n]); {
	case detected == "image/png", detected == "image/jpeg", detected == "image/gif", detected == "image/webp":
		typ, disposition = detected, "inline"
	case textInline && strings.HasPrefix(detected, "text/"):
		typ, disposition = "text/plain; charset=utf-8", "inline"
	}
	w.Header().Set("Content-Type", typ)
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": name}))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'; frame-ancestors 'none'; base-uri 'none'")
	w.Header().Set("Cache-Control", "private, no-store")
	http.ServeContent(w, r, name, st.ModTime(), f)
}
