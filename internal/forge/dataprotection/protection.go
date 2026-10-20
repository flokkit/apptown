package dataprotection

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/forge/dataprotection/internal/protectionsql"
	"flokkit.io/forge/internal/forge/deployment"
	"flokkit.io/forge/internal/forge/operations"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/stamp"
)

// Protection is how a deployment's data is protected, item by item, as the
// records and the provider say now (design/subsystems/deployment.md,
// "Protecting data off-host"): the forge's own copies and how recent they are, the
// provider's own backups, and when a copy was last seen to come back.
// Unknown is said, never shown as none.

// CopyState is how recent an item's forge copies are.
type CopyState string

const (
	// CopyFresh: the newest good copy is within the cadence plus six hours.
	CopyFresh CopyState = "fresh"
	// CopyStale: older than that, or the last copy failed.
	CopyStale CopyState = "stale"
	// CopyUnknown: the last copy was interrupted, or the forge could not
	// ask; shown with the newest good copy's time.
	CopyUnknown CopyState = "unknown"
	// CopyNone: the forge holds no copy of it.
	CopyNone CopyState = "none"
	// CopyDisposable: the app declared it disposable; it starts empty.
	CopyDisposable CopyState = "disposable"
)

// CopyCadence is how often the forge copies production data; a copy is
// fresh within the cadence plus six hours.
const CopyCadence = 24 * time.Hour

// Protection is a deployment's protection.
type Protection struct {
	Deployment string `json:"deployment"`
	DataClass  string `json:"dataClass"`
	// OffHost says the forge keeps this deployment's copies off its host,
	// in the attached backup store. Production data without it reads
	// "not protected off-host".
	OffHost bool `json:"offHost"`
	// KeptOn is where a Docker host's deployment's newest retained backup
	// is: "store", "host", or "" when it has none.
	KeptOn string `json:"keptOn,omitempty"`
	// Unknown says why what the deployment holds could not be read; its
	// protection is then not assessed, which is not the same as none.
	Unknown string `json:"unknown,omitempty"`
	// Note is what a person should know first: no store attached.
	Note string `json:"note,omitempty"`
	// Target is the target the deployment runs on, whose settings name
	// its copy runner.
	Target string `json:"target"`
	// Operations says, for each operation on its data, whether it can start
	// now and when not why, each from its own door's admission check. Nil
	// on a target where the forge takes no copies of its own.
	Operations *DataOperations `json:"operations,omitempty"`
	// TakeDown is what taking it down would do to its data now, in plain
	// words: a final copy, then the cluster deleted; or, with no final copy
	// possible (Operations.FinalCopy), that it is refused unless the person
	// chooses to go without one, keeping the data.
	TakeDown  string                 `json:"takeDown,omitempty"`
	Items     []ItemProtection       `json:"items"`
	Running   *operations.Operation  `json:"running,omitempty"`
	Recent    []operations.Operation `json:"recent"`
	StartFrom *StartedFromCopy       `json:"startFrom,omitempty"`
	// Rehearsal is the last restore rehearsal (design/subsystems/deployment.md,
	// "Restore rehearsals"); nil with RehearsalNote when there was none.
	Rehearsal     *RehearsalRef `json:"rehearsal,omitempty"`
	RehearsalNote string        `json:"rehearsalNote,omitempty"`
	// RestoreTest is the last restore test on the deployment's own platform
	// (design/subsystems/deployment.md, "Restored copies"), shown apart from the
	// rehearsal: a rehearsal never stands in for it. Nil with
	// RestoreTestNote when there was none.
	RestoreTest     *RestoredCopy `json:"restoreTest,omitempty"`
	RestoreTestNote string        `json:"restoreTestNote,omitempty"`
	// RestoredCopies are its restored copies, newest first: those standing
	// and the last few ended.
	RestoredCopies []RestoredCopy `json:"restoredCopies"`
	// Coverage is what this answer speaks for and what it does not. Every
	// copy, check, rehearsal and restore test above is evidence for the
	// assessed items only.
	Coverage Coverage `json:"coverage"`
}

// Coverage is the scope of a deployment's protection answer: each item the
// forge assessed, by name, and each thing it did not, with why. Data the
// app keeps outside its deployment is never assessed: the forge cannot
// tell from an app's settings which outside stores hold its data, and a
// database's fresh copy says nothing of an upload in a bucket. So no
// answer here is a whole application's protection, even when no item of
// its own is known.
type Coverage struct {
	Assessed    []string `json:"assessed"`
	NotAssessed []string `json:"notAssessed"`
}

