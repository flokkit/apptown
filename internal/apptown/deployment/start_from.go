package deployment

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/apptown/deployment/internal/releasesql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/fault"
)

// Starting a deployment from a copy (design/subsystems/deployment.md,
// "Protecting data off-host"): a new deployment names a set in the backup
// store, and its first release, once the provider has made its database and
// before the app starts, fills that database from the copy and checks it.
// The deployment's record names the set it started from. Putting data into
// a deployment is a restore: it needs deployment:restore in the set's
// project and in the deployment's, checked when the start is chosen and
// again when the release that fills it is asked for; no agent carries it. A
// production copy goes only into a production deployment.
//
// Deployment owns the choice (chooseStartFrom's guards and write), the
// match of a release to the set it was admitted to fill, and the record of
// the start (RecordStartedFrom). The set, its authority rule and the fill
// itself are data protection's, reached through CopyStarter.

var (
	ErrStartFrom = fault.New(fault.Conflict, "this deployment cannot start from that copy")
	// ErrProductionCopy: a production copy keeps its class wherever it goes.
	ErrProductionCopy = fault.New(fault.Forbidden, "a copy of production data goes only into a deployment that holds production data")
	// errNoCopyStarter refuses a start from a copy on an AppTown built
	// without data protection: never an empty deployment or a fill taken
	// as done.
	errNoCopyStarter = errors.New("starting a deployment from a copy is not configured")
)

// copyLauncher is data protection's side of starting a deployment from a
// copy: whether the caller may start a deployment of destinationProject
// holding dataClass from a set (the set must be a whole store copy a new
// managed database can be filled from; deployment:restore in the set's
// project and the destination's; production only into production), which
// of the set's items were not shown to come back by their last check of it
// (UncheckedStart, "" when none), and the fill itself.
type copyLauncher interface {
	CheckStart(ctx context.Context, setID ids.ID, destinationProject ids.ID, dataClass string) error
	UncheckedStart(ctx context.Context, setID ids.ID) (string, error)
	FillStart(ctx context.Context, in StartFill) (StartReceipt, error)
}

// StartFill is a fill of a deployment that has never run from the set it
// starts from: its placement and target, where the fill's steps are logged, and Safe for a restored copy (a failure is
// said by the kind's exit status, never by what a command printed). Allowed,
// when set, is asked before each database is made new and again before
// production data goes into it.
type StartFill struct {
	Deployment *Deployment
	Target     *Target
	Logf       deploy.Logf
	Safe       bool
	Allowed    func(context.Context) error
}

// StartReceipt is what a fill did: the set, the deployment and project it
// was taken of, and each copy it read (need, instance, database, digest,
// size) and the database it went into.
type StartReceipt struct {
	Set            ids.ID
	FromDeployment string
	FromProject    ids.ID
	From, Into     []map[string]any
}

// RefusesStartFrom says why a deployment on t cannot start from a copy, nil
// when it can: the copy fills a database the target makes, on its copy
// runner, so it is refused when chosen, not when the first release gets
// there.
func RefusesStartFrom(t *Target) error {
	if t.Kind != KindDOApp || t.NeedsMode != "managed" {
		return fmt.Errorf("%w: starting from a copy fills a database the target makes; %s does not make one", ErrStartFrom, t.Name)
	}
	if t.CopyRunner() == "" {
		return ErrNoCopyRunner
	}
	return nil
}

// checkStart asks data protection whether the caller may start a
// deployment of projectID holding dataClass from set.
func (s *Service) checkStart(ctx context.Context, set, projectID ids.ID, dataClass string) error {
	if s.starter == nil {
		return errNoCopyStarter
	}
	return s.starter.CheckStart(ctx, set, projectID, dataClass)
}

// uncheckedStart refuses the first release of dep, which starts from a
// copy, when an item of the copy was not shown to come back by its last
// check of it, unless the person accepted that: then note is the words for
// the release's first log line, as releasing over uncopied data has.
func (s *Service) uncheckedStart(ctx context.Context, dep *Deployment, accepted bool, actor string) (note string, err error) {
	if s.starter == nil {
		return "", errNoCopyStarter
	}
	why, err := s.starter.UncheckedStart(ctx, dep.StartFrom)
	if err != nil || why == "" {
		return "", err
	}
	if !accepted {
		return "", fmt.Errorf("%w: %s; release accepting an unchecked copy (acceptUncheckedCopy, apptown release --accept-unchecked-copy)", ErrStartFrom, why)
	}
	return fmt.Sprintf("; released by %s accepting a copy not shown to come back whole: %s", actor, why), nil
}

