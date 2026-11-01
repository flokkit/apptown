package attention

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"time"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/ids"
)

type InventoryReading struct {
	Items      []deploy.Resource
	Missing    []deploy.Resource
	FailedAt   time.Time
	Reason     string
	ObservedAt time.Time
	// AttemptedAt is when the latest inventory attempt finished, successful or not.
	AttemptedAt    time.Time
	Unknown, Stale bool
	Invalidated    bool
	// Unreported: the target's kind has no inventory reader. Its presence is
	// unknown by design, not a failed assessment, so it raises no warning.
	Unreported bool
}

// Inventory retains only conditions established by a complete current provider
// answer. A stale list cannot turn newly recorded resources into missing ones.
type Inventory struct {
	Targets interface {
		All(context.Context) ([]*deployment.Target, error)
	}
	Records interface {
		Recorded(context.Context, *deployment.Target) ([]deploy.Resource, error)
	}
	Observer interface {
		Snapshot(context.Context, *deployment.Target) (InventoryReading, error)
	}
	Now func() time.Time
}

func (s *Inventory) Snapshot(ctx context.Context) (Snapshot, error) {
	if s == nil || s.Targets == nil || s.Records == nil || s.Observer == nil {
		return Snapshot{}, nil
	}
	targets, err := s.Targets.All(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	out := Snapshot{Known: true, ObservedAt: clock.Or(s.Now)()}
	for _, t := range targets {
		if t.Status == deployment.TargetRetired {
			continue
		}
		records, recordErr := s.Records.Recorded(ctx, t)
		sample, readErr := s.Observer.Snapshot(ctx, t)
		unknown := recordErr != nil || readErr != nil || sample.Unknown || sample.ObservedAt.IsZero()
		out.Known = out.Known && !unknown
		if sample.Reason != "" {
			if out.Reason != "" {
				out.Reason += " "
			}
			out.Reason += t.Name + ": " + sample.Reason
		}
		out.Stale = out.Stale || sample.Stale
		if sample.ObservedAt.IsZero() || sample.ObservedAt.Before(out.ObservedAt) {
			out.ObservedAt = sample.ObservedAt
		}
		label := t.DisplayLabel
		if label == "" {
			label = t.Name
		}
		href := "/infrastructure/" + url.PathEscape(t.Name)
		currentSubjects := make(map[string]bool)
		for _, r := range deployment.MergeInventory(records, nil) {
			currentSubjects[inventoryConditionKey(t.ID, r)] = true
		}
		var items []Item
		missing := sample.Missing
		for _, r := range missing {
			key := inventoryConditionKey(t.ID, r)
			if recordErr == nil && !currentSubjects[key] {
				continue
			}
			items = append(items, Item{Key: key, Unknown: unknown, Title: label + ": resource missing", Detail: label + ": the complete inventory did not contain a recorded " + r.Kind + ".", Severity: SeverityWarning, Href: href, ObservedAt: sample.ObservedAt, Stale: unknown || sample.Stale || !sample.FailedAt.IsZero() || sample.Invalidated})
		}
		out.Items = append(out.Items, items...)
		// A queued first check is incomplete coverage, not an actionable failure.
		pending := sample.AttemptedAt.IsZero() && sample.ObservedAt.IsZero() && recordErr == nil && readErr == nil
		if (unknown || sample.Stale && !sample.FailedAt.IsZero()) && !sample.Unreported && !pending {
			title := label + ": resources not known"
			if !unknown {
				title = label + ": inventory check failed"
			}
			out.Items = append(out.Items, Item{Key: t.ID.String() + "/unassessed", Unknown: unknown, Title: title, Detail: label + ": current resource presence and target availability could not be confirmed. " + sample.Reason, Severity: SeverityWarning, Href: href, ObservedAt: sample.ObservedAt, Stale: true})
		}
	}
	return out, nil
}

// The resource claim, not its target's entire record list, identifies a warning.
// Changes to a sibling cannot erase evidence about an unchanged subject.
func inventoryConditionKey(target ids.ID, r deploy.Resource) string {
	raw, _ := json.Marshal(struct {
		Kind, ID, Name string
		Hosts          []deploy.ResourceHost
		Retained       *deploy.RetainedNote
	}{r.Kind, r.ID, r.Name, r.Hosts, r.Retained})
	return fmt.Sprintf("%s/missing/%x", target, sha256.Sum256(raw))
}
