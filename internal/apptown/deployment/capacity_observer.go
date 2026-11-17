package deployment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy/docker"

	"github.com/flokkit/apptown/internal/apptown/jobs"
	"github.com/flokkit/apptown/internal/ids"
)

// CapacitySnapshot contains disk evidence and whole-capacity coverage for one
// exact target configuration. Unknown includes unread resource figures. The
// wrapper time never replaces Disk.ReadAt, which may be older than the wrapper.
type CapacitySnapshot struct {
	Disk       *DiskCapacity
	ObservedAt time.Time
	// AttemptedAt distinguishes a pending first check from a failed check.
	AttemptedAt time.Time
	// Reason is a safe summary of the failed or overdue check.
	FailedAt    time.Time
	Reason      string
	LiveUnknown bool
	DiskUnknown bool
	Unknown     bool
	Stale       bool
}

// CapacityObserver owns one persisted reading per target configuration.
type CapacityObserver struct {
	db          *database.DB
	now         func() time.Time
	target      func(context.Context, ids.ID) (*Target, error)
	read        func(context.Context, *Target) (TargetCapacity, error)
	hosts       *Hosts
	fixed       func(context.Context, *Target, docker.HostFacts, string) (TargetCapacity, error)
	readerImage func() string
}

func NewCapacityObserver(service *Service) *CapacityObserver {
	return &CapacityObserver{db: service.db, now: service.now, target: service.Targets.ByID, read: service.targetCapacity, hosts: service.hosts, fixed: service.fixedHostCapacity, readerImage: func() string { return service.env.ToolVersions().Images.DF }}
}

// Snapshot takes the current authorized target record. A changed configuration
// starts unassessed rather than inheriting the previous target's disk warning.

func (o *CapacityObserver) Snapshots(ctx context.Context, targets []*Target) (map[ids.ID]CapacitySnapshot, error) {
	records, err := o.loadMany(ctx, targets)
	out := make(map[ids.ID]CapacitySnapshot, len(records))
	for id, r := range records {
		out[id] = o.capacitySnapshot(r)
	}
	return out, err
}

func (o *CapacityObserver) capacitySnapshot(r capacityRecord) CapacitySnapshot {
	c := capacityAt(r.Capacity, o.now())
	out := CapacitySnapshot{Disk: c.Disk, ObservedAt: c.ReadAt, AttemptedAt: r.AttemptedAt, Reason: r.Reason, Unknown: c.ReadAt.IsZero() || o.now().Sub(c.ReadAt) > 6*time.Hour, Stale: c.Stale, FailedAt: r.FailedAt}
	out.Unknown = out.Unknown || capacityFailure(c) != ""
	out.Stale = out.Stale || capacityHasStaleFigures(c)
	out.LiveUnknown = capacityHasExpiredUse(c)
	if c.Disk != nil {
		out.DiskUnknown = !c.Disk.Free.Known
		out.Stale = out.Stale || c.Disk.StaleAt(o.now())
	}
	return out
}

type capacityRecord struct {
	HostActCounter        int64
	LastFailedAt          time.Time
	LastFailure           string
	Capacity              TargetCapacity
	AttemptedAt, FailedAt time.Time
	Reason                string
}

func (o *CapacityObserver) load(ctx context.Context, t *Target) (capacityRecord, error) {
	records, err := o.loadMany(ctx, []*Target{t})
	return records[t.ID], err
}
func (o *CapacityObserver) loadMany(ctx context.Context, targets []*Target) (map[ids.ID]capacityRecord, error) {
	subjects := make([]string, 0, len(targets))
	bySubject := make(map[string]*Target, len(targets))
	for _, t := range targets {
		subjects = append(subjects, t.ID.String())
		bySubject[t.ID.String()] = t
	}
	rows, err := deploymentsql.New(o.db.SQL()).TargetReadings(ctx, deploymentsql.TargetReadingsParams{Column1: subjects, Kind: "capacity"})
	if err != nil {
		return nil, err
	}
	out := make(map[ids.ID]capacityRecord, len(targets))
	for _, t := range targets {
		out[t.ID] = capacityRecord{}
	}
	for _, row := range rows {
		t := bySubject[row.Subject]
		if row.Identity != fmt.Sprint(t.SettingsRevision) {
			continue
		}
		var r capacityRecord
		if err := json.Unmarshal(row.Payload, &r); err != nil {
			return nil, err
		}
		out[t.ID] = r
	}
	return out, nil
}

