package httpapi

import (
	"net/http"

	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/ids"
)

type ExternalStartRequest struct {
	Name       string `json:"name"`
	ReturnPath string `json:"returnPath"`
	Password   string `json:"password"`
	Link       bool   `json:"link"`
}
type ExternalRedirect struct {
	URL string `json:"url"`
}
type ExternalProviderSummary struct {
	DisplayName string `json:"displayName"`
	AllowSignup bool   `json:"allowSignup"`
}
type ExternalEnableRequest struct {
	Revision int64 `json:"revision"`
	Enabled  bool  `json:"enabled"`
}
type ExternalBindRequest struct {
	ProviderID  ids.ID `json:"providerId"`
	PrincipalID ids.ID `json:"principalId"`
	Subject     string `json:"subject"`
}

// ExternalSignIn translates identity administration without storing credentials.
type ExternalSignIn struct{ access *identity.Service }

func NewExternalSignIn(s *identity.Service) ExternalSignIn { return ExternalSignIn{access: s} }
func (h ExternalSignIn) Provider(w http.ResponseWriter, r *http.Request) {
	p, e := h.access.ExternalProvider(r.Context())
	if e != nil {
		WriteFault(w, e)
		return
	}
	WriteJSON(w, 200, p)
}
func (h ExternalSignIn) Save(w http.ResponseWriter, r *http.Request) {
	jsonCommand(w, r, jsonBody{}, func(in identity.ExternalProviderChange) {
		p, e := h.access.SaveExternalProvider(r.Context(), in, Principal(r))
		if e != nil {
			WriteFault(w, e)
			return
		}
		WriteJSON(w, 200, p)
	})
}
func (h ExternalSignIn) Enabled(w http.ResponseWriter, r *http.Request) {
	jsonCommand(w, r, jsonBody{}, func(in ExternalEnableRequest) {
		p, e := h.access.SetExternalEnabled(r.Context(), in.Revision, in.Enabled, Principal(r))
		if e != nil {
			WriteFault(w, e)
			return
		}
		WriteJSON(w, 200, p)
	})
}
func (h ExternalSignIn) Test(w http.ResponseWriter, r *http.Request) {
	if e := h.access.TestExternalProvider(r.Context(), Principal(r)); e != nil {
		WriteFault(w, e)
		return
	}
	WriteJSON(w, 200, map[string]bool{"discovery": true})
}
func (h ExternalSignIn) Bind(w http.ResponseWriter, r *http.Request) {
	jsonCommand(w, r, jsonBody{}, func(in ExternalBindRequest) {
		if e := h.access.BindExternal(r.Context(), in.ProviderID, in.PrincipalID, in.Subject, Principal(r)); e != nil {
			WriteFault(w, e)
			return
		}
		WriteJSON(w, http.StatusOK, Done{OK: true})
	})
}
func (h ExternalSignIn) Revoke(w http.ResponseWriter, r *http.Request) {
	id, e := ids.Parse(r.PathValue("id"))
	if e != nil {
		WriteError(w, 400, "invalid binding ID")
		return
	}
	if e := h.access.RevokeExternalBinding(r.Context(), id, Principal(r)); e != nil {
		WriteFault(w, e)
		return
	}
	WriteJSON(w, http.StatusOK, Done{OK: true})
}

func (h ExternalSignIn) Bindings(w http.ResponseWriter, r *http.Request) {
	rows, e := h.access.ExternalBindings(r.Context(), Principal(r), r.URL.Query().Get("all") == "true")
	if e != nil {
		WriteFault(w, e)
		return
	}
	WriteJSON(w, 200, ExternalBindingList{Bindings: rows})
}

type ExternalBindingList struct {
	Bindings []identity.ExternalBindingView `json:"bindings"`
}
