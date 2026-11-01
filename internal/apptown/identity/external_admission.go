package identity

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"net"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/identity/internal/accesssql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/oidclogin"
)

// ExternalStart identifies the local intent. Principal comes from authentication,
// never JSON. Password is required only to link an existing local session.
type ExternalStart struct {
	Name, ReturnPath, Password string
	Link                       bool
	Principal                  access.Principal
	From                       net.IP
}

// externalFlow contains a redirect and the HttpOnly cookie value returned once.
type externalFlow struct{ Redirect, BrowserSecret string }

// externalSignedIn combines committed session and a previously admitted path.
type externalSignedIn struct {
	SignedIn
	ReturnPath string
	Lifetime   time.Duration
}

func externalRandom() (string, error) {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func externalNonce(verifier string) string { return HashToken("external-nonce:" + verifier) }
func externalReturn(path string) (string, error) {
	switch path {
	case "", "/":
		return "/", nil
	case "/people", "/account":
		return path, nil
	default:
		return "", oidclogin.ErrRejected
	}
}
func (s *Service) localLinkSession(ctx context.Context, q database.Queryer, pr access.Principal) (accesssql.SessionsNamed, error) {
	var zero accesssql.SessionsNamed
	if pr.Kind != "user" || pr.Via != "session" || pr.ID.IsZero() {
		return zero, ErrCredentials
	}
	raw, ok := strings.CutPrefix(pr.Credential, "session:")
	if !ok {
		return zero, ErrCredentials
	}
	id, e := ids.Parse(raw)
	if e != nil {
		return zero, ErrCredentials
	}
	row, e := queries(q).SessionByID(ctx, id)
	if e != nil {
		return zero, e
	}
	if row.PrincipalID != pr.ID || !row.ExpiresAt.After(s.now()) {
		return zero, ErrCredentials
	}
	external, e := queries(q).IsExternalSession(ctx, id)
	if e != nil {
		return zero, e
	}
	if external {
		return zero, ErrCredentials
	}
	return row, nil
}
func (s *Service) BeginExternal(ctx context.Context, in ExternalStart) (externalFlow, error) {
	if e := s.externalAvailable(); e != nil {
		return externalFlow{}, e
	}
	path, e := externalReturn(in.ReturnPath)
	if e != nil {
		return externalFlow{}, e
	}
	state, e := externalRandom()
	if e != nil {
		return externalFlow{}, e
	}
	verifier, e := externalRandom()
	if e != nil {
		return externalFlow{}, e
	}
	nonce := externalNonce(verifier)
	var local accesssql.SessionsNamed
	if in.Link {
		local, e = s.localLinkSession(ctx, s.db, in.Principal)
		if e != nil {
			return externalFlow{}, e
		}
		a, e := s.verifyLogin(ctx, PasswordLogin{Username: local.Username, Password: in.Password, From: in.From})
		if e != nil {
			return externalFlow{}, e
		}
		if a.PrincipalID != in.Principal.ID {
			return externalFlow{}, ErrCredentials
		}
	}
	var p accesssql.ExternalSigninProvider
	e = s.db.InTx(ctx, func(tx *database.Tx) error {
		if e := LockUsersIn(ctx, tx); e != nil {
			return e
		}
		q := queries(tx)
		var e error
		p, e = q.ExternalProviderCurrent(ctx)
		if e != nil {
			return e
		}
		if !p.Enabled {
			return errExternalDisabled
		}
		if p.CallbackOrigin != s.cfg.External.Origin {
			return errExternalChanged
		}
		purpose := "login"
		if in.Link {
			purpose = "link"
			if _, e = s.localLinkSession(ctx, tx, in.Principal); e != nil {
				return e
			}
		} else if in.Name != "" {
			if !directoryName.MatchString(in.Name) {
				return &AccountInputError{Reason: "username (slug) required"}
			}
			if e := ValidName(in.Name); e != nil {
				return e
			}
		}
		now := s.now()
		return q.InsertExternalTransaction(ctx, accesssql.InsertExternalTransactionParams{ID: ids.New(now), StateHash: HashToken(state), ProviderID: p.ID, ProviderRevision: p.Revision, VerifierHash: HashToken(verifier), NonceHash: HashToken(nonce), Purpose: purpose, LocalSessionID: local.ID, PrincipalID: local.PrincipalID, ProposedName: in.Name, Callback: p.CallbackOrigin + "/api/v1/auth/external/callback", ReturnPath: path, CreatedAt: now, ExpiresAt: now.Add(10 * time.Minute)})
	})
	if e != nil {
		return externalFlow{}, e
	}
	cfg, e := s.externalConfiguration(ctx, p)
	if e != nil {
		return externalFlow{}, e
	}
	redirect, e := s.cfg.External.Protocol.Begin(ctx, cfg, state, nonce, verifier, in.Link)
	if e != nil {
		return externalFlow{}, e
	}
	return externalFlow{Redirect: redirect, BrowserSecret: verifier}, nil
}
func (s *Service) CompleteExternal(ctx context.Context, state, verifier, code string, from net.IP) (externalSignedIn, error) {
	if e := s.externalAvailable(); e != nil {
		return externalSignedIn{}, e
	}
	if state == "" || len(verifier) != 43 {
		return externalSignedIn{}, oidclogin.ErrRejected
	}
	// Claim before remote work. A failed exchange requires a fresh flow; it never
	// leaves two parallel callbacks eligible to mint sessions.
	tr, e := queries(s.db).ConsumeExternalTransaction(ctx, accesssql.ConsumeExternalTransactionParams{StateHash: HashToken(state), VerifierHash: HashToken(verifier), At: s.now()})
	if e != nil {
		if errors.Is(e, sql.ErrNoRows) {
			return externalSignedIn{}, oidclogin.ErrRejected
		}
		return externalSignedIn{}, e
	}
	p, e := queries(s.db).ExternalProviderByID(ctx, tr.ProviderID)
	if e != nil {
		return externalSignedIn{}, e
	}
	if !p.Enabled || !p.CurrentSlot || p.Revision != tr.ProviderRevision {
		return externalSignedIn{}, errExternalChanged
	}
	nonce := externalNonce(verifier)
	if HashToken(nonce) != tr.NonceHash {
		return externalSignedIn{}, oidclogin.ErrRejected
	}
	cfg, e := s.externalConfiguration(ctx, p)
	if e != nil {
		return externalSignedIn{}, e
	}
	if cfg.Callback != tr.Callback {
		return externalSignedIn{}, errExternalChanged
	}
	evidence, e := s.cfg.External.Protocol.Complete(ctx, cfg, code, nonce, verifier, tr.Purpose == "link")
	if e != nil {
		return externalSignedIn{}, e
	}
	return database.Transact(ctx, s.db, func(tx *database.Tx) (externalSignedIn, error) {
		var out externalSignedIn
		if e := LockUsersIn(ctx, tx); e != nil {
			return out, e
		}
		q := queries(tx)
		current, e := q.ExternalProviderByID(ctx, p.ID)
		if e != nil {
			return out, e
		}
		if !current.Enabled || !current.CurrentSlot || current.Revision != tr.ProviderRevision {
			return out, errExternalChanged
		}
		b, e := q.ExternalBindingBySubject(ctx, accesssql.ExternalBindingBySubjectParams{ProviderID: p.ID, Issuer: evidence.Issuer, Subject: evidence.Subject})
		found, e := database.Lookup(e)
		if e != nil {
			return out, e
		}
		now := s.now()
		if tr.Purpose == "link" {
			row, e := q.SessionByID(ctx, tr.LocalSessionID)
			if e != nil {
				return out, e
			}
			pr := access.Principal{ID: tr.PrincipalID, Kind: "user", Name: row.Username, Via: "session", Credential: sessionCredential(tr.LocalSessionID)}
			if _, e := s.localLinkSession(ctx, tx, pr); e != nil {
				return out, e
			}
			if found && (b.RevokedAt.Valid || b.PrincipalID != tr.PrincipalID) {
				return out, ErrExternalUnbound
			}
			if !found {
				b = accesssql.ExternalSigninBinding{ID: ids.New(now), ProviderID: p.ID, Issuer: evidence.Issuer, Subject: evidence.Subject, PrincipalID: tr.PrincipalID, CreatedBy: tr.PrincipalID, CreatedAt: now}
				if e := insertExternalBinding(ctx, q, b); e != nil {
					return out, e
				}
			}
		} else if !found {
			if !current.AllowSignup || tr.ProposedName == "" {
				return out, ErrExternalUnbound
			}
			_, e := q.UserByName(ctx, tr.ProposedName)
			if !errors.Is(e, sql.ErrNoRows) {
				if e != nil {
					return out, e
				}
				return out, &AccountInputError{Reason: "that account name is already used; choose another name"}
			}
			if e := changeAccountIn(ctx, tx, accountEdit{Name: tr.ProposedName}, "external-signin", now); e != nil {
				return out, e
			}
			u, e := q.UserByName(ctx, tr.ProposedName)
			if e != nil {
				return out, e
			}
			if current.InitialGroup != "" {
				g, e := q.GroupByName(ctx, current.InitialGroup)
				if e != nil {
					return out, e
				}
				if g.RetiredAt.Valid {
					return out, errExternalChanged
				}
				if _, e := q.InsertMembership(ctx, accesssql.InsertMembershipParams{GroupName: g.Name, Username: u.Username, CreatedBy: "external-signin", CreatedByPrincipal: current.UpdatedBy, CreatedByVia: "standing-admission", CreatedAt: now}); e != nil {
					return out, e
				}
			}
			b = accesssql.ExternalSigninBinding{ID: ids.New(now), ProviderID: p.ID, Issuer: evidence.Issuer, Subject: evidence.Subject, PrincipalID: u.PrincipalID, CreatedBy: current.UpdatedBy, CreatedAt: now}
			if e := insertExternalBinding(ctx, q, b); e != nil {
				return out, e
			}
		}
		if b.RevokedAt.Valid {
			return out, ErrExternalUnbound
		}
		row, e := q.UserByPrincipal(ctx, b.PrincipalID)
		if e != nil {
			return out, e
		}
		a, e := accountIn(ctx, tx, row, now)
		if e != nil {
			return out, e
		}
		if e := a.admit(); e != nil {
			return out, e
		}
		if a.Role == "admin" {
			if e := s.administratorAdmission(ctx, a.Username, from, "external"); e != nil {
				return out, e
			}
		}
		ttl := min(time.Hour, s.sessions.ttl)
		token, e := issueSessionIn(ctx, tx, a.Username, ttl, now)
		if e != nil {
			return out, &sessionIssueError{Cause: e}
		}
		session, e := q.SessionByHash(ctx, HashToken(token))
		if e != nil {
			return out, e
		}
		if e := q.InsertExternalSession(ctx, accesssql.InsertExternalSessionParams{SessionID: session.ID, BindingID: b.ID, ProviderID: p.ID, ProviderRevision: p.Revision, AuthenticatedAt: sql.NullTime{Time: evidence.AuthenticatedAt, Valid: !evidence.AuthenticatedAt.IsZero()}}); e != nil {
			return out, e
		}
		if e := audit.At(now).RecordIn(ctx, tx, a.Username, ids.Nil, "auth.external-login", b.ID.String(), map[string]any{"provider": p.ID, "revision": p.Revision, "purpose": tr.Purpose}); e != nil {
			return out, e
		}
		pr := a.principal("session")
		pr.Credential = sessionCredential(session.ID)
		return externalSignedIn{SignedIn: SignedIn{Principal: pr, Token: token}, ReturnPath: tr.ReturnPath, Lifetime: ttl}, nil
	})
}
func insertExternalBinding(ctx context.Context, q *accesssql.Queries, b accesssql.ExternalSigninBinding) error {
	return q.InsertExternalBinding(ctx, accesssql.InsertExternalBindingParams{ID: b.ID, ProviderID: b.ProviderID, Issuer: b.Issuer, Subject: b.Subject, PrincipalID: b.PrincipalID, CreatedBy: b.CreatedBy, CreatedAt: b.CreatedAt})
}
