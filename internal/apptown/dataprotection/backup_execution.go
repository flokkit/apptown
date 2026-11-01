package dataprotection

// Backup execution derives copies from the current contract and dependency
// catalog. Deployment owns both the ledger and the configured target runtime;
// HTTP and scheduled jobs invoke the same commands. Restore takes a safety
// copy before stopping the app and holds the backup-file lock throughout.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/dependency"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/s3bucket"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/strs"
)

// backupPlanItem is one data item a deployment holds, and how it is
// copied, before it is: a Docker volume, copied on its host, or a managed
// instance's database, copied from the target's copy runner.
type backupPlanItem struct {
	Kind, Name, File, How, Restore string
	Volume                         string      // the volume this item copies ("" when its data is in none AppTown can name)
	Listed                         bool        // the volume is on the deployment's list: a take-down deletes it
	Step                           docker.Step // the command whose stdout is the copy (a file copy)
	Container                      string      // for a need: where the restore runs
	Copy                           CopyMethod
	RestoreStopped                 bool   // the need's restore runs with its container stopped
	Verify                         string // the need's check after a restore
	Caveat                         string // what the need's copy does not hold, as its kind declares
	// Declared is the need's kind when its copy is the kind's declared
	// copy (copy.export, run in Container with the connection it serves).
	// Format is set only for a copy that fills a new place: the item is
	// then the same kind of copy a managed database's is, and a rehearsal
	// can fill a new place from it.
	Declared *dependency.Kind
	Format   string
	// Unresolved says why the item cannot be copied (its kind cannot be
	// read, nothing mounts it, its record names no database): it is on the
	// plan so the set records it as not copied.
	Unresolved string
	// For a managed instance: the provider's id and name for it, the
	// database and user the copy is taken as, the need's version (for the
	// client image) and the provider's own protection the kind declares.
	Instance, Cluster, Database, User, Version, Native string
	// Bucket is a deployment's object storage bucket at the provider, from
	// its owned list (never a managed instance): copied as the deployment's
	// own key for it.
	Bucket string
}

// where is a managed item's place in words.
func (it backupPlanItem) where() string {
	if it.Bucket != "" {
		return "bucket " + it.Bucket
	}
	return fmt.Sprintf("database %s on %s", it.Database, it.Cluster)
}

// copyOf is the kind's copy of a managed item that can be copied.
func (it backupPlanItem) copyOf() *dependency.Copy { return it.Declared.Copy }

// unresolvedNeed is why a declared need cannot be copied or restored.
func unresolvedNeed(need string, err error) string {
	if errors.Is(err, dependency.ErrUnknown) {
		return fmt.Sprintf("the need %s is not in the dependency catalog, so whether it keeps data is not known; its data, if any, is not in this set", need)
	}
	return fmt.Sprintf("the dependency kind of %s cannot be read (%v); its data is not in this set", need, err)
}

// needItem is how a need's data is copied from its container and put
// back, as its kind's copy declares: by its export (restore in place,
// verify), run in that container with the connection it was started with;
// or, for a copy declared with no export, as a tar of its data directory
// taken while it runs, which may be inconsistent and whose restore nothing
// verifies. A kind that declares no copy is not copied: the set says so.
func needItem(d *dependency.Kind, need, container string) backupPlanItem {
	it := backupPlanItem{Kind: "need", Name: need, Container: container}
	cp := d.Copy
	if cp == nil {
		it.File, it.Unresolved = need+".uncopied", fmt.Sprintf("not copied: the %s kind declares no copy (copy: in its definition says how its data is copied)", need)
		return it
	}
	it.Caveat = cp.Caveat
	if cp.Export != "" {
		where := "in " + container
		if cp.InClient() {
			where = "in " + clientImage(cp, "") + " beside " + container
		}
		it.File, it.How, it.Restore, it.Copy = need+"."+cp.Ext, cp.Export+", "+where, cp.Restore, CopyDeclared
		// Only a copy that can fill a new place has a format (Parse holds
		// it to its import): a rehearsal, a start from a copy and a drill
		// fill only those.
		it.RestoreStopped, it.Verify, it.Format, it.Declared = cp.RestoreStopped, cp.Verify, cp.Format, d
		return it
	}
	it.File, it.How, it.Restore, it.Copy = need+".tar.gz", "tar of "+d.Local.Data, "tar xz -C "+d.Local.Data, copyLiveFiles
	it.Step = docker.Step{Exec: container, Argv: []string{"tar", "cz", "-C", d.Local.Data, "."}}
	return it
}

