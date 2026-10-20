package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/projectlifecycle"
)

// ProjectExport sends a project's move file: the export half of moving a
// project to another forge.
type ProjectExport struct {
	projects  *project.Projects
	access    *identity.Service
	lifecycle *projectlifecycle.Service
	sources   projectlifecycle.ExportSources
	build     string
	schema    int
	now       func() time.Time // the forge's clock, which dates the export's file name
}

func NewProjectExport(projects *project.Projects, access *identity.Service, lifecycle *projectlifecycle.Service, sources projectlifecycle.ExportSources, build string, schema int, now func() time.Time) ProjectExport {
	return ProjectExport{projects: projects, access: access, lifecycle: lifecycle, sources: sources, build: build, schema: schema, now: clock.Or(now)}
}

// ProjectExportRefused names everything that keeps a project from moving.
type ProjectExportRefused struct {
	Error   string   `json:"error"`
	Reasons []string `json:"reasons"`
}

// Export: GET /api/v1/projects/{slug}/export — administrators only. The
// file is spooled and hashed first, so its hash, size and counts come as
// headers (X-AppTown-Export-Sha256, X-AppTown-Export-Counts) before its body.
// The project.exported receipt is recorded once the whole file is written.
func (h ProjectExport) Export(w http.ResponseWriter, r *http.Request) {
	if !RequireAdmin(h.access, w, r) {
		return
	}
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	forge, err := h.access.ForgeID(r.Context())
	if err != nil {
		WriteFault(w, err)
		return
	}
	origin := projectlifecycle.ExportOrigin{AppTown: forge, Host: r.Host, Build: h.build, Schema: h.schema}
	file, err := h.lifecycle.Export(r.Context(), p, Actor(r), origin, h.sources)
	var refused *projectlifecycle.ExportRefusal
	if errors.As(err, &refused) {
		WriteJSON(w, http.StatusConflict, ProjectExportRefused{Error: refused.Error(), Reasons: refused.Reasons})
		return
	}
	if err != nil {
		WriteFault(w, err)
		return
	}
	counts, _ := json.Marshal(map[string]any{"counts": file.Manifest.Counts, "emptyChats": file.Manifest.EmptyChats, "missingTranscripts": file.Manifest.Missing,
		"skipped": file.Manifest.Skipped, "skippedChats": len(file.Manifest.SkippedChats)})
	w.Header().Set("Content-Type", "application/x-tar")
	w.Header().Set("Content-Length", strconv.FormatInt(file.Size, 10))
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", fmt.Sprintf("%s-%s.apptown-export", p.Slug, h.now().UTC().Format("20060102T150405Z"))))
	w.Header().Set("X-AppTown-Export-Sha256", file.SHA256)
	w.Header().Set("X-AppTown-Export-Counts", string(counts))
	w.WriteHeader(http.StatusOK)
	if err := file.Send(r.Context(), w); err != nil {
		// The status has gone; the receipt was not recorded, and the log
		// says why.
		log.Printf("export of %s: %v", p.Slug, err)
	}
}
