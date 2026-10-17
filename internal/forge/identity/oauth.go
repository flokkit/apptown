package identity

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/identity/internal/accesssql"
	"flokkit.io/forge/internal/stamp"
	"flokkit.io/forge/internal/strs"
)

// Delegations to connected agents (mcp.md §4; authorization-capabilities.md
// §9, §10) and object capabilities (§11). An OAuth client registers
// itself, a person consents on the forge's own login, and what the client
// holds is a delegation: a bound on the person's current authority, never
// a copy of their role. Codes are consumed once; refresh tokens rotate,
// and a rotated token presented again means it leaked, which ends the
// delegation. A git ticket is a short-lived credential for one push of
// one change from one expected tip.

const (
	AudienceUser  = "/mcp/user"
	CodeTTL       = 10 * time.Minute
	AccessTTL     = time.Hour
	RefreshTTL    = 30 * 24 * time.Hour
	AccessPrefix  = "forge_mcp_"
	RefreshPrefix = "forge_rt_"
	ClientPrefix  = "fc_"

	TicketPrefix = "forge_git_"
	TicketTTL    = 20 * time.Minute
	// TicketKindGit is the object-capability kind of a change git ticket.
	TicketKindGit = "git-change"
)

// Client is a registered OAuth client.
type Client struct {
	ID, Name     string
	RedirectURIs []string
}

func client(r accesssql.OauthClient) Client {
	c := Client{ID: r.ClientID, Name: r.Name}
	// A list that does not decode allows no redirect at all: the decoder can
	// fill part of a slice before it fails, and part of a list is not one.
	if err := json.Unmarshal([]byte(r.RedirectUris), &c.RedirectURIs); err != nil {
		c.RedirectURIs = nil
	}
	return c
}

// authorizationCode is the stored consent awaiting exchange.
type authorizationCode struct {
	ClientID, User, Redirect, Resource, Challenge string
	Scopes                                        []string
	AuthTime, Expires                             time.Time
	Used                                          stamp.Time
}

func code(r accesssql.OauthCodesNamed) authorizationCode {
	return authorizationCode{ClientID: r.ClientID, User: r.Username, Redirect: r.RedirectUri, Resource: r.Resource, Challenge: r.CodeChallenge,
		Scopes: strings.Fields(r.Scopes), AuthTime: r.AuthTime, Expires: r.ExpiresAt, Used: stamp.Time{NullTime: r.UsedAt}}
}

// Delegation is a person's delegation to one client, as the connected
// agents list shows it.
type Delegation struct {
	ID         ids.ID     `json:"id"`
	User       string     `json:"user"`
	ClientID   string     `json:"clientId"`
	ClientName string     `json:"clientName"`
	Audience   string     `json:"audience"`
	Scopes     []string   `json:"scopes"`
	AuthTime   stamp.Time `json:"authTime"`
	CreatedAt  string     `json:"createdAt"`
	LastUsed   stamp.Time `json:"lastUsed"`
	ExpiresAt  stamp.Time `json:"expiresAt"`
	RevokedAt  stamp.Time `json:"revokedAt,omitzero"`
	refresh    refreshCredential
}

func connection(r accesssql.DelegationsNamed) Delegation {
	c := Delegation{ID: r.ID, User: r.Username, ClientID: r.ClientID, ClientName: r.ClientName, Audience: r.Audience,
		AuthTime: stamp.Time{NullTime: r.AuthTime}, CreatedAt: r.CreatedAt.Format(time.RFC3339Nano), LastUsed: stamp.Time{NullTime: r.LastUsed},
		ExpiresAt: stamp.Time{NullTime: r.ExpiresAt}, RevokedAt: stamp.Time{NullTime: r.RevokedAt}}
	// Scopes that do not decode grant none: a partly decoded list is not the
	// consent that was given.
	if err := json.Unmarshal(r.ScopesJson, &c.Scopes); err != nil {
		c.Scopes = nil
	}
	return c
}

// LiveAt reports whether the delegation may still be exercised at now, the
// forge's clock: unrevoked and unexpired.
func (d *Delegation) LiveAt(now time.Time) bool {
	return d != nil && !d.RevokedAt.Valid && !d.ExpiresAt.PassedAt(now)
}

// Pair is one access and refresh token, plaintext, handed out once.
type Pair struct {
	Access, Refresh string
	Scopes          []string
}

