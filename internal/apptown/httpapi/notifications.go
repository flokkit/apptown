package httpapi

import (
	"github.com/flokkit/apptown/internal/apptown/notification"
	"net/http"
)

type Notifications struct{ service *notification.Service }

func NewNotifications(service *notification.Service) Notifications { return Notifications{service} }
func (h Notifications) Read(w http.ResponseWriter, r *http.Request) {
	v, err := h.service.Read(r.Context())
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, v)
}
func (h Notifications) Test(w http.ResponseWriter, r *http.Request) {
	jsonCommand(w, r, jsonBody{optional: true}, func(_ struct{}) {
		v, err := h.service.Test(r.Context(), r.PathValue("name"))
		if err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, v)
	})
}
