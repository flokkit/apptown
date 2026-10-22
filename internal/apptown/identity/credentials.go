package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/strs"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/identity/internal/accesssql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/stamp"
)

// Credentials (auth.md §1, §4; authorization-capabilities.md §10, §13):
// a session is a browser's proof of a login, a personal token is a
// command line's, and both are bounds on the person's current authority,
// never frozen authority of their own. Only hashes are stored; the
// plaintext is returned once. A decision receipt records what the rule
// decided and on what grounds, and a denial of a consequential act is
// also a ledger line pointing at its receipt.

const (
	SessionTTL  = 30 * 24 * time.Hour
	TokenPrefix = "apptown_"
)

// HashToken is the stored form of any presented secret.
func HashToken(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}

// RandomHex is n random bytes as hex.
func RandomHex(n int) string {
	raw := make([]byte, n)
	_, _ = rand.Read(raw)
	return hex.EncodeToString(raw)
}

// --- sessions -------------------------------------------------------------------

// sessionStore persists browser sessions.
type sessionStore struct {
	db  *database.DB
	ttl time.Duration
	now func() time.Time
}

// issueSessionIn starts a session and returns the cookie value once. A
// failed write returns an error so no cookie names a session that was never
// stored.
func issueSessionIn(ctx context.Context, q database.Queryer, username string, ttl time.Duration, now time.Time) (string, error) {
	tok := RandomHex(32)
	_, err := queries(q).InsertSession(ctx, accesssql.InsertSessionParams{ID: ids.New(now.UTC()), TokenHash: HashToken(tok), Username: username, CreatedAt: now.UTC(), ExpiresAt: now.Add(ttl).UTC()})
	if err != nil {
		return "", err
	}
	return tok, nil
}

// Resolve is the person behind a cookie value and the session's id (its
// non-secret reference), false for an unknown or expired session; an
// expired row is removed on sight.
func (s *sessionStore) Resolve(ctx context.Context, tok string) (username string, id ids.ID, ok bool, err error) {
	q := queries(s.db)
	row, err := q.SessionByHash(ctx, HashToken(tok))
	found, err := database.Lookup(err)
	if err != nil || !found {
		return "", ids.Nil, false, err
	}
	now := s.now()
	if !row.ExpiresAt.After(now) {
		_, err := q.DeleteSession(ctx, row.TokenHash)
		return "", ids.Nil, false, err
	}
	return row.Username, row.ID, true, q.TouchSession(ctx, accesssql.TouchSessionParams{TokenHash: row.TokenHash, At: now.UTC()})
}

// End is a logout: the row goes, so a cookie value that leaks afterwards
// names nothing.
func (s *sessionStore) End(ctx context.Context, tok string) error {
	_, err := queries(s.db).DeleteSession(ctx, HashToken(tok))
	return err
}

// --- personal access tokens -------------------------------------------------------

// Token is a personal access token as the settings page lists it: never
// the hash.
type Token struct {
	Name, User, Role, CreatedAt string
	// Purpose is cli or agent; "" only on a revoked token made before
	// purposes existed.
	Purpose             string
	LastUsed, RevokedAt stamp.Time
}

func token(r accesssql.AuthTokensNamed) Token {
	return Token{Name: r.Name, User: r.Username, Role: r.Role, Purpose: r.Purpose, CreatedAt: r.CreatedAt.Format(time.RFC3339Nano),
		LastUsed: stamp.Time{NullTime: r.LastUsed}, RevokedAt: stamp.Time{NullTime: r.RevokedAt}}
}

// tokenStore persists personal access tokens.
type tokenStore struct {
	db  *database.DB
	now func() time.Time
}

// Resolve is the person, the role and the purpose a live token was minted
// with, false for an unknown or revoked token; a use is stamped. The role
// is the token's bound; the caller narrows it to the person's current role.
func (t *tokenStore) Resolve(ctx context.Context, tok string) (user, role, purpose string, ok bool, err error) {
	row, ok, err := t.resolve(ctx, tok)
	return row.Username, row.Role, row.Purpose, ok, err
}

