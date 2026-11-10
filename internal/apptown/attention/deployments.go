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

// runtimeGrace is how long a deployment's last good runtime reading still
// stands in for it. A reading that is late or failed once (the host busy
// with another app's release, AppTown just restarted) is not news; only a
// status missing for longer is listed. The deployment's own page still shows
// the reading's age at once.
const runtimeGrace = 2 * time.Minute

// Deployments reads the owner's current intent and matching runtime cache.
// A missing runtime never establishes an unintentional stop.
type Deployments struct {
	Projects interface {
		ByID(context.Context, ids.ID) (project.Project, error)
	}
	Runtime interface {
		Operational(context.Context, ids.ID) ([]deployment.OperationalObservation, error)
	}
	Hosts hostReachabilityReader
	Now   func() time.Time
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
	now := clock.Or(s.Now)()
	out := Snapshot{Known: true, ObservedAt: now, Name: projectName + " · deployments", Href: "/projects/" + url.PathEscape(p.Slug) + "/deployments", NextStep: "Open the deployment diagnostics. Someone with deployment permission can repair or release it."}
	subjects, folded := 0, 0
	for _, d := range rows {
		down := false
		if s.Hosts != nil && !d.TargetID.IsZero() {
			host, err := s.Hosts.HostReachability(ctx, d.TargetID)
			if err != nil {
				return Snapshot{}, err
			}
			down = host.Down
		}
		subjects++
		if down {
			folded++
		}
		r := d.Runtime
		unknown := r.Err != nil || r.State == deployment.RuntimeUnknown || r.ObservedAt.IsZero()
		// The deployment's own act gives the observer time to read the new
		// runtime: a reading begun before the act, or taken while a runtime
		// control is under way, says nothing about what runs after it.
		acting := d.RuntimeState == "starting" || d.RuntimeState == "stopping" || d.RuntimeState == "restarting"
		withheld := !d.LastActAt.IsZero() && now.Sub(d.LastActAt) < runtimeGrace && (acting || !r.AttemptedAt.After(d.LastActAt))
		late := withheld || (unknown || r.Stale) && !r.ObservedAt.IsZero() && now.Sub(r.ObservedAt) < runtimeGrace
		// A deployment with nothing applied has no runtime to read; its
		// readings (often a refusal while its first deploy runs) say nothing.
		applied := d.ObservedGeneration > 0 || d.LastApplied != nil
		if applied {
			out.Known = out.Known && (!unknown || late || down)
			out.Stale = out.Stale || (r.Stale && !late)
			if r.ObservedAt.IsZero() || r.ObservedAt.Before(out.ObservedAt) {
				out.ObservedAt = r.ObservedAt
			}
		}
		label := d.DisplayLabel
		if label == "" {
			label = d.Name
		}
		base := fmt.Sprintf("%s/%d/%d", d.ID, d.ObservedGeneration, d.DesiredGeneration)
		href := "/projects/" + url.PathEscape(p.Slug) + "/deployments#diagnostics-" + url.PathEscape(d.Name)
		add := func(kind, title, detail, severity string, at time.Time, stale bool) {
			word := labelFailed
			if severity == severityAdvisory && r.State == deployment.RuntimeStopped {
				word = labelStopped
			}
			out.Items = append(out.Items, Item{StatusLabel: word, Key: base + "/" + kind, Title: projectName + " · " + label + ": " + title, Detail: label + ": " + detail, Severity: severity, Href: href, ObservedAt: at, Stale: stale, Unknown: stale})
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
		held := d.RuntimeState != "stopped" && d.Hold != nil && (unknown || r.Stale || r.State != deployment.RuntimeRunning)
		if held {
			title, detail := "Check whether the app stopped", "AppTown attempted to stop this generation after repeated crashes."
			if d.Hold.Confirmed {
				title, detail = "Stopped after repeated crashes", "AppTown stopped this generation after repeated crashes."
			}
			at, _ := time.Parse(time.RFC3339Nano, d.Hold.At)
			add("crash-hold", title, detail, SeverityWarning, at, unknown || r.Stale)
			if down && d.Hold.Confirmed {
				out.Items[len(out.Items)-1].Unknown = false
			}
		}
		// A queued first check is incomplete coverage, not an actionable failure.
		pending := d.LastApplied == nil && r.AttemptedAt.IsZero() && r.ObservedAt.IsZero() && r.Err == nil
		switch {
		case d.RuntimeState == "stopped": // The owner has recorded an intentional stop.
		case !applied:
			// Nothing has been applied here yet: there is no runtime to know or
			// miss. Its release conditions above still apply.
		case withheld: // The deployment's status says it is changing.
		case (unknown || r.Stale) && !pending && !late && !down:
			add("runtime", "App status not known", "The latest runtime observation is unavailable or out of date.", SeverityWarning, r.ObservedAt, true)
		case r.State == deployment.RuntimeStopped && !held && !(down && (unknown || r.Stale)):
			// The absence is observed; only its intent is unknown, which the
			// advisory says. The reading itself stays known and current.
			add("runtime", "Stopped; check whether it should be running", "The runtime is absent. This observation does not establish whether it was stopped intentionally.", severityAdvisory, r.ObservedAt, r.Stale)
		case r.Health == "unhealthy" || r.Rollout == "failed":
			add("runtime", "Deployment runtime needs attention", "The observed serving generation is unhealthy or its provider rollout failed.", SeverityWarning, r.ObservedAt, r.Stale)
		}
	}
	out.Folded = subjects > 0 && folded == subjects
	return out, nil
}
