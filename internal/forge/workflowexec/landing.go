package workflowexec

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"flokkit.io/forge/internal/execution"

	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/forge/agentenv"
	"flokkit.io/forge/internal/forge/deployment"
	"flokkit.io/forge/internal/forge/workflow"
)

type landingDeploymentFailure struct {
	status   workflow.StepState
	evidence map[string]any
	rework   bool
}

// Decision maps execution outcomes to workflow evidence without writing a step.
func landingDeploymentDecision(result deployment.ExecutionResult, err error, boot deployment.LandingBoot) *landingDeploymentFailure {
	failure := func(status workflow.StepState, reason string) *landingDeploymentFailure {
		return &landingDeploymentFailure{status: status, evidence: map[string]any{"reason": reason}}
	}
	if err != nil {
		var runErr *deployment.RunError
		if !errors.As(err, &runErr) {
			return failure(workflow.StatusError, err.Error())
		}
		switch {
		case runErr.Stage == deployment.Admission:
			return failure(workflow.StatusError, "admitting the deployment: "+runErr.Cause.Error())
		case runErr.Stage == deployment.Settlement:
			d := failure(workflow.StatusError, "recording the deployed deployment: "+runErr.Cause.Error())
			d.evidence["url"] = boot.URL
			return d
		case errors.Is(runErr.Cause, context.Canceled), errors.Is(runErr.Cause, context.DeadlineExceeded):
			return failure(workflow.StatusError, runErr.Cause.Error())
		case boot.Err != nil:
			be := docker.AsBootError(boot.Err)
			d := failure(workflow.StatusFail, be.Error())
			switch be.Phase {
			case docker.PhaseParts:
				d.evidence["reason"] = "bringing up the app: " + be.Error()
			case docker.PhaseProbe:
				d.evidence["url"], d.evidence["logTail"] = boot.URL, be.LogTail
			case docker.PhasePage:
				d.rework = true
				d.evidence["url"] = boot.URL
				if boot.Boot.Page != nil {
					d.evidence["frontPage"] = boot.Boot.Page
				}
				if be.LogTail != "" {
					d.evidence["logTail"] = be.LogTail
				}
			}
			return d
		default:
			d := failure(workflow.StatusError, runErr.Cause.Error())
			d.evidence["url"] = boot.URL
			return d
		}
	}
	if !result.Created {
		d := failure(workflow.StatusError, "deployment already admitted; observation owns its unfinished outcome")
		d.evidence["attempt"] = result.Attempt.ID
		return d
	}
	if result.Outcome != deployment.Applied {
		d := failure(workflow.StatusError, "deployment outcome "+string(result.Outcome))
		d.evidence["generation"] = result.Attempt.Generation
		return d
	}
	return nil
}

// cleanTreeBuilds compiles the already-selected committed tree in the agent
// image, where checks run (a worker's executor when one is configured), with
// the executor's own caches and amb, the tree's ambient build environment:
// the Go it builds with is the one its mise.toml declares, as in its tests
// step (the agent image has no Go of its own). Go-only for now (go.mod
// present); other toolchains vacuously pass. Returns (reason, ok).
// A build its caller stopped answers nothing (stopped): it is never read as
// a tree that does not build, which would send the change back for rework.
func cleanTreeBuilds(ctx context.Context, staged string, checks CompileCheck, amb agentenv.Ambient) (reason string, ok, stopped bool) {
	if _, err := os.Stat(filepath.Join(staged, "go.mod")); err != nil {
		return "", true, false // not a Go project — nothing to check yet
	}
	outcome := checks.Run(ctx, execution.Request{
		Workspace: staged,
		Argv: []string{"sh", "-c",
			"mise trust -q 2>/dev/null; mise install -y -q 2>&1 || { echo '" + cleanBuildNoInstall + "'; exit 1; }; " +
				`export PATH="$MISE_DATA_DIR/shims:$PATH"; ` +
				"command -v go >/dev/null || { echo '" + cleanBuildNoGo + "'; exit 1; }; go build ./... 2>&1"},
		Env:     amb.Env,
		Files:   amb.Files,
		Image:   checks.Image,
		Timeout: 5 * time.Minute,
		Name:    execution.UniqueName("forge-cleanbuild"),
	})
	if !outcome.Exited0() && ctx.Err() != nil {
		return "", false, true
	}
	if !outcome.Exited0() {
		out := string(outcome.Output)
		for _, mark := range []string{cleanBuildNoGo, cleanBuildNoInstall} {
			if strings.Contains(out, mark) {
				return mark, false, false
			}
		}
		tail := out
		if len(tail) > 800 {
			tail = tail[len(tail)-800:]
		}
		return "go build ./... failed on the committed tree:\n" + tail, false, false
	}
	return "", true, false
}

// What the clean build says when the tree has no Go to build with, and when
// the toolchain it declares did not install, in place of a compiler error.
const (
	cleanBuildNoGo      = "this is a Go project (go.mod) and it declares no Go toolchain: pin one in mise.toml, for example go = \"1.27.0\""
	cleanBuildNoInstall = "the toolchain this project declares in mise.toml did not install, so the committed tree was not built"
)
