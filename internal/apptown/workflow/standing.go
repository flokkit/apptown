package workflow

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

type StandingDependencies struct {
	Authority    access.Authorizer
	Identity     *identity.Service
	Procedures   StandingProcedures
	Destinations StandingDestinations
	Now          func() time.Time
}

// StandingRules manages explicit human consent. It has no dispatcher or release
// effect collaborator and cannot turn a saved rule into an executable approval.
type StandingRules struct {
	db   *database.DB
	deps StandingDependencies
	clocked
}

func NewStandingRules(db *database.DB, deps StandingDependencies) StandingRules {
	return StandingRules{db: db, deps: deps, clocked: clocked{deps.Now}}
}

type StandingStatus struct {
	Rule                StandingRevision `json:"rule"`
	Eligible            bool             `json:"eligible"`
	Reason              string           `json:"reason"`
	ProcessingAvailable bool             `json:"processingAvailable"`
}

func (s StandingRules) human(ctx context.Context, q database.Queryer) (access.Actor, error) {
	c, ok := access.CallerFrom(ctx)
	if !ok || !c.Verified || c.Principal.Kind != "user" || c.Principal.ID.IsZero() || !c.Principal.Actor().Assents() {
		return access.Actor{}, fault.New(fault.Forbidden, "standing consent requires a verified person acting directly in their browser or command line")
	}
	if err := identity.RequireEnabledIn(ctx, q, c.Principal); err != nil {
		return access.Actor{}, err
	}
	return c.Principal.Actor(), nil
}
func (s StandingRules) require(ctx context.Context, capability access.Capability, resource access.Resource) error {
	if s.deps.Authority == nil {
		return access.ErrAuthorityUnavailable
	}
	_, err := s.deps.Authority.Require(ctx, access.Permission{Capability: capability, Resource: resource, Context: access.Context{Route: "standing-approval"}})
	return err
}
func (s StandingRules) manage(ctx context.Context, q database.Queryer, in StandingDefinition) error {
	if in.Administrative {
		if err := s.require(ctx, access.CapInstanceStandingApproval, access.Resource{Type: "instance"}); err != nil {
			return err
		}
	}
	for _, p := range in.Projects {
		r, err := project.AuthorityResourceIn(ctx, q, p.ProjectID)
		if err != nil {
			return err
		}
		if !in.Administrative {
			if err = s.require(ctx, access.CapProjectStandingApproval, r); err != nil {
				return err
			}
		}
	}
	return nil
}
func (s StandingRules) question(ctx context.Context, q database.Queryer, in StandingDefinition, actor access.Actor, admission bool) (StandingQuestion, error) {
	if s.deps.Procedures == nil || s.deps.Identity == nil {
		return StandingQuestion{}, access.ErrAuthorityUnavailable
	}
	var meanings []StandingProjectMeaning
	permission := func(capability access.Capability, r access.Resource) error {
		if admission {
			return s.require(ctx, capability, r)
		}
		_, err := s.deps.Identity.StandingIssuerAllowsIn(ctx, q, actor.Principal, access.Permission{Capability: capability, Resource: r})
		return err
	}
	for _, p := range in.Projects {
		if err := project.RefuseUnlessActive(ctx, q, p.ProjectID); err != nil {
			return StandingQuestion{}, err
		}
		r, err := project.AuthorityResourceIn(ctx, q, p.ProjectID)
		if err != nil {
			return StandingQuestion{}, err
		}
		independent, err := workflowsql.New(q.SQL()).StandingIndependentApproval(ctx, p.ProjectID)
		if err != nil {
			return StandingQuestion{}, err
		}
		if independent {
			return StandingQuestion{}, fault.New(fault.Conflict, "this project's independent-approval policy requires a person to judge each change")
		}
		proc, err := s.deps.Procedures.StandingProcedure(ctx, q, p.ProjectID, p.Workflow)
		if err != nil {
			return StandingQuestion{}, err
		}
		for _, purpose := range []StandingPurpose{proc.Acceptance, proc.Landing} {
			if purpose.Workflow == "" || purpose.Version <= 0 || purpose.Step == "" || strings.TrimSpace(purpose.Means) == "" {
				return StandingQuestion{}, fault.New(fault.Conflict, "the selected workflow does not declare a supported standing approval purpose")
			}
			if err = requireApprover(ctx, q, operationPermission{ProjectID: p.ProjectID}, purpose.Approvers, actor.Name, actor.Principal, s.now()); err != nil {
				return StandingQuestion{}, err
			}
		}
		for _, capability := range []access.Capability{access.CapChangeApprove, access.CapChangeLand} {
			if err = permission(capability, r); err != nil {
				return StandingQuestion{}, err
			}
		}
		for _, e := range p.Environments {
			if s.deps.Destinations == nil {
				return StandingQuestion{}, access.ErrAuthorityUnavailable
			}
			dest, err := s.deps.Destinations.StandingDestination(ctx, q, p.ProjectID, e)
			if err != nil {
				return StandingQuestion{}, err
			}
			if dest.PromotionRequired && !e.Promotion {
				return StandingQuestion{}, fault.New(fault.Conflict, "the selected environment requires explicit promotion consent")
			}
			if err = permission(access.CapDeploymentDeploy, dest.Resource); err != nil {
				return StandingQuestion{}, err
			}
			if e.Promotion {
				pr := dest.PromotionResource
				if err = permission(access.CapReleasePromote, pr); err != nil {
					return StandingQuestion{}, err
				}
			}
		}
		meanings = append(meanings, StandingProjectMeaning{ProjectID: p.ProjectID, Procedure: proc})
	}
	return standingQuestion(in, meanings)
}
func (s StandingRules) Preview(ctx context.Context, in StandingDefinition) (StandingQuestion, error) {
	in, err := normalizeStanding(in)
	if err != nil {
		return StandingQuestion{}, err
	}
	actor, err := s.human(ctx, s.db)
	if err != nil {
		return StandingQuestion{}, err
	}
	if err = s.manage(ctx, s.db, in); err != nil {
		return StandingQuestion{}, err
	}
	return s.question(ctx, s.db, in, actor, true)
}
func lockStandingProjects(ctx context.Context, tx *database.Tx, sets ...StandingDefinition) error {
	var projects []ids.ID
	for _, set := range sets {
		for _, p := range set.Projects {
			projects = append(projects, p.ProjectID)
		}
	}
	slices.SortFunc(projects, func(a, b ids.ID) int { return strings.Compare(a.String(), b.String()) })
	for _, id := range slices.Compact(projects) {
		if _, err := workflowsql.New(tx.SQL()).ProjectForUpdate(ctx, id); err != nil {
			return database.Missing(err)
		}
	}
	return nil
}
func (s StandingRules) Enable(ctx context.Context, in StandingCommand) (StandingRevision, error) {
	definition, err := normalizeStanding(in.Definition)
	if err != nil {
		return StandingRevision{}, err
	}
	if in.ShownDigest == "" || in.ExpectedRevision < 0 || in.RuleID.IsZero() != (in.ExpectedRevision == 0) {
		return StandingRevision{}, fault.New(fault.Invalid, "a new rule needs no ID; editing requires its ID, expected revision and shown meaning")
	}
	actor, err := s.human(ctx, s.db)
	if err != nil {
		return StandingRevision{}, err
	}
	var old StandingRevision
	if !in.RuleID.IsZero() {
		old, err = currentStanding(ctx, s.db, in.RuleID)
		if err != nil {
			return old, err
		}
		if old.Revision != in.ExpectedRevision {
			return old, errStandingRevision
		}
		if err = s.manage(ctx, s.db, old.Question.Definition); err != nil {
			return old, err
		}
	}
	return database.Transact(ctx, s.db, func(tx *database.Tx) (StandingRevision, error) {
		if err := lockStandingProjects(ctx, tx, definition, old.Question.Definition); err != nil {
			return StandingRevision{}, err
		}
		if err := s.manage(ctx, tx, definition); err != nil {
			return StandingRevision{}, err
		}
		actor, err = s.human(ctx, tx)
		if err != nil {
			return StandingRevision{}, err
		}
		if !in.RuleID.IsZero() {
			n, err := workflowsql.New(tx.SQL()).LockStandingRule(ctx, in.RuleID)
			if err != nil {
				return StandingRevision{}, err
			}
			if n != in.ExpectedRevision {
				return StandingRevision{}, errStandingRevision
			}
			if err = s.manage(ctx, tx, old.Question.Definition); err != nil {
				return StandingRevision{}, err
			}
		}
		question, err := s.question(ctx, tx, definition, actor, true)
		if err != nil {
			return StandingRevision{}, err
		}
		if question.Digest != in.ShownDigest {
			return StandingRevision{}, errStandingRevision
		}
		at := s.now()
		id := in.RuleID
		if id.IsZero() {
			id = ids.New(at)
		}
		r := StandingRevision{RuleID: id, Revision: in.ExpectedRevision + 1, Enabled: true, Question: question, Issuer: actor, EnabledAt: at, ChangedBy: actor, ChangedAt: at, Reason: "explicit human consent"}
		if err = writeStandingRevision(ctx, tx, r, in.RuleID.IsZero()); err != nil {
			return StandingRevision{}, err
		}
		return r, nil
	})
}
func (s StandingRules) readable(ctx context.Context, q database.Queryer, in StandingDefinition) error {
	if in.Administrative {
		err := s.require(ctx, access.CapInstanceStandingApproval, access.Resource{Type: "instance"})
		if err == nil {
			return nil
		}
		if !standingRefusal(err) {
			return err
		}
	}
	for _, p := range in.Projects {
		r, err := project.AuthorityResourceIn(ctx, q, p.ProjectID)
		if err != nil {
			return err
		}
		if err = s.require(ctx, access.CapProjectRead, r); err != nil {
			return err
		}
	}
	return nil
}
func (s StandingRules) History(ctx context.Context, id ids.ID) ([]StandingRevision, error) {
	current, err := currentStanding(ctx, s.db, id)
	if err != nil {
		return nil, err
	}
	if err = s.readable(ctx, s.db, current.Question.Definition); err != nil {
		return nil, err
	}
	rows, err := workflowsql.New(s.db.SQL()).StandingHistory(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]StandingRevision, 0, len(rows))
	for _, row := range rows {
		r, err := readStandingRevision(row)
		if err != nil {
			return nil, err
		}
		if err = s.readable(ctx, s.db, r.Question.Definition); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}
