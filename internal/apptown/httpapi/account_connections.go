package httpapi

import (
	"context"
	"log"
	"net/http"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
)

// ListDelegations: GET /api/v1/auth/delegations — your own; every
// one with ?all=1 for whoever may manage authentication.
func (h Accounts) ListDelegations(w http.ResponseWriter, r *http.Request) {
	pr := Principal(r)
	all := false
	if r.URL.Query().Get("all") == "1" {
		var err error
		if all, err = h.holds(r.Context(), pr, access.CapIdentityAuth, access.Resource{Type: "instance"}); err != nil {
			WriteFaultOr(w, err, http.StatusServiceUnavailable)
			return
		}
	}
	out, err := h.access.Connections(r.Context(), pr.Name, all)
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, DelegationList{Delegations: out})
}

// RevokeDelegation: DELETE /api/v1/auth/delegations/{id} — yours, or
// anyone's for whoever may manage authentication; never through an agent's
// credential.
func (h Accounts) RevokeDelegation(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad id")
		return
	}
	d, err := h.liveDelegation(r.Context(), id)
	if err != nil {
		WriteFault(w, err)
		return
	}
	if d == nil {
		WriteError(w, 404, "no such connection")
		return
	}
	pr := Principal(r)
	// Disconnecting is the person's act even on their own connections: an
	// agent holding one of their credentials must not end the others.
	if access.AgentCredential(pr) {
		WriteFault(w, &access.Denied{Code: access.DenyDelegationNarrow, Reason: access.CredentialNarrowReason(pr, "disconnect coding-agent connections")})
		return
	}
	if d.User != pr.Name {
		if _, ok := AuthorizeRequest(h.access, w, r, access.CapIdentityAuth, access.Resource{Type: "instance"}, access.Context{}); !ok {
			return
		}
	}
	if err := h.revokeDelegation(r.Context(), id, pr.Name); err != nil {
		WriteError(w, 500, "disconnecting: %v", err)
		return
	}
	WriteJSON(w, 200, DelegationRevoked{OK: true, ID: id.String()})
}

// holds answers whether pr may, as a question: no receipt, and a
// fact that cannot be read is an error.
func (h Accounts) holds(ctx context.Context, pr access.Principal, c access.Capability, res access.Resource) (bool, error) {
	dec, err := h.access.Explain(ctx, pr, access.Permission{Capability: c, Resource: res})
	return dec.Allow, err
}

// liveDelegation is the live delegation with this id: nil when there is
// none or it has ended, an error when the read failed.
func (h Accounts) liveDelegation(ctx context.Context, id ids.ID) (*identity.Delegation, error) {
	return h.access.LiveConnection(ctx, id)
}

// revokeDelegation ends a delegation and every token it carries — one
// transaction, so a revoke never half-happens.
func (h Accounts) revokeDelegation(ctx context.Context, id ids.ID, by string) error {
	err := h.access.Disconnect(ctx, id, by)
	if err != nil {
		log.Printf("delegation %d: revoking: %v", id, err)
	}
	return err
}

// DelegationList is the coding-agent connections a person may see.
type DelegationList struct {
	Delegations []identity.Delegation `json:"delegations"`
}

// DelegationRevoked names the connection that was ended.
type DelegationRevoked struct {
	OK bool   `json:"ok"`
	ID string `json:"id"`
}
