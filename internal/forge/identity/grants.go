package identity

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/fault"

	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/identity/internal/accesssql"
	"flokkit.io/forge/internal/stamp"
)

// Grants (authorization-capabilities.md §6, §14): a grant gives a subject
// (a user, a group, or anonymous) a named role or one capability at a
// scope. Grants are never edited: a revocation stamps the row and stays as
// history, and identical live grants are never duplicated; the database
// keeps that promise with the partial unique index grants_live_identity.
// Setup joins the caller's transaction: creating a project or an
// administrator is incomplete until its ownership records exist, and setup
// never repairs a revoked membership or owner grant.

// GroupAllUsers is the built-in group every signed-in person belongs to.
const GroupAllUsers = "all-authenticated-users"

var ErrLastAdministrator = fault.New(fault.Conflict, "make someone else an administrator first")

// Spec is one grant to write.
type Spec struct {
	SubjectType, Subject string
	Role, Capability     string // one of the two
	Scope                access.ScopeKind
	ScopeID              string
	Origin, By, Reason   string
}

func grantRecord(r accesssql.GrantsNamed) access.Grant {
	return access.Grant{ID: r.ID, SubjectType: r.SubjectType, Subject: r.Subject, RoleName: r.RoleName,
		Capability: access.Capability(r.Capability), ScopeType: access.ScopeKind(r.ScopeType),
		ScopeID: r.ScopeID, Origin: r.Origin, GrantedBy: r.GrantedBy, GrantedByPrincipal: r.GrantedByPrincipal, Reason: r.Reason,
		CreatedAt: r.CreatedAt.Format(time.RFC3339Nano), ExpiresAt: stamp.Time{NullTime: r.ExpiresAt},
		ReviewAt: stamp.Time{NullTime: r.ReviewAt}, RevokedAt: stamp.Time{NullTime: r.RevokedAt}}
}

func records(rows []accesssql.GrantsNamed, err error) ([]access.Grant, error) {
	if err != nil {
		return nil, err
	}
	var out []access.Grant
	for _, r := range rows {
		out = append(out, grantRecord(r))
	}
	return out, nil
}

// ActiveGrantsIn resolves the principal, their active groups and the
// built-in group in one database snapshot, live as of at, the caller's
// clock. access.Authority is re-read on every evaluation, including
// long-lived chats.
func ActiveGrantsIn(ctx context.Context, q database.Queryer, pr access.Principal, at time.Time) ([]access.Grant, error) {
	kind, subject := "anonymous", "anonymous"
	if pr.Kind == "user" {
		kind, subject = "user", pr.Name
		// An admitted principal's grants are its own, whatever its name is
		// now or whoever holds its old one.
		if !pr.ID.IsZero() {
			return records(queries(q).ActiveGrantsOfPrincipal(ctx, accesssql.ActiveGrantsOfPrincipalParams{At: at, Principal: pr.ID, BuiltinGroup: GroupAllUsers}))
		}
	}
	return records(queries(q).ActiveGrants(ctx, accesssql.ActiveGrantsParams{At: at, SubjectType: kind, Subject: subject, BuiltinGroup: GroupAllUsers}))
}

// GrantsAtIn is the live grants at one scope, for administrative lists.
func GrantsAtIn(ctx context.Context, q database.Queryer, scope access.ScopeKind, scopeID string) ([]access.Grant, error) {
	return records(queries(q).GrantsAtScope(ctx, accesssql.GrantsAtScopeParams{ScopeType: string(scope), ScopeID: scopeID}))
}

// AllGrantsIn is every live grant.
func AllGrantsIn(ctx context.Context, q database.Queryer) ([]access.Grant, error) {
	return records(queries(q).AllGrants(ctx))
}

// InstanceSettingIn is one instance setting, "" when unset.
func InstanceSettingIn(ctx context.Context, q database.Queryer, key string) (string, error) {
	value, err := queries(q).InstanceSetting(ctx, key)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return value, err
}

// --- writes, in the caller's transaction ------------------------------------------

