package identity

import (
	"context"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/identity/internal/accesssql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/oidclogin"
)

var errLocalRecovery = fault.New(fault.Conflict, "keep an enabled local-password administrator with nonexpiring authority before using external sign-in")
var errExternalChanged = fault.New(fault.Conflict, "external sign-in changed; reload its settings or start sign-in again")
var errExternalDisabled = fault.New(fault.Conflict, "external sign-in is not enabled")
var ErrExternalUnbound = fault.New(fault.Forbidden, "this external identity has no AppTown account binding; ask an administrator")

// ExternalDependencies supplies protocol and secret lookup to the identity owner.
// Origin is fixed startup configuration, never a request's Host header.
type ExternalDependencies struct {
	Protocol *oidclogin.Client
	Secret   func(context.Context, string) (string, error)
	Origin   string
	Internal bool
}

// ExternalProvider is a safe settings projection. Credentials are references.
type ExternalProvider struct {
	ID             ids.ID `json:"id"`
	Revision       int64  `json:"revision"`
	Issuer         string `json:"issuer"`
	DisplayName    string `json:"displayName"`
	ClientID       string `json:"clientId"`
	SecretRef      string `json:"secretRef"`
	CallbackOrigin string `json:"callbackOrigin"`
	Enabled        bool   `json:"enabled"`
	AllowSignup    bool   `json:"allowSignup"`
	InitialGroup   string `json:"initialGroup"`
}

// ExternalProviderChange saves disabled configuration using an expected revision.
type ExternalProviderChange struct {
	Revision     int64  `json:"revision"`
	Issuer       string `json:"issuer"`
	DisplayName  string `json:"displayName"`
	ClientID     string `json:"clientId"`
	SecretRef    string `json:"secretRef"`
	AllowSignup  bool   `json:"allowSignup"`
	InitialGroup string `json:"initialGroup"`
}