// Run publishes only evidence for the target configuration it checked.
func (o *CapacityObserver) Run(ctx context.Context, id ids.ID, revision int64) error {
	target, err := o.target(ctx, id)
	if err != nil {
		return err
	}
	if target.SettingsRevision != revision {
		return nil
	}
	if o.hosts != nil {
		var release func()
		ctx, release, err = o.hosts.HoldCallSlot(ctx, target)
		if err != nil {
			return err
		}
		defer release()
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	reading, checkErr := o.read(ctx, target)
	if errors.Is(ctx.Err(), context.Canceled) {
		return ctx.Err()
	}
	// Finishing a timed-out check still records its failure, within a bounded write.
	finish, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer stop()
	current, err := o.target(finish, id)
	if err != nil {
		return err
	}
	if current.SettingsRevision != revision {
		return nil
	}
	if ctx.Err() != nil {
		checkErr = ctx.Err()
	}
	return o.publish(finish, target, reading, checkErr, 0)
}

const capacityInterval = 5 * time.Minute

// publish shares the capacity merge and persistence for both collectors.
func (o *CapacityObserver) publish(ctx context.Context, target *Target, reading TargetCapacity, checkErr error, actCounter int64) error {
	prior, err := o.load(ctx, target)
	if err != nil {
		return err
	}
	now := o.now()
	prior.AttemptedAt = now
	prior.HostActCounter = actCounter
	reason := ""
	if checkErr != nil {
		reason = "The host capacity check failed."
	} else {
		reason = capacityFailure(reading)
		// The first failure found is the cause; an unread disk is often only
		// its consequence (a host that did not answer).
		if reason == "" && DescribeTarget(target).ContainerMemoryLimits && (reading.Disk == nil || !reading.Disk.Free.Known || !reading.Disk.Total.Known) {
			disk := DiskCapacity{Free: unknown("the reading has no disk")}
			if reading.Disk != nil {
				disk = *reading.Disk
			}
			reason = disk.Unread()
		}
		merged := mergeCapacity(prior.Capacity, reading, now)
		// A partly read host keeps moving: its time is the newest figure
		// actually read, so one figure that can never be read does not
		// freeze the rest. An offline host keeps its last good time.
		if reason != "" {
			merged.ReadAt = prior.Capacity.ReadAt
			if at := successfulCapacityTime(merged); at.After(merged.ReadAt) {
				merged.ReadAt = at
			}
		}
		prior.Capacity = merged
	}
	prior.Reason = reason
	if reason != "" {
		prior.FailedAt = now
		prior.LastFailedAt = now
		prior.LastFailure = reason
	} else {
		prior.FailedAt = time.Time{}
	}
	raw, err := json.Marshal(prior)
	if err != nil {
		return err
	}
	err = deploymentsql.New(o.db.SQL()).StoreReading(ctx, deploymentsql.StoreReadingParams{ID: ids.New(now), Subject: target.ID.String(), Kind: "capacity", Identity: fmt.Sprint(target.SettingsRevision), Payload: raw})
	return errors.Join(checkErr, err)
}

func (s *Service) recordedCapacity(ctx context.Context, t *Target) (TargetCapacity, error) {
	o := NewCapacityObserver(s)
	r, err := o.load(ctx, t)
	if err != nil {
		return TargetCapacity{}, err
	}
	if r.Capacity.ReadAt.IsZero() {
		return TargetCapacity{Target: t.Name, Kind: t.Kind, FixedHost: fixedHostKind(t.Kind), Elastic: elasticKind(t.Kind), Words: "Pending first check", Stale: true, Error: r.Reason}, nil
	}
	out := capacityAt(r.Capacity, s.now())
	out.Error = r.Reason
	out.FixedHost = fixedHostKind(t.Kind)
	return out, nil
}

// Optional figures remain absent when the provider does not offer them. A
// present but unread figure is incomplete coverage, even with a healthy disk.
func capacityFailure(reading TargetCapacity) string {
	if reading.Error != "" {
		return reading.Error
	}
	for _, resource := range []struct {
		name    string
		reading *ResourceCapacity
	}{{"memory", reading.Memory}, {"CPU", reading.CPU}} {
		r := resource.reading
		if r == nil {
			continue
		}
		if !r.Total.Known {
			return "The host " + resource.name + " total could not be read."
		}
		if !r.Promised.Sum.AppTown.Known || !r.Promised.Sum.Other.Known || r.Promised.Unread.AppTown > 0 || r.Promised.Unread.Other > 0 {
			return "Container " + resource.name + " limits could not all be read."
		}
		if r.InUse != nil && (!r.InUse.AppTown.Known || !r.InUse.Other.Known) {
			return "Container " + resource.name + " use could not all be read."
		}
		if r.HostUsed != nil && !r.HostUsed.Known {
			return "The host's own " + resource.name + " use could not be read."
		}
	}
	for _, t := range reading.Tenants {
		if !t.Used.Known {
			return "Container memory use could not all be read."
		}
		if !t.Promised.Known {
			return "Container memory limits could not all be read."
		}
		if t.CPUUnread > 0 {
			return "Container CPU limits could not all be read."
		}
	}
	return ""
}

const CapacityObservationJob = "capacity-observation"

// RunJob validates the durable subject before allowing any provider read.
func (o *CapacityObserver) RunJob(ctx context.Context, key string, _ map[string]any) (map[string]any, error) {
	rawID, rawRevision, ok := strings.Cut(key, "/")
	id, idErr := ids.Parse(rawID)
	rawRevision, act, hasAct := strings.Cut(rawRevision, "/")
	if hasAct {
		if actID, err := ids.Parse(act); err != nil || actID.IsZero() {
			return nil, jobs.Permanent(errors.New("invalid capacity observation act"))
		}
	}
	revision, revisionErr := strconv.ParseInt(rawRevision, 10, 64)
	if !ok || idErr != nil || id.IsZero() || revisionErr != nil || revision < 0 {
		return nil, jobs.Permanent(errors.New("invalid capacity observation identity"))
	}
	return nil, o.Run(ctx, id, revision)
}
