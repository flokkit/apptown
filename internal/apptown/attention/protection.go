package attention

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/flokkit/apptown/internal/apptown/dataprotection"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/ids"
)

// Protection presents the copy owner's classifications only for deployments in
// the authorized project. It never interprets backup tables or calls providers.
type Protection struct {
	Projects interface {
		ByID(context.Context, ids.ID) (project.Project, error)
	}
	Deployments interface {
		ForProject(context.Context, ids.ID) ([]*deployment.Deployment, error)
	}
	Observer interface {
		Snapshot(context.Context, ids.ID) (dataprotection.ProtectionSnapshot, error)
	}
	Now func() time.Time
}

func (p Protection) ProjectSnapshot(ctx context.Context, id ids.ID) (Snapshot, error) {
	if p.Projects == nil || p.Deployments == nil || p.Observer == nil {
		return Snapshot{}, nil
	}
	project, err := p.Projects.ByID(ctx, id)
	if err != nil {
		return Snapshot{}, err
	}
	deps, err := p.Deployments.ForProject(ctx, id)
	if err != nil {
		return Snapshot{}, err
	}
	projectName := project.Name
	if projectName == "" {
		projectName = project.Slug
	}
	out := Snapshot{Known: true, ObservedAt: clock.Or(p.Now)()}
	href := "/projects/" + url.PathEscape(project.Slug) + "/settings/deployments"
	for _, dep := range deps {
		if dep.Kind != deployment.Standing {
			continue
		}
		reading, readErr := p.Observer.Snapshot(ctx, dep.ID)
		unknown := reading.Unknown || readErr != nil
		out.Known = out.Known && !unknown
		if reading.Reason != "" {
			if out.Reason != "" {
				out.Reason += " "
			}
			out.Reason += dep.Name + ": " + reading.Reason
		}
		out.Stale = out.Stale || reading.Stale
		if reading.ObservedAt.IsZero() || reading.ObservedAt.Before(out.ObservedAt) {
			out.ObservedAt = reading.ObservedAt
		}
		label := dep.DisplayLabel
		if label == "" {
			label = dep.Name
		}
		base := fmt.Sprintf("%s/%d/%d", dep.ID, dep.ObservedGeneration, dep.DesiredGeneration)
		add := func(key, title, detail, severity string, at time.Time) {
			out.Items = append(out.Items, Item{Key: base + "/" + key, Unknown: unknown, Title: projectName + " · " + label + ": " + title, Detail: label + ": " + detail, Severity: severity, Href: href, ObservedAt: at, Stale: reading.Stale || unknown})
		}
		// A queued first check is incomplete coverage, not an actionable failure.
		pending := reading.AttemptedAt.IsZero() && reading.ObservedAt.IsZero() && readErr == nil
		if (unknown || reading.Stale && !reading.FailedAt.IsZero()) && !pending && dep.DataClass == "production" {
			title := "Backups not known"
			if !unknown {
				title = "Protection check failed"
			}
			add("unassessed", title, "Current protection coverage is unavailable or out of date. "+reading.Reason, SeverityWarning, reading.ObservedAt)
		}
		// Copy cadence and disposable classification come from data protection.
		// Previews and non-production data acquire no universal backup requirement.
		required := 0
		for _, item := range reading.Items {
			if dep.DataClass != "production" || item.Copy == dataprotection.CopyDisposable {
				continue
			}
			required++
			switch item.Copy {
			case dataprotection.CopyFresh:
			case dataprotection.CopyStale:
				detail := item.Need + " has an overdue copy or a failed latest copy."
				if !item.CopyAt.IsZero() {
					detail += " Last successful copy: " + item.CopyAt.UTC().Format(time.RFC3339) + "."
				}
				add(item.Key+"/copy", "Check backups", detail, SeverityWarning, reading.ObservedAt)
			case dataprotection.CopyNone:
				add(item.Key+"/copy", "Backup missing", item.Need+" has no retained successful copy.", SeverityWarning, reading.ObservedAt)
			default:
				out.Known = false
				add(item.Key+"/copy", "A required data copy could not be assessed", item.Need+" has unknown copy coverage.", SeverityWarning, reading.ObservedAt)
			}
			if item.DrillKnown && !item.DrillPassed {
				add(item.Key+"/drill", "A data recovery check failed", item.Need+" did not pass its latest recovery check.", SeverityWarning, item.DrillAt)
			}
		}
		if required == 0 {
			continue
		}
		if !reading.OffHost {
			add("off-host", "Production data is not protected off-host", "No current off-host protection is recorded for this data.", SeverityWarning, reading.ObservedAt)
		}
		if reading.RestoreSupported && !reading.RestoreAvailable {
			add("restore-unavailable", "Data recovery is currently unavailable", "Open deployment protection to inspect the recovery requirements.", SeverityWarning, reading.ObservedAt)
		}
		if !reading.Rehearsed {
			add("rehearsal", "Recovery has not been rehearsed", "A successful data copy does not prove application recovery.", SeverityAdvisory, reading.ObservedAt)
		}
		if reading.Rehearsed && !reading.RehearsalPassed {
			if reading.RehearsalOutcome == dataprotection.RehearsalFailed {
				add("rehearsal", "A recovery rehearsal failed", "The latest rehearsal did not demonstrate application recovery.", SeverityWarning, reading.RehearsalAt)
			} else {
				out.Known = false
				add("rehearsal", "A recovery rehearsal has an unknown result", "The latest rehearsal does not establish whether recovery succeeded.", SeverityWarning, reading.RehearsalAt)
			}
		}
		if reading.RestoreTested {
			switch reading.RestoreOutcome {
			case dataprotection.RehearsalPassed, dataprotection.RehearsalPassedNoData:
			case dataprotection.RehearsalFailed:
				add("restore-test", "A platform recovery test failed", "The latest test did not demonstrate recovery on this deployment's platform.", SeverityWarning, reading.RestoreAt)
			default:
				out.Known = false
				add("restore-test", "A platform recovery test has an unknown result", "The latest test does not establish whether recovery succeeded.", SeverityWarning, reading.RestoreAt)
			}
		}
		if !reading.RestoreTested {
			add("restore-test", "Recovery has not been tested on its platform", "A rehearsal does not replace a restore test on the deployment's own platform.", SeverityAdvisory, reading.ObservedAt)
		}
	}
	return out, nil
}