func externalView(p accesssql.ExternalSigninProvider) ExternalProvider {
	return ExternalProvider{p.ID, p.Revision, p.Issuer, p.DisplayName, p.ClientID, p.SecretRef, p.CallbackOrigin, p.Enabled, p.AllowSignup, p.InitialGroup}
}
func externalSave(p accesssql.ExternalSigninProvider) accesssql.SaveExternalProviderParams {
	return accesssql.SaveExternalProviderParams(p)
}
func (s *Service) externalAvailable() error {
	d := s.cfg.External
	if d == nil || !d.Internal || d.Protocol == nil || d.Secret == nil || d.Origin == "" {
		return errExternalDisabled
	}
	return nil
}
func (s *Service) ExternalProvider(ctx context.Context) (*ExternalProvider, error) {
	p, e := queries(s.db).ExternalProviderCurrent(ctx)
	found, e := database.Lookup(e)
	if e != nil || !found {
		return nil, e
	}
	v := externalView(p)
	return &v, nil
}
func (s *Service) SaveExternalProvider(ctx context.Context, in ExternalProviderChange, pr access.Principal) (ExternalProvider, error) {
	if e := s.externalAvailable(); e != nil {
		return ExternalProvider{}, e
	}
	u, e := url.Parse(in.Issuer)
	if e != nil || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil || strings.TrimSpace(in.ClientID) == "" || strings.TrimSpace(in.DisplayName) == "" || strings.TrimSpace(in.SecretRef) == "" {
		return ExternalProvider{}, oidclogin.ErrConfiguration
	}
	if e := s.cfg.External.Protocol.Validate(oidclogin.Configuration{Issuer: in.Issuer, ClientID: in.ClientID, Callback: s.cfg.External.Origin + "/api/v1/auth/external/callback"}); e != nil {
		return ExternalProvider{}, e
	}
	var out ExternalProvider
	e = s.administer(ctx, pr, access.CapIdentityUsers, func(tx *database.Tx, now time.Time) error {
		q := queries(tx)
		p, e := q.ExternalProviderCurrent(ctx)
		found, e := database.Lookup(e)
		if e != nil {
			return e
		}
		if (found && p.Revision != in.Revision) || (!found && in.Revision != 0) {
			return errExternalChanged
		}
		if in.InitialGroup != "" {
			g, e := q.GroupByName(ctx, in.InitialGroup)
			if e != nil {
				return e
			}
			if g.RetiredAt.Valid {
				return errExternalChanged
			}
		}
		previousRevision := p.Revision
		if found && (p.Issuer != in.Issuer || p.ClientID != in.ClientID) {
			p.CurrentSlot = false
			p.Enabled = false
			p.Revision++
			if e := q.SaveExternalProvider(ctx, externalSave(p)); e != nil {
				return e
			}
			found = false
		}
		if !found {
			p = accesssql.ExternalSigninProvider{ID: ids.New(now), CurrentSlot: true, Revision: previousRevision, CreatedBy: pr.ID, CreatedAt: now}
		}
		p.Revision++
		p.Issuer = in.Issuer
		p.ClientID = in.ClientID
		p.DisplayName = in.DisplayName
		p.SecretRef = in.SecretRef
		p.CallbackOrigin = s.cfg.External.Origin
		p.Enabled = false
		p.AllowSignup = in.AllowSignup
		p.InitialGroup = in.InitialGroup
		p.UpdatedBy = pr.ID
		p.UpdatedAt = now
		if e := q.SaveExternalProvider(ctx, externalSave(p)); e != nil {
			return e
		}
		out = externalView(p)
		return audit.At(now).RecordIn(ctx, tx, pr.Name, ids.Nil, "auth.external-configured", p.ID.String(), map[string]any{"revision": p.Revision, "allowSignup": p.AllowSignup, "initialGroup": p.InitialGroup})
	})
	return out, e
}
func (s *Service) SetExternalEnabled(ctx context.Context, revision int64, enabled bool, pr access.Principal) (ExternalProvider, error) {
	if e := s.externalAvailable(); e != nil {
		return ExternalProvider{}, e
	}
	var out ExternalProvider
	e := s.administer(ctx, pr, access.CapIdentityUsers, func(tx *database.Tx, now time.Time) error {
		q := queries(tx)
		p, e := q.ExternalProviderCurrent(ctx)
		if e != nil {
			return e
		}
		if p.Revision != revision {
			return errExternalChanged
		}
		if p.CallbackOrigin != s.cfg.External.Origin {
			return errExternalChanged
		}
		p.Enabled = enabled
		p.Revision++
		p.UpdatedAt = now
		p.UpdatedBy = pr.ID
		if e := q.SaveExternalProvider(ctx, externalSave(p)); e != nil {
			return e
		}
		out = externalView(p)
		return audit.At(now).RecordIn(ctx, tx, pr.Name, ids.Nil, "auth.external-enabled", p.ID.String(), map[string]any{"enabled": enabled, "revision": p.Revision})
	})
	return out, e
}
func (s *Service) externalConfiguration(ctx context.Context, p accesssql.ExternalSigninProvider) (oidclogin.Configuration, error) {
	if e := s.externalAvailable(); e != nil {
		return oidclogin.Configuration{}, e
	}
	if p.CallbackOrigin != s.cfg.External.Origin {
		return oidclogin.Configuration{}, errExternalChanged
	}
	secret, e := s.cfg.External.Secret(ctx, p.SecretRef)
	if e != nil || secret == "" {
		return oidclogin.Configuration{}, oidclogin.ErrUnavailable
	}
	return oidclogin.Configuration{Issuer: p.Issuer, ClientID: p.ClientID, Secret: secret, Callback: p.CallbackOrigin + "/api/v1/auth/external/callback"}, nil
}
func (s *Service) TestExternalProvider(ctx context.Context, pr access.Principal) error {
	if e := s.administer(ctx, pr, access.CapIdentityUsers, func(tx *database.Tx, now time.Time) error { return nil }); e != nil {
		return e
	}
	p, e := queries(s.db).ExternalProviderCurrent(ctx)
	if e != nil {
		return e
	}
	cfg, e := s.externalConfiguration(ctx, p)
	if e != nil {
		return e
	}
	return s.cfg.External.Protocol.Inspect(ctx, cfg)
}

