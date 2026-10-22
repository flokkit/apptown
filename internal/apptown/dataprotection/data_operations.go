package dataprotection

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/project"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/dataprotection/internal/protectionsql"
	"github.com/flokkit/apptown/internal/apptown/operations"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/strs"
)

// The doors and the runner of data operations: a copy of a deployment's
// managed data to the store, a drill of its newest copy, and the take-down
// of an App Platform deployment that holds data (final copy, drill, then
// the cluster's deletion). Each door checks its authority and what the
// operation needs before it records anything; the job runs it.

// projectResource is the authority resource of a project.
func projectResource(projectID ids.ID) access.Resource {
	return access.Resource{Type: "project", ID: projectID.String(), ProjectID: projectID}
}

// protectedPlace is a standing deployment whose data AppTown copies on
// its target: its target, and the items it holds. When what it holds
// cannot be read (ErrDataNotKnown) the deployment and its target are
// still returned with the error, so protection can say unknown.
func (s *Service) protectedPlace(ctx context.Context, projectID ids.ID, name string) (*deployment.Deployment, *deployment.Target, []backupPlanItem, error) {
	dep, err := s.deployments.ByName(ctx, projectID, name)
	if err != nil || !dep.Standing() {
		return nil, nil, nil, fault.New(fault.NotFound, fmt.Sprintf("no standing deployment named %q", name))
	}
	t, err := s.targets.ByID(ctx, dep.TargetID)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%w: %w", deployment.ErrNoTarget, err)
	}
	if managedProvider(t) == "" {
		return dep, t, nil, nil // a Docker host's data is read where it is used (heldData)
	}
	items, err := s.managedList(ctx, dep, t)
	return dep, t, items, err
}

// ready says why an operation on the deployment's data cannot run now
// (no store, no copy runner, nothing to copy), nil when it can.
func (s *core) ready(ctx context.Context, t *deployment.Target, items []backupPlanItem) error {
	reachable := 0
	for _, it := range items {
		if it.Unresolved == "" {
			reachable++
		}
	}
	if reachable == 0 {
		return ErrNotReachable
	}
	store, err := s.Store(ctx)
	if err != nil {
		return err
	}
	if store == nil {
		return ErrNoStore
	}
	if t.CopyRunner() == "" {
		return deployment.ErrNoCopyRunner
	}
	return nil
}

// RequestCopy starts a copy of a standing deployment's managed data to
// the backup store, as the caller. It needs the deploy grant, as taking a
// Docker host's backup does.
func (s *Service) RequestCopy(ctx context.Context, projectID ids.ID, name string) (*operations.Operation, error) {
	actor, err := s.requireAuthority(ctx, access.CapDeploymentDeploy, projectID, "deployment.copy")
	if err != nil {
		return nil, err
	}
	dep, t, items, err := s.protectedPlace(ctx, projectID, name)
	if err != nil {
		return nil, err
	}
	if err := s.copyAdmission(ctx, t, items); err != nil {
		return nil, err
	}
	return s.admit(ctx, dep, OperationCopy, actor, []string{deployment.DataHold(dep)}, nil, nil)
}

// RequestDrill starts a drill of a deployment's newest copy in the store:
// it is filled into a new, empty database on the deployment's own cluster,
// checked there and removed. Production data moves, so it needs the
// restore grant; the result is pass or fail and why, never the data.
func (s *Service) RequestDrill(ctx context.Context, projectID ids.ID, name string) (*operations.Operation, error) {
	actor, err := s.requireAuthority(ctx, access.CapDeploymentRestore, projectID, "deployment.drill")
	if err != nil {
		return nil, err
	}
	dep, t, items, err := s.protectedPlace(ctx, projectID, name)
	if err != nil {
		return nil, err
	}
	if err := s.drillAdmission(ctx, dep, t, items); err != nil {
		return nil, err
	}
	return s.admit(ctx, dep, OperationDrill, actor, []string{deployment.DataHold(dep)}, nil, nil)
}

