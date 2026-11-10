package dataprotection

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/fly"
	"github.com/flokkit/apptown/internal/deploy/s3bucket"
)

// flyItem is the provider-local item runner below the shared set ledger.
// Its credentials and provider config live only for this operation.
type flyItem struct {
	driver    *fly.Driver
	placement deploy.Placement
	image     string
	env       map[string]string
	user      string
}

func (s *core) heldOnFly(ctx context.Context, dep *deployment.Deployment, t *deployment.Target) (heldItems, error) {
	driver, err := s.providers.FlyData(ctx, t, dep)
	if err != nil {
		return heldItems{}, err
	}
	owned, err := s.owned.Owned(ctx, dep.ID)
	if err != nil {
		return heldItems{}, err
	}
	ms, err := driver.DataMachines(ctx, dep.Placement())
	if errors.Is(err, fly.ErrNoApp) && !ownsFlyVolume(owned) {
		// Never released: no app and no volume, so no data to copy.
		return heldItems{}, errNothingToBackUp
	}
	if err != nil {
		return heldItems{}, fmt.Errorf("%w: %v", errDataNotKnown, err)
	}
	live, err := driver.VolumeIDs(ctx, dep.Placement())
	if err != nil {
		return heldItems{}, fmt.Errorf("%w: %v", errDataNotKnown, err)
	}
	contract, err := s.releaseContract(ctx, dep)
	if err != nil {
		return heldItems{}, err
	}
	held := heldItems{contract: contract}
	for _, o := range owned {
		if o.Kind != "fly-volume" || !o.Data || !live[o.ID] {
			continue
		}
		it := backupPlanItem{Kind: "volume", Name: o.Name, Volume: o.ID, Listed: true, File: o.ID + ".tar.gz", Copy: copyFiles, How: "tar of the volume " + o.ID}
		fi := &flyItem{driver: driver, placement: dep.Placement(), image: s.env.ToolVersions().Images.Sh}
		it.Fly = fi
		if o.Need != "" {
			k, err := s.catalog.ByCapability(ctx, o.Need)
			if err != nil {
				it.Unresolved = unresolvedNeed(o.Need, err)
				held.items = append(held.items, it)
				continue
			}
			it = needItem(k, o.Need, "")
			it.Fly = fi
			it.Volume = o.ID
			it.Listed = true
			if k.Copy != nil {
				it.File = o.ID + "." + k.Copy.Ext
			}
			var current *fly.DataMachine
			for i := range ms {
				m := &ms[i]
				if m.Role != "need" || m.Group != o.Need {
					continue
				}
				for _, v := range m.Volumes {
					if v.ID == o.ID && v.Path == k.Local.Data {
						current = m
					}
				}
			}
			if current == nil || current.State != "started" || !current.Known || k.Copy == nil || k.Copy.Client == "" || k.Copy.Export == "" {
				it.Unresolved = "no running need with a declared client export mounts volume " + o.ID
			} else {
				params := map[string]string{}
				for name, value := range k.Local.Env {
					if token := wholeToken.FindStringSubmatch(value); token != nil {
						if v, ok := current.Env[name]; ok {
							params[token[1]] = v
						}
					}
				}
				conn, _ := k.LocalConnection(params, params)
				conn.Host = current.ID + ".vm." + appOf(dep) + ".internal"
				// The Machine's recorded source image pins the client major version,
				// independently of today's contract or catalogue defaults.
				version := k.Defaults["version"]
				prefix, suffix, ok := strings.Cut(k.Local.Image, "{version}")
				if ok && strings.HasPrefix(current.Image, prefix) && strings.HasSuffix(current.Image, suffix) {
					version = strings.TrimSuffix(strings.TrimPrefix(current.Image, prefix), suffix)
				}
				fi.image = clientImage(k.Copy, version)
				_, env := copyEnv(k.Copy, conn)
				fi.env = map[string]string{}
				for _, kv := range env {
					key, value, _ := strings.Cut(kv, "=")
					fi.env[key] = value
				}
				fi.user = conn.User
			}
		}
		held.items = append(held.items, it)
	}
	if len(held.items) == 0 && (contract.PublicPart().DataDir != "" || len(contract.Needs) > 0) {
		return heldItems{}, fmt.Errorf("%w: the release declares data but its list records no Fly volume", errDataNotKnown)
	}
	if len(held.items) == 0 {
		return heldItems{}, errNothingToBackUp
	}
	return held, nil
}

func ownsFlyVolume(owned []deploy.Owned) bool {
	for _, o := range owned {
		if o.Kind == "fly-volume" {
			return true
		}
	}
	return false
}

func appOf(dep *deployment.Deployment) string {
	v, _ := dep.Placement().Handle["appName"].(string)
	return v
}

func (s *Service) copyFlyItem(ctx context.Context, p backupPlanItem, c *s3bucket.Client, bucket, key string) (int64, string, string, string, error) {
	fi := p.Fly
	objectURL, err := c.Presign("PUT", bucket, key, time.Hour)
	if err != nil {
		return 0, "", "", "", err
	}
	if p.Kind == "volume" {
		err = fi.driver.CopyVolume(ctx, fi.placement, p.Volume, fi.image, objectURL)
	} else {
		err = fi.driver.DataJob(ctx, fi.placement, fi.image, p.Declared.Copy.Export, fi.env, "", objectURL, true)
	}
	if err != nil {
		return 0, "", "", "", err
	}
	n, sum, err := c.GetTo(ctx, bucket, key, io.Discard)
	return n, sum, fi.image, fi.user, err
}

