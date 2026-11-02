package deployment

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/apptown/jobs"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/ids"
)

const HostSnapshotJob = "host-snapshot"

// hostReading retains successful section evidence across partial attempts.
// Invalidated means the evidence predates an act; deriving a view never renews it.
type hostReading struct {
	PredatesActAt time.Time
	docker.HostFacts
	AttemptedAt     time.Time
	Outcome         string
	WaitedFor, Took time.Duration
	Invalidated     bool
	Instance        string
	Also            []string
	Shared          []SharedResource
	Missing         []deploy.Resource
	PlacementReason string
}

type HostSnapshots struct {
	db  *database.DB
	now func() time.Time
}

func NewHostSnapshots(db *database.DB, now func() time.Time) *HostSnapshots {
	return &HostSnapshots{db: db, now: clock.Or(now)}
}

// reading resolves the settings revision without contacting the provider.
func (s *HostSnapshots) reading(ctx context.Context, t *Target) (hostReading, error) {
	row, err := deploymentsql.New(s.db.SQL()).Reading(ctx, deploymentsql.ReadingParams{Subject: t.ID.String(), Kind: HostSnapshotJob})
	if errors.Is(err, sql.ErrNoRows) || err == nil && row.Identity != strconv.FormatInt(t.SettingsRevision, 10) {
		return hostReading{}, nil
	}
	if err != nil {
		return hostReading{}, err
	}
	var r hostReading
	err = json.Unmarshal(row.Payload, &r)
	r.Invalidated = row.Invalidated
	return r, err
}

func mergeHostSection[T any](old *docker.HostSection[T], next docker.HostSection[T]) bool {
	if next.Value != nil {
		*old = next
		return true
	}
	old.Reason = next.Reason
	return false
}

// collect fences settings edits and acts with the reading's generation. A
// failed attempt is persisted as evidence, not retried as a second host read.
func (s *HostSnapshots) collect(ctx context.Context, t *Target, dk docker.Scope, q docker.HostQuery, also []string, records *Inventory) error {
	queries := deploymentsql.New(s.db.SQL())
	row, err := queries.StartHostSnapshot(ctx, deploymentsql.StartHostSnapshotParams{ID: ids.New(s.now()), Subject: t.ID.String(), Identity: strconv.FormatInt(t.SettingsRevision, 10)})
	if err != nil {
		return err
	}
	prior, err := s.reading(ctx, t)
	if err != nil {
		return err
	}
	started := s.now()
	facts, readErr := docker.ReadHost(ctx, dk, q)
	if errors.Is(ctx.Err(), context.Canceled) {
		return ctx.Err()
	}
	finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	current, err := NewTargets(s.db, nil).ByID(finish, t.ID)
	if err != nil {
		return err
	}
	if current.SettingsRevision != t.SettingsRevision || current.Status == TargetRetired {
		return nil
	}
	prior.AttemptedAt, prior.Took, prior.Instance, prior.Also = started, s.now().Sub(started), dk.Instance, also
	successes := 0
	if mergeHostSection(&prior.Host, facts.Host) {
		successes++
	}
	if mergeHostSection(&prior.Containers, facts.Containers) {
		successes++
	}
	if mergeHostSection(&prior.Resources, facts.Resources) {
		successes++
	}
	if mergeHostSection(&prior.Images, facts.Images) {
		successes++
	}
	if mergeHostSection(&prior.Use, facts.Use) {
		successes++
	}
	prior.Outcome = "read"
	if readErr != nil {
		prior.Outcome = "partial"
		if successes == 0 {
			prior.Outcome = "failed"
		}
	}
	// Placement verification may contact Portainer, so it belongs to collection.
	prior.Shared, prior.PlacementReason = nil, ""
	if records != nil {
		shared, err := records.Shared(finish, docker.HostInventory(prior.HostFacts, prior.Instance, prior.Also))
		if err != nil {
			prior.PlacementReason = "The shared resource placement could not be verified."
		} else {
			prior.Shared = shared
			// Only a complete collection can establish a new absence. Retained
			// sections and newer records must not create missing claims.
			if facts.Host.Value != nil && facts.Containers.Value != nil && facts.Resources.Value != nil && facts.Images.Value != nil {
				recorded, err := records.Recorded(finish, t)
				if err != nil {
					prior.PlacementReason = "The recorded resources could not be read."
				} else {
					prior.Missing = nil
					for _, item := range MergeInventory(recorded, docker.HostInventory(facts, dk.Instance, also)) {
						if item.Record == deploy.RecordMissing {
							prior.Missing = append(prior.Missing, item)
						}
					}
				}
			}
		}
	}
	// A partial collection cannot certify that every section postdates the act.
	invalidated := row.Invalidated && readErr != nil
	if !invalidated {
		prior.PredatesActAt = time.Time{}
	}
	raw, err := json.Marshal(prior)
	if err != nil {
		return err
	}
	_, err = queries.FinishInventoryReading(finish, deploymentsql.FinishInventoryReadingParams{Subject: t.ID.String(), Kind: HostSnapshotJob, Identity: strconv.FormatInt(t.SettingsRevision, 10), Generation: row.Generation, Payload: raw, Invalidated: invalidated})
	return err
}