func (t *tokenStore) resolve(ctx context.Context, tok string) (accesssql.AuthTokensNamed, bool, error) {
	q := queries(t.db)
	row, err := q.AuthTokenByHash(ctx, HashToken(tok))
	found, err := database.Lookup(err)
	if err != nil || !found || !token(row).live() {
		return accesssql.AuthTokensNamed{}, false, err
	}
	return row, true, q.TouchAuthToken(ctx, accesssql.TouchAuthTokenParams{TokenHash: row.TokenHash, At: t.now().UTC()})
}

// --- the account's own credential facts -------------------------------------------

// access.Role is an account's grant-derived role, found=false for an unknown name.
func (d *directoryStore) Role(ctx context.Context, username string) (role string, found bool, err error) {
	return RoleIn(ctx, d.db, username, d.now())
}

// NeedsBootstrap reports whether initial authority setup has never completed.
func (d *directoryStore) NeedsBootstrap(ctx context.Context) (bool, error) {
	return queries(d.db).NeedsBootstrap(ctx)
}

// SeedAdministrator promotes a listed account to admin at startup, creating
// it if needed. This explicit startup seed writes the root grant atomically.
func (d *directoryStore) SeedAdministrator(ctx context.Context, username string) error {
	if err := ValidName(username); err != nil {
		return err
	}
	now := d.now()
	return d.db.InTx(ctx, func(tx *database.Tx) error {
		if err := LockUsersIn(ctx, tx); err != nil {
			return err
		}
		person, err := personIn(ctx, tx, username, now)
		if err != nil {
			return err
		}
		if _, err := queries(tx).SeedAdministrator(ctx, accesssql.SeedAdministratorParams{Username: username, PrincipalID: person, At: now.UTC()}); err != nil {
			return err
		}
		if err := AdminGrantIn(ctx, tx, username, "apptown-startup", now); err != nil {
			return err
		}
		return completeSetupIn(ctx, tx)
	})
}

// --- decision receipts ------------------------------------------------------------

func recordDecision(requestContext context.Context, db *database.DB, pr access.Principal, d access.Channel, c access.Capability, res access.Resource, ctx access.Context, dec access.Decision, at time.Time) (ids.ID, error) {
	outcome := "deny"
	if dec.Allow {
		outcome = "allow"
	}
	grants, err := rawJSON(orEmpty(dec.GrantIDs))
	if err != nil {
		return ids.Nil, err
	}
	roles, err := rawJSON(orEmptyS(dec.Roles))
	if err != nil {
		return ids.Nil, err
	}
	paths, err := rawJSON(orEmptyS(dec.Paths))
	if err != nil {
		return ids.Nil, err
	}
	route, err := rawJSON(map[string]any{"route": ctx.Route})
	if err != nil {
		return ids.Nil, err
	}
	var id ids.ID
	err = db.InTx(requestContext, func(tx *database.Tx) error {
		bg := requestContext
		row, err := queries(tx).InsertDecision(bg, accesssql.InsertDecisionParams{ID: ids.New(at.UTC()),
			DecidedAt: at.UTC(), Outcome: outcome, ReasonCode: dec.Code, PrincipalType: pr.Kind, Principal: pr.Name,
			DelegationKind: d.Kind, DelegationID: d.ID, Capability: string(c), ResourceType: res.Type, ResourceID: res.ID, ProjectID: res.ProjectID,
			GrantIdsJson: grants, RolesJson: roles, MembershipPathsJson: paths,
			ContextJson: route, Reason: dec.Reason})
		if err != nil {
			return err
		}
		id = row.ID
		if dec.Allow {
			return nil
		}
		return audit.At(at).RecordDecisionIn(bg, tx, id, pr.Name, res.ProjectID, "authz.denied", string(c),
			map[string]any{"code": dec.Code, "reason": dec.Reason, "resource": res.Type + ":" + res.ID, "route": ctx.Route})
	})
	return id, err
}

func rawJSON(v any) (json.RawMessage, error) {
	text, err := strs.JSON(v)
	return json.RawMessage(text), err
}

func orEmpty(xs []ids.ID) []ids.ID {
	if xs == nil {
		return []ids.ID{}
	}
	return xs
}

func orEmptyS(xs []string) []string {
	if xs == nil {
		return []string{}
	}
	return xs
}
