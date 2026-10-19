package dataprotection

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/deploy/digitalocean"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/deploy/s3bucket"
	"flokkit.io/forge/internal/forge/dataprotection/internal/protectionsql"
	"flokkit.io/forge/internal/forge/dependency"
	"flokkit.io/forge/internal/forge/deployment"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/strs"
)

// Copies of managed data (design/subsystems/deployment.md, "Protecting data
// off-host"). The
// kind says what a copy is (its format and its export, import and verify
// commands); the provider says how the forge reaches an instance (connect:
// a throwaway container of the kind's client image on the target's named
// copy runner, given the app user's connection); the forge keeps the copy
// in the attached backup store. A copy is only ever filled into a new,
// empty database the provider makes, and checked there by the kind's
// verify before anything uses it.

var (
	// ErrNotReachable: nothing of the deployment's data is reached for a
	// copy on its target.
	ErrNotReachable error = &deployment.Unavailable{Reason: "not-reachable", Why: "Nothing the deployment holds is copied by AppTown on its target."}
)

// CopiesOn says whether the forge can run copies of the managed data of
// deployments on a target: nil where it takes none of its own (only App
// Platform's managed databases are copied by the forge), unavailable with
// ErrNoCopyRunner's reason while no copy runner is named.
func CopiesOn(t *deployment.Target) *Availability {
	if managedProvider(t) == "" {
		return nil
	}
	if t.CopyRunner() == "" {
		u := deployment.ErrNoCopyRunner.(*deployment.Unavailable)
		return &Availability{Reason: u.Reason, Why: u.Why}
	}
	return &Availability{Available: true}
}

// managedProvider is the provider name a target kind's managed needs are
// declared under in a kind's cloud block.
func managedProvider(t *deployment.Target) string {
	if t.DataAccess() == deployment.DataOnManagedPlatform {
		return "digitalocean"
	}
	return ""
}

// heldManaged is what a deployment on a managed target holds: its
// recorded managed need instances and the object storage buckets on its
// owned list, never the contract at the project's head, each with the
// kind's declared copy for the target's provider. An item the forge cannot
// copy is listed with why. The release is not read: a release records each
// instance and bucket it makes before the app runs, and one whose record
// could not be written failed, so the list is complete.
// ErrNothingToBackUp when it records none; ErrDataNotKnown when the
// records cannot be read.
func (s *core) heldManaged(ctx context.Context, dep *deployment.Deployment, t *deployment.Target) (heldItems, error) {
	insts, err := s.needs.ForDeployment(ctx, dep.ID)
	if err != nil {
		return heldItems{}, fmt.Errorf("%w: reading the deployment's need instances: %v", ErrDataNotKnown, err)
	}
	provider := managedProvider(t)
	var out []backupPlanItem
	for _, inst := range insts {
		if inst.Mode != "managed" {
			continue
		}
		str := func(k string) string { v, _ := inst.Handle[k].(string); return v }
		it := backupPlanItem{Kind: "need", Name: inst.Need, Instance: str("id"), Cluster: str("name"), Database: str("database"), User: str("user"), Version: str("version")}
		kind, kerr := s.catalog.ByCapability(ctx, inst.Need)
		switch {
		case kerr != nil:
			it.Unresolved = unresolvedNeed(inst.Need, kerr)
		case kind.Cloud[provider].Reach == nil || kind.Copy == nil:
			it.Unresolved = fmt.Sprintf("the %s kind declares no copy reached on %s", inst.Need, provider)
		case it.Instance == "" || it.Database == "" || it.User == "":
			it.Unresolved = fmt.Sprintf("the record of %s names no instance, database and user (it was not made through)", inst.Need)
		default:
			it.Declared, it.Format = kind, kind.Copy.Format
			it.How = fmt.Sprintf("%s, from the copy runner, as %s", kind.Copy.Export, it.User)
			if n := kind.Cloud[provider].Native; n != nil {
				it.Native = n.Mechanism
			}
			if it.Version == "" {
				it.Version = kind.Defaults["version"]
			}
		}
		out = append(out, it)
	}
	owned, err := s.owned.Owned(ctx, dep.ID)
	if err != nil {
		return heldItems{}, fmt.Errorf("%w: reading the deployment's list: %v", ErrDataNotKnown, err)
	}
	for _, o := range owned {
		if o.Kind != digitalocean.KindBucket || !o.Data {
			continue
		}
		it := backupPlanItem{Kind: "need", Name: o.Need, Bucket: o.ID}
		kind, kerr := s.catalog.ByCapability(ctx, o.Need)
		switch {
		case kerr != nil:
			it.Unresolved = unresolvedNeed(o.Need, kerr)
		case kind.Cloud[provider].Reach == nil || kind.Copy == nil || kind.Copy.Format == "":
			it.Unresolved = fmt.Sprintf("the %s kind declares no copy reached on %s", o.Need, provider)
		default:
			it.Declared, it.Format = kind, kind.Copy.Format
			it.How = kind.Copy.Export + ", from the copy runner, as the deployment's own key for its bucket"
		}
		out = append(out, it)
	}
	if len(out) == 0 {
		return heldItems{}, ErrNothingToBackUp
	}
	return heldItems{items: out}, nil
}

