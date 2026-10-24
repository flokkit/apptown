package httpapi

import (
	"io/fs"
	"net/http"
)

type IntegrationDocuments struct{ docs fs.ReadFileFS }

func NewIntegrationDocuments(docs fs.ReadFileFS) IntegrationDocuments {
	return IntegrationDocuments{docs: docs}
}

// IntegrationDocs are a connector's admin and developer documents; either may be empty.
type IntegrationDocs struct {
	Admin     string `json:"admin"`
	Developer string `json:"developer"`
}

// IntegrationDocs: GET /api/v1/integrations/docs/{capability}/{provider}
// — the fifth contribution: per-integration
// ADMIN (org-level setup) and DEVELOPER (day-to-day use) documents. Durable
// prose about the mechanism; instance state stays in generated steering.
func (h IntegrationDocuments) IntegrationDocs(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("capability") + "-" + r.PathValue("provider")
	admin, errA := h.docs.ReadFile("integrations/" + key + ".admin.md")
	dev, errD := h.docs.ReadFile("integrations/" + key + ".developer.md")
	if errA != nil && errD != nil {
		WriteError(w, 404, "no docs for %s", key)
		return
	}
	WriteJSON(w, 200, IntegrationDocs{Admin: string(admin), Developer: string(dev)})
}
