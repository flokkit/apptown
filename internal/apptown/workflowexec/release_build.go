package workflowexec

import (
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/tasks"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/oci"
	"github.com/flokkit/apptown/internal/trust"
)

// Release builds on the worker (design/operations/workers.md, "Releases
// built on the worker"). On an AppTown with a worker, a landing's deploy step has the worker
// build every image of the release once, from the landed commit, in one
// release-build task: private Docker, the commit as its source, the
// generated build files, and no environment, inputs, services or registry
// credential. AppTown publishes what the task left through the
// deployment owner (PublishRelease), which commits the bundle in the step
// attempt's fence. Only then does the step deploy, to a Docker host, which
// pulls the bundle's digests. What would stop it is refused before
// anything is built.

// ReleaseBuilds is where an AppTown with a worker builds its releases.
type ReleaseBuilds struct {
	// Tasks is the task runner whose driver is the worker's
	// (ReleaseTaskDriver).
	Tasks *TaskRunner
	// PrivateDocker is apptownd -private-docker: a release build needs
	// private Docker on the worker.
	PrivateDocker bool
}

// ReleaseTaskDriver is the worker's driver as a release build needs it:
// its agent image (which has Docker's client and buildx), its measured
// profile, and the images a task left (execution.WorkerTasks).
type ReleaseTaskDriver interface {
	execution.TaskDriver
	execution.ImageFiles
	AgentImage(context.Context) (execution.AgentImage, error)
	Profile(context.Context) (execution.WorkerProfile, error)
	ImageResults(context.Context, execution.TaskRef) (map[string]execution.ImageCapture, error)
}

const (
	// releaseBuildWallClock is a release build's wall clock: every image of
	// the release, built cold.
	releaseBuildWallClock = 30 * time.Minute
	// releaseImageMax is the admitted size of one image's export.
	releaseImageMax = 4 << 30
	// releaseStaticDockerfiles is where each static site's generated
	// Dockerfile is presented to the build, as <image>.Dockerfile.
	releaseStaticDockerfiles = "/forge/release"
	releaseImagesOut         = execution.StateRoot + "/out"
	// releaseStaticExports is where a static site's built stage is
	// exported, as <image>/, before its output folder is packed.
	releaseStaticExports = execution.StateRoot + "/static"
)

// releaseRefused finishes a deploy step refused before any effect.
func releaseRefused(task *workflow.Execution, reason string) {
	task.Settle(workflow.EncodeOutcome(workflow.StatusError, map[string]any{"reason": "release refused: " + reason}, nil))
}

// releaseTarget is what a release build is checked against before it
// starts: the deployment it lands on, the commit, the repository holding it
// and the tree staged from it, and the contract read from that tree.
type releaseTarget struct {
	deployment *deployment.Deployment
	repository string
	commit     string
	tree       string
	contract   *appcontract.Contract
	raw        []byte
}

