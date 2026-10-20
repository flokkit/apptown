package identity

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/identity/internal/accesssql"
	"flokkit.io/forge/internal/stamp"
)

// The directory (auth.md §2, authorization-capabilities.md §3A, §4): who
// the accounts and groups are. An account is part of the record and is
// never deleted once it has a password, has signed in or has acted; a name
// that never did (open-mode sediment, a typo in a member list) may be
// removed. Groups carry memberships and a
// kind: ordinary, personal (minted for one person, never deleted), or
// built-in (everyone signed in). Administrator authority comes from current grants; the directory exposes
// its role word as a derived display value.

var (
	ErrAlreadyBootstrapped = errors.New("an account already exists — sign in")
	ErrRealAccount         = fault.New(fault.Conflict, "accounts are kept for the record")
	ErrProtectedGroup      = fault.New(fault.Conflict, "a built-in or personal group cannot be deleted")
	ErrNoSuchName          = fault.New(fault.NotFound, "no such name")
)

// Account is a person and their account state; it carries no database handle.
type Account struct {
	// PrincipalID is the person the account belongs to: what credentials,
	// grants and memberships are held by. The name can change; it cannot.
	PrincipalID ids.ID `json:"principalId"`
	Username    string `json:"username"`
	Role        string `json:"role"` // derived from grants; never stored on users
	HasPassword bool   `json:"hasPassword"`
	// Acted: the name has signed in or appears as an actor on the ledger.
	// With a password, that makes it an account, which is disabled and
	// never removed.
	Acted        bool       `json:"acted"`
	LastSeen     stamp.Time `json:"lastSeen"`
	Disabled     bool       `json:"disabled"`
	Groups       []string   `json:"groups"`
	DefaultGroup string     `json:"-"`
	passwordHash string
	createdAt    time.Time
	disabledAt   stamp.Time
}

func account(r accesssql.User) Account {
	return Account{PrincipalID: r.PrincipalID, Username: r.Username, Role: "member", HasPassword: r.PasswordHash != "", LastSeen: stamp.Time{NullTime: r.LastSeen}, Disabled: r.DisabledAt.Valid, Groups: []string{}, passwordHash: r.PasswordHash, createdAt: r.CreatedAt, disabledAt: stamp.Time{NullTime: r.DisabledAt}, DefaultGroup: r.DefaultGroup}
}

// accountEdit is the private prepared save of an account: create or update, a password set or
// reset when given, a role change, disabling, a default group.
type accountEdit struct {
	Name, Role, PasswordHash string
	Disabled                 *bool
	DefaultGroup             *string
}

// Group is a group with its members.
type Group struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Kind        string   `json:"kind"` // ordinary | personal | built-in
	Members     []string `json:"members"`
}

func group(r accesssql.Group) Group {
	return Group{Name: r.Name, Description: r.Description, Kind: r.Kind, Members: []string{}}
}

// directoryStore persists accounts, groups and memberships behind Service.
type directoryStore struct {
	db    *database.DB
	clock func() time.Time // nil is the real clock
}

func (d *directoryStore) now() time.Time { return clock.Or(d.clock)() }

// Ensure records a name seen on the wire and returns its grant-derived role;
// an unknown name becomes a member with no password. A name AppTown would
// read as something else (ValidName) is refused and not recorded.
func (d *directoryStore) Ensure(ctx context.Context, username string) (string, error) {
	a, err := d.ensure(ctx, username)
	return a.Role, err
}

// ensure is Ensure's account, its principal with it.
func (d *directoryStore) ensure(ctx context.Context, username string) (Account, error) {
	if err := ValidName(username); err != nil {
		return Account{}, err
	}
	now := d.now()
	person, err := personIn(ctx, d.db, username, now)
	if err != nil {
		return Account{}, err
	}
	row, err := queries(d.db).TouchUser(ctx, accesssql.TouchUserParams{Username: username, PrincipalID: person, At: now.UTC()})
	if err != nil {
		return Account{}, err
	}
	return accountIn(ctx, d.db, row, now)
}

// Disabled reports whether a name may not act as an account: it is
// disabled, or there is no account by that name (removed, or never seen),
// which is refused the same way rather than treated as enabled.
func (d *directoryStore) Disabled(ctx context.Context, username string) (bool, error) {
	return DisabledIn(ctx, d.db, username)
}

// DisabledIn is Disabled through q, for the authority adapters other
// domains hand their transactions to.
func DisabledIn(ctx context.Context, q database.Queryer, username string) (bool, error) {
	u, err := queries(q).UserByName(ctx, username)
	found, err := database.Lookup(err)
	return !found || u.DisabledAt.Valid, err
}

