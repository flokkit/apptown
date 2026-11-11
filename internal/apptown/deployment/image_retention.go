package deployment

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/oci"
)

const ImageRetentionJob = "image-retention"

// backupImageKeeper is Data protection's kept-copy evidence. Every read must
// succeed before the pass deletes anything, including on unrelated stores.
type backupImageKeeper interface {
	KeptBackupReleases(context.Context) ([]ids.ID, error)
}

type ImageRetentionResult struct {
	At      time.Time          `json:"at"`
	Unknown string             `json:"unknown,omitempty"`
	Stores  []ImageStoreResult `json:"stores"`
}
type ImageStoreResult struct {
	Store      string        `json:"store"`
	Targets    []ids.ID      `json:"targets,omitempty"`
	Hub        bool          `json:"hub,omitempty"`
	Deleted    []string      `json:"deleted"`
	Left       []deploy.Left `json:"left,omitempty"`
	Unknown    string        `json:"unknown,omitempty"`
	Collection string        `json:"collection,omitempty"`
}

type imageRetentionSnapshot struct {
	bundles     []*ReleaseBundle
	releases    []*Release
	running     map[ids.ID]bool
	backups     map[ids.ID]bool
	deployments []ProjectDeployment
}

func (s *Service) imageRetentionSnapshot(ctx context.Context) (imageRetentionSnapshot, error) {
	out := imageRetentionSnapshot{running: map[ids.ID]bool{}, backups: map[ids.ID]bool{}}
	if s.backupImages == nil {
		return out, fmt.Errorf("kept backups could not be read: no data protection reader")
	}
	backups, err := s.backupImages.KeptBackupReleases(ctx)
	if err != nil {
		return out, fmt.Errorf("reading kept backups: %w", err)
	}
	for _, id := range backups {
		out.backups[id] = true
	}
	q := deploymentsql.New(s.db.SQL())
	bundles, err := q.RetentionBundles(ctx)
	if err != nil {
		return out, err
	}
	for _, row := range bundles {
		b, err := bundleRecord(row, nil)
		if err != nil {
			return out, err
		}
		out.bundles = append(out.bundles, b)
	}
	releases, err := q.RetentionReleases(ctx)
	if err != nil {
		return out, err
	}
	for _, row := range releases {
		r, err := s.Releases.record(row, nil)
		if err != nil {
			return out, err
		}
		out.releases = append(out.releases, r)
	}
	running, err := q.RetentionRunningBundles(ctx)
	if err != nil {
		return out, err
	}
	for _, id := range running {
		out.running[id] = true
	}
	out.deployments, err = s.Deployments.All(ctx)
	return out, err
}

// retentionKeep computes bundle identities, never individual part names. A
// shared image is removed from expired only after all store users are unioned.
func retentionKeep(now time.Time, snapshot imageRetentionSnapshot, policy integration.HubPolicy) (map[ids.ID]map[ids.ID]bool, map[ids.ID]bool) {
	spoke := map[ids.ID]map[ids.ID]bool{}
	hub := maps.Clone(snapshot.running)
	successful := map[ids.ID]int{}
	for _, r := range snapshot.releases {
		if spoke[r.DeploymentID] == nil {
			spoke[r.DeploymentID] = map[ids.ID]bool{}
		}
		keep := snapshot.running[r.BundleID] || snapshot.backups[r.ID]
		if r.State == Released {
			successful[r.DeploymentID]++
			keep = keep || successful[r.DeploymentID] <= 3 || (r.ReleasedAt.Valid && !r.ReleasedAt.Time.Before(now.AddDate(0, 0, -7)))
		}
		// Waiting approvals also retain their selected bundle.
		if r.State == Releasing || r.State == releaseWaiting {
			keep = true
		}
		if keep {
			spoke[r.DeploymentID][r.BundleID] = true
			hub[r.BundleID] = true
		}
	}
	counts := map[ids.ID]int{}
	for _, b := range snapshot.bundles {
		counts[b.ProjectID]++
		at, _ := time.Parse(time.RFC3339Nano, b.CreatedAt)
		if counts[b.ProjectID] <= policy.KeepReleases || !at.Before(now.AddDate(0, 0, -policy.KeepDays)) {
			hub[b.ID] = true
		}
	}
	return spoke, hub
}

type imageRetentionStore struct {
	store     deploy.ImageStore
	placement deploy.Placement
	result    ImageStoreResult
	recorded  map[string]bool
	kept      map[string]bool
	collector deploy.ImageCollector
}

