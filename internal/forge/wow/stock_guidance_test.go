package wow

import (
	"context"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"flokkit.io/forge/stock"

	"flokkit.io/forge/internal/check"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/ids"
)

func TestSyncStockGuidance(t *testing.T) {
	t.Parallel()
	s := stockService(t)
	s.SyncStockGuidance(t.Context())

	// The embedded stock set is seeded, marked stock, and carries the current
	// (compose + needs) skill/implementation body — not the retired app.json.
	skill, err := s.BlockByName(t.Context(), "skill/implementation")
	if err != nil {
		t.Fatalf("skill/implementation must be seeded: %v", err)
	}
	if skill.Origin != "stock" {
		t.Fatalf("skill/implementation must be origin=stock, got %q", skill.Origin)
	}
	body, _ := s.BlockVersionBody(t.Context(), skill.ID, skill.LatestVersion)
	if !strings.Contains(body, ".apptown/app.yaml") || strings.Contains(body, "run contract to .apptown/compose.yaml") {
		t.Fatalf("stock skill/implementation body is stale (should teach app.yaml)")
	}

	// Every exported stock file is embedded and synced — guards against the
	// go:embed `**` gotcha silently dropping nested (lang/selection/**) files.
	all, err := s.Blocks(t.Context(), BlockFilter{})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, b := range all {
		if b.Origin == "stock" {
			n++
		}
	}
	want := 0
	_ = fs.WalkDir(stock.FS, "guidance", func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(path, ".md") {
			want++
		}
		return nil
	})
	if want == 0 || n != want {
		t.Fatalf("expected %d stock blocks synced from the embed, got %d", want, n)
	}

	// Idempotent: a second sync with unchanged files bumps nothing.
	v1 := skill.LatestVersion
	s.SyncStockGuidance(t.Context())
	skill2, _ := s.BlockByName(t.Context(), "skill/implementation")
	if skill2.LatestVersion != v1 {
		t.Fatalf("re-sync must not bump an unchanged block: %d -> %d", v1, skill2.LatestVersion)
	}
}

// A release cuts the next version of a stock block and stops there: the
// project stays on the version it adopted (design/subsystems/ways-of-working-versions.md
// §9 step 1, ruling 1). The old auto-upgrade is what turned a wording change
// into a fleet-wide gate change.
func TestStockGuidanceReleaseDoesNotMoveImporters(t *testing.T) {
	t.Parallel()
	s := stockService(t)
	s.SyncStockGuidance(t.Context())
	blk, _ := s.BlockByName(t.Context(), "prose/terse-style")

	// A project pins the current latest.
	pid, err := database.Transact(context.Background(), s.db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "p", Name: "P"})
	})
	if err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: Pin an importer before private stock synchronization and assert it never moves; adoption owns the writer but imports wow, so this owner test cannot call it without an import cycle.
	if _, err := s.db.Exec(`INSERT INTO guidance_imports (id, project_id, position, block_id, version, disabled) VALUES (?,?,0,?,?,false)`,
		ids.New(time.Now()), pid, blk.ID, blk.LatestVersion); err != nil {
		t.Fatal(err)
	}
	before := blk.LatestVersion

	// A release changes the shipped body: one block moves.
	cur, _ := s.BlockVersionBody(t.Context(), blk.ID, before)
	c := s.syncGuidance(t.Context(), []StockBlock{{Name: "prose/terse-style", Tags: "prose,style", Body: cur + "\nNew shipped rule.\n"}})
	if c.Updated != 1 {
		t.Fatalf("the changed block is updated: %+v", c)
	}
	after, _ := s.BlockByName(t.Context(), "prose/terse-style")
	if after.LatestVersion != before+1 {
		t.Fatalf("changed stock body must bump version: %d -> %d", before, after.LatestVersion)
	}
	var pin int
	// sql-guarantee: The project's import pin lives in guidance_imports, which the wow library does not read; the guarantee is that a stock sync never moves it.
	_ = s.db.QueryRow(`SELECT version FROM guidance_imports WHERE project_id=? AND block_id=?`, pid, blk.ID).Scan(&pin)
	if pin != before {
		t.Fatalf("the importer stays on the version it adopted (%d), moved to %d", before, pin)
	}
}

// A user block created after a stock block of the same shape stays editable;
// stock blocks are guarded read-only (the handler keys on Origin=="stock").
func TestStockVsUserOrigin(t *testing.T) {
	t.Parallel()
	s := stockService(t)
	s.SyncStockGuidance(t.Context())
	stock, _ := s.BlockByName(t.Context(), "arch/boring-technology")
	if stock.Origin != "stock" {
		t.Fatalf("pack-referenced block must be stock, got %q", stock.Origin)
	}
	// A hand-authored block is not stock.
	u, err := s.CreateBlock(t.Context(), "team/our-way", "Our house style.", "team", "bls")
	if err != nil {
		t.Fatal(err)
	}
	ub, _ := s.BlockByName(t.Context(), u.Name)
	if ub.Origin != "" {
		t.Fatalf("user-authored block must have empty origin, got %q", ub.Origin)
	}
}

