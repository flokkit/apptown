package deployment

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/deploy"
)

const observationInterval = 15 * time.Second
const observationSweepTimeout = 10 * time.Second
const observationStaleAfter = 45 * time.Second

type observationEntry struct {
	key          [32]byte
	reachability [32]byte
	value        RuntimeObservation
}
type observations struct {
	readinessSweep sync.Mutex
	readiness      map[ids.ID]readinessEntry
	sweep          sync.Mutex
	mu             sync.RWMutex
	entries        map[ids.ID]observationEntry
}

// Include desired generation as well as the observed placement: an in-flight
// replacement must not inherit the previous workload's apparent running state.
func observationKey(r listingRecord) [32]byte {
	raw, _ := json.Marshal(struct {
		Placement deploy.Placement
		Target    *Target
		Desired   int64
	}{r.deployment.Placement(), r.target, r.deployment.DesiredGeneration})
	return sha256.Sum256(raw)
}

// reachabilityKey identifies the placed workload's provider connection. Unlike
// the observation/permit key, it survives non-placement settings edits.
func reachabilityKey(r listingRecord) [32]byte {

	// Labels, capacity and check status do not move a workload. Only the
	// provider connection and placed generation bind its runtime identity.
	type targetIdentity struct {
		ID, ConnectionID       ids.ID
		Kind, Provider, Origin string
		Address                map[string]any
		Scope                  json.RawMessage
	}
	var target *targetIdentity
	if t := r.target; t != nil {
		target = &targetIdentity{t.ID, t.ConnectionID, t.Kind, t.Provider, t.Origin, t.Address, t.ProviderScope}
	}
	raw, _ := json.Marshal(struct {
		Placement deploy.Placement
		Target    *targetIdentity
		Desired   int64
	}{r.deployment.Placement(), target, r.deployment.DesiredGeneration})
	return sha256.Sum256(raw)
}
func (o *observations) read(r listingRecord, now time.Time) RuntimeObservation {
	o.mu.RLock()
	entry, ok := o.entries[r.deployment.ID]
	health := o.readiness[r.deployment.ID]
	o.mu.RUnlock()
	if !ok || entry.key != observationKey(r) {
		return RuntimeObservation{State: RuntimeUnknown, TargetName: runtimeTargetName(r), Stale: true}
	}
	v := entry.value
	v.Stale = v.ObservedAt.IsZero() || now.Sub(v.ObservedAt) > observationStaleAfter || v.Err != nil
	if v.Stale && v.State != RuntimeUnreachable {
		v.State = RuntimeUnknown
	}
	v.TargetName = runtimeTargetName(r)
	return overlayReadiness(v, r, health, now)
}

