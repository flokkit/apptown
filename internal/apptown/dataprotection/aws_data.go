package dataprotection

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/apptown/dataprotection/internal/protectionsql"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/awsecs"
	"github.com/flokkit/apptown/internal/deploy/fly"
	"github.com/flokkit/apptown/internal/ids"
)

// takeAWSSet uses the same set ledger and item verification as driver-run Fly
// copies. The first task writes this set's immutable database manifest. Only
// after reading it back do we sign one URL per database and launch dump tasks.
func (s *Service) takeAWSSet(ctx context.Context, t *tracker, dep *deployment.Deployment, target *deployment.Target, trigger string) (*Backup, error) {
	held, err := s.heldManaged(ctx, dep, target)
	if err != nil {
		return nil, err
	}
	if len(held.items) != 1 {
		return nil, fmt.Errorf("AWS requires one recorded dedicated Postgres instance")
	}
	item := held.items[0]
	if item.Unresolved != "" {
		return nil, fmt.Errorf("%s", item.Unresolved)
	}
	c, store, err := s.storeClient(ctx)
	if err != nil {
		return nil, err
	}
	driver, err := s.providers.AWSData(ctx, target, dep)
	if err != nil {
		return nil, err
	}
	md := s.providers.ManagedData(target)
	conn, err := md.AppConnection(ctx, item.Instance, "postgres", item.User)
	if err != nil {
		return nil, err
	}
	p, err := project.ByIDIn(ctx, s.db, dep.ProjectID)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	prefix := fmt.Sprintf("%s/%s/%s-%06d", p.Slug, dep.Name, now.Format("2006-01-02T15-04-05Z"), now.Nanosecond()/1000)
	b := &Backup{ProjectID: dep.ProjectID, Deployment: dep.Name, TakenAt: now.Format(time.RFC3339), Trigger: trigger, Actor: t.op.AskedBy, Path: prefix, Store: StoreBackupStore, DataClass: dep.DataClass, StoreEndpoint: store.Endpoint, StoreBucket: store.Bucket, Status: BackupOK}
	b.RunningReleaseID, err = s.backupRelease(ctx, dep, held.dk)
	if err != nil {
		return nil, err
	}
	image := clientImage(item.copyOf(), item.Version)
	env := awsCopyEnv(item, conn)
	env["APPTOWN_RDS_INSTANCE"] = item.Instance
	manifest := BackupItem{Kind: "manifest", Name: "postgres database list", Instance: item.Instance, File: "manifest.json", Client: image, AsUser: item.User, How: "list every non-template database in the dedicated RDS instance"}
	objectURL, err := c.Presign("PUT", store.Bucket, prefix+"/manifest.json", time.Hour)
	if err == nil {
		err = t.allowed(ctx)
	}
	if err == nil {
		err = driver.ListDatabases(ctx, dep.Placement(), image, env, objectURL)
	}
	var list awsecs.DatabaseManifest
	if err == nil {
		var buf bytes.Buffer
		manifest.Bytes, manifest.SHA256, err = c.GetTo(ctx, store.Bucket, prefix+"/manifest.json", &buf)
		if err == nil {
			list, err = awsecs.ParseDatabaseManifest(buf.Bytes(), item.Instance)
		}
	}
	if err != nil {
		manifest.Error = err.Error()
		b.Status = BackupFailed
		b.Error = manifest.Error
	} else {
		b.Bytes += manifest.Bytes
	}
	b.Items = append(b.Items, manifest)
	if err == nil {
		for _, database := range list.Databases {
			if database == awsecs.ProviderOwnedDatabase {
				continue
			}
			digest := sha256.Sum256([]byte(database))
			row := BackupItem{Kind: "need", Name: item.Name, Instance: item.Instance, Database: database, File: fmt.Sprintf("database-%x.%s", digest, item.copyOf().Ext), Copy: CopyDeclared, Format: item.Format, Restore: item.copyOf().Import, Verify: item.copyOf().Verify, Caveat: item.copyOf().Caveat, AsUser: item.User, Client: image, How: item.copyOf().Export + ", one-off ECS task"}
			if list.Empty[database] {
				row.Kind, row.Format, row.Restore, row.Verify = "empty", "", "", ""
				row.File = fmt.Sprintf("database-%x.empty", digest)
				row.How = "no user tables, checked again after listing"
			}
			err = t.allowed(ctx)
			var put string
			if err == nil {
				put, err = c.Presign("PUT", store.Bucket, prefix+"/"+row.File, time.Hour)
			}
			if err == nil {
				connection := conn
				connection.Database = database
				command := item.copyOf().Export
				if list.Empty[database] {
					command = ""
				}
				if list.Empty[database] {
					err = driver.EmptyDatabaseProof(ctx, dep.Placement(), image, awsCopyEnv(item, connection), put)
				} else {
					err = driver.DataJob(ctx, dep.Placement(), image, command, awsCopyEnv(item, connection), put, true)
				}
			}
			if err == nil {
				row.Bytes, row.SHA256, err = c.GetTo(ctx, store.Bucket, prefix+"/"+row.File, io.Discard)
			}
			if err != nil {
				row.Error = err.Error()
				b.Status = BackupFailed
				b.Error = "a database could not be copied: " + database
			} else {
				b.Bytes += row.Bytes
			}
			b.Items = append(b.Items, row)
		}
	}
	id, recordErr := s.backups.Record(context.WithoutCancel(ctx), b, t.op.AskedBy)
	if recordErr != nil {
		return nil, recordErr
	}
	b.ID = id
	if b.Status == BackupOK && trigger != triggerTakeDown {
		if err = t.allowed(ctx); err == nil {
			err = s.pruneSets(ctx, dep.ProjectID, dep.Name, keepFor(dep.DataClass))
		}
		if err != nil {
			return b, err
		}
	}
	return b, nil
}
func awsCopyEnv(item backupPlanItem, conn deploy.Connection) map[string]string {
	_, given := copyEnv(item.copyOf(), conn)
	env := map[string]string{}
	for _, kv := range given {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	return env
}
func (s *Service) readAWSManifest(ctx context.Context, b *Backup, instance string) (awsecs.DatabaseManifest, error) {
	if b.Status != BackupOK {
		return awsecs.DatabaseManifest{}, fmt.Errorf("the final backup set is %s", b.Status)
	}
	var manifest *BackupItem
	for i := range b.Items {
		it := &b.Items[i]
		if it.Kind == "manifest" && it.Instance == instance && it.File == "manifest.json" && it.Error == "" {
			if manifest != nil {
				return awsecs.DatabaseManifest{}, fmt.Errorf("the final set has repeated database manifests")
			}
			manifest = it
		}
	}
	if manifest == nil || manifest.SHA256 == "" || manifest.Bytes == 0 {
		return awsecs.DatabaseManifest{}, fmt.Errorf("the final set has no verified database manifest")
	}
	c, store, err := s.storeOf(ctx, b)
	if err != nil {
		return awsecs.DatabaseManifest{}, err
	}
	var body bytes.Buffer
	n, sum, err := c.GetTo(ctx, store.Bucket, b.Path+"/manifest.json", &body)
	if err != nil {
		return awsecs.DatabaseManifest{}, err
	}
	if n != manifest.Bytes || sum != manifest.SHA256 {
		return awsecs.DatabaseManifest{}, fmt.Errorf("the database manifest differs from the final set's recorded size or digest")
	}
	m, err := awsecs.ParseDatabaseManifest(body.Bytes(), instance)
	if err != nil {
		return m, err
	}
	covered := map[string]bool{}
	for _, it := range b.Items {
		if (it.Kind == "need" || it.Kind == "empty") && it.Instance == instance && it.Error == "" && it.SHA256 != "" && it.Bytes > 0 {
			if it.Kind == "need" && !m.Empty[it.Database] {
				covered[it.Database] = true
			}
			if it.Kind == "empty" && m.Empty[it.Database] {
				var proof bytes.Buffer
				n, sum, err := c.GetTo(ctx, store.Bucket, b.Path+"/"+it.File, &proof)
				if err != nil {
					return m, err
				}
				if n != it.Bytes || sum != it.SHA256 || proof.String() != "empty\n" {
					return m, fmt.Errorf("empty database proof differs from the final set")
				}
				covered[it.Database] = true
			}
		}
	}
	return m, m.Covered(covered)
}
func (s *Service) refusesAWSDeletion(ctx context.Context, b *Backup, it backupPlanItem) (string, string) {
	open, err := s.retained.OpenForInstance(ctx, it.Instance)
	if err != nil {
		return "", err.Error()
	}
	if len(open) > 0 {
		return "", "the instance holds retained data; resolve its retained resource first"
	}
	m, err := s.readAWSManifest(ctx, b, it.Instance)
	if err != nil {
		return "", err.Error()
	}
	return fmt.Sprintf("the final set lists and covers every non-template database (%d listed); only %s is provider-owned", len(m.Databases), awsecs.ProviderOwnedDatabase), ""
}
func (s *Service) drillAWS(ctx context.Context, t *tracker, b *Backup, target *deployment.Target) (bool, string, error) {
	dep, err := s.deployments.ByName(ctx, b.ProjectID, b.Deployment)
	if err != nil {
		return false, "", err
	}
	driver, err := s.providers.AWSData(ctx, target, dep)
	if err != nil {
		return false, "", err
	}
	md := s.providers.ManagedData(target)
	c, store, err := s.storeOf(ctx, b)
	if err != nil {
		return false, "", err
	}
	passed := true
	var details []string
	for _, it := range b.Items {
		if it.Kind == "manifest" {
			if _, err = s.readAWSManifest(ctx, b, it.Instance); err != nil {
				return false, "", err
			}
			continue
		}
		if it.Kind != "need" || it.Error != "" || it.Format == "" {
			continue
		}
		if err = t.allowed(ctx); err != nil {
			return false, "", err
		}
		kind, err := s.catalog.ByCapability(ctx, it.Name)
		if err != nil {
			return false, "", err
		}
		if kind.Copy == nil || kind.Copy.Format != it.Format {
			return false, "", fmt.Errorf("the catalogue cannot read copy format %s", it.Format)
		}
		place := checkPlace("apptown", ids.New(s.now()))
		detail := ""
		good := false
		err = md.NewDatabase(ctx, it.Instance, place, it.AsUser)
		if err == nil {
			var conn deploy.Connection
			conn, err = md.AppConnection(ctx, it.Instance, place, it.AsUser)
			if err == nil {
				var objectURL string
				objectURL, err = c.Presign("GET", store.Bucket, b.Path+"/"+it.File, time.Hour)
				if err == nil {
					env := awsCopyEnv(backupPlanItem{Declared: kind}, conn)
					env["APPTOWN_COPY_SHA256"] = it.SHA256
					err = driver.DataJob(ctx, dep.Placement(), it.Client, fly.ShellCommand(it.Restore, it.Verify), env, objectURL, false)
				}
			}
		}
		if err == nil {
			good = true
			detail = "came back into a new database and passed its check"
		} else {
			detail = err.Error()
			passed = false
		}
		// Remove a place made (or possibly made) by this operation, even after cancellation.
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
		drop := md.DropDatabase(cleanup, it.Instance, place)
		cancel()
		if drop != nil {
			passed = false
			t.left = append(t.left, place+" on "+it.Instance)
			detail += "; drill database could not be removed: " + drop.Error()
		}
		_, err = protectionsql.New(s.db.SQL()).InsertBackupDrill(context.WithoutCancel(ctx), protectionsql.InsertBackupDrillParams{ID: ids.New(s.now()), BackupID: b.ID, ProjectID: b.ProjectID, Deployment: b.Deployment, Need: it.Name, Passed: good && drop == nil, Target: target.Name, Place: place, AsUser: it.AsUser, Detail: detail, OperationID: t.op.ID, DeploymentID: t.op.DeploymentID, DrilledAt: s.now()})
		if err != nil {
			return false, "", err
		}
		details = append(details, it.Database+": "+detail)
	}
	if len(details) == 0 {
		return passed, "every listed database was shown empty; no dump needed checking", nil
	}
	return passed, strings.Join(details, "; "), nil
}

func (s *Service) awsRestoreItems(ctx context.Context, b *Backup, items []backupPlanItem) ([]BackupItem, error) {
	if len(items) != 1 {
		return nil, fmt.Errorf("AWS restore requires the deployment's recorded dedicated instance")
	}
	it := items[0]
	manifest, err := s.readAWSManifest(ctx, b, it.Instance)
	if err != nil {
		return nil, err
	}
	var out []BackupItem
	for _, row := range b.Items {
		if row.Kind == "manifest" {
			continue
		}
		if row.Kind == "empty" && row.Instance == it.Instance && row.Name == it.Name && row.Error == "" && manifest.Empty[row.Database] {
			out = append(out, row)
			continue
		}
		if row.Kind != "need" || row.Instance != it.Instance || row.Name != it.Name || row.Format != it.Format || row.Error != "" || row.Restore == "" {
			return nil, fmt.Errorf("the set has a database copy that does not belong to this deployment's dedicated instance")
		}
		out = append(out, row)
	}
	return out, nil
}
func (s *Service) restoreAWS(ctx context.Context, rec *restoreLog, dep *deployment.Deployment, target *deployment.Target, b *Backup, keepStopped bool) (*RestoreResult, error) {
	held, err := s.heldManaged(ctx, dep, target)
	if err != nil {
		return nil, err
	}
	items, err := s.awsRestoreItems(ctx, b, held.items)
	if err != nil {
		return nil, err
	}
	c, store, err := s.storeOf(ctx, b)
	if err != nil {
		return nil, err
	}
	for _, it := range items {
		n, sum, err := c.GetTo(ctx, store.Bucket, b.Path+"/"+it.File, io.Discard)
		if err != nil {
			return nil, err
		}
		if n != it.Bytes || sum != it.SHA256 {
			return nil, fmt.Errorf("the dump of %s differs from the set's recorded size or digest", it.Database)
		}
	}
	if err = rec.allowed(ctx); err != nil {
		return nil, err
	}
	safety, err := s.takeAWSSet(ctx, rec.t, dep, target, TriggerPreRestore)
	if err != nil {
		return nil, err
	}
	rec.record(ctx, "safetyBackup", safety.ID)
	if safety.Status != BackupOK {
		return nil, fmt.Errorf("the safety set %s failed; nothing was restored", safety.ID)
	}
	driver, err := s.providers.AWSData(ctx, target, dep)
	if err != nil {
		return nil, err
	}
	resume, stopped, err := driver.StopWriters(ctx, dep.Placement(), func(id string) error { return rec.stopping(ctx, id) }, func(id string) { rec.started(ctx, id) })
	if err != nil {
		return nil, err
	}
	result := &RestoreResult{Backup: b.ID, From: b.Deployment, SafetyBackup: safety.ID, Stopped: stopped > 0, LeftStopped: keepStopped}
	md := s.providers.ManagedData(target)
	failed := false
	for _, it := range items {
		row := RestoredItem{Kind: it.Kind, Name: it.Database, Outcome: restoreVerified}
		err = rec.allowed(ctx)
		if err == nil {
			var conn deploy.Connection
			conn, err = md.AppConnection(ctx, it.Instance, it.Database, it.AsUser)
			if err == nil {
				env := awsCopyEnv(held.items[0], conn)
				if it.Kind == "empty" {
					err = driver.CheckEmptyDatabase(ctx, dep.Placement(), it.Client, env)
				} else {
					var get string
					get, err = c.Presign("GET", store.Bucket, b.Path+"/"+it.File, time.Hour)
					if err == nil {
						env["APPTOWN_COPY_SHA256"] = it.SHA256
						err = driver.DataJob(ctx, dep.Placement(), it.Client, fly.ShellCommand(held.items[0].copyOf().Restore, it.Verify), env, get, false)
					}
				}
			}
		}
		if err != nil {
			row.Outcome = restoreFailed
			row.Detail = err.Error()
			failed = true
		}
		result.Items = append(result.Items, row)
		rec.record(ctx, "items", result.Items)
	}
	if !keepStopped {
		rec.begin(ctx, "starting the app again")
		result.NotStarted = resume(context.WithoutCancel(ctx))
		rec.restarted(ctx, result.NotStarted)
	}
	started := !keepStopped && len(result.NotStarted) == 0
	result.Started = &started
	result.Status = restoreStatus(result.Items)
	if failed {
		return result, fmt.Errorf("some databases did not restore; safety set %s holds the previous data", safety.ID)
	}
	return result, nil
}
