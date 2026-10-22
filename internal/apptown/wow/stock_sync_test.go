package wow

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/flokkit/apptown/internal/apptown/wow/internal/wowsql"
	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/stock"
)

// One stock sync: every kind goes
// through Sync with the same contract — idempotent on re-sync, a version
// and a receipt on change for versioned kinds, a custom row under a stock
// name kept and counted, never shadowed. These run the release's own files.

func stockService(t *testing.T) *Service {
	t.Helper()
	return New(testDB(t), Dependencies{Stock: stock.FS})
}

// stockLibrary is stockService with the stock workflows, guidance and
// policies in the library, as boot syncs them before the packs that pin them.
func stockLibrary(t *testing.T) *Service {
	t.Helper()
	s := stockService(t)
	for _, c := range []SyncCounts{s.SyncStockWorkflows(t.Context()), s.SyncStockGuidance(t.Context()), s.SyncStockPolicies(t.Context())} {
		if c.Errors != 0 {
			t.Fatalf("%s: %+v", c.Kind, c)
		}
	}
	return s
}

func TestStockSyncIsIdempotent(t *testing.T) {
	t.Parallel()
	s := stockService(t)
	first := s.SyncStockWorkflows(t.Context())
	if first.Added == 0 || first.Errors != 0 || first.Skipped != 0 {
		t.Fatalf("first sync must seed the stock workflows cleanly: %+v", first)
	}
	again := s.SyncStockWorkflows(t.Context())
	if again.Added != 0 || again.Updated != 0 || again.Unchanged != first.Added {
		t.Fatalf("re-sync of unchanged stock must change nothing: %+v", again)
	}
	for _, c := range []SyncCounts{s.SyncStockPolicies(t.Context()), s.SyncStockGuidance(t.Context()), s.SyncStockPacks(t.Context())} {
		if c.Errors != 0 || c.Added == 0 {
			t.Fatalf("%s: first sync must seed cleanly: %+v", c.Kind, c)
		}
	}
	for _, c := range []SyncCounts{s.SyncStockPolicies(t.Context()), s.SyncStockGuidance(t.Context()), s.SyncStockPacks(t.Context())} {
		if c.Added != 0 || c.Updated != 0 || c.Errors != 0 {
			t.Fatalf("%s: re-sync of unchanged stock must change nothing: %+v", c.Kind, c)
		}
	}
}

// A versioned kind: changed stock content becomes a new library version
// with a receipt naming it; the old version stays readable.
func TestStockSyncVersionsChangedWorkflow(t *testing.T) {
	t.Parallel()
	s := stockService(t)
	items, _ := readStockWorkflows(stock.FS)
	for _, item := range items {
		body := item.Content
		if item.Name == "quick-change" {
			body = "# an older release"
		}
		if err := s.workflows.StockInsert(t.Context(), item.Name, item.Data.(string), body); err != nil {
			t.Fatal(err)
		}
	}
	stored, err := libraryRows(s.db).WorkflowByName(context.Background(), "quick-change")
	if err != nil {
		t.Fatal(err)
	}
	before := stored.LatestVersion
	c := s.SyncStockWorkflows(t.Context())
	if c.Updated != 1 {
		t.Fatalf("exactly the changed workflow is updated: %+v", c)
	}
	moved, err := libraryRows(s.db).WorkflowByName(context.Background(), "quick-change")
	if after := moved.LatestVersion; err != nil || after != before+1 {
		t.Fatalf("a changed stock workflow gets a new version: %d -> %d", before, after)
	}
	old, err := libraryRows(s.db).WorkflowVersion(context.Background(), wowsql.WorkflowVersionParams{Name: "quick-change", Version: before})
	if err != nil || old.Yaml != "# an older release" {
		t.Fatal("the previous version row must survive")
	}
	audits := 0
	for _, receipt := range wowReceipts(t, s.db, "library.workflow.updated", "quick-change") {
		if receipt.Kind == "library.workflow.updated" { // the read filters by prefix; the kind is exact
			audits++
		}
	}
	if audits != 1 {
		t.Fatalf("a version bump is audited once, got %d", audits)
	}
}

