// Package attention composes current, authorized operational observations.
// Source owners retain observation, freshness and recovery semantics. Reads here
// must use their nonblocking snapshot ports, never provider calls or HTTP handlers.
package attention

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

// Item is a current condition, not a historical incident or a mutable workflow.
// Owners supply stable subject/generation keys and safe human-readable text.
const (
	SeverityWarning  = "warning"
	severityAdvisory = "advisory"
)

type Item struct {
	Count       int            `json:"count,omitempty"`
	StatusLabel AttentionLabel `json:"statusLabel"`

	Severity   string    `json:"severity"`
	Key        string    `json:"key"`
	Title      string    `json:"title"`
	Detail     string    `json:"detail"`
	Href       string    `json:"href"`
	ObservedAt time.Time `json:"observedAt"`
	Stale      bool      `json:"stale"`
	Unknown    bool      `json:"unknown,omitempty"`
}

// Snapshot can retain stale items while its source is unavailable. ObservedAt
// describes the actual source evidence; nested item timestamps remain unchanged.
type Snapshot struct {
	Name, Href, NextStep string
	Coverage             []Coverage
	// Reason is an owner-supplied safe summary, never a raw provider error.
	Reason     string
	Items      []Item
	ObservedAt time.Time
	Known      bool
	Stale      bool
}

// instanceReader reads an already-authorized instance snapshot. Cold/expired
// reads request a bounded owner refresh and return immediately as unassessed.
type instanceReader interface {
	Snapshot(context.Context) (Snapshot, error)
}

// projectReader reads only the named authorized project's snapshot. A snapshot
// for an old deployment identity must never become current under a newer one.
type projectReader interface {
	ProjectSnapshot(context.Context, ids.ID) (Snapshot, error)
}

// Sources is the complete finite source set. Missing sources remain unassessed,
// rather than silently shrinking what an empty Attention view claims to cover.
type Sources struct {
	Integrations instanceReader
	Checks       interface {
		Snapshot(context.Context, []ids.ID) (Snapshot, error)
	}
	Disks       instanceReader
	Capacity    instanceReader
	Inventory   instanceReader
	Workers     instanceReader
	Deployments projectReader
	Protection  projectReader
}

// Coverage reports source availability separately from the visible item count.
// No underlying error strings or provider details cross this boundary.
type Coverage struct {
	Href       string    `json:"href"`
	NextStep   string    `json:"nextStep"`
	Reason     string    `json:"reason,omitempty"`
	Key        string    `json:"key"`
	Name       string    `json:"name"`
	ObservedAt time.Time `json:"observedAt"`
	Unknown    bool      `json:"unknown"`
	Stale      bool      `json:"stale"`
}

type View struct {
	Items   []Item     `json:"items"`
	Sources []Coverage `json:"sources"`
}

type Service struct {
	authority access.ReadAuthorizer
	sources   Sources
}

func New(authority access.ReadAuthorizer, sources Sources) *Service {
	return &Service{authority: authority, sources: sources}
}