// oauthStore persists clients, codes, delegations and their tokens.
type oauthStore struct {
	db    *database.DB
	clock func() time.Time // nil is the real clock
}

func (o *oauthStore) now() time.Time { return clock.Or(o.clock)() }

// RegisterClient records a self-registering client with its receipt.
func (o *oauthStore) RegisterClient(ctx context.Context, name string, redirectURIs []string) (Client, error) {
	c := Client{ID: ClientPrefix + RandomHex(12), Name: strings.TrimSpace(name), RedirectURIs: redirectURIs}
	if c.Name == "" {
		c.Name = "a coding agent"
	}
	uris, err := strs.JSON(redirectURIs)
	if err != nil {
		return Client{}, err
	}
	at := o.now()
	err = o.db.InTx(ctx, func(tx *database.Tx) error {
		if _, err := queries(tx).InsertClient(ctx, accesssql.InsertClientParams{ClientID: c.ID, Name: c.Name, RedirectUris: uris, CreatedAt: at.UTC()}); err != nil {
			return err
		}
		return audit.At(at).RecordIn(ctx, tx, "forge", ids.Nil, "oauth.client-registered", c.Name, map[string]any{"clientId": c.ID, "redirectUris": redirectURIs})
	})
	return c, err
}

// ClientByID is a client, nil when unknown.
func (o *oauthStore) ClientByID(ctx context.Context, id string) (*Client, error) {
	r, err := queries(o.db).ClientByID(ctx, id)
	found, err := database.Lookup(err)
	if err != nil || !found {
		return nil, err
	}
	c := client(r)
	return &c, nil
}

// IssueCode records a person's consent and returns the code once, with
// the `oauth.authorized` receipt in the same transaction.
func (o *oauthStore) IssueCode(ctx context.Context, c Client, user, redirectURI string, scopes []string, resource, challenge string) (string, error) {
	code := RandomHex(32)
	now := o.now().UTC()
	err := o.db.InTx(ctx, func(tx *database.Tx) error {
		if _, err := queries(tx).InsertCode(ctx, accesssql.InsertCodeParams{CodeHash: HashToken(code), ClientID: c.ID, Username: user, RedirectUri: redirectURI,
			Scopes: strings.Join(scopes, " "), Resource: resource, CodeChallenge: challenge, AuthTime: now, ExpiresAt: now.Add(CodeTTL)}); err != nil {
			return err
		}
		return audit.At(now).RecordIn(ctx, tx, user, ids.Nil, "oauth.authorized", c.Name, map[string]any{"clientId": c.ID, "scopes": scopes})
	})
	if err != nil {
		return "", err
	}
	return code, nil
}

// CodeByValue is the record behind a presented code, nil when unknown.
func (o *oauthStore) CodeByValue(ctx context.Context, value string) (*authorizationCode, error) {
	r, err := queries(o.db).CodeByHash(ctx, HashToken(value))
	found, err := database.Lookup(err)
	if err != nil || !found {
		return nil, err
	}
	c := code(r)
	return &c, nil
}

// RevokeMintedBy ends what a replayed code minted: every token of the
// delegations this client got for this person since the code was used.
func (o *oauthStore) RevokeMintedBy(ctx context.Context, c *authorizationCode) error {
	if !c.Used.Valid {
		return nil
	}
	_, err := queries(o.db).RevokeTokensMintedSince(ctx, accesssql.RevokeTokensMintedSinceParams{ClientID: c.ClientID, Username: c.User, Since: c.Used.Time, At: o.now().UTC()})
	return err
}

