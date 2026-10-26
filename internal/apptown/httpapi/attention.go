package httpapi

import (
	"github.com/flokkit/apptown/internal/apptown/attention"
	"net/http"
)

type Attention struct{ service *attention.Service }

func NewAttention(service *attention.Service) Attention { return Attention{service: service} }
func (h Attention) Read(w http.ResponseWriter, r *http.Request) {
	view, err := h.service.Read(r.Context())
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, view)
}
