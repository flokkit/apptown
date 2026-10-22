package httpapi

import (
	"net/http"

	"github.com/flokkit/apptown/internal/console"
)

// ConsoleFromPath is the session a request names, admitted for activity.
func ConsoleFromPath(sessions *console.Service, w http.ResponseWriter, r *http.Request) *console.Session {
	s, err := sessions.Get(r.Context(), r.PathValue("sid"))
	if err != nil {
		WriteError(w, 404, "no such session")
		return nil
	}
	return s
}
