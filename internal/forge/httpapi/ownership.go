package httpapi

import (
	"errors"
	"net/http"

	"flokkit.io/forge/internal/forge/deployment"
)

// The one-time ownership inspection (design/subsystems/deployment.md): an
// administrator looks at what deployments made before the forge recorded
// its creations hold, and approves what to record as theirs. Only approved
// entries are ever deleted at take-down.

type (
	// OwnershipInspected is what the inspection proposes, and the targets
	// it could not look at.
	OwnershipInspected = deployment.OwnershipInspection
	// OwnershipApproved is what an approval recorded.
	OwnershipApproved struct {
		OK       bool                           `json:"ok"`
		Approved []deployment.OwnershipProposal `json:"approved"`
	}
)

// InspectOwnership: GET /ownership/inspection — what each target holds
// under the forge's names and labels that its deployments' records lack.
// It changes nothing.
func (h Infrastructure) InspectOwnership(w http.ResponseWriter, r *http.Request) {
	out, err := h.deployment.InspectOwnership(r.Context())
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, out)
}

// ApproveOwnership: POST /ownership/approvals {keys} — record the
// proposals named, as the inspection proposes them now. A key it no longer
// proposes refuses the whole approval.
func (h Infrastructure) ApproveOwnership(w http.ResponseWriter, r *http.Request) {
	jsonCommand(w, r, jsonBody{}, func(in struct {
		Keys []string `json:"keys"`
	}) {
		if len(in.Keys) == 0 {
			WriteError(w, 400, "keys names the proposals to approve")
			return
		}
		approved, err := h.deployment.ApproveOwnership(r.Context(), in.Keys, Actor(r))
		if errors.Is(err, deployment.ErrNotProposed) {
			WriteError(w, 409, "%v", err)
			return
		}
		if err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 200, OwnershipApproved{OK: true, Approved: approved})
	})
}
