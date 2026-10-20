package deployment

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"log"
	"sort"
	"sync"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/deploy"
)

const observationInterval = 15 * time.Second
const observationSweepTimeout = 10 * time.Second
const observationStaleAfter = 45 * time.Second

type observationEntry struct {
	key   [32]byte
	value RuntimeObservation
}
type observations struct {
	sweep   sync.Mutex
	mu      sync.RWMutex
	entries map[ids.ID]observationEntry
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
func (o *observations) read(r listingRecord, now time.Time) RuntimeObservation {
	o.mu.RLock()
	entry, ok := o.entries[r.deployment.ID]
	o.mu.RUnlock()
	if !ok || entry.key != observationKey(r) {
		return RuntimeObservation{Stale: true}
	}
	v := entry.value
	v.Stale = v.ObservedAt.IsZero() || now.Sub(v.ObservedAt) > observationStaleAfter || v.Err != nil
	return v
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
		jobs := make(chan int, len(records))
		for i, r := range records {
			if (r.target != nil && r.target.Kind == KindDockerHost) == dockerHost {
				jobs <- i
			}
		}
		close(jobs)
		for range min(4, len(jobs)) {
			workers.Add(1)
			go func() {
				defer workers.Done()
				for i := range jobs {
					values[i] = s.observeRuntime(ctx, records[i])
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
	for _, r := range current {
		keys[r.deployment.ID] = observationKey(r)
	}
	s.observations.mu.Lock()
	defer s.observations.mu.Unlock()
	for id, entry := range s.observations.entries {
		if key, ok := keys[id]; !ok || key != entry.key {
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
				v.State, v.ObservedAt, v.URL, v.Rollout, v.Health = old.value.State, old.value.ObservedAt, old.value.URL, old.value.Rollout, old.value.Health
				if v.AttemptedAt.IsZero() {
					v.AttemptedAt = old.value.AttemptedAt
				}
			}
		}
		s.observations.entries[r.deployment.ID] = observationEntry{key: key, value: v}
	}
	return nil
}
func (s *Service) observeRuntime(ctx context.Context, r listingRecord) RuntimeObservation {
	now := s.now()
	v := RuntimeObservation{}
	ctx, cancel := context.WithTimeout(ctx, s.observationTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		v.Err = err
		return v
	}
	v.AttemptedAt = now
	if r.target == nil {
		v.Err = errors.New("deployment has no target")
		return v
	}
	if r.deployment.DesiredGeneration != r.deployment.ObservedGeneration {
		v.Err = errors.New("deployment replacement is in progress")
		return v
	}
	driver, err := s.providers.Driver(ctx, r.target, r.deployment)
	if err != nil {
		v.Err = err
		return v
	}
	observed, err := driver.Observe(ctx, r.deployment.Placement())
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		v.Err = err
		return v
	}
	if observed.Generation != r.deployment.ObservedGeneration {
		v.Err = errors.New("provider observation has a different generation")
		return v
	}
	v.State, v.ObservedAt, v.URL, v.Rollout, v.Health = runtimeState(observed), s.now(), observed.URL, observed.Rollout, observed.Health
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
