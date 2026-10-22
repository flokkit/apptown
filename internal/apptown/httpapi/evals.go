package httpapi

import (
	"context"
	"math"
	"net/http"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/evals"
)

// Evals are the evals of model profiles and pack versions.
// The evals owner checks every operation's authority itself: a standards
// manager starts and reads them; the starter continues one; an
// administrator, or the starter while still a standards manager, deletes
// one.
type Evals struct{ service EvalOperations }

// EvalOperations is the evals owner's surface.
type EvalOperations interface {
	Start(ctx context.Context, in evals.Start) (*evals.Eval, error)
	List(ctx context.Context) ([]evals.Eval, error)
	Scorecard(ctx context.Context, id ids.ID) (*evals.Scorecard, error)
	Continue(ctx context.Context, id ids.ID) (*evals.Eval, error)
	Delete(ctx context.Context, id ids.ID) error
	StartForm(ctx context.Context) (evals.StartForm, error)
	ArmDiff(ctx context.Context, evalID, armID ids.ID) (string, error)
}

func NewEvals(service EvalOperations) Evals { return Evals{service: service} }

// StartEval: POST /api/v1/evals {changes:[id…], profile | pack+version,
// budgetDollars, shown} — one thing varies; shown is the meaning the
// starter was shown and agreed to.
func (h Evals) StartEval(w http.ResponseWriter, r *http.Request) {
	jsonCommand(w, r, jsonBody{strict: true}, func(in struct {
		Changes       []ids.ID `json:"changes"`
		Profile       string   `json:"profile"`
		Pack          string   `json:"pack"`
		Version       int64    `json:"version"`
		BudgetDollars float64  `json:"budgetDollars"`
		Shown         string   `json:"shown"`
	}) {
		e, err := h.service.Start(r.Context(), evals.Start{Changes: in.Changes, Profile: in.Profile, Pack: in.Pack, PackVersion: in.Version,
			BudgetMicros: int64(math.Round(in.BudgetDollars * 1e6)), Shown: in.Shown})
		if err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 201, e)
	})
}

// EvalStartForm: GET /api/v1/evals/start — what starting means and the
// profiles an eval may vary to.
func (h Evals) EvalStartForm(w http.ResponseWriter, r *http.Request) {
	out, err := h.service.StartForm(r.Context())
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, out)
}

// ListEvals: GET /api/v1/evals.
func (h Evals) ListEvals(w http.ResponseWriter, r *http.Request) {
	out, err := h.service.List(r.Context())
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, EvalList{Evals: out})
}

// EvalScorecard: GET /api/v1/evals/{id}.
func (h Evals) EvalScorecard(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad eval id")
		return
	}
	out, err := h.service.Scorecard(r.Context(), id)
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, out)
}

// ContinueEval: POST /api/v1/evals/{id}/continue — the starter answers for
// unknown spend and lets the next arm start.
func (h Evals) ContinueEval(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad eval id")
		return
	}
	out, err := h.service.Continue(r.Context(), id)
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, out)
}

// DeleteEval: DELETE /api/v1/evals/{id} — its copies are deleted; what
// they cost and what was complained about in them stays.
func (h Evals) DeleteEval(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad eval id")
		return
	}
	if err := h.service.Delete(r.Context(), id); err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, EvalDeleted{OK: true})
}

// EvalList is the newest evals.
type EvalList struct {
	Evals []evals.Eval `json:"evals"`
}

// EvalDeleted says an eval and its copies are gone.
type EvalDeleted struct {
	OK bool `json:"ok"`
}

// EvalArmDiff: GET /api/v1/evals/{id}/arms/{arm}/diff — what an arm's
// work changed, its copy's main against the Change's base, as plain text.
// Read under the scorecard's authority: copies have no members.
func (h Evals) EvalArmDiff(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad eval id")
		return
	}
	arm, err := PathID(r, "arm")
	if err != nil {
		WriteError(w, 400, "bad arm id")
		return
	}
	diff, err := h.service.ArmDiff(r.Context(), id, arm)
	if err != nil {
		WriteFault(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(diff))
}
