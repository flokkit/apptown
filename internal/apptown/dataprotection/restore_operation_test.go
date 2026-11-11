package dataprotection

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/jobs"
	"github.com/flokkit/apptown/internal/apptown/operations"
	"github.com/flokkit/apptown/internal/containerresult/containerresulttest"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/docker/dockerfake"
	"github.com/flokkit/apptown/internal/ids"
)

// A restore of a set is an operation (soak finding 56): admitted by its
// door, run once by the ledger's worker, written as it goes, and settled
// unknown with what it had confirmed when AppTown stops in the middle,
// naming what it may have left stopped and starting nothing on a guess.

// restoreThroughOperation asks for a restore at its door, runs the
// operation as the worker does, and returns it ended, with its receipt.
func restoreThroughOperation(t *testing.T, s *both, pid ids.ID, name string, backupID ids.ID) (*operations.Operation, *RestoreResult) {
	t.Helper()
	withJobs(t, s)
	op, err := s.RequestRestore(context.Background(), pid, name, backupID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Service.ops.Run(context.Background(), op.ID); err != nil {
		t.Fatal(err)
	}
	return endedRestore(t, s, op.ID)
}

// withJobs gives the world's database the job queue's tables, which an
// admission writes its job into.
func withJobs(t *testing.T, s *both) {
	t.Helper()
	if err := jobs.Migrate(context.Background(), s.Service.db.Pool()); err != nil {
		t.Fatal(err)
	}
}

// endedRestore reads a restore operation and its receipt (nil when it has
// none).
func endedRestore(t *testing.T, s *both, id ids.ID) (*operations.Operation, *RestoreResult) {
	t.Helper()
	got, err := s.Service.ops.ByID(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	raw, ok := got.Result["restore"]
	if !ok {
		return got, nil
	}
	var res RestoreResult
	if err := decode(raw, &res); err != nil {
		t.Fatal(err)
	}
	return got, &res
}

// restartAppTown runs the ledger's restart repair, as an exclusive start
// does after an AppTown stopped.
func restartAppTown(t *testing.T, s *both, db *database.DB) operations.RestartRepair {
	t.Helper()
	r, err := database.Transact(context.Background(), db, func(tx *database.Tx) (operations.RestartRepair, error) {
		return s.Service.ops.RepairInterruptedIn(context.Background(), tx)
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// restoredReceipts are the project's restored receipts.
func restoredReceipts(t *testing.T, db *database.DB, pid ids.ID) []string {
	t.Helper()
	lines, err := audit.Recent(context.Background(), db, audit.Filter{ProjectID: pid, KindPrefix: "restored"})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, l := range lines {
		out = append(out, string(l.Payload))
	}
	return out
}

// restoreDaemon answers a world's copies and restores: each copy writes
// its file, each restore reads it; every other command is recorded in
// order. hold, when it returns a channel for a command, blocks that
// command until the channel closes (AppTown stops while it runs).
type restoreDaemon struct {
	mu   sync.Mutex
	ran  []string
	hold func(line string, restore bool) chan struct{}
	// fail fails every command once set: AppTown's own context ended.
	fail bool
}

func (d *restoreDaemon) answer(req docker.Command) ([]byte, error) {
	line := strings.Join(req.Args, " ")
	d.mu.Lock()
	d.ran = append(d.ran, line)
	hold, fail := d.hold, d.fail
	d.mu.Unlock()
	if fail {
		return nil, context.Canceled
	}
	if hold != nil {
		if ch := hold(line, req.Stdin != nil); ch != nil {
			<-ch
		}
	}
	if req.Stdout != nil {
		_, err := containerresulttest.WriteString(req.Stdout, "copy of "+filepath.Base(containerresulttest.Destination(req.Stdout).(*os.File).Name()))
		return nil, err
	}
	if req.Stdin != nil {
		_, err := io.ReadAll(req.Stdin)
		return nil, err
	}
	return nil, nil
}

func (d *restoreDaemon) lines() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.ran...)
}

// restoreWorld is a Docker host's main, running, with a set to restore.
func restoreWorld(t *testing.T, slug string) (*both, *database.DB, *dockerfake.Daemon, *restoreDaemon, ids.ID, *Backup) {
	t.Helper()
	s, db, client, _, pid := backupWorld(t, slug, answeringPort(t))
	withJobs(t, s)
	d := &restoreDaemon{}
	setBackupDockerCommand(client, d.answer)
	client.SetRunning(deployedNames(t, s, pid, "main", slug).App(), true)
	b, err := s.TakeBackup(context.Background(), pid, "main", "bls")
	if err != nil || b.Status != BackupOK {
		t.Fatalf("the set: %+v %v", b, err)
	}
	return s, db, client, d, pid, b
}

// The door admits the restore and does nothing else: no safety set, no
// stop. While it lives a second restore of the deployment is refused,
// naming it; what would refuse it before any change refuses at the door,
// recording nothing.
//
// Kills: a restore run in the request (nothing on the record, nothing a
// deploy's guard can see); a second restore admitted beside the first.
func TestARestoreIsAdmittedAsAnOperationAndChangesNothingUntilItRuns(t *testing.T) {
	t.Parallel()
	s, db, _, d, pid, b := restoreWorld(t, "ra")
	ctx := context.Background()
	before := len(d.lines())
	sets := backupsWhere(t, db, func(x ProjectBackup) bool { return x.ProjectID == pid })
	op, err := s.RequestRestore(ctx, pid, "main", b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if op.Kind != operationRestoreSet || op.State != operations.Queued {
		t.Fatalf("the restore's operation: %s %s", op.Kind, op.State)
	}
	if ran := d.lines()[before:]; slices.ContainsFunc(ran, func(l string) bool { return strings.HasPrefix(l, "stop ") }) {
		t.Fatalf("the door stopped something: %v", ran)
	}
	wantN(t, "sets after the door", backupsWhere(t, db, func(x ProjectBackup) bool { return x.ProjectID == pid }), len(sets))
	var held *operations.HeldError
	if _, err := s.RequestRestore(ctx, pid, "main", b.ID); !errors.As(err, &held) || held.Holder.ID != op.ID {
		t.Fatalf("a second restore while the first lives: %v", err)
	}
	for what, ask := range map[string]func() error{
		"no such set":        func() error { _, err := s.RequestRestore(ctx, pid, "main", ids.New(time.Now())); return err },
		"no such deployment": func() error { _, err := s.RequestRestore(ctx, pid, "nowhere", b.ID); return err },
	} {
		if err := ask(); err == nil {
			t.Fatalf("%s was admitted", what)
		}
	}
	dep, err := s.Deployments.ByName(ctx, pid, "main")
	if err != nil {
		t.Fatal(err)
	}
	if ops, err := s.Service.ops.OnSubject(ctx, deployment.SubjectOf(dep), 10); err != nil || len(ops) != 1 {
		t.Fatalf("the refusals recorded operations: %d %v", len(ops), err)
	}
}

// An in-place restore holds the whole deployment while it lives: a
// person's restart and any attempt (a release, a repair) are refused, with
// nothing asked of the host.
//
// Kills: a restore holding only the deployment's data, which runtime acts
// and attempt admission do not meet.
func TestARestoreRefusesEveryWriterStartWhileItLives(t *testing.T) {
	t.Parallel()
	s, _, client, _, pid, b := restoreWorld(t, "rw")
	ctx := t.Context()
	if _, err := s.RequestRestore(ctx, pid, "main", b.ID); err != nil {
		t.Fatal(err)
	}
	dep, err := s.Deployments.ByName(ctx, pid, "main")
	if err != nil {
		t.Fatal(err)
	}
	var held *operations.HeldError
	if _, err := s.ControlRuntime(ctx, pid, "main", "restart"); !errors.As(err, &held) || len(client.Matching("restart")) != 0 {
		t.Fatalf("a restart during the restore: %v, host asked %v", err, client.Matching("restart"))
	}
	if _, _, err := s.Outcomes.Admit(ctx, deployment.Start{DeploymentID: dep.ID, TargetID: dep.TargetID, Key: "during-restore", Inputs: deployment.Inputs{Revision: dep.ReleaseSHA}}); !errors.As(err, &held) {
		t.Fatalf("an attempt during the restore: %v", err)
	}
}

// A restore is admitted only while nothing may start the deployment's
// writers: a release in flight refuses it, except the rollback whose own
// restore it is.
//
// Kills: a restore admitted beside a release that will start writers; a
// rollback refused by its own release.
func TestARestoreWaitsForAReleaseInFlightButARollbackRunsItsOwn(t *testing.T) {
	t.Parallel()
	s, _, _, _, pid, _ := restoreWorld(t, "rq")
	ctx := t.Context()
	dep, err := s.Deployments.ByName(ctx, pid, "main")
	if err != nil {
		t.Fatal(err)
	}
	src := s.sources.(*releasedContracts)
	src.mu.Lock()
	yaml := src.by[dep.ReleaseSHA]
	src.mu.Unlock()
	bundle, err := s.Bundles.Cut(ctx, pid, dep.ReleaseSHA, map[string]string{"web": "image-one"}, nil, []byte(yaml), deployment.BundleOrigin{})
	if err != nil {
		t.Fatal(err)
	}
	rel, err := s.releases.Start(ctx, deployment.ReleaseDraft{Source: deployment.ReleaseFromBundle, ProjectID: pid, DeploymentID: dep.ID, Deployment: "main", BundleID: bundle, Revision: dep.ReleaseSHA, Actor: "bls"}, "released")
	if err != nil {
		t.Fatal(err)
	}
	attempt, _, err := s.Outcomes.Admit(ctx, deployment.Start{DeploymentID: dep.ID, TargetID: dep.TargetID, Key: "released", Inputs: deployment.Inputs{ReleaseID: rel.ID, BundleID: bundle, Revision: dep.ReleaseSHA}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Outcomes.Complete(ctx, deployment.Completion{Attempt: attempt, Observed: deploy.Observation{Generation: attempt.Generation, Rollout: "active", Health: "healthy"}}); err != nil {
		t.Fatal(err)
	}
	witness, err := s.BeforeRelease(ctx, pid, "main", true)
	if err != nil || witness.copy == nil {
		t.Fatalf("the paired set: %+v %v", witness, err)
	}
	rollback, err := s.releases.Start(ctx, deployment.ReleaseDraft{Source: deployment.ReleaseFromBundle, ProjectID: pid, DeploymentID: dep.ID, Deployment: "main", BundleID: bundle, Revision: dep.ReleaseSHA, Actor: "bls"}, "rollback")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RequestRestore(ctx, pid, "main", witness.copy.ID); !errors.Is(err, deployment.ErrRemovalBusy) {
		t.Fatalf("a restore beside a release in flight: %v", err)
	}
	principal, err := s.Service.requireAuthority(ctx, access.CapDeploymentDeploy, pid, operationRestoreSet)
	if err != nil {
		t.Fatal(err)
	}
	s.Service.releaseAuthority = approvedRelease{}
	if err := s.RestoreForRelease(ctx, pid, "main", witness.copy.ID, rollback.ID, principal); err != nil {
		t.Fatalf("the rollback's own restore: %v", err)
	}
}

// approvedRelease is Deployment's release check answering that the release
// is still authorised and approved.
type approvedRelease struct{}

func (approvedRelease) CheckRelease(context.Context, ids.ID) error { return nil }

// Run, the restore writes each step and fact as it goes and ends with its
// receipt, which its settlement writes once.
//
// Kills: a receipt written only by a request that may not finish; steps
// and facts not on the record as they happen.
func TestARestoreOperationRecordsEachStepAndItsReceipt(t *testing.T) {
	t.Parallel()
	s, db, _, _, pid, b := restoreWorld(t, "rb")
	op, res := restoreThroughOperation(t, s, pid, "main", b.ID)
	if op.State != operations.Succeeded || op.Verification != operations.NotVerified || res == nil || res.Status != restoreNotVerified {
		t.Fatalf("the restore: %s %s %q %+v", op.State, op.Verification, op.Detail, res)
	}
	var steps []string
	for _, st := range op.Steps {
		steps = append(steps, st.Name)
	}
	app := deployedNames(t, s, pid, "main", "rb").App()
	want := []string{"taking a safety set of what is there now", "stopping the app, so nothing writes while the data goes back", "restoring " + res.Items[0].Name, "restoring postgres", "starting the app again", "waiting for the app to answer"}
	if !slices.Equal(steps, want) {
		t.Fatalf("the steps:\n%s\nwant\n%s", strings.Join(steps, "\n"), strings.Join(want, "\n"))
	}
	if got := stringsOf(op.Result["stopped"]); !slices.Equal(got, []string{app}) || !slices.Equal(stringsOf(op.Result["started"]), []string{app}) || inputID(op.Result, "safetyBackup") != res.SafetyBackup {
		t.Fatalf("the facts: %v", op.Result)
	}
	if !strings.Contains(op.Detail, "postgres verified") || !strings.Contains(op.Detail, "The app is started again.") || strings.Contains(op.Detail, "may be stopped") {
		t.Fatalf("the detail: %q", op.Detail)
	}
	receipts := restoredReceipts(t, db, pid)
	if len(receipts) != 1 || !strings.Contains(receipts[0], `"status":"restored, not verified"`) || !strings.Contains(receipts[0], op.ID.String()) {
		t.Fatalf("the receipt: %v", receipts)
	}
}

// AppTown dies in the middle of a restore: its writer on the host is
// still at work and the old executor never settles. The restart settles
// it unknown with the item it had restored, names what it may have left
// stopped, writes the unknown receipt once, and starts nothing.
//
// Kills: no record of a cut-off restore (soak finding 56); a stop not on
// the record before it is issued; an interruption that says nothing of
// the app; starting writers while the host's restore may still write.
func TestAForgeThatStopsInARestoreLeavesItUnknownAndStartsNothing(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		// cut says where AppTown dies: the command that never returns.
		cut func(line string, restore bool) bool
		// left is what the detail names as possibly stopped; done the item
		// it confirmed.
		left, done string
	}{
		{name: "in the second item", cut: func(line string, restore bool) bool { return restore && strings.Contains(line, "pg_restore -d") },
			left: "APP may be stopped", done: "Items restored before it stopped: DATA copied."},
		{name: "in the stop", cut: func(line string, _ bool) bool { return line == "stop APP" },
			left: "APP may be stopped"},
		{name: "in the safety set", cut: func(line string, restore bool) bool { return !restore && strings.Contains(line, "pg_dump") },
			left: "No item was confirmed restored. It stopped no container."},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			s, db, _, d, pid, b := restoreWorld(t, "rc")
			// The table names the app and its data volume as APP and DATA.
			names := deployedNames(t, s, pid, "main", "rc")
			c.left = strings.NewReplacer("APP", names.App(), "DATA", names.DataVolume()).Replace(c.left)
			c.done = strings.NewReplacer("APP", names.App(), "DATA", names.DataVolume()).Replace(c.done)
			reached, never := make(chan struct{}), make(chan struct{})
			var once sync.Once
			d.mu.Lock()
			d.hold = func(line string, restore bool) chan struct{} {
				if c.cut(strings.ReplaceAll(line, names.App(), "APP"), restore) {
					once.Do(func() { close(reached) })
					return never
				}
				return nil
			}
			d.mu.Unlock()
			op, err := s.RequestRestore(context.Background(), pid, "main", b.ID)
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			go func() { defer close(done); _, _ = s.Service.ops.Run(context.Background(), op.ID) }()
			// The old daemon's writer is let go only after the test, so it
			// never settles what the restart settles.
			t.Cleanup(func() { close(never); <-done })
			<-reached
			running, _ := endedRestore(t, s, op.ID)
			if running.State != operations.Running {
				t.Fatalf("before the restart the restore is %s", running.State)
			}
			before := len(d.lines())
			repaired := make(chan operations.RestartRepair, 1)
			go func() { repaired <- restartAppTown(t, s, db) }()
			var r operations.RestartRepair
			select {
			case r = <-repaired:
			case <-time.After(30 * time.Second):
				t.Fatal("the restart waited on the host: it acted on the deployment's containers")
			}
			if r.Settled != 1 {
				t.Fatalf("the restart settled %d", r.Settled)
			}
			got, _ := endedRestore(t, s, op.ID)
			if got.State != operations.Unknown || !strings.Contains(got.Detail, c.left) || !strings.Contains(got.Detail, c.done) || strings.Contains(got.Detail, "not serving") {
				t.Fatalf("the cut-off restore: %s %q", got.State, got.Detail)
			}
			if ran := d.lines()[before:]; len(ran) != 0 {
				t.Fatalf("the restart ran on the host: %v", ran)
			}
			receipts := restoredReceipts(t, db, pid)
			stopped := c.done != "" || strings.Contains(c.left, "may be stopped")
			if stopped && (len(receipts) != 1 || !strings.Contains(receipts[0], `"status":"unknown"`) || !strings.Contains(receipts[0], `"mayBeStopped":["`+names.App()+`"]`)) {
				t.Fatalf("the unknown receipt: %v", receipts)
			}
			if !stopped && len(receipts) != 0 {
				t.Fatalf("a restore that changed nothing wrote a receipt: %v", receipts)
			}
			if again := restartAppTown(t, s, db); again.Settled != 0 || len(restoredReceipts(t, db, pid)) != len(receipts) {
				t.Fatalf("a second restart settled it again: %+v", again)
			}
		})
	}
}

// An AppTown that shuts down cancels the restore's context: whatever it
// could not finish, it ends unknown, and what it stopped and could not
// start again is named as possibly stopped, and its receipt says unknown
// (code review R2).
//
// Kills: a shutdown read as a plain failure, or one that says nothing of
// the app it left; a receipt that reports the data's outcome as known.
func TestARestoreCutByAShutdownSaysWhatMayBeStopped(t *testing.T) {
	t.Parallel()
	s, db, _, d, pid, b := restoreWorld(t, "rd")
	ctx, cancel := context.WithCancel(context.Background())
	d.mu.Lock()
	d.hold = func(line string, restore bool) chan struct{} {
		if restore && strings.Contains(line, "pg_restore -d") {
			// The shutdown: the context ends and the daemon answers no more.
			cancel()
			d.fail = true
		}
		return nil
	}
	d.mu.Unlock()
	op, err := s.RequestRestore(context.Background(), pid, "main", b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Service.ops.Run(ctx, op.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := endedRestore(t, s, op.ID)
	if got.State != operations.Unknown || !strings.Contains(got.Detail, deployedNames(t, s, pid, "main", "rd").App()+" may be stopped") {
		t.Fatalf("a restore cut by a shutdown: %s %q", got.State, got.Detail)
	}
	receipts := restoredReceipts(t, db, pid)
	if len(receipts) != 1 || !strings.Contains(receipts[0], `"status":"unknown"`) {
		t.Fatalf("a shutdown writes an unknown restored receipt: %v", receipts)
	}
}

// The admitting credential is asked again before the restore's effects:
// withdrawn before the stop, nothing changes; withdrawn after the first
// item, no further item is written and the app is started again.
//
// Kills: a restore that checks its authority only at the door.
func TestARestoreAsksAgainBeforeItsEffects(t *testing.T) {
	t.Parallel()
	t.Run("before the stop", func(t *testing.T) {
		t.Parallel()
		s, db, _, d, pid, b := restoreWorld(t, "re")
		sets := len(backupsWhere(t, db, func(x ProjectBackup) bool { return x.ProjectID == pid }))
		op, err := s.RequestRestore(context.Background(), pid, "main", b.ID)
		if err != nil {
			t.Fatal(err)
		}
		before := len(d.lines())
		s.Service.authority.(*grants).revoke("token:bls")
		if _, err := s.Service.ops.Run(context.Background(), op.ID); err != nil {
			t.Fatal(err)
		}
		got, _ := endedRestore(t, s, op.ID)
		if got.State != operations.Failed || !strings.Contains(got.Detail, "authority withdrawn") || !strings.Contains(got.Detail, "nothing was restored") {
			t.Fatalf("withdrawn before the stop: %s %q", got.State, got.Detail)
		}
		if ran := d.lines()[before:]; slices.ContainsFunc(ran, func(l string) bool { return strings.HasPrefix(l, "stop ") }) {
			t.Fatalf("it stopped the app: %v", ran)
		}
		wantN(t, "sets", backupsWhere(t, db, func(x ProjectBackup) bool { return x.ProjectID == pid }), sets)
	})
	t.Run("between items", func(t *testing.T) {
		t.Parallel()
		s, _, _, d, pid, b := restoreWorld(t, "rf")
		d.mu.Lock()
		d.hold = func(line string, restore bool) chan struct{} {
			if restore && strings.Contains(line, "tar xz") {
				s.Service.authority.(*grants).revoke("token:bls")
			}
			return nil
		}
		d.mu.Unlock()
		op, res := restoreThroughOperation(t, s, pid, "main", b.ID)
		if op.State != operations.Failed || !strings.Contains(op.Detail, "authority withdrawn") || res == nil {
			t.Fatalf("withdrawn between items: %s %q", op.State, op.Detail)
		}
		if slices.ContainsFunc(d.lines(), func(l string) bool { return strings.Contains(l, "pg_restore -d") }) {
			t.Fatal("an item was written after its authority was withdrawn")
		}
		if !slices.Contains(d.lines(), "start "+deployedNames(t, s, pid, "main", "rf").App()) || strings.Contains(op.Detail, "may be stopped") {
			t.Fatalf("the app is started again: %q %v", op.Detail, d.lines())
		}
	})
}

// An app already stopped is not stopped again, so nothing is recorded as
// stopped; a restore of it cut off in its second item still says what it
// had restored, and does not say it restored nothing (code review R3).
//
// Kills: inferring "no item was restored" from no container stopped.
func TestAnInterruptedRestoreOfAStoppedAppKeepsItsConfirmedItems(t *testing.T) {
	t.Parallel()
	words := restoreInterrupted(operations.Operation{Result: map[string]any{
		"items": []RestoredItem{{Kind: "volume", Name: "app-data", Outcome: restoreCopied}},
	}})
	if !strings.Contains(words, "app-data copied") || strings.Contains(words, "No item was confirmed restored") || !strings.Contains(words, "It stopped no container.") {
		t.Fatalf("an interrupted restore must keep its confirmed item: %q", words)
	}
}

// A rollback restore never starts the rejected code on recovered data. The
// rejected app corrupts a sentinel on every start, so a restart cannot pass
// unnoticed even when the final app would later start successfully.
func TestRollbackRestoreLeavesTheSentinelAndAppStopped(t *testing.T) {
	t.Parallel()
	s, db, client, daemon, pid, b := restoreWorld(t, "rollback-sentinel")
	app := deployedNames(t, s, pid, "main", "rollback-sentinel").App()
	sentinel, starts := "current", 0
	setBackupDockerCommand(client, func(req docker.Command) ([]byte, error) {
		line := strings.Join(req.Args, " ")
		if req.Stdin != nil && strings.Contains(line, "pg_restore") {
			sentinel = "before"
		}
		if strings.HasPrefix(line, "start ") && strings.Contains(line, app) {
			starts++
			sentinel = "corrupted by rejected code"
		}
		return daemon.answer(req)
	})
	dep, err := s.Deployments.ByName(t.Context(), pid, "main")
	if err != nil {
		t.Fatal(err)
	}
	principal, err := s.Service.requireAuthority(t.Context(), access.CapDeploymentDeploy, pid, operationRestoreSet)
	if err != nil {
		t.Fatal(err)
	}
	op, err := s.Service.admit(t.Context(), dep, operationRestoreSet, principal, []string{deployment.DataHold(dep)}, map[string]any{"backup": b.ID, "keepStopped": true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Service.ops.Run(t.Context(), op.ID); err != nil {
		t.Fatal(err)
	}
	ended, res := endedRestore(t, s, op.ID)
	if ended.State != operations.Succeeded || res == nil || !res.LeftStopped || res.Started == nil || *res.Started || starts != 0 || sentinel != "before" {
		t.Fatalf("rollback restore: %+v %+v starts=%d sentinel=%q", ended, res, starts, sentinel)
	}
	if (docker.Scope{Client: client}).Running(t.Context(), app) || !strings.Contains(ended.Detail, "stays stopped") || strings.Contains(ended.Detail, "may be stopped") {
		t.Fatalf("stopped outcome: %s", ended.Detail)
	}
	if len(restoredReceipts(t, db, pid)) != 1 {
		t.Fatal("missing restore receipt")
	}
}

// Backups carry only recorded running-release evidence. Missing historical
// evidence, stopped apps and failed or in-flight replacements cannot establish
// a rollback pair. Ordinary Restore still accepts copies without that evidence.
func TestBackupsRecordTheRunningReleaseAndPairOnlyRecordedEvidence(t *testing.T) {
	t.Parallel()
	s, _, client, _, pid, historical := restoreWorld(t, "backup-release")
	dep, err := s.Deployments.ByName(t.Context(), pid, "main")
	if err != nil {
		t.Fatal(err)
	}
	src := s.sources.(*releasedContracts)
	src.mu.Lock()
	yaml := src.by[dep.ReleaseSHA]
	src.mu.Unlock()
	bundle, err := s.Bundles.Cut(t.Context(), pid, dep.ReleaseSHA, map[string]string{"web": "image-one"}, nil, []byte(yaml), deployment.BundleOrigin{})
	if err != nil {
		t.Fatal(err)
	}
	rel, err := s.releases.Start(t.Context(), deployment.ReleaseDraft{Source: deployment.ReleaseFromBundle, ProjectID: pid, DeploymentID: dep.ID, Deployment: "main", BundleID: bundle, Revision: dep.ReleaseSHA, Actor: "bls"}, "release for backup")
	if err != nil {
		t.Fatal(err)
	}
	attempt, _, err := s.Outcomes.Admit(t.Context(), deployment.Start{DeploymentID: dep.ID, TargetID: dep.TargetID, Key: "recorded-release", Inputs: deployment.Inputs{ReleaseID: rel.ID, BundleID: bundle, Revision: dep.ReleaseSHA}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Outcomes.Complete(t.Context(), deployment.Completion{Attempt: attempt, Observed: deploy.Observation{Generation: attempt.Generation, Rollout: "active", Health: "healthy"}}); err != nil {
		t.Fatal(err)
	}
	witness, err := s.BeforeRelease(t.Context(), pid, "main", true)
	if err != nil || witness.copy == nil || witness.copy.RunningReleaseID != rel.ID {
		t.Fatalf("backup release: %+v %v", witness, err)
	}
	stored, err := s.backups.inProject(t.Context(), pid, witness.copy.ID)
	if err != nil || stored.RunningReleaseID != rel.ID {
		t.Fatalf("stored backup: %+v %v", stored, err)
	}
	number, err := s.ReleaseBefore(t.Context(), pid, "main", stored.ID)
	if err != nil || number != rel.Number {
		t.Fatalf("recorded pair: %d %v", number, err)
	}
	if _, err := s.ReleaseBefore(t.Context(), pid, "main", historical.ID); err == nil {
		t.Fatal("historical backup inferred a pair")
	}
	// A later boot can start replacement code and then fail its readiness
	// probe. Failure preserves the previous observed generation, although
	// the container name now runs the replacement. A running bit cannot
	// establish which successful release produced the data being copied.
	replacement, err := s.Bundles.Cut(t.Context(), pid, dep.ReleaseSHA, map[string]string{"web": "image-two"}, nil, []byte(yaml), deployment.BundleOrigin{})
	if err != nil {
		t.Fatal(err)
	}
	failedRelease, err := s.releases.Start(t.Context(), deployment.ReleaseDraft{Source: deployment.ReleaseFromBundle, ProjectID: pid, DeploymentID: dep.ID, Deployment: "main", BundleID: replacement, Revision: dep.ReleaseSHA, Actor: "bls"}, "replacement release")
	if err != nil {
		t.Fatal(err)
	}
	next, _, err := s.Outcomes.Admit(t.Context(), deployment.Start{DeploymentID: dep.ID, TargetID: dep.TargetID, Key: "failed-replacement", Inputs: deployment.Inputs{ReleaseID: failedRelease.ID, BundleID: replacement, Revision: dep.ReleaseSHA}})
	if err != nil {
		t.Fatal(err)
	}
	dk := docker.Scope{Client: client}
	app := deployedNames(t, s, pid, "main", "backup-release").App()
	if _, err := dk.Run(t.Context(), "rm", "-f", app); err != nil {
		t.Fatal(err)
	}
	if _, err := dk.Run(t.Context(), "run", "-d", "--name", app, "image-two"); err != nil {
		t.Fatal(err)
	}
	inFlight, err := s.BeforeRelease(t.Context(), pid, "main", true)
	if err != nil || inFlight.copy == nil || !inFlight.copy.RunningReleaseID.IsZero() {
		t.Fatalf("in-flight replacement claimed a running release: %+v %v", inFlight, err)
	}
	if _, err := s.Outcomes.Fail(t.Context(), next, errors.New("replacement started but failed its readiness probe")); err != nil {
		t.Fatal(err)
	}
	unproven, err := s.BeforeRelease(t.Context(), pid, "main", true)
	if err != nil {
		t.Fatal(err)
	}
	if !unproven.copy.RunningReleaseID.IsZero() {
		t.Fatalf("backup after failed replacement claims old successful release %s; running code is not established", unproven.copy.RunningReleaseID)
	}
	stored, err = s.backups.inProject(t.Context(), pid, unproven.copy.ID)
	if err != nil || !stored.RunningReleaseID.IsZero() {
		t.Fatalf("stored unproven backup: %+v %v", stored, err)
	}
	if _, err := s.ReleaseBefore(t.Context(), pid, "main", stored.ID); err == nil {
		t.Fatal("backup after failed replacement offered a rollback pair")
	}
	ended, res := restoreThroughOperation(t, s, pid, "main", stored.ID)
	if ended.State != operations.Succeeded || res == nil || res.Started == nil || !*res.Started {
		t.Fatalf("ordinary restore without running-release evidence: %+v %+v", ended, res)
	}
	client.SetRunning(deployedNames(t, s, pid, "main", "backup-release").App(), false)
	stopped, err := s.TakeBackup(t.Context(), pid, "main", "bls")
	if err != nil || !stopped.RunningReleaseID.IsZero() {
		t.Fatalf("a stopped app claimed a running release: %+v %v", stopped, err)
	}
	// A successful repair of the serving attempt settles it again: backups
	// pair with the release it still serves.
	current, err := s.Deployments.ByName(t.Context(), pid, "main")
	if err != nil {
		t.Fatal(err)
	}
	inputs, err := s.Outcomes.InputsFor(t.Context(), deployment.Attempt{ID: current.ServingAttemptID, DeploymentID: current.ID, Generation: current.ObservedGeneration})
	if err != nil {
		t.Fatal(err)
	}
	inputs.ReleaseID, inputs.Rebuild = ids.Nil, true
	repair, _, err := s.Outcomes.Admit(t.Context(), deployment.Start{DeploymentID: current.ID, TargetID: current.TargetID, Key: "repair", Inputs: inputs, Repairs: current.ServingAttemptID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Outcomes.Complete(t.Context(), deployment.Completion{Attempt: repair, Observed: deploy.Observation{Generation: repair.Generation, Rollout: "active", Health: "healthy"}}); err != nil {
		t.Fatal(err)
	}
	client.SetRunning(deployedNames(t, s, pid, "main", "backup-release").App(), true)
	paired, err := s.TakeBackup(t.Context(), pid, "main", "bls")
	if err != nil || paired.RunningReleaseID != rel.ID {
		t.Fatalf("a backup after a successful repair: %+v %v", paired, err)
	}
}
