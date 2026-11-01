package httpapi

import (
	"context"
	"net/http"
)

type ConsoleEnd struct{ end consoleEnder }

func NewConsoleEnd(end consoleEnder) ConsoleEnd { return ConsoleEnd{end: end} }

// consoleEnder ends a chat for the caller, who must be allowed to act on it.
type consoleEnder interface {
	EndBy(context.Context, string) error
}

// ConsoleEnd answers 404 only for a chat its owner says does not exist: the
// page takes 404 as "already gone" and opens a fresh chat, so any other
// failure keeps its own status (500 when unclassified).
func (h ConsoleEnd) ConsoleEnd(w http.ResponseWriter, r *http.Request) {
	if err := h.end.EndBy(r.Context(), r.PathValue("sid")); err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, Done{OK: true})
}