// TakeBackup takes one set for a standing deployment and records
// it. The deployment must be on a Docker host AppTown talks to.
func (s *Service) TakeBackup(ctx context.Context, projectID ids.ID, deploymentName, actor string) (*Backup, error) {
	return s.takeBackup(ctx, projectID, deploymentName, "manual", actor)
}

func (s *Service) takeBackup(ctx context.Context, projectID ids.ID, deploymentName, trigger, actor string) (*Backup, error) {
	// A project being deleted, or deleted, takes no new data work; a
	// restore's safety copy was admitted with its restore.
	if trigger != TriggerPreRestore {
		if err := project.RefuseUnlessActive(ctx, s.db, projectID); err != nil {
			return nil, err
		}
	}
	slugMeta, slugErr := project.ByIDIn(ctx, s.db, projectID)
	slug := slugMeta.Slug
	if err := slugErr; err != nil {
		return nil, fmt.Errorf("no such project")
	}
	dep, err := s.deployments.ByName(ctx, projectID, deploymentName)
	if err != nil || !dep.Standing() {
		return nil, fmt.Errorf("no standing deployment named %q", deploymentName)
	}
	// A target that cannot be read is not one known to take AppTown's
	// backups: the refusal for one that keeps its own is never skipped.
	t, err := s.targets.ByID(ctx, dep.TargetID)
	if err != nil {
		return nil, fmt.Errorf("%s: reading its deployment target: %w", deploymentName, err)
	}
	if err := t.HoldsBackups(); err != nil {
		return nil, fmt.Errorf("%s: %w", deploymentName, err)
	}
	held, err := s.heldData(ctx, dep, t)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", deploymentName, err)
	}
	return s.takeSet(ctx, held.dk, projectID, slug, deploymentName, held.items, trigger, actor)
}

// takeSet copies each planned item into one new set, records it with its
// receipt, and prunes the deployment's older sets (not for a safety copy).
// The set goes to the backup store when one is attached, staged on the
// AppTown's disk only while it uploads; without one it stays on AppTown's
// disk (design/subsystems/deployment.md, "Backups and restore").
func (s *Service) takeSet(ctx context.Context, dk docker.Scope, projectID ids.ID, slug, deploymentName string, plan []backupPlanItem, trigger, actor string) (*Backup, error) {
	return s.takeSetWhere(ctx, dk, projectID, slug, deploymentName, plan, trigger, actor, false)
}

