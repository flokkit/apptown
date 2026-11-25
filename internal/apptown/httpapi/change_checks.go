package httpapi

import (
	"context"
	"encoding/json"
	"github.com/flokkit/apptown/internal/fault"
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
// guidance/conformance STEP (they share runShChecks). Runtime and model
// judgements are reported as skipped here; the landing judges them.
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
		streamChangeCheck(w, r, func(ctx context.Context, report func(string)) (map[string]any, error) {
			return h.checker.Check(verification.WithCheckProgress(ctx, report), ch, verification.Selection{Commit: in.Commit, Published: in.Published})
		})
	})
}

// streamChangeCheck emits one NDJSON response: progress, then a result or error.
// Errors before progress keep their HTTP classification.
func streamChangeCheck(w http.ResponseWriter, r *http.Request, run func(context.Context, func(string)) (map[string]any, error)) {
	started := false
	enc := json.NewEncoder(w)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	report := func(line string) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		started = true
		if err := enc.Encode(map[string]string{"progress": line}); err != nil {
			cancel()
			return
		}
		if err := http.NewResponseController(w).Flush(); err != nil {
			cancel()
		}
	}
	out, err := run(ctx, report)
	if err != nil {
		if !started {
			w.Header().Set("Content-Type", "application/x-ndjson")
			if code := fault.CodeOf(err); code != "" {
				w.Header().Set("X-AppTown-Deny-Code", code)
			}
			w.WriteHeader(faultStatus(fault.KindOf(err), http.StatusInternalServerError))
		}
		_ = enc.Encode(map[string]string{"error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	_ = enc.Encode(noNullLists(out))
}
