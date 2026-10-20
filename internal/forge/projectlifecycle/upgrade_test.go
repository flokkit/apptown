package projectlifecycle

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/gitexec/gittest"
	"flokkit.io/forge/internal/ids"
)

type upgradeWork struct {
	open     []workflow.OpenRun
	branches map[int64]string
}

func (w upgradeWork) OpenRunsEverywhere(context.Context) ([]workflow.OpenRun, error) {
	return w.open, nil
}
func (w upgradeWork) OpenChangeBranches(context.Context, ids.ID) (map[int64]string, error) {
	return w.branches, nil
}

// The upgrade renames .forge to .apptown on the default branch and on open
// changes' branches, one attributed commit each, records main as moved by
// the upgrade, moves refs/forge/ to refs/apptown/, leaves a branch with both
// folders for a person, and changes nothing when run again.
func TestTheUpgradeRenamesTheFolderOnMainAndOpenChangeBranchesOnce(t *testing.T) {
	t.Parallel()
	f := newOwnerFixture(t, true)
	ctx := context.Background()
	p, err := f.lifecycle.Create(ctx, Draft{Slug: "shop", Name: "Shop", Actor: "bls"})
	if err != nil {
		t.Fatal(err)
	}
	// The project's history from before the rename, seeded through the
	// owner's door like any imported repository.
	src := t.TempDir()
	write := func(dir, name, body string) {
		t.Helper()
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gittest.RunAsTester(t, src, "init", "-q", "-b", "main")
	write(src, ".gitignore", ".forge/*\n!.forge/app.yaml\nnode_modules/\n")
	write(src, ".forge/app.yaml", "parts:\n  web:\n    port: 8080\n")
	write(src, "main.go", "package main\n")
	gittest.RunAsTester(t, src, "add", "-A")
	gittest.RunAsTester(t, src, "commit", "-qm", "before the rename")
	before := gittest.RunAsTester(t, src, "rev-parse", "HEAD")
	bundle := filepath.Join(t.TempDir(), "shop.bundle")
	gittest.RunAsTester(t, src, "bundle", "create", "-q", bundle, "refs/heads/main")
	body, err := os.Open(bundle)
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	if err := f.lifecycle.SeedFromBundle(ctx, p, body, "main", "bls"); err != nil {
		t.Fatal(err)
	}
	seeded, err := project.ByIDIn(ctx, f.db, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	p = &seeded
	hub := hubDir(f.lifecycle.dataDir, p.Slug)
	// Change branches and a candidate ref as changes made before the rename
	// left them: written in the hub, since no owner door writes the old
	// names any more.
	gittest.Run(t, hub, "branch", "forge/change-1", before)
	gittest.Run(t, hub, "update-ref", "refs/forge/candidates/step-1", before)
	other := t.TempDir()
	gittest.RunAsTester(t, other, "clone", "-q", hub, ".")
	gittest.RunAsTester(t, other, "checkout", "-q", "-b", "forge/change-2")
	write(other, ".apptown/app.yaml", "parts: {}\n")
	gittest.RunAsTester(t, other, "add", "-f", ".apptown/app.yaml")
	gittest.RunAsTester(t, other, "commit", "-qm", "both")
	both := gittest.RunAsTester(t, other, "rev-parse", "HEAD")
	gittest.Run(t, hub, "fetch", "-q", other, "forge/change-2:forge/change-2")

	work := upgradeWork{branches: map[int64]string{1: "forge/change-1", 2: "forge/change-2"}}
	report, err := f.lifecycle.UpgradeToAppTown(ctx, "admin", work)
	if err != nil {
		t.Fatal(err)
	}
	row := report.Projects[slices.IndexFunc(report.Projects, func(r UpgradedProject) bool { return r.Slug == "shop" })]
	if row.Error != "" || !slices.Equal(row.Renamed, []string{"main", "forge/change-1"}) || row.Refs != 1 {
		t.Fatalf("report %+v, want main and forge/change-1 renamed and one ref moved", row)
	}
	if why := row.Refused["forge/change-2"]; !strings.Contains(why, "both") {
		t.Fatalf("a branch with both folders: %q, want it left for a person", why)
	}
	for _, branch := range []string{"main", "forge/change-1"} {
		files := gittest.Run(t, hub, "ls-tree", "-r", "--name-only", branch)
		if !strings.Contains(files, ".apptown/app.yaml") || strings.Contains(files, ".forge/") {
			t.Fatalf("%s holds %s, want .apptown/app.yaml and no .forge/", branch, files)
		}
		if parent := gittest.Run(t, hub, "rev-parse", branch+"^"); parent != before {
			t.Fatalf("%s's rename commit has parent %s, want %s", branch, parent, before)
		}
		if msg := gittest.Run(t, hub, "log", "-1", "--format=%B", branch); !strings.Contains(msg, upgradeSubject) || !strings.Contains(msg, "AppTown-Actor: admin") {
			t.Fatalf("%s: commit message %q", branch, msg)
		}
		ignore := gittest.Run(t, hub, "show", branch+":.gitignore")
		if !strings.Contains(ignore, ".apptown/*\n!.apptown/app.yaml") || !strings.HasSuffix(ignore, ".forge/") {
			t.Fatalf("%s .gitignore %q", branch, ignore)
		}
	}
	if got := gittest.Run(t, hub, "rev-parse", "forge/change-2"); got != both {
		t.Fatalf("the refused branch moved to %s", got)
	}
	if _, err := os.Stat(filepath.Join(hub, ".apptown", "app.yaml")); err != nil {
		t.Fatalf("the hub's checkout did not follow main: %v", err)
	}
	wantMainRecorded(t, f.db, f.lifecycle.dataDir, p.ID, scm.ViaUpgrade)
	if refs := gittest.Run(t, hub, "for-each-ref", "--format=%(refname)", "refs/forge/", "refs/apptown/"); refs != "refs/apptown/candidates/step-1" {
		t.Fatalf("refs %q, want the candidate under refs/apptown/", refs)
	}

	tips := gittest.Run(t, hub, "for-each-ref", "--format=%(objectname) %(refname)")
	again, err := f.lifecycle.UpgradeToAppTown(ctx, "admin", work)
	if err != nil {
		t.Fatal(err)
	}
	row = again.Projects[slices.IndexFunc(again.Projects, func(r UpgradedProject) bool { return r.Slug == "shop" })]
	if len(row.Renamed) != 0 || row.Refs != 0 || row.Error != "" {
		t.Fatalf("second run %+v, want nothing renamed", row)
	}
	if now := gittest.Run(t, hub, "for-each-ref", "--format=%(objectname) %(refname)"); now != tips {
		t.Fatalf("second run moved refs:\n%s\nwas\n%s", now, tips)
	}
}

// While any run is open the upgrade changes nothing and names the runs.
func TestTheUpgradeIsRefusedWhileARunIsOpen(t *testing.T) {
	t.Parallel()
	f := newOwnerFixture(t, false)
	_, err := f.lifecycle.UpgradeToAppTown(context.Background(), "admin", upgradeWork{open: []workflow.OpenRun{{Project: "shop", Number: 4}}})
	var open ErrUpgradeRunsOpen
	if !errors.As(err, &open) || !strings.Contains(err.Error(), "shop run 4") {
		t.Fatalf("err %v, want the open run named", err)
	}
}

// The text rename touches the contract's names and nothing that only looks
// like them.
func TestRenameContractTextRenamesTheContractNamesOnly(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"read .forge/app.yaml":                       "read .apptown/app.yaml",
		"(.forge) and .forge.":                       "(.apptown) and .apptown.",
		"FORGE_ORIGINS and FORGE_BROWSER_ENV_KEYS":   "APPTOWN_ORIGINS and APPTOWN_BROWSER_ENV_KEYS",
		"window.__FORGE_ENV__ from /_forge/env.js":   "window.__APPTOWN_ENV__ from /_apptown/env.js",
		"apiVersion: forge.flokkit.io/v1":            "apiVersion: apptown.dev/v1",
		"x.forge .forge-cache FORGE_TOKEN my_forge/": "x.forge .forge-cache FORGE_TOKEN my_forge/",
	} {
		if got := RenameContractText(in); got != want {
			t.Errorf("%q became %q, want %q", in, got, want)
		}
	}
}
