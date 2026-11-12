package workflowexec

import (
	"fmt"
	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
	"os"
	"path/filepath"
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
			if moves, err := fix.CompareUpdate(sides[0], sides[1], "patch", []check.Finding{target}); err != nil || len(moves) != 1 {
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