// RunObservations owns the display-only observer. It starts immediately and
// never overlaps sweeps. Docker health enforcement runs independently.
func (s *Service) RunObservations(ctx context.Context) {
	for ctx.Err() == nil {
		if err := s.ObserveRuntime(ctx); err != nil && ctx.Err() == nil {
			log.Printf("deployment observation: %v", err)
		}
		timer := time.NewTimer(observationInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// ObserveRuntime refreshes all recorded placements, including previews and
// in-flight deployments. It never reconciles, settles or acts on a workload.
func (s *Service) ObserveRuntime(ctx context.Context) error {
	if !s.observations.sweep.TryLock() {
		return nil
	}
	defer s.observations.sweep.Unlock()
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, observationSweepTimeout)
	defer cancel()
	records, err := s.listRecords(ctx, nil)
	if err != nil {
		return err
	}
	// Oldest/unattempted placements go first, so a deadline cannot repeatedly
	// starve the tail of a large fleet.
	attempted := make(map[ids.ID]time.Time, len(records))
	s.observations.mu.RLock()
	for _, r := range records {
		if e, ok := s.observations.entries[r.deployment.ID]; ok && e.key == observationKey(r) {
			attempted[r.deployment.ID] = e.value.AttemptedAt
		}
	}
	s.observations.mu.RUnlock()
	sort.SliceStable(records, func(i, j int) bool {
		return attempted[records[i].deployment.ID].Before(attempted[records[j].deployment.ID])
	})
	values := make([]RuntimeObservation, len(records))
	var workers sync.WaitGroup
	// Independent pools ensure a slow cloud provider cannot use Docker capacity.
	for _, dockerHost := range []bool{true, false} {
		groups := s.observationGroups(records, dockerHost)
		jobs := make(chan observationGroup, len(groups))
		for _, g := range groups {
			jobs <- g
		}
		close(jobs)
		for range min(4, len(jobs)) {
			workers.Add(1)
			go func() {
				defer workers.Done()
				for g := range jobs {
					if g.fleet == nil {
						values[g.at[0]] = s.observeRuntime(ctx, records[g.at[0]])
						continue
					}
					s.observeFleet(ctx, g, records, values)
				}
			}()
		}
	}
	workers.Wait()
	if err := parent.Err(); err != nil {
		return err
	}
	// Re-read after external I/O; neither the selection snapshot nor a database
	// connection is held while a provider runs. Deleted/replaced rows cannot publish.
	refreshCtx, refreshCancel := context.WithTimeout(parent, 5*time.Second)
	defer refreshCancel()
	current, err := s.listRecords(refreshCtx, nil)
	if err != nil {
		return err
	}
	keys := make(map[ids.ID][32]byte, len(current))
	connections := make(map[ids.ID][32]byte, len(current))
	for _, r := range current {
		keys[r.deployment.ID] = observationKey(r)
		connections[r.deployment.ID] = reachabilityKey(r)
	}
	s.observations.mu.Lock()
	defer s.observations.mu.Unlock()
	for id, entry := range s.observations.entries {
		if connection, ok := connections[id]; !ok || connection != entry.reachability {
			delete(s.observations.entries, id)
		}
	}
	for i, r := range records {
		key := observationKey(r)
		if current, ok := keys[r.deployment.ID]; !ok || key != current {
			continue
		}
		v := values[i]
		if v.Err != nil {
			if old, ok := s.observations.entries[r.deployment.ID]; ok && old.key == key {
				v.ObservedAt, v.URL, v.Rollout, v.Health = old.value.ObservedAt, old.value.URL, old.value.Rollout, old.value.Health
				if v.AttemptedAt.IsZero() {
					v.AttemptedAt = old.value.AttemptedAt
				}
			}
		}
		connection := reachabilityKey(r)
		if old, ok := s.observations.entries[r.deployment.ID]; ok && old.reachability == connection && v.Err != nil && !old.value.UnreachableSince.IsZero() {
			v.UnreachableSince = old.value.UnreachableSince
		}
		s.observations.entries[r.deployment.ID] = observationEntry{key: key, reachability: connection, value: v}
	}
	return nil
}

// observationGroup is one job of a sweep: the records at these indexes,
// observed together by fleet with one look at their target, or a single
// record observed through its driver when fleet is nil.
type observationGroup struct {
	at    []int
	fleet deploy.FleetObserver
}

// observationGroups are a pool's jobs: the records on a target whose
// inventory can observe its whole fleet are one job per target; every
// other record is a job of its own.
func (s *Service) observationGroups(records []listingRecord, dockerHost bool) []observationGroup {
	var out []observationGroup
	byTarget := map[ids.ID]int{}
	for i, r := range records {
		if (r.target != nil && r.target.Kind == KindDockerHost) != dockerHost {
			continue
		}
		if r.target != nil {
			if g, ok := byTarget[r.target.ID]; ok {
				out[g].at = append(out[g].at, i)
				continue
			}
			if inv, err := s.providers.TargetInventory(r.target); err == nil {
				if fleet, ok := inv.(deploy.FleetObserver); ok {
					byTarget[r.target.ID] = len(out)
					out = append(out, observationGroup{at: []int{i}, fleet: fleet})
					continue
				}
			}
		}
		out = append(out, observationGroup{at: []int{i}})
	}
	return out
}

func (s *Service) observeRuntime(ctx context.Context, r listingRecord) RuntimeObservation {
	ctx, cancel := context.WithTimeout(ctx, s.observationTimeout)
	defer cancel()
	v, ok := s.observable(ctx, r)
	if !ok {
		return v
	}
	driver, err := s.providers.Driver(ctx, r.target, r.deployment)
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		v.Err = err
		if deploy.IsUnreachable(err) {
			v.State = RuntimeUnreachable
			v.UnreachableSince = v.AttemptedAt
		}
		return v
	}
	observed, err := driver.Observe(ctx, r.deployment.Placement())
	return s.observed(ctx, v, r, observed, err)
}

// observeFleet observes a target's records with one look, writing each
// record's observation at its index. A failed look is every record's error.
func (s *Service) observeFleet(ctx context.Context, g observationGroup, records []listingRecord, values []RuntimeObservation) {
	ctx, cancel := context.WithTimeout(ctx, s.observationTimeout)
	defer cancel()
	var asked []int
	var placements []deploy.Placement
	for _, i := range g.at {
		v, ok := s.observable(ctx, records[i])
		values[i] = v
		if ok {
			asked = append(asked, i)
			placements = append(placements, records[i].deployment.Placement())
		}
	}
	if len(asked) == 0 {
		return
	}
	observed, err := g.fleet.ObserveAll(ctx, placements)
	if err == nil && len(observed) != len(placements) {
		err = fmt.Errorf("the provider answered %d observations for %d deployments", len(observed), len(placements))
	}
	for n, i := range asked {
		var o deploy.Observation
		if err == nil {
			o = observed[n]
		}
		values[i] = s.observed(ctx, values[i], records[i], o, err)
	}
}

// observable starts a record's observation: when it was attempted, and
// whether the provider may be asked at all. A record with no target, or
// whose replacement is in progress, is not asked, so a new workload never
// inherits the old one's state.
func (s *Service) observable(ctx context.Context, r listingRecord) (RuntimeObservation, bool) {
	v := RuntimeObservation{State: RuntimeUnknown}
	if err := ctx.Err(); err != nil {
		v.Err = err
		return v, false
	}
	v.AttemptedAt = s.now()
	if r.target == nil {
		v.Err = errors.New("deployment has no target")
		return v, false
	}
	if r.deployment.DesiredGeneration != r.deployment.ObservedGeneration {
		v.Err = errors.New("deployment replacement is in progress")
		return v, false
	}
	return v, true
}

// observed finishes a record's observation from the provider's answer: an
// error, a cut-off look or another generation's answer is not a state.
func (s *Service) observed(ctx context.Context, v RuntimeObservation, r listingRecord, observed deploy.Observation, err error) RuntimeObservation {
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		v.Err = err
		if deploy.IsUnreachable(err) {
			v.State = RuntimeUnreachable
			v.UnreachableSince = v.AttemptedAt
		}
		return v
	}
	if observed.Generation != r.deployment.ObservedGeneration {
		v.Err = errors.New("provider observation has a different generation")
		return v
	}
	v.State, v.ObservedAt, v.URL, v.Rollout, v.Health, v.running = runtimeState(observed), s.now(), observed.URL, observed.Rollout, observed.Health, observed.Running
	return v
}

