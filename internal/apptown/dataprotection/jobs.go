package dataprotection

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/jobs"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

// JobWorker adapts durable backup, sweep and restored-copy sweep requests to
// data protection's operations. It owns payload decoding and receipt/error
// mapping; River owns delivery and retry. The job names are unchanged, so
// queued work needs no migration.
type JobWorker struct {
	Service  *Service
	Projects *project.Projects
	Backups  interface {
		RequestBackup(context.Context, string, string) error
	}
}

// RestoredCopySweepJob takes down restored copies whose time is up, and
// those a start left standing, every few minutes.
const RestoredCopySweepJob = "restored-copy-sweep"

// Backup captures one deployment's state through TakeBackup. A backup
// that failed is a receipt, never retried blind (one attempt).
func (w JobWorker) Backup(ctx context.Context, key string, args map[string]any) (map[string]any, error) {
	owner, name, ok := strings.Cut(key, "/")
	projectID, err := ids.Parse(owner)
	if !ok || err != nil {
		return nil, jobs.Permanent(fmt.Errorf("a backup key is project-id/deployment, got %q", key))
	}
	p, err := w.Projects.ByID(ctx, projectID)
	if err != nil {
		// A project that is gone stays gone; a read that failed may not.
		if fault.KindOf(err) == fault.NotFound {
			return nil, jobs.Permanent(err)
		}
		return nil, err
	}
	trigger, _ := args["trigger"].(string)
	switch trigger {
	case "":
		trigger = "scheduled"
	case "scheduled", "release":
	default:
		return nil, jobs.Permanent(fmt.Errorf("backup job trigger %q is not scheduled or release", trigger))
	}
	b, err := w.Service.takeBackup(ctx, p.ID, name, trigger, "apptown-system")
	if err != nil {
		if errors.Is(err, ErrNothingToBackUp) || errors.Is(err, deployment.ErrTargetKeepsBackups) || errors.Is(err, project.ErrNotActive) {
			return map[string]any{"skipped": err.Error()}, nil
		}
		return nil, err
	}
	if b.Status != BackupOK {
		return nil, fmt.Errorf("%s: %s", b.Status, b.Error)
	}
	return map[string]any{"backup": b.ID, "bytes": b.Bytes, "items": len(b.Items)}, nil
}

// backup-sweep: ask for a backup of every standing deployment on a Docker
// host or in a Portainer environment; each is its own job with its own
// receipt.
func (w JobWorker) Sweep(ctx context.Context, _ string, _ map[string]any) (map[string]any, error) {
	asked := 0
	for _, e := range w.Service.hosts.Watched(ctx, w.Service.deployments) {
		if e.Container == "" {
			continue
		}
		if err := w.Backups.RequestBackup(ctx, e.ProjectID.String()+"/"+e.Name, "scheduled"); err != nil {
			return nil, err
		}
		asked++
	}
	standing, err := w.Service.deployments.StandingWithoutOpenAttempt(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading the standing deployments: %w", err)
	}
	for _, e := range standing {
		t, err := w.Service.targets.ByID(ctx, e.TargetID)
		if err != nil || t.DataAccess() != deployment.DataThroughPortainer {
			continue
		}
		if err := w.Backups.RequestBackup(ctx, e.ProjectID.String()+"/"+e.Name, "scheduled"); err != nil {
			return nil, err
		}
		asked++
	}
	// Production data on a cloud target is copied to the backup store by
	// operations of its own.
	copies, skipped, err := w.Service.ScheduledCopies(ctx)
	if err != nil {
		return nil, fmt.Errorf("starting the daily copies: %w", err)
	}
	// With a store attached every set is kept there: sets still on the
	// AppTown's disk move, and what a stopped take left staged goes.
	// Neither fails the sweep, whose copies are already started: what did
	// not happen is on its receipt, and the next day's sweep tries again.
	moved, notMoved, err := w.Service.MoveSetsToStore(ctx)
	if err != nil {
		notMoved = append(notMoved, "the move stopped: "+err.Error())
	}
	staged, err := w.Service.ClearStaging()
	if err != nil {
		notMoved = append(notMoved, "clearing staged backups: "+err.Error())
	}
	// What an interrupted rehearsal left is removed: production data does
	// not stay on a rehearsal host.
	cleared, left, err := w.Service.SweepRehearsals(ctx)
	if err != nil {
		return nil, fmt.Errorf("removing what ended rehearsals left: %w", err)
	}
	return map[string]any{"asked": asked, "copies": copies, "copiesSkipped": skipped,
		"rehearsalsCleared": cleared, "rehearsalsLeft": left, "movedToStore": moved, "notMoved": notMoved, "stagingCleared": staged}, nil
}

// RestoredCopySweep starts a take-down of each restored copy that is due
// one (SweepRestoredCopies).
func (w JobWorker) RestoredCopySweep(ctx context.Context, _ string, _ map[string]any) (map[string]any, error) {
	started, skipped, err := w.Service.SweepRestoredCopies(ctx)
	if err != nil {
		return nil, fmt.Errorf("taking down restored copies: %w", err)
	}
	return map[string]any{"takeDowns": started, "skipped": skipped}, nil
}
