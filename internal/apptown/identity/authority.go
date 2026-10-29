package identity

import (
	"context"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/database"
)

var _ access.Authority = (*Service)(nil)

// Require returns the supplied identity only after current policy permits the
// operation. A consequential denial is recorded in the activity log, and one
// that cannot be recorded fails the operation.
func (s *Service) Require(ctx context.Context, pr access.Principal, p access.Permission) (access.Principal, error) {
	if pr.Name == "" {
		return access.Principal{}, access.ErrPrincipalRequired
	}
	now := s.now()
	dec, err := s.explain(ctx, pr, access.DelegationOf(pr), p, now)
	if err != nil {
		return access.Principal{}, err
	}
	if p.Context.Consequential {
		if err = s.denials.record(ctx, pr, p, dec, now); err != nil {
			return access.Principal{}, err
		}
	}
	if !dec.Allow {
		return access.Principal{}, &access.Denied{Code: dec.Code, Reason: dec.Reason}
	}
	return pr, nil
}

// Decide is the decision on an act: a denial is recorded in the activity log,
// and a fact that cannot be read or a denial that cannot be recorded is an
// error, never an answer. Route guards and tool guards use it.
func (s *Service) Decide(ctx context.Context, pr access.Principal, p access.Permission) (access.Decision, error) {
	now := s.now()
	dec, err := s.explain(ctx, pr, access.DelegationOf(pr), p, now)
	if err != nil {
		return access.Decision{}, err
	}
	if err = s.denials.record(ctx, pr, p, dec, now); err != nil {
		return access.Decision{}, err
	}
	return dec, nil
}

// Explain answers a question (a menu, a holding, a visibility filter)
// without recording a denial; unavailable facts are errors.
func (s *Service) Explain(ctx context.Context, pr access.Principal, p access.Permission) (access.Decision, error) {
	return s.explain(ctx, pr, access.DelegationOf(pr), p, s.now())
}

func (s *Service) explain(ctx context.Context, pr access.Principal, d access.Channel, p access.Permission, now time.Time) (access.Decision, error) {
	return explainWithFacts(ctx, pr, d, p, &serviceFacts{ctx: ctx, q: s.db, at: now})
}

func explainWithFacts(ctx context.Context, pr access.Principal, d access.Channel, p access.Permission, f *serviceFacts) (access.Decision, error) {
	if err := ctx.Err(); err != nil {
		return access.Decision{}, err
	}
	if pr.Instrument == "console" {
		disabled := f.Disabled(access.Principal{ID: pr.ID, Kind: "user", Name: pr.Person})
		if f.err != nil {
			return access.Decision{}, f.err
		}
		if disabled {
			return access.Decision{Code: access.DenyAccountDisabled, Reason: pr.Person + "'s account is disabled or does not exist"}, nil
		}
	}
	dec := access.Decide(pr, d, p.Capability, p.Resource, p.Context, f)
	if f.err != nil {
		return access.Decision{}, f.err
	}
	return dec, nil
}

func (r denialRecorder) record(ctx context.Context, pr access.Principal, p access.Permission, dec access.Decision, at time.Time) error {
	return recordDenial(ctx, r.db, pr, p.Capability, p.Resource, p.Context, dec, at)
}

// access.Facts are read only when the pure decision reaches them. Remember errors
// separately because access.Facts predates checked service operations. A source
// retains each fact on first read for this operation only. It is never shared
// with a later request or command; project scope remains supplied per decision.
// Grants are judged live as of at, the operation's clock.
type serviceFacts struct {
	ctx      context.Context
	q        database.Queryer
	at       time.Time
	disabled map[string]bool
	grants   map[string][]access.Grant
	err      error
}

func (f *serviceFacts) failed(err error) {
	if err != nil && f.err == nil {
		f.err = err
	}
}
func (f *serviceFacts) Disabled(pr access.Principal) bool {
	key := factKey(pr)
	if value, ok := f.disabled[key]; ok {
		return value
	}
	disabled, err := PrincipalDisabledIn(f.ctx, f.q, pr)
	f.failed(err)
	if f.disabled == nil {
		f.disabled = make(map[string]bool)
	}
	f.disabled[key] = disabled
	return disabled
}

// factKey is who a fact is about: the principal's ID when the caller was
// admitted with one, else its kind and name.
func factKey(pr access.Principal) string {
	if !pr.ID.IsZero() {
		return pr.ID.String()
	}
	return pr.Kind + "\x00" + pr.Name
}
func (f *serviceFacts) Grants(pr access.Principal) []access.Grant {
	key := factKey(pr)
	if value, ok := f.grants[key]; ok {
		return value
	}
	grants, err := ActiveGrantsIn(f.ctx, f.q, pr, f.at)
	f.failed(err)
	if f.grants == nil {
		f.grants = make(map[string][]access.Grant)
	}
	f.grants[key] = grants
	return grants
}
