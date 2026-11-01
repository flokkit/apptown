package identity

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/identity/internal/accesssql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
)

// Invites (auth.md "Invites"): an administrator invites a person by link
// instead of choosing their password. The link carries a random token that
// is stored only as a hash. It lets its holder do one thing — set the
// password of the one account it names, once, before it expires — and then
// signs them in. The invite stays as the record of who invited whom, with
// what role; the inviter must still be able to add people when it is used.
//
// A password reset link is the same link for an account that already has a
// password: its holder replaces the password, every session of the account
// ends, and they are signed in. It grants nothing, so it has no role; the
// account keeps exactly the authority it had. AppTown sends no email here
// either — the administrator hands the link over.

const (
	// inviteTTL is how long an invite lasts when the administrator does not say.
	inviteTTL = 7 * 24 * time.Hour
	// maxInviteDays bounds a link that grants an account to whoever holds it.
	maxInviteDays = 30
	// InvitePrefix marks an invite token, so a leaked one is recognisable.
	InvitePrefix = "apptown_invite_"
	// passwordLinkTTL is how long a password reset link lasts when the
	// administrator does not say: shorter than an invite, because it can
	// take over an account that is already in use.
	passwordLinkTTL = 3 * 24 * time.Hour

	// What a link is for: an invite sets a new account's first password; a
	// password reset link replaces an existing account's password.
	purposeInvite   = "invite"
	purposePassword = "password"
)

var (
	errInviteUnknown = fault.New(fault.NotFound, "this link is not valid — ask whoever gave it to you for a new one")
	errInviteUsed    = fault.New(fault.Conflict, "this link has already been used — sign in instead")
	errInviteRevoked = fault.New(fault.Conflict, "this link was withdrawn — ask whoever gave it to you for a new one")
	errInviteExpired = fault.New(fault.Conflict, "this link has expired — ask whoever gave it to you for a new one")
	errInviterGone   = fault.New(fault.Conflict, "whoever made this link is no longer an administrator — ask an administrator for a new one")
	errAccountExists = fault.New(fault.Conflict, "this account already has a password")
	errNoPassword    = fault.New(fault.Conflict, "this account has no password yet — invite them instead")
	errAlreadyAdmin  = fault.New(fault.Conflict, "this account already holds administrator authority")
)

// NewInvite is an administrator's invite: the account to create, an optional
// label (a display name or email address — AppTown sends no email), the
// role, and how many days the link lasts (0 means seven).
type NewInvite struct {
	Username string `json:"username"`
	Label    string `json:"label"`
	Role     string `json:"role"`
	Days     int    `json:"days"`
}

// NewPasswordLink is an administrator's password reset link: the account,
// which must already have a password, and how many days the link lasts (0
// means three).
type NewPasswordLink struct {
	Username string `json:"username"`
	Days     int    `json:"days"`
}

// Invite is an invite or a password reset link as the record keeps it,
// without its token. A password reset link has no role.
type Invite struct {
	ID        ids.ID `json:"id"`
	Purpose   string `json:"purpose"` // invite | password
	Username  string `json:"username"`
	Label     string `json:"label"`
	Role      string `json:"role"`
	InvitedBy string `json:"invitedBy"`
	// InvitedByPrincipal is the inviter's principal; zero when the inviter
	// was not resolved to a person.
	InvitedByPrincipal ids.ID    `json:"invitedByPrincipal,omitzero"`
	CreatedAt          time.Time `json:"createdAt"`
	ExpiresAt          time.Time `json:"expiresAt"`
	State              string    `json:"state"` // pending | accepted | revoked | expired
}

// issuedInvite releases the token once, after the invite and its receipt commit.
type issuedInvite struct {
	Invite Invite
	Token  string
}

// InviteAcceptance is the invitee's answer: the token from their link, their
// chosen password, and where they are connecting from.
type InviteAcceptance struct {
	Token, Password string
	From            net.IP
}

func invite(r accesssql.Invite, now time.Time) Invite {
	state := "pending"
	switch {
	case r.AcceptedAt.Valid:
		state = "accepted"
	case r.RevokedAt.Valid:
		state = "revoked"
	case !r.ExpiresAt.After(now):
		state = "expired"
	}
	return Invite{ID: r.ID, Purpose: r.Purpose, Username: r.Username, Label: r.Label, Role: r.Role, InvitedBy: r.InvitedBy, InvitedByPrincipal: r.InvitedByPrincipal,
		CreatedAt: r.CreatedAt, ExpiresAt: r.ExpiresAt, State: state}
}

