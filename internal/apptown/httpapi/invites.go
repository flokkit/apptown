package httpapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/flokkit/apptown/internal/apptown/identity"
)

// Invites are the routes for inviting a person by link, and for a password
// reset link, which is the same link for an account that already has a
// password (auth.md "Invites"). Creating, listing and revoking are an
// administrator's; reading and accepting take the link's token itself as
// the credential, in the request body so it never sits in an API URL.
type Invites struct {
	access   *identity.Service
	browser  BrowserSession
	address  ClientAddress
	internal bool // invites set passwords, which only internal sign-in uses
}

// The invite routes' responses.
type (
	// InviteLink carries a new invite's link, shown once.
	InviteLink struct {
		Invite identity.Invite `json:"invite"`
		Link   string          `json:"link"`
		Note   string          `json:"note"`
	}
	InviteList struct {
		Invites []identity.Invite `json:"invites"`
	}
	// InviteOffer is what the invite link's page shows before anyone signs in.
	InviteOffer struct {
		Purpose   string    `json:"purpose"` // invite | password
		Username  string    `json:"username"`
		Role      string    `json:"role"`
		InvitedBy string    `json:"invitedBy"`
		ExpiresAt time.Time `json:"expiresAt"`
		State     string    `json:"state"`
	}
	// SignedInAs is who a browser is signed in as after an accepted invite.
	SignedInAs struct {
		OK   bool   `json:"ok"`
		User string `json:"user"`
		Role string `json:"role"`
	}
)

func NewInvites(access *identity.Service, browser BrowserSession, address ClientAddress, internal bool) Invites {
	return Invites{access: access, browser: browser, address: address, internal: internal}
}

// What each kind of link says outside AppTown's own sign-in: with single
// sign-on, people and their passwords live in the identity provider.
const (
	invitesNeedInternal       = "Invites need AppTown's own sign-in (--auth internal); with single sign-on, add people in your identity provider."
	passwordLinksNeedInternal = "Password reset links need AppTown's own sign-in (--auth internal); with single sign-on, reset the password in your identity provider."
	linksNeedInternal         = "Invite and password reset links need AppTown's own sign-in (--auth internal); with single sign-on, use your identity provider."
)

func (h Invites) refuseOutsideInternal(w http.ResponseWriter, why string) bool {
	if !h.internal {
		WriteError(w, 404, "%s", why)
		return true
	}
	return false
}

// CreateInvite: POST /api/v1/invites {username, label?, role?, days?} — the
// link is returned once; only its hash is stored.
func (h Invites) CreateInvite(w http.ResponseWriter, r *http.Request) {
	if h.refuseOutsideInternal(w, invitesNeedInternal) {
		return
	}
	jsonCommand(w, r, jsonBody{errorText: "username (slug) required"}, func(in identity.NewInvite) {
		issued, err := h.access.Invite(r.Context(), in, Principal(r))
		if err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 201, InviteLink{Invite: issued.Invite, Link: h.browser.Origin(r) + "/invite/" + issued.Token,
			Note: "send this link to " + issued.Invite.Username + " yourself — it is shown once and AppTown sends no email"})
	})
}

// CreatePasswordLink: POST /api/v1/password-links {username, days?} — a
// password reset link for an account that has a password, returned once.
func (h Invites) CreatePasswordLink(w http.ResponseWriter, r *http.Request) {
	if h.refuseOutsideInternal(w, passwordLinksNeedInternal) {
		return
	}
	jsonCommand(w, r, jsonBody{errorText: "username (slug) required"}, func(in identity.NewPasswordLink) {
		issued, err := h.access.PasswordLink(r.Context(), in, Principal(r))
		if err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 201, InviteLink{Invite: issued.Invite, Link: h.browser.Origin(r) + "/invite/" + issued.Token,
			Note: "give this link to " + issued.Invite.Username + " yourself — it is shown once and AppTown sends no email. Their current password works until they use it."})
	})
}

// ListInvites: GET /api/v1/invites — invites still waiting to be used.
func (h Invites) ListInvites(w http.ResponseWriter, r *http.Request) {
	invites, err := h.access.Invites(r.Context(), Principal(r))
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, InviteList{Invites: invites})
}

// RevokeInvite: DELETE /api/v1/invites/{id}
func (h Invites) RevokeInvite(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r, "id")
	if err != nil {
		WriteError(w, 400, "invite id must be an id")
		return
	}
	if err := h.access.RevokeInvite(r.Context(), id, Principal(r)); err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, Done{OK: true})
}

// ShowInvite: POST /api/v1/auth/invite {token} — whose account the link is
// for, from whom, and whether it can still be used.
func (h Invites) ShowInvite(w http.ResponseWriter, r *http.Request) {
	if h.refuseOutsideInternal(w, linksNeedInternal) {
		return
	}
	jsonCommand(w, r, jsonBody{errorText: "token required"}, func(in struct {
		Token string `json:"token"`
	}) {
		inv, err := h.access.InviteFor(r.Context(), in.Token)
		if err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 200, InviteOffer{Purpose: inv.Purpose, Username: inv.Username, Role: inv.Role, InvitedBy: inv.InvitedBy, ExpiresAt: inv.ExpiresAt, State: inv.State})
	})
}

// AcceptInvite: POST /api/v1/auth/invite/accept {token, password} — sets the
// account's password (first or replacement) and signs the browser in.
func (h Invites) AcceptInvite(w http.ResponseWriter, r *http.Request) {
	if h.refuseOutsideInternal(w, linksNeedInternal) {
		return
	}
	jsonCommand(w, r, jsonBody{errorText: "token and password required"}, func(in struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}) {
		signed, err := h.access.AcceptInvite(r.Context(), identity.InviteAcceptance{Token: in.Token, Password: in.Password, From: h.address.ClientIP(r)})
		if err != nil {
			var denied *identity.AdministratorDenied
			if errors.As(err, &denied) {
				WriteError(w, 403, "%s", denied.Reason)
				return
			}
			WriteFault(w, err)
			return
		}
		h.browser.Start(w, r, signed.Token)
		WriteJSON(w, 200, SignedInAs{OK: true, User: signed.Principal.Name, Role: signed.Principal.Role})
	})
}
