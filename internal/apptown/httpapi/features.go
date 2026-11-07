package httpapi

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/integration"
)

type Features struct{ settings *identity.Service }

func NewFeatures(settings *identity.Service) Features { return Features{settings} }
func (h Features) List(w http.ResponseWriter, r *http.Request) {
	value, err := h.settings.Features(r.Context())
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, value)
}

// FeatureChange is the administrator's edit of the choice they last read.
type FeatureChange struct {
	Expected *bool `json:"expected"`
	Enabled  *bool `json:"enabled"`
}

func (h Features) Set(w http.ResponseWriter, r *http.Request) {
	jsonCommand(w, r, jsonBody{strict: true}, func(in FeatureChange) {
		if in.Expected == nil || in.Enabled == nil {
			WriteError(w, 400, "expected and enabled are required")
			return
		}
		caller, _ := access.CallerFrom(r.Context())
		f, err := h.settings.SetFeature(r.Context(), caller.Principal, r.PathValue("area"), *in.Expected, *in.Enabled)
		if err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 200, f)
	})
}

// Shared connector endpoints classify the requested capability before probes
// or writes. Runtime integrations used by adopted workflows are not changed.

func (h Connectors) requireSavedFeature(w http.ResponseWriter, r *http.Request) bool {
	b, err := h.integrations.Bindings.ByName(r.Context(), r.PathValue("name"))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			WriteError(w, http.StatusNotFound, "integration not found")
		} else {
			WriteFault(w, err)
		}
		return false
	}
	if err := h.access.RequireFeature(r.Context(), integration.FeatureArea(b.Capability, b.Provider)); err != nil {
		WriteFault(w, err)
		return false
	}
	return true
}