// connection is how a copy job reaches a managed item: a database as the
// app's user, from the provider; a bucket as the deployment's own key for
// it, from the target's storage module. Read at use time, never recorded.
func (s *core) connection(ctx context.Context, md deploy.ManagedData, bd deploy.BucketData, it backupPlanItem) (deploy.Connection, error) {
	if it.Bucket == "" {
		return md.AppConnection(ctx, it.Instance, it.Database, it.User)
	}
	if bd == nil {
		return deploy.Connection{}, fmt.Errorf("the target cannot reach the bucket %s for a copy", it.Bucket)
	}
	return bd.BucketConnection(ctx, it.Bucket)
}

// bucketData is the target's storage module for a deployment's bucket:
// nil, with no error, when the target has none.
func (s *core) bucketData(ctx context.Context, t *deployment.Target, dep *deployment.Deployment) (deploy.BucketData, error) {
	d, err := s.providers.Driver(ctx, t, dep)
	if err != nil {
		return nil, err
	}
	bd, _ := d.(deploy.BucketData)
	return bd, nil
}

// managedList is heldManaged as a list: one that records no instance holds
// nothing, which a managed operation reads as an empty list.
func (s *core) managedList(ctx context.Context, dep *deployment.Deployment, t *deployment.Target) ([]backupPlanItem, error) {
	held, err := s.heldManaged(ctx, dep, t)
	if errors.Is(err, ErrNothingToBackUp) {
		return nil, nil
	}
	return held.items, err
}

// copyRunner is the Docker host a target's copy jobs run on.
func (s *core) copyRunner(ctx context.Context, t *deployment.Target) (docker.Scope, string, error) {
	name := t.CopyRunner()
	if name == "" {
		return docker.Scope{}, "", deployment.ErrNoCopyRunner
	}
	runner, err := s.targets.ByName(ctx, name)
	if err != nil {
		return docker.Scope{}, name, fmt.Errorf("the copy runner %s: %w", name, err)
	}
	dk, err := s.hosts.DockerScope(ctx, runner)
	return dk, name, err
}

// copyJob runs one of a kind's copy commands in a throwaway container of
// its client image on the runner, given the connection by name only: the
// values, the password among them, reach the docker CLI through its
// environment and never its arguments. It returns the command's error
// output.
func copyJob(ctx context.Context, dk docker.Scope, cp *dependency.Copy, image, command string, conn deploy.Connection, stdin io.Reader, stdout io.Writer) ([]byte, error) {
	return copyJobOn(ctx, dk, nil, cp, image, command, conn, stdin, stdout)
}

// copyJobOn is copyJob with more docker run options (a network, a volume):
// a rehearsal's import reaches its new need on the rehearsal's own network
// and reads the copy staged on the host.
func copyJobOn(ctx context.Context, dk docker.Scope, extra []string, cp *dependency.Copy, image, command string, conn deploy.Connection, stdin io.Reader, stdout io.Writer) ([]byte, error) {
	flags, env := copyEnv(cp, conn)
	args := []string{"run", "--rm", "-i", "--entrypoint", ""}
	args = append(args, extra...)
	args = append(args, flags...)
	args = append(args, image, "sh", "-c", command)
	if stdout == nil {
		stdout = io.Discard
	}
	out, err := dk.Command(ctx, docker.Command{Args: args, Stdin: stdin, Stdout: stdout, Env: env})
	if err != nil {
		return out, fmt.Errorf("%w: %s", err, strs.Truncate(strings.TrimSpace(string(out)), 400))
	}
	return out, nil
}

