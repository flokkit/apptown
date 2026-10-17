package httpapi

import (
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"sort"
	"strings"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/forge/workflow"
)

type Workspaces struct {
	db      *database.DB
	dataDir string
}

func NewWorkspaces(db *database.DB, dataDir string) Workspaces {
	return Workspaces{db: db, dataDir: dataDir}
}

// handleWorkspace serves a run's workspace directory over HTTP — the "browse"
// materialization rung (state-model §4.1). Agents and project code write
// the folder, so every read goes through an os.Root on it (no path or link
// resolves outside), a directory is a listing the forge writes, and a file
// is served as untrusted: never as a page that runs on the forge's origin.
func (h Workspaces) Workspace(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad id")
		return
	}
	run, err := workflow.ReadRunIn(r.Context(), h.db, id)
	if err != nil {
		writeReadFault(w, err, "no such run")
		return
	}
	source, err := workflow.ReadSourceIn(r.Context(), h.db, workflow.RunRef{ID: run.ID, ProjectID: run.ProjectID})
	if err != nil {
		WriteFault(w, err)
		return
	}
	ws := source.Workspace(h.dataDir)
	if _, err := os.Stat(ws); err != nil {
		WriteError(w, 404, "run has no workspace")
		return
	}
	// A folder behind its published branch (scm.Behind) is not shown as the
	// work: the change's diff shows the branch.
	if behind, err := scm.Behind(r.Context(), ws, source.Repository(h.dataDir)); err != nil {
		WriteFault(w, err)
		return
	} else if behind {
		WriteError(w, 409, "this change's folder has not caught up with the work saved on its branch, so its files cannot be shown here now; the change's diff shows the saved work")
		return
	}
	serveConfinedTree(w, r, ws, r.PathValue("path"))
}

// serveConfinedTree answers rel inside dir: a listing for a directory, an
// untrusted file for a regular file, not found for anything else or
// anything that resolves outside dir.
func serveConfinedTree(w http.ResponseWriter, r *http.Request, dir, rel string) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer root.Close()
	name := strings.TrimPrefix(path.Clean("/"+rel), "/")
	if name == "" {
		name = "."
	}
	f, err := root.Open(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if !st.IsDir() {
		serveUntrusted(w, r, f, path.Base(name), true)
		return
	}
	// A listing's links are relative, so a directory is always addressed
	// with its trailing slash.
	if !strings.HasSuffix(r.URL.Path, "/") {
		http.Redirect(w, r, r.URL.Path+"/", http.StatusMovedPermanently)
		return
	}
	entries, err := f.ReadDir(-1)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir() != entries[j].IsDir() {
			return entries[i].IsDir()
		}
		return entries[i].Name() < entries[j].Name()
	})
	var b strings.Builder
	b.WriteString("<!doctype html><meta charset=\"utf-8\"><title>")
	b.WriteString(html.EscapeString(name))
	b.WriteString("</title><pre>\n")
	if name != "." {
		b.WriteString("<a href=\"../\">../</a>\n")
	}
	for _, e := range entries {
		label := e.Name()
		if e.IsDir() {
			label += "/"
		}
		// "./" keeps a name like "javascript:x" a path, never a scheme.
		fmt.Fprintf(&b, "<a href=\"./%s\">%s</a>\n", html.EscapeString((&url.URL{Path: label}).EscapedPath()), html.EscapeString(label))
	}
	b.WriteString("</pre>\n")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
	w.Header().Set("Cache-Control", "private, no-store")
	_, _ = io.WriteString(w, b.String())
}