// PrincipalDisabledIn is DisabledIn for a principal: an admitted one by its
// ID, so a rename or a later account with the name changes nothing; a name
// alone by the account of that name.
func PrincipalDisabledIn(ctx context.Context, q database.Queryer, pr access.Principal) (bool, error) {
	if pr.ID.IsZero() {
		return DisabledIn(ctx, q, pr.Name)
	}
	u, err := queries(q).UserByPrincipal(ctx, pr.ID)
	found, err := database.Lookup(err)
	return !found || u.DisabledAt.Valid, err
}

// OfferGroups is every live group an offer can be made to: the built-in
// group and the ordinary ones, never a personal group.
func (d *directoryStore) OfferGroups(ctx context.Context) ([]string, error) {
	groups, err := queries(d.db).GroupsLive(ctx)
	out := []string{}
	for _, g := range groups {
		if g.Kind != "personal" {
			out = append(out, g.Name)
		}
	}
	sort.Strings(out)
	return out, err
}

// DefaultGroup is where a person's projects go by default, "" when unset.
func (d *directoryStore) DefaultGroup(ctx context.Context, username string) (string, error) {
	u, err := queries(d.db).UserByName(ctx, username)
	_, err = database.Lookup(err)
	return u.DefaultGroup, err
}

// Users is every account with its groups.
func (d *directoryStore) Users(ctx context.Context) ([]Account, error) {
	q := queries(d.db)
	rows, err := q.UsersAll(ctx)
	if err != nil {
		return nil, err
	}
	admins, err := q.AdminUsers(ctx, d.now())
	if err != nil {
		return nil, err
	}
	roots := map[string]bool{}
	for _, a := range admins {
		roots[a.Username] = true
	}
	memberships, err := q.MembershipsAll(ctx)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(rows))
	for _, r := range rows {
		names = append(names, r.Username)
	}
	acted, err := actedIn(ctx, d.db, names)
	if err != nil {
		return nil, err
	}
	users := make([]Account, 0, len(rows))
	index := map[string]int{}
	for i, r := range rows {
		a := account(r)
		a.Acted = acted[r.Username]
		if roots[r.Username] {
			a.Role = "admin"
		}
		users = append(users, a)
		index[r.Username] = i
	}
	for _, m := range memberships {
		if i, ok := index[m.Username]; ok {
			users[i].Groups = append(users[i].Groups, m.GroupName)
		}
	}
	return users, nil
}

// bootstrapIn records the first administrator, defaults, and receipt inside
// the transaction that also creates the first session, stamped at now.
func bootstrapIn(ctx context.Context, tx *database.Tx, user, passwordHash string, now time.Time) (Account, error) {
	if err := LockUsersIn(ctx, tx); err != nil {
		return Account{}, err
	}
	q := queries(tx)
	needs, err := q.NeedsBootstrap(ctx)
	if err != nil {
		return Account{}, err
	}
	if !needs {
		return Account{}, ErrAlreadyBootstrapped
	}
	person, err := personIn(ctx, tx, user, now)
	if err != nil {
		return Account{}, err
	}
	row, err := q.BootstrapAdministrator(ctx, accesssql.BootstrapAdministratorParams{Username: user, PrincipalID: person, PasswordHash: passwordHash, At: now.UTC()})
	if err != nil {
		return Account{}, err
	}
	if err := AdminGrantIn(ctx, tx, user, user, now); err != nil {
		return Account{}, err
	}
	if err := DefaultsIn(ctx, tx, false, now); err != nil {
		return Account{}, err
	}
	if err := completeSetupIn(ctx, tx); err != nil {
		return Account{}, err
	}
	if err := audit.At(now).RecordIn(ctx, tx, user, ids.Nil, "auth.bootstrap", user, nil); err != nil {
		return Account{}, err
	}
	return accountIn(ctx, tx, row, now)
}

// allowedFor checks the edit's name against the account it names. A new
// account takes a slug that is a usable person's name (ValidName). An
// existing account is changed whatever its name's syntax — a single sign-on
// name such as an email address — except that a name AppTown would read
// as something else (a row recorded before names were checked) may only be
// disabled: disabling is how an account is offboarded and must never be
// refused.
func (edit accountEdit) allowedFor(missing bool) error {
	if missing {
		if !directoryName.MatchString(edit.Name) {
			return &AccountInputError{Reason: "username (slug) required"}
		}
		return ValidName(edit.Name)
	}
	if err := ValidName(edit.Name); err != nil {
		if edit.Disabled != nil && *edit.Disabled && edit.PasswordHash == "" && edit.Role == "" && edit.DefaultGroup == nil {
			return nil
		}
		return err
	}
	return nil
}

