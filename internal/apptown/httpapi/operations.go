package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/operations"
	"github.com/flokkit/apptown/internal/apptown/project"
)

// Operations is the doors of the operations ledger
// (internal/apptown/operations): read one, ask it to stop, put
// an ended one away. Owner routes start operations and answer 202 with
// OperationAdmitted; these read and steer them. The owner checks read and
// cancel authority; one the caller may not read is missing.
type Operations struct {
	ops      *operations.Service
	projects *project.Projects
}

func NewOperations(ops *operations.Service, projects *project.Projects) Operations {
	return Operations{ops: ops, projects: projects}
}

type (
	// Operation is one operation as the ledger has it.
	Operation = operations.Operation
	// OperationView is one operation as the ledger has it, with the
	// project it belongs to, so its page can name and link the project
	// instead of showing the ledger's subject key. Project is absent when
	// the project is gone or the caller may not read it: an operation
	// outlives its project.
	OperationView struct {
		operations.Operation
		Project *OperationProject `json:"project,omitempty"`
	}
	// OperationProject names an operation's project.
	OperationProject struct {
		Slug string `json:"slug"`
		Name string `json:"name"`
	}
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

// MarshalJSON writes the operation's own encoding (which sets its status
// label) with the project added. Without it the embedded operation's
// MarshalJSON would stand for the whole view and drop the project.
func (v OperationView) MarshalJSON() ([]byte, error) {
	op, err := json.Marshal(v.Operation)
	if err != nil || v.Project == nil {
		return op, err
	}
	p, err := json.Marshal(v.Project)
	if err != nil {
		return nil, err
	}
	out := append(op[:len(op)-1:len(op)-1], `,"project":`...)
	return append(append(out, p...), '}'), nil
}

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
	p, err := h.operationProject(r, op.ProjectID)
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, OperationView{Operation: *op, Project: p})
}

// operationProject reads an operation's project through the projects
// owner as the caller. A project that is gone or that the caller may not
// read is left out; any other failure is the read's error.
func (h Operations) operationProject(r *http.Request, id ids.ID) (*OperationProject, error) {
	if id.IsZero() {
		return nil, nil
	}
	p, err := h.projects.ByID(r.Context(), id)
	if err == nil {
		p, err = h.projects.Readable(r.Context(), p.Slug)
	}
	switch fault.KindOf(err) {
	case fault.NotFound, fault.Forbidden:
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &OperationProject{Slug: p.Slug, Name: p.Name}, nil
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
