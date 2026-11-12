package verification

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/apptown/agentenv"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/fix"
	"github.com/flokkit/apptown/internal/trust"
)

type UpdateListing struct {
	Part        string
	Declaration int
	Packages    []fix.Listing
}

// listUpdates runs adopted listing commands in isolated copies of committed trees,
// on the same check executor as tests. It never trusts the fix command's account.
func ListUpdates(ctx context.Context, db *database.DB, executor Executor, tree string, ds []fix.Declaration) ([]UpdateListing, error) {
	src := CheckSource(tree)
	if src.PartsErr != nil {
		return nil, src.PartsErr
	}
	if src.NoContract {
		return nil, fmt.Errorf("the app has no .apptown/app.yaml")
	}
	if executor.Run == nil || executor.AgentImage == "" {
		return nil, fmt.Errorf("the update listing executor is unavailable")
	}
	bound, err := integration.RegistryBindings(ctx, db)
	if err != nil {
		return nil, err
	}
	amb := agentenv.AmbientFor(bound, tree)
	var out []UpdateListing
	for _, p := range src.Parts {
		matched := false
		for i, d := range ds {
			kind, ok := fix.Lookup(d.Kind)
			if !ok || kind.Classifier != fix.UpdateClassifier {
				continue
			}
			places, _, err := check.Applies(d.AppliesTo, check.Source{Parts: []check.Part{p}})
			if err != nil {
				return nil, err
			}
			if len(places) == 0 {
				continue
			}
			matched = true
			if d.List == "" {
				return nil, fmt.Errorf("part %s: %s fix has no list command", p.Name, d.Ecosystem)
			}
			folder := p.Folder
			if folder == "" {
				folder = "."
			}
			cmd := "cd " + listingWord(filepath.ToSlash(folder)) + " && mise install -y -q && (" + d.List + ") > /work/.apptown-update-list.jsonl"
			result := executor.Run(ctx, execution.Request{Workspace: tree, Image: executor.AgentImage, Argv: []string{"sh", "-c", trust.Run + `; export PATH="$MISE_DATA_DIR/shims:$PATH"; ` + cmd}, Env: amb.Env, Files: amb.Files, Report: ".apptown-update-list.jsonl", Timeout: 10 * time.Minute, Name: execution.UniqueName("apptown-update-list"), Limits: execution.DefaultTaskLimits})
			if !result.Exited0() || result.RunErr != nil || result.ReportErr != nil {
				return nil, fmt.Errorf("part %s: %s listing failed (exit %d): %s: %v", p.Name, d.Ecosystem, result.ExitCode, result.Output, errors.Join(result.RunErr, result.ReportErr, result.CleanupErr))
			}
			packages, err := fix.ReadListing(result.Report)
			if err != nil {
				return nil, fmt.Errorf("part %s listing: %w", p.Name, err)
			}
			for _, pkg := range packages {
				if pkg.Ecosystem != d.Ecosystem {
					return nil, fmt.Errorf("part %s: list returned ecosystem %s instead of %s", p.Name, pkg.Ecosystem, d.Ecosystem)
				}
			}
			out = append(out, UpdateListing{Part: p.Name, Declaration: i, Packages: packages})
		}
		if !matched && len(p.Tools) > 0 {
			return nil, fmt.Errorf("part %s has tools %v but no update fix", p.Name, p.Tools)
		}
	}
	return out, nil
}

func listingWord(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