// OutsideNotAssessed is the limit every protection answer carries. The
// backup store holds the forge's copies; it is not an app's own bucket.
const OutsideNotAssessed = "data the app keeps outside the needs it declares, such as a bucket or a database it reaches with its own settings"

// assess adds an item's row and says whether it was assessed: an item
// whose kind or place could not be read is listed with why, not assessed.
func (p *Protection) assess(row ItemProtection, unresolved string) {
	p.Items = append(p.Items, row)
	label := row.Need
	if row.Volume != "" {
		label += " · volume " + row.Volume
	} else if row.Where != "" {
		label += " · " + row.Where
	}
	if unresolved != "" {
		p.Coverage.NotAssessed = append(p.Coverage.NotAssessed, label+": "+unresolved)
		return
	}
	p.Coverage.Assessed = append(p.Coverage.Assessed, label)
}

// Availability is whether an operation can start now; when it cannot,
// Reason is a stable code (no-copy-runner, no-store, not-reachable) and
// Why the sentence the refusal says.
type Availability struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
	Why       string `json:"why,omitempty"`
}

// DataOperations is each operation on a deployment's data, as its own door
// would admit it now. They share some prerequisites (a store, a copy
// runner) but not all: a check needs a copy to check, a rehearsal runs on
// its own host from a stored copy and needs no copy runner.
type DataOperations struct {
	Copy  Availability `json:"copy"`
	Check Availability `json:"check"`
	// Restore is a restore test and a restored copy to look at: one door.
	Restore   Availability `json:"restore"`
	Rehearsal Availability `json:"rehearsal"`
	// StartFromCopy is whether a deployment on this target can start from
	// a copy (its first release fills its data on the copy runner).
	StartFromCopy Availability `json:"startFromCopy"`
	// FinalCopy is whether a take-down now would take a final copy first.
	FinalCopy Availability `json:"finalCopy"`
}

// availabilityOf is a door's admission answer as a person reads it: the
// refusal's code (or "refused") and its sentence. A check that fails for
// any reason is unavailable, never available.
func availabilityOf(err error) Availability {
	if err == nil {
		return Availability{Available: true}
	}
	code := fault.CodeOf(err)
	if code == "" {
		code = "refused"
	}
	return Availability{Reason: code, Why: fault.Message(err)}
}

// operationsOf asks each operation's own admission check, without its
// authority (the door asks that of whoever presses it) and without
// recording anything. unread is why the deployment's list could not be
// read: the copy, the check and a final copy are then refused with it.
func (s *Service) operationsOf(ctx context.Context, dep *deployment.Deployment, t *deployment.Target, items []backupPlanItem, unread error) *DataOperations {
	_, restore := s.planRestoredCopy(ctx, dep.ProjectID, dep.Name, ids.Nil)
	_, rehearsal := s.planRehearsal(ctx, dep.ProjectID, dep.Name, ids.Nil)
	// A take-down of a deployment whose backups the forge takes itself
	// always takes its final set first.
	final := Availability{Available: true}
	if t.HoldsBackups() != nil {
		final = availabilityOf(s.ready(ctx, t, items))
	}
	ops := &DataOperations{
		Copy: availabilityOf(s.copyAdmission(ctx, t, items)), Check: availabilityOf(s.drillAdmission(ctx, dep, t, items)), Restore: availabilityOf(restore),
		Rehearsal: availabilityOf(rehearsal), StartFromCopy: availabilityOf(deployment.RefusesStartFrom(t)), FinalCopy: final,
	}
	if unread != nil {
		ops.Copy, ops.Check, ops.FinalCopy = availabilityOf(unread), availabilityOf(unread), availabilityOf(unread)
	}
	return ops
}

// takeDownWords is what a take-down would do to the deployment's data now.
func takeDownWords(finalCopy Availability, holdsData bool) string {
	switch {
	case !holdsData:
		return ""
	case finalCopy.Available:
		return "Taking it down deletes the app, takes a final copy of its data to the backup store and checks it comes back, then deletes its database cluster. The copy is kept 30 days."
	default:
		return "A take-down now could take no final copy of its data, so it is refused unless you choose to take it down without one. Its database cluster then stays at the provider, listed under retained resources, until a person deletes or keeps it."
	}
}

