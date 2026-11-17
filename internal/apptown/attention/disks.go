package attention

import (
	"context"
	"crypto/sha256"
	"fmt"

	"github.com/flokkit/apptown/internal/apptown/maintenance"
)

// Disks translates the maintenance owner's cached evidence. Authority is checked
// by Service before Snapshot is called; it never requests a synchronous read.
type Disks struct {
	Observer interface {
		Snapshot(context.Context) (maintenance.DiskSnapshot, error)
	}
}

func (d Disks) Snapshot(ctx context.Context) (Snapshot, error) {
	if d.Observer == nil {
		return Snapshot{}, nil
	}
	reading, err := d.Observer.Snapshot(ctx)
	out := Snapshot{ObservedAt: reading.ObservedAt, Known: len(reading.Disks) > 0 && !reading.ObservedAt.IsZero(), Stale: len(reading.Disks) == 0 && reading.Stale}
	for _, disk := range reading.DisplayDisks() {
		out.Stale = out.Stale || disk.Stale
		r := disk.Reading
		unknown := !disk.Known()
		if !unknown && !r.Low && !r.Refuses {
			continue
		}
		title, detail, label := "Disk space is low", "Free space is below the warning threshold.", labelLow
		if r.Refuses {
			title, detail = "Disk space blocks new work", "Free space is below the admission threshold."
		}
		if unknown {
			title, detail, label = "disk space not known", "There is no recent disk reading.", labelUnknown
			if !disk.FailedAt.IsZero() && disk.Reason != "" {
				detail = disk.Reason
			}
			out.Known = false
		} else {
			detail = fmt.Sprintf("%.1f GiB free of %.1f GiB. Warning below %.1f GiB. ", float64(r.FreeBytes)/(1<<30), float64(r.TotalBytes)/(1<<30), float64(r.WarningBytes)/(1<<30)) + detail
			if disk.Controller {
				detail += " This is advisory: controller admission is not protected by this disk reading."
			}
		}
		out.Items = append(out.Items, Item{StatusLabel: label, Severity: SeverityWarning, Key: fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%q/%q", r.What, r.Path)))), Unknown: unknown, Title: r.What + ": " + title, Detail: detail, Href: "/usage", ObservedAt: r.ReadAt, Stale: disk.Stale || unknown})
	}
	return out, err
}