// copyAdmission is why a copy of the deployment's data cannot start now,
// nil when it can. A Docker host's deployment is copied by its backups,
// not by a copy operation.
func (s *Service) copyAdmission(ctx context.Context, t *deployment.Target, items []backupPlanItem) error {
	if t.HoldsBackups() == nil {
		return ErrDockerBackupsPage
	}
	return s.ready(ctx, t, items)
}

// drillAdmission is why a check of the deployment's newest copy cannot
// start now, nil when it can. A check fills a new database a provider
// makes, which a Docker host does not.
func (s *Service) drillAdmission(ctx context.Context, dep *deployment.Deployment, t *deployment.Target, items []backupPlanItem) error {
	if t.HoldsBackups() == nil {
		return ErrDrillAppPlatform
	}
	if err := s.ready(ctx, t, items); err != nil {
		return err
	}
	_, err := s.newestStoreSet(ctx, dep.ProjectID, dep.Name)
	return err
}

// requireAuthority checks the caller's capability in the project and
// returns the principal an operation captures.
func (s *Service) requireAuthority(ctx context.Context, c access.Capability, projectID ids.ID, route string) (access.Principal, error) {
	if s.authority == nil {
		return access.Principal{}, errors.New("data operations are not configured")
	}
	actor, err := s.authority.Require(ctx, access.Permission{Capability: c, Resource: projectResource(projectID), Context: access.Context{Route: route, Consequential: true}})
	if err != nil {
		return access.Principal{}, err
	}
	if actor.Principal.Name == "" {
		return access.Principal{}, errors.New("a caller is required")
	}
	return actor.Principal, nil
}

var ErrNoCopy error = &deployment.Unavailable{Reason: "no-copy", Why: "There is no good copy of its data in the backup store yet: copy it first."}

// newestStoreSet is a deployment's newest good set in the store.
func (s *Service) newestStoreSet(ctx context.Context, projectID ids.ID, name string) (*Backup, error) {
	sets, err := s.backups.ForDeployment(ctx, projectID, name, 50)
	if err != nil {
		return nil, err
	}
	for i := range sets {
		if sets[i].Store == StoreBackupStore && sets[i].Status == BackupOK {
			return &sets[i], nil
		}
	}
	return nil, ErrNoCopy
}

// TakeDownResult is what the take-down door did: a removal done now, or an
// operation started that will do it.
type TakeDownResult struct {
	Removed   *RemovalResult
	Operation *operations.Operation
}

// TakeDown is the take-down door. A deployment on App Platform that holds
// data AppTown copies, with a backup store attached and a copy runner
// named, is taken down by an operation: the app is deleted so nothing
// writes, a final copy goes to the store and is filled into a new place
// and checked, and only then is the cluster deleted. One whose final copy
// cannot be taken now (no store, no copy runner, nothing AppTown
// reaches) is refused (ErrNoFinalCopy), unless the person chose to take
// it down without one (WithoutFinalCopy): then its cluster is kept as a
// retained resource for a person to settle. Anything else is removed now.
func (s *Service) TakeDown(ctx context.Context, in RemoveDeployment) (TakeDownResult, error) {
	op, noCopy, err := s.beginManagedTakeDown(ctx, in)
	if err != nil || op != nil {
		return TakeDownResult{Operation: op}, err
	}
	in.noCopy = noCopy
	removed, err := s.Remove(ctx, in)
	if err != nil {
		return TakeDownResult{}, err
	}
	return TakeDownResult{Removed: &removed}, nil
}

