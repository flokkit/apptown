package adoption

import (
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/stock"
)

// releaseStock is the release's own stock files with extra ones laid over
// them: an earlier release that still shipped what this one withdraws.
func releaseStock(t *testing.T, extra map[string]string) fstest.MapFS {
	t.Helper()
	out := fstest.MapFS{}
	err := fs.WalkDir(stock.FS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := fs.ReadFile(stock.FS, path)
		out[path] = &fstest.MapFile{Data: raw}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	for path, text := range extra {
		if path == "policies.yaml" || path == "packs.yaml" {
			text = string(out[path].Data) + text // a further entry in the shipped list
		}
		out[path] = &fstest.MapFile{Data: []byte(text)}
	}
	return out
}

const retiredLane = `apiVersion: apptown.dev/v1
kind: Workflow
metadata: {name: retired-lane, description: an old lane}
spec:
  kind: lifecycle
  role: working
  stages:
    - name: work
      steps: [{name: check, uses: script, with: {run: "true"}}]
`

// An upgrade to a release that no longer ships a stock workflow, guidance
// block and policy: none of them is offered any more, a custom block is,
// and a project that adopted the block and selected the workflow keeps
// both — the block still rendered with its checks and read as no longer
// shipped, the workflow still in the revisions adoption builds and in the
// project's selected workflows, labelled — while a project that had not
// selected the workflow is not offered it. (Fresh installs are the same
// minus the history; the upgrade is the case that needs the rule.)
//
// Kills: offering every stored stock entry (the library lists, a project's
// lanes, the platform blocks a project is given); filtering adoption's own enumeration (the revision would drop the
// selected workflow); dropping or not labelling the adopted block.
func TestAnUpgradeStopsOfferingWithdrawnStockAndKeepsWhatProjectsUse(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	old := releaseStock(t, map[string]string{
		"retired-lane.yaml":                   retiredLane,
		"guidance/data/retired-rule.md":       "---\nname: data/retired-rule\ntags: data\nchecks: [{\"name\": \"no-db-files\", \"expects\": \"no database files\", \"severity\": \"high\", \"evaluator\": \"sh\", \"sh\": \"true\", \"title\": \"No database files\"}]\n---\nKeep records in one file.\n",
		"guidance/deploy/retired-platform.md": "---\nname: deploy/retired-platform\ntier: platform\n---\nAn old rule for every app.\n",
		"policies.yaml":                       "  - name: retired-policy\n    description: an old rule\n    rule: requireStepBefore\n    with: { uses: deploy, requires: scan }\n",
	})
	before := wow.New(db, wow.Dependencies{Stock: old})
	before.SyncStock(t.Context())
	s := New(db, Dependencies{Ways: before})
	user, other := adoptionTestProject(t, db, "user", "User"), adoptionTestProject(t, db, "other", "Other")
	rule, err := before.BlockByName(t.Context(), "data/retired-rule")
	if err != nil {
		t.Fatal(err)
	}
	pins := []wow.ResolvedPin{{BlockID: rule.ID, Name: rule.Name, Version: rule.LatestVersion}}
	if _, err := s.ReplaceGuidance(t.Context(), user, new(int64), pins, "adopt", "alice"); err != nil {
		t.Fatal(err)
	}
	if err := s.SelectLane(t.Context(), user, "retired-lane", true, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := before.CreateBlock(t.Context(), "house/own-rule", "ours", "house", "alice"); err != nil {
		t.Fatal(err)
	}

	// AppTown restarts on the new release.
	after := wow.New(db, wow.Dependencies{Stock: stock.FS, ToolLines: stock.Versions()})
	after.SyncStock(t.Context())
	s = New(db, Dependencies{Ways: after})

	blocks, err := after.Blocks(t.Context(), wow.BlockFilter{})
	if err != nil {
		t.Fatal(err)
	}
	offered := map[string]bool{}
	for _, b := range blocks {
		offered[b.Name] = true
	}
	if offered["data/retired-rule"] || !offered["house/own-rule"] || !offered["data/networked-database"] {
		t.Fatalf("the guidance library offers the withdrawn block, or not the kept ones: %v", offered)
	}
	missing, err := s.PlatformBlocksMissingFrom(t.Context(), other)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range missing {
		if b.Name == "deploy/retired-platform" {
			t.Fatal("a withdrawn platform block is still given to projects")
		}
	}
	policies, err := after.LibraryPolicies(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range policies {
		if p.Name == "retired-policy" {
			t.Fatal("the policy library offers the withdrawn policy")
		}
	}

	view, err := s.GuidanceSnapshot(t.Context(), user)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Imports) != 1 || view.Imports[0].Name != "data/retired-rule" || !view.Imports[0].Withdrawn {
		t.Fatalf("the adopted block is not kept and read as no longer shipped: %+v", view.Imports)
	}
	if !strings.Contains(view.Text, "Keep records in one file.") {
		t.Fatalf("the adopted block is no longer rendered: %q", view.Text)
	}
	if checks, err := after.BlockVersionChecks(t.Context(), rule.ID, rule.LatestVersion); err != nil || !strings.Contains(checks, "no-db-files") {
		t.Fatalf("the adopted version's checks are gone: %q %v", checks, err)
	}

	// A further revision is built from the library's enumeration.
	current, err := s.Current(t.Context(), user)
	if err != nil {
		t.Fatal(err)
	}
	revised, err := s.ReplaceGuidance(t.Context(), user, &current.Revision, pins, "edit", "alice")
	if err != nil {
		t.Fatal(err)
	}
	inRevision := false
	for _, w := range revised.Receipt.Workflows {
		inRevision = inRevision || w.Name == "retired-lane"
	}
	if !inRevision {
		t.Fatalf("the selected withdrawn workflow left the project's revision: %+v", revised.Receipt.Workflows)
	}
	selected, err := s.AvailableWorkflows(t.Context(), user)
	if err != nil {
		t.Fatal(err)
	}
	labelled := false
	for _, w := range selected {
		labelled = labelled || (w.Name == "retired-lane" && w.Withdrawn)
	}
	if !labelled {
		t.Fatalf("the project's selected workflows lost or did not label the withdrawn one: %+v", selected)
	}
	elsewhere, err := s.AvailableWorkflows(t.Context(), other)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range elsewhere {
		if w.Name == "retired-lane" {
			t.Fatal("a project that never selected the withdrawn workflow is given it")
		}
	}
	library, err := after.Workflows(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range library {
		if w.Name == "retired-lane" && !w.Withdrawn {
			t.Fatal("the library does not mark the withdrawn workflow, so offers cannot leave it out")
		}
		if w.Name == "land" && w.Withdrawn {
			t.Fatal("a shipped workflow is marked withdrawn")
		}
	}
}

// A stock pack the release no longer ships is turned off at the upgrade's
// sync, so new projects are not offered it; a project on it stays on it.
//
// Kills: leaving the pack offered; deleting it or moving its projects.
func TestAnUpgradeTurnsOffAStockPackItNoLongerShips(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	old := releaseStock(t, map[string]string{
		"packs.yaml": "  - name: retired-pack\n    description: an old pack\n    guidance: [repo/no-build-artifacts]\n    workflows: [land]\n",
	})
	before := wow.New(db, wow.Dependencies{Stock: old})
	before.SyncStock(t.Context())
	s := New(db, Dependencies{Ways: before})
	pid := adoptionTestProject(t, db, "p", "P")
	if err := s.SetPack(t.Context(), pid, "retired-pack"); err != nil {
		t.Fatal(err)
	}

	after := wow.New(db, wow.Dependencies{Stock: stock.FS, ToolLines: stock.Versions()})
	after.SyncStock(t.Context())
	offered, err := after.OfferedPacks(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range offered {
		if p.Name == "retired-pack" {
			t.Fatal("new projects are still offered the withdrawn pack")
		}
	}
	if len(offered) == 0 {
		t.Fatal("no pack is offered at all")
	}
	if _, err := after.Pack(t.Context(), "retired-pack"); err != nil {
		t.Fatalf("the withdrawn pack is no longer readable: %v", err)
	}
	current, err := New(db, Dependencies{Ways: after}).Current(t.Context(), pid)
	if err != nil || current.Pack != "retired-pack" {
		t.Fatalf("the project left the withdrawn pack: %+v %v", current, err)
	}
}