// copyEnv is the connection a copy command is given, as the kind's
// declared variables: "-e NAME" flags for docker, and the NAME=value pairs
// for the docker CLI's own environment, so no value (the password among
// them) is ever an argument.
func copyEnv(cp *dependency.Copy, conn deploy.Connection) (flags, env []string) {
	parts := map[string]string{"host": conn.Host, "port": strconv.Itoa(conn.Port), "database": conn.Database, "user": conn.User, "password": conn.Password, "sslmode": conn.SSLMode}
	names := make([]string, 0, len(cp.Env))
	for name := range cp.Env {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		value := cp.Env[name]
		for part, v := range parts {
			value = strings.ReplaceAll(value, "{"+part+"}", v)
		}
		flags = append(flags, "-e", name)
		env = append(env, name+"="+value)
	}
	return flags, env
}

// clientImage is the image a kind's copy commands run in for an instance
// of version.
func clientImage(cp *dependency.Copy, version string) string {
	return strings.ReplaceAll(cp.Client, "{version}", version)
}

// staging is a folder on the forge's disk where an operation's copies wait
// while they go to or come from the store; it is removed when the
// operation ends.
func (s *core) staging(op ids.ID) (string, func(), error) {
	dir := filepath.Join(s.dataDir, "backups", ".staging", op.String())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", func() {}, err
	}
	return dir, func() { _ = os.RemoveAll(dir) }, nil
}

// takeStoreCopy copies each of a deployment's managed instances that holds
// data into one new set in the backup store and records it: by the kind's
// export, run on the copy runner as the app's user, staged on the forge's
// disk, written to the store private and checked there. The set records,
// for each item, the instance and database it certifies.
func (s *Service) takeStoreCopy(ctx context.Context, t *tracker, dep *deployment.Deployment, target *deployment.Target, trigger string) (*Backup, error) {
	items, err := s.managedList(ctx, dep, target)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, ErrNotReachable
	}
	c, store, err := s.storeClient(ctx)
	if err != nil {
		return nil, err
	}
	dk, runner, err := s.copyRunner(ctx, target)
	if err != nil {
		return nil, err
	}
	md := s.providers.ManagedData(target)
	bd, err := s.bucketData(ctx, target, dep)
	if err != nil {
		return nil, err
	}
	p, err := project.ByIDIn(ctx, s.db, dep.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("reading the project: %w", err)
	}
	dir, cleanup, err := s.staging(t.op.ID)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	now := s.now().UTC()
	prefix := fmt.Sprintf("%s/%s/%s-%06d", p.Slug, dep.Name, now.Format("2006-01-02T15-04-05Z"), now.Nanosecond()/1000)
	b := &Backup{ProjectID: dep.ProjectID, Deployment: dep.Name, TakenAt: now.Format(time.RFC3339), Trigger: trigger, Actor: t.op.AskedBy,
		Path: prefix, Store: StoreBackupStore, DataClass: dep.DataClass, Status: BackupOK, StoreEndpoint: store.Endpoint, StoreBucket: store.Bucket}
	for i, it := range items {
		// Authority withdrawn ends the set here with what was copied.
		if err := t.allowed(ctx); err != nil {
			for _, rest := range items[i:] {
				b.Items = append(b.Items, BackupItem{Kind: "need", Name: rest.Name, Error: "not copied: " + err.Error(), Instance: rest.Instance, Database: rest.Database, Bucket: rest.Bucket})
			}
			break
		}
		row := BackupItem{Kind: "need", Name: it.Name, Instance: it.Instance, Database: it.Database, Bucket: it.Bucket, Copy: CopyDeclared}
		if it.Unresolved != "" {
			row.Error = it.Unresolved
			b.Items = append(b.Items, row)
			continue
		}
		row.File, row.Format, row.Restore, row.Verify, row.Caveat = it.Name+"."+it.copyOf().Ext, it.copyOf().Format, it.copyOf().Import, it.copyOf().Verify, it.copyOf().Caveat
		row.AsUser, row.Client = it.User, clientImage(it.copyOf(), it.Version)
		as := it.User
		if it.Bucket != "" {
			as = "the deployment's own key"
		}
		row.How = fmt.Sprintf("%s, in %s on %s, as %s", it.copyOf().Export, row.Client, runner, as)
		t.begin(ctx, "copying "+it.Name+" ("+it.where()+") on "+runner+" as "+as)
		stored, err := s.copyOne(ctx, dk, func(ctx context.Context) (deploy.Connection, error) { return s.connection(ctx, md, bd, it) }, c, store.Bucket, prefix, dir, it, row.File, t.allowed)
		if err != nil {
			row.Error = strs.Truncate(err.Error(), 400)
			t.fail(ctx, row.Error)
		} else {
			row.Bytes, row.SHA256 = stored.Size, stored.SHA256
			b.Bytes += stored.Size
			t.end(ctx, fmt.Sprintf("%d bytes in the store, sha256 %s", stored.Size, shortDigest(stored.SHA256)), stored.Size)
		}
		b.Items = append(b.Items, row)
	}
	failed := 0
	for _, it := range b.Items {
		if it.Error != "" {
			failed++
		}
	}
	b.Status = Taken(failed, len(b.Items))
	if b.Status == BackupFailed {
		b.Error = b.Items[0].Error
	}
	id, err := s.backups.Record(ctx, b, t.op.AskedBy)
	if err != nil {
		return nil, fmt.Errorf("recording the set: %w", err)
	}
	b.ID = id
	// The manifest is written once the set has its id, so someone reading
	// the bucket can tie the copies to their set.
	manifest, err := s.manifest(ctx, *b)
	if err == nil {
		err = c.PutPrivate(ctx, store.Bucket, prefix+"/manifest.json", "application/json", manifest)
	}
	if err != nil && b.Status != BackupFailed {
		// The copies are in the store; the record is what the forge reads.
		// A missing manifest is said on it, not hidden.
		note := "the manifest could not be written beside the copies: " + err.Error()
		// The set is recorded: its caveat is too, though the upload's
		// context ended.
		if nerr := s.backups.Note(context.WithoutCancel(ctx), id, note); nerr != nil {
			return nil, fmt.Errorf("set %d: %s, and that could not be recorded: %w", id, note, nerr)
		}
		b.Error = strings.TrimSpace(b.Error + " " + note)
	}
	if trigger != TriggerTakeDown && t.withdrawn == nil {
		if err := t.allowed(ctx); err != nil {
			t.note = "older copies were not pruned: " + err.Error()
		} else if err := s.PruneSets(ctx, dep.ProjectID, dep.Name, keepFor(dep.DataClass)); err != nil {
			t.note = "older copies were not pruned: " + err.Error()
		}
	}
	return b, nil
}

