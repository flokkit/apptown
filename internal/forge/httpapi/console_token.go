package httpapi

import (
	"net/http"

	"flokkit.io/forge/internal/console"
)

// consoleForTokenRequest: the session whose token the request presents. The
// agent inside holds the token; nothing else does. Refused while the
// session is being removed.
func consoleForTokenRequest(sessions *console.Service, w http.ResponseWriter, r *http.Request) *console.Session {
	s, err := sessions.Get(r.Context(), r.PathValue("sid"))
	if err != nil {
		WriteError(w, 404, "no such session")
		return nil
	}
	id, ok := sessions.Identity(r.Header.Get("X-AppTown-Session-Token"))
	if !ok || id.SessionID != s.ID {
		WriteError(w, 403, "invalid session token")
		return nil
	}
	return s
}
