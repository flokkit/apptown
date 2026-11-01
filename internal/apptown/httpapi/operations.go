package httpapi

import (
	"net/http"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/operations"
)

// Operations is the doors of the operations ledger
// (internal/apptown/operations): read one, ask it to stop, put
// an ended one away. Owner routes start operations and answer 202 with
// OperationAdmitted; these read and steer them. The owner checks read and
// cancel authority; one the caller may not read is missing.
type Operations struct {
	ops *operations.Service
}

func NewOperations(ops *operations.Service) Operations { return Operations{ops: ops} }

type (
	// Operation is one operation as the ledger has it.
	Operation = operations.Operation
	// OperationAdmitted is an owner door's 202: the operation was admitted
	// and queued; follow it at URL.
	OperationAdmitted struct {
		OperationID ids.ID                    `json:"operationId"`
		State       operations.OperationState `json:"state"`
		URL         string                    `json:"url"`
	}
	// OperationDismissed is an ended operation put away.
	OperationDismissed struct {
		OK bool `json:"ok"`
	}
)

// writeAdmitted answers an admitted operation: 202, Location and the handle.
func writeAdmitted(w http.ResponseWriter, op *operations.Operation) {
	w.Header().Set("Location", op.URL())
	WriteJSON(w, 202, OperationAdmitted{OperationID: op.ID, State: op.State, URL: op.URL()})
}

// Get: GET /operations/{id}.
func (h Operations) Get(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad id")
		return
	}
	op, err := h.ops.Get(r.Context(), id)
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, Operation(*op))
}

// Cancel: POST /operations/{id}/cancel — a queued operation is cancelled
// at once; a running one stops at its next safe step.
func (h Operations) Cancel(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad id")
		return
	}
	op, err := h.ops.Cancel(r.Context(), id)
	if err != nil {
		WriteFault(w, err)
		return
	}
	writeAdmitted(w, op)
}

// Dismiss: POST /operations/{id}/dismiss — the caller puts away one of
// their own ended operations. The owner matches the asker, so it needs no
// read of the operation's project, which may be gone.
func (h Operations) Dismiss(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad id")
		return
	}
	if err := h.ops.Dismiss(r.Context(), id, actor(r)); err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, OperationDismissed{OK: true})
}
