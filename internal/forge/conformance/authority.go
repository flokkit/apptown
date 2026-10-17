package conformance

import (
	"context"
	"fmt"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/identity"
)

// RunScope resolves a run's recorded project afresh. Its owner supplies the
// binding; an actor label cannot create one.
type RunScope interface {
	ProjectID(context.Context) (ids.ID, error)
}

// AdoptionScope belongs to the adoption owner: the revision of one project's
// way of working its transaction just recorded. Current says whether that
// revision is still the project's current one. Under it the system may
// request a standing review of that project and nothing else.
type AdoptionScope interface {
	ProjectID() ids.ID
	Current(context.Context) (bool, error)
}

// VerificationScope belongs to the remediation owner. Review requires the exact
// current observation; reading already-admitted work requires its project binding.
type VerificationScope interface {
	ProjectID() ids.ID
	Current(context.Context) (bool, error)
	RequireRead(context.Context) error
}
type scopeKey struct{}
type systemScope struct {
	run          RunScope
	verification VerificationScope
	adoption     AdoptionScope
}

func WithRun(ctx context.Context, run RunScope) context.Context {
	return context.WithValue(ctx, scopeKey{}, systemScope{run: run})
}
func WithVerification(ctx context.Context, scope VerificationScope) context.Context {
	return context.WithValue(ctx, scopeKey{}, systemScope{verification: scope})
}

// WithAdoption is the context in which an adoption change requests its
// project's review; the adoption owner builds the scope for the revision it
// recorded.
func WithAdoption(ctx context.Context, scope AdoptionScope) context.Context {
	return context.WithValue(ctx, scopeKey{}, systemScope{adoption: scope})
}

type commandAuthority struct {
	db      *database.DB
	checked access.Authorizer
}

func (a commandAuthority) Actor(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if a.checked == nil {
		return "", ErrCallerRequired
	}

	caller, _ := ctx.Value(scopeKey{}).(systemScope)
	if caller.run != nil || caller.verification != nil || caller.adoption != nil {
		return "forge-system", nil
	}
	c, ok := access.CallerFrom(ctx)
	if !ok || !c.Verified || c.Principal.Name == "" {
		return "", ErrCallerRequired
	}
	return c.Principal.Name, nil
}
func (a commandAuthority) Require(ctx context.Context, permission Permission) (string, error) {
	who, err := a.Actor(ctx)
	if err != nil {
		return "", err
	}
	caller, _ := ctx.Value(scopeKey{}).(systemScope)
	deny := func(reason string) (string, error) {
		return "", &access.Denied{Code: access.DenyOutsideScope, Reason: reason}
	}
	if caller.adoption != nil {
		recorded := caller.adoption
		if permission.Action != RunReview || len(permission.Projects) != 1 || recorded.ProjectID().IsZero() || permission.Projects[0] != recorded.ProjectID() {
			return deny("an adoption change may request only its own project's review")
		}
		current, err := recorded.Current(ctx)
		if err != nil {
			return "", err
		}
		if !current {
			return deny("this adoption revision is no longer the project's current one")
		}
		return who, nil
	}
	if caller.verification != nil {
		item := caller.verification
		if permission.Action == RunReview {
			if len(permission.Projects) != 1 || permission.Projects[0] != item.ProjectID() {
				return deny("a remediation may review only its project")
			}
			current, err := item.Current(ctx)
			if err != nil {
				return "", err
			}
			if !current {
				return deny("this remediation observation is no longer awaiting verification")
			}
			return who, nil
		}
		if permission.Action != ReadProject || permission.ProjectID != item.ProjectID() {
			return deny("a remediation may read only its project")
		}
		if err := item.RequireRead(ctx); err != nil {
			return "", err
		}
		return who, nil
	}
	if caller.run != nil {
		if permission.Action != ReadProject {
			return deny("a run may read only its project's conformance")
		}
		projectID, err := caller.run.ProjectID(ctx)
		if err != nil {
			return "", err
		}
		if projectID != permission.ProjectID {
			return deny("the run belongs to another project")
		}
		return who, nil
	}
	asked, _ := access.CallerFrom(ctx)
	pr := asked.Principal
	var capability access.Capability
	switch permission.Action {
	case ReadFleet, ReadProject:
		capability = access.CapConformanceRead
	case RunReview, RunWhatIf:
		capability = access.CapConformanceRev
	case ClearHistory:
		capability = access.CapConformanceClear
	case ConfirmFlag:
		capability = access.CapConformanceAI
	case CreateException:
		capability = access.CapExceptionApprove
	case RevokeException:
		capability = access.CapExceptionRevoke
	default:
		return deny("unknown conformance operation")
	}
	// Instruments must never turn an instance read into access to other projects.
	if pr.Kind == "instrument" && (permission.ProjectID.IsZero() || permission.ProjectID != pr.ProjectID) {
		return deny("this session may read only its own project")
	}
	if err := identity.RequireEnabledIn(ctx, a.db, pr); err != nil {
		return "", err
	}
	resource := access.Resource{Type: "instance"}
	if !permission.ProjectID.IsZero() {
		resource = access.Resource{Type: "project", ID: permission.ProjectID.String(), ProjectID: permission.ProjectID}
	}
	_, err = a.checked.Require(ctx, access.Permission{Capability: capability, Resource: resource, Context: access.Context{Route: fmt.Sprintf("conformance:%d", permission.Action)}})
	if err != nil {
		return "", err
	}
	return who, nil
}
