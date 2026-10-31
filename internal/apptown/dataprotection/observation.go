package dataprotection

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/jobs"
	"github.com/flokkit/apptown/internal/ids"
)

// ProtectionObservationQueue admits work only. Identical unfinished subjects
// coalesce in the durable queue, including while the provider read is running.
type ProtectionObservationQueue interface {
	RequestProtectionObservation(context.Context, ids.ID, string) error
}

// ProtectionItemObservation keeps owner classifications and evidence times,
// without raw provider errors, volume paths or recovery credentials.
type ProtectionItemObservation struct {
	Key, Need               string
	Copy                    CopyState
	CopyAt                  time.Time
	Native                  bool
	NativeCount             int
	NativeAt                time.Time
	NativeUnknown           bool
	DrillKnown, DrillPassed bool
	DrillAt                 time.Time
}

type ProtectionSnapshot struct {
	DataClass                          string
	OffHost                            bool
	Items                              []ProtectionItemObservation
	RestoreSupported, RestoreAvailable bool
	Rehearsed, RehearsalPassed         bool
	RehearsalAt                        time.Time
	RehearsalOutcome                   string
	RestoreTested                      bool
	RestoreOutcome                     string
	RestoreAt                          time.Time
	Unassessed                         bool
	ObservedAt                         time.Time
	// Reason is a safe summary of the failed or overdue check.
	Reason string
	// AttemptedAt is when the latest protection attempt finished, successful or not.
	AttemptedAt    time.Time
	Unknown, Stale bool
}

type protectionObservation struct {
	identity string
	snapshot ProtectionSnapshot
}

// ProtectionObserver calls the real protection owner in a background job. Its
// read path resolves only current local identity and returns recorded evidence.
// Caller authority must be checked before selecting a deployment for Snapshot.
type ProtectionObserver struct {
	mu      sync.Mutex
	entries map[ids.ID]*protectionObservation
	queue   ProtectionObservationQueue
	now     func() time.Time
	resolve func(context.Context, ids.ID) (*deployment.Deployment, string, error)
	read    func(context.Context, ids.ID, string) (*Protection, error)
}

func NewProtectionObserver(service *Service, queue ProtectionObservationQueue) *ProtectionObserver {
	return &ProtectionObserver{queue: queue, now: service.now, read: service.DeploymentProtection, resolve: func(ctx context.Context, id ids.ID) (*deployment.Deployment, string, error) {
		dep, err := service.deployments.ByID(ctx, id)
		if err != nil {
			return nil, "", err
		}
		target, err := service.targets.ByID(ctx, dep.TargetID)
		if err != nil {
			return nil, "", err
		}
		// Include placement, both generations, held-data interpretation, restore
		// state and target configuration. A local read never supplies held-data facts.
		raw, err := json.Marshal(struct {
			Deployment     *deployment.Deployment
			TargetRevision int64
		}{dep, target.SettingsRevision})
		if err != nil {
			return nil, "", err
		}
		return dep, fmt.Sprintf("%x", sha256.Sum256(raw)), nil
	}}
}

func (o *ProtectionObserver) Snapshot(ctx context.Context, id ids.ID) (ProtectionSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return ProtectionSnapshot{Unknown: true, Stale: true}, err
	}
	dep, identity, err := o.resolve(ctx, id)
	if err != nil {
		return ProtectionSnapshot{Unknown: true, Stale: true}, err
	}
	if dep.Kind != deployment.Standing {
		return ProtectionSnapshot{Unknown: true}, errors.New("protection observations require a standing deployment")
	}
	now := o.now()
	o.mu.Lock()
	if o.entries == nil {
		o.entries = make(map[ids.ID]*protectionObservation)
	}
	e := o.entries[id]
	if e == nil || e.identity != identity {
		e = &protectionObservation{identity: identity, snapshot: ProtectionSnapshot{Unknown: true}}
		o.entries[id] = e
	}
	out := protectionSnapshotAt(e.snapshot, now)
	o.mu.Unlock()
	return out, nil
}

func protectionSnapshotAt(s ProtectionSnapshot, now time.Time) ProtectionSnapshot {
	s.Items = slices.Clone(s.Items)
	s.Stale = s.Stale || s.ObservedAt.IsZero() || now.Sub(s.ObservedAt) > 12*time.Minute
	// A newly collected wrapper cannot renew the provider's own cached read.
	for _, item := range s.Items {
		if item.Native && (item.NativeAt.IsZero() || now.Sub(item.NativeAt) > 12*time.Minute) {
			s.Stale = true
		}
	}
	if s.Stale {
		s.Unknown = true
		if s.ObservedAt.IsZero() && s.AttemptedAt.IsZero() {
			s.Reason = "Waiting for the first protection check."
		} else if s.Reason == "" {
			s.Reason = "The protection check is overdue."
		}
	}
	return s
}