// beginManagedTakeDown admits the take-down operation when the deployment
// is one, nil when it is not. Project deletion admits it with its project
// (main too) and then runs it, or waits for it (TakeDownWithProject). A
// deployment holding managed data whose final copy cannot be taken now is
// refused, unless the person chose to go without one: then noCopy says
// why, for the removal's receipt.
func (s *Service) beginManagedTakeDown(ctx context.Context, in RemoveDeployment) (op *operations.Operation, noCopy string, err error) {
	actor, err := s.takeDownActor(ctx, in, "deployment.remove")
	if err != nil {
		return nil, "", err
	}
	dep, err := s.deployments.ByName(ctx, in.ProjectID, in.Name)
	if err != nil {
		if found, lerr := database.Lookup(err); lerr == nil && !found {
			return nil, "", deployment.ErrDeploymentMissing
		}
		return nil, "", err
	}
	if in.WithoutFinalCopy != "" && in.WithoutFinalCopy != dep.Name {
		return nil, "", fault.New(fault.Invalid, fmt.Sprintf("the choice to go without a final copy names %q, not this deployment (%s)", in.WithoutFinalCopy, dep.Name))
	}
	t, err := s.targets.ByID(ctx, dep.TargetID)
	if err != nil || t.DataAccess() != deployment.DataOnManagedPlatform {
		return nil, "", nil
	}
	// An eval copy's data is synthetic: it gets no final copy, so it is
	// removed now (TakeDownWithProject).
	purpose, err := s.env.Purpose(ctx, in.ProjectID)
	if err != nil || !purpose.KeepsData() {
		return nil, "", err
	}
	items, err := s.managedList(ctx, dep, t)
	if err != nil {
		return nil, "", err
	}
	if why := s.ready(ctx, t, items); why != nil {
		if len(items) == 0 {
			return nil, "", nil // it holds no managed data
		}
		var u *deployment.Unavailable
		if !errors.As(why, &u) {
			return nil, "", why
		}
		if in.WithoutFinalCopy == "" {
			return nil, "", &NoFinalCopy{Deployment: dep.Name, Cause: u}
		}
		return nil, u.Why, nil
	}
	// A final copy can be taken now, so this take-down would copy and then
	// delete the cluster: not what a person who chose to keep the data
	// without a copy agreed to. They confirm again what it means now.
	if in.WithoutFinalCopy != "" {
		return nil, "", ErrFinalCopyPossible
	}
	// The same admission a removal has (not main, no release or attempt in
	// flight), decided in the transaction that records the operation.
	admit := func(tx *database.Tx) error {
		_, err := selectRemovalIn(ctx, tx, in)
		return err
	}
	// Removing main is allowed only because its project is being deleted:
	// that is fixed with the operation when it is admitted.
	var input map[string]any
	if in.withProject {
		input = map[string]any{"withProject": true}
	}
	op, err = s.admit(ctx, dep, OperationTakeDown, actor, []string{deployment.SubjectOf(dep)}, input, admit)
	return op, "", err
}

// NoFinalCopy refuses the take-down of a deployment holding data AppTown
// cannot take a final copy of now: the data is kept or deleted only by a
// person's explicit choice (RemoveDeployment.WithoutFinalCopy). It says
// why, in the words its data operations are refused with.
type NoFinalCopy struct {
	Deployment string
	Cause      *deployment.Unavailable
}

func (e *NoFinalCopy) Error() string {
	return fmt.Sprintf("%s holds data AppTown cannot take a final copy of now. %s To take it down anyway, choose to take it down without a final copy: its data stays where it is, as a retained resource for a person to settle", e.Deployment, e.Cause.Why)
}
func (e *NoFinalCopy) Unwrap() error         { return e.Cause }
func (e *NoFinalCopy) FaultKind() fault.Kind { return fault.Conflict }
func (e *NoFinalCopy) FaultCode() string     { return "no-final-copy" }

// ErrFinalCopyPossible refuses a choice to go without a final copy made
// when none could be taken, arriving now that one can: the take-down would
// delete the cluster after a copy, not keep it, so its meaning changed.
var ErrFinalCopyPossible error = &deployment.Unavailable{Reason: "final-copy-possible", Why: "A final copy of its data can be taken now, so a take-down would copy it, check it and then delete its database cluster, not keep it. Nothing was done: confirm the take-down again."}

// ErrNoFinalCopy matches every NoFinalCopy refusal (errors.Is).
var ErrNoFinalCopy = &NoFinalCopy{}

func (e *NoFinalCopy) Is(target error) bool { return target == ErrNoFinalCopy }