func (s *Service) restoreFly(ctx context.Context, dep *deployment.Deployment, held heldItems, moves []restoreMove, untouched []backupPlanItem, paths []string, b, safety *Backup, rec *restoreLog, keepStopped bool) (*RestoreResult, error) {
	// The shared door already checked every file's digest and took a whole
	// safety set. All writers stop before either database or files change.
	driver := held.items[0].Fly.driver
	resume, stopped, err := driver.StopWriters(ctx, dep.Placement(), func(id string) error { return rec.stopping(ctx, id) }, func(id string) { rec.started(ctx, id) })
	if err != nil {
		if resume != nil {
			resume(context.WithoutCancel(ctx))
		}
		return nil, err
	}
	result := &RestoreResult{Backup: b.ID, From: b.Deployment, SafetyBackup: safety.ID, Stopped: stopped > 0}
	c, store, err := s.storeOf(ctx, b)
	if err != nil {
		resume(context.WithoutCancel(ctx))
		return nil, err
	}
	var failed bool
	for i, m := range moves {
		_ = paths[i] // read-back and digest validation completed before the safety set
		it := RestoredItem{Kind: m.into.Kind, Name: m.into.Name, Caveat: m.item.Caveat}
		if err = rec.allowed(ctx); err == nil {
			url, e := c.Presign("GET", store.Bucket, b.Path+"/"+m.item.File, time.Hour)
			err = e
			if err == nil {
				fi := m.into.Fly
				if m.into.Kind == "volume" {
					err = driver.RestoreVolume(ctx, dep.Placement(), m.into.Volume, fi.image, url, m.item.SHA256)
					it.Outcome = restoreCopied
					it.Detail = "files copied, not verified: nothing checks that the app reads them"
				} else if m.item.Restore == "" {
					err = fmt.Errorf("the set records no restore command")
				} else {
					err = driver.DataJob(ctx, dep.Placement(), fi.image, fly.ShellCommand(m.item.Restore, m.item.Verify), restoreFlyEnv(fi.env, m.item.SHA256), "", url, false)
					it.Outcome = restoreLoaded
					if m.item.Verify != "" {
						it.Outcome = restoreVerified
					}
				}
			}
		}
		if err != nil {
			it.Outcome = restoreFailed
			it.Detail = err.Error()
			failed = true
		}
		result.Items = append(result.Items, it)
		rec.record(ctx, "items", result.Items)
	}
	for _, p := range untouched {
		result.Items = append(result.Items, RestoredItem{Kind: p.Kind, Name: p.Name, Outcome: restoreSkipped, Detail: "the set holds no good copy of this item; it is left as it is"})
	}
	if !keepStopped {
		result.NotStarted = resume(context.WithoutCancel(ctx))
	}
	started := !keepStopped && stopped > 0 && len(result.NotStarted) == 0
	result.Started = &started
	result.LeftStopped = keepStopped
	result.Status = restoreStatus(result.Items)
	if failed {
		return result, fmt.Errorf("some items did not restore; safety set %s holds what was there before", safety.ID)
	}
	return result, nil
}

func restoreFlyEnv(env map[string]string, sum string) map[string]string {
	out := map[string]string{"APPTOWN_COPY_SHA256": sum}
	for k, v := range env {
		out[k] = v
	}
	return out
}

func (s *Service) finalBackupOnFly(ctx context.Context, dep *deployment.Deployment, t *deployment.Target, actor string) (finalCopy, error) {
	held, err := s.heldOnFly(ctx, dep, t)
	if err == errNothingToBackUp {
		return finalCopy{}, nil
	}
	if err != nil {
		return finalCopy{}, err
	}
	store, err := s.Store(ctx)
	if err != nil {
		return finalCopy{}, err
	}
	if store == nil {
		return finalCopy{}, &finalCopyUnavailable{why: errNoStore.Error()}
	}
	if why := unresolvedOf(held.items); why != "" {
		return finalCopy{}, &finalCopyUnavailable{why: why}
	}

	driver := held.items[0].Fly.driver
	resume, _, err := driver.StopWriters(ctx, dep.Placement(), nil, nil)
	refuse := func(err error) (finalCopy, error) {
		if resume != nil {
			if failed := resume(context.WithoutCancel(ctx)); len(failed) > 0 {
				err = fmt.Errorf("%w; could not restart: %s", err, strings.Join(failed, "; "))
			}
		}
		return finalCopy{}, err
	}
	if err != nil {
		return refuse(err)
	}
	p, err := project.ByIDIn(ctx, s.db, dep.ProjectID)
	if err != nil {
		return refuse(err)
	}
	b, err := s.takeSet(ctx, held.dk, dep.ProjectID, p.Slug, dep.Name, held.items, triggerTakeDown, actor)
	if err != nil {
		return refuse(err)
	}
	if why := b.uncovered(held.items); why != "" {
		return refuse(fmt.Errorf("%w: %s. To take it down anyway, take it down without a final copy: the Fly app and its volumes are then kept, with every Machine stopped", errFinalBackupIncomplete, why))
	}
	return finalCopy{set: b, parts: stoppedParts{providerRestart: resume}}, nil
}

// failedFinalSet says why the deployment's newest set, when it is a final
// set that failed, did not cover its data; empty when the newest set is
// anything else. That failure is why a person may then choose to take the
// deployment down without a final copy although every item resolves.
func (s *Service) failedFinalSet(ctx context.Context, dep *deployment.Deployment) (string, error) {
	sets, err := s.backups.ForDeployment(ctx, dep.ProjectID, dep.Name, 1)
	if err != nil {
		return "", fmt.Errorf("reading the deployment's newest backup: %w", err)
	}
	if len(sets) == 0 || sets[0].Trigger != triggerTakeDown || sets[0].Status == BackupOK {
		return "", nil
	}
	return fmt.Sprintf("the final backup %s failed: %s", sets[0].ID, setShortfall(&sets[0])), nil
}