// RuntimeDeployment is a display observation with its recorded routing facts.
type RuntimeDeployment struct {
	ProjectID          ids.ID
	Project, Name, URL string
	HostOnly           bool // URL answers on the AppTown host only (Reach)
	HostPort           int
	Observation        RuntimeObservation
}

// Runtime is the checked cross-project deployment display for activity readers.
func (s *Service) Runtime(ctx context.Context) ([]RuntimeDeployment, error) {
	return s.runtimeProjects(ctx, access.CapDeploymentRead, nil)
}

// ProjectRuntime exposes only main/prod badges for readable requested projects.
func (s *Service) ProjectRuntime(ctx context.Context, idList []ids.ID) (map[ids.ID]RuntimeObservation, error) {
	out := make(map[ids.ID]RuntimeObservation)
	if len(idList) == 0 {
		return out, ctx.Err()
	}
	rows, err := s.runtimeProjects(ctx, access.CapProjectRead, idList)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if r.Name == "main" || (r.Name == "prod" && out[r.ProjectID].ObservedAt.IsZero()) {
			out[r.ProjectID] = r.Observation
		}
	}
	return out, nil
}

// RecentReleases is the newest releases of the projects where the verified
// caller can read both the project and its releases, newest first: a row
// needs both to be shown. The selection is made before the limit, so releases
// the caller could not be shown never crowd out the ones they can.
func (s *Service) RecentReleases(ctx context.Context, limit int) ([]*Release, error) {
	authority, ok := s.authority.(access.ReadAuthorizer)
	if !ok {
		return nil, access.ErrAuthorityUnavailable
	}
	releases, err := authority.VisibleProjects(ctx, access.CapReleaseRead)
	if err != nil {
		return nil, err
	}
	projects, err := authority.VisibleProjects(ctx, access.CapProjectRead)
	if err != nil {
		return nil, err
	}
	readable := make(map[ids.ID]bool, len(projects))
	for _, id := range projects {
		readable[id] = true
	}
	idList := make([]ids.ID, 0, len(releases))
	for _, id := range releases {
		if readable[id] {
			idList = append(idList, id)
		}
	}
	return s.Releases.Recent(ctx, idList, limit)
}

func (s *Service) runtimeProjects(ctx context.Context, cap access.Capability, requested []ids.ID) ([]RuntimeDeployment, error) {
	authority, ok := s.authority.(access.ReadAuthorizer)
	if !ok {
		return nil, access.ErrAuthorityUnavailable
	}
	idList, err := authority.VisibleProjects(ctx, cap)
	if err != nil {
		return nil, err
	}
	if requested != nil {
		selected := make(map[ids.ID]bool, len(requested))
		for _, id := range requested {
			selected[id] = true
		}
		filtered := make([]ids.ID, 0, len(requested))
		for _, id := range idList {
			if selected[id] {
				filtered = append(filtered, id)
			}
		}
		idList = filtered
	}
	out := []RuntimeDeployment{}
	if len(idList) == 0 {
		return out, ctx.Err()
	}
	rows, err := s.listRecords(ctx, idList)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		e := r.deployment
		observed := s.observations.read(r, s.now())
		reach := s.reachedAt(ctx, r.project, e, r.target, observed.URL)
		out = append(out, RuntimeDeployment{ProjectID: e.ProjectID, Project: r.project, Name: e.Name, URL: reach.URL, HostOnly: reach.HostOnly, HostPort: e.HostPort, Observation: observed})
	}
	return out, ctx.Err()
}

func runtimeTargetName(r listingRecord) string {
	if r.target == nil {
		return ""
	}
	return r.target.DisplayLabel
}
