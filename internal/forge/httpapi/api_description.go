package httpapi

import (
	"io/fs"
	"net/http"
)

// APIDescription serves the OpenAPI document shipped with this build
// (stock/docs/api/openapi.json), so a client can read what the forge it
// talks to offers. The document is generated from the route table by the
// forge package's TestAPIDescriptionIsCurrent.
type APIDescription struct{ files fs.FS }

func NewAPIDescription(files fs.FS) APIDescription { return APIDescription{files: files} }

// Serve: GET /api/v1/openapi.json — the same document for every caller.
func (h APIDescription) Serve(w http.ResponseWriter, _ *http.Request) {
	doc, err := fs.ReadFile(h.files, "docs/api/openapi.json")
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "this build carries no API description: %v", err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(doc)
}