// A shipped block's front matter names it, its tags, its checks, its tier
// and its conflicts; an unknown tier is refused.
func TestParseStockGuidance(t *testing.T) {
	t.Parallel()
	b, err := ParseStockGuidance("---\nname: data/x\ntags: data, sql\nchecks: [{\"name\":\"c\"}]\ntier: platform\nconflicts: data/y\n---\n\nThe body.\n")
	if err != nil || b.Name != "data/x" || b.Tags != "data,sql" || b.ChecksJSON != `[{"name":"c"}]` || b.Tier != "platform" || b.Conflicts != "data/y" || b.Body != "The body.\n" {
		t.Fatalf("parsed: %+v %v", b, err)
	}
	for _, raw := range []string{"no front matter", "---\nname: x\n", "---\ntags: t\n---\nbody", "---\nname: x\ntier: gold\n---\nbody"} {
		if _, err := ParseStockGuidance(raw); err == nil {
			t.Fatalf("refused: %q", raw)
		}
	}
}

// Everything the release ships reads under the strict rules a team's own
// content is held to: no unknown key, no misspelt field, every check
// declaration valid. The boot sync logs and skips a stock item that fails
// (a bad vendor file never breaks startup), so a failure would otherwise
// ship as a missing workflow, pack, policy or check.
func TestEveryStockFileReadsStrictly(t *testing.T) {
	t.Parallel()
	if workflows, skipped := readStockWorkflows(stock.FS); skipped != 0 || len(workflows) == 0 {
		t.Errorf("stock workflows: read %d, skipped %d", len(workflows), skipped)
	}
	if packs, err := ReadStockPacks(stock.FS); err != nil || len(packs) == 0 {
		t.Errorf("stock packs: %d, %v", len(packs), err)
	}
	if policies, err := ReadStockPolicies(stock.FS); err != nil || len(policies) == 0 {
		t.Errorf("stock policies: %d, %v", len(policies), err)
	}
	blocks, skipped := ReadStockGuidance(stock.FS, nil)
	if skipped != 0 || len(blocks) == 0 {
		t.Fatalf("read %d stock blocks, skipped %d", len(blocks), skipped)
	}
	for _, b := range blocks {
		if b.ChecksJSON == "" {
			continue
		}
		if _, err := check.ParseDeclarations(b.ChecksJSON); err != nil {
			t.Errorf("%s: %v", b.Name, err)
		}
	}
}

// The strict readers refuse what the lenient ones let through: an unknown
// key, a pack defining a policy inline, a policy option its rule does not
// take, and an unknown key in a check.
func TestStrictReadersRefuseUnknownShapes(t *testing.T) {
	t.Parallel()
	files := func(name, body string) fstest.MapFS { return fstest.MapFS{name: {Data: []byte(body)}} }
	if _, err := ReadStockPacks(files("packs.yaml", "packs:\n  - name: p\n    auto-adopt: true\n")); err == nil {
		t.Error("a pack with auto-adopt was read")
	}
	if _, err := ReadStockPacks(files("packs.yaml", "packs:\n  - name: p\n    policies:\n      - {name: x, rule: requireStepBefore}\n")); err == nil {
		t.Error("a pack defining a policy inline was read")
	}
	if _, err := ReadStockPolicies(files("policies.yaml", "policies:\n  - name: x\n    rule: requireStepBefore\n    with: {uses: deploy, requires: scan, also: 1}\n")); err == nil {
		t.Error("a policy option its rule does not take was read")
	}
	if _, err := ReadStockPolicies(files("policies.yaml", "policies:\n  - name: x\n    rule: requireStepBefore\n    rulz: 1\n")); err == nil {
		t.Error("a policy with an unknown key was read")
	}
	if _, err := ParseStockGuidance("---\nname: x\nchecsk: []\n---\nbody\n"); err == nil {
		t.Error("guidance with an unknown front-matter key was read")
	}
	if _, err := check.ParseDeclarations(`[{"name":"c","sh":"true","severty":"high"}]`); err == nil {
		t.Error("a check with an unknown key was read")
	}
}

// A pack and a block may state the format they are written in; absent and
// apptown.dev/v1 read the same, and another version is refused by name, so a
// later format can be told apart from this one.
func TestPacksAndBlocksTakeAnOptionalFormatVersion(t *testing.T) {
	t.Parallel()
	files := func(body string) fstest.MapFS { return fstest.MapFS{"packs.yaml": {Data: []byte(body)}} }
	if packs, err := ReadStockPacks(files("packs:\n  - name: p\n    apiVersion: apptown.dev/v1\n")); err != nil || len(packs) != 1 {
		t.Fatalf("a pack stating v1: %v %v", packs, err)
	}
	if _, err := ReadStockPacks(files("packs:\n  - name: p\n    apiVersion: apptown.dev/v2\n")); err == nil || !strings.Contains(err.Error(), "apptown.dev/v2") {
		t.Fatalf("a pack stating v2 was read: %v", err)
	}
	if b, err := ParseStockGuidance("---\napiVersion: apptown.dev/v1\nname: x\n---\nbody\n"); err != nil || b.Name != "x" {
		t.Fatalf("a block stating v1: %+v %v", b, err)
	}
	if _, err := ParseStockGuidance("---\napiVersion: apptown.dev/v2\nname: x\n---\nbody\n"); err == nil || !strings.Contains(err.Error(), "apptown.dev/v2") {
		t.Fatalf("a block stating v2 was read: %v", err)
	}
}
