package httpapi

import (
	"net/http"

	"github.com/flokkit/apptown/internal/apptown/instanceupdate"
)

// InstanceUpdates projects the authenticated notification owner; GET never fetches.
type InstanceUpdates struct{ owner *instanceupdate.Service }

func NewInstanceUpdates(owner *instanceupdate.Service) InstanceUpdates {
	return InstanceUpdates{owner: owner}
}
func (h InstanceUpdates) Status(w http.ResponseWriter, r *http.Request) {
	status, err := h.owner.Status(r.Context())
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, status)
}
func (h InstanceUpdates) Check(w http.ResponseWriter, r *http.Request) {
	jsonCommand(w, r, jsonBody{errorText: "expected an empty check request"}, func(_ struct{}) {
		status, err := h.owner.Check(r.Context())
		if err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, status)
	})
}
