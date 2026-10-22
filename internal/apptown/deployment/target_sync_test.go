package deployment

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/dependency"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy/digitalocean"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/docker/dockerfake"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/stock"
)

// syncWorld is an AppTown's targets as a start records them: the built-in
// host, a DigitalOcean account's App Platform target from its connector, and
// an attached Docker host to name as the copy runner.
type syncWorld struct {
	s            *Service
	integrations *integration.Service
}

func newSyncWorld(t *testing.T) *syncWorld {
	t.Helper()
	db := testDB(t)
	dir := t.TempDir()
	projects := project.New(db, nil)
	secrets := secret.NewService(db, secret.Config{DataDir: dir})
	integrations := integration.NewService(db, integration.Dependencies{Projects: projects, Secrets: secrets})
	catalog := dependency.NewCatalog(db, nil)
	if _, _, err := catalog.SyncStock(context.Background(), stock.FS, "dependencies"); err != nil {
		t.Fatal(err)
	}
	w := &syncWorld{integrations: integrations, s: New(context.Background(), db, assembled(db, testRuntime{Scope: docker.Scope{Client: dockerfake.New()}, DataDir: dir, Projects: projects, Secrets: secrets, Integrations: integrations, Needs: catalog, BuiltInTarget: "local"}, Dependencies{}))}
	w.saveConnector(t, "syd")
	if _, err := w.s.Targets.Put(context.Background(), &Target{Name: "runner", Kind: KindDockerHost, Origin: OriginAttached, Status: TargetReady, Address: map[string]any{"host": "ssh://deploy@runner"}}); err != nil {
		t.Fatal(err)
	}
	w.s.SeedTargets(t.Context(), []string{"team=apps.example"})
	return w
}

// saveConnector saves the do-syd cloud-account connector in a region, as
// the connectors page does, followed by the target sync that page runs.
func (w *syncWorld) saveConnector(t *testing.T, region string) {
	t.Helper()
	if _, err := w.integrations.Bindings.Save(context.Background(), "do-syd", digitalocean.CapCloudAccount, "digitalocean", asJSON(map[string]any{"tokenFrom": "DIGITALOCEAN_ACCESS_TOKEN", "region": region, "registry": "forge-test"}), "alice"); err != nil {
		t.Fatal(err)
	}
	w.s.SeedCloudTargets(t.Context(), "alice")
}

func (w *syncWorld) target(t *testing.T, name string) *Target {
	t.Helper()
	target, err := w.s.Targets.ByName(t.Context(), name)
	if err != nil {
		t.Fatal(err)
	}
	return target
}

func refreshes(t *testing.T, w *syncWorld, name string) int {
	t.Helper()
	n := 0
	for _, e := range operationReceipts(t, w.s.db, "target.refreshed", ids.Nil) {
		if e.Subject == name {
			n++
		}
	}
	return n
}

// The live demo's loss (RT-L1): an administrator names the App Platform
// target's copy runner and a domain; the next apptown start and the next save
// of its connector keep both, write nothing and leave the revision where the
// edit put it. A connector change (another region) does reach the target,
// with a new revision and a receipt naming who saved it, and still keeps the
// administrator's settings.
//
// Kills: the start or the connector save replacing the whole address (the
// copy runner goes) or the whole ingress (the domain reverts); an unchanged
// sync moving the revision or writing a receipt; a connector change not
// reaching the target, not moving the revision, or not receipted.
func TestStartsAndConnectorSavesKeepACloudTargetsSettings(t *testing.T) {
	t.Parallel()
	w := newSyncWorld(t)
	ctx := context.Background()
	cloud := w.target(t, "do-syd")
	runner, domain := "runner", "apps.example.org"
	edited, err := w.s.Targets.EditSettings(ctx, "do-syd", TargetSettingsEdit{ExpectedRevision: &cloud.SettingsRevision, CopyRunner: &runner, Domain: &domain}, "alice")
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		name string
		run  func()
	}{
		{"an AppTown start", func() { w.s.SeedTargets(ctx, []string{"team=apps.example"}) }},
		{"a connector save", func() { w.saveConnector(t, "syd") }},
	} {
		step.run()
		after := w.target(t, "do-syd")
		if after.CopyRunner() != runner || after.Ingress["domain"] != domain {
			t.Fatalf("%s dropped the administrator's settings: address %v ingress %v", step.name, after.Address, after.Ingress)
		}
		if after.SettingsRevision != edited.SettingsRevision || refreshes(t, w, "do-syd") != 0 {
			t.Fatalf("%s with nothing changed wrote: revision %d -> %d, %d receipts", step.name, edited.SettingsRevision, after.SettingsRevision, refreshes(t, w, "do-syd"))
		}
	}

	w.saveConnector(t, "sgp")
	moved := w.target(t, "do-syd")
	if moved.Address["region"] != "sgp" || moved.SettingsRevision != edited.SettingsRevision+1 {
		t.Fatalf("a connector change did not reach the target: %v at revision %d", moved.Address, moved.SettingsRevision)
	}
	if moved.CopyRunner() != runner || moved.Ingress["domain"] != domain {
		t.Fatalf("a connector change dropped the administrator's settings: address %v ingress %v", moved.Address, moved.Ingress)
	}
	receipts := operationReceipts(t, w.s.db, "target.refreshed", ids.Nil)
	if len(receipts) != 1 || receipts[0].Actor != "alice" || receipts[0].Subject != "do-syd" {
		t.Fatalf("receipts: %+v", receipts)
	}
	var payload struct {
		Before, After struct {
			Address map[string]any `json:"address"`
		}
	}
	if err := json.Unmarshal(receipts[0].Payload, &payload); err != nil || payload.Before.Address["region"] != "syd" || payload.After.Address["region"] != "sgp" {
		t.Fatalf("receipt: %s %v", receipts[0].Payload, err)
	}
}