// Exchange consumes a code once and mints the delegation with its first
// token pair, the client's last-use stamp and the `oauth.connected`
// receipt, all in one transaction. The consuming update decides by its
// affected rows: two concurrent exchanges of one code see one success and
// one replay (replayed=true, nothing minted). A failure anywhere leaves
// the code unused so the client may retry.
func (o *oauthStore) Exchange(ctx context.Context, value string, c *authorizationCode, clientName, authMethod string) (delegationID ids.ID, pair Pair, replayed bool, err error) {
	now := o.now().UTC()
	err = o.db.InTx(ctx, func(tx *database.Tx) error {
		q := queries(tx)
		n, err := q.ConsumeCode(ctx, accesssql.ConsumeCodeParams{CodeHash: HashToken(value), At: now})
		if err != nil {
			return err
		}
		if n == 0 {
			// The failed conditional update waited for the winner. Its stored
			// consumption time, not this loser's wall clock, bounds revocation.
			row, err := q.CodeByHash(ctx, HashToken(value))
			found, err := database.Lookup(err)
			if err != nil {
				return err
			}
			if found {
				*c = code(row)
			}
			replayed = true
			return nil
		}
		scopes, err := rawJSON(c.Scopes)
		if err != nil {
			return err
		}
		d, err := q.InsertDelegation(ctx, accesssql.InsertDelegationParams{ID: ids.New(now), Username: c.User, ClientID: c.ClientID, ClientName: clientName, Audience: AudienceUser,
			ScopesJson: scopes, AuthTime: sql.NullTime{Time: c.AuthTime, Valid: true}, AuthMethod: authMethod, CreatedAt: now,
			ExpiresAt: sql.NullTime{Time: now.Add(RefreshTTL), Valid: true}})
		if err != nil {
			return err
		}
		delegationID = d
		if err := q.TouchClient(ctx, accesssql.TouchClientParams{ClientID: c.ClientID, At: now}); err != nil {
			return err
		}
		if err := audit.At(now).RecordIn(ctx, tx, c.User, ids.Nil, "oauth.connected", clientName, map[string]any{"clientId": c.ClientID, "delegationId": delegationID, "scopes": c.Scopes}); err != nil {
			return err
		}
		pair, err = mintPairIn(ctx, tx, delegationID, now)
		return err
	})
	return delegationID, pair, replayed, err
}

// mintPairIn records a new token pair minted at now, the caller's clock.
func mintPairIn(ctx context.Context, tx *database.Tx, delegationID ids.ID, now time.Time) (Pair, error) {
	p := Pair{Access: AccessPrefix + RandomHex(24), Refresh: RefreshPrefix + RandomHex(24)}
	now = now.UTC()
	n, err := queries(tx).InsertTokenPair(ctx, accesssql.InsertTokenPairParams{DelegationID: delegationID, At: now,
		AccessHash: HashToken(p.Access), AccessExpires: now.Add(AccessTTL), RefreshHash: HashToken(p.Refresh), RefreshExpires: now.Add(RefreshTTL)})
	if err == nil && n != 2 {
		err = errors.New("token pair was not recorded")
	}
	return p, err
}

// tokenRecord is what a presented access or refresh token points at.
type tokenRecord struct {
	DelegationID ids.ID
	Expires      time.Time
	Revoked      bool
}

// Token is the record behind a presented token of one kind, nil when
// unknown.
func (o *oauthStore) Token(ctx context.Context, tok, kind string) (*tokenRecord, error) {
	r, err := queries(o.db).TokenByHashAndKind(ctx, accesssql.TokenByHashAndKindParams{TokenHash: HashToken(tok), Kind: kind})
	found, err := database.Lookup(err)
	if err != nil || !found {
		return nil, err
	}
	return &tokenRecord{DelegationID: r.DelegationID, Expires: r.ExpiresAt, Revoked: r.RevokedAt.Valid}, nil
}

// DelegationOfToken is the delegation behind any token, found=false when
// unknown (RFC 7009: an unknown token is not an error).
func (o *oauthStore) DelegationOfToken(ctx context.Context, tok string) (ids.ID, bool, error) {
	r, err := queries(o.db).TokenByHash(ctx, HashToken(tok))
	found, err := database.Lookup(err)
	return r.DelegationID, found, err
}

// Connection is one delegation whatever its state, nil when unknown.
func (o *oauthStore) Connection(ctx context.Context, id ids.ID) (*Delegation, error) {
	r, err := queries(o.db).DelegationByID(ctx, id)
	found, err := database.Lookup(err)
	if err != nil || !found {
		return nil, err
	}
	c := connection(r)
	return &c, nil
}

// Connections is a person's live delegations, newest first, or everyone's.
func (o *oauthStore) Connections(ctx context.Context, user string, all bool) ([]Delegation, error) {
	q := queries(o.db)
	var rows []accesssql.DelegationsNamed
	var err error
	if all {
		rows, err = q.DelegationsLive(ctx)
	} else {
		rows, err = q.DelegationsLiveForUser(ctx, user)
	}
	out := make([]Delegation, 0, len(rows))
	for _, r := range rows {
		out = append(out, connection(r))
	}
	return out, err
}

