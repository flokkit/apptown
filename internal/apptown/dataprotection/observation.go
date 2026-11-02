package dataprotection

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/apptown/dataprotection/internal/protectionsql"
	"github.com/flokkit/apptown/internal/database"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/jobs"
	"github.com/flokkit/apptown/internal/ids"
)

// protectionObservationQueue admits work only. Identical unfinished subjects
// coalesce in the durable queue, including while the provider read is running.
type protectionObservationQueue interface {
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
	Protection                         *Protection
	Plan                               []BackupPlanItem
	PlanNote                           string
	LastFailedAt                       time.Time
	LastFailure                        string
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
	// NextStep is the protection owner's safe action for a failed data reading.
	NextStep string
	// AttemptedAt is when the latest protection attempt finished, successful or not.
	AttemptedAt    time.Time
	FailedAt       time.Time
	Unknown, Stale bool
}

// ProtectionObserver calls the real protection owner in a background job. Its
// read path resolves only current local identity and returns recorded evidence.
// Caller authority must be checked before selecting a deployment for Snapshot.
type ProtectionObserver struct {
	db      *database.DB
	queue   protectionObservationQueue
	now     func() time.Time
	resolve func(context.Context, ids.ID) (*deployment.Deployment, string, error)
	read    func(context.Context, ids.ID, string) (*Protection, error)
}

func NewProtectionObserver(service *Service, queue protectionObservationQueue) *ProtectionObserver {
	return &ProtectionObserver{db: service.db, queue: queue, now: service.now, read: service.collectProtection, resolve: service.protectionIdentity}
}

func (s *Service) protectionIdentity(ctx context.Context, id ids.ID) (*deployment.Deployment, string, error) {
	dep, err := s.deployments.ByID(ctx, id)
	if err != nil {
		return nil, "", err
	}
	target, err := s.targets.ByID(ctx, dep.TargetID)
	if err != nil {
		return nil, "", err
	}
	// Bind held-data facts to settled placement and target configuration.
	store, err := s.store(ctx)
	if err != nil {
		return nil, "", err
	}
	raw, err := json.Marshal(struct {
		Deployment     any
		TargetRevision int64
		Store          *BackupStore
	}{dep.ReadingBasis(), target.SettingsRevision, store})
	if err != nil {
		return nil, "", err
	}
	return dep, fmt.Sprintf("%x", sha256.Sum256(raw)), nil
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
	r, err := o.load(ctx, id, identity)
	if err != nil {
		return ProtectionSnapshot{Unknown: true, Stale: true}, err
	}
	return protectionSnapshotAt(r, o.now()), nil
}
func (o *ProtectionObserver) load(ctx context.Context, id ids.ID, identity string) (ProtectionSnapshot, error) {
	row, err := protectionsql.New(o.db.SQL()).Reading(ctx, protectionsql.ReadingParams{Subject: id.String(), Kind: "protection"})
	if errors.Is(err, sql.ErrNoRows) {
		return ProtectionSnapshot{}, nil
	}
	if err != nil {
		return ProtectionSnapshot{}, err
	}
	var out ProtectionSnapshot
	err = json.Unmarshal(row.Payload, &out)
	if row.Identity != identity {
		out = ProtectionSnapshot{ObservedAt: out.ObservedAt, AttemptedAt: out.AttemptedAt}
		out.Unknown = true
		out.Stale = true
		out.Reason = "The deployment changed. Waiting for a protection check."
	}
	return out, err
}
func protectionSnapshotAt(s ProtectionSnapshot, now time.Time) ProtectionSnapshot {
	s.Items = slices.Clone(s.Items)
	s.Stale = s.Stale || s.ObservedAt.IsZero() || now.Sub(s.ObservedAt) > 15*time.Minute
	s.Unknown = s.Unknown || s.ObservedAt.IsZero() || now.Sub(s.ObservedAt) > 6*time.Hour
	for _, item := range s.Items {
		if item.Native {
			s.Stale = s.Stale || item.NativeAt.IsZero() || now.Sub(item.NativeAt) > 15*time.Minute
			s.Unknown = s.Unknown || item.NativeAt.IsZero() || now.Sub(item.NativeAt) > 6*time.Hour
		}
	}
	if s.ObservedAt.IsZero() && s.AttemptedAt.IsZero() {
		s.Reason = "Backups have not been checked yet."
	}
	return s
}