// takeSetWhere is takeSet, kept on AppTown's disk whatever store is
// attached when onDisk: a whole-AppTown backup's archive holds the files.
func (s *Service) takeSetWhere(ctx context.Context, dk docker.Scope, projectID ids.ID, slug, deploymentName string, plan []backupPlanItem, trigger, actor string, onDisk bool) (*Backup, error) {
	store, err := s.Store(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading the backup store: %w", err)
	}
	if onDisk {
		store = nil
	}
	var c *s3bucket.Client
	if store != nil {
		if c, err = s.storeClientFor(ctx, store); err != nil {
			return nil, fmt.Errorf("the backup store: %w", err)
		}
	}
	// One directory per set, even for two sets in one second (a release
	// and a manual take can coincide): the microseconds keep them apart.
	t0 := s.now().UTC()
	stamp := fmt.Sprintf("%s-%06d", t0.Format("2006-01-02T15-04-05Z"), t0.Nanosecond()/1000)
	dir := filepath.Join(DiskDirectory(s.dataDir), slug, deploymentName, stamp)
	b := &Backup{ProjectID: projectID, Deployment: deploymentName, TakenAt: t0.Format(time.RFC3339), Trigger: trigger, Actor: actor, Path: dir, Status: BackupOK, Store: StoreAppTownDisk}
	if store != nil {
		dir = filepath.Join(DiskDirectory(s.dataDir), ".staging", "take-"+slug+"-"+deploymentName+"-"+stamp)
		b.Path, b.Store, b.StoreEndpoint, b.StoreBucket = slug+"/"+deploymentName+"/"+stamp, StoreBackupStore, store.Endpoint, store.Bucket
		defer os.RemoveAll(dir)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if dep, err := s.deployments.ByName(ctx, projectID, deploymentName); err == nil {
		b.DataClass = dep.DataClass
	}
	for _, p := range plan {
		it := BackupItem{Kind: p.Kind, Name: p.Name, File: p.File, How: p.How, Restore: p.Restore, Volume: p.Volume,
			Copy: p.Copy, RestoreStopped: p.RestoreStopped, Verify: p.Verify, Caveat: p.Caveat, Format: p.Format}
		path := filepath.Join(dir, p.File)
		switch {
		case p.Unresolved != "":
			it.Error = p.Unresolved
		default:
			var err error
			if p.Kind == "need" && p.RestoreStopped {
				p, it.EngineImageID, err = physicalCopySource(ctx, dk, p)
			}
			if err == nil {
				if p.Declared != nil {
					it.Client, it.AsUser, err = exportDeclared(ctx, dk, p, path)
				} else {
					err = stepToFile(ctx, dk, path, p.Step)
				}
			}
			if err != nil {
				it.Error = strs.Truncate(err.Error(), 300)
				_ = os.Remove(path)
				break
			}
			if fi, err := os.Stat(path); err == nil {
				it.Bytes = fi.Size()
				it.SHA256 = fileSHA256(path)
			}
			if c != nil {
				// The store holds it only when what it took is what was
				// copied: PutPrivateFile reads back its size and digest.
				_, err := c.PutPrivateFile(ctx, store.Bucket, b.Path+"/"+p.File, path)
				_ = os.Remove(path)
				if err != nil {
					it.Error = strs.Truncate("writing the copy to the backup store: "+err.Error(), 300)
				}
			}
			if it.Error != "" {
				it.Bytes, it.SHA256 = 0, ""
			} else {
				b.Bytes += it.Bytes
			}
		}
		b.Items = append(b.Items, it)
	}
	failed := 0
	for _, it := range b.Items {
		if it.Error != "" {
			failed++
		}
	}
	b.Status = taken(failed, len(b.Items))
	b.StatusLabel = b.label()
	if b.Status == BackupFailed {
		b.Error = b.Items[0].Error
	}
	// The record is what AppTown reads; a manifest missing beside the
	// copies is said on it, not hidden. On the disk it is written before
	// the record; in the store, once the set has its id, as a managed
	// copy's is.
	if c == nil {
		manifest, err := s.manifest(ctx, *b)
		if err == nil {
			err = os.WriteFile(filepath.Join(dir, "manifest.json"), manifest, 0o600)
		}
		if err != nil {
			b.Error = strings.TrimSpace(b.Error + " the manifest could not be written beside the copies: " + err.Error())
		}
	}
	id, err := s.backups.Record(ctx, b, actor)
	if err != nil {
		return nil, err
	}
	b.ID = id
	if c != nil {
		manifest, err := s.manifest(ctx, *b)
		if err == nil {
			err = c.PutPrivate(ctx, store.Bucket, b.Path+"/manifest.json", "application/json", manifest)
		}
		if err != nil {
			note := "the manifest could not be written beside the copies: " + err.Error()
			// The set is recorded: its caveat is too, though the upload's
			// context ended.
			if nerr := s.backups.note(context.WithoutCancel(ctx), id, note); nerr != nil {
				return nil, fmt.Errorf("set %d: %s, and that could not be recorded: %w", id, note, nerr)
			}
			b.Error = strings.TrimSpace(b.Error + " " + note)
		}
	}
	if !b.safetyCopy() {
		if err := s.pruneSets(ctx, projectID, deploymentName, keepFor(b.DataClass)); err != nil {
			log.Printf("backup retention %s/%s: %v", slug, deploymentName, err)
		}
	}
	return b, nil
}

// exportDeclared runs a need's declared copy (its kind's copy.export)
// beside the need, with the connection its container serves, writing the
// copy to path. It returns the image the copy ran in and the user ("" for
// a copy given no connection), which the item records.
func exportDeclared(ctx context.Context, dk docker.Scope, p backupPlanItem, path string) (image, user string, err error) {
	_, env, image, user, err := declaredConnection(ctx, dk, p.Declared, p.Container)
	if err != nil {
		return "", "", err
	}
	cp := p.Declared.Copy
	if cp.InClient() {
		image = clientImage(cp, "")
	}
	if err := stepToFile(ctx, dk, path, besideNeed(cp.InClient(), p.Container, image, env, cp.Export)); err != nil {
		return "", "", err
	}
	return image, user, nil
}

// stepToFile runs a step whose stdout is a copy, writing it to path.
func stepToFile(ctx context.Context, dk docker.Scope, path string, step docker.Step) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	step.Stdout = f
	out, runErr := dk.RunStep(ctx, step)
	if cerr := f.Close(); runErr == nil && cerr != nil {
		return cerr
	}
	if runErr != nil {
		return fmt.Errorf("%w: %s", runErr, strs.Truncate(strings.TrimSpace(string(out)), 300))
	}
	return nil
}