// StartedFromCopy names the set a deployment started from, and when its
// data was filled ("" while it waits for its first release).
type StartedFromCopy struct {
	Backup ids.ID     `json:"backup"`
	Filled stamp.Time `json:"filled,omitzero"`
}

// ItemProtection is one data-holding item.
type ItemProtection struct {
	Need string `json:"need"`
	// Volume is the Docker volume the row is for: two of one need are
	// two rows.
	Volume string    `json:"volume,omitempty"`
	Where  string    `json:"where"`
	Copy   CopyState `json:"copy"`
	// CopyNote says why it is stale, unknown, none or not copied.
	CopyNote string         `json:"copyNote,omitempty"`
	LastCopy *CopyRef       `json:"lastCopy,omitempty"`
	Native   *NativeBackups `json:"native,omitempty"`
	Drill    *DrillRef      `json:"drill,omitempty"`
}

// CopyRef is a copy of the item.
type CopyRef struct {
	Backup  ids.ID `json:"backup"`
	TakenAt string `json:"takenAt"`
	Bytes   int64  `json:"bytes"`
	SHA256  string `json:"sha256"`
}

// NativeBackups is the provider's own backups of an item, as the provider says
// now; Unknown says why it could not be asked.
type NativeBackups struct {
	Mechanism string     `json:"mechanism"`
	Count     int        `json:"count"`
	Oldest    stamp.Time `json:"oldest,omitzero"`
	Newest    stamp.Time `json:"newest,omitzero"`
	Unknown   string     `json:"unknown,omitempty"`
}

// DrillRef is the last time an item's copy was filled into a new place and
// checked: "came back on <date> from set <n> into <place> as <user>", or
// that it did not.
type DrillRef struct {
	At     stamp.Time `json:"at"`
	Backup ids.ID     `json:"backup"`
	// Target is the deployment's target. It is not where the check ran: a
	// bucket's copy is filled into a new store on the copy runner, and a
	// database's into a new database on the item's own instance. Place says
	// which.
	Target string `json:"target"`
	// Place is where the copy was filled and checked: "a new database on
	// the deployment's cluster", or "a new <need> store on <runner>".
	Place string `json:"place"`
	// User is the app's database user the copy was filled as; empty for a
	// bucket, whose new store is filled with the check's own key.
	User   string `json:"user"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail,omitempty"`
}

// DeploymentProtection reads a standing deployment's protection now, with
// what it covers: unknown held data leaves the deployment's own data not
// assessed, and data outside the deployment is never assessed.
func (s *Service) DeploymentProtection(ctx context.Context, projectID ids.ID, name string) (*Protection, error) {
	out, err := s.protection(ctx, projectID, name)
	if err != nil {
		return nil, err
	}
	if out.Unknown != "" {
		out.Coverage.NotAssessed = append(out.Coverage.NotAssessed, "the deployment's own data: what it holds is unknown")
	}
	out.Coverage.NotAssessed = append(out.Coverage.NotAssessed, OutsideNotAssessed)
	return out, nil
}