// chooseStartFrom records that a deployment just bound starts from a set,
// inside the binding's transaction. Only a deployment that has never run
// starts from a copy, only on a target that makes its data, and only one
// fed by a person's deploy.
func (s *Service) chooseStartFrom(ctx context.Context, tx *database.Tx, dep *Deployment, t *Target, setID ids.ID, who string) error {
	if err := RefusesStartFrom(t); err != nil {
		return err
	}
	if dep.Feed == "landing" {
		return fmt.Errorf("%w: a deployment fed by every landing is deployed by no person; start a promotion-fed deployment from a copy", ErrStartFrom)
	}
	// A release already in flight was authorised without this copy; the
	// start is chosen before it, or after it ends.
	busy, err := releasesql.New(tx.SQL()).ReleaseInFlight(ctx, releasesql.ReleaseInFlightParams{ProjectID: dep.ProjectID, DeploymentID: dep.ID, OpenState: Releasing.String()})
	if err != nil {
		return err
	}
	if busy {
		return fmt.Errorf("%w: a release of %s is in flight; choose the start once it has ended", ErrStartFrom, dep.Name)
	}
	n, err := deploymentsql.New(tx.SQL()).SetStartFrom(ctx, deploymentsql.SetStartFromParams{BackupID: setID, ID: dep.ID})
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("%w: %s has run, or has already started from a copy; a copy is filled only into a deployment that has never run", ErrStartFrom, dep.Name)
	}
	dep.StartFrom = setID
	return nil
}

// startFromCopy fills the deployment's managed databases from the set it
// starts from, after the provider has made them and before the app starts:
// each database is made new and empty by the provider, filled as the app's
// user and checked by the kind's verify. A deployment that has run is never
// filled. The record and the receipt name the set and its digests, and the
// instance and database each copy came from and went into.
func (s *Service) startFromCopy(ctx context.Context, rel NumberedRelease, dep *Deployment, t *Target, actor string, logf deploy.Logf, allowed func(context.Context) error) error {
	current, err := s.Deployments.ByID(ctx, dep.ID)
	if err != nil {
		return err
	}
	if current.StartFrom.IsZero() || current.StartedFrom.Valid {
		return nil
	}
	// The release fills the copy its caller was authorised for when it was
	// admitted, or nothing.
	authorised, err := deploymentsql.New(s.db.SQL()).ReleaseStartFrom(ctx, rel.ID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if authorised != current.StartFrom {
		return fmt.Errorf("%w: this release was admitted to fill set %d, not set %d; ask for a release again", ErrStartFrom, authorised, current.StartFrom)
	}
	if s.starter == nil {
		return errNoCopyStarter
	}
	receipt, err := s.starter.FillStart(ctx, StartFill{Deployment: current, Target: t, Logf: logf, Allowed: allowed})
	if err != nil {
		return err
	}
	return s.Deployments.RecordStartedFrom(ctx, current, receipt, actor, map[string]any{"release": rel.Number})
}

// RecordStartedFrom records that a deployment that had never run started
// from a set, with its receipt (extra adds what started it: a release, a
// restored copy), together. The start is recorded once: a deployment whose
// start changed while it was filled is refused.
func (s *Deployments) RecordStartedFrom(ctx context.Context, dep *Deployment, r StartReceipt, actor string, extra map[string]any) error {
	return s.db.InTx(ctx, func(tx *database.Tx) error {
		at := s.now()
		n, err := deploymentsql.New(tx.SQL()).MarkStartedFrom(ctx, deploymentsql.MarkStartedFromParams{ID: dep.ID, BackupID: r.Set, At: at})
		if err != nil {
			return err
		}
		if n != 1 {
			return fmt.Errorf("%w: the deployment's start changed while it was filled", ErrStartFrom)
		}
		payload := map[string]any{"backup": r.Set, "fromDeployment": r.FromDeployment, "fromProject": r.FromProject, "from": r.From, "into": r.Into}
		maps.Copy(payload, extra)
		return audit.At(at).RecordIn(ctx, tx, actor, dep.ProjectID, "deployment.started-from-copy", dep.Name, payload)
	})
}