// besideNeed is the step that runs one of a kind's copy commands against
// a running need, given env: in the need's own container, or, for a kind
// whose copy runs in its client image (copy.runIn: client), in a throwaway
// container of that image sharing the need container's network, so
// 127.0.0.1 is the need's either way.
func besideNeed(inClient bool, container, image string, env []string, command string) docker.Step {
	if inClient {
		return docker.Step{Image: image, Options: []string{"--entrypoint", "", "--network", "container:" + container}, Env: env, Command: command}
	}
	return docker.Step{Exec: container, Env: env, Command: command}
}

// declaredConnection is what a kind's copy commands are given in a need's
// running container: the docker -e flags and their values for the kind's
// copy.env, from the connection the container serves. A kind whose copy
// reads no connection (Redis) is given none, and its container is not
// asked.
func declaredConnection(ctx context.Context, dk docker.Scope, kind *dependency.Kind, container string) (flags, env []string, image, user string, err error) {
	if len(kind.Copy.Env) == 0 {
		return nil, nil, "", "", nil
	}
	conn, image, err := containerConnection(ctx, dk, kind, container)
	if err != nil {
		return nil, nil, "", "", err
	}
	flags, env = copyEnv(kind.Copy, conn)
	return flags, env, image, conn.User, nil
}

// wholeToken is a declared value that is exactly one {token}.
var wholeToken = regexp.MustCompile(`^\{([a-zA-Z0-9_]+)\}$`)

// containerConnection is the connection a need's running container serves,
// read from the environment it was started with: each of the kind's
// local.env variables declared as exactly one {token} gives that token's
// value, over the kind's defaults; a generated credential's value is the
// container's too, over the catalogue's (a deployment's own, or the
// constant a container made before credentials were generated still
// serves). It is reached from inside the container, at 127.0.0.1. The
// container's image is returned too.
func containerConnection(ctx context.Context, dk docker.Scope, kind *dependency.Kind, container string) (deploy.Connection, string, error) {
	out, err := dk.Run(ctx, "inspect", "--format", "{{json .Config}}", container)
	if err != nil {
		return deploy.Connection{}, "", fmt.Errorf("reading how %s was started: %v: %s", container, err, strs.Truncate(strings.TrimSpace(string(out)), 200))
	}
	var cfg struct {
		Image string
		Env   []string
	}
	if err := json.Unmarshal(out, &cfg); err != nil {
		return deploy.Connection{}, "", fmt.Errorf("reading how %s was started: %v", container, err)
	}
	env := map[string]string{}
	for _, kv := range cfg.Env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	params := map[string]string{}
	for name, value := range kind.Local.Env {
		if m := wholeToken.FindStringSubmatch(value); m != nil {
			if v, ok := env[name]; ok {
				params[m[1]] = v
			}
		}
	}
	conn, _ := kind.LocalConnection(params, params)
	conn.Host = "127.0.0.1"
	return conn, cfg.Image, nil
}