// Revoke ends a delegation and every token it carries, with the
// `oauth.disconnected` receipt, in one transaction; a revoke never
// half-happens. An unknown delegation is ErrNoSuchName.
func (o *oauthStore) Revoke(ctx context.Context, id ids.ID, by string) error {
	return o.db.InTx(ctx, func(tx *database.Tx) error { return revokeDelegationIn(ctx, tx, id, by, o.now().UTC()) })
}

// revokeDelegationIn ends a delegation and its tokens as of at, the
// caller's clock.
func revokeDelegationIn(ctx context.Context, tx *database.Tx, id ids.ID, by string, at time.Time) error {
	q := queries(tx)
	d, err := q.DelegationByIDForUpdate(ctx, id)
	found, err := database.Lookup(err)
	if err != nil {
		return err
	}
	if !found {
		return ErrNoSuchName
	}
	object := connection(accesssql.DelegationsNamed(d))
	object.end(at)
	if err := saveDelegationIn(ctx, tx, accesssql.DelegationsNamed(d), object); err != nil {
		return err
	}
	if err := revokeDelegationTokensIn(ctx, tx, id, at); err != nil {
		return err
	}
	return audit.At(at).RecordIn(ctx, tx, by, ids.Nil, "oauth.disconnected", d.ClientName, map[string]any{"delegationId": id, "user": d.Username})
}

// SweepExpired drops what can never be presented again: expired consents,
// used or expired codes, expired or long-revoked tokens, expired or spent
// tickets; and marks expired delegations revoked so the connected-agents
// list stays honest. Delegations themselves are kept as the record of a
// person's consent.
func (o *oauthStore) SweepExpired(ctx context.Context) error {
	q := queries(o.db)
	now := o.now().UTC()
	cutoff := now.Add(-24 * time.Hour)
	if _, err := q.DeleteExpiredConsents(ctx, now); err != nil {
		return err
	}
	if _, err := q.DeleteSpentCodes(ctx, cutoff); err != nil {
		return err
	}
	if _, err := q.DeleteDeadTokens(ctx, cutoff); err != nil {
		return err
	}
	if _, err := q.DeleteDeadTickets(ctx, cutoff); err != nil {
		return err
	}
	_, err := q.RevokeExpiredDelegations(ctx, now)
	return err
}

// --- git tickets ------------------------------------------------------------------

// Ticket is one change's git capability: it lets a checkout push exactly
// one ref from exactly the tip it last saw, once, for a short while.
type Ticket struct {
	ID           ids.ID
	User         string
	DelegationID ids.ID
	ChangeID     ids.ID
	Ref          string // refs/heads/forge/change-N
	ExpectedTip  string // the hub's ref when minted; "" for a ref that does not exist yet
	ExpiresAt    string
	ConsumedAt   stamp.Time
	RevokedAt    stamp.Time
}

// Tickets is the supported API over git tickets.
type Tickets struct {
	db    *database.DB
	clock func() time.Time // nil is the real clock
}

func NewTickets(db *database.DB) *Tickets { return &Tickets{db: db} }

func (t *Tickets) now() time.Time { return clock.Or(t.clock)() }

// Mint writes the capability and returns its plaintext once.
func (t *Tickets) Mint(ctx context.Context, user string, delegationID, changeID ids.ID, ref, expectedTip string) (string, *Ticket, error) {
	tok := TicketPrefix + RandomHex(24)
	constraints := map[string]any{"ref": ref, "expected_tip": expectedTip, "operations": []string{"fetch-main", "fetch-change", "push-change"}}
	now := t.now().UTC()
	exp := now.Add(TicketTTL)
	constraintsJSON, err := rawJSON(constraints)
	if err != nil {
		return "", nil, err
	}
	r, err := queries(t.db).InsertTicket(ctx, accesssql.InsertTicketParams{ID: ids.New(now), Kind: TicketKindGit, Username: user, DelegationID: delegationID,
		ResourceType: "change", ResourceID: changeID.String(), ConstraintsJson: constraintsJSON, TokenHash: HashToken(tok), CreatedAt: now, ExpiresAt: exp})
	if err != nil {
		return "", nil, err
	}
	return tok, &Ticket{ID: r, User: user, DelegationID: delegationID, ChangeID: changeID, Ref: ref, ExpectedTip: expectedTip, ExpiresAt: exp.Format(time.RFC3339)}, nil
}

