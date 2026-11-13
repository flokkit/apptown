package workflowexec

import (
	"fmt"
	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/fix"
	"github.com/flokkit/apptown/stock"
)

func TestShippedGoUVAndRubyConvertersEmitStandardListings(t *testing.T) {
	blocks, skipped := wow.ReadStockGuidance(stock.FS, nil)
	if skipped != 0 {
		t.Fatal(skipped)
	}
	for _, tc := range []struct {
		name, ecosystem string
		files           func(string) map[string]string
	}{
		{"go", "Go", func(v string) map[string]string {
			return map[string]string{"go.mod": fmt.Sprintf("module example.test/app\n\ngo 1.23\nrequire example.test/dependency v%s\nreplace example.test/dependency => ./dependency\n", v), "dependency/go.mod": "module example.test/dependency\n\ngo 1.23\n"}
		}},
		{"python", "PyPI", func(v string) map[string]string {
			return map[string]string{"uv.lock": fmt.Sprintf("version = 1\n[[package]]\nname = \"example\"\nversion = \"%s\"\nsource = { registry = \"https://pypi.org/simple\" }\n", v)}
		}},
		{"ruby", "RubyGems", func(v string) map[string]string {
			return map[string]string{"Gemfile": "source 'https://rubygems.org'\ngem 'example'\n", "Gemfile.lock": fmt.Sprintf("GEM\n  remote: https://rubygems.org/\n  specs:\n    example (%s)\n\nPLATFORMS\n  ruby\n\nDEPENDENCIES\n  example\n\nBUNDLED WITH\n   1.17.2\n", v)}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var ds []fix.Declaration
			for _, b := range blocks {
				if b.Name == "lang/"+tc.name+"/dependency-updates" {
					var err error
					ds, err = fix.Parse(b.FixesJSON)
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			if len(ds) != 1 || ds[0].Ecosystem != tc.ecosystem {
				t.Fatalf("declaration %+v", ds)
			}
			folder := t.TempDir()
			var sides [2][]fix.Listing
			for i, v := range []string{"1.2.3", "1.2.4"} {
				for name, body := range tc.files(v) {
					path := filepath.Join(folder, name)
					if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, []byte(body), 0644); err != nil {
						t.Fatal(err)
					}
				}
				raw, exit := fixtureShell(t, folder, []string{"sh", "-c", ds[0].List}, nil)
				if exit != 0 {
					t.Fatalf("converter: %d %s", exit, raw)
				}
				var err error
				sides[i], err = fix.ReadListing(raw)
				if err != nil {
					t.Fatal(err)
				}
				if len(sides[i]) != 1 || sides[i][0].Version != v {
					t.Fatalf("listing %+v", sides[i])
				}
			}
			target := check.Finding{Package: &check.PackageRef{Name: sides[0][0].Package, Ecosystem: tc.ecosystem, FixedIn: "1.2.4"}}
			if moves, err := fix.CompareUpdate(sides[0], sides[1], "routine", []check.Finding{target}); err != nil || len(moves) != 1 {
				t.Fatalf("moves %+v %v", moves, err)
			}
		})
	}
}

func TestFixCommandFailuresKeepToolAndPart(t *testing.T) {
	for _, tc := range []struct {
		name, command string
		want          workflow.StepState
	}{
		{"command not found", "missing-update-command-s2a", workflow.StatusError},
		{"nonzero command", "printf 'the update failed'; exit 1", workflow.StatusFail},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "fix", Steps: []schema.Step{{Name: "update", Uses: FixKind, With: map[string]any{"kind": "update"}}}}}}
			f, _ := newNodeFixFixture(t, "command", spec)
			f.bound = "aggressive" // With no runMajor declaration the ordinary command still runs.
			block, err := f.ways.CreateBlock(f.ctx, "fixture/update-command", "Update dependencies.\n", "", taskAppTownAdministrator)
			if err != nil {
				t.Fatal(err)
			}
			declarations := []fix.Declaration{{Kind: "update", AppliesTo: []string{"tool:node"}, Ecosystem: "npm", Run: tc.command}}
			block, err = f.ways.EditBlock(f.ctx, block.ID, wow.BlockPatch{Fixes: &declarations}, taskAppTownAdministrator)
			if err != nil {
				t.Fatal(err)
			}
			receipt := adoption.EmptyReceipt()
			receipt.Guidance = []adoption.ReceiptBlock{{Name: block.Name, BlockID: block.ID, Version: block.LatestVersion}}
			if err := f.db.InTx(t.Context(), func(tx *database.Tx) error {
				return adoption.RecordIn(f.ctx, tx, f.projectID, 3, adoption.AdoptionEdit, "custom update command", receipt, taskAppTownAdministrator, time.Now())
			}); err != nil {
				t.Fatal(err)
			}
			run := f.startChangeRun(t, "command", f.ctx)
			step := queueSteps(t, f, run)["update"]
			if err := f.work.ExecuteStep(f.ctx, step.ID); err != nil {
				t.Fatal(err)
			}
			state, ev := stepStatus(t, f, step.ID)
			if state != tc.want || !strings.Contains(fmt.Sprint(ev), "node") || !strings.Contains(fmt.Sprint(ev), "web") {
				t.Fatalf("%s: %+v", state, ev)
			}

			for n := 0; n < 2; n++ {
				queued := queueRun(t, f, "command", "updates")
				queueExecute(t, f, queueSteps(t, f, queued)["update"])
			}
			attempts, err := f.work.Attempts(f.ctx, f.projectID, "updates", 20)
			if err != nil {
				t.Fatal(err)
			}
			open := 0
			for _, attempt := range attempts {
				if !attempt.OpenChange.IsZero() {
					open++
				}
				if !attempt.Failed {
					t.Fatalf("failed command not retained: %+v", attempt)
				}
			}
			if open != 1 {
				t.Fatalf("failed command left %d open attempts", open)
			}
		})
	}
}