// restoreBackup puts a set back into a deployment: any set of the
// project's, its own or another deployment's (a taken-down deployment's
// final set). Items go by role: the set's data volume into the
// destination's app volume, whatever either is named, and each need's
// copy into the destination's need of the same name, by its declared
// restore command in its own container. A set item with nowhere to go
// refuses the restore before anything changes. Then a safety set of what
// is there now is taken, the app container is stopped, each item is
// restored, and the app is started again. The safety set and the outcome
// are on the record; each item says what witnessed it (RestoreOutcome), so
// files put in place are never reported as data a service loaded.
//
// It is the body of a restore operation (RequestRestore, restore_operation.go):
// rec writes each step and each fact as it happens, a container's id
// before it is stopped and again once it is started, so an AppTown that stops
// part way leaves what was done on the record, and what it stopped and did
// not start again is named. It asks the admitting authority again before
// the safety set and before each item.
func (s *Service) restoreBackup(ctx context.Context, rec *restoreLog, projectID ids.ID, deploymentName string, backupID ids.ID, actor string) (*RestoreResult, error) {
	// The restore's volume writes run in one snapshot's shell image.
	sh := s.env.ToolVersions().Images.Sh
	now := s.now()
	files, err := lockBackupFiles(DiskDirectory(s.dataDir), false)
	if err != nil {
		return nil, err
	}
	defer files.Close()
	b, dep, err := s.restorable(ctx, projectID, deploymentName, backupID)
	if err != nil {
		return nil, err
	}
	// Not into a deployment a restore of AppTown holds: its records may be
	// stale, or its own data is still to come back.
	if err := dep.HeldAfterRestore(); err != nil {
		return nil, err
	}
	rec.record(ctx, "from", b.Deployment)
	// The restore holds its deployment until it ends: a take-down (a
	// deletion's too) is refused meanwhile, and one under way refuses
	// this. The lifecycle is asked again once it is held, so a project
	// marked deleting before then restores nothing.
	release, err := s.dataWork.claim(dep.ID, "a restore of "+dep.Name)
	if err != nil {
		return nil, err
	}
	defer release()
	if err := project.RefuseUnlessActive(ctx, s.db, projectID); err != nil {
		return nil, err
	}
	targetID, container, hostPort := dep.TargetID, dep.Container, dep.HostPort
	t, err := s.targets.ByID(ctx, targetID)
	if err != nil {
		return nil, fmt.Errorf("%s: reading its deployment target: %w", deploymentName, err)
	}
	held, err := s.heldData(ctx, dep, t)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", deploymentName, err)
	}
	dk := held.dk
	c := held.contract
	moves, untouched, err := restoreMoves(b, held.items)
	if err != nil {
		return nil, err
	}
	// Every copy to restore is in hand before anything changes: a set in
	// the store is read back to staging first and held to its digests, so
	// an unreachable store or a set pruned meanwhile refuses the restore
	// here.
	paths := make([]string, len(moves))
	for i, m := range moves {
		paths[i] = filepath.Join(b.Path, m.item.File)
	}
	if b.Store == StoreBackupStore {
		rec.begin(ctx, fmt.Sprintf("reading set %d back from the backup store", backupID))
		c, store, err := s.storeOf(ctx, b)
		if err != nil {
			return nil, fmt.Errorf("reading set %d back from the backup store: %w; nothing was restored", backupID, err)
		}
		dir := filepath.Join(DiskDirectory(s.dataDir), ".staging", fmt.Sprintf("restore-%d-%d", backupID, now.UnixNano()))
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		defer os.RemoveAll(dir)
		for i, m := range moves {
			if paths[i], err = readBack(ctx, c, store.Bucket, b, &m.item, dir); err != nil {
				return nil, fmt.Errorf("reading set %d's %s back from the backup store: %w; nothing was restored", backupID, m.item.File, err)
			}
		}
	}
	if err := rec.allowed(ctx); err != nil {
		return nil, fmt.Errorf("%w; nothing was restored", err)
	}
	rec.begin(ctx, "taking a safety set of what is there now")
	safety, err := s.takeBackup(ctx, projectID, deploymentName, TriggerPreRestore, actor)
	if err != nil {
		return nil, fmt.Errorf("taking the safety set first: %w", err)
	}
	rec.record(ctx, "safetyBackup", safety.ID)
	// Nothing is overwritten unless every item of what is there now was
	// copied first: a partial safety set leaves data with no copy.
	if safety.Status != BackupOK {
		var missing []string
		for _, it := range safety.Items {
			if it.Error != "" {
				missing = append(missing, it.Kind+" "+it.Name+": "+it.Error)
			}
		}
		return nil, fmt.Errorf("the safety set %d is %s (%s); nothing was restored; fix what is listed, or take a new safety set, then restore again", safety.ID, safety.Status, strings.Join(missing, "; "))
	}
	rec.end(ctx, fmt.Sprintf("set %d", safety.ID))
	names, err := dep.HostNames()
	if err != nil {
		return nil, fmt.Errorf("naming %s on its host: %w; nothing was restored", deploymentName, err)
	}
	if container == "" {
		container = names.App()
	}
	// Nothing writes the data while it is put back: every application part
	// is stopped first, as for a final backup. The deployment's list says
	// what AppTown recorded creating; the running release names every
	// part, so the public container and the release's other parts are
	// stopped even where the list predates recording them. A part that will
	// not stop refuses the restore before anything is written.
	owned, err := s.deployments.Owned(ctx, dep.ID)
	if err != nil {
		return nil, fmt.Errorf("reading what %s runs: %w; nothing was restored", deploymentName, err)
	}
	writers := []deploy.Owned{{Kind: "container", ID: container, Name: container}}
	if c != nil {
		for _, p := range c.Parts {
			if !p.Public {
				name := names.Service(p.Name)
				writers = append(writers, deploy.Owned{Kind: "container", ID: name, Name: name})
			}
		}
	}
	if held.layout != nil {
		// On Portainer the stack says its writers: its containers that are
		// not a need's and run now.
		if writers, err = s.stackWriters(ctx, dk, *held.layout); err != nil {
			return nil, fmt.Errorf("%w; nothing was restored", err)
		}
		owned = nil
	}
	rec.begin(ctx, "stopping the app, so nothing writes while the data goes back")
	recordStop := func(id string) error { return rec.stopping(ctx, id) }
	recordStart := func(id string) { rec.started(ctx, id) }
	parts, err := stopParts(ctx, dk, append(writers, owned...), recordStop)
	// A part the running release names that Docker does not have cannot
	// write, so the data still goes back; but the app does not come back
	// without it, and the result says so. (A list entry with no container is
	// history, not a missing part; a part someone stopped is there and is
	// left stopped.) Any other inspection failure refused above.
	var absent []string
	for _, w := range writers {
		if !slices.Contains(parts.ids, w.ID) && !slices.Contains(parts.idle, w.ID) {
			absent = append(absent, w.ID+": not there, so not started")
		}
	}
	if err != nil {
		why := err.Error()
		if failed := parts.restartEach(ctx, recordStart); len(failed) > 0 {
			why += "; these could not be started again, so the deployment is not serving: " + strings.Join(failed, "; ")
		}
		return nil, fmt.Errorf("%s; nothing was restored", why)
	}
	rec.end(ctx, strings.Join(parts.ids, ", "))
	result := &RestoreResult{Backup: backupID, From: b.Deployment, SafetyBackup: safety.ID, Stopped: len(parts.ids) > 0}
	var withdrawn error
	for i, m := range moves {
		if withdrawn == nil {
			withdrawn = rec.allowed(ctx)
		}
		if withdrawn != nil {
			result.Items = append(result.Items, RestoredItem{Kind: m.into.Kind, Name: m.into.Name, Outcome: restoreSkipped, Detail: "not restored: " + withdrawn.Error()})
			continue
		}
		rec.begin(ctx, "restoring "+m.into.Name)
		it := s.restoreItem(ctx, rec, dk, m, paths[i], sh)
		result.Items = append(result.Items, it)
		rec.record(ctx, "items", result.Items)
		if it.Outcome == restoreFailed {
			rec.fail(ctx, it.Detail)
		} else {
			rec.end(ctx, it.Outcome.String())
		}
	}
	for _, p := range untouched {
		result.Items = append(result.Items, RestoredItem{Kind: p.Kind, Name: p.Name, Outcome: restoreSkipped, Detail: "the set holds no good copy of this item; it is left as it is"})
	}
	rec.record(ctx, "items", result.Items)
	if result.Stopped {
		rec.begin(ctx, "starting the app again")
		result.NotStarted = parts.restartEach(ctx, recordStart)
		if len(result.NotStarted) > 0 {
			rec.fail(ctx, strings.Join(result.NotStarted, "; "))
		}
	}
	result.NotStarted = append(result.NotStarted, absent...)
	started := result.Stopped && len(result.NotStarted) == 0
	result.Started = &started
	if started {
		// An app can take a while to come back (greeting-api boots in 17 s):
		// the door waits for readiness and says whether it arrived, rather
		// than returning while the front door still answers 502.
		if hostPort > 0 {
			rec.begin(ctx, "waiting for the app to answer")
			probe := docker.RenderColdBootProbe(c.PublicPart(), s.env.FrontDoorHeaders())
			up, _ := s.env.Probe().Check(ctx, dk, container, dk.ProbeURL(hostPort), 90*time.Second, probe)
			result.Ready = &up
			if up {
				rec.end(ctx, "it answers")
			} else {
				rec.end(ctx, "it did not answer within 90 s")
			}
		}
	}
	result.Status = restoreStatus(result.Items)
	// The restored receipt is written when the operation settles
	// (settleRestore), in the same transaction, also when a restart
	// settles it unknown.
	failed := 0
	for _, it := range result.Items {
		if it.Outcome == restoreFailed {
			failed++
		}
	}
	if withdrawn != nil {
		return result, fmt.Errorf("%w; the items before it are restored and the safety set %d holds what was there before", withdrawn, safety.ID)
	}
	if failed > 0 {
		return result, fmt.Errorf("%d item(s) did not restore; the safety set %d holds what was there before", failed, safety.ID)
	}
	return result, nil
}