// usable is nil for a pending invite and says plainly why any other cannot be used.
func (i Invite) usable() error {
	switch i.State {
	case "accepted":
		return errInviteUsed
	case "revoked":
		return errInviteRevoked
	case "expired":
		return errInviteExpired
	}
	return nil
}

func (in NewInvite) prepare() (NewInvite, time.Duration, error) {
	in.Username, in.Label = strings.TrimSpace(in.Username), strings.TrimSpace(in.Label)
	if !directoryName.MatchString(in.Username) {
		return in, 0, &AccountInputError{Reason: "username (slug) required"}
	}
	if err := ValidName(in.Username); err != nil {
		return in, 0, err
	}
	if in.Role == "" {
		in.Role = "member"
	}
	if in.Role != "admin" && in.Role != "member" {
		return in, 0, &AccountInputError{Reason: "role must be admin or member"}
	}
	if len(in.Label) > 200 {
		return in, 0, &AccountInputError{Reason: "the label is at most 200 characters"}
	}
	ttl, err := linkLifetime(in.Days, inviteTTL)
	return in, ttl, err
}

func linkLifetime(days int, fallback time.Duration) (time.Duration, error) {
	if days < 0 || days > maxInviteDays {
		return 0, &AccountInputError{Reason: fmt.Sprintf("a link lasts 1 to %d days", maxInviteDays)}
	}
	if days == 0 {
		return fallback, nil
	}
	return time.Duration(days) * 24 * time.Hour, nil
}

// Invite records an invite for an account that has no password yet. A
// pending invite for the same name is withdrawn by the new one, so a lost
// link is replaced rather than left working beside it.
func (s *Service) Invite(ctx context.Context, in NewInvite, pr access.Principal) (issuedInvite, error) {
	if err := ctx.Err(); err != nil {
		return issuedInvite{}, err
	}
	in, ttl, err := in.prepare()
	if err != nil {
		return issuedInvite{}, err
	}
	token := InvitePrefix + RandomHex(32)
	var issued issuedInvite
	err = s.administer(ctx, pr, access.CapIdentityUsers, func(tx *database.Tx, now time.Time) error {
		q := queries(tx)
		row, err := q.UserByNameForUpdate(ctx, in.Username)
		if found, err := database.Lookup(err); err != nil {
			return err
		} else if found && row.PasswordHash != "" {
			return fmt.Errorf("%w: %s can already sign in — reset their password instead", errAccountExists, in.Username)
		}
		// The invite's role is what the person gets: a member invite for a
		// name that already holds administrator authority would sign in an
		// administrator under a member's invite.
		if in.Role == "member" {
			if root, err := rootIn(ctx, tx, in.Username, now); err != nil {
				return err
			} else if root {
				return fmt.Errorf("%w: %s — make an admin invite instead, or remove that authority first", errAlreadyAdmin, in.Username)
			}
		}
		inviter, err := ActorIn(ctx, tx, pr.Name)
		if err != nil {
			return err
		}
		// The invite is for one person: their account is recorded now, and
		// the link sets a password on it and no other.
		if err := recordPersonIn(ctx, tx, in.Username, now); err != nil {
			return err
		}
		invitee, err := q.UserByName(ctx, in.Username)
		if err != nil {
			return err
		}
		r, replaced, err := insertLinkIn(ctx, q, accesssql.InsertInviteParams{ID: ids.New(now.UTC()), Purpose: purposeInvite, Username: in.Username, PrincipalID: invitee.PrincipalID, Label: in.Label, Role: in.Role,
			InvitedBy: pr.Name, InvitedByPrincipal: inviter.Principal, InvitedByVia: inviter.Via, TokenHash: HashToken(token), CreatedAt: now.UTC(), ExpiresAt: now.UTC().Add(ttl)}, now)
		if err != nil {
			return err
		}
		issued = issuedInvite{Invite: invite(r, now), Token: token}
		return audit.At(now).RecordIn(ctx, tx, pr.Name, ids.Nil, "user.invited", in.Username,
			map[string]any{"invite": r.ID, "role": r.Role, "expiresAt": r.ExpiresAt, "replaced": replaced})
	})
	if err != nil {
		return issuedInvite{}, err
	}
	return issued, nil
}

