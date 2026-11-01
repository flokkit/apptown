package deployment

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/database"
	"strconv"
	"strings"
	"time"

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
	db     *database.DB
	now    func() time.Time
	target func(context.Context, ids.ID) (*Target, error)
	read   func(context.Context, *Target) (TargetCapacity, error)
}

func NewCapacityObserver(service *Service) *CapacityObserver {
	return &CapacityObserver{db: service.db, now: service.now, target: service.Targets.ByID, read: service.targetCapacity}
}

// Snapshot takes the current authorized target record. A changed configuration
// starts unassessed rather than inheriting the previous target's disk warning.
func (o *CapacityObserver) Snapshot(ctx context.Context, target *Target) (CapacitySnapshot, error) {
	r, err := o.load(ctx, target)
	if err != nil {
		return CapacitySnapshot{Unknown: true, Stale: true}, err
	}
	c := capacityAt(r.Capacity, o.now())
	out := CapacitySnapshot{Disk: c.Disk, ObservedAt: c.ReadAt, AttemptedAt: r.AttemptedAt, Reason: r.Reason, Unknown: c.ReadAt.IsZero() || o.now().Sub(c.ReadAt) > 6*time.Hour, Stale: c.Stale, FailedAt: r.FailedAt}
	out.Unknown = out.Unknown || capacityFailure(c) != ""
	out.Stale = out.Stale || capacityHasStaleFigures(c)
	out.LiveUnknown = capacityHasExpiredUse(c)
	if c.Disk != nil {
		out.DiskUnknown = !c.Disk.Free.Known
		out.Stale = out.Stale || c.Disk.StaleAt(o.now())
	}
	return out, nil
}

type capacityRecord struct {
	LastFailedAt          time.Time
	LastFailure           string
	Capacity              TargetCapacity
	AttemptedAt, FailedAt time.Time
	Reason                string
}

func (o *CapacityObserver) load(ctx context.Context, t *Target) (capacityRecord, error) {
	row, err := deploymentsql.New(o.db.SQL()).Reading(ctx, deploymentsql.ReadingParams{Subject: t.ID.String(), Kind: "capacity"})
	if errors.Is(err, sql.ErrNoRows) {
		return capacityRecord{}, nil
	}
	if err != nil {
		return capacityRecord{}, err
	}
	if row.Identity != fmt.Sprint(t.SettingsRevision) {
		return capacityRecord{}, nil
	}
	var r capacityRecord
	err = json.Unmarshal(row.Payload, &r)
	return r, err
}

// Run publishes only evidence for the target configuration it checked.
func (o *CapacityObserver) Run(ctx context.Context, id ids.ID, revision int64) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	target, err := o.target(ctx, id)
	if err != nil {
		return err
	}
	if target.SettingsRevision != revision {
		return nil
	}
	reading, checkErr := o.read(ctx, target)
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
	prior, err := o.load(finish, target)
	if err != nil {
		return err
	}
	now := o.now()
	prior.AttemptedAt = now
	reason := ""
	if checkErr != nil {
		reason = "The host capacity check failed."
	} else {
		reason = capacityFailure(reading)
		if DescribeTarget(target).ContainerMemoryLimits && (reading.Disk == nil || !reading.Disk.Free.Known || !reading.Disk.Total.Known) {
			reason = "The host disk check failed."
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
	err = deploymentsql.New(o.db.SQL()).StoreReading(finish, deploymentsql.StoreReadingParams{ID: ids.New(now), Subject: id.String(), Kind: "capacity", Identity: fmt.Sprint(revision), Payload: raw})
	return errors.Join(checkErr, err)
}

func (s *Service) recordedCapacity(ctx context.Context, t *Target) (TargetCapacity, error) {
	o := NewCapacityObserver(s)
	r, err := o.load(ctx, t)
	if err != nil {
		return TargetCapacity{}, err
	}
	if r.Capacity.ReadAt.IsZero() {
		return TargetCapacity{Target: t.Name, Kind: t.Kind, Words: "Pending first check", Stale: true, Error: r.Reason}, nil
	}
	out := capacityAt(r.Capacity, s.now())
	out.Error = r.Reason
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
	revision, revisionErr := strconv.ParseInt(rawRevision, 10, 64)
	if !ok || idErr != nil || id.IsZero() || revisionErr != nil || revision < 0 {
		return nil, jobs.Permanent(errors.New("invalid capacity observation identity"))
	}
	return nil, o.Run(ctx, id, revision)
}
