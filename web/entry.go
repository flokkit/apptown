package web

import (
	"net/http"
	"regexp"
	"sync"
)

// EntryHeader carries, on every response, the entry script of the page this
// AppTown serves. A tab opened on an earlier build compares it with the
// script it was loaded from and offers a reload when they differ: the data
// a tab polls refreshes by itself, its own code does not.
const EntryHeader = "X-AppTown-Web"

// The bundler names the entry script by its content, so the name changes
// exactly when the page's code does, and not when only the server's does.
var entryScript = regexp.MustCompile(`<script[^>]*\btype="module"[^>]*\bsrc="([^"]+)"`)

// entryIn is the entry script an index.html names, or "" when it names none.
func entryIn(index []byte) string {
	m := entryScript.FindSubmatch(index)
	if m == nil {
		return ""
	}
	return string(m[1])
}

// Entry is the embedded build's entry script, read once: the embedded bytes
// cannot change. Empty when no frontend was built into this binary.
var Entry = sync.OnceValue(func() string {
	b, err := Dist.ReadFile("dist/index.html")
	if err != nil {
		return ""
	}
	return entryIn(b)
})

// WithEntry names the embedded build's entry script on every response.
func WithEntry(next http.Handler) http.Handler {
	entry := Entry()
	if entry == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(EntryHeader, entry)
		next.ServeHTTP(w, r)
	})
}
