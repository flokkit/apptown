package deployment

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/fault"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/deploy/appcontract"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/deployment/internal/deploymentsql"
	"flokkit.io/forge/internal/forge/deployment/internal/releasesql"
	"flokkit.io/forge/internal/forge/project"
)

// ConfigureDeployment selects settings; identity and project ownership come from
// the authority port, never from caller-supplied attribution or group names.
type ConfigureDeployment struct {
	ProjectID        ids.ID
	Name             string
	Target           string
	DeploymentPolicy DeploymentPolicy
	Attributes
	// StartFrom is a set in the backup store a new deployment's first
	// release fills its data from (0: it starts empty).
	StartFrom ids.ID
}
type ConfigurationContracts interface {
	CurrentContract(context.Context, string) (*appcontract.Contract, error)
}
type ConfigurationDependencies struct {
	Authority access.Authorizer
	Projects  ProjectSource
	Contracts ConfigurationContracts
}

// ConfigurationResult always describes committed settings. ContractIssue is a
// refusal when checked, or explains why compatibility could not be checked.
// A contract issue does not undo settings and is not an operation error.
type ConfigurationResult struct {
	Deployment      *Deployment
	ContractChecked bool
	ContractIssue   string
}
type ConfigurationInputError struct{ Cause error }

func (e *ConfigurationInputError) Error() string         { return e.Cause.Error() }
func (e *ConfigurationInputError) Unwrap() error         { return e.Cause }
func (e *ConfigurationInputError) FaultKind() fault.Kind { return fault.Invalid }

type OfferDeniedError struct{ Reason string }

func (e *OfferDeniedError) Error() string         { return e.Reason }
func (e *OfferDeniedError) FaultKind() fault.Kind { return fault.Forbidden }

func (s *Service) Configure(ctx context.Context, in ConfigureDeployment) (ConfigurationResult, error) {
	if err := ctx.Err(); err != nil {
		return ConfigurationResult{}, err
	}
	if s.configuration.Authority == nil || s.configuration.Contracts == nil || s.configuration.Projects == nil {
		return ConfigurationResult{}, errors.New("deployment configuration is not configured")
	}
	actor, err := s.configuration.Authority.Require(ctx, access.Permission{Capability: access.CapDeploymentBind, Resource: access.Resource{Type: "project", ID: in.ProjectID.String(), ProjectID: in.ProjectID}, Context: access.Context{Route: "deployment.configure", Consequential: true}})
	if err != nil {
		return ConfigurationResult{}, err
	}
	if actor.Principal.Name == "" {
		return ConfigurationResult{}, errors.New("configuration caller required")
	}
	policy, err := in.DeploymentPolicy.available()
	if err != nil {
		return ConfigurationResult{}, err
	}
	proj, err := s.configuration.Projects.Project(ctx, in.ProjectID)
	if err != nil {
		return ConfigurationResult{}, err
	}
	attrs := in.Attributes.WithDefaults()
	if err := validateStanding(in.Name, attrs); err != nil {
		return ConfigurationResult{}, &ConfigurationInputError{err}
	}
	// A start from a copy is a restore: checked in the copy's project and
	// in this one before anything is recorded.
	if !in.StartFrom.IsZero() {
		if err := s.checkStart(ctx, in.StartFrom, in.ProjectID, attrs.DataClass); err != nil {
			return ConfigurationResult{}, err
		}
	}
	// Source I/O is outside the transaction. Settings can be recorded before an
	// app has a readable contract; actual release admission still requires one.
	contract, contractErr := s.configuration.Contracts.CurrentContract(ctx, proj.Slug)
	if err := ctx.Err(); err != nil {
		return ConfigurationResult{}, err
	}
	return database.Transact(ctx, s.db, func(tx *database.Tx) (ConfigurationResult, error) {
		at := s.now()
		// Match release/workflow lock order: project, target, then deployment.
		if _, err := releasesql.New(tx.SQL()).LockReleaseProject(ctx, in.ProjectID); err != nil {
			return ConfigurationResult{}, err
		}
		if err := project.RefuseUnlessActiveIn(ctx, tx, in.ProjectID); err != nil {
			return ConfigurationResult{}, err
		}
		q := deploymentsql.New(tx.SQL())
		// One target lock serializes offer edits and the count-plus-bind decision.
		// Trusted landing bindings take a shared lock on the same row.
		target, err := targetRecord(q.TargetByNameForUpdate(ctx, in.Target))
		if errors.Is(err, sql.ErrNoRows) {
			return ConfigurationResult{}, ErrTargetMissing
		}
		if err != nil {
			return ConfigurationResult{}, err
		}
		if !target.Status.Offerable() {
			return ConfigurationResult{}, fmt.Errorf("%w: target %s is %s", ErrTargetConflict, target.Name, target.Status)
		}
		if err := s.refusesPlacement(ctx, in.ProjectID, target); err != nil {
			return ConfigurationResult{}, err
		}
		offer, err := s.Targets.offerFor(ctx, q, target, proj.OwnerGroup)
		if err != nil {
			return ConfigurationResult{}, err
		}
		standing := int64(0)
		if offer.Caps.MaxStanding > 0 {
			standing, err = q.StandingCount(ctx, deploymentsql.StandingCountParams{ProjectID: in.ProjectID, TargetID: target.ID, Kind: Standing.String(), Excluding: in.Name})
			if err != nil {
				return ConfigurationResult{}, err
			}
		}
		if why := offer.Permits(target, proj.Slug, attrs, int(standing)); why != "" {
			return ConfigurationResult{}, &OfferDeniedError{why}
		}
		dep, err := ensureStandingIn(ctx, tx, in.ProjectID, in.Name, target.ID, attrs, at)
		if err != nil {
			return ConfigurationResult{}, err
		}
		if !in.StartFrom.IsZero() {
			if err := s.chooseStartFrom(ctx, tx, dep, target, in.StartFrom, actor.Principal.Name); err != nil {
				return ConfigurationResult{}, err
			}
		}
		// A start still to be filled is filled by a person's deploy.
		if !dep.StartFrom.IsZero() && !dep.StartedFrom.Valid && dep.Feed == "landing" {
			return ConfigurationResult{}, fmt.Errorf("%w: %s starts from a copy, so it is fed by a person's deploy, not by every landing", ErrStartFrom, dep.Name)
		}
		if err := audit.At(at).RecordIn(ctx, tx, actor.Principal.Name, in.ProjectID, "deployment.bound", in.Name, map[string]any{"target": target.Name, "kind": target.Kind, "deploymentPolicy": policy, "feed": dep.Feed, "audience": dep.Audience, "data": dep.DataClass, "protected": dep.Protected(), "startFrom": dep.StartFrom}); err != nil {
			return ConfigurationResult{}, err
		}
		result := ConfigurationResult{Deployment: dep}
		switch {
		case contractErr != nil:
			result.ContractIssue = contractErr.Error()
		case contract == nil:
			result.ContractIssue = "no readable application contract"
		default:
			result.ContractChecked = true
			why, err := s.env.RefusesContract(ctx, target, contract)
			if err != nil {
				return ConfigurationResult{}, err
			}
			result.ContractIssue = why
		}
		return result, nil
	})
}