func (o *ProtectionObserver) Run(ctx context.Context, id ids.ID, identity string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	dep, current, err := o.resolve(ctx, id)
	if err == nil && (current != identity || dep.Kind != deployment.Standing) {
		return nil
	}
	var protection *Protection
	if err == nil {
		protection, err = o.read(ctx, dep.ProjectID, dep.Name)
	}
	if err == nil {
		_, current, err = o.resolve(ctx, id)
		if err == nil && current != identity {
			return nil
		}
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	now := o.now()
	o.mu.Lock()
	defer o.mu.Unlock()
	e := o.entries[id]
	if o.entries == nil {
		o.entries = make(map[ids.ID]*protectionObservation)
	}
	if e == nil || e.identity != identity {
		e = &protectionObservation{identity: identity}
		o.entries[id] = e
	}
	e.snapshot.AttemptedAt = now
	if err != nil || protection == nil {
		e.snapshot.Unknown = true
		e.snapshot.Reason = "The protection check failed."
		if errors.Is(err, context.DeadlineExceeded) {
			e.snapshot.Reason = "The protection check timed out."
		}
		if err == nil {
			err = errors.New("protection observation missing")
		}
		return err
	}
	next := observedProtection(protection, now)
	// The detail view returns held-data failures as an unassessed answer.
	// Background collection must also report that failure to its job, so the
	// completed attempt does not erase the diagnostic needed to repair it.
	var checkErr error
	if protection.Unknown != "" {
		checkErr = errors.New(protection.Unknown)
	}
	if next.Unknown && !e.snapshot.ObservedAt.IsZero() {
		// Unread held data or provider evidence cannot clear old conditions.
		e.snapshot.Unknown, e.snapshot.Stale = true, true
		e.snapshot.Reason = next.Reason
		return checkErr
	}
	e.snapshot = next
	return checkErr
}

func observedProtection(p *Protection, at time.Time) ProtectionSnapshot {
	out := ProtectionSnapshot{DataClass: p.DataClass, OffHost: p.OffHost, ObservedAt: at, AttemptedAt: at, Unknown: p.Unknown != ""}
	if p.Unknown != "" {
		out.Reason = "The deployment's data could not be read."
	}
	for _, limit := range p.Coverage.NotAssessed {
		if limit != OutsideNotAssessed {
			out.Unassessed = true
			out.Unknown = true
			if out.Reason == "" {
				out.Reason = "Protection could not be assessed for some declared data."
			}
		}
	}
	if p.Operations != nil {
		out.RestoreSupported = true
		out.RestoreAvailable = p.Operations.Restore.Available
	}
	if p.Rehearsal != nil {
		out.Rehearsed = true
		out.RehearsalPassed = p.Rehearsal.Passed
		out.RehearsalOutcome = p.Rehearsal.Outcome
		out.RehearsalAt = p.Rehearsal.At.Time
	}
	if p.RestoreTest != nil {
		out.RestoreTested = true
		out.RestoreOutcome = p.RestoreTest.Outcome
		out.RestoreAt = p.RestoreTest.CheckedAt.Time
	}
	for _, item := range p.Items {
		row := ProtectionItemObservation{Key: fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%q/%q/%q", item.Need, item.Volume, item.Where)))), Need: item.Need, Copy: item.Copy}
		if item.LastCopy != nil {
			row.CopyAt = mustTime(item.LastCopy.TakenAt)
		}
		if item.Copy == CopyUnknown {
			out.Unknown = true
			out.Reason = "The latest data copy was interrupted."
		}
		if item.Native != nil {
			row.Native = true
			row.NativeCount = item.Native.Count
			row.NativeAt = item.Native.observedAt
			row.NativeUnknown = item.Native.Unknown != ""
			out.Unknown = out.Unknown || row.NativeUnknown
			if row.NativeUnknown {
				out.Reason = "The provider backup check failed."
			}
		}
		if item.Drill != nil {
			row.DrillKnown = true
			row.DrillPassed = item.Drill.Passed
			row.DrillAt = item.Drill.At.Time
		}
		out.Items = append(out.Items, row)
	}
	return out
}

const ProtectionObservationJob = "protection-observation"

func (o *ProtectionObserver) RunJob(ctx context.Context, key string, _ map[string]any) (map[string]any, error) {
	rawID, identity, ok := strings.Cut(key, "/")
	id, idErr := ids.Parse(rawID)
	digest, digestErr := hex.DecodeString(identity)
	if !ok || idErr != nil || id.IsZero() || digestErr != nil || len(digest) != sha256.Size {
		return nil, jobs.Permanent(errors.New("invalid protection observation identity"))
	}
	return nil, o.Run(ctx, id, identity)
}

// Request admits a periodic background check for the current local identity.
func (o *ProtectionObserver) Request(ctx context.Context, id ids.ID) error {
	dep, identity, err := o.resolve(ctx, id)
	if err != nil {
		return err
	}
	if dep.Kind != deployment.Standing {
		return nil
	}
	return o.queue.RequestProtectionObservation(ctx, id, identity)
}