// copyOne exports one item's data to a staged file and writes it to the
// store, checked there.
func (s *Service) copyOne(ctx context.Context, dk docker.Scope, connect func(context.Context) (deploy.Connection, error), c *s3bucket.Client, bucket, prefix, dir string, it backupPlanItem, file string, allowed func(context.Context) error) (storedCopy, error) {
	conn, err := connect(ctx)
	if err != nil {
		return storedCopy{}, err
	}
	path := filepath.Join(dir, file)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return storedCopy{}, err
	}
	_, runErr := copyJob(ctx, dk, it.copyOf(), clientImage(it.copyOf(), it.Version), it.copyOf().Export, conn, nil, f)
	if cerr := f.Close(); runErr == nil {
		runErr = cerr
	}
	if runErr != nil {
		return storedCopy{}, fmt.Errorf("the export did not finish: %w", runErr)
	}
	// The export can take a long time; the upload is asked for again.
	if err := allowed(ctx); err != nil {
		return storedCopy{}, fmt.Errorf("exported but not uploaded: %w", err)
	}
	stored, err := c.PutPrivateFile(ctx, bucket, prefix+"/"+file, path)
	if err != nil {
		return storedCopy{}, err
	}
	return storedCopy{Size: stored.Size, SHA256: stored.SHA256}, nil
}

type storedCopy struct {
	Size   int64
	SHA256 string
}

func shortDigest(sum string) string {
	if len(sum) > 12 {
		return sum[:12]
	}
	return sum
}

// fillResult is what filling a new place from a copy found.
type fillResult struct {
	Place   string // where the copy was filled, in a reader's words
	Passed  bool
	Detail  string
	Left    string // a place made and not removed, for a person to remove
	Elapsed time.Duration
}