// The built-in host takes no edits, so every fact of its record is the
// AppTown's: a start that changes one (its tier domains) refreshes it with a
// revision and a receipt to AppTown; a start that changes nothing writes
// nothing.
//
// Kills: keeping stale built-in facts as though they were an
// administrator's; an unchanged start moving the built-in revision.
func TestAStartRefreshesTheBuiltInHostOnlyWhenItsFactsChange(t *testing.T) {
	t.Parallel()
	w := newSyncWorld(t)
	first := w.target(t, "local")
	w.s.SeedTargets(t.Context(), []string{"team=apps.example"})
	if again := w.target(t, "local"); again.SettingsRevision != first.SettingsRevision || refreshes(t, w, "local") != 0 {
		t.Fatalf("an unchanged start wrote: revision %d -> %d", first.SettingsRevision, again.SettingsRevision)
	}
	// A record from an older AppTown whose own defaults differed.
	stale := *first
	stale.NeedsMode, stale.Capabilities.Previews = "register", false
	if _, err := w.s.Targets.Put(context.Background(), &stale); err != nil {
		t.Fatal(err)
	}
	w.s.SeedTargets(t.Context(), []string{"team=apps.example.net"})
	moved := w.target(t, "local")
	if moved.NeedsMode != "emulators" || !moved.Capabilities.Previews {
		t.Fatalf("the built-in host kept stale settings as though an administrator had set them: %+v", moved)
	}
	if domains, _ := moved.Ingress["domains"].([]any); len(domains) != 1 || domains[0] != "team=apps.example.net" || moved.SettingsRevision != first.SettingsRevision+2 {
		t.Fatalf("the built-in host kept stale facts: %v at revision %d", moved.Ingress, moved.SettingsRevision)
	}
	receipts := operationReceipts(t, w.s.db, "target.refreshed", ids.Nil)
	if len(receipts) != 1 || receipts[0].Actor != "apptown-system" {
		t.Fatalf("receipts: %+v", receipts)
	}
}