// buildRelease builds, publishes and commits a landing's release, and
// answers its bundle. When it answers false the step is finished (refused,
// failed or errored) or no longer this attempt's, and nothing was
// deployed.
func (a *Shipping) buildRelease(task *workflow.Execution, in releaseTarget) (*deployment.ReleaseBundle, bool) {
	// The task lives by its durable record, not a context: what a stopped
	// AppTown leaves running is collected on restart.
	ctx := context.WithoutCancel(task.Context())
	runner := a.builds.Tasks
	driver, ok := runner.Driver.(ReleaseTaskDriver)
	if !ok {
		releaseRefused(task, "AppTown's task backend ("+runner.backend()+") cannot build releases: it reports no images")
		return nil, false
	}
	run, step := task.Run(), task.Step()
	logf := func(format string, args ...any) { a.progress.Append(step.ID, fmt.Sprintf(format, args...)) }
	logf("Validating the release build and deployment target")
	// Refused before anything is built, each with its next action.
	t, err := a.deployment.Targets.ByID(ctx, in.deployment.TargetID)
	if err != nil {
		finishError(task, "reading the deployment's target: "+err.Error())
		return nil, false
	}
	if t.Kind != deployment.KindDockerHost {
		releaseRefused(task, fmt.Sprintf("a landing deploys to a Docker host, and %s is on %s (%s); deploy %s on a Docker host and promote to %s", in.deployment.Name, t.Name, t.Kind, in.deployment.Name, t.Kind))
		return nil, false
	}
	logf("Deployment target: %s (Docker host)", t.Name)
	if _, err := a.deployment.LandingRegistry(ctx, run.ProjectID, "apptown-system", map[string]any{"run": run.ID, "step": step.ID}); errors.Is(err, deployment.ErrReleaseRefused) {
		releaseRefused(task, strings.TrimPrefix(err.Error(), deployment.ErrReleaseRefused.Error()+": "))
		return nil, false
	} else if err != nil {
		finishError(task, err.Error())
		return nil, false
	}
	if !a.builds.PrivateDocker {
		releaseRefused(task, "a release is built in private Docker on the worker, and this AppTown was started with -private-docker=false")
		return nil, false
	}
	profile, err := driver.Profile(ctx)
	if err != nil {
		finishError(task, "reading the worker's profile: "+err.Error())
		return nil, false
	}
	platform, err := oci.Platform("linux", profile.Architecture)
	if err != nil {
		releaseRefused(task, "the worker's platform is unknown: "+err.Error())
		return nil, false
	}
	targetPlatform, err := a.deployment.TargetPlatform(ctx, t)
	if err != nil {
		releaseRefused(task, "the target's platform is unknown: "+err.Error())
		return nil, false
	}
	if targetPlatform != platform {
		releaseRefused(task, fmt.Sprintf("the worker builds for %s and %s runs %s", platform, t.Name, targetPlatform))
		return nil, false
	}
	plan := deployment.PlanRelease(in.contract)
	// The build's static server, from the tool versions current as it begins.
	script, files, images, err := releaseBuildRequest(in.tree, plan, platform, a.deployment.ToolVersions().Images.StaticServer)
	if err != nil {
		releaseRefused(task, err.Error())
		return nil, false
	}
	build := deployment.BundleBuild{Worker: runner.backend(), Role: "agent", Profile: profile}
	publication := deployment.ReleasePublication{By: task, ChangeID: run.ChangeID, Tree: in.commit, Contract: in.raw, Plan: plan, Platform: platform, Build: build}
	publication.Log = logf
	if len(images) == 0 {
		// Every image is declared by reference: nothing to build, only
		// copies for AppTown to make.
		return a.publishBuilt(task, publication)
	}
	agentImage, err := driver.AgentImage(ctx)
	if err != nil {
		finishError(task, "reading the worker's agent image: "+err.Error())
		return nil, false
	}
	limits := privateLimits(runner.Profile.Limits)
	limits.WallClock = releaseBuildWallClock
	now := runner.now()
	admitted := execution.Admission{Profile: runner.Profile.Name, Limits: limits, Network: runner.Profile.Network, Expires: now.Add(releaseBuildWallClock + taskGrace)}
	private := &execution.PrivateDocker{Daemon: true, Profile: &profile}
	if err := private.Valid(limits); err != nil {
		releaseRefused(task, "the release build's private Docker: "+err.Error())
		return nil, false
	}
	operation := fmt.Sprintf("step-%d-attempt-%d-release-build", step.ID, step.OwnedAttempt())
	spec := execution.TaskSpec{Key: operation, Image: agentImage.Name, Argv: runOnce([]string{"sh", "-c", script}), Admission: admitted,
		Source: &execution.TaskSource{Repository: in.repository, Commit: in.commit}, Files: files, Images: images, Private: private}
	attempt, err := runner.Records.Reserve(ctx, tasks.Admission{Operation: operation, Invocation: tasks.ReleaseBuildInvocation,
		Owner:   tasks.Owner{ProjectID: run.ProjectID, RunID: run.ID, StepID: step.ID, Attempt: step.OwnedAttempt()},
		Backend: runner.backend(), Container: operation, Digest: spec.Digest(), Source: in.commit, Admitted: admitted, Deadline: now.Add(releaseBuildWallClock)})
	if err != nil {
		finishError(task, "recording the release build: "+err.Error())
		return nil, false
	}
	logf("Building %d release images on the worker (%s)", len(images), platform)
	attempt, code, timedOut, _, ok := runner.execute(task, a.db, attempt, spec, taskKind{})
	if !ok {
		return nil, false // the step is finished, or no longer this attempt's
	}
	ref := execution.TaskRef{Key: operation, ProviderID: attempt.ProviderID}
	// Whatever follows, the task is settled and released once the step has
	// what it needs of it: its images are not kept past publication.
	defer runner.settleTask(ctx, attempt, ref, &code, "")
	if timedOut || code != 0 {
		var page execution.OutputPage
		_ = runner.whileUnreachable(ctx, operation, func(ctx context.Context) (err error) {
			page, err = driver.Output(ctx, ref, execution.OutputRead{Max: maxTaskOutput})
			return err
		})
		reason := fmt.Sprintf("the release build failed (exit %d)", code)
		if timedOut {
			runner.stopQuietly(ctx, attempt, ref) // its stop is recorded; the release removes it either way
			reason = fmt.Sprintf("the release build ran past its %s", releaseBuildWallClock)
		}
		task.Settle(workflow.EncodeOutcome(workflow.StatusFail, map[string]any{"reason": reason, "logTail": outputTail(page.Data)}, nil))
		return nil, false
	}
	var captured map[string]execution.ImageCapture
	logf("Reading the worker's completed release images")
	if err := runner.whileUnreachable(ctx, operation, func(ctx context.Context) (err error) {
		captured, err = driver.ImageResults(ctx, ref)
		return err
	}); err != nil {
		finishError(task, "reading the release build's images: "+err.Error())
		return nil, false
	}
	built := map[string]oci.Layout{}
	for _, img := range images {
		c := captured[img.Name]
		if c.State != execution.ResultPresent {
			finishError(task, fmt.Sprintf("the release build left its image %s %s %s", img.Name, c.State, c.Reason))
			return nil, false
		}
		built[img.Name] = execution.ImageLayout{Files: driver, Ref: ref, Image: img.Name}
	}
	publication.Built = built
	return a.publishBuilt(task, publication)
}