func (s *Service) runCopy(ctx context.Context, t *tracker) (runEnd, string, map[string]any) {
	dep, target, _, err := s.protectedPlace(ctx, t.op.ProjectID, t.op.Deployment)
	if err != nil {
		return OperationFailed, err.Error(), nil
	}
	if err := t.allowed(ctx); err != nil {
		return OperationFailed, "no copy was taken: " + err.Error(), nil
	}
	trigger := "manual"
	if t.op.AskedBy == SystemActor {
		trigger = "scheduled"
	}
	b, err := s.takeStoreCopy(ctx, t, dep, target, trigger)
	if err != nil {
		return OperationFailed, "no copy was taken: " + err.Error(), nil
	}
	result := map[string]any{"backup": b.ID, "bytes": b.Bytes}
	if b.Status != BackupOK {
		return OperationFailed, fmt.Sprintf("set %d is %s: %s", b.ID, b.Status, setShortfall(b)), result
	}
	// AppTown's own daily copy of production data is also drilled once a
	// week: the newest copy is filled into a new place and checked.
	if t.op.AskedBy == SystemActor && s.drillDue(ctx, dep, b) {
		passed, detail, err := s.drill(ctx, t, b, target)
		if err != nil {
			return OperationFailed, fmt.Sprintf("set %d is in the store (%d bytes); its weekly check could not run: %v", b.ID, b.Bytes, err), result
		}
		if !passed {
			return OperationFailed, fmt.Sprintf("set %d is in the store (%d bytes) but did not come back: %s", b.ID, b.Bytes, detail), result
		}
		return OperationVerified, fmt.Sprintf("set %d is in the store (%d bytes) and came back: %s", b.ID, b.Bytes, detail), result
	}
	return OperationNotVerified, fmt.Sprintf("set %d is in the store (%d bytes); nothing has filled it into a new place yet", b.ID, b.Bytes), result
}

// DrillEvery is how often AppTown drills production data on its own.
const DrillEvery = 7 * 24 * time.Hour

// drillDue says whether a production deployment's set b is to be drilled:
// any item the drill would fill (a good copy with a portable format) whose
// own last drill on this deployment failed, is more than DrillEvery old, or
// is missing. A drill records each item alone, so the deployment's newest
// row is only its last item's.
func (s *Service) drillDue(ctx context.Context, dep *deployment.Deployment, b *Backup) bool {
	if dep.DataClass != "production" {
		return false
	}
	for _, it := range b.Items {
		if it.Error != "" || it.Format == "" {
			continue
		}
		last, err := protectionsql.New(s.db.SQL()).LatestDrillOf(ctx, protectionsql.LatestDrillOfParams{DeploymentID: dep.ID, Need: it.Name})
		if err != nil || !last.Passed || s.now().Sub(last.DrilledAt) > DrillEvery {
			return true
		}
	}
	return false
}

// ScheduledCopies starts AppTown's own daily copy of every production
// deployment whose data it copies on its target, where a store is
// attached and a copy runner named. One already running is left to run;
// what could not start is named.
func (s *Service) ScheduledCopies(ctx context.Context) (started int, skipped []string, err error) {
	deps, err := s.deployments.ProductionOnTargetKind(ctx, deployment.KindDOApp)
	if err != nil {
		return 0, nil, err
	}
	for _, dep := range deps {
		t, err := s.targets.ByID(ctx, dep.TargetID)
		if err != nil {
			skipped = append(skipped, dep.Name+": "+err.Error())
			continue
		}
		items, err := s.managedList(ctx, dep, t)
		if err != nil {
			return started, skipped, err
		}
		if why := s.ready(ctx, t, items); why != nil {
			skipped = append(skipped, fmt.Sprintf("%d/%s: %v", dep.ProjectID, dep.Name, why))
			continue
		}
		if _, err := s.admit(ctx, dep, OperationCopy, operations.System("scheduled-copy"), []string{deployment.DataHold(dep)}, nil, nil); err != nil {
			var running *operations.HeldError
			if errors.As(err, &running) || errors.Is(err, project.ErrNotActive) {
				skipped = append(skipped, fmt.Sprintf("%d/%s: %v", dep.ProjectID, dep.Name, err))
				continue
			}
			return started, skipped, err
		}
		started++
	}
	return started, skipped, nil
}

