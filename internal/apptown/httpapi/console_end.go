package httpapi

import (
	"context"
	"net/http"
)

type ConsoleEnd struct{ end ConsoleEnder }

func NewConsoleEnd(end ConsoleEnder) ConsoleEnd { return ConsoleEnd{end: end} }

// ConsoleEnder ends a chat for the caller, who must be allowed to act on it.
type ConsoleEnder interface {
	EndBy(context.Context, string) error
}

func (h ConsoleEnd) ConsoleEnd(w http.ResponseWriter, r *http.Request) {
	if err := h.end.EndBy(r.Context(), r.PathValue("sid")); err != nil {
		WriteFaultOr(w, err, 404)
		return
	}
	WriteJSON(w, 200, Done{OK: true})
}
