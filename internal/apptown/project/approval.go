package project

import (
	"context"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/project/internal/projectsql"
	"github.com/flokkit/apptown/internal/database"
)

// SetIndependentApproval changes the project's non-root author approval rule.
// Permission precedes the transaction; policy and its receipt commit together.
func (s *Projects) SetIndependentApproval(ctx context.Context, id ids.ID, required bool) error {
	caller, err := s.authority.Require(ctx, access.Permission{Capability: access.CapProjectConfigure, Resource: access.Resource{Type: "project", ID: id.String(), ProjectID: id}, Context: access.Context{Route: "project.approval-policy", Consequential: true}})
	if err != nil {
		return err
	}
	return s.db.InTx(ctx, func(tx *database.Tx) error {
		before, err := queries(tx).ProjectByIDForUpdate(ctx, id)
		if err != nil {
			return err
		}
		saved, err := queries(tx).SetIndependentApproval(ctx, projectsql.SetIndependentApprovalParams{ID: id, RequireIndependentApproval: required})
		if err != nil {
			return err
		}
		return audit.At(s.now()).RecordIn(ctx, tx, caller.Receipt, id, "project.approval-policy", id.String(), struct {
			Before   bool `json:"before"`
			Required bool `json:"requireIndependentApproval"`
		}{before.RequireIndependentApproval, saved.RequireIndependentApproval})
	})
}
