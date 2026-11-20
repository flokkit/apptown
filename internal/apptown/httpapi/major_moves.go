package httpapi

import (
	"net/http"

	"github.com/flokkit/apptown/internal/apptown/verification"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/packagefacts"
)

// MajorMoves answers a run's agent: which of its project's direct
// dependencies are due to move to their newest major. The answer is advice;
// the run's routine step decides again from the change itself.
type MajorMoves struct {
	Majors verification.Majors
	Work   *workflow.Service
}

// Decide: GET /runs/{id}/major-moves?purl=pkg:npm/react@18.3.1 (repeatable).
func (h MajorMoves) Decide(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad id")
		return
	}
	purls := r.URL.Query()["purl"]
	if len(purls) == 0 {
		WriteError(w, 400, "purl required (pkg:npm/react@18.3.1)")
		return
	}
	moves := make([]verification.MajorMove, 0, len(purls))
	for _, s := range purls {
		p, version, err := packagefacts.ParsePurl(s)
		if err != nil {
			WriteError(w, 400, "%s", err)
			return
		}
		if packagefacts.Line(version) == "" {
			WriteError(w, 400, "%s: %s is not a version", s, version)
			return
		}
		moves = append(moves, verification.MajorMove{Package: p, From: version, FromLine: packagefacts.Line(version)})
	}
	run, err := h.Work.Run(r.Context(), id)
	if err != nil {
		WriteFault(w, err)
		return
	}
	decided, err := h.Majors.Decide(r.Context(), run.ProjectID, moves)
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, decided)
}