// PasswordLink records a password reset link for an account that has a
// password and is not disabled. The account's password and sessions keep
// working until the link is used. A pending link or invite for the same
// name is withdrawn by the new one.
func (s *Service) PasswordLink(ctx context.Context, in NewPasswordLink, pr access.Principal) (issuedInvite, error) {
	if err := ctx.Err(); err != nil {
		return issuedInvite{}, err
	}
	in.Username = strings.TrimSpace(in.Username)
	if !directoryName.MatchString(in.Username) {
		return issuedInvite{}, &AccountInputError{Reason: "username (slug) required"}
	}
	ttl, err := linkLifetime(in.Days, passwordLinkTTL)
	if err != nil {
		return issuedInvite{}, err
	}
	token := InvitePrefix + RandomHex(32)
	var issued issuedInvite
	err = s.administer(ctx, pr, access.CapIdentityUsers, func(tx *database.Tx, now time.Time) error {
		q := queries(tx)
		row, err := q.UserByNameForUpdate(ctx, in.Username)
		if found, err := database.Lookup(err); err != nil {
			return err
		} else if !found || row.PasswordHash == "" {
			return fmt.Errorf("%w: %s", errNoPassword, in.Username)
		} else if row.DisabledAt.Valid {
			return fault.Wrap(fault.Forbidden, fmt.Errorf("%w — enable it first", ErrAccountDisabled))
		}
		issuer, err := ActorIn(ctx, tx, pr.Name)
		if err != nil {
			return err
		}
		r, replaced, err := insertLinkIn(ctx, q, accesssql.InsertInviteParams{ID: ids.New(now.UTC()), Purpose: purposePassword, Username: in.Username, PrincipalID: row.PrincipalID,
			InvitedBy: pr.Name, InvitedByPrincipal: issuer.Principal, InvitedByVia: issuer.Via, TokenHash: HashToken(token), CreatedAt: now.UTC(), ExpiresAt: now.UTC().Add(ttl)}, now)
		if err != nil {
			return err
		}
		issued = issuedInvite{Invite: invite(r, now), Token: token}
		return audit.At(now).RecordIn(ctx, tx, pr.Name, ids.Nil, "user.password-link", in.Username,
			map[string]any{"link": r.ID, "expiresAt": r.ExpiresAt, "replaced": replaced})
	})
	if err != nil {
		return issuedInvite{}, err
	}
	return issued, nil
}

// insertLinkIn withdraws the name's pending links, so a lost one is replaced
// rather than left working beside the new one, and records the new link.
func insertLinkIn(ctx context.Context, q *accesssql.Queries, p accesssql.InsertInviteParams, now time.Time) (accesssql.Invite, []ids.ID, error) {
	pending, err := q.PendingInvitesForUpdate(ctx, accesssql.PendingInvitesForUpdateParams{PrincipalID: p.PrincipalID, Username: p.Username})
	if err != nil {
		return accesssql.Invite{}, nil, err
	}
	now = now.UTC()
	replaced := []ids.ID{}
	for _, old := range pending {
		if err := settleInviteIn(ctx, q, old.ID, accesssql.SettleInviteParams{RevokedAt: sql.NullTime{Time: now, Valid: true}, RevokedBy: p.InvitedBy, RevokedByPrincipal: p.InvitedByPrincipal, RevokedByVia: p.InvitedByVia}); err != nil {
			return accesssql.Invite{}, nil, err
		}
		replaced = append(replaced, old.ID)
	}
	r, err := q.InsertInvite(ctx, p)
	return r, replaced, err
}

// Invites lists the invites still waiting to be used, for an administrator.
func (s *Service) Invites(ctx context.Context, pr access.Principal) ([]Invite, error) {
	now := s.now()
	if err := s.requireAdministration(ctx, s.db, pr, access.CapIdentityUsers, now); err != nil {
		return nil, err
	}
	rows, err := queries(s.db).PendingInvites(ctx, now)
	out := make([]Invite, 0, len(rows))
	for _, r := range rows {
		out = append(out, invite(r, now))
	}
	return out, err
}

