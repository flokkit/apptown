package attention

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"

	"github.com/flokkit/apptown/internal/apptown/maintenance"
	"github.com/flokkit/apptown/internal/diskspace"
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
	out := Snapshot{ObservedAt: reading.ObservedAt, Known: !reading.Unknown && !reading.ObservedAt.IsZero(), Stale: reading.Stale}
	for _, disk := range reading.Disks {
		r := disk.Reading
		unknown := disk.Unknown || r.Unknown != ""
		failedStale := disk.Stale && !disk.FailedAt.IsZero()
		if !unknown && !failedStale && !r.Low && !r.Refuses && !r.AboveWarning() {
			continue
		}
		title, detail := "Disk space is low", "Free space is below the warning threshold."
		if r.AboveWarning() {
			title = fmt.Sprintf("Disk is %.0f%% full", 100*(1-float64(r.FreeBytes)/float64(r.TotalBytes)))
			detail = "Disk use is above 80%."
			categories := append([]diskspace.Category(nil), r.Categories...)
			sort.Slice(categories, func(i, j int) bool { return categories[i].Bytes > categories[j].Bytes })
			var biggest []string
			for _, c := range categories {
				if c.Unknown == "" && c.Bytes > 0 && len(biggest) < 3 {
					biggest = append(biggest, fmt.Sprintf("%s %.1f GiB", c.Name, float64(c.Bytes)/(1<<30)))
				}
			}
			if len(biggest) > 0 {
				detail += " Biggest measured categories: " + strings.Join(biggest, ", ") + "."
			}
		}
		if r.Refuses {
			if r.AboveWarning() {
				title += "; new work is blocked"
				detail += " Free space is below the admission threshold."
			} else {
				title, detail = "Disk space blocks new work", "Free space is below the admission threshold."
			}
		}
		if unknown || failedStale {
			title, detail = "Disk space could not be assessed", "The latest disk reading is unavailable. "+disk.Reason
			if unknown {
				out.Known = false
			} else {
				title = "Disk check failed"
			}
			if r.Unknown == "" && (r.Low || r.Refuses || r.AboveWarning()) {
				if r.AboveWarning() {
					detail += " Last successful reading was above 80% full."
				} else {
					detail += " Last successful reading was below the warning threshold."
				}
				if r.Refuses {
					detail += " It was also below the admission threshold."
				}
			}
		}
		if r.Unknown == "" {
			detail = fmt.Sprintf("%.1f GiB free of %.1f GiB. Warning below %.1f GiB. ", float64(r.FreeBytes)/(1<<30), float64(r.TotalBytes)/(1<<30), float64(r.WarningBytes)/(1<<30)) + detail
		}
		if disk.Controller {
			detail += " This is advisory: controller admission is not protected by this disk reading."
		} else if r.Refuses || unknown {
			detail += " The worker refuses new tasks when its disk is below the admission threshold or cannot be read."
		}
		label := labelWaiting
		if !unknown && !failedStale {
			label = labelLow
		}
		out.Items = append(out.Items, Item{StatusLabel: label, Severity: SeverityWarning, Key: fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%q/%q", r.What, r.Path)))), Unknown: unknown, Title: r.What + ": " + title, Detail: r.What + ": " + detail, Href: "/usage", ObservedAt: r.ReadAt, Stale: disk.Stale || unknown})
	}
	return out, err
}