func bundleImages(b *ReleaseBundle) []string {
	var refs []string
	for _, images := range []map[string]string{b.Parts, b.Init} {
		for _, ref := range images {
			refs = append(refs, ref)
		}
	}
	return slices.Compact(slices.Sorted(slices.Values(refs)))
}
func imageDifference(recorded, kept map[string]bool) []string {
	var expired []string
	for ref := range recorded {
		if !kept[ref] {
			expired = append(expired, ref)
		}
	}
	return slices.Sorted(slices.Values(expired))
}

// retainImages is the daily pass. It records one receipt even when its inputs
// are unreadable. Provider failures never fail a release or become empty stores.
func (s *Service) retainImages(ctx context.Context) (ImageRetentionResult, error) {
	result := ImageRetentionResult{At: s.now(), Stores: []ImageStoreResult{}}
	snapshot, err := s.imageRetentionSnapshot(ctx)
	if err != nil {
		result.Unknown = err.Error()
		return result, s.recordImageRetention(ctx, result)
	}
	policy := integration.HubPolicy{KeepDays: 90, KeepReleases: 10}
	hubName := ""
	if s.env.integrations != nil {
		hub, p, err := s.env.integrations.Hub(ctx)
		if err != nil {
			result.Unknown = err.Error()
			return result, s.recordImageRetention(ctx, result)
		}
		if hub != nil {
			policy = p
			hubName = hub.Name
		}
	}
	spokeKeep, hubKeep := retentionKeep(result.At, snapshot, policy)
	bundles := map[ids.ID]*ReleaseBundle{}
	for _, b := range snapshot.bundles {
		bundles[b.ID] = b
	}
	stores := map[string]*imageRetentionStore{}
	// Resolve every store identity before trimming. If one cannot be identified,
	// its shared users cannot be excluded, so no spoke may be deleted this pass.
	identityUnknown := false
	for _, dep := range snapshot.deployments {
		t, err := s.Targets.ByID(ctx, dep.TargetID)
		if err != nil {
			result.Unknown = err.Error()
			return result, s.recordImageRetention(ctx, result)
		}
		d, err := s.providers.Driver(ctx, t, &dep.Deployment)
		if err != nil {
			result.Stores = append(result.Stores, ImageStoreResult{Store: t.Name, Targets: []ids.ID{t.ID}, Unknown: err.Error()})
			identityUnknown = true
			continue
		}
		store, ok := d.(deploy.ImageStore)
		if !ok {
			continue
		}
		placement := dep.Placement()
		key, err := store.ImageStore(ctx, placement)
		if err != nil || key == "" {
			why := "the image store could not be identified"
			if err != nil {
				why = err.Error()
			}
			result.Stores = append(result.Stores, ImageStoreResult{Store: t.Name, Targets: []ids.ID{t.ID}, Unknown: why})
			identityUnknown = true
			continue
		}
		group := stores[key]
		if group == nil {
			group = &imageRetentionStore{store: store, placement: placement, result: ImageStoreResult{Store: key}, recorded: map[string]bool{}, kept: map[string]bool{}}
			group.collector, _ = d.(deploy.ImageCollector)
			stores[key] = group
		}
		if !slices.Contains(group.result.Targets, t.ID) {
			group.result.Targets = append(group.result.Targets, t.ID)
		}
		for _, r := range snapshot.releases {
			if r.DeploymentID == dep.ID {
				if b := bundles[r.BundleID]; b != nil {
					for _, ref := range bundleImages(b) {
						group.recorded[ref] = true
						if spokeKeep[dep.ID][b.ID] {
							group.kept[ref] = true
						}
					}
				}
			}
		}
		// Observed attempt inputs remain protected even if a release row is absent.
		for _, b := range snapshot.bundles {
			if snapshot.running[b.ID] {
				for _, ref := range bundleImages(b) {
					group.kept[ref] = true
				}
			}
		}
	}
	collectors := map[string]deploy.ImageCollector{}
	for _, key := range slices.Sorted(maps.Keys(stores)) {
		group := stores[key]
		if identityUnknown {
			group.result.Unknown = "another image store could not be identified; shared keep sets are unknown"
		} else {
			deleted, left, err := group.store.TrimImages(ctx, group.placement, imageDifference(group.recorded, group.kept), slices.Sorted(maps.Keys(group.kept)))
			group.result.Deleted, group.result.Left = deleted, left
			if err != nil {
				group.result.Unknown = err.Error()
			}
			if len(deleted) > 0 && group.collector != nil {
				key, err := group.collector.CollectionStore(ctx)
				if err != nil {
					group.result.Collection = err.Error()
				} else {
					collectors[key] = group.collector
				}
			}
		}
		result.Stores = append(result.Stores, group.result)
	}
	// Registry-wide collection waits for every App Platform release, checked
	// again after deletes because releases may have been admitted during the pass.
	for _, key := range slices.Sorted(maps.Keys(collectors)) {
		collection := ImageStoreResult{Store: key}
		busy, err := deploymentsql.New(s.db.SQL()).AppPlatformReleaseInFlight(ctx)
		switch {
		case err != nil:
			collection.Collection = "not started: " + err.Error()
		case busy:
			collection.Collection = "not started: an App Platform release is in flight"
		default:
			if err := collectors[key].CollectImages(ctx); err != nil {
				collection.Collection = "not started: " + err.Error()
			} else {
				collection.Collection = "garbage collection requested"
			}
		}
		result.Stores = append(result.Stores, collection)
	}
	if hubName != "" {
		result.Stores = append(result.Stores, s.trimHub(ctx, hubName, snapshot.bundles, hubKeep)...)
	}
	return result, s.recordImageRetention(ctx, result)
}