// RevokeInvite withdraws an invite that has not been used.
func (s *Service) RevokeInvite(ctx context.Context, id ids.ID, pr access.Principal) error {
	return s.administer(ctx, pr, access.CapIdentityUsers, func(tx *database.Tx, now time.Time) error {
		q := queries(tx)
		r, err := q.InviteByIDForUpdate(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			return fault.New(fault.NotFound, "no such invite")
		} else if err != nil {
			return err
		}
		if inv := invite(r, now); inv.State == "accepted" || inv.State == "revoked" {
			return inv.usable()
		}
		now = now.UTC()
		revoker, err := ActorIn(ctx, tx, pr.Name)
		if err != nil {
			return err
		}
		if err := settleInviteIn(ctx, q, id, accesssql.SettleInviteParams{RevokedAt: sql.NullTime{Time: now, Valid: true}, RevokedBy: pr.Name, RevokedByPrincipal: revoker.Principal, RevokedByVia: revoker.Via}); err != nil {
			return err
		}
		return audit.At(now).RecordIn(ctx, tx, pr.Name, ids.Nil, "user.invite-revoked", r.Username, map[string]any{"invite": id, "purpose": r.Purpose})
	})
}

// InviteFor is what an invite link shows before it is used: whose account,
// from whom, and whether it can still be used. The token is the credential.
func (s *Service) InviteFor(ctx context.Context, token string) (Invite, error) {
	r, err := queries(s.db).InviteByHash(ctx, HashToken(token))
	if errors.Is(err, sql.ErrNoRows) {
		return Invite{}, errInviteUnknown
	} else if err != nil {
		return Invite{}, err
	}
	return invite(r, s.now()), nil
}