func standingRefusal(err error) bool {
	var denied *access.Denied
	var decision *DecisionDenied
	return errors.As(err, &denied) || errors.As(err, &decision) || fault.KindOf(err) == fault.Forbidden || fault.KindOf(err) == fault.Conflict || fault.KindOf(err) == fault.NotFound || fault.KindOf(err) == fault.Unprocessable || errors.Is(err, access.ErrPrincipalRequired)
}
func (s StandingRules) status(ctx context.Context, r StandingRevision) (StandingStatus, error) {
	status := StandingStatus{Rule: r, Reason: "automatic processing is not available yet"}
	if !r.Enabled {
		status.Reason = r.Reason
		return status, nil
	}
	name, err := identity.PersonNameIn(ctx, s.db, r.Issuer.Principal)
	if err != nil {
		return status, err
	}
	if name == "" {
		status.Reason = "the enabling person's account no longer exists"
		return status, nil
	}
	current := r.Issuer
	current.Name = name
	q, err := s.question(ctx, s.db, r.Question.Definition, current, false)
	if err != nil {
		if standingRefusal(err) {
			status.Reason = err.Error()
			return status, nil
		}
		return status, err
	}
	if q.Digest != r.Question.Digest {
		status.Reason = "the governing meaning changed; a new human confirmation is required"
		return status, nil
	}
	status.Eligible = true
	return status, nil
}
func (s StandingRules) List(ctx context.Context, projectID ids.ID) ([]StandingStatus, error) {
	if projectID.IsZero() {
		if err := s.require(ctx, access.CapInstanceStandingApproval, access.Resource{Type: "instance"}); err != nil {
			return nil, err
		}
	} else {
		r, err := project.AuthorityResourceIn(ctx, s.db, projectID)
		if err != nil {
			return nil, err
		}
		if err = s.require(ctx, access.CapProjectRead, r); err != nil {
			return nil, err
		}
	}
	rows, err := workflowsql.New(s.db.SQL()).CurrentStandingRules(ctx)
	if err != nil {
		return nil, err
	}
	out := []StandingStatus{}
	for _, row := range rows {
		r, err := readStandingRevision(row)
		if err != nil {
			return nil, err
		}
		if !projectID.IsZero() && !slices.ContainsFunc(r.Question.Definition.Projects, func(p StandingProject) bool { return p.ProjectID == projectID }) {
			continue
		}
		if err = s.readable(ctx, s.db, r.Question.Definition); err != nil {
			if standingRefusal(err) {
				continue
			}
			return nil, err
		}
		status, err := s.status(ctx, r)
		if err != nil {
			return nil, err
		}
		out = append(out, status)
	}
	return out, nil
}
func (s StandingRules) Disable(ctx context.Context, id ids.ID, expected int64) (StandingRevision, error) {
	actor, err := s.human(ctx, s.db)
	if err != nil {
		return StandingRevision{}, err
	}
	old, err := currentStanding(ctx, s.db, id)
	if err != nil {
		return old, err
	}
	if old.Revision != expected {
		return old, errStandingRevision
	}
	authorize := func(q database.Queryer) error {
		// Revocation accepts either management scope, regardless of the scope
		// originally used to create the rule. Neither path grants assent.
		err := s.require(ctx, access.CapInstanceStandingApproval, access.Resource{Type: "instance"})
		if err == nil {
			return nil
		}
		if !standingRefusal(err) {
			return err
		}
		projects := old.Question.Definition
		projects.Administrative = false
		err = s.manage(ctx, q, projects)
		if err == nil {
			return nil
		}
		if !standingRefusal(err) {
			return err
		}
		if old.Issuer.Principal != actor.Principal {
			return err
		}
		_, eligible := s.question(ctx, q, old.Question.Definition, actor, true)
		return eligible
	}
	if err = authorize(s.db); err != nil {
		return old, err
	}
	return database.Transact(ctx, s.db, func(tx *database.Tx) (StandingRevision, error) {
		if err := lockStandingProjects(ctx, tx, old.Question.Definition); err != nil {
			return old, err
		}
		if err := authorize(tx); err != nil {
			return old, err
		}
		n, err := workflowsql.New(tx.SQL()).LockStandingRule(ctx, id)
		if err != nil {
			return old, err
		}
		if n != expected {
			return old, errStandingRevision
		}
		if !old.Enabled {
			return old, nil
		}
		old.Revision++
		old.Enabled = false
		old.ChangedBy = actor
		old.ChangedAt = s.now()
		old.Reason = "disabled by " + actor.Name
		if err = writeStandingRevision(ctx, tx, old, false); err != nil {
			return StandingRevision{}, err
		}
		return old, nil
	})
}
