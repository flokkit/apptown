package httpapi

import (
	"net/http"

	"github.com/flokkit/apptown/internal/apptown/wow"
)

// RoutinePublications transports explicit human publication consent; authority,
// exact material and revocation live in the publication owner.
type RoutinePublications struct{ owner wow.RoutineTransitions }

func NewRoutinePublications(owner wow.RoutineTransitions) RoutinePublications {
	return RoutinePublications{owner: owner}
}
func (h RoutinePublications) Preview(w http.ResponseWriter, r *http.Request) {
	jsonCommand(w, r, jsonBody{}, func(in wow.RoutinePublicationTransition) {
		out, err := h.owner.Preview(r.Context(), in)
		if err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, out)
	})
}
func (h RoutinePublications) Classify(w http.ResponseWriter, r *http.Request) {
	jsonCommand(w, r, jsonBody{}, func(in wow.RoutinePublicationConsent) {
		out, err := h.owner.Classify(r.Context(), in)
		if err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, out)
	})
}
func (h RoutinePublications) History(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad publication identity")
		return
	}
	out, err := h.owner.History(r.Context(), wow.RoutinePublicationTransition{Kind: r.PathValue("kind"), ID: id})
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, out)
}
func (h RoutinePublications) Revoke(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad classification identity")
		return
	}
	out, err := h.owner.Revoke(r.Context(), id)
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, out)
}