// readBack reads an item of a store set to a staged file and holds it to
// the set's record: its size and digest.
func readBack(ctx context.Context, c *s3bucket.Client, bucket string, b *Backup, it *BackupItem, dir string) (string, error) {
	path := filepath.Join(dir, "back-"+it.File)
	n, sum, err := c.GetToFile(ctx, bucket, b.Path+"/"+it.File, path)
	if err != nil {
		return "", err
	}
	if n != it.Bytes || sum != it.SHA256 {
		_ = os.Remove(path)
		return "", fmt.Errorf("the store holds %d bytes with sha256 %s for %s; the set records %d bytes with sha256 %s", n, shortDigest(sum), it.File, it.Bytes, shortDigest(it.SHA256))
	}
	return path, nil
}

// fill imports a staged copy into a database the provider has just made,
// as the app's user, and runs the kind's verify there. The place is made
// by the caller.
func fill(ctx context.Context, dk docker.Scope, md deploy.ManagedData, cp *dependency.Copy, image, instance, place, user, path string) error {
	conn, err := md.AppConnection(ctx, instance, place, user)
	if err != nil {
		return err
	}
	return fillAt(ctx, dk, nil, cp, image, cp.Import, conn, path)
}

// fillAt runs load (the kind's import, or its restore) with the staged copy
// on its stdin against the place conn reaches, then the kind's verify, in
// throwaway containers of image on the runner (with extra docker run
// options: a network).
func fillAt(ctx context.Context, dk docker.Scope, extra []string, cp *dependency.Copy, image, load string, conn deploy.Connection, path string) error {
	open := func() (*os.File, error) { return os.Open(path) }
	f, err := open()
	if err != nil {
		return err
	}
	var out bytes.Buffer
	_, err = copyJobOn(ctx, dk, extra, cp, image, load, conn, f, &out)
	f.Close()
	if err != nil {
		return fmt.Errorf("the import did not finish: %w", err)
	}
	if f, err = open(); err != nil {
		return err
	}
	out.Reset()
	_, err = copyJobOn(ctx, dk, extra, cp, image, cp.Verify, conn, f, &out)
	f.Close()
	if err != nil {
		return fmt.Errorf("the copy was imported but its check did not confirm the place holds it: %w %s", err, strs.Truncate(strings.TrimSpace(out.String()), 200))
	}
	return nil
}

// drill fills each good item of a store set into a new, empty database on
// the item's own instance, checks it there with the kind's verify, and
// removes it. Each item's result is recorded as a drill of the set; a place
// that could not be removed is named. It reports pass or fail and why,
// never the data.
func (s *Service) drill(ctx context.Context, t *tracker, b *Backup, target *deployment.Target) (bool, string, error) {
	c, store, err := s.storeOf(ctx, b)
	if err != nil {
		return false, "", err
	}
	dk, runner, err := s.copyRunner(ctx, target)
	if err != nil {
		return false, "", err
	}
	md := s.providers.ManagedData(target)
	dir, cleanup, err := s.staging(t.op.ID)
	if err != nil {
		return false, "", err
	}
	defer cleanup()
	passed, details := true, []string{}
	for i := range b.Items {
		it := &b.Items[i]
		if it.Error != "" || it.Format == "" {
			continue
		}
		// Authority withdrawn fills nothing more.
		if err := t.allowed(ctx); err != nil {
			return false, "", err
		}
		kind, err := s.catalog.ByCapability(ctx, it.Name)
		if err != nil || kind.Copy == nil || kind.Copy.Format != it.Format {
			passed = false
			details = append(details, fmt.Sprintf("%s: the %s kind no longer reads the copy's format %s", it.Name, it.Name, it.Format))
			continue
		}
		user := it.AsUser
		var res fillResult
		if it.Bucket != "" {
			res = s.drillBucket(ctx, t, c, store.Bucket, dk, kind, b, it, dir, runner)
		} else {
			res = s.drillOne(ctx, t, c, store.Bucket, dk, md, kind.Copy, b, it, dir, runner)
		}
		drilled := s.now().UTC()
		if _, err := protectionsql.New(s.db.SQL()).InsertBackupDrill(ctx, protectionsql.InsertBackupDrillParams{ID: ids.New(drilled), BackupID: b.ID, ProjectID: b.ProjectID, Deployment: b.Deployment, Need: it.Name,
			Passed: res.Passed, Target: target.Name, Place: res.Place, AsUser: user, Detail: res.Detail, OperationID: t.op.ID, DeploymentID: t.op.DeploymentID, DrilledAt: drilled}); err != nil {
			return false, "", fmt.Errorf("recording the drill: %w", err)
		}
		if !res.Passed {
			passed = false
		}
		d := it.Name + ": " + res.Detail
		if res.Left != "" {
			d += "; left for a person to remove: " + res.Left
			t.left = append(t.left, res.Left)
		}
		details = append(details, d)
	}
	if len(details) == 0 {
		return false, "the set holds no copy AppTown can fill into a new place", nil
	}
	return passed, strings.Join(details, "; "), nil
}