// Read resolves current scope before collecting observations. No authority
// decision or caller-filtered response is cached. A denied instance source is
// omitted entirely, including its coverage/count; an authority failure aborts.
func (s *Service) Read(ctx context.Context) (View, error) {
	empty := View{Items: []Item{}, Sources: []Coverage{}}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if s == nil || s.authority == nil {
		return empty, access.ErrAuthorityUnavailable
	}
	projects, err := s.authority.VisibleProjects(ctx, access.CapDeploymentRead)
	if err != nil {
		return empty, err
	}
	_, instanceErr := s.authority.Require(ctx, access.Permission{
		Capability: access.CapDeploymentRead,
		Resource:   access.Resource{Type: "instance"},
		Context:    access.Context{Route: "attention.read"},
	})
	if instanceErr != nil && fault.KindOf(instanceErr) != fault.Forbidden {
		return empty, instanceErr
	}
	out := empty
	if instanceErr == nil {
		for _, source := range []struct {
			key, name string
			source    instanceReader
		}{
			{"disks", "AppTown and worker disks", s.sources.Disks},
			{"capacity", "Deployment host capacity", s.sources.Capacity},
			{"inventory", "Deployment resources", s.sources.Inventory},
			{"workers", "Worker availability", s.sources.Workers},
		} {
			snapshot, readErr := Snapshot{}, error(nil)
			if source.source != nil {
				snapshot, readErr = s.observe(ctx, source.key, source.source.Snapshot)
			}
			if kind := fault.KindOf(readErr); kind == fault.Forbidden || kind == fault.Unauthenticated {
				return empty, readErr
			}
			out.add(source.key, source.name, snapshot, readErr)
		}
	}
	// Visibility is an owner decision. Deduplicate IDs defensively so a repeated
	// grant/subject never duplicates a condition or a background refresh request.
	seen := make(map[ids.ID]bool)
	for _, project := range projects {
		if project.IsZero() || seen[project] {
			continue
		}
		seen[project] = true
		// Recheck this project at the consequential read boundary, not only when the
		// initial visible-project list was assembled.
		_, err := s.authority.Require(ctx, access.Permission{Capability: access.CapDeploymentRead,
			Resource: access.Resource{Type: "project", ID: project.String(), ProjectID: project},
			Context:  access.Context{Route: "attention.project"}})
		if fault.KindOf(err) == fault.Forbidden {
			continue
		}
		if err != nil {
			return empty, err
		}
		for _, source := range []struct {
			key, name string
			source    projectReader
		}{
			{"deployments", "Deployment health", s.sources.Deployments},
			{"protection", "Data protection", s.sources.Protection},
		} {
			snapshot, readErr := Snapshot{}, error(nil)
			if source.source != nil {
				snapshot, readErr = s.observe(ctx, source.key+"/"+project.String(), func(ctx context.Context) (Snapshot, error) { return source.source.ProjectSnapshot(ctx, project) })
			}
			if kind := fault.KindOf(readErr); kind == fault.Forbidden || kind == fault.Unauthenticated {
				return empty, readErr
			}
			out.add(source.key+"/"+project.String(), source.name, snapshot, readErr)
		}
	}
	// Integration visibility is the same instance permission as its list, not
	// deployment visibility. Denial omits the source; authority failures abort.
	_, integrationErr := s.authority.Require(ctx, access.Permission{Capability: access.CapProjectRead, Resource: access.Resource{Type: "instance"}, Context: access.Context{Route: "attention.integrations"}})
	if integrationErr == nil {
		snap, err := Snapshot{}, error(nil)
		if s.sources.Integrations != nil {
			snap, err = s.sources.Integrations.Snapshot(ctx)
		}
		out.add("integrations", "Integrations", snap, err)
	} else if fault.KindOf(integrationErr) != fault.Forbidden {
		return empty, integrationErr
	}
	checkProjects, err := s.authority.VisibleProjects(ctx, access.CapConformanceRead)
	if err != nil {
		return empty, err
	}
	authorized := []ids.ID{}
	checked := map[ids.ID]bool{}
	for _, id := range checkProjects {
		if id.IsZero() || checked[id] {
			continue
		}
		checked[id] = true
		_, err := s.authority.Require(ctx, access.Permission{Capability: access.CapConformanceRead, Resource: access.Resource{Type: "project", ID: id.String(), ProjectID: id}, Context: access.Context{Route: "attention.checks"}})
		if fault.KindOf(err) == fault.Forbidden {
			continue
		}
		if err != nil {
			return empty, err
		}
		authorized = append(authorized, id)
	}
	if len(authorized) > 0 {
		snap, readErr := Snapshot{}, error(nil)
		if s.sources.Checks != nil {
			snap, readErr = s.sources.Checks.Snapshot(ctx, authorized)
		}
		if kind := fault.KindOf(readErr); kind == fault.Forbidden || kind == fault.Unauthenticated {
			return empty, readErr
		}
		out.add("checks", "App checks", snap, readErr)
	}

	if err := ctx.Err(); err != nil {
		return empty, err
	}
	sort.Slice(out.Items, func(i, j int) bool { return out.Items[i].Key < out.Items[j].Key })
	sort.Slice(out.Sources, func(i, j int) bool { return out.Sources[i].Key < out.Sources[j].Key })
	return out, nil
}

func (v *View) add(key, name string, snapshot Snapshot, err error) {
	if snapshot.Name != "" {
		name = snapshot.Name
	}
	coverage := Coverage{Href: snapshot.Href, NextStep: snapshot.NextStep, Reason: snapshot.Reason, Key: key, Name: name, ObservedAt: snapshot.ObservedAt,
		Unknown: !snapshot.Known || err != nil, Stale: snapshot.Stale || err != nil}
	if err != nil {
		coverage.Reason = "The source check failed."
	}
	firstItem := len(v.Items)
	seen := make(map[string]bool)
	for _, item := range snapshot.Items {
		// An invalid owner key cannot support a stable current condition. Preserve
		// unknown coverage rather than silently displaying an apparently clear view.
		if item.Key == "" || seen[item.Key] {
			coverage.Unknown = true
			continue
		}
		seen[item.Key] = true
		item.Key = fmt.Sprintf("%s/%s", key, item.Key)
		item.Stale = item.Stale || err != nil
		item.Unknown = item.Unknown || err != nil
		v.Items = append(v.Items, item)
	}
	if coverage.Unknown {
		for i := firstItem; i < len(v.Items); i++ {
			v.Items[i].Stale = true
		}
	}
	if coverage.Reason == "" && coverage.Unknown {
		coverage.Reason = "No usable reading has been recorded yet."
	}
	if coverage.NextStep == "" {
		coverage.NextStep = "Open the resource to check its settings. Ask an administrator to run the check."
	}
	if coverage.Href == "" {
		coverage.Href = "/infrastructure"
	}
	if len(snapshot.Coverage) > 0 && err == nil {
		for _, child := range snapshot.Coverage {
			child.Key = key + "/" + child.Key
			v.Sources = append(v.Sources, child)
		}
	} else {
		v.Sources = append(v.Sources, coverage)
	}
}

// sourceEvidence contains owner observations, never caller grants or a View.
// It is selected only after current authority admits this source and subject.
func (s *Service) observe(ctx context.Context, key string, read func(context.Context) (Snapshot, error)) (Snapshot, error) {
	return read(ctx)
}

func (v Item) MarshalJSON() ([]byte, error) {
	type view Item
	out := view(v)
	if out.StatusLabel == 0 {
		out.StatusLabel = labelWaiting
	}
	if v.Unknown {
		out.StatusLabel = labelUnknown
	}
	return json.Marshal(out)
}