func (o *ProtectionObserver) run(ctx context.Context, id ids.ID, identity string) error {
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
	if errors.Is(ctx.Err(), context.Canceled) {
		return ctx.Err()
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	finish, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer stop()
	prior, loadErr := o.load(finish, id, identity)
	if loadErr != nil {
		return errors.Join(err, loadErr)
	}
	now := o.now()
	prior.AttemptedAt = now
	reason := ""
	prior.NextStep = ""
	if err != nil || protection == nil {
		reason = "The protection check failed."
		if errors.Is(err, context.DeadlineExceeded) {
			reason = "The protection check timed out."
		}
		if err == nil {
			err = errors.New("protection observation missing")
		}
	} else {
		next := observedProtection(protection, now)
		next.Plan, next.PlanNote = protection.plan, protection.planNote
		next.Protection = protection
		if next.Unknown {
			if prior.Protection == nil || protection.Unknown == "" {
				prior.Protection = protection
				prior.Plan = next.Plan
				prior.PlanNote = next.PlanNote
			}
			reason = next.Reason
			prior.NextStep = next.NextStep
			err = errors.New(reason)
			if protection.Unknown != "" {
				err = errors.New(protection.Unknown)
			}
		} else {
			next.LastFailedAt, next.LastFailure = prior.LastFailedAt, prior.LastFailure
			prior = next
		}
	}
	prior.Reason = reason
	if reason != "" {
		prior.FailedAt = now
		prior.LastFailedAt = now
		prior.LastFailure = reason
	} else {
		prior.FailedAt = time.Time{}
	}
	raw, marshalErr := json.Marshal(prior)
	if marshalErr != nil {
		return errors.Join(err, marshalErr)
	}
	storeErr := protectionsql.New(o.db.SQL()).StoreReading(finish, protectionsql.StoreReadingParams{ID: ids.New(now), Subject: id.String(), Kind: "protection", Identity: identity, Payload: raw})
	return errors.Join(err, storeErr)
}

func observedProtection(p *Protection, at time.Time) ProtectionSnapshot {
	out := ProtectionSnapshot{DataClass: p.DataClass, OffHost: p.OffHost, ObservedAt: at, AttemptedAt: at, Unknown: p.Unknown != ""}
	if p.Unknown != "" {
		out.Reason = p.failureReason
		out.NextStep = p.failureNextStep
	}
	for _, limit := range p.Coverage.NotAssessed {
		if limit != outsideNotAssessed {
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
		if item.Copy == copyUnknown {
			out.Unknown = true
			out.Reason = "The latest data copy was interrupted."
		}
		if item.Native != nil {
			row.Native = true
			row.NativeCount = item.Native.Count
			row.NativeAt = item.Native.ObservedAt
			row.NativeUnknown = item.Native.Unknown != "" || item.Native.CheckFailed
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
	identity, act, hasAct := strings.Cut(identity, "/")
	if hasAct {
		if _, err := ids.Parse(act); err != nil {
			return nil, jobs.Permanent(err)
		}
	}
	digest, digestErr := hex.DecodeString(identity)
	if !ok || idErr != nil || id.IsZero() || digestErr != nil || len(digest) != sha256.Size {
		return nil, jobs.Permanent(errors.New("invalid protection observation identity"))
	}
	return nil, o.run(ctx, id, identity)
}

// Request admits a periodic background check for the current local identity.
func (o *ProtectionObserver) Request(ctx context.Context, id ids.ID) error {
	return o.request(ctx, id, false)
}

// AfterAct guarantees a fresh check after an act, even if a check is running.
func (o *ProtectionObserver) AfterAct(ctx context.Context, id ids.ID) error {
	return o.request(ctx, id, true)
}

func (o *ProtectionObserver) request(ctx context.Context, id ids.ID, afterAct bool) error {
	dep, identity, err := o.resolve(ctx, id)
	if err != nil {
		return err
	}
	if dep.Kind != deployment.Standing {
		return nil
	}
	if afterAct {
		identity += "/" + ids.New(o.now()).String()
	}
	return o.queue.RequestProtectionObservation(ctx, id, identity)
}
