package attention

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/ids"
)

// Deployments reads the owner's current intent and matching runtime cache.
// A missing runtime never establishes an unintentional stop.
type Deployments struct {
	Projects interface {
		ByID(context.Context, ids.ID) (project.Project, error)
	}
	Runtime interface {
		Operational(context.Context, ids.ID) ([]deployment.OperationalObservation, error)
	}
	Now func() time.Time
}

func (s Deployments) ProjectSnapshot(ctx context.Context, id ids.ID) (Snapshot, error) {
	if s.Projects == nil || s.Runtime == nil {
		return Snapshot{}, nil
	}
	p, err := s.Projects.ByID(ctx, id)
	if err != nil {
		return Snapshot{}, err
	}
	rows, err := s.Runtime.Operational(ctx, id)
	if err != nil {
		return Snapshot{}, err
	}
	projectName := p.Name
	if projectName == "" {
		projectName = p.Slug
	}
	out := Snapshot{Known: true, ObservedAt: clock.Or(s.Now)()}
	for _, d := range rows {
		r := d.Runtime
		unknown := r.Err != nil || r.State == deployment.RuntimeUnknown || r.ObservedAt.IsZero()
		out.Known = out.Known && !unknown
		out.Stale = out.Stale || r.Stale
		if r.ObservedAt.IsZero() || r.ObservedAt.Before(out.ObservedAt) {
			out.ObservedAt = r.ObservedAt
		}
		label := d.DisplayLabel
		if label == "" {
			label = d.Name
		}
		base := fmt.Sprintf("%s/%d/%d", d.ID, d.ObservedGeneration, d.DesiredGeneration)
		href := "/projects/" + url.PathEscape(p.Slug) + "/settings/deployments#diagnostics-" + url.PathEscape(d.Name)
		add := func(kind, title, detail, severity string, at time.Time, stale bool) {
			out.Items = append(out.Items, Item{Key: base + "/" + kind, Title: projectName + " · " + label + ": " + title, Detail: label + ": " + detail, Severity: severity, Href: href, ObservedAt: at, Stale: stale})
		}
		changedAt, _ := time.Parse(time.RFC3339Nano, d.UpdatedAt)
		switch d.Readiness {
		case deployment.ReadinessFailed:
			detail := "The latest deployment attempt failed."
			if !unknown && !r.Stale && r.State == deployment.RuntimeRunning && d.ObservedGeneration > 0 && d.DesiredGeneration > d.ObservedGeneration {
				detail += " The previously deployed generation is still running."
			}
			add("attempt", "Release failed", detail, SeverityWarning, changedAt, false)
		case deployment.ReadinessUncertain:
			add("attempt", "Release not confirmed", "The latest attempt ended without a confirmed outcome.", SeverityWarning, changedAt, false)
		}
		// A fresh running observation supersedes an old stop receipt. An unread
		// runtime retains the exact-generation hold as stale evidence.
		held := d.Hold != nil && (unknown || r.Stale || r.State != deployment.RuntimeRunning)
		if held {
			title, detail := "Check whether the app stopped", "AppTown attempted to stop this generation after repeated crashes."
			if d.Hold.Confirmed {
				title, detail = "Stopped after repeated crashes", "AppTown stopped this generation after repeated crashes."
			}
			at, _ := time.Parse(time.RFC3339Nano, d.Hold.At)
			add("crash-hold", title, detail, SeverityWarning, at, unknown || r.Stale)
		}
		switch {
		case unknown:
			add("runtime", "App status not known", "The latest runtime observation is unavailable.", SeverityWarning, r.ObservedAt, true)
		case r.State == deployment.RuntimeStopped && !held:
			out.Known = false
			add("runtime", "Stopped; check whether it should be running", "The runtime is absent. This observation does not establish whether it was stopped intentionally.", SeverityAdvisory, r.ObservedAt, r.Stale)
		case r.Health == "unhealthy" || r.Rollout == "failed":
			add("runtime", "Deployment runtime needs attention", "The observed serving generation is unhealthy or its provider rollout failed.", SeverityWarning, r.ObservedAt, r.Stale)
		}
	}
	return out, nil
}