func (s *Service) runDrill(ctx context.Context, t *tracker) (runEnd, string, map[string]any) {
	dep, target, _, err := s.protectedPlace(ctx, t.op.ProjectID, t.op.Deployment)
	if err != nil {
		return OperationFailed, err.Error(), nil
	}
	if err := t.allowed(ctx); err != nil {
		return OperationFailed, "nothing was filled: " + err.Error(), nil
	}
	b, err := s.newestStoreSet(ctx, dep.ProjectID, dep.Name)
	if err != nil {
		return OperationFailed, err.Error(), nil
	}
	passed, detail, err := s.drill(ctx, t, b, target)
	result := map[string]any{"backup": b.ID}
	if err != nil {
		return OperationFailed, err.Error(), result
	}
	if !passed {
		return OperationFailed, fmt.Sprintf("set %d did not come back: %s", b.ID, detail), result
	}
	return OperationVerified, fmt.Sprintf("set %d came back: %s", b.ID, detail), result
}

// setShortfall names what a set does not hold.
func setShortfall(b *Backup) string {
	var missing []string
	for _, it := range b.Items {
		if it.Error != "" {
			missing = append(missing, it.Name+": "+it.Error)
		}
	}
	return strings.Join(missing, "; ")
}

// SystemActor is AppTown acting for itself (the scheduled copies).
const SystemActor = operations.SystemActor

