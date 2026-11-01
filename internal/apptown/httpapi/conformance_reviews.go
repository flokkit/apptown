package httpapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/project"
)

type ConformanceReviews struct {
	projects *project.Projects
	reviews  fleetReviews
}

func NewConformanceReviews(projects *project.Projects, reviews fleetReviews) ConformanceReviews {
	return ConformanceReviews{projects: projects, reviews: reviews}
}

// RunConformanceReview: POST /api/v1/conformance/review — start a review
// now. Body (optional): {projects: [slug…], build: bool}. build lets exec-image
// tool checks build a missing app image, within the review's budget; a manual
// review from the UI asks for it, a scheduled one does not.
func (h ConformanceReviews) RunConformanceReview(w http.ResponseWriter, r *http.Request) {
	// strict: an unknown key ("project" for "projects") must not read as
	// no projects named, which is the whole estate.
	jsonCommand(w, r, jsonBody{optional: true, strict: true}, func(in struct {
		Projects []string `json:"projects"`
		Build    *bool    `json:"build"`
	}) {
		// an empty body is the whole estate
		opts := conformance.ReviewOptions{Build: true}
		if in.Build != nil {
			opts.Build = *in.Build
		}
		if len(in.Projects) > 0 {
			for _, slug := range in.Projects {
				p, err := h.projects.BySlug(r.Context(), strings.TrimSpace(slug))
				if err != nil {
					writeReadFault(w, err, "no such project %q", slug)
					return
				}
				opts.Projects = append(opts.Projects, p.ID)
			}
		}
		rev, err := h.reviews.StartFleetReviewWith(r.Context(), "manual", opts)
		if err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 202, rev)
	})
}

type fleetReviews interface {
	StartFleetReviewWith(ctx context.Context, trigger string, opts conformance.ReviewOptions) (*conformance.Review, error)
}
