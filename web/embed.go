// Package web embeds the built frontend (web/dist) into the apptownd binary.
// Build the frontend first: cd web && npm run build (see Makefile).
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"sync"
)

//go:embed all:dist
var Dist embed.FS

// DistHandler serves the embedded build. Its file table (types, ETags,
// variants) is built once per process: the embedded bytes cannot change, and
// building it reads and hashes the whole bundle, which every App paid again.
var DistHandler = sync.OnceValue(func() http.Handler {
	sub, err := fs.Sub(Dist, "dist")
	if err != nil {
		return http.NotFoundHandler()
	}
	return Handler(sub)
})