// Every setting EditSettings writes survives a sync that proposes another
// value for it. The table must name every field of TargetSettingsEdit, so a
// new setting without a case fails here until Target.keepSettings keeps it.
//
// Kills: a sync that overwrites any one setting; a setting added to the
// edit without being kept.
func TestASyncKeepsEverySettingAnEditWrites(t *testing.T) {
	t.Parallel()
	off, on, empty, mode, runner := false, true, "", "register", "runner"
	cases := map[string]struct {
		proposed *Target
		edit     TargetSettingsEdit
		kept     func(*Target) bool
	}{
		"Domain": { // cleared by the edit: the configuration's domain does not come back
			proposed: &Target{Kind: KindDockerHost, Ingress: map[string]any{"domain": "apps.example"}},
			edit:     TargetSettingsEdit{Domain: &empty},
			kept:     func(t *Target) bool { _, ok := t.Ingress["domain"]; return !ok },
		},
		"PersistentVolumes": {
			proposed: &Target{Kind: KindDockerHost, Capabilities: TargetCapabilities{PersistentVolumes: true}},
			edit:     TargetSettingsEdit{PersistentVolumes: &off},
			kept:     func(t *Target) bool { return !t.Capabilities.PersistentVolumes },
		},
		"Previews": {
			proposed: &Target{Kind: KindDockerHost},
			edit:     TargetSettingsEdit{Previews: &on},
			kept:     func(t *Target) bool { return t.Capabilities.Previews },
		},
		"NeedsMode": {
			proposed: &Target{Kind: KindDockerHost, NeedsMode: "emulators"},
			edit:     TargetSettingsEdit{NeedsMode: &mode},
			kept:     func(t *Target) bool { return t.NeedsMode == mode },
		},
		"Evals": {
			proposed: &Target{Kind: KindDockerHost, Ingress: map[string]any{"domain": "evals.example"}},
			edit:     TargetSettingsEdit{Evals: &on},
			kept:     func(t *Target) bool { return t.ForEvals() },
		},
		"Rehearsals": {
			proposed: &Target{Kind: KindDockerHost},
			edit:     TargetSettingsEdit{Rehearsals: &on},
			kept:     func(t *Target) bool { return t.ForRehearsals() },
		},
		"CopyRunner": {
			proposed: &Target{Kind: KindDOApp, NeedsMode: "managed"},
			edit:     TargetSettingsEdit{CopyRunner: &runner},
			kept:     func(t *Target) bool { return t.CopyRunner() == runner },
		},
	}
	fields := reflect.TypeFor[TargetSettingsEdit]()
	for i := range fields.NumField() {
		if name := fields.Field(i).Name; name != "ExpectedRevision" && cases[name].kept == nil {
			t.Fatalf("TargetSettingsEdit.%s has no case: add one, and keep it in Target.keepSettings", name)
		}
	}
	w := newSyncWorld(t)
	ctx := context.Background()
	for name, c := range cases {
		c.proposed.Name, c.proposed.Origin, c.proposed.Status = "sync-"+name, OriginAttached, TargetReady
		created, err := w.s.Targets.Sync(ctx, c.proposed, "apptown-system")
		if err != nil {
			t.Fatal(err)
		}
		c.edit.ExpectedRevision = &created.SettingsRevision
		edited, err := w.s.Targets.EditSettings(ctx, created.Name, c.edit, "alice")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, err := w.s.Targets.Sync(ctx, c.proposed, "apptown-system"); err != nil {
			t.Fatal(err)
		}
		after := w.target(t, created.Name)
		if !c.kept(after) || after.SettingsRevision != edited.SettingsRevision {
			t.Fatalf("%s: a sync undid the edit: %+v", name, after)
		}
	}
	if n := len(operationReceipts(t, w.s.db, "target.refreshed", ids.Nil)); n != 0 {
		t.Fatalf("syncs that changed no configuration wrote %d receipts", n)
	}
}

// Sync and a settings edit of one target, in either order and at once:
// they queue on the targets lock order (table, then row) instead of
// deadlocking. An edit made first survives a sync (above); a sync that
// changes the configuration first makes an edit from the older revision
// conflict instead of saving over facts it never saw; and many overlapping
// pairs end with every edit either saved or refused as moved, never failed.
//
// Kills: either operation taking the row lock before the table lock (the
// edit's update then waits on the sync's table lock while the sync waits on
// the edit's row: "deadlock detected").
func TestSyncAndASettingsEditQueueInsteadOfDeadlocking(t *testing.T) {
	t.Parallel()
	w := newSyncWorld(t)
	ctx := context.Background()
	stale := w.target(t, "do-syd")
	w.saveConnector(t, "sgp")
	runner := "runner"
	if _, err := w.s.Targets.EditSettings(ctx, "do-syd", TargetSettingsEdit{ExpectedRevision: &stale.SettingsRevision, CopyRunner: &runner}, "alice"); !errors.Is(err, ErrTargetSettingsMoved) {
		t.Fatalf("an edit from before a changing sync: %v", err)
	}
	regions := []string{"syd", "sgp"}
	for round := range 40 {
		current := w.target(t, "do-syd")
		proposed := *current
		proposed.Address = withKeysOf(current.Address, map[string]any{"region": regions[round%2]}, []string{"region"})
		previews := round%2 == 0
		var syncErr, editErr error
		var wg sync.WaitGroup
		wg.Go(func() { _, syncErr = w.s.Targets.Sync(ctx, &proposed, "apptown-system") })
		wg.Go(func() {
			_, editErr = w.s.Targets.EditSettings(ctx, "do-syd", TargetSettingsEdit{ExpectedRevision: &current.SettingsRevision, Previews: &previews}, "alice")
		})
		wg.Wait()
		if syncErr != nil || editErr != nil && !errors.Is(editErr, ErrTargetSettingsMoved) {
			t.Fatalf("round %d: sync %v, edit %v", round, syncErr, editErr)
		}
		after := w.target(t, "do-syd")
		if after.Address["region"] != regions[round%2] || editErr == nil && after.Capabilities.Previews != previews {
			t.Fatalf("round %d: a change was lost: %+v (edit %v)", round, after, editErr)
		}
	}
}

func operationReceipts(t *testing.T, db *database.DB, kind string, project ids.ID) []audit.Entry {
	t.Helper()
	rows, err := audit.Recent(context.Background(), db, audit.Filter{KindPrefix: kind, ProjectID: project, Limit: audit.MaxLimit})
	if err != nil {
		t.Fatal(err)
	}
	return rows
}