func (s *Service) protection(ctx context.Context, projectID ids.ID, name string) (*Protection, error) {
	dep, t, items, err := s.protectedPlace(ctx, projectID, name)
	var unread error
	switch {
	case errors.Is(err, ErrDataNotKnown) && dep != nil:
		// Not read is not none: the rows are not shown and the page says why.
		unread = err
	case err != nil:
		return nil, err
	}
	out := &Protection{Deployment: dep.Name, DataClass: dep.DataClass, Target: t.Name, Items: []ItemProtection{},
		Coverage: Coverage{Assessed: []string{}, NotAssessed: []string{}}}
	if !dep.StartFrom.IsZero() {
		out.StartFrom = &StartedFromCopy{Backup: dep.StartFrom, Filled: dep.StartedFrom}
	}
	// The operations and drills shown are this deployment's, by its id: a
	// deployment re-created under the name of one taken down does not
	// show the old one's as its own.
	out.Recent = []operations.Operation{}
	if s.ops != nil {
		if out.Running, err = s.ops.LiveOn(ctx, deployment.SubjectOf(dep)); err != nil {
			return nil, err
		}
		if out.Recent, err = s.ops.OnSubject(ctx, deployment.SubjectOf(dep), 5); err != nil {
			return nil, err
		}
	}
	if out.Rehearsal, err = s.lastRehearsalOf(ctx, dep); err != nil {
		return nil, err
	}
	if out.Rehearsal == nil {
		out.RehearsalNote = "never rehearsed"
		if _, err := s.rehearsalHost(ctx); errors.Is(err, ErrNoRehearsalHost) {
			out.RehearsalNote = "never rehearsed: no rehearsal place"
		}
	}
	if out.RestoreTest, err = s.LastRestoreTest(ctx, dep); err != nil {
		return nil, err
	}
	if out.RestoreTest == nil {
		out.RestoreTestNote = "never tested on its platform"
	}
	if out.RestoredCopies, err = s.RestoredCopiesOf(ctx, dep, 5); err != nil {
		return nil, err
	}
	store, err := s.Store(ctx)
	if err != nil {
		return nil, err
	}
	switch {
	case t.HoldsBackups() == nil: // the forge takes its own backups here
		return out, s.dockerProtection(ctx, out, dep, t, store != nil)
	case t.DataAccess() != deployment.DataOnManagedPlatform:
		out.Note = "copies of this deployment's data are on the Backups page"
		out.Coverage.NotAssessed = append(out.Coverage.NotAssessed, "the deployment's own data: AppTown does not read what a deployment on "+t.Kind+" holds")
		return out, nil
	case store == nil && dep.DataClass == "production":
		out.Note = "not protected off-host: no backup store is attached"
	case store == nil:
		out.Note = "no backup store is attached, so AppTown keeps no copies of this data"
	case t.CopyRunner() == "":
		// Operations says why, and what an administrator does about it.
	default:
		out.OffHost = true
	}
	if unread != nil {
		// No copy, check or final copy can start on a list not read: each
		// door says so, and no take-down words are guessed.
		out.Unknown = unread.Error()
		out.Operations = s.operationsOf(ctx, dep, t, nil, unread)
		return out, nil
	}
	out.Operations = s.operationsOf(ctx, dep, t, items, nil)
	// An eval copy's data is synthetic: its take-down takes no final copy
	// and is never refused for it (beginManagedTakeDown).
	purpose, err := s.env.Purpose(ctx, projectID)
	if err != nil {
		return nil, err
	}
	out.TakeDown = takeDownWords(out.Operations.FinalCopy, len(items) > 0 && purpose.KeepsData())
	sets, err := s.backups.ForDeployment(ctx, projectID, name, 50)
	if err != nil {
		return nil, err
	}
	// The last copy is asked of the ledger by the operations' own order,
	// not read from the few shown above: other work on the deployment must
	// not push a failed or interrupted copy out of the reckoning.
	var lastCopy *operations.Operation
	if s.ops != nil {
		if lastCopy, err = s.ops.LatestEnded(ctx, deployment.SubjectOf(dep), OperationCopy); err != nil {
			return nil, err
		}
	}
	md := s.providers.ManagedData(t)
	for _, it := range items {
		drill, err := s.lastDrillOf(ctx, dep.ID, it.Name)
		if err != nil {
			return nil, err
		}
		row := ItemProtection{Need: it.Name, Where: it.where(), Drill: drill}
		if it.Unresolved != "" {
			row.Copy, row.CopyNote = CopyNone, it.Unresolved
			out.assess(row, it.Unresolved)
			continue
		}
		row.LastCopy = newestCopyOf(sets, it)
		row.Copy, row.CopyNote = copyState(row.LastCopy, lastCopy, s.now())
		row.Native = s.nativeOf(ctx, md, it)
		out.assess(row, "")
	}
	return out, nil
}

// lastDrillOf is the item's own last drill on the deployment, or nil when
// it has none: a drill fills each item of a set in turn, and one item's
// result is never another's.
func (s *Service) lastDrillOf(ctx context.Context, deploymentID ids.ID, need string) (*DrillRef, error) {
	d, err := protectionsql.New(s.db.SQL()).LatestDrillOf(ctx, protectionsql.LatestDrillOfParams{DeploymentID: deploymentID, Need: need})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &DrillRef{At: stamp.Of(d.DrilledAt), Backup: d.BackupID, Target: d.Target, Place: d.Place, User: d.AsUser, Passed: d.Passed, Detail: d.Detail}, nil
}

