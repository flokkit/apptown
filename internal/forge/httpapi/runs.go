package httpapi

import (
	"encoding/base64"
	"net/http"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/forge/workflow"
)

// Runs translates run and evidence requests into Work operations.
// Authentication and route guards establish caller context before invocation.
type Runs struct{ work *workflow.Service }

func NewRuns(work *workflow.Service) Runs { return Runs{work: work} }

// Artifact reports can exceed the ordinary JSON body limit.
const maxArtifactBody = 64 << 20

func (h Runs) GetArtifact(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad id")
		return
	}
	artifact, err := h.work.Artifact(r.Context(), id)
	if err != nil {
		WriteFault(w, err)
		return
	}
	if artifact.Binary {
		WriteJSON(w, 200, struct {
			workflow.Artifact
			BodyBase64 string `json:"bodyBase64"`
		}{artifact.Artifact, base64.StdEncoding.EncodeToString(artifact.Raw)})
		return
	}
	WriteJSON(w, 200, artifact.Artifact)
}

func (h Runs) GetRun(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad id")
		return
	}
	d, err := h.work.Snapshot(r.Context(), id, workflow.DetailView)
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, d)
}

func (h Runs) AddArtifact(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad id")
		return
	}
	jsonCommand(w, r, jsonBody{limit: maxArtifactBody, errorText: "kind is required"}, func(in struct{ Kind, Name, Ref, Body string }) {
		if in.Kind == "" {
			WriteError(w, 400, "kind is required")
			return
		}

		artifactID, err := h.work.AttachArtifact(r.Context(), id, workflow.ArtifactDraft{Kind: in.Kind, Name: in.Name, Ref: in.Ref, Body: in.Body})
		if err != nil {
			WriteFaultOr(w, err, 500)
			return
		}
		WriteJSON(w, 201, ArtifactAttached{ID: artifactID})
	})
}

// ArtifactAttached is the id of a newly attached artifact.
type ArtifactAttached struct {
	ID ids.ID `json:"id"`
}
