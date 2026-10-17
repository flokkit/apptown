package httpapi

import (
	"net/http"
)

func (h ProjectPolicies) ProjectApprovalPolicy(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	jsonCommand(w, r, jsonBody{errorText: "requireIndependentApproval must be true or false"}, func(in struct {
		Required *bool `json:"requireIndependentApproval"`
	}) {
		if in.Required == nil {
			WriteError(w, 400, "requireIndependentApproval must be true or false")
			return
		}

		ctx := r.Context()
		if err := h.projects.SetIndependentApproval(ctx, p.ID, *in.Required); err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 200, ApprovalPolicy{RequireIndependentApproval: *in.Required})
	})
}

// ApprovalPolicy is whether a project's approvals must come from someone
// other than the change's author.
type ApprovalPolicy struct {
	RequireIndependentApproval bool `json:"requireIndependentApproval"`
}
