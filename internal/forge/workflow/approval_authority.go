package workflow

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
	"flokkit.io/forge/internal/ids"
)

// Eligibility is a checked policy observation. Named/group/role expressions use
// current facts, and unavailable facts never become an empty author or membership.
// The command separately requires the capability and locks its Work snapshot.
// Grants and memberships are judged live as of now, the forge's clock.
func requireApprover(ctx context.Context, db database.Queryer, scope operationPermission, approvers []string, user string, voter ids.ID, now time.Time) error {
	if base, _, ok := strings.Cut(user, " via "); ok {
		user = base
	}
	// Root, groups and roles are the voter's: by principal when known, so a
	// rename keeps them and a later account with the old name lends none.
	who := access.Principal{ID: voter, Kind: "user", Name: user}
	root, err := identity.PrincipalRootIn(ctx, db, who, now)
	if err != nil {
		return err
	}
	policy, err := workflowsql.New(db.SQL()).ApproverEligibility(ctx, workflowsql.ApproverEligibilityParams{ProjectID: scope.ProjectID, ChangeID: scope.ChangeID, Root: root})
	if err != nil {
		return err
	}
	defaults, independent, author := policy.DefaultApprovers, policy.RequireIndependentApproval, policy.Author
	if root {
		return nil
	}
	if base, _, ok := strings.Cut(author, " via "); ok {
		author = base
	}
	// Independence compares people, not names: the opener's principal with
	// the voter's. An opener not resolved to a person cannot be judged
	// independent of anyone, unless it was the forge itself.
	if independent && !scope.ChangeID.IsZero() {
		switch {
		case !policy.AuthorPrincipal.IsZero() && policy.AuthorPrincipal == voter:
			return &DecisionDenied{Code: access.DenyNotEligible, Reason: fmt.Sprintf("%s opened this change and may not approve it (this project requires independent approval)", user)}
		case policy.AuthorPrincipal.IsZero() && author == user:
			return &DecisionDenied{Code: access.DenyNotEligible, Reason: fmt.Sprintf("%s opened this change and may not approve it (this project requires independent approval)", user)}
		case policy.AuthorPrincipal.IsZero() && author != "" && !identity.SystemActor(author):
			return &DecisionDenied{Code: access.DenyNotEligible, Reason: fmt.Sprintf("this change's opener (%s) is not recorded as a person, so this project's independent approval cannot be judged; an administrator may approve it", author)}
		}
	}
	expr := slices.Clone(approvers)
	if len(expr) == 0 {
		for _, item := range strings.Split(defaults, ",") {
			if item = strings.TrimSpace(item); item != "" {
				expr = append(expr, item)
			}
		}
	}
	if len(expr) == 0 {
		return &DecisionDenied{Code: access.DenyNotEligible, Reason: "nobody is named as an approver for this step, and the project has no default approvers"}
	}
	// Immediate named/built-in matches need no optional membership/role reads.
	for _, item := range expr {
		if item == user || item == "group:"+identity.GroupAllUsers && user != "" && user != "anonymous" {
			return nil
		}
	}
	var groups []string
	var grants []access.Grant
	needGroups, needRoles := false, false
	for _, item := range expr {
		needGroups = needGroups || strings.HasPrefix(item, "group:")
		needRoles = needRoles || strings.HasPrefix(item, "role:")
	}
	if needGroups {
		if voter.IsZero() {
			groups, err = identity.ApproverGroupsIn(ctx, db, user, now)
		} else {
			groups, err = identity.PrincipalGroupsIn(ctx, db, voter, now)
		}
		if err != nil {
			return err
		}
	}
	if needRoles {
		grants, err = identity.ActiveGrantsIn(ctx, db, who, now)
		if err != nil {
			return err
		}
	}
	for _, item := range expr {
		if group, ok := strings.CutPrefix(item, "group:"); ok && slices.Contains(groups, group) {
			return nil
		}
		if role, ok := strings.CutPrefix(item, "role:"); ok && hasRole(grants, role, access.ScopeProject, scope.ProjectID.String()) {
			return nil
		}
	}
	return &DecisionDenied{Code: access.DenyNotEligible, Reason: fmt.Sprintf("%s is not an approver for this step (%s)", user, strings.Join(expr, ", "))}
}
func hasRole(grants []access.Grant, role string, scope access.ScopeKind, scopeID string) bool {
	for _, g := range grants {
		if g.RoleName == role && g.Capability == "" && g.ScopeType == scope && g.ScopeID == scopeID {
			return true
		}
	}
	return false
}
