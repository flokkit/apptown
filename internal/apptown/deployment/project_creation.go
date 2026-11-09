package deployment

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/flokkit/apptown/internal/deploy/appcontract"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/apptown/operations"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

type creationAuthority interface {
	RequireIn(context.Context, *database.Tx, access.Principal, access.Permission) (access.Principal, error)
}

// CreationPlacement selects main's target. Empty keeps the instance default;
// none creates no deployment. Contracts are known starter declarations.
type CreationPlacement struct {
	Target    string
	Contracts []*appcontract.Contract
}

// CreationTarget is a target offered for a new project's main deployment.
type CreationTarget struct {
	Area string `json:"area"`
	Name string `json:"name"`
	// Label is what people call the target: its display label, or its kind
	// when nobody changed the label from the name. Name stays the value a
	// choice records.
	Label       string `json:"label"`
	Description string `json:"description"`
	Default     bool   `json:"default"`
	Reason      string `json:"reason"`
}

// CreationTargets lists current offers, keeping incompatible choices visible.
// The caller resolves the new project's owning group before calling.
func (s *Service) CreationTargets(ctx context.Context, group string, contracts []*appcontract.Contract) ([]CreationTarget, error) {
	targets, err := targetsIn(ctx, s.db)
	if err != nil {
		return nil, err
	}
	p := project.Project{OwnerGroup: group}
	attrs := Attributes{Feed: "main", Audience: "team", DataClass: "synthetic"}
	def, err := s.landingTargetIn(ctx, s.db, p, "main", attrs)
	if err != nil && !errors.Is(err, ErrNoTarget) && !errors.Is(err, ErrReleaseRefused) {
		return nil, err
	}
	out := []CreationTarget{}
	for _, t := range targets {
		if t.Status == TargetRetired || t.reserved() != "" {
			continue
		}
		offer, err := s.Targets.offerFor(ctx, deploymentsql.New(s.db.SQL()), t, group)
		if err != nil {
			var denied *offerDeniedError
			if errors.As(err, &denied) {
				continue
			}
			return nil, err
		}
		why := offer.permits(t, "new project", attrs, 0)
		if !t.Status.Offerable() {
			why = fmt.Sprintf("%s is %s. Check the target before choosing it.", t.Name, t.Status)
		}
		if why == "" {
			why = creationContractIssue(t, contracts)
		}
		kind := DescribeTarget(t)
		description := kind.creationDescription
		if place := TargetPlace(t); place != "" {
			description += " · " + place
		}
		// A target's label defaults to its name; a key like built-in-docker
		// is not what people call it, so an unchanged label reads as its kind.
		label := t.DisplayLabel
		if label == "" || label == t.Name {
			label = kind.Title
		}
		out = append(out, CreationTarget{Area: TargetFeatureArea(t.Kind), Name: t.Name, Label: label, Description: description, Default: def != nil && t.ID == def.ID, Reason: why})
	}
	return out, nil
}

func creationContractIssue(t *Target, contracts []*appcontract.Contract) string {
	for _, c := range contracts {
		if why := t.refusesContract(c); why != "" {
			return why
		}
	}
	return ""
}

// InitializeProjectIn gives a newly created project its follows-main placement.
// It joins project creation, after ownership has been recorded. No default
// target or no deploy authority means no placement. Agent credentials may
// create an unconfirmed placement but never supply standing human authority.
// Read or write failures abort creation rather than silently losing the default.
func (s *Service) InitializeProjectIn(ctx context.Context, tx *database.Tx, projectID ids.ID, choice CreationPlacement) error {
	if choice.Target == "none" {
		return nil
	}
	caller, ok := access.CallerFrom(ctx)
	if !ok || !caller.Verified || caller.Principal.Kind != "user" {
		if choice.Target != "" {
			return access.ErrPrincipalRequired
		}
		return nil
	}
	if s.creationAuthority == nil {
		return access.ErrAuthorityUnavailable
	}
	p, err := project.ByIDIn(ctx, tx, projectID)
	if err != nil {
		return err
	}
	attrs := Attributes{Feed: "main", Audience: "team", DataClass: "synthetic"}
	var target *Target
	if choice.Target == "" {
		target, err = s.landingTargetIn(ctx, tx, p, "main", attrs)
		if errors.Is(err, ErrNoTarget) || errors.Is(err, ErrReleaseRefused) {
			return nil
		}
	} else {
		target, err = targetRecord(deploymentsql.New(tx.SQL()).TargetByNameForUpdate(ctx, choice.Target))
		if errors.Is(err, sql.ErrNoRows) {
			return errTargetMissing
		}
	}
	if err != nil {
		return err
	}
	if !target.Status.Offerable() {
		return fmt.Errorf("%w: target %s is %s", errTargetConflict, target.Name, target.Status)
	}
	if target.reserved() != "" {
		return refused("%s is kept for %s", target.Name, target.reserved())
	}
	offer, err := s.Targets.offerFor(ctx, deploymentsql.New(tx.SQL()), target, p.OwnerGroup)
	if err != nil {
		return err
	}
	if why := offer.permits(target, p.Slug, attrs, 0); why != "" {
		return &offerDeniedError{why}
	}
	if why := creationContractIssue(target, choice.Contracts); why != "" {
		return &configurationInputError{errors.New(why)}
	}
	var authority *json.RawMessage
	if !access.AgentCredential(caller.Principal) {
		resource, err := project.AuthorityResourceIn(ctx, tx, projectID)
		if err != nil {
			return err
		}
		setter, err := s.creationAuthority.RequireIn(ctx, tx, caller.Principal, access.Permission{Capability: access.CapDeploymentDeploy, Resource: resource, Context: access.Context{Consequential: true, Route: "deployment.confirm-feed"}})
		if err != nil {
			var denied *access.Denied
			if errors.As(err, &denied) && choice.Target == "" {
				return nil
			}
			return err
		}
		raw, err := confirmedFeedAuthority(access.Caller{Principal: setter, Verified: true})
		if err != nil {
			return err
		}
		captured := json.RawMessage(raw)
		authority = &captured
	}
	dep, err := ensureStandingIn(ctx, tx, projectID, "main", target.ID, attrs, s.now())
	if err != nil {
		return err
	}
	if authority != nil {
		n, err := deploymentsql.New(tx.SQL()).ConfirmMainFeed(ctx, deploymentsql.ConfirmMainFeedParams{ID: dep.ID, Revision: dep.FeedRevision, Authority: authority})
		if err != nil {
			return err
		}
		if n != 1 {
			return errFeedMoved
		}
	}
	return audit.At(s.now()).RecordIn(ctx, tx, caller.Receipt, projectID, "deployment.bound", "main", map[string]any{"target": target.Name, "feed": "main", "audience": attrs.Audience, "data": attrs.DataClass})
}

func confirmedFeedAuthority(caller access.Caller) (json.RawMessage, error) {
	if !caller.Verified || caller.Principal.Kind != "user" || access.AgentCredential(caller.Principal) {
		return nil, errReleaseAuthority
	}
	raw, err := json.Marshal(operations.AuthorityOf(caller.Principal))
	if err != nil {
		return nil, err
	}
	if _, err := releasePrincipal(raw); err != nil {
		return nil, err
	}
	return raw, nil
}
