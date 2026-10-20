package httpapi

import (
	"net/http"
	"slices"
	"strings"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/forge/conformance"
	"flokkit.io/forge/internal/stamp"
)

// CreateDisposition: POST /projects/{slug}/dispositions
func (h Conformance) CreateDisposition(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	jsonCommand(w, r, jsonBody{}, func(in struct {
		Fingerprint   string `json:"fingerprint"`
		Identifier    string `json:"identifier"`
		Package       string `json:"package"`
		Status        string `json:"status"`
		Justification string `json:"justification"`
		ExpiresAt     string `json:"expiresAt"`
	}) {
		if in.Fingerprint == "" && in.Identifier == "" {
			WriteError(w, 400, "fingerprint or identifier required")
			return
		}
		kind, err := conformance.ParseDispositionKind(in.Status)
		if err != nil {
			WriteError(w, 400, "%v", err)
			return
		}
		if strings.TrimSpace(in.Justification) == "" {
			WriteError(w, 400, "a disposition requires a justification — like every override")
			return
		}
		expires := stamp.Parse(in.ExpiresAt)
		if in.ExpiresAt != "" && !expires.Valid {
			WriteError(w, 400, "expiresAt must be an RFC3339 timestamp")
			return
		}
		saved, err := h.conformance.CreateDisposition(r.Context(), conformance.ExceptionRequest{ProjectID: p.ID, Fingerprint: in.Fingerprint, Identifier: in.Identifier, Package: in.Package, Kind: kind, Justification: in.Justification, ExpiresAt: expires})
		if err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 201, DispositionCreated{ID: saved.ID})
	})
}

// ListDispositions: GET /projects/{slug}/dispositions (all, incl. expired)
func (h Conformance) ListDispositions(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	out, err := h.conformance.Dispositions(r.Context(), p.ID, false)
	if err != nil {
		WriteFault(w, err)
		return
	}
	slices.Reverse(out)

	WriteJSON(w, 200, DispositionList{Dispositions: out})
}

// RevokeDisposition: DELETE /dispositions/{id} — audited revoke.
func (h Conformance) RevokeDisposition(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad id")
		return
	}
	if _, err := h.conformance.RevokeDisposition(r.Context(), id); err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, Done{OK: true})
}

// OpenVEX: GET /projects/{slug}/dispositions/openvex — the interop
// export (findings.md §3): accepted → affected + action_statement,
// false_positive → not_affected.
func (h Conformance) OpenVEX(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	stmts := []OpenVEXStatement{}
	dispositions, err := h.conformance.Dispositions(r.Context(), p.ID, true)
	if err != nil {
		WriteFault(w, err)
		return
	}
	for _, d := range dispositions {
		name := d.Identifier
		if name == "" {
			name = d.Fingerprint
		}
		s := OpenVEXStatement{
			Vulnerability: map[string]string{"name": name},
			Products:      []OpenVEXProduct{{ID: "apptown:project/" + p.Slug}},
			Timestamp:     d.CreatedAt,
		}
		if d.Status == conformance.FalsePositive {
			s.Status = "not_affected"
			s.Justification = "vulnerable_code_not_present"
			s.ImpactStatement = d.Justification
		} else {
			s.Status = "affected"
			s.ActionStatement = d.Justification
		}
		stmts = append(stmts, s)
	}
	WriteJSON(w, 200, OpenVEXDocument{
		Context:    "https://openvex.dev/ns/v0.2.0",
		ID:         "apptown:project/" + p.Slug + "/vex",
		Author:     "apptown",
		Timestamp:  h.now().UTC().Format(time.RFC3339),
		Version:    1,
		Statements: stmts,
	})
}

// DispositionCreated is the id of a disposition just recorded.
type DispositionCreated struct {
	ID ids.ID `json:"id"`
}

// DispositionList is a project's dispositions, newest first, expired included.
type DispositionList struct {
	Dispositions []conformance.Disposition `json:"dispositions"`
}

// OpenVEXDocument is the OpenVEX 0.2.0 export of a project's dispositions.
type OpenVEXDocument struct {
	Context    string             `json:"@context"`
	ID         string             `json:"@id"`
	Author     string             `json:"author"`
	Timestamp  string             `json:"timestamp"`
	Version    int                `json:"version"`
	Statements []OpenVEXStatement `json:"statements"`
}

// OpenVEXStatement is one disposition in OpenVEX words.
type OpenVEXStatement struct {
	Vulnerability   map[string]string `json:"vulnerability"`
	Products        []OpenVEXProduct  `json:"products"`
	Status          string            `json:"status"`
	Justification   string            `json:"justification,omitempty"`
	ActionStatement string            `json:"action_statement,omitempty"`
	ImpactStatement string            `json:"impact_statement,omitempty"`
	Timestamp       string            `json:"timestamp"`
}

// OpenVEXProduct names the product a statement is about.
type OpenVEXProduct struct {
	ID string `json:"@id"`
}