// restoreItem puts one item of a set back and says what witnessed it. The
// item's own record says how (its restore command, mode, check and
// caveat) and what its file must be (its digest): an item whose record
// lacks either is not restored, never put back by today's command or
// unchecked.
// The app's data volume is emptied and unpacked through a helper. A need
// is restored by its command in its running container or, when declared
// restoreStopped, in a throwaway container of its image on its volumes
// while it is stopped, after which it is started again. Then the need's
// verify, if declared, runs in it with the copy on stdin.
func (s *core) restoreItem(ctx context.Context, rec *restoreLog, dk docker.Scope, m restoreMove, path, sh string) RestoredItem {
	it, p := m.item, m.into
	row := RestoredItem{Kind: p.Kind, Name: p.Name, Caveat: it.Caveat}
	fail := func(detail string) RestoredItem {
		row.Outcome, row.Detail = restoreFailed, strs.Truncate(strings.TrimSpace(detail), 300)
		return row
	}
	if it.SHA256 == "" {
		return fail("the record carries no digest of this file, so what it holds is not known; not restored")
	}
	if fileSHA256(path) != it.SHA256 {
		return fail("the file on disk does not match the record's digest; not restored")
	}
	if p.Kind != "volume" && it.Restore == "" {
		return fail("the record carries no restore command (the set is from before sets recorded theirs); AppTown does not restore it by today's command")
	}
	if p.Kind == "volume" {
		f, err := os.Open(path)
		if err != nil {
			return fail(err.Error())
		}
		out, err := dk.RunStep(ctx, docker.Step{Image: sh, Options: []string{"--entrypoint", "", "--network", "none", "-v", p.Name + ":/dst"},
			Command: "find /dst -mindepth 1 -delete && tar xz -C /dst", Stdin: f})
		f.Close()
		if err != nil {
			return fail(string(out))
		}
		row.Outcome, row.Detail = restoreCopied, "files copied, not verified: nothing checks that the app reads them"
		return row
	}
	restore, stoppedMode, verify := it.Restore, it.RestoreStopped, it.Verify
	// The restore and the check run in the running service as the export
	// did, given the connection it serves (Postgres's PG* variables). A
	// restore into a stopped service is given none: nothing serves one.
	// A file copy is unpacked, and reads no connection: it is given none.
	var env []string
	if p.Declared != nil && it.Copy != copyLiveFiles && it.Copy != copyFiles {
		var err error
		if _, env, _, _, err = declaredConnection(ctx, dk, p.Declared, p.Container); err != nil {
			return fail("reading the connection " + p.Container + " serves: " + err.Error())
		}
	}
	// A declared copy goes back where its kind's commands run; a set's file
	// copy (the fallback, or a set from before the kind declared a copy) is
	// unpacked in the need's own container, where its files are.
	inClient := it.Copy == CopyDeclared && p.Declared != nil && p.Declared.Copy.InClient()
	image := ""
	if inClient {
		image = clientImage(p.Declared.Copy, "")
	}
	inService := func(command string) ([]byte, error) {
		step := besideNeed(inClient, p.Container, image, env, command)
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		step.Stdin = f
		return dk.RunStep(ctx, step)
	}
	if stoppedMode {
		if out, err := restoreIntoStopped(ctx, rec, dk, p.Container, path, restore); err != nil {
			return fail(string(out) + " " + err.Error())
		}
	} else if out, err := inService(restore); err != nil {
		return fail(string(out) + " " + err.Error())
	}
	switch {
	case verify != "":
		if out, err := inService(verify); err != nil {
			return fail("the copy was put back but its check did not confirm the service holds it: " + string(out))
		}
		row.Outcome = restoreVerified
	case it.Copy == CopyDeclared && !stoppedMode:
		row.Outcome = restoreLoaded
	default:
		row.Outcome, row.Detail = restoreCopied, "files copied, not verified: the need declares no restore that its service confirms"
	}
	return row
}

