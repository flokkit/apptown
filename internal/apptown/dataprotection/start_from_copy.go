package dataprotection

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/dataprotection/internal/protectionsql"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/fault"
)

// Starting a deployment from a copy (design/subsystems/deployment.md,
// "Protecting data off-host"): a new deployment names a set in the backup store, and its
// first release, once the provider has made its database and before the app
// starts, fills that database from the copy and checks it. The deployment's
// record names the set it started from. Putting data into a deployment is a
// restore: it needs deployment:restore in the set's project and in the
// deployment's, checked when the start is chosen and again when the release
// that fills it is asked for; no agent carries it. A production copy goes
// only into a production deployment.

// FinalCopy is a taken-down deployment's final copy, offered to start a new
// deployment from.
type FinalCopy struct {
	Backup     ids.ID `json:"backup"`
	Deployment string `json:"deployment"`
	TakenAt    string `json:"takenAt"`
	Bytes      int64  `json:"bytes"`
	DataClass  string `json:"dataClass"`
	// Verified says every item a start would fill from it was last
	// checked, on this copy, to come back into a new place.
	Verified bool `json:"verified"`
}

// FinalCopies are the project's final copies in the store, newest first:
// "start from the final copy of <name>". They are kept with the project.
func (s *Service) FinalCopies(ctx context.Context, projectID ids.ID) ([]FinalCopy, error) {
	rows, err := protectionsql.New(s.db.SQL()).FinalStoreSets(ctx, protectionsql.FinalStoreSetsParams{ProjectID: projectID, Store: StoreBackupStore,
		FinalTrigger: triggerTakeDown, OkState: BackupOK.String()})
	if err != nil {
		return nil, err
	}
	out := []FinalCopy{}
	for _, r := range rows {
		b, err := backupRecord(r, nil)
		if err != nil {
			return nil, err
		}
		// Only a copy a new deployment can start from whole is offered.
		if managedFillable(&b, deployment.ErrStartFrom) != nil {
			continue
		}
		unchecked, err := s.uncheckedItems(ctx, &b)
		if err != nil {
			return nil, err
		}
		out = append(out, FinalCopy{Backup: b.ID, Deployment: b.Deployment, TakenAt: b.TakenAt, Bytes: b.Bytes, DataClass: b.DataClass, Verified: len(unchecked) == 0})
	}
	return out, nil
}

// CheckStart says whether the caller may start a deployment of
// destinationProject holding dataClass from set: a whole copy in the store
// a new managed database can be filled from (startSet), deployment:restore
// in the set's project and in the destination's, and a production copy
// only into production.
func (s *starter) CheckStart(ctx context.Context, set, destinationProject ids.ID, dataClass string) error {
	b, err := s.startSet(ctx, set)
	if err != nil {
		return err
	}
	if s.authority == nil {
		return errors.New("starting from a copy is not configured")
	}
	for _, project := range []ids.ID{b.ProjectID, destinationProject} {
		if _, err := s.authority.Require(ctx, access.Permission{Capability: access.CapDeploymentRestore, Resource: projectResource(project), Context: access.Context{Route: "deployment.start-from", Consequential: true}}); err != nil {
			return err
		}
	}
	if b.DataClass == "production" && dataClass != "production" {
		return deployment.ErrProductionCopy
	}
	return nil
}

// UncheckedStart names each item a start would fill from set that its
// last check of this set did not show to come back, with that check: ""
// when every one did. A drill fills a set's items in turn and records each
// alone, so one item's pass says nothing of another's. The first release
// of a deployment that starts from set is refused on a non-empty answer
// unless a person accepts it (deployment.DeploySelection.AcceptUncheckedCopy).
func (s *starter) UncheckedStart(ctx context.Context, set ids.ID) (string, error) {
	b, err := s.startSet(ctx, set)
	if err != nil {
		return "", err
	}
	unchecked, err := s.uncheckedItems(ctx, b)
	if err != nil || len(unchecked) == 0 {
		return "", err
	}
	return fmt.Sprintf("set %d is not shown to come back whole: %s", b.ID, strings.Join(unchecked, "; ")), nil
}