// publishBuilt publishes what the worker built; a failed publication or
// commit errors the step, before any target effect.
func (a *Shipping) publishBuilt(task *workflow.Execution, in deployment.ReleasePublication) (*deployment.ReleaseBundle, bool) {
	bundle, err := a.publishRelease(task, func(ctx context.Context) (*deployment.ReleaseBundle, error) {
		return a.deployment.PublishRelease(ctx, in)
	})
	if err != nil {
		finishError(task, err.Error())
		return nil, false
	}
	return bundle, true
}

// publishRelease publishes under the step's ownership, wherever the release
// was built: a transfer under way when the step stops being this attempt's
// is ended, and nothing is committed (PublishRelease checks again before
// each image and at the commit).
func (a *Shipping) publishRelease(task *workflow.Execution, publish func(context.Context) (*deployment.ReleaseBundle, error)) (*deployment.ReleaseBundle, error) {
	ctx, cancel := context.WithCancel(context.WithoutCancel(task.Context()))
	watched := make(chan struct{})
	go func() {
		defer close(watched)
		tick := time.NewTicker(a.publicationWatch())
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				if errors.Is(task.Current(), workflow.ErrStepAttemptLost) {
					cancel()
					return
				}
			}
		}
	}()
	bundle, err := publish(ctx)
	cancel()
	<-watched
	return bundle, err
}

// publicationWatch is how often a publication asks whether its step is
// still this attempt's: as often as a worker's task is watched.
func (a *Shipping) publicationWatch() time.Duration {
	if a.builds != nil && a.builds.Tasks != nil {
		return a.builds.Tasks.poll()
	}
	return (&TaskRunner{}).poll()
}