// ByToken resolves a presented ticket that is live: unexpired, unrevoked,
// not yet consumed. Nil otherwise.
func (t *Tickets) ByToken(ctx context.Context, tok string) (*Ticket, error) {
	if !strings.HasPrefix(tok, TicketPrefix) {
		return nil, nil
	}
	r, err := queries(t.db).TicketByHash(ctx, accesssql.TicketByHashParams{TokenHash: HashToken(tok), Kind: TicketKindGit})
	found, err := database.Lookup(err)
	if err != nil || !found {
		return nil, err
	}
	if r.RevokedAt.Valid || r.ConsumedAt.Valid || !r.ExpiresAt.After(t.now()) {
		return nil, nil
	}
	k, err := ticket(accesssql.ObjectCapabilitiesNamed(r))
	if err != nil {
		return nil, err
	}
	return &k, nil
}

// ticket reads a recorded ticket. One whose change or constraints cannot be
// read is an error: it is refused, never honoured for change 0 or no ref.
func ticket(r accesssql.ObjectCapabilitiesNamed) (Ticket, error) {
	k := Ticket{ID: r.ID, User: r.Username, DelegationID: r.DelegationID, ExpiresAt: r.ExpiresAt.Format(time.RFC3339),
		ConsumedAt: stamp.Time{NullTime: r.ConsumedAt}, RevokedAt: stamp.Time{NullTime: r.RevokedAt}}
	var err error
	if k.ChangeID, err = ids.Parse(r.ResourceID); err != nil {
		return Ticket{}, fmt.Errorf("ticket %d: reading its change: %w", r.ID, err)
	}
	var c struct {
		Ref         string `json:"ref"`
		ExpectedTip string `json:"expected_tip"`
	}
	if err := json.Unmarshal(r.ConstraintsJson, &c); err != nil {
		return Ticket{}, fmt.Errorf("ticket %d: reading its constraints: %w", r.ID, err)
	}
	k.Ref, k.ExpectedTip = c.Ref, c.ExpectedTip
	return k, nil
}

// Consume locks the capability, applies its rule, and saves it with its receipt.
// The guarded full-row write still detects a veto or a concurrently spent ticket.
func (t *Tickets) Consume(ctx context.Context, id ids.ID) (bool, error) {
	return database.Transact(ctx, t.db, func(tx *database.Tx) (bool, error) {
		q := queries(tx)
		r, err := q.TicketByIDForUpdate(ctx, id)
		found, err := database.Lookup(err)
		if err != nil || !found {
			return false, err
		}
		k, err := ticket(accesssql.ObjectCapabilitiesNamed(r))
		if err != nil {
			return false, err
		}
		at := t.now()
		if err := k.Consume(at.UTC()); err != nil {
			return false, nil
		}
		n, err := q.ConsumeTicket(ctx, accesssql.ConsumeTicketParams{
			ID: r.ID, Kind: r.Kind, DelegationID: r.DelegationID, ResourceType: r.ResourceType,
			ResourceID: r.ResourceID, ConstraintsJson: r.ConstraintsJson, TokenHash: r.TokenHash, CreatedAt: r.CreatedAt,
			ExpiresAt: r.ExpiresAt, ConsumedAt: k.ConsumedAt.NullTime, RevokedAt: r.RevokedAt,
		})
		if err != nil {
			return false, err
		}
		if n != 1 {
			return false, errors.New("ticket consumption was not recorded")
		}
		if err := audit.At(at).RecordIn(ctx, tx, k.User, ids.Nil, "git-ticket.consumed", r.ResourceID,
			map[string]any{"ticketId": k.ID, "ref": k.Ref, "expectedTip": k.ExpectedTip}); err != nil {
			return false, err
		}
		return true, nil
	})
}

// RevokeForChange ends every live ticket on a change: when it lands, is
// abandoned, or a run takes the workspace.
func (t *Tickets) RevokeForChange(ctx context.Context, changeID ids.ID) error {
	_, err := queries(t.db).RevokeChangeTickets(ctx, accesssql.RevokeChangeTicketsParams{RevokedAt: t.now().UTC(), Kind: TicketKindGit, ResourceID: changeID.String()})
	return err
}