// newestCopyOf is the newest good copy in the store of this item, held to
// the instance and database it certifies.
func newestCopyOf(sets []Backup, it backupPlanItem) *CopyRef {
	for _, b := range sets {
		if b.Store != StoreBackupStore || !b.Status.Holds() {
			continue
		}
		if c := b.GoodCopy("need", it.Name); c != nil && c.Instance == it.Instance && c.Database == it.Database && c.Bucket == it.Bucket {
			return &CopyRef{Backup: b.ID, TakenAt: b.TakenAt, Bytes: c.Bytes, SHA256: c.SHA256}
		}
	}
	return nil
}

// copyState is the rule: fresh within the cadence plus six hours; stale
// when older or when the last copy failed; unknown when the last copy was
// interrupted; none when there is no copy. A failed last copy reads stale
// whatever the times say: an operation's time is the database's clock and
// a copy's the forge's, so ordering one against the other could show a
// later failure as earlier than the good copy before it.
func copyState(last *CopyRef, lastOp *operations.Operation, now time.Time) (CopyState, string) {
	if lastOp != nil && lastOp.State == operations.Unknown {
		return CopyUnknown, "the last copy was interrupted; whether it finished is not known"
	}
	if last == nil {
		if lastOp != nil && lastOp.State == operations.Failed {
			return CopyNone, "the last copy failed: " + lastOp.Detail
		}
		return CopyNone, ""
	}
	if lastOp != nil && lastOp.State == operations.Failed {
		return CopyStale, "the last copy failed: " + lastOp.Detail
	}
	if taken := mustTime(last.TakenAt); now.Sub(taken) > CopyCadence+6*time.Hour {
		return CopyStale, fmt.Sprintf("the newest copy is %s old", now.Sub(taken).Round(time.Hour))
	}
	return CopyFresh, ""
}

func mustTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

// nativeOf asks the provider for its own backups of the item's instance,
// when the kind declares the provider has any. A provider that cannot be
// asked is unknown, with why.
func (s *Service) nativeOf(ctx context.Context, md deploy.ManagedData, it backupPlanItem) *NativeBackups {
	if it.Native == "" || md == nil {
		return nil
	}
	// A deployment's card asks every few seconds; the provider's backups
	// change once a day, and its API has a rate limit.
	if v, ok := s.natives.Load(it.Instance); ok {
		if c := v.(nativeRead); s.now().Sub(c.at) < s.nativeFresh {
			n := c.n
			return &n
		}
	}
	n := &NativeBackups{Mechanism: it.Native}
	defer func() { s.natives.Store(it.Instance, nativeRead{at: s.now(), n: *n}) }()
	backups, err := md.Backups(ctx, it.Instance)
	if err != nil {
		n.Unknown = err.Error()
		return n
	}
	n.Count = len(backups)
	if n.Count > 0 {
		n.Newest, n.Oldest = stamp.Of(backups[0].CreatedAt), stamp.Of(backups[n.Count-1].CreatedAt)
	}
	return n
}

// nativeRead is a provider's answer about its backups and when it was read.
type nativeRead struct {
	at time.Time
	n  NativeBackups
}

// NativeReadFor is how long a provider's answer about its own backups is
// shown before it is asked again.
const NativeReadFor = 5 * time.Minute

// Words for a Docker host's deployment whose backups cannot leave the
// forge's disk, and for the operations only App Platform has so far.
var (
	// ErrOnForgeDisk: with no store attached a Docker host's backups stay
	// on the forge's own disk, which a rehearsal does not read from.
	ErrOnForgeDisk error = &deployment.Unavailable{Reason: "on-apptown-disk", Why: "A rehearsal needs a backup store."}
	// ErrDockerBackupsPage: a Docker host's deployment is copied by its
	// backups, from the Backups page, not by a copy operation.
	ErrDockerBackupsPage error = &deployment.Unavailable{Reason: "backups-page", Why: "A Docker host's deployment is backed up daily and from the Backups page."}
	// ErrDrillAppPlatform: a check fills a new database the provider makes.
	ErrDrillAppPlatform error = &deployment.Unavailable{Reason: "not-on-this-platform", Why: "Checks run on App Platform only; a rehearsal checks a Docker host's copies."}
	// ErrRestoreTestAppPlatform: a restore test runs behind a gate only
	// App Platform places so far.
	ErrRestoreTestAppPlatform error = &deployment.Unavailable{Reason: "not-on-this-platform", Why: "Restore tests run on App Platform only; a rehearsal checks a Docker host's copies."}
)