// changeAccountIn creates or updates an account. Demoting or disabling the last
// enabled administrator is refused; a new password or a disabling ends the
// account's sessions; the administrator grant follows the role; one
// receipt records what changed. now is the operation's clock.
func changeAccountIn(ctx context.Context, tx *database.Tx, edit accountEdit, by string, now time.Time) error {
	q := queries(tx)
	row, err := q.UserByNameForUpdate(ctx, edit.Name)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	missing := errors.Is(err, sql.ErrNoRows)
	if err := edit.allowedFor(missing); err != nil {
		return err
	}
	a := account(row)
	if missing {
		a = Account{Username: edit.Name, createdAt: now.UTC(), LastSeen: stamp.Of(now)}
	}
	disabling := edit.Disabled != nil && *edit.Disabled
	if disabling {
		a.Disabled, a.disabledAt = true, stamp.Of(now)
	} else if edit.Disabled != nil {
		a.Disabled, a.disabledAt = false, stamp.Time{}
	}
	followup := a.SetPassword(edit.PasswordHash)
	if edit.DefaultGroup != nil {
		a.DefaultGroup = *edit.DefaultGroup
	}
	if err := saveAccountIn(ctx, tx, a); err != nil {
		return err
	}
	if followup.EndSessions || disabling {
		gone, err := q.DeleteUserSessions(ctx, edit.Name)
		if err != nil {
			return err
		}
		if gone.Selected != gone.Removed {
			return errors.New("old sessions were not removed")
		}
	}
	// An empty role (the operator's password reset) leaves authority as it is.
	if edit.Role == "admin" {
		if err := AdminGrantIn(ctx, tx, edit.Name, by, now); err != nil {
			return err
		}
	} else if edit.Role != "" {
		if err := RevokeAdminGrantsIn(ctx, tx, edit.Name, by, now); err != nil {
			return err
		}
		if root, err := RootIn(ctx, tx, edit.Name, now); err != nil {
			return err
		} else if root {
			return errors.New("administrator authority is inherited from a group; change that grant or membership")
		}
	}
	return audit.At(now).RecordIn(ctx, tx, by, ids.Nil, "user.saved", edit.Name, map[string]any{"role": edit.Role, "passwordSet": edit.PasswordHash != "", "disabled": disabling})
}

// RemoveName removes a name that never signed in or acted. Everything the name
// held goes with it in the same transaction — direct grants, memberships,
// personal tokens, sessions, OAuth delegations and pending invites —
// because any later sighting of the name (a trusted header, an open-mode
// actor) re-creates the account, which must then hold nothing of the old
// one. The receipt lists what was revoked. A real account is ErrRealAccount.
func removeNameIn(ctx context.Context, tx *database.Tx, name, by string, now time.Time) error {
	q := queries(tx)
	u, err := q.UserByNameForUpdate(ctx, name)
	found, err := database.Lookup(err)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%w: %s", ErrNoSuchName, name)
	}
	if real, err := realAccountIn(ctx, tx, u); err != nil {
		return err
	} else if real {
		return fmt.Errorf("%w: %s has signed in or acted — disable the account instead", ErrRealAccount, name)
	}
	revoked, err := revokeEverythingIn(ctx, tx, name, by, now)
	if err != nil {
		return err
	}
	if _, err := q.DeleteUser(ctx, name); err != nil {
		return err
	}
	// The person is kept, removed, so history still names them; the name
	// is free, and a later account with it is a new principal holding
	// nothing of this one.
	if _, err := q.RemovePrincipal(ctx, accesssql.RemovePrincipalParams{ID: u.PrincipalID, At: now.UTC()}); err != nil {
		return err
	}
	return audit.At(now).RecordIn(ctx, tx, by, ids.Nil, "user.name-removed", name, revoked)
}

// realAccountIn: an account is real once it has a password, has signed in
// (single sign-on and proxy accounts never have a password), or appears as
// an actor on the ledger. A real account is disabled, never removed, so the
// disabled mark survives a later sighting of the name.
func realAccountIn(ctx context.Context, q database.Queryer, u accesssql.User) (bool, error) {
	if u.PasswordHash != "" {
		return true, nil
	}
	acted, err := actedIn(ctx, q, []string{u.Username})
	return acted[u.Username], err
}