// runTakeDown takes an App Platform deployment that holds data down:
//  1. delete the app, so nothing writes (it is rebuilt from the records);
//     its bucket and the bucket's key stay, so the copy can read it;
//  2. take the final copy to the store;
//  3. fill a new database on the same cluster from it, check it, drop it;
//     fill a bucket's copy into a new store on the copy runner and check it;
//  4. record the copy as checked (the drill);
//  5. delete the cluster, only when it holds nothing but this deployment's
//     checked data: an open retained database on it, or any database the
//     provider lists that is not the deployment's own or the provider's,
//     refuses; delete the bucket's key, and the bucket, whose copy came
//     back;
//  6. remove the deployment's records.
//
// A step that fails after the app is deleted leaves the data where it is
// and the deployment's record standing: "app removed, data kept; deploy
// again to serve".
func (s *Service) runTakeDown(ctx context.Context, t *tracker, withProject bool) (runEnd, string, map[string]any) {
	in := RemoveDeployment{ProjectID: t.op.ProjectID, Name: t.op.Deployment, withProject: withProject}
	selected, err := database.Transact(ctx, s.db, func(tx *database.Tx) (*deployment.Deployment, error) { return selectRemovalIn(ctx, tx, in) })
	if err != nil {
		return OperationFailed, "nothing was changed: " + err.Error(), nil
	}
	target, err := s.targets.ByID(ctx, selected.TargetID)
	if err != nil {
		return OperationFailed, "nothing was changed: " + err.Error(), nil
	}
	driver, err := s.providers.Driver(ctx, target, selected)
	if err != nil {
		return OperationFailed, "nothing was changed: " + err.Error(), nil
	}
	owned, err := s.deployments.Owned(ctx, selected.ID)
	if err != nil {
		return OperationFailed, "nothing was changed: reading what the deployment owns: " + err.Error(), nil
	}
	owned, keys := splitOwned(owned)
	owned, storage := splitStorage(owned)
	items, err := s.managedList(ctx, selected, target)
	if err != nil {
		return OperationFailed, "nothing was changed: " + err.Error(), nil
	}
	others, err := s.deployments.OthersOnConnector(ctx, selected, target)
	if err != nil {
		return OperationFailed, "nothing was changed: reading the project's other deployments on this account: " + err.Error(), nil
	}
	const kept = "app removed, data kept; deploy again to serve. "

	if err := t.allowed(ctx); err != nil {
		return OperationFailed, "nothing was changed: " + err.Error(), nil
	}
	if t.stop(ctx) {
		return OperationFailed, "stopped as asked before anything was changed", nil
	}
	t.begin(ctx, "deleting the app, so nothing writes")
	taken, err := driver.Teardown(ctx, selected.Placement(), selected.ObservedGeneration, deploy.TakeDown{Owned: owned, LastUser: others == 0})
	if s.applied != nil {
		s.applied()
	}
	if err != nil {
		if t.providerFailed(ctx, err) {
			return OperationFailed, "deleting the app got no answer AppTown can trust, so whether the app still stands is not known; nothing else was changed. Look at the provider before asking again: " + err.Error(), nil
		}
		return OperationFailed, "the provider refused to delete the app, so nothing was changed: " + err.Error(), nil
	}
	t.end(ctx, strings.Join(taken.Deleted, "; "), 0)
	// Shown on the operation's page; the steps above are the record, so a
	// failure loses only this display fact.
	if err := t.record(ctx, "writesStopped", true); err != nil {
		log.Printf("operation: recording that writes stopped: %v", err)
	}
	result := map[string]any{"deleted": taken.Deleted}
	if err := t.allowed(ctx); err != nil {
		return OperationFailed, kept + "Its AI model keys were not revoked and no final copy was taken: " + err.Error(), result
	}
	s.releaseDeploymentKeys(ctx, selected, keys, &taken)
	result["deleted"] = taken.Deleted
	if t.stop(ctx) {
		return OperationFailed, kept + "Stopped as asked before its final copy.", result
	}

	final, err := s.takeStoreCopy(ctx, t, selected, target, TriggerTakeDown)
	if err != nil {
		return OperationFailed, kept + "No final copy was taken: " + err.Error(), result
	}
	result["finalBackup"] = final.ID
	if why := final.uncoveredManaged(items); why != "" {
		return OperationFailed, fmt.Sprintf("%sThe final copy, set %d, falls short: %s", kept, final.ID, why), result
	}
	if t.stop(ctx) {
		return OperationFailed, fmt.Sprintf("%sStopped as asked after its final copy, set %d, before checking it.", kept, final.ID), result
	}
	passed, detail, err := s.drill(ctx, t, final, target)
	if err != nil {
		return OperationFailed, fmt.Sprintf("%sSet %d could not be checked: %s", kept, final.ID, err), result
	}
	if !passed {
		return OperationFailed, fmt.Sprintf("%sSet %d did not come back, so the cluster stays: %s", kept, final.ID, detail), result
	}
	result["verified"] = detail
	if len(t.left) > 0 {
		return OperationFailed, fmt.Sprintf("%sSet %d came back, but its check left %s, so the cluster stays.", kept, final.ID, strings.Join(t.left, "; ")), result
	}

	deleted := map[string]bool{}
	md := s.providers.ManagedData(target)
	var shownEmpty []string
	for _, it := range items {
		if it.Bucket != "" {
			continue // a bucket is the storage teardown's, below
		}
		if t.stop(ctx) {
			return OperationFailed, fmt.Sprintf("%sSet %d came back; stopped as asked before deleting the cluster %s.", kept, final.ID, it.Cluster), result
		}
		t.begin(ctx, "deleting the cluster "+it.Cluster)
		empty, why := s.refusesInstanceDeletion(ctx, md, target, it, items)
		if why != "" {
			t.fail(ctx, why)
			return OperationFailed, fmt.Sprintf("%sSet %d holds this deployment's data and came back, but the cluster %s stays: %s", kept, final.ID, it.Cluster, why), result
		}
		// Asked again here, after the inspection, which can take a while.
		if err := t.allowed(ctx); err != nil {
			t.fail(ctx, err.Error())
			return OperationFailed, fmt.Sprintf("%sSet %d came back, but the cluster %s stays: %v", kept, final.ID, it.Cluster, err), result
		}
		if err := md.DeleteInstance(ctx, it.Instance); err != nil {
			if t.providerFailed(ctx, err) {
				return OperationFailed, fmt.Sprintf("%sSet %d came back; deleting the cluster %s got no answer AppTown can trust, so whether it still stands is not known. Look at the provider before asking again: %s", kept, final.ID, it.Cluster, err), result
			}
			return OperationFailed, fmt.Sprintf("%sSet %d came back; the provider refused to delete the cluster %s: %s", kept, final.ID, it.Cluster, err), result
		}
		deleted[it.Name] = true
		taken.Deleted = append(taken.Deleted, "cluster "+it.Cluster+" ("+it.Instance+") with database "+it.Database+"; "+empty)
		shownEmpty = append(shownEmpty, it.Cluster+": "+empty)
		t.end(ctx, empty, 0)
	}
	// The bucket and its key go once the bucket's copy came back: every
	// item of the set did, or the take-down ended above.
	if len(storage) > 0 {
		// Asked again here, as before a cluster: the check of the bucket's
		// copy can take a while.
		if t.stop(ctx) {
			return OperationFailed, fmt.Sprintf("%sSet %d came back; stopped as asked before deleting the bucket.", kept, final.ID), result
		}
		if err := t.allowed(ctx); err != nil {
			return OperationFailed, fmt.Sprintf("%sSet %d came back, but its bucket and key stay: %v", kept, final.ID, err), result
		}
		t.begin(ctx, "deleting the bucket's key and the bucket")
		bd, err := s.bucketData(ctx, target, selected)
		if err == nil && bd == nil {
			err = fmt.Errorf("%s cannot delete a bucket", target.Kind)
		}
		if err != nil {
			t.fail(ctx, err.Error())
			return OperationFailed, fmt.Sprintf("%sSet %d came back, but its bucket and key stay: %v", kept, final.ID, err), result
		}
		// Every item, each bucket of its own bucket, is in the final set and
		// came back (uncoveredManaged and the drill, above).
		gone := bd.TearDownStorage(ctx, selected.Placement(), deploy.TakeDown{Owned: storage, BackedUp: true})
		taken.Deleted = append(taken.Deleted, gone.Deleted...)
		taken.Left = append(taken.Left, gone.Left...)
		t.end(ctx, strings.Join(gone.Deleted, "; "), 0)
	}
	result["deleted"] = taken.Deleted
	result["shownEmpty"] = shownEmpty

	if err := t.allowed(ctx); err != nil {
		return OperationFailed, fmt.Sprintf("the app and the cluster were deleted after set %d came back, but the deployment's records were not removed: %v", final.ID, err), result
	}
	t.begin(ctx, "removing the deployment's records")
	_, err = database.Transact(ctx, s.db, func(tx *database.Tx) (bool, error) {
		_, err := settleRemovalIn(ctx, tx, in, selected, deployment.RemovalSettlement{Target: target.Name, By: t.op.Authority, Taken: taken, FinalBackup: final.ID, Deleted: deleted, At: s.now()})
		return err == nil, err
	})
	if err != nil {
		return OperationFailed, fmt.Sprintf("the app and the cluster were deleted after set %d came back, but the deployment's records were not removed: %v", final.ID, err), result
	}
	// Data the teardown kept (a bucket whose deletion failed) is said beside
	// the set, with why.
	var keptData []string
	for _, l := range taken.Left {
		if l.Data {
			name, _ := l.Handle["name"].(string)
			keptData = append(keptData, l.What+" "+name+" ("+l.Why+")")
		}
	}
	if len(keptData) > 0 {
		return OperationVerified, fmt.Sprintf("taken down: set %d (%d bytes) holds its data, kept with the project, and came back before it was deleted. Kept: %s, listed under retained resources until a person deletes or keeps it", final.ID, final.Bytes, strings.Join(keptData, ", ")), result
	}
	return OperationVerified, fmt.Sprintf("taken down: set %d (%d bytes) holds its data, kept with the project, and came back before it was deleted", final.ID, final.Bytes), result
}