// uncheckedItems are the items a start would fill from b (FillStart's: a
// good copy with a portable format) whose last check of b did not pass,
// each with that check or "never checked".
func (s *core) uncheckedItems(ctx context.Context, b *Backup) ([]string, error) {
	var out []string
	for _, it := range b.Items {
		if it.Error != "" || it.Format == "" {
			continue
		}
		d, err := protectionsql.New(s.db.SQL()).LatestDrillOfSet(ctx, protectionsql.LatestDrillOfSetParams{BackupID: b.ID, Need: it.Name})
		switch {
		case errors.Is(err, sql.ErrNoRows):
			out = append(out, it.Name+": never checked")
		case err != nil:
			return nil, err
		case !d.Passed:
			out = append(out, fmt.Sprintf("%s: its last check (%s) failed: %s", it.Name, d.DrilledAt.UTC().Format("2006-01-02 15:04 UTC"), d.Detail))
		}
	}
	return out, nil
}

// storeSet is a set a new place may be filled from: a whole set in the
// backup store. Each path that fills from it adds its
// own rule for the items (managedFillable, a rehearsal's moves); refused is
// the sentinel its refusals wrap.
func (s *core) storeSet(ctx context.Context, id ids.ID, refused error) (*Backup, error) {
	b, err := backupRecord(protectionsql.New(s.db.SQL()).BackupByIDAnywhere(ctx, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fault.New(fault.NotFound, fmt.Sprintf("no copy %d", id))
	}
	if err != nil {
		return nil, err
	}
	if b.Store != StoreBackupStore || b.Status != BackupOK {
		return nil, fmt.Errorf("%w: set %d is not a whole copy in the backup store (%s, %s)", refused, b.ID, b.Store, b.Status)
	}
	return &b, nil
}

// startSet is a set a managed deployment may start from: a store set
// every good item of which a new database can be filled from.
func (s *core) startSet(ctx context.Context, id ids.ID) (*Backup, error) {
	b, err := s.storeSet(ctx, id, deployment.ErrStartFrom)
	if err != nil {
		return nil, err
	}
	if err := managedFillable(b, deployment.ErrStartFrom); err != nil {
		return nil, err
	}
	return b, nil
}

// managedFillable says why a set cannot fill a managed deployment's new
// databases, nil when it can: every good item must be a need's portable
// copy (a Format). A Docker host's set can also hold the app's files or a
// copy with no portable format (Redis); filling only the rest would start
// the deployment without them, so the set is refused and they are named.
func managedFillable(b *Backup, refused error) error {
	var not []string
	portable := 0
	for _, it := range b.Items {
		switch {
		case it.Error != "":
		case it.Kind == "need" && it.Format != "":
			portable++
		case it.Kind == "volume":
			not = append(not, "the app's files (its data folder)")
		default:
			not = append(not, it.Name+" (a copy with no portable format)")
		}
	}
	if len(not) > 0 {
		return fmt.Errorf("%w: set %d also holds %s, which a new managed database cannot be filled from; starting from it would leave them behind; choose a set that holds only portable copies", refused, b.ID, strings.Join(not, " and "))
	}
	if portable == 0 {
		return fmt.Errorf("%w: set %d holds no portable copy", refused, b.ID)
	}
	return nil
}

// FillStart fills a deployment that has never run from the set it starts
// from: each database made new and empty by the provider, filled as the
// app's user and checked by the kind's verify. It records nothing: the
// receipt says what came from where, and Deployment records the start
// (Deployments.RecordStartedFrom). A safe fill is a restored copy's: a
// failing import or check is said by its exit status, never by what the
// command printed, which can hold rows of production data.
//
// Allowed, when set, is asked before each database is made new and again
// before production data goes into it (an operation's admitting
// authority); a release passes nil, its authority having been decided when
// it was admitted.
func (s *starter) FillStart(ctx context.Context, in deployment.StartFill) (deployment.StartReceipt, error) {
	current, t, logf, safe, allowed := in.Deployment, in.Target, in.Logf, in.Safe, in.Allowed
	if allowed == nil {
		allowed = func(context.Context) error { return nil }
	}
	if current.StartFrom.IsZero() || current.StartedFrom.Valid {
		return deployment.StartReceipt{}, fmt.Errorf("%w: %s does not start from a copy it has not started from", deployment.ErrStartFrom, current.Name)
	}
	if current.ObservedGeneration != 0 {
		return deployment.StartReceipt{}, fmt.Errorf("%w: %s has run, so it is not filled from a copy", deployment.ErrStartFrom, current.Name)
	}
	if current.Feed == "landing" {
		return deployment.StartReceipt{}, fmt.Errorf("%w: a deployment fed by every landing is not filled from a copy", deployment.ErrStartFrom)
	}
	b, err := s.startSet(ctx, current.StartFrom)
	if err != nil {
		return deployment.StartReceipt{}, err
	}
	if b.DataClass == "production" && current.DataClass != "production" {
		return deployment.StartReceipt{}, deployment.ErrProductionCopy
	}
	items, err := s.managedList(ctx, current, t)
	if err != nil {
		return deployment.StartReceipt{}, err
	}
	type move struct {
		from *BackupItem
		into backupPlanItem
	}
	var moves []move
	var homeless []string
	for i := range b.Items {
		from := &b.Items[i]
		if from.Error != "" || from.Format == "" {
			continue
		}
		found := false
		for _, it := range items {
			if it.Name == from.Name && it.Unresolved == "" && it.copyOf().Format == from.Format {
				moves = append(moves, move{from, it})
				found = true
				break
			}
		}
		if !found {
			homeless = append(homeless, from.Name+" ("+from.Format+")")
		}
	}
	if len(homeless) > 0 {
		return deployment.StartReceipt{}, fmt.Errorf("%w: set %d holds %s, which this deployment has no database of that kind to take", deployment.ErrStartFrom, b.ID, strings.Join(homeless, ", "))
	}
	// A bucket the set holds no copy of would start empty while the
	// deployment reads as started from the copy.
	for _, it := range items {
		if it.Bucket == "" {
			continue
		}
		if !slices.ContainsFunc(moves, func(m move) bool { return m.into.Bucket == it.Bucket }) {
			return deployment.StartReceipt{}, fmt.Errorf("%w: set %d holds no copy of the %s bucket's objects, so the new bucket %s would start empty", deployment.ErrStartFrom, b.ID, it.Name, it.Bucket)
		}
	}
	c, store, err := s.storeOf(ctx, b)
	if err != nil {
		return deployment.StartReceipt{}, err
	}
	dk, runner, err := s.copyRunner(ctx, t)
	if err != nil {
		return deployment.StartReceipt{}, err
	}
	md := s.providers.ManagedData(t)
	bd, err := s.bucketData(ctx, t, current)
	if err != nil {
		return deployment.StartReceipt{}, err
	}
	// A start-from-copy stages under the deployment's id; operations stage
	// under theirs, and the two never collide.
	dir, cleanup, err := s.staging(current.ID)
	if err != nil {
		return deployment.StartReceipt{}, err
	}
	defer cleanup()
	var from, into []map[string]any
	for _, m := range moves {
		logf("starting from set %d (taken %s of %s): reading its copy of %s back from the store", b.ID, b.TakenAt, b.Deployment, m.from.Name)
		path, err := readBack(ctx, c, store.Bucket, b, m.from, dir)
		if err != nil {
			return deployment.StartReceipt{}, err
		}
		logf("the copy is %d bytes with sha256 %s, as the set records", m.from.Bytes, m.from.SHA256)
		if err := allowed(ctx); err != nil {
			return deployment.StartReceipt{}, err
		}
		if m.into.Bucket != "" {
			// The app has never run on this bucket: the kind's restore makes it
			// hold exactly the copy, whatever an earlier attempt left in it,
			// and its verify checks it, as the deployment's own key.
			logf("filling the new %s from it on %s as the deployment's own key, then checking it", m.into.where(), runner)
			conn, err := s.connection(ctx, md, bd, m.into)
			if err == nil {
				err = fillAt(ctx, dk, nil, m.into.copyOf(), clientImage(m.into.copyOf(), ""), m.into.copyOf().Restore, conn, path)
			}
			if err != nil {
				if safe {
					return deployment.StartReceipt{}, &safeFailure{fmt.Sprintf("filling %s from the copy failed: the %s kind's restore or check ended with %s", m.from.Name, m.from.Name, exitOf(err))}
				}
				return deployment.StartReceipt{}, err
			}
			logf("%s came back: the %s check passed", m.from.Name, m.from.Format)
			from = append(from, map[string]any{"need": m.from.Name, "bucket": m.from.Bucket, "sha256": m.from.SHA256, "bytes": m.from.Bytes})
			into = append(into, map[string]any{"need": m.into.Name, "bucket": m.into.Bucket})
			continue
		}
		// The app has never run on this database: the provider makes it
		// new and empty, whatever an earlier attempt left in it.
		if err := md.DropDatabase(ctx, m.into.Instance, m.into.Database); err != nil {
			return deployment.StartReceipt{}, err
		}
		if err := md.NewDatabase(ctx, m.into.Instance, m.into.Database, m.into.User); err != nil {
			return deployment.StartReceipt{}, err
		}
		if err := allowed(ctx); err != nil {
			return deployment.StartReceipt{}, err
		}
		logf("filling the new %s from it on %s as %s, then checking it", m.into.where(), runner, m.into.User)
		if err := fill(ctx, dk, md, m.into.copyOf(), clientImage(m.into.copyOf(), m.into.Version), m.into.Instance, m.into.Database, m.into.User, path); err != nil {
			if safe {
				return deployment.StartReceipt{}, &safeFailure{fmt.Sprintf("filling %s from the copy failed: the %s kind's import or check ended with %s", m.from.Name, m.from.Name, exitOf(err))}
			}
			return deployment.StartReceipt{}, err
		}
		logf("%s came back: the %s check passed", m.from.Name, m.from.Format)
		from = append(from, map[string]any{"need": m.from.Name, "instance": m.from.Instance, "database": m.from.Database, "sha256": m.from.SHA256, "bytes": m.from.Bytes})
		into = append(into, map[string]any{"need": m.into.Name, "instance": m.into.Instance, "cluster": m.into.Cluster, "database": m.into.Database})
	}
	return deployment.StartReceipt{Set: b.ID, FromDeployment: b.Deployment, FromProject: b.ProjectID, From: from, Into: into}, nil
}

// CheckStartAuthority checks a queued release's captured caller against current
// restore grants on both projects. It does not substitute a background caller.
func (s *starter) CheckStartAuthority(ctx context.Context, set, destinationProject ids.ID, dataClass string, captured access.Principal) error {
	b, err := s.startSet(ctx, set)
	if err != nil {
		return err
	}
	if s.still == nil || s.authorityProjects == nil {
		return errors.New("copy-start authority recheck is not configured")
	}
	for _, projectID := range []ids.ID{b.ProjectID, destinationProject} {
		resource, err := s.authorityProjects.AuthorityResource(ctx, projectID)
		if err != nil {
			return err
		}
		if err := s.still.RequireStill(ctx, captured, access.Permission{Capability: access.CapDeploymentRestore, Resource: resource, Context: access.Context{Route: "deployment.start-from", Consequential: true}}); err != nil {
			return err
		}
	}
	if b.DataClass == "production" && dataClass != "production" {
		return deployment.ErrProductionCopy
	}
	return nil
}