// actedIn is which of the names have signed in or appear as an actor on
// the ledger.
func actedIn(ctx context.Context, q database.Queryer, names []string) (map[string]bool, error) {
	acted, err := audit.ActorsAmongIn(ctx, q, names)
	if err != nil {
		return nil, err
	}
	signedIn, err := queries(q).UsersSignedIn(ctx)
	for _, n := range signedIn {
		acted[n] = true
	}
	return acted, err
}

// heldByName is what a name's removal revoked, as its receipt lists it.
type heldByName struct {
	Grants      []ids.ID `json:"grants"`
	Tokens      []string `json:"tokens"`
	Delegations []ids.ID `json:"delegations"`
	Invites     []ids.ID `json:"invites"`
	Memberships int64    `json:"memberships"`
	Sessions    int64    `json:"sessions"`
}

func revokeEverythingIn(ctx context.Context, tx *database.Tx, name, by string, now time.Time) (heldByName, error) {
	q := queries(tx)
	held := heldByName{Grants: []ids.ID{}, Tokens: []string{}, Delegations: []ids.ID{}, Invites: []ids.ID{}}
	grants, err := q.LiveGrantIDsOfUser(ctx, name)
	if err != nil {
		return held, err
	}
	for _, id := range grants {
		if ok, err := RevokeIn(ctx, tx, id, by, now); err != nil {
			return held, err
		} else if ok {
			held.Grants = append(held.Grants, id)
		}
	}
	if held.Memberships, err = q.DeleteUserMemberships(ctx, name); err != nil {
		return held, err
	}
	tokens, err := q.LiveAuthTokenNamesOfUser(ctx, name)
	if err != nil {
		return held, err
	}
	for _, t := range tokens {
		if ok, err := revokeTokenIn(ctx, tx, t, by, now); err != nil {
			return held, err
		} else if ok {
			held.Tokens = append(held.Tokens, t)
		}
	}
	sessions, err := q.DeleteUserSessions(ctx, name)
	if err != nil {
		return held, err
	}
	if sessions.Selected != sessions.Removed {
		return held, errors.New("sessions were not all removed")
	}
	held.Sessions = sessions.Removed
	now = now.UTC()
	delegations, err := q.DelegationsLiveForUser(ctx, name)
	if err != nil {
		return held, err
	}
	for _, d := range delegations {
		if err := revokeDelegationIn(ctx, tx, d.ID, by, now); err != nil {
			return held, err
		}
		held.Delegations = append(held.Delegations, d.ID)
	}
	var person ids.ID
	if u, err := q.UserByName(ctx, name); err == nil {
		person = u.PrincipalID
	} else if !errors.Is(err, sql.ErrNoRows) {
		return held, err
	}
	invites, err := q.PendingInvitesForUpdate(ctx, accesssql.PendingInvitesForUpdateParams{PrincipalID: person, Username: name})
	if err != nil {
		return held, err
	}
	revoker, err := ActorIn(ctx, tx, by)
	if err != nil {
		return held, err
	}
	for _, inv := range invites {
		if err := settleInviteIn(ctx, q, inv.ID, accesssql.SettleInviteParams{RevokedAt: sql.NullTime{Time: now, Valid: true}, RevokedBy: by, RevokedByPrincipal: revoker.Principal, RevokedByVia: revoker.Via}); err != nil {
			return held, err
		}
		held.Invites = append(held.Invites, inv.ID)
	}
	return held, nil
}

// --- groups -------------------------------------------------------------------------

// Groups is every live group with its members, built-in first, then
// personal, then the rest by name.
func (d *directoryStore) Groups(ctx context.Context) ([]Group, error) {
	q := queries(d.db)
	rows, err := q.GroupsLive(ctx)
	if err != nil {
		return nil, err
	}
	memberships, err := q.MembershipsAll(ctx)
	if err != nil {
		return nil, err
	}
	groups := make([]Group, 0, len(rows))
	index := map[string]int{}
	for i, r := range rows {
		groups = append(groups, group(r))
		index[r.Name] = i
	}
	for _, m := range memberships {
		if i, ok := index[m.GroupName]; ok {
			groups[i].Members = append(groups[i].Members, m.Username)
		}
	}
	return groups, nil
}

// GroupKind is a live group's kind, "" when there is none.
func (d *directoryStore) GroupKind(ctx context.Context, name string) (string, error) {
	g, err := queries(d.db).GroupByName(ctx, name)
	found, err := database.Lookup(err)
	if !found || err != nil || g.RetiredAt.Valid {
		return "", err
	}
	return g.Kind, nil
}