// releaseBuildRequest is the build's script, its generated files and the
// images it leaves: one `docker buildx build` per built image, for one
// platform, without provenance or SBOM, exported as an OCI layout under
// the workspace's state. Each folder built is the tree's own (no link out
// of it); image-only declarations are not built, but copied by AppTown.
func releaseBuildRequest(tree string, plan deployment.ReleasePlan, platform, staticServer string) (string, []execution.File, []execution.ImageResult, error) {
	// The extra certificates this machine's containers trust reach each
	// build as its secret (internal/trust), from the task's own variable:
	// the worker's, never this machine's. None when it has none. Their
	// SHA-256 goes with them as a build argument by name, as
	// trust.BuildArgs gives it, for a Dockerfile that writes them into the
	// image.
	lines := []string{"set -e", `[ -z "${` + trust.EnvVar + `:-}" ] || export ` + trust.SumArg + `="$(printf '%s' "$` + trust.EnvVar + `" | sha256sum | cut -d ' ' -f 1)"`}
	buildSecret := ` ${` + trust.EnvVar + `:+--secret id=` + trust.SecretID + `,env=` + trust.EnvVar + ` --build-arg ` + trust.SumArg + `} `
	var files []execution.File
	var images []execution.ImageResult
	quote := func(args []string) string {
		quoted := make([]string, len(args))
		for i, arg := range args {
			quoted[i] = shellQuote(arg)
		}
		return strings.Join(quoted, " ")
	}
	for _, img := range plan.Images {
		folder := img.Build
		if img.Static != "" {
			folder = img.Static
		}
		if img.StaticBuild.Context != "" {
			folder = img.StaticBuild.Context
		}
		if folder == "" {
			continue
		}
		full, err := docker.SelectedPath(tree, folder)
		if err != nil {
			return "", nil, nil, fmt.Errorf("the image %s: %w", img.Name, err)
		}
		rel, err := filepath.Rel(tree, full)
		if err != nil {
			return "", nil, nil, err
		}
		context := path.Join("/work", filepath.ToSlash(rel))
		if img.StaticBuild.Context != "" {
			// The site's stage is exported inside the task, on the task's
			// own builder (BUILDX_BUILDER=apptown, its private daemon), for
			// the builder's own platform: the site is files. Its output is
			// checked for links before anything reads it, then packed.
			export := path.Join(releaseStaticExports, img.Name)
			args := append([]string{"docker", "buildx", "build", "--provenance=false", "--sbom=false"}, docker.StaticExportArgs(img.StaticBuild.Target, context, export)...)
			lines = append(lines, quote(args[:len(args)-1])+buildSecret+shellQuote(context), docker.StaticExportCheckScript(export, img.StaticBuild.Output))
			context = path.Join(export, img.StaticBuild.Output)
		}
		out := path.Join(releaseImagesOut, img.Name)
		args := []string{"docker", "buildx", "build", "--platform", platform, "--provenance=false", "--sbom=false",
			"--output", "type=oci,dest=" + out + ",tar=false"}
		if img.IsStatic() {
			// Each static image has its own Dockerfile: its command names
			// the port it serves on.
			dockerfile := path.Join(releaseStaticDockerfiles, img.Name+".Dockerfile")
			args = append(args, "-f", dockerfile)
			body, err := img.Dockerfile(staticServer)
			if err != nil {
				return "", nil, nil, err
			}
			files = append(files, execution.File{Path: dockerfile, Content: []byte(body), Mode: 0o644})
		}
		lines = append(lines, quote(args)+buildSecret+shellQuote(context))
		images = append(images, execution.ImageResult{Name: img.Name, Path: out, Max: releaseImageMax})
	}
	if err := execution.ValidImages(images); err != nil {
		return "", nil, nil, err
	}
	return strings.Join(lines, "\n") + "\n", files, images, nil
}

// outputTail is the end of a build's output: where its failure is.
func outputTail(out []byte) string {
	const keep = 4000
	if len(out) > keep {
		out = out[len(out)-keep:]
	}
	return strings.ToValidUTF8(string(out), "")
}

func finishError(task *workflow.Execution, reason string) {
	task.Settle(workflow.EncodeOutcome(workflow.StatusError, map[string]any{"reason": reason}, nil))
}
