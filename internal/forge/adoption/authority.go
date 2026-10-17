package adoption

import (
	"context"
	"database/sql"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/forge/identity"
)

type systemAuthorityKey struct{}

// WithSystemAuthority marks explicitly admitted project creation or validated
// uplift completion. Callers must establish that provenance before using it.
// A name in a command or an unverified transport cannot mint this authority.
func WithSystemAuthority(ctx context.Context, projectID ids.ID) context.Context {
	return context.WithValue(ctx, systemAuthorityKey{}, projectID)
}
func (s *Service) require(ctx context.Context, p authorization) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.ProjectID.IsZero() {
		return sql.ErrNoRows
	}
	var capability access.Capability
	switch p.Operation {
	case OperationSelect:
		capability = access.CapProjectConfigure
	case OperationApplyPack:
		capability = access.CapProjectWayOfWorking
	default:
		return &access.Denied{Code: access.DenyOutsideScope, Reason: "unknown Adoption operation"}
	}
	if projectID, system := ctx.Value(systemAuthorityKey{}).(ids.ID); system {
		if projectID.IsZero() || projectID != p.ProjectID {
			return &access.Denied{Code: access.DenyOutsideScope, Reason: "this system act is scoped to another project"}
		}
		return nil
	}
	caller, ok := access.CallerFrom(ctx)
	if !ok || caller.Principal.Name == "" {
		return access.ErrPrincipalRequired
	}
	if p.Actor != "" && p.Actor != caller.Receipt {
		return &access.Denied{Code: access.DenyOutsideScope, Reason: "receipt actor does not match the verified caller"}
	}
	if !caller.Verified {
		return access.ErrPrincipalRequired
	}
	if err := identity.RequireEnabledIn(ctx, s.db, caller.Principal); err != nil {
		return err
	}
	pr := caller.Principal
	if pr.Kind == "instrument" && (pr.ProjectID != p.ProjectID) {
		return &access.Denied{Code: access.DenyOutsideScope, Reason: "this session may access only its project's adoption"}
	}
	_, err := s.deps.Authority.Require(ctx, access.Permission{Capability: capability, Resource: access.Resource{Type: "project", ID: p.ProjectID.String(), ProjectID: p.ProjectID}, Context: access.Context{Route: "adoption"}})
	if err != nil {
		return err
	}
	if p.Weakens {
		_, err = s.deps.Authority.Require(ctx, access.Permission{Capability: access.CapStandardsPacks, Resource: access.Resource{Type: "instance"}, Context: access.Context{Route: "adoption"}})
	}
	return err
}