// AcceptInvite sets the invited account's password and signs its holder in,
// consuming the invite in the same transaction. The token grants nothing
// else: an invite cannot change an account that already has a password, a
// password reset link can change only one that has, and either works only
// while whoever made it is still an administrator. A password reset ends
// every session the account had and leaves its authority as it was.
func (s *Service) AcceptInvite(ctx context.Context, in InviteAcceptance) (SignedIn, error) {
	if err := ctx.Err(); err != nil {
		return SignedIn{}, err
	}
	if len(in.Password) < 8 {
		return SignedIn{}, &AccountInputError{Reason: "passwords are at least 8 characters"}
	}
	// Refuse a dead link, and an administrator outside the allowed networks,
	// before paying bcrypt's cost or consuming anything.
	seen, err := s.InviteFor(ctx, in.Token)
	if err != nil {
		return SignedIn{}, err
	}
	if err := seen.usable(); err != nil {
		return SignedIn{}, err
	}
	if seen.Role == "admin" {
		if err := s.administratorAdmission(ctx, seen.Username, in.From, "invite"); err != nil {
			return SignedIn{}, err
		}
	}
	hash, err := hashPassword(in.Password)
	if err != nil {
		return SignedIn{}, err
	}
	now := s.now()
	return database.Transact(ctx, s.db, func(tx *database.Tx) (SignedIn, error) {
		if err := LockUsersIn(ctx, tx); err != nil {
			return SignedIn{}, err
		}
		q := queries(tx)
		r, err := q.InviteByHashForUpdate(ctx, HashToken(in.Token))
		if errors.Is(err, sql.ErrNoRows) {
			return SignedIn{}, errInviteUnknown
		} else if err != nil {
			return SignedIn{}, err
		}
		inv := invite(r, now)
		if err := inv.usable(); err != nil {
			return SignedIn{}, err
		}
		if err := inviterCurrentIn(ctx, tx, r.InvitedBy, r.InvitedByPrincipal, now); err != nil {
			return SignedIn{}, err
		}
		reset := inv.Purpose == purposePassword
		// The link acts on the person it was made for, by principal: renamed,
		// they keep it; a later account with the old name is someone else. A
		// link naming no principal (one the identity cutover could not tie to
		// an account) acts on no one.
		if r.PrincipalID.IsZero() {
			return SignedIn{}, errInviteUnknown
		}
		row, err := q.UserByPrincipalForUpdate(ctx, r.PrincipalID)
		found, err := database.Lookup(err)
		if !found && err == nil {
			return SignedIn{}, errInviteUnknown
		}
		if found {
			inv.Username = row.Username
		}
		switch {
		case err != nil:
			return SignedIn{}, err
		case reset && (!found || row.PasswordHash == ""):
			return SignedIn{}, errNoPassword
		case !reset && found && row.PasswordHash != "":
			return SignedIn{}, fmt.Errorf("%w — sign in instead", errAccountExists)
		case found && row.DisabledAt.Valid:
			return SignedIn{}, fault.Wrap(fault.Forbidden, ErrAccountDisabled)
		}
		// The administrator networks apply whenever the session will be an
		// administrator's: from this invite, or from authority the account
		// gained since the invite was made (directly or through a group).
		// Refusing here rolls back, so the invite stays pending.
		admin := inv.Role == "admin"
		if !admin {
			if admin, err = rootIn(ctx, tx, inv.Username, now); err != nil {
				return SignedIn{}, err
			}
		}
		if admin {
			if err := s.administratorAdmission(ctx, inv.Username, in.From, "invite"); err != nil {
				return SignedIn{}, err
			}
		}
		// The holder sets their own password; the role is the inviter's
		// grant. Setting a password ends every session the account had
		// (changeAccountIn), which is what makes a reset take the account
		// back; no role is passed, so its authority stays as it is.
		if err := changeAccountIn(ctx, tx, accountEdit{Name: inv.Username, PasswordHash: hash}, inv.Username, now); err != nil {
			return SignedIn{}, err
		}
		if inv.Role == "admin" {
			// The grant is the maker's, as the invite recorded them: their
			// principal and way, not whoever holds their name now.
			maker := access.WithCaller(ctx, access.Caller{Verified: true, Receipt: inv.InvitedBy,
				Principal: access.Principal{ID: r.InvitedByPrincipal, Kind: "user", Name: inv.InvitedBy, Via: r.InvitedByVia}})
			if err := adminGrantIn(maker, tx, inv.Username, inv.InvitedBy, now); err != nil {
				return SignedIn{}, err
			}
		}
		if err := settleInviteIn(ctx, q, inv.ID, accesssql.SettleInviteParams{AcceptedAt: sql.NullTime{Time: now.UTC(), Valid: true}}); err != nil {
			return SignedIn{}, err
		}
		kind, receipt := "user.invite-accepted", map[string]any{"invite": inv.ID, "invitedBy": inv.InvitedBy, "role": inv.Role}
		if reset {
			kind, receipt = "user.password-link-used", map[string]any{"link": inv.ID, "issuedBy": inv.InvitedBy}
		}
		if err := audit.At(now).RecordIn(ctx, tx, inv.Username, ids.Nil, kind, inv.Username, receipt); err != nil {
			return SignedIn{}, err
		}
		token, err := issueSessionIn(ctx, tx, inv.Username, s.cfg.sessionTTL, now)
		if err != nil {
			return SignedIn{}, &sessionIssueError{Cause: err}
		}
		saved, err := q.UserByName(ctx, inv.Username)
		if err != nil {
			return SignedIn{}, err
		}
		a, err := accountIn(ctx, tx, saved, now)
		if err != nil {
			return SignedIn{}, err
		}
		return SignedIn{Principal: a.principal("session"), Token: token}, nil
	})
}

// inviterCurrentIn holds an invite or a password reset link to its maker's
// current authority: an administrator who has since been demoted or disabled
// no longer adds people or resets passwords.
func inviterCurrentIn(ctx context.Context, tx *database.Tx, inviter string, principal ids.ID, now time.Time) error {
	// The maker by principal: a renamed maker is still themselves, and
	// someone later given their old name is not. A maker the link names no
	// principal for vouches for nothing.
	if principal.IsZero() {
		return errInviterGone
	}
	row, err := queries(tx).UserByPrincipal(ctx, principal)
	found, err := database.Lookup(err)
	if err != nil {
		return err
	}
	if !found || row.DisabledAt.Valid {
		return errInviterGone
	}
	root, err := rootIn(ctx, tx, row.Username, now)
	if err != nil {
		return err
	}
	if !root {
		return errInviterGone
	}
	return nil
}

func settleInviteIn(ctx context.Context, q *accesssql.Queries, id ids.ID, p accesssql.SettleInviteParams) error {
	p.ID = id
	n, err := q.SettleInvite(ctx, p)
	if err != nil {
		return err
	}
	if n != 1 {
		return errInviteUsed
	}
	return nil
}
