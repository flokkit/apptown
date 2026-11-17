package httpapi

import (
	"context"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/apptown/changeactions"
	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/workflow"
)

type ConformanceReviews struct {
	projects  *project.Projects
	reviews   fleetReviews
	work      *workflow.Service
	schedules changeactions.Schedules
	now       func() time.Time
}

func NewConformanceReviews(projects *project.Projects, reviews fleetReviews, work *workflow.Service, schedules changeactions.Schedules, now func() time.Time) ConformanceReviews {
	return ConformanceReviews{projects: projects, reviews: reviews, work: work, schedules: schedules, now: now}
}

// RunConformanceReview: POST /api/v1/conformance/review — start a review
// now. Named projects with recheck also request a security scan when their
// latest main scans are unknown, under RunJanitor authority. Scan refusals are
// logged and do not fail the standards review.
// Body (optional): {projects: [slug…], build: bool, recheck: bool}. build lets exec-image
// tool checks build a missing app image, within the review's budget; a manual
// review from the UI asks for it, a scheduled one does not.
func (h ConformanceReviews) RunConformanceReview(w http.ResponseWriter, r *http.Request) {
	// strict: an unknown key ("project" for "projects") must not read as
	// no projects named, which is the whole estate.
	jsonCommand(w, r, jsonBody{optional: true, strict: true}, func(in struct {
		Recheck  bool     `json:"recheck"`
		Projects []string `json:"projects"`
		Build    *bool    `json:"build"`
	}) {
		// an empty body is the whole estate
		opts := conformance.ReviewOptions{Build: true, Recheck: in.Recheck}
		if in.Build != nil {
			opts.Build = *in.Build
		}
		var named []project.Project
		if len(in.Projects) > 0 {
			for _, slug := range in.Projects {
				p, err := h.projects.BySlug(r.Context(), strings.TrimSpace(slug))
				if err != nil {
					writeReadFault(w, err, "no such project %q", slug)
					return
				}
				opts.Projects = append(opts.Projects, p.ID)
				named = append(named, p)
			}
		}
		rev, err := h.reviews.StartFleetReviewWith(r.Context(), "manual", opts)
		if err != nil {
			WriteFault(w, err)
			return
		}
		if in.Recheck {
			for _, p := range named {
				scans, err := h.work.LatestMainScans(r.Context(), p.ID, 0)
				if err != nil {
					log.Printf("project %s: reading scans for conformance recheck: %v", p.Slug, err)
					continue
				}
				if scans.Known {
					continue
				}
				starts, err := h.schedules.RunJanitor(r.Context(), p, changeactions.JanitorRun{Queue: "security", Again: true}, h.now())
				if err != nil {
					log.Printf("project %s: requesting security scan for conformance recheck: %v", p.Slug, err)
					continue
				}
				for _, start := range starts {
					if start.Refused != "" {
						log.Printf("project %s: conformance recheck queue %s refused: %s", p.Slug, start.Queue, start.Refused)
					}
				}
			}
		}
		WriteJSON(w, 202, rev)
	})
}

type fleetReviews interface {
	StartFleetReviewWith(ctx context.Context, trigger string, opts conformance.ReviewOptions) (*conformance.Review, error)
}
