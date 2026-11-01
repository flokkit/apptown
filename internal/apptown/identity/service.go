// Package identity owns who exists and what they hold: accounts and groups,
// the grants that carry roles and capabilities, sessions, personal tokens,
// change tickets and OAuth connections. The authorization rule itself is a
// library (internal/access); this package stores the facts that rule reads and
// is the one kernel a subsystem may ask for an account or a grant.
package identity

import (
	"context"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/access"

	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/database"
)

// Config holds construction-time defaults. Stored authorization policy remains
// live: a long-lived caller never retains a snapshot of its grants.
type Config struct {
	// Now is AppTown's clock, by which expiries and grants are judged;
	// nil is the real one.
	Now          func() time.Time
	sessionTTL   time.Duration
	OpenInstance bool
	Projects     projectCatalogue
	// SetupTokenPath is where the first administrator's setup token is
	// kept while no account exists (setup_token.go); "" checks none.
	SetupTokenPath string
	External       *ExternalDependencies
}

// Service is the Access subsystem. Its stores are private implementation details;
// callers invoke operations rather than constructing stores on each request.
type Service struct {
	db        *database.DB
	cfg       Config
	directory *directoryStore
	grants    *grants
	sessions  *sessionStore
	tokens    *tokenStore
	oauth     *oauthStore
	tickets   *tickets
	denials   denialRecorder
	now       func() time.Time
}

type denialRecorder struct{ db *database.DB }

// New constructs Access once and performs no reads, writes or background work.
func New(db *database.DB, cfg Config) *Service {
	if cfg.sessionTTL == 0 {
		cfg.sessionTTL = SessionTTL
	}
	now := clock.Or(cfg.Now)
	s := &Service{db: db, cfg: cfg, directory: &directoryStore{db: db, clock: now}, grants: newGrants(db),
		sessions: &sessionStore{db: db, ttl: cfg.sessionTTL, now: now}, tokens: &tokenStore{db: db, now: now}, oauth: &oauthStore{db: db, clock: now},
		tickets: newTickets(db), denials: denialRecorder{db: db}, now: now}
	s.grants.clock = now
	s.tickets.clock = now
	return s
}

func (s *Service) SignOut(ctx context.Context, tok string) error {
	return s.sessions.end(ctx, tok)
}

func (s *Service) Role(ctx context.Context, username string) (role string, found bool, err error) {
	return s.directory.role(ctx, username)
}

func (s *Service) NeedsBootstrap(ctx context.Context) (bool, error) {
	return s.directory.needsBootstrap(ctx)
}

func (s *Service) SeedAdministrator(ctx context.Context, username string) error {
	return s.directory.seedAdministrator(ctx, username)
}

func (s *Service) Grant(ctx context.Context, sp Spec, expiresAt string) (ids.ID, error) {
	return s.grants.add(ctx, sp, expiresAt)
}

func (s *Service) PutGrant(ctx context.Context, sp Spec) (ids.ID, error) {
	return s.grants.put(ctx, sp)
}

func (s *Service) Revoke(ctx context.Context, id ids.ID, by string) (bool, error) {
	return s.grants.revoke(ctx, id, by)
}

func (s *Service) LiveGrant(ctx context.Context, id ids.ID) (scope access.ScopeKind, scopeID string, found bool, err error) {
	return s.grants.live(ctx, id)
}

func (s *Service) PersonalGroup(ctx context.Context, user, by string) (string, error) {
	return s.grants.personalGroup(ctx, user, by)
}

func (s *Service) EnsureDefaults(ctx context.Context, open bool) error {
	return s.grants.ensureDefaults(ctx, open)
}

func (s *Service) sovereignGroup(ctx context.Context) (string, error) {
	return s.grants.sovereignGroup(ctx)
}

func (s *Service) Connection(ctx context.Context, id ids.ID) (*Delegation, error) {
	return s.oauth.connection(ctx, id)
}