func (s *Service) drillOne(ctx context.Context, t *tracker, c *s3bucket.Client, bucket string, dk docker.Scope, md deploy.ManagedData, cp *dependency.Copy, b *Backup, it *BackupItem, dir, runner string) fillResult {
	user := it.AsUser
	place := checkPlace(it.Database, t.op.ID)
	// The place shown is where the copy came back, in words a reader
	// knows: the database's own name is gone once it is removed.
	res := fillResult{Place: "a new database on the deployment's cluster"}
	start := s.now()
	t.begin(ctx, fmt.Sprintf("reading set %d's copy of %s back from the store", b.ID, it.Name))
	path, err := readBack(ctx, c, bucket, b, it, dir)
	if err != nil {
		res.Detail = err.Error()
		t.fail(ctx, res.Detail)
		return res
	}
	defer os.Remove(path)
	t.end(ctx, fmt.Sprintf("%d bytes, sha256 %s, as recorded", it.Bytes, shortDigest(it.SHA256)), it.Bytes)
	t.begin(ctx, fmt.Sprintf("filling a new database %s from the copy on %s as %s, and checking it", place, runner, user))
	if err := t.allowed(ctx); err != nil {
		res.Detail = "not filled: " + err.Error()
		t.fail(ctx, res.Detail)
		return res
	}
	if err := md.NewDatabase(ctx, it.Instance, place, user); err != nil {
		if t.providerFailed(ctx, err) {
			// It may stand: named as left, never forgotten, and not
			// removed on a guess.
			res.Detail = "making the new database got no answer AppTown can trust, so whether it was made is not known: " + err.Error()
			res.Left = place + " on " + it.Instance + " (it may have been made: " + err.Error() + ")"
			return res
		}
		res.Detail = "no new place to fill: " + err.Error()
		return res
	}
	// Production data goes into it next: asked again.
	ferr := t.allowed(ctx)
	if ferr == nil {
		ferr = fill(ctx, dk, md, cp, it.Client, it.Instance, place, user, path)
	}
	if ferr != nil {
		res.Detail = ferr.Error()
		t.fail(ctx, res.Detail)
	} else {
		res.Passed, res.Detail = true, fmt.Sprintf("came back into a new database as %s and passed the %s check", user, it.Format)
		t.end(ctx, res.Detail, 0)
	}
	// Removing what the drill made is not asked again: it is the forge
	// cleaning up its own copy of production data.
	t.begin(ctx, "removing the database "+place)
	if err := md.DropDatabase(ctx, it.Instance, place); err != nil {
		if t.providerFailed(ctx, err) {
			res.Left = place + " on " + it.Instance + " (whether it was removed is not known: " + err.Error() + ")"
		} else {
			res.Left = place + " on " + it.Instance + " (" + err.Error() + ")"
		}
	} else {
		t.end(ctx, "", 0)
	}
	res.Elapsed = s.now().Sub(start)
	return res
}

// operationNeedScope is what the needs an operation starts for a copy of a
// deployment's data (a rehearsal, a drill) derive their generated
// credentials for: that operation alone.
func operationNeedScope(op ids.ID) string { return "need:operation:" + op.String() }