// GrantIn records a grant unless an identical one is live, in which case
// the live one is returned, never duplicated. Explicit grants use it;
// default seeding must first respect historical choices. at is the
// caller's clock, which stamps a new grant.
func GrantIn(ctx context.Context, tx *database.Tx, s Spec, at time.Time) (ids.ID, error) {
	id, _, err := grantIn(ctx, tx, s, at)
	return id, err
}

// grantIn also reports whether the row was created now. The insert is
// decided by the live-identity index: no row back means one is live, and
// a second writer racing this one waits for the first to commit, then
// reads what it wrote.
func grantIn(ctx context.Context, tx *database.Tx, s Spec, at time.Time) (ids.ID, bool, error) {
	if s.Role != "" {
		rv := access.RoleByName(s.Role)
		if rv == nil {
			return ids.Nil, false, fmt.Errorf("unknown role %q", s.Role)
		}
	}
	q := queries(tx)
	// A grant to a person records the person's account with it, as the
	// grant and member doors do: authority is decided only for an account,
	// and People lists everyone who holds some.
	if s.SubjectType == "user" {
		if err := ValidName(s.Subject); err != nil {
			return ids.Nil, false, err
		}
		if err := recordPersonIn(ctx, tx, s.Subject, at); err != nil {
			return ids.Nil, false, err
		}
	}
	granter, err := ActorIn(ctx, tx, s.By)
	if err != nil {
		return ids.Nil, false, err
	}
	g, err := q.InsertGrant(ctx, accesssql.InsertGrantParams{ID: ids.New(at.UTC()), SubjectType: s.SubjectType, Subject: s.Subject, RoleName: s.Role,
		Capability: s.Capability, ScopeType: string(s.Scope), ScopeID: s.ScopeID, Origin: s.Origin, GrantedBy: s.By, GrantedByPrincipal: granter.Principal, GrantedByVia: granter.Via, Reason: s.Reason, CreatedAt: at.UTC()})
	if err == nil {
		return g.ID, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ids.Nil, false, err
	}
	live, err := q.LiveGrantByIdentity(ctx, accesssql.LiveGrantByIdentityParams{SubjectType: s.SubjectType, Subject: s.Subject, RoleName: s.Role,
		Capability: s.Capability, ScopeType: string(s.Scope), ScopeID: s.ScopeID})
	if err != nil {
		return ids.Nil, false, fmt.Errorf("grant refused as a duplicate but no live grant found: %w", err)
	}
	return live.ID, false, nil
}

// RevokeIn stamps a grant revoked with its receipt. Revoking a person's
// instance administrator role is refused if the result leaves no enabled root. A grant already
// revoked, or missing, is false with no error. at is the caller's clock:
// the revocation's time, and the moment the remaining roots are judged.
func RevokeIn(ctx context.Context, tx *database.Tx, id ids.ID, by string, at time.Time) (bool, error) {
	var revoked bool
	err := preserveRoot(ctx, tx, at, func() error {
		var err error
		revoked, err = revokeGrantIn(ctx, tx, id, by, at)
		return err
	})
	return revoked, err
}

