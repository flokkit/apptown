package workflowexec

import (
	"context"
	"fmt"
	"strings"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/verification"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/fix"
)

const FixKind = "fix"
const RoutineKind = "routine"

type fixConfig struct {
	Kind string `json:"kind"`
	For  string `json:"for"`
}
type fixDeclarations interface {
	Fixes(context.Context, adoption.Source) ([]fix.Declaration, error)
}

func (a *commands) declarations(ctx context.Context, run *workflow.Run) ([]fix.Declaration, error) {
	if a.fixes == nil {
		return nil, fmt.Errorf("fix declarations are unavailable")
	}
	revision, err := adoption.ByIDIn(ctx, a.db, run.AdoptionID)
	if err != nil {
		return nil, err
	}
	return a.fixes.Fixes(ctx, adoption.Source{ProjectID: run.ProjectID, Revision: revision})
}

func runTargets(run *workflow.Run) ([]check.Finding, error) {
	return fix.Targets(run.InputsJSON)
}

// fixScript builds the command solely from the run's adopted declarations.
func (a *commands) fixScript(ctx context.Context, run *workflow.Run, step *workflow.Step, ws string) (scriptConfig, error) {
	var cfg fixConfig
	if err := workflow.DecodeStepConfig(step.Config, &cfg); err != nil {
		return scriptConfig{}, err
	}
	ds, err := a.declarations(ctx, run)
	if err != nil {
		return scriptConfig{}, err
	}
	targets, err := runTargets(run)
	if err != nil {
		return scriptConfig{}, err
	}
	src := verification.CheckSource(ws)
	if src.PartsErr != nil {
		return scriptConfig{}, src.PartsErr
	}
	if src.NoContract {
		return scriptConfig{}, fmt.Errorf("the app declares no parts: no .apptown/app.yaml")
	}
	level := ""
	if a.bounds != nil {
		level, err = a.bounds.UpdateBoundForRun(ctx, run.ID)
		if err != nil {
			return scriptConfig{}, err
		}
	}
	var commands []string
	for _, p := range src.Parts {
		matched := false
		for _, d := range ds {
			if d.Kind != cfg.Kind {
				continue
			}
			places, _, err := check.Applies(d.AppliesTo, check.Source{Parts: []check.Part{p}})
			if err != nil {
				return scriptConfig{}, err
			}
			if len(places) == 0 {
				continue
			}
			matched = true
			var names []string
			for _, t := range targets {
				if t.Package != nil && t.Package.Ecosystem == d.Ecosystem {
					names = append(names, t.Package.Name)
				}
			}
			if cfg.For == "findings" && len(names) == 0 {
				continue
			}
			if level == "aggressive" && cfg.For != "findings" && d.RunMajor != "" {
				d.Run = d.RunMajor
			}
			cmd, err := fix.UpdateCommand(d, names, cfg.For == "findings")
			if err != nil {
				return scriptConfig{}, fmt.Errorf("part %s (tools %s): %w", p.Name, strings.Join(p.Tools, ", "), err)
			}
			for _, place := range places {
				commands = append(commands, `(export PATH="$MISE_DATA_DIR/shims:$PATH"; cd `+shellWord(place.Folder)+" && mise install -y -q && "+cmd+`) || { fix_status=$?; printf '%s\n' `+shellWord("part "+p.Name+" (tools "+strings.Join(p.Tools, ", ")+"): update command failed")+`; exit "$fix_status"; }`)
			}
		}
		if !matched && len(p.Tools) > 0 {
			return scriptConfig{}, fmt.Errorf("part %s has tools %s but no adopted fix of kind %s", p.Name, strings.Join(p.Tools, ", "), cfg.Kind)
		}
	}
	if len(commands) == 0 {
		return scriptConfig{}, nil
	}
	// Updates install the declared tools in the same image as tests, including
	// the worker's image when checks run there.
	return scriptConfig{Run: strings.Join(commands, " && "), Image: a.checks.Image}, nil
}
func shellWord(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
