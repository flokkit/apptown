package httpapi

import (
	"net/http"
	"strings"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/forge/conformance"
	"flokkit.io/forge/internal/forge/project"
)

type Conformance struct {
	conformance *conformance.Service
	projects    *project.Projects
	now         func() time.Time // the forge's clock, which dates an OpenVEX document
}

func NewConformance(conformance *conformance.Service, projects *project.Projects, now func() time.Time) Conformance {
	return Conformance{conformance: conformance, projects: projects, now: clock.Or(now)}
}

// Conformance: GET /api/v1/conformance[?review=N] — the whole surface
// under a lens.
func (h Conformance) Conformance(w http.ResponseWriter, r *http.Request) {
	var reviewID ids.ID
	if q := strings.TrimSpace(r.URL.Query().Get("review")); q != "" {
		var err error
		if reviewID, err = ids.Parse(q); err != nil || reviewID.IsZero() {
			WriteError(w, 400, "bad review id")
			return
		}
	}
	view, err := h.conformance.View(r.Context(), reviewID)
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, view)
}

// ListReviews: GET /api/v1/conformance/reviews — history with what each
// review found and how it moved against the previous comparable one.
func (h Conformance) ListReviews(w http.ResponseWriter, r *http.Request) {
	reviews, err := h.conformance.Reviews(r.Context(), 100)
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, ConformanceReviewList{Reviews: reviews})
}

// ReviewDetail: GET /api/v1/conformance/reviews/{id} — one review's live
// state plus the verdicts recorded so far, in evaluation order. This is the
// activity feed: while status=running the verdicts accrue project-by-project.
func (h Conformance) ReviewDetail(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad review id")
		return
	}
	detail, err := h.conformance.Review(r.Context(), id)
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, detail)
}

// ProjectConformanceHistory: GET /api/v1/conformance/projects/{slug}/history
func (h Conformance) ProjectConformanceHistory(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	history, err := h.conformance.ProjectHistory(r.Context(), p.ID, 40)
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, ConformanceHistory{History: history})
}

// ConfirmVerdict: POST /api/v1/conformance/verdicts/{id}/confirm {note}
// — a person says an AI flag is real. Immutable, keyed to this verdict only.
func (h Conformance) ConfirmVerdict(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad verdict id")
		return
	}
	jsonCommand(w, r, jsonBody{optional: true}, func(in struct {
		Note string `json:"note"`
	}) {
		ad, err := h.conformance.Confirm(r.Context(), conformance.Confirmation{VerdictID: id, Note: in.Note})
		if err != nil {
			WriteFault(w, err)
			return
		}

		WriteJSON(w, 201, ad)
	})
}

// ProjectFixes: GET /api/v1/conformance/projects/{slug}/fixes
func (h Conformance) ProjectFixes(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	fixes, err := h.conformance.ProjectFixes(r.Context(), p.ID, 100)
	if err != nil {
		WriteFault(w, err)
		return
	}
	out := make([]ConformanceFix, 0, len(fixes))
	for _, f := range fixes {
		out = append(out, ConformanceFix{Fix: f, Sentence: conformance.FixSentence(f)})
	}
	WriteJSON(w, 200, ConformanceFixList{Fixes: out})
}

// ConformanceReviewList is the review history, newest first.
type ConformanceReviewList struct {
	Reviews []conformance.ReviewSummary `json:"reviews"`
}

// ConformanceHistory is one project's standing over time.
type ConformanceHistory struct {
	History []conformance.HistoryPoint `json:"history"`
}

// ConformanceFixList is the fixes recorded for one project.
type ConformanceFixList struct {
	Fixes []ConformanceFix `json:"fixes"`
}

// ConformanceFix is one recorded fix and its sentence for people.
type ConformanceFix struct {
	Fix      conformance.Fix `json:"fix"`
	Sentence string          `json:"sentence"`
}