func revokeGrantIn(ctx context.Context, tx *database.Tx, id ids.ID, by string, at time.Time) (bool, error) {
	q := queries(tx)
	g, err := q.GrantByID(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := q.LockGrant(ctx, id); err != nil {
		return false, err
	}
	if g, err = q.GrantByID(ctx, id); err != nil {
		return false, err
	}
	if g.RevokedAt.Valid {
		return false, nil
	}
	// Revoke the locked grant without changing its historical attribution.
	grant := grantRecord(g)
	event, err := grant.Revoke(access.Principal{Name: by}, at.UTC())
	if err != nil {
		return false, err
	}
	if _, err := q.RevokeGrant(ctx, accesssql.RevokeGrantParams{
		ID: g.ID, SubjectType: g.SubjectType, RoleName: g.RoleName,
		Capability: g.Capability, ScopeType: g.ScopeType, ScopeID: g.ScopeID,
		Origin: g.Origin, GrantedBy: g.GrantedBy, Reason: g.Reason, CreatedAt: g.CreatedAt,
		ExpiresAt: g.ExpiresAt, ReviewAt: g.ReviewAt, RevokedAt: grant.RevokedAt.NullTime,
	}); err != nil {
		return false, err
	}
	if err := audit.At(at).RecordIn(ctx, tx, event.Actor, ids.Nil, event.Kind, event.Subject, nil); err != nil {
		return false, err
	}
	return true, nil
}

// RevokeAdminGrantsIn revokes every live instance administrator grant a
// person holds, under the last-administrator rule.
func RevokeAdminGrantsIn(ctx context.Context, tx *database.Tx, user, by string, at time.Time) error {
	live, err := queries(tx).LiveRoleGrantsForSubject(ctx, accesssql.LiveRoleGrantsForSubjectParams{
		SubjectType: "user", Subject: user, RoleName: access.RoleInstanceAdmin, ScopeType: string(access.ScopeInstance)})
	if err != nil {
		return err
	}
	for _, g := range live {
		if _, err := RevokeIn(ctx, tx, g.ID, by, at); err != nil {
			return err
		}
	}
	return nil
}

// LockUsersIn is taken by role edits, first-account bootstrap and
// administrator revocation before reading users or locking grants, so they
// cannot race the last-administrator decision. No password hashing or
// external work happens under it.
func LockUsersIn(ctx context.Context, tx *database.Tx) error { return queries(tx).LockUsers(ctx) }

// PersonalGroupIn is a person's own group, created on first use with the
// person as its owner. An existing personal group is returned as recorded;
// a name already taken by another kind of group, or a personal group that
// lost its ownership record, is an error, never repaired here.
func PersonalGroupIn(ctx context.Context, tx *database.Tx, user, by string, at time.Time) (string, error) {
	q := queries(tx)
	if err := q.LockPersonalGroup(ctx, user); err != nil {
		return "", err
	}
	for _, name := range []string{user, user + "-personal"} {
		g, err := q.GroupByNameForShare(ctx, name)
		if err == nil {
			if g.Kind != "personal" && name == user {
				continue
			}
			owned, err := q.GrantEverRecorded(ctx, accesssql.GrantEverRecordedParams{SubjectType: "user", Subject: user, RoleName: access.RoleGroupOwner, ScopeType: string(access.ScopeGroup), ScopeID: name})
			if err != nil {
				return "", err
			}
			if g.Kind != "personal" || !owned {
				return "", fmt.Errorf("cannot create a personal group for %s: %s is already claimed or missing its ownership record", user, name)
			}
			return name, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
		creator, err := ActorIn(ctx, tx, by)
		if err != nil {
			return "", err
		}
		if _, err := q.InsertPersonalGroup(ctx, accesssql.InsertPersonalGroupParams{Name: name, Description: user + "'s own projects", CreatedBy: by, CreatedByPrincipal: creator.Principal, CreatedByVia: creator.Via, CreatedAt: at.UTC()}); err != nil {
			return "", err
		}
		if err := recordPersonIn(ctx, tx, user, at); err != nil {
			return "", err
		}
		if _, err := q.InsertMembership(ctx, accesssql.InsertMembershipParams{GroupName: name, Username: user, CreatedBy: by, CreatedByPrincipal: creator.Principal, CreatedByVia: creator.Via, CreatedAt: at.UTC()}); err != nil {
			return "", err
		}
		if _, err := GrantIn(ctx, tx, Spec{SubjectType: "user", Subject: user, Role: access.RoleGroupOwner, Scope: access.ScopeGroup, ScopeID: name, Origin: "bootstrap", By: by}, at); err != nil {
			return "", err
		}
		return name, nil
	}
	panic("personal group candidates exhausted")
}

// SovereignGroupIn is the first administrator's personal group, "" when
// there is no administrator yet; who administers is judged as of at, the
// caller's clock.
func SovereignGroupIn(ctx context.Context, tx *database.Tx, at time.Time) (string, error) {
	admins, err := queries(tx).AdminUsers(ctx, at)
	if err != nil {
		return "", err
	}
	if len(admins) == 0 {
		return "", nil
	}
	return PersonalGroupIn(ctx, tx, admins[0].Username, "forge", at)
}

// AdminGrantIn makes a person an instance administrator: their personal
// group, then the sovereign role at the instance, stamped at the caller's
// clock.
func AdminGrantIn(ctx context.Context, tx *database.Tx, user, by string, at time.Time) error {
	if _, err := PersonalGroupIn(ctx, tx, user, by, at); err != nil {
		return err
	}
	_, err := GrantIn(ctx, tx, Spec{SubjectType: "user", Subject: user, Role: access.RoleInstanceAdmin, Scope: access.ScopeInstance, Origin: "bootstrap", By: by}, at)
	return err
}

// ProjectOwnershipIn grants ownership to the named creator, and the approver
// role: stock workflows name role:project-approver, and a new project would
// otherwise have nobody but administrators to accept or land its changes.
// Both grants are visible and removable on the Members tab.
func ProjectOwnershipIn(ctx context.Context, tx *database.Tx, projectID ids.ID, creator string, at time.Time) error {
	pid := projectID.String()
	// AppTown's own names are not people: a project AppTown creates
	// gets no personal owner.
	if creator == "" || creator == "anonymous" || SystemActor(creator) {
		return nil
	}
	for _, role := range []string{access.RoleProjectOwner, access.RoleProjectApprover} {
		if _, err := GrantIn(ctx, tx, Spec{SubjectType: "user", Subject: creator, Role: role, Scope: access.ScopeProject, ScopeID: pid, By: creator, Reason: "created the project"}, at); err != nil {
			return err
		}
	}
	return nil
}

// SeedTransparentReadsIn records the transparent-structure default once:
// everyone signed in reads everything. The first grant establishes the
// default; a revoked or expired one records an explicit choice and stops
// startup from granting it again.
func SeedTransparentReadsIn(ctx context.Context, q database.Queryer, at time.Time) error {
	return queries(q).SeedTransparentReads(ctx, accesssql.SeedTransparentReadsParams{ID: ids.New(at.UTC()),
		Subject: GroupAllUsers, RoleName: access.RoleAuditor, At: at.UTC(),
	})
}

// DefaultsIn is required startup state: the built-in group, the open-mode reader
// (granted once in open mode, revoked when the mode closes), and the
// transparent-reads default, each stamped at the caller's clock.
func DefaultsIn(ctx context.Context, tx *database.Tx, open bool, at time.Time) error {
	if err := LockUsersIn(ctx, tx); err != nil {
		return err
	}
	q := queries(tx)
	if _, err := q.EnsureBuiltinGroup(ctx, accesssql.EnsureBuiltinGroupParams{Name: GroupAllUsers,
		Description: "Everyone who is signed in. Granting this group something gives it to everyone.", CreatedAt: at.UTC()}); err != nil {
		return err
	}
	reader := accesssql.GrantEverRecordedParams{SubjectType: "anonymous", Subject: "anonymous", RoleName: access.RoleAuditor, ScopeType: string(access.ScopeInstance)}
	if open {
		chosen, err := q.GrantEverRecorded(ctx, reader)
		if err != nil {
			return err
		}
		if !chosen {
			if _, err := GrantIn(ctx, tx, Spec{SubjectType: "anonymous", Subject: "anonymous", Role: access.RoleAuditor, Scope: access.ScopeInstance, Origin: "profile:open", By: "forge", Reason: "open mode: anyone on the network can read"}, at); err != nil {
				return err
			}
		}
	} else {
		live, err := q.LiveRoleGrantsForSubject(ctx, accesssql.LiveRoleGrantsForSubjectParams(reader))
		if err != nil {
			return err
		}
		for _, g := range live {
			if _, err := RevokeIn(ctx, tx, g.ID, "forge", at); err != nil {
				return err
			}
		}
	}
	if err := SeedTransparentReadsIn(ctx, tx, at); err != nil {
		return err
	}
	chosen, err := q.GrantEverRecorded(ctx, accesssql.GrantEverRecordedParams{SubjectType: "group", Subject: GroupAllUsers, RoleName: access.RoleAuditor, ScopeType: string(access.ScopeInstance)})
	if err != nil {
		return err
	}
	if !chosen {
		return errors.New("transparent read default was not recorded")
	}
	return nil
}

// --- the service ------------------------------------------------------------------

// Grants is the supported API over grants for callers without a
// transaction of their own.
type Grants struct {
	db    *database.DB
	clock func() time.Time // nil is the real clock
}

func NewGrants(db *database.DB) *Grants { return &Grants{db: db} }

func (s *Grants) now() time.Time { return clock.Or(s.clock)() }

// Add writes a person's explicit grant with its `grant.added` receipt in
// the same transaction; an expiry, when given, is stamped on it. An
// identical live grant is returned unchanged and leaves no receipt.
func (s *Grants) Add(ctx context.Context, sp Spec, expiresAt string) (ids.ID, error) {
	var id ids.ID
	now := s.now()
	apply := func(tx *database.Tx) error {
		var created bool
		var err error
		if id, created, err = grantIn(ctx, tx, sp, now); err != nil {
			return err
		}
		if expiresAt != "" {
			if _, err := queries(tx).SetGrantExpiry(ctx, accesssql.SetGrantExpiryParams{ID: id, ExpiresAt: expiresAt}); err != nil {
				return err
			}
		}
		if !created && expiresAt == "" {
			return nil
		}
		var projectID ids.ID
		if sp.Scope == access.ScopeProject {
			projectID, _ = ids.Parse(sp.ScopeID)
		}
		return audit.At(now).RecordIn(ctx, tx, sp.By, projectID, "grant.added", id.String(),
			map[string]any{"subject": sp.SubjectType + ":" + sp.Subject, "role": sp.Role, "capability": sp.Capability, "scope": string(sp.Scope), "scopeId": sp.ScopeID})
	}
	err := s.db.InTx(ctx, func(tx *database.Tx) error {
		return preserveRoot(ctx, tx, now, func() error { return apply(tx) })
	})
	return id, err
}

// Put writes a grant with no receipt of its own, for callers that record
// the wider act (a project's roster).
func (s *Grants) Put(ctx context.Context, sp Spec) (ids.ID, error) {
	return database.Transact(ctx, s.db, func(tx *database.Tx) (ids.ID, error) { return GrantIn(ctx, tx, sp, s.now()) })
}

// Revoke stamps one grant revoked, under the last-administrator rule.
func (s *Grants) Revoke(ctx context.Context, id ids.ID, by string) (bool, error) {
	return database.Transact(ctx, s.db, func(tx *database.Tx) (bool, error) { return RevokeIn(ctx, tx, id, by, s.now()) })
}

// Live is one grant's scope while it is live, for the door that decides who
// may revoke it.
func (s *Grants) Live(ctx context.Context, id ids.ID) (scope access.ScopeKind, scopeID string, found bool, err error) {
	g, err := queries(s.db).GrantByID(ctx, id)
	found, err = database.Lookup(err)
	if !found || err != nil || g.RevokedAt.Valid {
		return "", "", false, err
	}
	return access.ScopeKind(g.ScopeType), g.ScopeID, true, nil
}

// PersonalGroup is PersonalGroupIn in its own transaction.
func (s *Grants) PersonalGroup(ctx context.Context, user, by string) (string, error) {
	return database.Transact(ctx, s.db, func(tx *database.Tx) (string, error) { return PersonalGroupIn(ctx, tx, user, by, s.now()) })
}

// EnsureDefaults is DefaultsIn in its own transaction: required startup
// state, so a failure is returned to the server's startup gate.
func (s *Grants) EnsureDefaults(ctx context.Context, open bool) error {
	return s.db.InTx(ctx, func(tx *database.Tx) error { return DefaultsIn(ctx, tx, open, s.now()) })
}

// SovereignGroup is SovereignGroupIn in its own transaction.
func (s *Grants) SovereignGroup(ctx context.Context) (string, error) {
	return database.Transact(ctx, s.db, func(tx *database.Tx) (string, error) { return SovereignGroupIn(ctx, tx, s.now()) })
}
