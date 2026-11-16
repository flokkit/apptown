package httpapi

import (
	"net/http"

	"github.com/flokkit/apptown/internal/apptown/imagelines"
	"github.com/flokkit/apptown/internal/imageline"
)

// ImageLines exposes the instance's recorded public-image registry readings.
type ImageLines struct{ Resolver imagelines.Resolver }

func (h ImageLines) Newest(w http.ResponseWriter, r *http.Request) {
	ref, scope := r.URL.Query().Get("ref"), imageline.Scope(r.URL.Query().Get("scope"))
	if ref == "" {
		WriteError(w, 400, "ref required")
		return
	}
	if _, err := imageline.Parse(ref, scope); err != nil {
		WriteError(w, 400, "%s", err)
		return
	}
	answer, err := h.Resolver.Answer(r.Context(), ref, scope)
	if err != nil {
		WriteError(w, 503, "%s", err)
		return
	}
	WriteJSON(w, 200, answer)
}