// packUpdateFix reads a pack's update fix through the pack's own guidance
// list, as adoption does, so a pack that stops adopting it fails too.
func packUpdateFix(t *testing.T, pack string) fix.Declaration {
	t.Helper()
	packs, err := wow.ReadStockPacks(stock.FS)
	if err != nil {
		t.Fatal(err)
	}
	blocks, skipped := wow.ReadStockGuidance(stock.FS, nil)
	if skipped != 0 {
		t.Fatal(skipped)
	}
	var ds []fix.Declaration
	for _, p := range packs {
		if p.Name != pack {
			continue
		}
		for _, b := range blocks {
			if slices.Contains(p.Guidance, b.Name) {
				declared, err := fix.Parse(b.FixesJSON)
				if err != nil {
					t.Fatalf("%s: %v", b.Name, err)
				}
				ds = append(ds, declared...)
			}
		}
	}
	if len(ds) != 1 {
		t.Fatalf("pack %s adopts %d update fixes: %+v", pack, len(ds), ds)
	}
	return ds[0]
}

// mavenListing runs the java pack's shipped listing unchanged, with a
// stand-in mvn that writes what the dependency plugin's list and
// list-repositories goals report: one block per module, each artifact
// with the repository id the resolver recorded beside its file.
func mavenListing(t *testing.T, list string, modules []map[string]string, repositories string) ([]fix.Listing, error) {
	t.Helper()
	folder, bin, repo := t.TempDir(), t.TempDir(), t.TempDir()
	// The dependency plugin runs once per module and, without appendOutput,
	// each module's report replaces the last.
	last := ""
	for i, artifacts := range modules {
		var lines strings.Builder
		lines.WriteString("The following files have been resolved:\n")
		if len(artifacts) == 0 {
			lines.WriteString("   none\n")
		}
		mavenModule(t, &lines, repo, artifacts)
		last = filepath.Join(bin, fmt.Sprintf("module-%d.txt", i))
		if err := os.WriteFile(last, []byte(lines.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(bin, "repositories.txt"), []byte("[INFO] Project remote repositories used by this build:\n"+repositories+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A shell function stands in for mvn ahead of the shipped command.
	mvn := "mvn() { append=; for a; do case $a in -DoutputFile=*) out=${a#-DoutputFile=};; -DappendOutput=true) append=1;; *:list-repositories) cat '" + filepath.Join(bin, "repositories.txt") + "'; return 0;; esac; done\n" +
		"if [ -n \"$append\" ]; then cat '" + bin + "'/module-*.txt >> \"$out\"; else cp '" + last + "' \"$out\"; fi; }\n"
	raw, exit := fixtureShell(t, folder, []string{"sh", "-c", mvn + list}, nil)
	if exit != 0 {
		return nil, fmt.Errorf("exit %d: %s", exit, raw)
	}
	return fix.ReadListing(raw)
}

func mavenModule(t *testing.T, lines *strings.Builder, repo string, artifacts map[string]string) {
	t.Helper()
	for coordinate, recorded := range artifacts {
		// The plugin's own form: group:artifact:type[:classifier]:version:scope,
		// the file, then " (optional)" for an optional dependency.
		coordinate, optional := strings.CutSuffix(coordinate, " (optional)")
		parts := strings.Split(coordinate, ":")
		version := parts[len(parts)-2]
		dir := filepath.Join(repo, parts[0], parts[1], version)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		file := parts[1] + "-" + version + ".jar"
		if len(parts) == 6 {
			file = parts[1] + "-" + version + "-" + parts[3] + ".jar"
		}
		if err := os.WriteFile(filepath.Join(dir, "_remote.repositories"), []byte("#NOTE: resolver record\n"+file+">"+recorded+"=\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		suffix := ""
		if optional {
			suffix = " (optional)"
		}
		fmt.Fprintf(lines, "   %s:%s%s -- module %s\n", coordinate, filepath.Join(dir, file), suffix, parts[1])
	}
}

// The comparator orders semver, Maven does not. The shipped converter keeps
// the three numbers, drops Maven's exact release aliases, keeps any other
// tail as build metadata (ordered equal, so a move within it is refused)
// and refuses pre-releases, which Maven orders below the release a FixedIn
// names. Each artifact's source is the URL its recorded repository resolves
// to in this build, mirrors included.
func TestShippedMavenConverterListsVersionsOnlyWhereMavenAgrees(t *testing.T) {
	d := packUpdateFix(t, "java")
	if d.Ecosystem != "Maven" || !slices.Equal(d.AppliesTo, []string{"tool:java"}) || d.Run == "" || d.RunMajor == "" || d.RunFor == "" {
		t.Fatalf("declaration %+v", d)
	}
	central := " * central (https://repo.maven.apache.org/maven2, default, releases) mirrored by apptown-local (https://mirror.example/maven2/, default, releases)"
	listed := func(t *testing.T, version, recorded, repositories string) ([]fix.Listing, error) {
		return mavenListing(t, d.List, []map[string]string{{"org.example:lib:jar:" + version + ":compile": recorded}}, repositories)
	}
	for maven, want := range map[string]string{"2.2": "2.2.0", "6.5.2.Final": "6.5.2", "4.0.1.RELEASE": "4.0.1", "1.9.25.1": "1.9.25+1", "33.4.0-jre": "33.4.0+jre", "1.0.0.SP1": "1.0.0+SP1"} {
		got, err := listed(t, maven, "apptown-local", central)
		if err != nil || len(got) != 1 || got[0].Version != want || got[0].Source != "https://mirror.example/maven2/" || got[0].Package != "org.example:lib" {
			t.Errorf("%s: %+v %v", maven, got, err)
		}
	}
	for maven, why := range map[string]string{"6.6.0.CR1": "pre-release", "2.0.0-M1": "pre-release", "1.0-beta-2": "pre-release", "1.0a1": "pre-release", "3.0.0-SNAPSHOT": "pre-release", "1.02": "without a semver form"} {
		if got, err := listed(t, maven, "apptown-local", central); err == nil || !strings.Contains(err.Error(), why) {
			t.Errorf("%s listed: %+v %v", maven, got, err)
		}
	}
	for name, c := range map[string]struct{ recorded, repositories string }{
		"no recorded repository": {"", central},
		"id without a location":  {"elsewhere", central},
		"one id, two locations":  {"central", central + "\n * central (https://other.example/maven2, default, releases)"},
	} {
		if got, err := listed(t, "1.0.0", c.recorded, c.repositories); err == nil {
			t.Errorf("%s listed: %+v", name, got)
		}
	}

	// A classifier and an optional dependency keep their own attributes.
	got, err := mavenListing(t, d.List, []map[string]string{{"org.example:lib:jar:tests:1.0.0:test (optional)": "apptown-local"}}, central)
	if err != nil || len(got) != 1 || got[0].Attributes["classifier"] != "tests" || got[0].Attributes["scope"] != "test" || got[0].Attributes["optional"] != true {
		t.Fatalf("classified listing %+v %v", got, err)
	}

	// A project with no dependencies lists nothing; a reactor reports each
	// module on its own and is refused, so one module's dependencies are
	// never taken for the whole build's.
	if got, err := mavenListing(t, d.List, []map[string]string{{}}, central); err != nil || len(got) != 0 {
		t.Fatalf("no dependencies: %+v %v", got, err)
	}
	reactor := []map[string]string{{"org.example:first:jar:1.0.0:compile": "apptown-local"}, {"org.example:last:jar:1.0.0:compile": "apptown-local"}}
	if got, err := mavenListing(t, d.List, reactor, central); err == nil || !strings.Contains(err.Error(), "single-module") {
		t.Fatalf("a reactor was listed: %+v %v", got, err)
	}

	move := func(before, after string, beforeRepositories, afterRepositories string) error {
		a, err := listed(t, before, "apptown-local", beforeRepositories)
		if err != nil {
			t.Fatal(err)
		}
		b, err := listed(t, after, "apptown-local", afterRepositories)
		if err != nil {
			t.Fatal(err)
		}
		_, err = fix.CompareUpdate(a, b, "routine", nil)
		return err
	}
	if err := move("6.5.2.Final", "6.6.1.Final", central, central); err != nil {
		t.Fatalf("a release move was refused: %v", err)
	}
	if err := move("1.9.25.1", "1.9.25", central, central); err == nil || !strings.Contains(err.Error(), "did not move forward") {
		t.Fatalf("a downgrade within the build metadata was accepted: %v", err)
	}
	moved := strings.ReplaceAll(central, "mirror.example", "other-mirror.example")
	if err := move("1.0.0", "1.0.1", central, moved); err == nil || !strings.Contains(err.Error(), "changed source") {
		t.Fatalf("a repository that moved under the same id was accepted: %v", err)
	}
}
