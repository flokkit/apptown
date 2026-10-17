package web

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"
)

// Cache policy. Vite names everything under assets/ by content hash, so a
// given URL never changes and browsers may keep it for a year without asking.
// Everything else (index.html, which names the current hashes, and the SPA
// fallback) is revalidated on every load so a deploy shows at once; the ETag
// keeps that revalidation to a 304.
const (
	cacheImmutable  = "public, max-age=31536000, immutable"
	cacheRevalidate = "no-cache"
)

// encodings in preference order. The build (web/precompress.js) writes
// name.br and name.gz beside each text file that compresses well.
var encodings = []struct{ coding, suffix string }{{"br", ".br"}, {"gzip", ".gz"}}

type variant struct {
	coding, file, etag string
	size               int64
}

type staticFile struct {
	contentType string
	cache       string
	variants    []variant // preferred first; identity last
}

// Handler serves a built single-page app from fsys: hashed assets are
// immutable, anything else is revalidated, the smallest variant the client
// accepts is chosen by Accept-Encoding, and an unknown path outside assets/
// gets index.html so client-side routes load. Serving the compressed bytes
// from forged itself means compression and caching hold behind any proxy.
func Handler(fsys fs.FS) http.Handler {
	files := map[string]*staticFile{}
	_ = fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || isVariant(name) {
			return nil
		}
		raw, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil
		}
		sum := sha256.Sum256(raw)
		tag := hex.EncodeToString(sum[:12])
		f := &staticFile{contentType: mime.TypeByExtension(path.Ext(name)), cache: cacheRevalidate}
		if f.contentType == "" {
			f.contentType = http.DetectContentType(raw)
		}
		if strings.HasPrefix(name, "assets/") {
			f.cache = cacheImmutable
		}
		for _, e := range encodings {
			if info, err := fs.Stat(fsys, name+e.suffix); err == nil {
				f.variants = append(f.variants, variant{e.coding, name + e.suffix, `"` + tag + "-" + e.coding + `"`, info.Size()})
			}
		}
		f.variants = append(f.variants, variant{"identity", name, `"` + tag + `"`, int64(len(raw))})
		files[name] = f
		return nil
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name == "" {
			name = "index.html"
		}
		f := files[name]
		if f == nil && !strings.HasPrefix(name+"/", "assets/") {
			// SPA fallback: a client-side route gets the app shell. A missing
			// hashed asset stays a 404 (a stale tab asking for a chunk an
			// older build had), never HTML posing as JavaScript.
			f = files["index.html"]
		}
		if f == nil {
			// no-store: a miss may be a deploy in progress; never let a
			// proxy or browser keep the 404 for a URL that will exist.
			w.Header().Set("Cache-Control", "no-store")
			http.NotFound(w, r)
			return
		}
		v := f.pick(r.Header.Get("Accept-Encoding"))
		body, err := fsys.Open(v.file)
		if err != nil {
			http.Error(w, "static file unavailable", http.StatusInternalServerError)
			return
		}
		defer body.Close()
		rs, ok := body.(io.ReadSeeker)
		if !ok {
			http.Error(w, "static file unavailable", http.StatusInternalServerError)
			return
		}
		h := w.Header()
		h.Set("Content-Type", f.contentType)
		h.Set("Cache-Control", f.cache)
		h.Set("ETag", v.etag)
		if len(f.variants) > 1 {
			h.Set("Vary", "Accept-Encoding")
		}
		if v.coding != "identity" {
			// ServeContent leaves Content-Length out once Content-Encoding is
			// set, because a range of the encoded bytes has a different length.
			// Encoded variants are therefore always sent whole (a full
			// response is a valid answer to a Range request) with their length.
			h.Set("Content-Encoding", v.coding)
			h.Set("Content-Length", strconv.FormatInt(v.size, 10))
			r = r.Clone(r.Context())
			r.Header.Del("Range")
		}
		// ServeContent answers If-None-Match with 304 (dropping the length
		// and encoding), HEAD, and identity ranges.
		http.ServeContent(w, r, "", time.Time{}, rs)
	})
}

func isVariant(name string) bool {
	for _, e := range encodings {
		if strings.HasSuffix(name, e.suffix) {
			return true
		}
	}
	return false
}

// pick returns the first variant the client accepts; identity is always last
// and always acceptable here (a client refusing identity still gets bytes).
func (f *staticFile) pick(acceptEncoding string) variant {
	for _, v := range f.variants[:len(f.variants)-1] {
		if accepts(acceptEncoding, v.coding) {
			return v
		}
	}
	return f.variants[len(f.variants)-1]
}

// accepts reports whether an Accept-Encoding header allows coding: listed by
// name (or by "*") with a q-value above zero. An exact entry outranks "*".
func accepts(header, coding string) bool {
	exact, wild := -1.0, -1.0
	for _, part := range strings.Split(header, ",") {
		fields := strings.Split(part, ";")
		name := strings.ToLower(strings.TrimSpace(fields[0]))
		q := 1.0
		for _, p := range fields[1:] {
			k, val, ok := strings.Cut(strings.TrimSpace(p), "=")
			if ok && strings.EqualFold(strings.TrimSpace(k), "q") {
				if n, err := strconv.ParseFloat(strings.TrimSpace(val), 64); err == nil {
					q = n
				} else {
					q = 0
				}
			}
		}
		switch name {
		case coding:
			exact = q
		case "*":
			wild = q
		}
	}
	if exact >= 0 {
		return exact > 0
	}
	return wild > 0
}