// BindExternal explicitly associates the exact stable subject with a local UUID.
func (s *Service) BindExternal(ctx context.Context, providerID, person ids.ID, subject string, pr access.Principal) error {
	if strings.TrimSpace(subject) == "" || len(subject) > 255 {
		return oidclogin.ErrConfiguration
	}
	return s.administer(ctx, pr, access.CapIdentityUsers, func(tx *database.Tx, now time.Time) error {
		q := queries(tx)
		p, e := q.ExternalProviderByID(ctx, providerID)
		if e != nil {
			return e
		}
		if !p.CurrentSlot {
			return errExternalChanged
		}
		u, e := q.UserByPrincipal(ctx, person)
		if e != nil {
			return e
		}
		if u.DisabledAt.Valid {
			return ErrAccountDisabled
		}
		if e := q.InsertExternalBinding(ctx, accesssql.InsertExternalBindingParams{ID: ids.New(now), ProviderID: p.ID, Issuer: p.Issuer, Subject: subject, PrincipalID: person, CreatedBy: pr.ID, CreatedAt: now}); e != nil {
			return e
		}
		return audit.At(now).RecordIn(ctx, tx, pr.Name, ids.Nil, "auth.external-bound", person.String(), map[string]any{"provider": p.ID, "subject": subject})
	})
}
func (s *Service) RevokeExternalBinding(ctx context.Context, id ids.ID, pr access.Principal) error {
	return s.administer(ctx, pr, access.CapIdentityUsers, func(tx *database.Tx, now time.Time) error {
		n, e := queries(tx).RevokeExternalBinding(ctx, accesssql.RevokeExternalBindingParams{ID: id, At: now})
		if e != nil {
			return e
		}
		if n != 1 {
			return errExternalChanged
		}
		return audit.At(now).RecordIn(ctx, tx, pr.Name, ids.Nil, "auth.external-unbound", id.String(), nil)
	})
}

// ExternalBindingView makes the exact issuer/subject association visible.
type ExternalBindingView struct {
	ID          ids.ID `json:"id"`
	ProviderID  ids.ID `json:"providerId"`
	PrincipalID ids.ID `json:"principalId"`
	Issuer      string `json:"issuer"`
	Subject     string `json:"subject"`
	Revoked     bool   `json:"revoked"`
}

func bindingViews(rows []accesssql.ExternalSigninBinding) []ExternalBindingView {
	out := make([]ExternalBindingView, 0, len(rows))
	for _, b := range rows {
		out = append(out, ExternalBindingView{b.ID, b.ProviderID, b.PrincipalID, b.Issuer, b.Subject, b.RevokedAt.Valid})
	}
	return out
}
func (s *Service) ExternalBindings(ctx context.Context, pr access.Principal, all bool) ([]ExternalBindingView, error) {
	if all {
		if e := s.administer(ctx, pr, access.CapIdentityUsers, func(tx *database.Tx, now time.Time) error { return nil }); e != nil {
			return nil, e
		}
		p, e := queries(s.db).ExternalProviderCurrent(ctx)
		found, e := database.Lookup(e)
		if e != nil {
			return nil, e
		}
		if !found {
			return []ExternalBindingView{}, nil
		}
		rows, e := queries(s.db).ExternalBindings(ctx, p.ID)
		return bindingViews(rows), e
	}
	current, e := s.stillPrincipal(ctx, pr)
	if e != nil {
		return nil, e
	}
	rows, e := queries(s.db).ExternalBindingsForPrincipal(ctx, current.ID)
	return bindingViews(rows), e
}

// UnlinkExternal removes only the caller's association after local password
// verification. It leaves the local account and independent credentials intact.
func (s *Service) UnlinkExternal(ctx context.Context, id ids.ID, password string, pr access.Principal, from net.IP) error {
	current, e := s.stillPrincipal(ctx, pr)
	if e != nil {
		return e
	}
	a, e := s.verifyLogin(ctx, PasswordLogin{Username: current.Name, Password: password, From: from})
	if e != nil {
		return e
	}
	return s.db.InTx(ctx, func(tx *database.Tx) error {
		if e := LockUsersIn(ctx, tx); e != nil {
			return e
		}
		q := queries(tx)
		u, e := q.UserByPrincipal(ctx, current.ID)
		if e != nil {
			return e
		}
		if u.PasswordHash != a.passwordHash || u.DisabledAt.Valid {
			return ErrCredentials
		}
		b, e := q.ExternalBindingByID(ctx, id)
		if e != nil {
			return e
		}
		if b.PrincipalID != current.ID {
			return ErrExternalUnbound
		}
		n, e := q.RevokeExternalBinding(ctx, accesssql.RevokeExternalBindingParams{ID: id, At: s.now()})
		if e != nil {
			return e
		}
		if n != 1 {
			return errExternalChanged
		}
		return audit.At(s.now()).RecordIn(ctx, tx, current.Name, ids.Nil, "auth.external-unbound", id.String(), nil)
	})
}