// restoreIntoStopped stops a need's container, runs the restore with the
// copy on stdin in a throwaway container of the same image on the same
// volumes, and starts the need again whatever the restore did, so a failed
// restore never leaves the service down.
func restoreIntoStopped(ctx context.Context, rec *restoreLog, dk docker.Scope, container, path, restore string) ([]byte, error) {
	image := dk.Text(ctx, "inspect", "--format", "{{.Config.Image}}", container)
	if image == "" {
		return nil, fmt.Errorf("could not read the image of %s", container)
	}
	if err := rec.stopping(ctx, container); err != nil {
		return nil, err
	}
	if out, err := dk.Run(ctx, "stop", container); err != nil {
		return out, fmt.Errorf("stopping %s: %w", container, err)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	out, rerr := dk.RunStep(ctx, docker.Step{Image: image, Options: []string{"--entrypoint", "", "--network", "none", "--volumes-from", container}, Command: restore, Stdin: f})
	f.Close()
	if sout, err := dk.Run(ctx, "start", container); err != nil {
		return append(out, sout...), errors.Join(rerr, fmt.Errorf("starting %s again: %w", container, err))
	}
	rec.started(ctx, container)
	return out, rerr
}

// restoreMove is one good item of a set and where it goes.
type restoreMove struct {
	item BackupItem
	into backupPlanItem
}

// errRestoreMismatch refuses a restore whose set holds data the
// destination has nowhere to put. Nothing was changed.
var errRestoreMismatch = fault.New(fault.Conflict, "the set holds data this deployment has nowhere to put; nothing was restored")

// restoreMoves maps a set's good items onto what the destination holds
// (placeCopies): each into the volume it was taken from, else by role when
// that is unambiguous. A good item with no certain destination is refused
// before any change; a destination item with no good copy in the set is
// left as it is.
func restoreMoves(b *Backup, plan []backupPlanItem) ([]restoreMove, []backupPlanItem, error) {
	// A destination need whose kind cannot be read has no known way back.
	var unresolved []string
	for _, p := range plan {
		if p.Unresolved != "" {
			unresolved = append(unresolved, p.Unresolved)
		}
	}
	if len(unresolved) > 0 {
		return nil, nil, fmt.Errorf("%w: %s", errRestoreMismatch, strings.Join(unresolved, "; "))
	}
	into, unplaced := placeCopies(b.Items, plan)
	if len(unplaced) > 0 {
		return nil, nil, fmt.Errorf("%w: this set holds %s", errRestoreMismatch, strings.Join(unplaced, "; "))
	}
	var moves []restoreMove
	used := map[int]bool{}
	for i, it := range b.Items {
		if j, ok := into[i]; ok {
			used[j] = true
			moves = append(moves, restoreMove{item: it, into: plan[j]})
		}
	}
	var untouched []backupPlanItem
	for i, p := range plan {
		if !used[i] {
			untouched = append(untouched, p)
		}
	}
	return moves, untouched, nil
}

func fileSHA256(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

// BackupPlan describes what a standing deployment holds and how each item
// is copied (heldData); why says there is nothing to copy, or why that is
// not known. The execution commands remain private to the runtime.
func (s *Service) BackupPlan(ctx context.Context, projectID ids.ID, slug, name string) ([]BackupPlanItem, string) {
	items, err := s.heldPlan(ctx, projectID, slug, name)
	if err != nil {
		return []BackupPlanItem{}, err.Error()
	}
	out := make([]BackupPlanItem, 0, len(items))
	for _, it := range items {
		how := it.How
		if it.Unresolved != "" {
			how = "not copied: " + strings.TrimPrefix(it.Unresolved, "not copied: ")
		}
		out = append(out, BackupPlanItem{Kind: it.Kind, Name: it.Name, Volume: it.Volume, How: how, Caveat: it.Caveat})
	}
	return out, ""
}

// heldPlan is heldData for a standing deployment named in a project.
func (s *Service) heldPlan(ctx context.Context, projectID ids.ID, slug, name string) ([]backupPlanItem, error) {
	dep, err := s.deployments.ByName(ctx, projectID, name)
	if err != nil || !dep.Standing() {
		return nil, fmt.Errorf("no standing deployment named %q", name)
	}
	t, err := s.targets.ByID(ctx, dep.TargetID)
	if err != nil {
		return nil, fmt.Errorf("%w: reading its deployment target: %v", errDataNotKnown, err)
	}
	held, err := s.heldData(ctx, dep, t)
	return held.items, err
}

type BackupPlanItem struct {
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Volume string `json:"volume,omitempty"`
	How    string `json:"how"`
	Caveat string `json:"caveat,omitempty"`
}

// physicalCopySource pins the exporter to the exact observed container. The
// later instance SQL dump cannot certify which engine wrote these bytes.
func physicalCopySource(ctx context.Context, dk docker.Scope, p backupPlanItem) (backupPlanItem, string, error) {
	if p.Declared == nil {
		return p, "", fmt.Errorf("the %s physical copy has no declared source", p.Name)
	}
	id, image, err := docker.ObserveStorageEngine(ctx, dk, p.Container, p.Declared.Local.Data, p.Volume)
	if err != nil {
		return p, "", err
	}
	p.Container = id
	return p, image, nil
}
