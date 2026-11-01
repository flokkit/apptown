package identity

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/identity/internal/accesssql"
	"github.com/flokkit/apptown/internal/database"
)

// Scoped system bindings: authority AppTown holds for one purpose on one
// project, granted by a person's decision, such as deleting a project and
// purging it after its kept period. A binding acts as AppTown, never as
// the person; its receipts name the binding, who granted it and what they
// decided. It permits only what its purpose names, is resolved before each
// effect like any other credential (RequireStill), and stops what it runs
// when revoked.

// BindingDeletion is a project's deletion and its purge when due.
const BindingDeletion = "deletion"

// bindingCapabilities are what each purpose permits, in its project only.
var bindingCapabilities = map[string][]access.Capability{
	BindingDeletion: {access.CapDeploymentTeardown, access.CapProjectDelete},
}

// BindingCredential is a binding's credential reference.
func BindingCredential(id ids.ID) string { return "system:binding/" + id.String() }

// BindingPrincipal is AppTown acting under a binding: its receipts name
// AppTown, the binding and (as Person) who granted it.
func BindingPrincipal(id ids.ID, grantedBy string) access.Principal {
	return access.Principal{Kind: "instrument", Name: "apptown-system", Instrument: access.SystemInstrument, Person: grantedBy, Via: "binding", Credential: BindingCredential(id)}
}

// GrantBindingIn records a binding in the caller's transaction, with the
// decision that grants it and its receipt, granted at the caller's clock.
func GrantBindingIn(ctx context.Context, tx *database.Tx, purpose string, projectID ids.ID, grantedBy string, decision map[string]any, at time.Time) (ids.ID, error) {
	if _, ok := bindingCapabilities[purpose]; !ok {
		return ids.Nil, fmt.Errorf("no binding purpose %q", purpose)
	}
	raw, err := json.Marshal(decision)
	if err != nil {
		return ids.Nil, err
	}
	granter, err := ActorIn(ctx, tx, grantedBy)
	if err != nil {
		return ids.Nil, err
	}
	id, err := queries(tx).InsertSystemBinding(ctx, accesssql.InsertSystemBindingParams{ID: ids.New(at.UTC()), Purpose: purpose, ProjectID: projectID, GrantedBy: grantedBy, GrantedByPrincipal: granter.Principal, GrantedByVia: granter.Via, Decision: raw, CreatedAt: at.UTC()})
	if err != nil {
		return ids.Nil, err
	}
	return id, audit.At(at).RecordIn(ctx, tx, grantedBy, projectID, "binding.granted", BindingCredential(id), map[string]any{"binding": id, "purpose": purpose, "decision": decision})
}

// RevokeBindingIn revokes a binding at the caller's clock; one already
// revoked stays as it was.
func RevokeBindingIn(ctx context.Context, tx *database.Tx, id ids.ID, by, why string, at time.Time) error {
	revoker, err := ActorIn(ctx, tx, by)
	if err != nil {
		return err
	}
	n, err := queries(tx).RevokeSystemBinding(ctx, accesssql.RevokeSystemBindingParams{ID: id, RevokedBy: by, RevokedByPrincipal: revoker.Principal, RevokedByVia: revoker.Via, RevokedWhy: why, At: at.UTC()})
	if err != nil || n == 0 {
		return err
	}
	b, err := queries(tx).SystemBinding(ctx, id)
	if err != nil {
		return err
	}
	return audit.At(at).RecordIn(ctx, tx, by, b.ProjectID, "binding.revoked", BindingCredential(id), map[string]any{"binding": id, "purpose": b.Purpose, "why": why})
}

// Binding is a binding as its project's page shows it.
type Binding struct {
	ID        ids.ID `json:"id"`
	Purpose   string `json:"purpose"`
	ProjectID ids.ID `json:"projectId"`
	GrantedBy string `json:"grantedBy"`
	// GrantedByPrincipal and RevokedByPrincipal are those people's
	// principals; zero when not resolved to a person.
	GrantedByPrincipal ids.ID `json:"grantedByPrincipal,omitzero"`
	Revoked            bool   `json:"revoked"`
	RevokedBy          string `json:"revokedBy,omitempty"`
	RevokedByPrincipal ids.ID `json:"revokedByPrincipal,omitzero"`
}

// BindingIn reads a binding.
func BindingIn(ctx context.Context, q database.Queryer, id ids.ID) (Binding, error) {
	r, err := queries(q).SystemBinding(ctx, id)
	if err != nil {
		return Binding{}, err
	}
	return Binding{ID: r.ID, Purpose: r.Purpose, ProjectID: r.ProjectID, GrantedBy: r.GrantedBy, GrantedByPrincipal: r.GrantedByPrincipal, Revoked: r.RevokedAt.Valid, RevokedBy: r.RevokedBy, RevokedByPrincipal: r.RevokedByPrincipal}, nil
}

// bindingAllows checks a binding credential against what it is asked to
// do: live, in its project, and a capability its purpose names.
func (s *Service) bindingAllows(ctx context.Context, ref string, p access.Permission) error {
	id, err := ids.Parse(strings.TrimPrefix(ref, "binding/"))
	if err != nil || !strings.HasPrefix(ref, "binding/") {
		return fmt.Errorf("%w: %q names no binding", errCredentialGone, ref)
	}
	r, err := queries(s.db).SystemBinding(ctx, id)
	if found, err := database.Lookup(err); err != nil {
		return err
	} else if !found {
		return fmt.Errorf("%w: binding %d does not exist", errCredentialGone, id)
	}
	if r.RevokedAt.Valid {
		return fmt.Errorf("%w: binding %d was revoked by %s: %s", errCredentialGone, id, r.RevokedBy, r.RevokedWhy)
	}
	if p.Resource.ProjectID != r.ProjectID {
		return &access.Denied{Code: "binding-scope", Reason: fmt.Sprintf("binding %d is for project %d only", id, r.ProjectID)}
	}
	for _, c := range bindingCapabilities[r.Purpose] {
		if c == p.Capability {
			return nil
		}
	}
	return &access.Denied{Code: "binding-scope", Reason: fmt.Sprintf("binding %d (%s) does not permit %s", id, r.Purpose, p.Capability)}
}