// drillBucket fills a copy of a bucket into a new store of the kind's own,
// started for the check on the copy runner on a network of its own and
// removed after, and checks it there: the copy is shown to come back into
// an S3 store with every object and its headers. It makes nothing at the
// provider; it does not show the copy restored into the provider's store.
func (s *Service) drillBucket(ctx context.Context, t *tracker, c *s3bucket.Client, bucket string, dk docker.Scope, kind *dependency.Kind, b *Backup, it *BackupItem, dir, runner string) fillResult {
	res := fillResult{Place: "a new " + it.Name + " store on " + runner}
	start := s.now()
	t.begin(ctx, fmt.Sprintf("reading set %d's copy of %s back from the store", b.ID, it.Name))
	path, err := readBack(ctx, c, bucket, b, it, dir)
	if err != nil {
		res.Detail = err.Error()
		t.fail(ctx, res.Detail)
		return res
	}
	defer os.Remove(path)
	t.end(ctx, fmt.Sprintf("%d bytes, sha256 %s, as recorded", it.Bytes, shortDigest(it.SHA256)), it.Bytes)
	t.begin(ctx, fmt.Sprintf("filling %s from the copy, and checking it", res.Place))
	if err := t.allowed(ctx); err != nil {
		res.Detail = "not filled: " + err.Error()
		t.fail(ctx, res.Detail)
		return res
	}
	// The new store holds a copy of a deployment's data: it gets the
	// operation's own credentials, not the catalogue's.
	generated, err := kind.Generated(ctx, s.env.NeedCredentials(operationNeedScope(t.op.ID)))
	if err != nil {
		res.Detail = "the new store's credentials: " + err.Error()
		t.fail(ctx, res.Detail)
		return res
	}
	local := kind.ResolveLocal(nil, "", generated)
	if local == nil {
		res.Detail = "the " + it.Name + " kind has no store of its own to fill"
		t.fail(ctx, res.Detail)
		return res
	}
	network, cleanup, err := docker.StartServices(ctx, dk, "check-"+shortDigest(strings.ReplaceAll(t.op.ID.String(), "-", "")), []deploy.ServiceSpec{local.Spec()})
	if err != nil {
		res.Detail = "the new store did not start: " + err.Error()
		t.fail(ctx, res.Detail)
		return res
	}
	defer cleanup()
	conn, _ := kind.LocalConnection(nil, generated)
	if err := fillAt(ctx, dk, []string{"--network", network}, kind.Copy, it.Client, kind.Copy.Import, conn, path); err != nil {
		res.Detail = err.Error()
		t.fail(ctx, res.Detail)
		return res
	}
	res.Passed, res.Detail = true, fmt.Sprintf("filled into %s and passed the %s check", res.Place, it.Format)
	t.end(ctx, res.Detail, 0)
	res.Elapsed = s.now().Sub(start)
	return res
}

// keepFor is how many ordinary sets a deployment keeps, wherever they
// are: newest 14 of production data, newest 7 otherwise.
func keepFor(dataClass string) int {
	if dataClass == "production" {
		return 14
	}
	return BackupKeep
}

// pruneStore deletes the store objects of sets and records them pruned.
func (s *Service) pruneStore(ctx context.Context, old []protectionsql.Backup) error {
	return s.pruneStoreSets(ctx, old, false)
}

// pruneStoreSets is pruneStore; purging settles a set whatever its state:
// a failed or partial set goes with its project too.
func (s *Service) pruneStoreSets(ctx context.Context, old []protectionsql.Backup, purging bool) error {
	if len(old) == 0 {
		return nil
	}
	for _, r := range old {
		b, err := backupRecord(r, nil)
		if err != nil {
			return err
		}
		// A set that never reached the store (no place recorded) has
		// nothing there to delete: a purge only settles it.
		if purging && (b.Path == "" || b.StoreBucket == "") {
			if err := s.backups.settlePruned(ctx, r, r.Status); err != nil {
				return err
			}
			continue
		}
		c, store, err := s.storeOf(ctx, &b)
		if err != nil {
			return err
		}
		for _, it := range b.Items {
			if it.File == "" {
				continue
			}
			if err := c.DeleteObject(ctx, store.Bucket, b.Path+"/"+it.File); err != nil {
				return fmt.Errorf("deleting set %d's %s: %w", b.ID, it.File, err)
			}
		}
		if err := c.DeleteObject(ctx, store.Bucket, b.Path+"/manifest.json"); err != nil {
			return fmt.Errorf("deleting set %d's manifest: %w", b.ID, err)
		}
		if err := s.backups.settlePruned(ctx, r, map[bool]string{true: r.Status, false: BackupOK.String()}[purging]); err != nil {
			return fmt.Errorf("set %d's copies were deleted, but %w", b.ID, err)
		}
	}
	return nil
}

// checkPlace is the new database a check of database fills, named for the
// operation: <database>_check_<12 hex of a digest of its id>, the
// database's name cut so the whole is at most 40 characters, DigitalOcean's
// limit on a database's name (a full UUID made it 46, and every check was
// refused).
func checkPlace(database string, op ids.ID) string {
	sum := sha256.Sum256([]byte(op.String()))
	suffix := "_check_" + hex.EncodeToString(sum[:6])
	if room := 40 - len(suffix); len(database) > room {
		database = database[:room]
	}
	return database + suffix
}
