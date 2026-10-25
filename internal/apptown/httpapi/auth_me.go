package httpapi

// AuthMe is GET /auth/me: how this AppTown signs people in, and who the caller
// is when the request carries an identity. The web reads it to decide whether
// to show the sign-in page; what a caller may do is read from /authz/me.
type AuthMe struct {
	Mode string `json:"mode"`
	// NeedsBootstrap is said only under AppTown's own sign-in: no account
	// exists yet, so the first one made is the administrator.
	NeedsBootstrap *bool  `json:"needsBootstrap,omitempty"`
	User           string `json:"user,omitempty"`
	Role           string `json:"role,omitempty"`
	Via            string `json:"via,omitempty"`
	From           string `json:"from,omitempty"`
}