// Invalidate preserves facts, including an act before the first collection.
func (s *HostSnapshots) Invalidate(ctx context.Context, t *Target) error {
	q := deploymentsql.New(s.db.SQL())
	_, err := q.StartHostSnapshot(ctx, deploymentsql.StartHostSnapshotParams{ID: ids.New(s.now()), Subject: t.ID.String(), Identity: strconv.FormatInt(t.SettingsRevision, 10)})
	if err != nil {
		return err
	}
	return q.InvalidateHostSnapshot(ctx, deploymentsql.InvalidateHostSnapshotParams{Subject: t.ID.String(), ActedAt: s.now()})
}

// inventory derives the inventory view with the oldest source evidence time.
// An unread inventory section cannot establish any missing resource.
func (s *HostSnapshots) inventory(r hostReading) (inventoryObservation, error) {
	out := inventoryObservation{AttemptedAt: r.AttemptedAt}
	for _, section := range []struct {
		at     time.Time
		reason string
	}{{r.Host.ObservedAt, r.Host.Reason}, {r.Containers.ObservedAt, r.Containers.Reason}, {r.Resources.ObservedAt, r.Resources.Reason}, {r.Images.ObservedAt, r.Images.Reason}} {
		if section.at.IsZero() {
			out.Unknown = true
		}
		// An unrelated failed use read cannot invalidate fresh inventory facts.
		// Acts are stored as PostgreSQL timestamptz (microseconds); JSON
		// section times retain nanoseconds. Equal at that precision is not
		// proof that evidence follows the act. Collection generations, not
		// timestamps, fence overlapping reads in FinishInventoryReading.
		if r.Invalidated && (r.PredatesActAt.IsZero() || !section.at.Truncate(time.Microsecond).After(r.PredatesActAt)) {
			out.Invalidated = true
		}
		if out.ObservedAt.IsZero() || section.at.Before(out.ObservedAt) {
			out.ObservedAt = section.at
		}
		if section.reason != "" {
			out.Reason = "The host inventory could not be fully refreshed."
			out.FailedAt = r.AttemptedAt
		}
	}
	out.Stale = out.Unknown || s.now().Sub(out.ObservedAt) > liveFresh
	out.Unknown = out.Unknown || s.now().Sub(out.ObservedAt) > liveUnknown
	if out.Unknown {
		out.Reason = "Waiting for a complete host inventory reading."
		return out, nil
	}
	out.Items = docker.HostInventory(r.HostFacts, r.Instance, r.Also)
	if r.PlacementReason != "" {
		return out, errors.New(r.PlacementReason)
	}
	out.Shared = r.Shared
	if out.Invalidated {
		return out, nil
	}
	out.Missing = r.Missing
	return out, nil
}

// HostSnapshotWorker resolves exact target revisions before collecting.
type HostSnapshotWorker struct {
	Snapshots *HostSnapshots
	Targets   *Targets
	Hosts     *Hosts
	Providers *Providers
	Inventory *Inventory
}

func (w HostSnapshotWorker) Run(ctx context.Context, key string, _ map[string]any) (map[string]any, error) {
	raw, rev, ok := strings.Cut(key, "/")
	id, err := ids.Parse(raw)
	if err != nil || !ok {
		return nil, jobs.Permanent(errors.New("invalid host snapshot identity"))
	}
	t, err := w.Targets.ByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if t.Status == TargetRetired || strconv.FormatInt(t.SettingsRevision, 10) != rev {
		return nil, nil
	}
	dk, err := w.Hosts.DockerScope(ctx, t)
	if err != nil {
		return nil, err
	}
	refs, err := w.Providers.recordedImages(ctx, t)
	if err != nil {
		return nil, err
	}
	var also []string
	if t.Origin == OriginBuiltIn && w.Providers.databaseContainer != "" {
		also = []string{w.Providers.databaseContainer}
	}
	err = w.Snapshots.collect(ctx, t, dk, docker.HostQuery{Host: true, Containers: true, Resources: true, Images: true, ImageRefs: refs}, also, w.Inventory)
	return map[string]any{"target": t.Name}, err
}
