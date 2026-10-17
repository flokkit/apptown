package httpapi

import (
	"net/http"

	"flokkit.io/forge/internal/forge/identity"
)

type Accounts struct{ access *identity.Service }

// The account routes' responses.
type (
	// IssuedToken carries a new token's plaintext, shown once.
	IssuedToken struct {
		Token string `json:"token"`
		Name  string `json:"name"`
		User  string `json:"user"`
		Role  string `json:"role"`
		// Purpose is cli (its person's command line) or agent (a coding
		// agent's connection, bounded like an OAuth one).
		Purpose string `json:"purpose"`
		Note    string `json:"note"`
	}
	TokenList struct {
		Tokens []identity.Token `json:"tokens"`
	}
	UserList struct {
		Users []identity.Account `json:"users"`
	}
	GroupList struct {
		Groups []identity.Group `json:"groups"`
	}
)

func NewAccounts(access *identity.Service) Accounts { return Accounts{access: access} }

// CreateToken: POST /api/v1/tokens {name, user, role, purpose} (admin) — the
// plaintext is returned ONCE; only the hash is stored.
func (h Accounts) CreateToken(w http.ResponseWriter, r *http.Request) {
	jsonCommand(w, r, jsonBody{errorText: "name required"}, func(in identity.NewToken) {
		issued, err := h.access.IssueToken(r.Context(), in, Principal(r))
		if err != nil {
			WriteFault(w, err)
			return
		}
		meta := issued.Token
		WriteJSON(w, 201, IssuedToken{Token: issued.Plaintext, Name: meta.Name, User: meta.User, Role: meta.Role, Purpose: meta.Purpose,
			Note: "store this now — it is not retrievable again"})
	})
}

// ListTokens returns metadata only, never hashes.
func (h Accounts) ListTokens(w http.ResponseWriter, r *http.Request) {
	tokens, err := h.access.Tokens(r.Context(), Principal(r))
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, TokenList{Tokens: tokens})
}

func (h Accounts) RevokeToken(w http.ResponseWriter, r *http.Request) {
	revoked, err := h.access.RevokeToken(r.Context(), r.PathValue("name"), Principal(r))
	if err != nil {
		WriteFault(w, err)
		return
	}
	if !revoked {
		WriteError(w, 404, "no such live token")
		return
	}
	WriteJSON(w, 200, Done{OK: true})
}

// ListUsers: GET /api/v1/users — with group memberships.
func (h Accounts) ListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := h.access.Users(r.Context())
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, UserList{Users: users})
}

// SaveUser: POST /api/v1/users {username, password?, role?} — create
// or update. Password sets/resets; role changes recorded.
func (h Accounts) SaveUser(w http.ResponseWriter, r *http.Request) {
	jsonCommand(w, r, jsonBody{errorText: "username (slug) required"}, func(in identity.AccountChange) {
		if err := h.access.SaveUser(r.Context(), in, Principal(r)); err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 200, Done{OK: true})
	})
}

// DeleteUser: DELETE /api/v1/users/{name} (admin) — only a name that never
// signed in or acted may be removed (open-mode sediment, a typo in a member
// list); its grants, tokens and invites are revoked with it. Real accounts
// are disabled instead: an account is part of the record. Deleting a name
// never touches the ledger — attribution there is text, not a reference.
func (h Accounts) DeleteUser(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := h.access.RemoveName(r.Context(), name, Principal(r)); err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, Done{OK: true})
}

// RenameRequest is a rename's new name.
type RenameRequest struct {
	Name string `json:"name"`
}

// RenameUser: POST /api/v1/users/{name}/rename {name} — the person keeps
// everything they hold under the new name.
func (h Accounts) RenameUser(w http.ResponseWriter, r *http.Request) {
	jsonCommand(w, r, jsonBody{errorText: "the new name is required"}, func(in RenameRequest) {
		if err := h.access.Rename(r.Context(), r.PathValue("name"), in.Name, Principal(r)); err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 200, Done{OK: true})
	})
}

// ListGroups: GET /api/v1/groups
func (h Accounts) ListGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := h.access.Groups(r.Context())
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, GroupList{Groups: groups})
}

// SaveGroup: POST /api/v1/groups {name, description?, members?} —
// create/replace, membership included (small instance, whole-set semantics).
func (h Accounts) SaveGroup(w http.ResponseWriter, r *http.Request) {
	jsonCommand(w, r, jsonBody{errorText: "group name (slug) required"}, func(in identity.GroupChange) {
		if err := h.access.SaveGroup(r.Context(), in, Principal(r)); err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 200, Done{OK: true})
	})
}

// DeleteGroup: DELETE /api/v1/groups/{name}
func (h Accounts) DeleteGroup(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := h.access.DeleteGroup(r.Context(), name, Principal(r)); err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, Done{OK: true})
}
