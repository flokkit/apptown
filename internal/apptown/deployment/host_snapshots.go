package deployment

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"strconv"
	"time"

	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/ids"
)

const hostReadingKind = "host-snapshot"

// hostReading retains successful section evidence across partial attempts.
// Invalidated means the evidence predates an act; deriving a view never renews it.
type hostReading struct {
	docker.HostFacts
	ContainerFailure string
	HostFailures     int
	UnreachableSince time.Time
	UnreachableError string
	HostDown         bool
	NextAt           time.Time
	AttemptedAt      time.Time
	Outcome          string
	LastError        string
	Took             time.Duration
	Invalidated      bool
	Instance         string
	Also             []string
	Shared           []SharedResource
	Missing          []deploy.Resource
	PlacementReason  string
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
	row, err := deploymentsql.New(s.db.SQL()).Reading(ctx, deploymentsql.ReadingParams{Subject: t.ID.String(), Kind: hostReadingKind})
	if errors.Is(err, sql.ErrNoRows) || err == nil && row.Identity != strconv.FormatInt(t.SettingsRevision, 10) {
		return hostReading{}, nil
	}
	if err != nil {
		return hostReading{}, err
	}
	var r hostReading
	err = json.Unmarshal(row.Payload, &r)
	r.Invalidated = row.CompleteActCounter < row.ActCounter
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

// collect captures act provenance before any host command. A
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
	if facts.Host.Reason != "" {
		if prior.HostFailures == 0 && !prior.HostDown {
			prior.UnreachableSince = started
		}
		prior.HostFailures++
		prior.UnreachableError = facts.Host.Reason
		if prior.HostFailures >= 2 {
			prior.HostDown = true
		}
	} else {
		prior.HostFailures = 0
	}
	if readErr == nil {
		prior.HostDown = false
		prior.UnreachableSince = time.Time{}
		prior.UnreachableError = ""
	}
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
	prior.ContainerFailure = ""
	if facts.Containers.Reason != "" {
		prior.ContainerFailure = safeReason(readErr)
	}
	prior.NextAt = s.now().Add(hostReadInterval)
	prior.LastError = ""
	prior.Outcome = "read"
	if readErr != nil {
		prior.LastError = readErr.Error()
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
	raw, err := json.Marshal(prior)
	if err != nil {
		return err
	}
	_, err = queries.FinishHostSnapshot(finish, deploymentsql.FinishHostSnapshotParams{Subject: t.ID.String(), Identity: strconv.FormatInt(t.SettingsRevision, 10), ActCounter: row, Payload: raw, Complete: readErr == nil})
	return err
}

// BeforeAct records that a Docker host may change, before the effect starts.
// Failure prevents the caller's effect. Foreground reads never store evidence.
// The caller defers done, which marks the host again when the effect ends: a
// complete lap that began during the effect saw it half done, and must not
// leave the reading current after it. A failed end mark is logged; the
// effect has happened and its outcome stands.
func (s *HostSnapshots) BeforeAct(ctx context.Context, targetID ids.ID) (done func(), err error) {
	t, err := NewTargets(s.db, nil).ByID(ctx, targetID)
	if err != nil {
		return nil, err
	}
	mark := func(ctx context.Context) error {
		return deploymentsql.New(s.db.SQL()).BeforeHostAct(ctx, deploymentsql.BeforeHostActParams{ID: ids.New(s.now()), Subject: t.ID.String(), Identity: strconv.FormatInt(t.SettingsRevision, 10)})
	}
	if err := mark(ctx); err != nil {
		return nil, err
	}
	return func() {
		if err := mark(context.WithoutCancel(ctx)); err != nil {
			log.Printf("host %s: marking the end of an act: %v", t.Name, err)
		}
	}, nil
}

// inventory derives the inventory view with the oldest source evidence time.
// An unread inventory section cannot establish any missing resource.
func (s *HostSnapshots) inventory(r hostReading) (inventoryObservation, error) {
	out := inventoryObservation{LastFailure: r.LastError, AttemptedAt: r.AttemptedAt, Invalidated: r.Invalidated, TookSeconds: r.Took.Seconds()}
	for i, section := range []struct {
		at     time.Time
		reason string
	}{{r.Host.ObservedAt, r.Host.Reason}, {r.Containers.ObservedAt, r.Containers.Reason}, {r.Resources.ObservedAt, r.Resources.Reason}, {r.Images.ObservedAt, r.Images.Reason}} {
		if section.at.IsZero() {
			out.Unknown = true
		}
		if i == 0 || section.at.Before(out.ObservedAt) {
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
	// Missing claims were witnessed by a complete collection; under a later
	// act they stay, marked invalidated, until a reading after it.
	out.Shared, out.Missing = r.Shared, r.Missing
	return out, nil
}

// HostReachability describes the loop's confirmed host outage. Only a complete
// lap clears an outage; a successful info command alone cannot clear it.
type HostReachability struct {
	Down  bool
	Since time.Time
	Error string
	// Deployments counts the standing deployments on the host.
	Deployments int
}

// HostReachability reads local evidence only. Callers authorize the target or
// its deployment before reading; no connection check is performed here.
// A target without a stored host reading has no confirmed outage.
func (s *Service) HostReachability(ctx context.Context, targetID ids.ID) (HostReachability, error) {
	t, err := s.Targets.ByID(ctx, targetID)
	if err != nil {
		return HostReachability{}, err
	}
	r, err := NewHostSnapshots(s.db, s.now).reading(ctx, t)
	if err != nil || !r.HostDown {
		return HostReachability{}, err
	}
	deps, err := s.Deployments.OnTarget(ctx, targetID)
	// Count standing deployments, the ones the target page lists; previews
	// and restored copies belong to a change or a restore, not to the host.
	standing := 0
	for _, d := range deps {
		if d.Standing() {
			standing++
		}
	}
	return HostReachability{Down: r.HostDown, Since: r.UnreachableSince, Error: r.UnreachableError, Deployments: standing}, err
}