// A custom row under a stock name is yours: kept as it is, counted, and
// the stock one is not installed — for every kind.
func TestStockSyncKeepsYours(t *testing.T) {
	t.Parallel()
	s := stockService(t)
	packs, err := ReadStockPacks(stock.FS)
	if err != nil || len(packs) == 0 {
		t.Fatalf("the release ships packs: %v", err)
	}
	stockPack := packs[0].Name
	if _, err := s.PublishPack(t.Context(), stockPack, "mine", `{"guidance":[]}`, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveLibraryPolicy(t.Context(), LibraryPolicy{Name: "scan-failon-ceiling", Description: "mine", Rule: "scanFailOnCeiling", With: map[string]any{"max": "critical"}}, "bls"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.PublishWorkflow(t.Context(), sprintf(laneYAML, "quick-change", "mine", ""), "bls"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateBlock(t.Context(), "prose/terse-style", "Our own terseness.", "prose", "bls"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []SyncCounts{s.SyncStockWorkflows(t.Context()), s.SyncStockGuidance(t.Context()), s.SyncStockPolicies(t.Context()), s.SyncStockPacks(t.Context())} {
		if c.Kept != 1 || c.Errors != 0 {
			t.Fatalf("%s: the custom row under a stock name must be kept, once: %+v", c.Kind, c)
		}
	}
	ctx := context.Background()
	if p, err := libraryRows(s.db).PackByName(ctx, stockPack); err != nil || p.Description != "mine" || p.Origin != "" {
		t.Fatalf("the custom pack must be untouched: %+v %v", p, err)
	}
	if p, err := libraryRows(s.db).LibraryPolicyByName(ctx, "scan-failon-ceiling"); err != nil || p.Description != "mine" || p.Origin != "" {
		t.Fatalf("the custom policy must be untouched: %+v %v", p, err)
	}
	if w, err := libraryRows(s.db).WorkflowByName(ctx, "quick-change"); err != nil || w.Description != "mine" || w.Origin != "" {
		t.Fatalf("the custom workflow must be untouched: %+v %v", w, err)
	}
	if b, err := s.BlockByName(ctx, "prose/terse-style"); err != nil || b.Origin != "" || b.LatestVersion != 1 {
		t.Fatalf("the custom block must be untouched: %+v %v", b, err)
	}
}

// Unversioned kinds are overwritten in place, and a stock pack keeps its
// switch (enabled) across syncs.
func TestStockSyncOverwritesUnversioned(t *testing.T) {
	t.Parallel()
	s := stockLibrary(t)
	s.SyncStockPacks(t.Context())
	packs, _ := ReadStockPacks(stock.FS)
	name := packs[0].Name
	pack, err := s.packs.ByName(t.Context(), name)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := wowsql.New(s.db.SQL()).RefreshPackContents(context.Background(), wowsql.RefreshPackContentsParams{ID: pack.ID, Description: pack.Description, Spec: json.RawMessage(`{}`)}); err != nil || n != 1 {
		t.Fatalf("drift stock contents: %d %v", n, err)
	}
	if n, err := wowsql.New(s.db.SQL()).SetPackEnabled(context.Background(), wowsql.SetPackEnabledParams{Name: name, Enabled: false, At: time.Now().UTC()}); err != nil || n != 1 {
		t.Fatalf("store disabled switch without an unrelated audit: %d %v", n, err)
	}
	c := s.SyncStockPacks(t.Context())
	if c.Updated != 1 {
		t.Fatalf("the drifted stock pack is overwritten: %+v", c)
	}
	synced, err := libraryRows(s.db).PackByName(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	if spec, enabled := string(synced.SpecJson), synced.Enabled; spec == "{}" || enabled {
		t.Fatalf("spec follows the release, the switch is the instance's: %s enabled=%v", spec, enabled)
	}
	if packVersions := len(wowReceipts(t, s.db, "", name)); packVersions != 0 {
		t.Fatal("unversioned kinds are not audited on overwrite")
	}
}

// A release publishes a changed stock pack without rewriting its history.
func TestStockPackReleasesCutVersions(t *testing.T) {
	t.Parallel()
	s := stockLibrary(t)
	const older = `{"name":"go-small","guidance":[]}`
	if version, err := s.PublishPack(t.Context(), "go-small", "old release", older, "stock"); err != nil || version != 1 {
		t.Fatalf("old stock: version=%d %v", version, err)
	}
	ctx := context.Background()
	published, err := libraryRows(s.db).PackByName(ctx, "go-small")
	if err != nil {
		t.Fatal(err)
	}
	original, err := libraryRows(s.db).PackVersion(ctx, wowsql.PackVersionParams{PackID: published.ID, Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	counts := s.SyncStockPacks(t.Context())
	if counts.Errors != 0 || counts.Updated != 1 {
		t.Fatalf("new release: %+v", counts)
	}
	pack, err := libraryRows(s.db).PackByName(ctx, "go-small")
	if err != nil || pack.LatestVersion != 2 {
		t.Fatalf("latest version: %+v %v", pack, err)
	}
	versions, err := libraryRows(s.db).PackVersions(ctx, pack.ID)
	if err != nil || len(versions) != 2 {
		t.Fatalf("pack versions: %+v %v", versions, err)
	}
	first, err := libraryRows(s.db).PackVersion(ctx, wowsql.PackVersionParams{PackID: pack.ID, Version: 1})
	var kept, want any
	if err != nil || json.Unmarshal(first.SpecJson, &kept) != nil || json.Unmarshal(original.SpecJson, &want) != nil || !reflect.DeepEqual(kept, want) {
		t.Fatalf("the old release was rewritten: %s %v", first.SpecJson, err)
	}
}

// A release that only rewords a stock pack's description refreshes the
// words in place: no new pack version, so no project is offered an update
// for words alone, and every page reading the pack shows the new words.
func TestStockPackRewordingRefreshesInPlace(t *testing.T) {
	t.Parallel()
	s := stockLibrary(t)
	s.SyncStock(t.Context())
	packs, err := ReadStockPacks(stock.FS)
	if err != nil {
		t.Fatal(err)
	}
	shipped := packs[0]
	before, err := s.packs.ByName(t.Context(), shipped.Name)
	if err != nil {
		t.Fatal(err)
	}
	// An older release worded the pack differently.
	if err := s.packs.Describe(t.Context(), shipped.Name, "older words"); err != nil {
		t.Fatal(err)
	}
	if c := s.SyncStockPacks(t.Context()); c.Updated != 0 || c.Errors != 0 {
		t.Fatalf("rewording alone must not publish the pack again: %+v", c)
	}
	after, err := s.packs.ByName(t.Context(), shipped.Name)
	if err != nil {
		t.Fatal(err)
	}
	if after.Description != shipped.Description {
		t.Fatalf("the release's words are shown: %q, want %q", after.Description, shipped.Description)
	}
	if after.LatestVersion != before.LatestVersion {
		t.Fatalf("rewording cut a pack version: %d → %d", before.LatestVersion, after.LatestVersion)
	}
}

// changedStock is the release's stock with some files replaced.
type changedStock struct {
	fs.FS
	files fstest.MapFS
}

func (c changedStock) Open(name string) (fs.File, error) {
	if _, ok := c.files[name]; ok {
		return c.files.Open(name)
	}
	return c.FS.Open(name)
}

// A pack version pins the policies it names by their definitions. A release
// that changes only a policy publishes a new version of each pack naming it,
// carrying the new definition, recommended no differently; the version that
// carried the old definition keeps it, and applying it installs it; a second
// sync publishes nothing more.
//
// Kills: pins that covered only blocks and workflows, so a policy-only
// release changed the library and published no pack version; and pack
// setup reading the library's current definition for an old version.
func TestAPolicyOnlyReleasePublishesThePacksThatNameIt(t *testing.T) {
	t.Parallel()
	s := stockService(t)
	s.SyncStock(t.Context())
	pack := "go-small"
	before, err := s.Pack(t.Context(), pack)
	if err != nil {
		t.Fatal(err)
	}
	applied := func(version int64) map[string]any {
		t.Helper()
		setup, err := PackSetupIn(t.Context(), s.db, pack, version)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range setup.Policies {
			if p.Name == "deploy-requires-scan" {
				return p.With
			}
		}
		t.Fatalf("%s v%d installs no deploy-requires-scan", pack, version)
		return nil
	}
	if got := applied(before.Version); got["requires"] != "scan" {
		t.Fatalf("v%d installs %v", before.Version, got)
	}

	raw, err := fs.ReadFile(stock.FS, "policies.yaml")
	if err != nil {
		t.Fatal(err)
	}
	tightened := strings.Replace(string(raw), "with: { uses: deploy, requires: scan }", "with: { uses: deploy, requires: tests }", 1)
	release := New(s.db, Dependencies{Stock: changedStock{FS: stock.FS, files: fstest.MapFS{"policies.yaml": {Data: []byte(tightened)}}}})
	release.SyncStock(t.Context())
	latest, err := libraryRows(s.db).PackByName(t.Context(), pack)
	if err != nil {
		t.Fatal(err)
	}
	if latest.LatestVersion <= before.Version {
		t.Fatalf("a policy-only release published no %s version: still v%d", pack, latest.LatestVersion)
	}
	if got := applied(latest.LatestVersion); got["requires"] != "tests" {
		t.Fatalf("the new version installs %v", got)
	}
	if got := applied(before.Version); got["requires"] != "scan" {
		t.Fatalf("the old version changed with the library: installs %v", got)
	}
	release.SyncStock(t.Context())
	if again, _ := libraryRows(s.db).PackByName(t.Context(), pack); again.LatestVersion != latest.LatestVersion {
		t.Fatalf("a second sync published again: v%d -> v%d", latest.LatestVersion, again.LatestVersion)
	}
}

// A new publication that includes one from before policies were pinned (a
// policy named, no pin) pins that policy at the library's definition when it
// is published: applying it later installs what it was published with, even
// after the library changes, and the change is a new publication of it.
//
// Kills: copying an included publication's unpinned policy names as they
// were, which left a newly published pack following the library.
func TestANewPackPinsAPolicyItsLegacyParentOnlyNamed(t *testing.T) {
	t.Parallel()
	s := New(testDB(t), Dependencies{})
	ctx := t.Context()
	policy := func(requires string) LibraryPolicy {
		return LibraryPolicy{Name: "gate", Description: "a gate", Rule: "requireStepBefore", With: map[string]any{"uses": "deploy", "requires": requires}}
	}
	// A save names only what the library has, so the parent is published
	// with its policy, which then leaves the library: the recut keeps the
	// name unpinned, as a publication from before pinning held it.
	if err := s.SaveLibraryPolicy(ctx, policy("old"), "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PublishPack(ctx, "parent", "", `{"policies":[{"name":"gate"}]}`, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteLibraryPolicy(ctx, "gate", "alice"); err != nil {
		t.Fatal(err)
	}
	if recuts, err := s.CutPacksOnPinChange(ctx); err != nil || len(recuts) != 1 || recuts[0].Version != 2 {
		t.Fatalf("the parent without its policy: %+v %v", recuts, err)
	}
	if err := s.SaveLibraryPolicy(ctx, policy("scan"), "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PublishPack(ctx, "ours", "", `{"includes":"parent@2"}`, ""); err != nil {
		t.Fatal(err)
	}
	installs := func(pack string, version int64) any {
		t.Helper()
		setup, err := PackSetupIn(ctx, s.db, pack, version)
		if err != nil || len(setup.Policies) != 1 {
			t.Fatalf("%s v%d: %+v %v", pack, version, setup, err)
		}
		return setup.Policies[0].With["requires"]
	}
	if got := installs("ours", 1); got != "scan" {
		t.Fatalf("ours v1 installs %v", got)
	}
	if err := s.SaveLibraryPolicy(ctx, policy("tests"), "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CutPacksOnPinChange(ctx); err != nil {
		t.Fatal(err)
	}
	if got := installs("ours", 1); got != "scan" {
		t.Fatalf("ours v1 followed the library: installs %v", got)
	}
	latest, err := libraryRows(s.db).PackByName(ctx, "ours")
	if err != nil || latest.LatestVersion < 2 || installs("ours", latest.LatestVersion) != "tests" {
		t.Fatalf("the library change is a new publication of ours: v%d %v", latest.LatestVersion, err)
	}
}

// A parent publication from before policies were pinned may hold a policy's
// full definition with no pin. A pack including it inherits that definition
// exactly, whatever the library says when the pack is published.
//
// Kills: treating every unpinned inherited policy as named only, which
// replaced the parent's definition with the library's.
func TestANewPackKeepsTheDefinitionItsLegacyParentHeld(t *testing.T) {
	t.Parallel()
	s := New(testDB(t), Dependencies{})
	ctx := t.Context()
	policy := func(requires string) LibraryPolicy {
		return LibraryPolicy{Name: "gate", Description: "a gate", Rule: "requireStepBefore", With: map[string]any{"uses": "deploy", "requires": requires}}
	}
	if err := s.SaveLibraryPolicy(ctx, policy("scan"), "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PublishPack(ctx, "parent", "", `{"policies":[{"name":"gate"}]}`, ""); err != nil {
		t.Fatal(err)
	}
	// v2 repeats v1's contents (the definition requiring scan) with pins
	// that hold no policies: a publication from before policies were
	// pinned. No operation makes one any more, so it is written directly.
	q := libraryRows(s.db)
	parent, err := q.PackByName(ctx, "parent")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := q.PackVersions(ctx, parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	const legacy = 2
	v1 := rows[len(rows)-1]
	if _, err := q.InsertPackVersion(ctx, wowsql.InsertPackVersionParams{ID: ids.New(time.Now()), PackID: parent.ID, Version: legacy, Spec: v1.SpecJson, Pins: []byte(`{"blocks":{},"workflows":{}}`),
		Declaration: v1.DeclarationJson, IncludedPack: v1.IncludedPack, IncludedVersion: v1.IncludedVersion, BasedOn: v1.Version, At: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveLibraryPolicy(ctx, policy("tests"), "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PublishPack(ctx, "child", "", fmt.Sprintf(`{"includes":"parent@%d"}`, legacy), ""); err != nil {
		t.Fatal(err)
	}
	setup, err := PackSetupIn(ctx, s.db, "child", 1)
	if err != nil || len(setup.Policies) != 1 || setup.Policies[0].With["requires"] != "scan" {
		t.Fatalf("child v1 must install the parent's definition (scan): %+v %v", setup, err)
	}
}
