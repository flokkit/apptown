package workflow

import (
	"context"
	"errors"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

type StandingReach struct{ Repository bool }
type standingPortfolio interface {
	StandingReach(context.Context, database.Queryer, ids.ID) (StandingReach, error)
	StandingHold(context.Context, database.Queryer, ids.ID) (string, error)
}
type packRuleEnabler interface {
	PackRuleEnabledIn(context.Context, *database.Tx, StandingRevision) error
}
type StandingDependencies struct {
	Packs      packRuleEnabler
	Portfolio  standingPortfolio
	Adoptions  standingAdoptions
	Inspector  standingInspector
	Presence   standingPresence
	Authority  access.Authorizer
	Identity   *identity.Service
	Procedures standingProcedures
	Now        func() time.Time
}

// StandingRules owns Janitor choices and effect-time eligibility.
type StandingRules struct {
	db   *database.DB
	deps StandingDependencies
	clocked
}

func NewStandingRules(db *database.DB, deps StandingDependencies) StandingRules {
	return StandingRules{db: db, deps: deps, clocked: clocked{deps.Now}}
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

// Current reads the saved choices and their issuer.
func (s StandingRules) Current(ctx context.Context) (*StandingRevision, error) {
	if err := s.require(ctx, access.CapInstanceStandingApproval, access.Resource{Type: "instance"}); err != nil {
		return nil, err
	}
	return latestStanding(ctx, s.db)
}

func (s StandingRules) janitorChoices(ctx context.Context) (*StandingRevision, error) {
	if err := s.require(ctx, access.CapProjectRead, access.Resource{Type: "instance"}); err != nil {
		return nil, err
	}
	return latestStanding(ctx, s.db)
}

// Save records a verified person's choices at the expected revision.
func (s StandingRules) Save(ctx context.Context, in JanitorChoicesCommand) (StandingRevision, error) {
	if err := in.Choices.validate(); err != nil {
		return StandingRevision{}, err
	}
	if in.ExpectedRevision < 0 {
		return StandingRevision{}, fault.New(fault.Invalid, "expected revision must not be negative")
	}
	return database.Transact(ctx, s.db, func(tx *database.Tx) (StandingRevision, error) {
		actor, err := s.human(ctx, tx)
		if err != nil {
			return StandingRevision{}, err
		}
		if err = s.require(ctx, access.CapInstanceStandingApproval, access.Resource{Type: "instance"}); err != nil {
			return StandingRevision{}, err
		}
		// Serialize the first save as well as revisions of an existing head.
		if err = workflowsql.New(tx.SQL()).LockJanitorChoices(ctx); err != nil {
			return StandingRevision{}, err
		}
		old, err := latestStanding(ctx, tx)
		if err != nil {
			return StandingRevision{}, err
		}
		at := s.now()
		r := StandingRevision{RuleID: ids.New(at), Revision: 1, Choices: in.Choices, Enabled: in.Choices.enabled(), Issuer: actor, At: at}
		if old != nil {
			if old.Revision != in.ExpectedRevision {
				return StandingRevision{}, errStandingRevision
			}
			if _, err = workflowsql.New(tx.SQL()).LockStandingRule(ctx, old.RuleID); err != nil {
				return StandingRevision{}, err
			}
			r.RuleID, r.Revision = old.RuleID, old.Revision+1
		} else if in.ExpectedRevision != 0 {
			return StandingRevision{}, errStandingRevision
		}
		if err = writeStandingRevision(ctx, tx, r, old == nil); err != nil {
			return StandingRevision{}, err
		}
		if r.Choices.Packs && s.deps.Packs != nil {
			if err = s.deps.Packs.PackRuleEnabledIn(ctx, tx, r); err != nil {
				return StandingRevision{}, err
			}
		}
		return r, nil
	})
}
func (s StandingRules) History(ctx context.Context) ([]StandingRevision, error) {
	if err := s.require(ctx, access.CapInstanceStandingApproval, access.Resource{Type: "instance"}); err != nil {
		return nil, err
	}
	rows, err := workflowsql.New(s.db.SQL()).StandingHistory(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]StandingRevision, 0, len(rows))
	for _, row := range rows {
		r, err := readStandingRevision(row)
		if err != nil {
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
