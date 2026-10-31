package attention

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/clock"
)

// Capacity reads current target records and cached owner observations after the
// service's instance authority check. It never calls the provider capacity API.
type Capacity struct {
	Targets interface {
		All(context.Context) ([]*deployment.Target, error)
	}
	Observer interface {
		Snapshot(context.Context, *deployment.Target) (deployment.CapacitySnapshot, error)
	}
	Now func() time.Time
}

func (c Capacity) Snapshot(ctx context.Context) (Snapshot, error) {
	if c.Targets == nil || c.Observer == nil {
		return Snapshot{}, nil
	}
	targets, err := c.Targets.All(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	out := Snapshot{Known: true, ObservedAt: clock.Or(c.Now)()}
	for _, target := range targets {
		if target.Status == deployment.TargetRetired || (target.Kind != deployment.KindDockerHost && target.Kind != deployment.KindPortainer) {
			continue
		}
		reading, readErr := c.Observer.Snapshot(ctx, target)
		if reading.Unknown || readErr != nil {
			out.Known = false
		}
		out.Stale = out.Stale || reading.Stale
		if reading.ObservedAt.IsZero() || reading.ObservedAt.Before(out.ObservedAt) {
			out.ObservedAt = reading.ObservedAt
		}
		label := target.DisplayLabel
		if label == "" {
			label = target.Name
		}
		href := "/infrastructure/" + url.PathEscape(target.Name)
		disk := reading.Disk
		// A queued first check is incomplete coverage, not an actionable failure.
		if reading.AttemptedAt.IsZero() && reading.ObservedAt.IsZero() && disk == nil && readErr == nil {
			continue
		}
		if reading.Unknown || reading.Stale || readErr != nil || disk == nil || !disk.Free.Known || !disk.Total.Known {
			out.Known = false
			at := reading.ObservedAt
			if reading.Unknown && !reading.AttemptedAt.IsZero() {
				at = reading.AttemptedAt
			}
			out.Items = append(out.Items, Item{Severity: SeverityWarning, Key: target.ID.String() + "/disk-unassessed", Title: label + ": disk space is unknown", Detail: label + ": check the host’s disk. The latest reading is unavailable or out of date. " + capacityAdmission(target.Kind), Href: href, ObservedAt: at, Stale: true})
			if disk == nil || !disk.Free.Known || !disk.Total.Known {
				continue
			}
		}
		if !disk.Low && !disk.Refuses {
			continue
		}
		title := label + ": low on disk"
		detail := fmt.Sprintf("%s: %.1f GiB free of %.1f GiB. Warning below %.1f GiB.", label, disk.Free.Value/(1<<30), disk.Total.Value/(1<<30), float64(disk.Warning)/(1<<30))
		if disk.Refuses && target.Kind == deployment.KindDockerHost {
			title = label + ": free disk space to start new work"
			detail += " New releases and previews are refused below the admission threshold."
		} else {
			detail += " Free space is below the warning threshold."
			if target.Kind == deployment.KindPortainer {
				detail += " " + capacityAdmission(target.Kind)
			}
		}
		out.Items = append(out.Items, Item{Severity: SeverityWarning, Key: target.ID.String() + "/disk", Title: title, Detail: detail, Href: href, ObservedAt: disk.ReadAt, Stale: reading.Stale || reading.Unknown || readErr != nil})
	}
	return out, nil
}

func capacityAdmission(kind string) string {
	if kind == deployment.KindDockerHost {
		return "New releases and previews are refused when the disk cannot be read or is below the admission threshold."
	}
	return "This is advisory: Portainer releases are not protected by this disk reading."
}
