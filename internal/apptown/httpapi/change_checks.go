package httpapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/verification"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
)

// changeVerifier is the committed-tree command shared with other transports.
// sel is the commit the caller asks about (verification.Selection). The
// owner decides whether it can answer for it.
type changeVerifier interface {
	Check(ctx context.Context, ch *workflow.Change, sel verification.Selection) (map[string]any, error)
}

// ChangeCheckRequest is the body of `POST /changes/{id}/check`: the commit the
// caller asks about (the CLI's folder HEAD), or published for the tip of the
// change's branch on AppTown (`apptown check --published`). A body naming
// neither (`{}`, or none) asks for the change's latest commit on AppTown,
// as it always has. The answer's commit field names what was checked.
type ChangeCheckRequest struct {
	Commit    string `json:"commit,omitempty"`
	Published bool   `json:"published,omitempty"`
}
type ChangeChecks struct {
	db      database.Queryer
	checker changeVerifier
}

func NewChangeChecks(db database.Queryer, checker changeVerifier) ChangeChecks {
	return ChangeChecks{db: db, checker: checker}
}

// `apptown check` (design/subsystems/guidance-checks.md): let an agent — or a human — run a
// change's DECLARED sh gate checks against its committed tree BEFORE handoff,
// so a failing gate is fixed in the loop instead of discovered after the run
// hands back. Same checks, same committed-tree posture, same gating rule as the
// guidance/conformance STEP (they share runShChecks), so this is a faithful
// preview of the gate, not a second opinion. llm checks are advisory and never
// gate, so they're out of scope here — this reports only what can actually
// block a landing and that the agent can act on.
func (h ChangeChecks) Check(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad id")
		return
	}
	ch, err := workflow.ReadChangeIn(r.Context(), h.db, id)
	if err != nil {
		writeReadFault(w, err, "no such change #%d", id)
		return
	}
	// A run token may check ONLY its own change — a step agent's whole
	// authority is "verify the work I am doing", never a lever on siblings.
	if who := actor(r); strings.HasPrefix(who, "run/") {
		runID, _ := ids.Parse(strings.TrimPrefix(who, "run/"))
		run, err := workflow.ReadRunIn(r.Context(), h.db, runID)
		if err != nil {
			WriteError(w, 403, "this run's token can only check its own change")
			return
		}
		source, err := workflow.ReadSourceIn(r.Context(), h.db, workflow.RunRef{ID: run.ID, ProjectID: run.ProjectID})
		if err != nil || source.ChangeID != ch.ID {
			WriteError(w, 403, "this run's token can only check its own change")
			return
		}
	}
	jsonCommand(w, r, jsonBody{optional: true, limit: 4 << 10}, func(in ChangeCheckRequest) {
		out, err := h.checker.Check(r.Context(), ch, verification.Selection{Commit: in.Commit, Published: in.Published})
		if err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 200, out)
	})
}