// uncoveredManaged names the reachable items the set holds no good copy
// of, each held to the instance and database it was taken from.
func (b *Backup) uncoveredManaged(items []backupPlanItem) string {
	var missing []string
	for _, it := range items {
		if it.Unresolved != "" {
			missing = append(missing, it.Name+": "+it.Unresolved)
			continue
		}
		got := b.GoodCopy("need", it.Name)
		if got == nil || got.Instance != it.Instance || got.Database != it.Database || got.Bucket != it.Bucket {
			missing = append(missing, it.Name+" from "+it.where())
		}
	}
	if len(missing) == 0 && b.Status != BackupOK {
		missing = append(missing, "the set reads "+b.Status.String())
	}
	return strings.Join(missing, ", ")
}

// refusesInstanceDeletion says why a cluster must not be deleted: an open
// retained resource names a database in it, or the provider lists a
// database in it that no copy covers and that is not shown empty by the
// kind's check. A cluster that cannot be asked is not deleted
// either. When it may go, empty says which other databases were checked
// and shown empty, on which runner and as whom: the take-down's record
// witnesses the check.
func (s *Service) refusesInstanceDeletion(ctx context.Context, md deploy.ManagedData, t *deployment.Target, it backupPlanItem, items []backupPlanItem) (empty, why string) {
	open, err := s.retained.OpenForInstance(ctx, it.Instance)
	if err != nil {
		return "", "the retained resources could not be read: " + err.Error()
	}
	if len(open) > 0 {
		r := open[0]
		db, _ := r.Handle["database"].(string)
		if db == "" {
			db = "a database"
		}
		return "", fmt.Sprintf("it holds %s kept from the deployment %s (retained resource %d); delete that first and say so under Left behind", db, r.Deployment, r.ID)
	}
	dbs, err := md.Databases(ctx, it.Instance)
	if err != nil {
		return "", "the provider could not say what the cluster holds: " + err.Error()
	}
	covered := map[string]bool{}
	for _, other := range items {
		if other.Instance == it.Instance {
			covered[other.Database] = true
		}
	}
	// A database no copy covers goes with the cluster only when it answers
	// empty to the kind's check, run as the app's user like a copy:
	// whatever its name, and a provider's own default database included.
	dk, runner, err := s.copyRunner(ctx, t)
	if err != nil {
		return "", "what the cluster's other databases hold could not be checked: " + err.Error()
	}
	var held, checked []string
	for _, db := range dbs {
		if covered[db] {
			continue
		}
		conn, err := md.AppConnection(ctx, it.Instance, db, it.User)
		if err == nil {
			_, err = copyJob(ctx, dk, it.copyOf(), clientImage(it.copyOf(), it.Version), it.copyOf().Empty, conn, nil, nil)
		}
		if err != nil {
			held = append(held, fmt.Sprintf("%s (not shown empty: %s)", db, strs.Truncate(err.Error(), 160)))
			continue
		}
		checked = append(checked, db)
	}
	if len(held) > 0 {
		return "", "it also holds " + strings.Join(held, ", ") + ", which no copy covers"
	}
	if len(checked) == 0 {
		return "it held no other database", ""
	}
	return fmt.Sprintf("%s shown empty (the %s kind's empty check, on %s as %s)", strings.Join(checked, ", "), it.Name, runner, it.User), ""
}

// runRemoval is a take-down operation on a target whose take-down is a
// removal (a Docker host, Portainer, a bucket website, an eval copy): the
// removal as the operation's authority, asking it again before each effect.
func (s *Service) runRemoval(ctx context.Context, t *tracker, withProject bool) (runEnd, string, map[string]any) {
	in := RemoveDeployment{ProjectID: t.op.ProjectID, Name: t.op.Deployment, withProject: withProject}
	t.begin(ctx, "taking "+t.op.Deployment+" down: its final backup, then what AppTown made for it")
	res, err := s.remove(ctx, in, t.op.Authority, t.allowed, t.stop)
	if err != nil {
		t.fail(ctx, err.Error())
		return OperationFailed, err.Error(), nil
	}
	detail := "taken down"
	if len(res.Deleted) > 0 {
		detail += ": " + strings.Join(res.Deleted, "; ")
	}
	result := map[string]any{"deleted": res.Deleted, "retained": res.Retained}
	if res.FinalBackup != nil {
		detail += fmt.Sprintf("; final backup set %d", res.FinalBackup.ID)
		result["finalBackup"] = res.FinalBackup.ID
	}
	t.end(ctx, detail, 0)
	return OperationDone, detail, result
}