func (s *Service) trimHub(ctx context.Context, name string, bundles []*ReleaseBundle, keep map[ids.ID]bool) []ImageStoreResult {
	type repository struct {
		project        ids.ID
		recorded, kept map[string]bool
	}
	repositories := map[string]*repository{}
	for _, b := range bundles {
		if !b.ByDigest() {
			continue
		}
		repo := repositories[b.BuiltFrom.Registry]
		if repo == nil {
			repo = &repository{project: b.ProjectID, recorded: map[string]bool{}, kept: map[string]bool{}}
			repositories[b.BuiltFrom.Registry] = repo
		}
		for _, ref := range bundleImages(b) {
			repo.recorded[ref] = true
			if keep[b.ID] {
				repo.kept[ref] = true
			}
		}
	}
	var results []ImageStoreResult
	for _, repositoryName := range slices.Sorted(maps.Keys(repositories)) {
		repo := repositories[repositoryName]
		entry := ImageStoreResult{Hub: true, Store: "hub/" + name + "/" + repositoryName}
		publication, err := s.env.ReleaseRepository(ctx, repo.project)
		if err != nil {
			entry.Unknown = err.Error()
		} else if publication.Name != repositoryName {
			entry.Unknown = "the bundle's repository is outside the current hub; its images are kept"
		} else {
			deleted, left, err := oci.Trim(ctx, publication, imageDifference(repo.recorded, repo.kept), slices.Sorted(maps.Keys(repo.kept)))
			entry.Deleted = deleted
			for _, image := range left {
				entry.Left = append(entry.Left, deploy.Left{What: image.Reference, Why: image.Why})
			}
			if err != nil {
				entry.Unknown = err.Error()
			}
		}
		results = append(results, entry)
	}
	return results
}
func (s *Service) recordImageRetention(ctx context.Context, result ImageRetentionResult) error {
	return audit.At(result.At).Record(ctx, s.db, "apptown-system", ids.Nil, "images.retained", "instance", result)
}

func (w JobWorker) RetainImages(ctx context.Context, _ string, _ map[string]any) (map[string]any, error) {
	result, err := w.Service.retainImages(ctx)
	return map[string]any{"retention": result}, err
}

// RetentionSummary is the last pass for an instance hub or a target page.
func (r ImageRetentionResult) Summary(target ids.ID, hub bool) string {
	if r.At.IsZero() {
		return "image retention has not run"
	}
	if r.Unknown != "" {
		return "image retention unknown: " + r.Unknown
	}
	deleted := 0
	var left []string
	for _, store := range r.Stores {
		if (hub && store.Hub) || (!hub && slices.Contains(store.Targets, target)) {
			deleted += len(store.Deleted)
			if store.Unknown != "" {
				left = append(left, store.Unknown)
			}
			for _, image := range store.Left {
				left = append(left, image.Why)
			}
		}
	}
	summary := fmt.Sprintf("last applied %s, %d images deleted", r.At.UTC().Format(time.RFC3339), deleted)
	if len(left) > 0 {
		summary = "last pass " + r.At.UTC().Format(time.RFC3339) + ", " + strings.Join(slices.Compact(slices.Sorted(slices.Values(left))), "; ")
	}
	return summary
}

// LastImageRetention reads the single receipt of the most recent pass.
func (s *Service) LastImageRetention(ctx context.Context) (ImageRetentionResult, error) {
	var out ImageRetentionResult
	rows, err := audit.Recent(ctx, s.db, audit.Filter{KindPrefix: "images.retained", Subject: "instance", Limit: 1})
	if err != nil {
		return out, err
	}
	if len(rows) == 0 {
		return out, nil
	}
	err = json.Unmarshal(rows[0].Payload, &out)
	return out, err
}