// LiveConnection is the delegation while it is live by AppTown's clock;
// nil when there is none or it has ended.
func (s *Service) LiveConnection(ctx context.Context, id ids.ID) (*Delegation, error) {
	d, err := s.oauth.connection(ctx, id)
	if err != nil || !d.LiveAt(s.now()) {
		return nil, err
	}
	return d, nil
}

func (s *Service) Connections(ctx context.Context, user string, all bool) ([]Delegation, error) {
	return s.oauth.connections(ctx, user, all)
}

func (s *Service) Disconnect(ctx context.Context, id ids.ID, by string) error {
	return s.oauth.revoke(ctx, id, by)
}

func (s *Service) Sweep(ctx context.Context) error {
	if err := queries(s.db).SweepExternalTransactions(ctx, s.now()); err != nil {
		return err
	}
	return s.oauth.sweepExpired(ctx)
}

func (s *Service) IssueTicket(ctx context.Context, user string, delegationID, changeID ids.ID, ref, expectedTip string) (string, *Ticket, error) {
	return s.tickets.mint(ctx, user, delegationID, changeID, ref, expectedTip)
}

func (s *Service) TicketByToken(ctx context.Context, tok string) (*Ticket, error) {
	return s.tickets.byToken(ctx, tok)
}

func (s *Service) ConsumeTicket(ctx context.Context, id ids.ID) (bool, error) {
	return s.tickets.consume(ctx, id)
}

func (s *Service) RevokeChangeTickets(ctx context.Context, changeID ids.ID) error {
	return s.tickets.revokeForChange(ctx, changeID)
}

func (s *Service) EnsureUser(ctx context.Context, username string) (string, error) {
	return s.directory.ensureRole(ctx, username)
}

// EnsurePerson is EnsureUser's account: its derived role and the principal
// it belongs to.
func (s *Service) EnsurePerson(ctx context.Context, username string) (Account, error) {
	return s.directory.ensure(ctx, username)
}

func (s *Service) OfferGroups(ctx context.Context) ([]string, error) {
	return s.directory.offerGroups(ctx)
}

func (s *Service) defaultGroup(ctx context.Context, username string) (string, error) {
	return s.directory.defaultGroup(ctx, username)
}

func (s *Service) Users(ctx context.Context) ([]Account, error) {
	return s.directory.users(ctx)
}

func (s *Service) RemoveName(ctx context.Context, name string, pr access.Principal) error {
	return s.administer(ctx, pr, access.CapIdentityUsers, func(tx *database.Tx, now time.Time) error {
		return removeNameIn(ctx, tx, name, pr.Name, now)
	})
}

func (s *Service) Groups(ctx context.Context) ([]Group, error) {
	return s.directory.groups(ctx)
}

func (s *Service) GroupKind(ctx context.Context, name string) (string, error) {
	return s.directory.GroupKind(ctx, name)
}

func (s *Service) GroupNames(ctx context.Context) ([]string, error) {
	return s.directory.groupNames(ctx)
}

func (s *Service) DeleteGroup(ctx context.Context, name string, pr access.Principal) error {
	return s.administer(ctx, pr, access.CapIdentityGroups, func(tx *database.Tx, at time.Time) error {
		return deleteGroupIn(ctx, tx, name, pr.Name, at)
	})
}

// ActiveGrants reads current authority through the caller's context.
func (s *Service) ActiveGrants(ctx context.Context, pr access.Principal) ([]access.Grant, error) {
	return ActiveGrantsIn(ctx, s.db, pr, s.now())
}
func (s *Service) GrantsAt(ctx context.Context, scope access.ScopeKind, id string) ([]access.Grant, error) {
	return GrantsAtIn(ctx, s.db, scope, id)
}
func (s *Service) AllGrants(ctx context.Context) ([]access.Grant, error) {
	return allGrantsIn(ctx, s.db)
}