// GroupNames is every live group a project could be created in: personal
// groups first, then the rest by name; the built-in group never.
func (d *directoryStore) GroupNames(ctx context.Context) ([]string, error) {
	groups, err := queries(d.db).GroupsLive(ctx)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, g := range groups {
		if g.Kind != "built-in" {
			out = append(out, g.Name)
		}
	}
	return out, nil
}

// DeleteGroup removes an ordinary group and its memberships with the
// receipt; a built-in or personal group is ErrProtectedGroup.
func deleteGroupIn(ctx context.Context, tx *database.Tx, name, by string, at time.Time) error {
	q := queries(tx)
	g, err := q.GroupByName(ctx, name)
	found, err := database.Lookup(err)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%w: no such group %s", ErrNoSuchName, name)
	}
	if g.Kind == "built-in" || g.Kind == "personal" {
		return fmt.Errorf("%w: %s is a %s group", ErrProtectedGroup, name, g.Kind)
	}
	if err := clearGroupMembersIn(ctx, tx, name); err != nil {
		return err
	}
	removed, err := q.DeleteGroup(ctx, name)
	if err != nil {
		return err
	}
	if removed != 1 {
		return errors.New("group was not removed")
	}
	return audit.At(at).RecordIn(ctx, tx, by, ids.Nil, "group.deleted", name, nil)
}

// RevokeProjectRoles revokes every live project-scoped grant a subject
// holds on one project, with the roster receipt; zero means the subject
// held nothing there.
func (d *directoryStore) RevokeProjectRoles(ctx context.Context, subjectType, subject string, projectID ids.ID, by string) (int64, error) {
	var count int64
	err := d.db.InTx(ctx, func(tx *database.Tx) error {
		var err error
		at := d.now()
		count, err = queries(tx).RevokeProjectGrants(ctx, accesssql.RevokeProjectGrantsParams{SubjectType: subjectType, Subject: subject, ScopeID: projectID.String(), At: at.UTC()})
		if err != nil || count == 0 {
			return err
		}
		grantee := subject
		if subjectType != "user" {
			grantee = subjectType + ":" + subject
		}
		return audit.At(at).RecordIn(ctx, tx, by, projectID, "project.member-removed", grantee, map[string]any{"grants": count})
	})
	return count, err
}

// RoleIn reads the current account role inside the caller's checked
// observation, its grants judged live as of at.
func RoleIn(ctx context.Context, q database.Queryer, username string, at time.Time) (string, bool, error) {
	u, err := queries(q).UserByName(ctx, username)
	found, err := database.Lookup(err)
	if err != nil || !found {
		return "", found, err
	}
	a, err := accountIn(ctx, q, u, at)
	return a.Role, true, err
}

// ApproverGroupsIn is the live membership observation for approver
// expressions, memberships judged live as of at.
func ApproverGroupsIn(ctx context.Context, q database.Queryer, user string, at time.Time) ([]string, error) {
	return queries(q).ApproverGroups(ctx, accesssql.ApproverGroupsParams{Username: user, At: at})
}

// PrincipalGroupsIn is ApproverGroupsIn for a principal: its live groups,
// whatever its name is now.
func PrincipalGroupsIn(ctx context.Context, q database.Queryer, person ids.ID, at time.Time) ([]string, error) {
	return queries(q).GroupsOfPrincipal(ctx, accesssql.GroupsOfPrincipalParams{Principal: person, At: at})
}

func saveAccountIn(ctx context.Context, tx *database.Tx, a Account) error {
	if a.PrincipalID.IsZero() {
		// A new account: its person is made with it.
		person, err := personIn(ctx, tx, a.Username, a.createdAt)
		if err != nil {
			return err
		}
		a.PrincipalID = person
	}
	_, err := queries(tx).SaveUser(ctx, accesssql.SaveUserParams{
		Username: a.Username, PrincipalID: a.PrincipalID, PasswordHash: a.passwordHash,
		CreatedAt: a.createdAt, LastSeen: a.LastSeen.NullTime, DisabledAt: a.disabledAt.NullTime, DefaultGroup: a.DefaultGroup,
	})
	return err
}

// RequireLiveGroupIn holds the owning group live through the caller's project
// creation transaction. Retirement cannot race admission after this check.
func RequireLiveGroupIn(ctx context.Context, tx *database.Tx, name string) error {
	r, err := accesssql.New(tx.SQL()).GroupByNameForShare(ctx, name)
	if err != nil {
		return err
	}
	if r.RetiredAt.Valid {
		return sql.ErrNoRows
	}
	return nil
}