// OnForgeDiskNote is what a Docker host's deployment says first when no
// store is attached: where its backups are and what that does not cover,
// calmly, since it is a choice an instance may make (snapshots of that
// disk, say).
const OnForgeDiskNote = "Backups are kept on this host only: they do not outlive its disk."

// dockerProtection is a Docker host's deployment's protection: each item
// its backups hold and how recent its newest good copy is, wherever it is
// kept; off-host only when that newest set is in the store; and each
// operation as its own door answers.
func (s *Service) dockerProtection(ctx context.Context, out *Protection, dep *deployment.Deployment, t *deployment.Target, hasStore bool) error {
	p, err := project.ByIDIn(ctx, s.db, dep.ProjectID)
	if err != nil {
		return err
	}
	plan, err := s.heldPlan(ctx, dep.ProjectID, p.Slug, dep.Name)
	switch {
	case errors.Is(err, ErrNothingToBackUp):
		return nil // known to hold no data: nothing to say
	case err != nil:
		out.Unknown = err.Error() // not read, or named and not there: never shown as nothing
		return nil
	}
	sets, err := s.backups.ForDeployment(ctx, dep.ProjectID, dep.Name, 50)
	if err != nil {
		return err
	}
	var newest *Backup
	for i := range sets {
		if sets[i].Status.Holds() {
			newest = &sets[i]
			break
		}
	}
	if newest != nil {
		out.KeptOn = "host"
		if newest.Store == StoreBackupStore {
			out.KeptOn = "store"
		}
	}
	switch {
	case !hasStore:
		out.Note = OnForgeDiskNote
	case newest != nil && newest.Store == StoreBackupStore:
		out.OffHost = true
	case newest != nil:
		out.Note = "The newest backup is still on this host; it moves to the backup store with the nightly sweep."
	}
	now := s.now()
	// Each set's copies are placed on what the deployment holds now, by
	// the volume each was taken from (placeCopies): two volumes of one
	// role never borrow each other's copy.
	copyFor := make([]map[int]*BackupItem, len(sets))
	for k := range sets {
		copyFor[k] = map[int]*BackupItem{}
		into, _ := placeCopies(sets[k].Items, plan)
		for i, j := range into {
			// A restore may put another volume's copy into the one volume of
			// its role; that is not a copy of this volume. Only a copy with
			// no volume recorded is evidence by role.
			if v := copyVolume(sets[k].Items[i]); v != "" && v != plan[j].Volume {
				continue
			}
			copyFor[k][j] = &sets[k].Items[i]
		}
	}
	for j, it := range plan {
		row := ItemProtection{Need: it.Name, Volume: it.Volume, Where: "on " + t.Name}
		if it.Kind == "volume" {
			row.Need = "data folder"
		}
		for k, b := range sets {
			if it.Unresolved != "" {
				break // what it holds is not known, so no copy is evidence for it
			}
			if c := copyFor[k][j]; c != nil && b.Status.Holds() {
				row.LastCopy = &CopyRef{Backup: b.ID, TakenAt: b.TakenAt, Bytes: c.Bytes, SHA256: c.SHA256}
				break
			}
		}
		row.Copy, row.CopyNote = copyState(row.LastCopy, nil, now)
		switch {
		case it.Unresolved != "" && row.LastCopy == nil:
			row.CopyNote = it.Unresolved
		// The newest backup failing after the newest good copy is stale.
		case len(sets) > 0 && row.LastCopy != nil && sets[0].ID.Compare(row.LastCopy.Backup) > 0 && (sets[0].Status == BackupFailed || copyFor[0][j] == nil) && sets[0].Status != BackupPruned:
			row.Copy, row.CopyNote = CopyStale, "the last backup did not copy it: "+sets[0].Error
		}
		out.assess(row, it.Unresolved)
	}
	// Each operation as its own door answers it: the card and the refusal
	// say the same thing.
	ops := s.operationsOf(ctx, dep, t, nil, nil)
	out.Operations = ops
	if out.Rehearsal == nil && !ops.Rehearsal.Available && ops.Rehearsal.Reason == "on-apptown-disk" {
		out.RehearsalNote = "needs a backup store: its backups are on this host"
	}
	if out.RestoreTest == nil {
		out.RestoreTestNote = "App Platform only; a rehearsal checks a Docker host's copies"
	}
	return nil
}
