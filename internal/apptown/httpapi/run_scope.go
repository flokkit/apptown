package httpapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/flokkit/apptown/internal/apptown/workflowexec"
	"github.com/flokkit/apptown/internal/ids"
)

type runScope interface {
	Scope(context.Context, ids.ID, string) (workflowexec.ScopeResult, error)
}

// RunScope translates scope feedback requests into workflow execution operations.
type RunScope struct{ Scope runScope }

type RunScopeRequest struct {
	Commit string `json:"commit"`
}

func (h RunScope) Check(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad id")
		return
	}
	if who := actor(r); strings.HasPrefix(who, "run/") && who != "run/"+id.String() {
		WriteError(w, 403, "this run's token can only check its own run")
		return
	}
	jsonCommand(w, r, jsonBody{limit: 4 << 10}, func(in RunScopeRequest) {
		out, err := h.Scope.Scope(r.Context(), id, in.Commit)
		if err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 200, out)
	})
}
