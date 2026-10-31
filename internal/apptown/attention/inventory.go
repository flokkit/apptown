package attention

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"sync"
	"time"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/ids"
)

type InventoryReading struct {
	Items          []deploy.Resource
	ObservedAt     time.Time
	Unknown, Stale bool
	// Unreported: the target's kind has no inventory reader. Its presence is
	// unknown by design, not a failed assessment, so it raises no warning.
	Unreported bool
}
type comparedInventory struct {
	revision int64
	at       time.Time
	items    []Item
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
	Now      func() time.Time
	mu       sync.Mutex
	compared map[ids.ID]comparedInventory
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
	present := map[ids.ID]bool{}
	for _, t := range targets {
		if t.Status == deployment.TargetRetired {
			continue
		}
		present[t.ID] = true
		records, recordErr := s.Records.Recorded(ctx, t)
		sample, readErr := s.Observer.Snapshot(ctx, t)
		unknown := recordErr != nil || readErr != nil || sample.Unknown || sample.ObservedAt.IsZero()
		out.Known = out.Known && !unknown
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
		s.mu.Lock()
		prior, found := s.compared[t.ID]
		s.mu.Unlock()
		if !unknown && !sample.Stale {
			for _, r := range deployment.MergeInventory(records, sample.Items) {
				if r.Record != deploy.RecordMissing {
					continue
				}
				items = append(items, Item{Key: inventoryConditionKey(t.ID, r), Title: label + ": resource missing", Detail: label + ": the complete inventory did not contain a recorded " + r.Kind + ".", Severity: SeverityWarning, Href: href, ObservedAt: sample.ObservedAt})
			}
			s.mu.Lock()
			if s.compared == nil {
				s.compared = make(map[ids.ID]comparedInventory)
			}
			if newer, ok := s.compared[t.ID]; ok && newer.revision == t.SettingsRevision && newer.at.After(sample.ObservedAt) {
				items = slices.Clone(newer.items)
			} else {
				s.compared[t.ID] = comparedInventory{revision: t.SettingsRevision, at: sample.ObservedAt, items: slices.Clone(items)}
			}
			s.mu.Unlock()
		} else if found && prior.revision == t.SettingsRevision {
			for _, item := range prior.items {
				if recordErr != nil || currentSubjects[item.Key] {
					item.Stale = true
					items = append(items, item)
				}
			}
		}
		out.Items = append(out.Items, items...)
		if unknown && !sample.Unreported {
			out.Items = append(out.Items, Item{Key: t.ID.String() + "/unassessed", Title: label + ": resources not known", Detail: label + ": current resource presence and target availability could not be confirmed.", Severity: SeverityWarning, Href: href, ObservedAt: sample.ObservedAt, Stale: true})
		}
	}
	s.mu.Lock()
	for id := range s.compared {
		if !present[id] {
			delete(s.compared, id)
		}
	}
	s.mu.Unlock()
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
