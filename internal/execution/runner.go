// Package execution runs bounded processes. It knows nothing about projects,
// workflow states, approvals or persistence; callers interpret its results.
package execution

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/gitexec"
)

const (
	Local             = "local"
	Docker            = "docker"
	maxCapturedOutput = 16 << 20
	maxStreamLine     = 1 << 20
)

type Request struct {
	Workspace    string
	WorkspaceRO  bool
	Mounts       []string
	Argv         []string
	Env          []string
	Timeout      time.Duration
	Image        string
	Name         string
	Network      string
	NoEntrypoint bool
	Labels       []string // key=value, supplied by the owner
	ExtraHosts   []string
	OnLine       func(string)
	// Report names one file the command writes under its workspace, which
	// the answer carries back (Outcome.Report): a caller never reads it off
	// the disk itself, so a check reads the same whichever machine ran it.
	Report string
	// History is a repository presented read-only at /history: the commits
	// a check reads beyond its tree (a landing's history scan).
	History *History
	// ExportImage names an image (by ID) saved to .apptown/image.tar in the
	// workspace before the command runs: an image scan reads the image
	// there, on the machine that has it.
	ExportImage string
	// Files are generated files presented read-only in the container
	// (files.go), written beside the tree by whoever runs it: never in it.
	Files []File
	// FilesDir is where they are written: a folder the caller owns, that
	// does not exist yet. "" is a folder the runner makes under its
	// Scratch, and removes after the run.
	FilesDir string
	// Services run beside a Docker run, on a network of its own that the
	// run joins, each reachable by its name (services.go): made healthy
	// before the command starts and removed with it. The run's Network is
	// then "" (the network reaches out) or "none" (it does not).
	Services []deploy.ServiceSpec
	// Limits, when set, bound this run in place of the runner's own.
	Limits Limits
}

// History is a repository and the commits in it a check needs, with their
// full histories.
type History struct {
	Repository string
	Commits    []string
}

// MaxReport bounds a report carried back: a larger one is not read, and is
// an error, never a truncated answer. The bound keeps a runaway tool from
// filling AppTown's memory; it is set above what a supported scanner
// writes for an ordinary image. Trivy's SARIF over a full Debian image
// (rust:1.84.1, every severity) is 27 MB, mostly rule descriptions, and it
// cannot be cut down by severity without hiding findings the gate judges:
// trivy filters on the vendor's label, AppTown on the CVSS score.
const MaxReport = 64 << 20

type Termination string

const (
	Exited       Termination = "exited"
	TimedOut     Termination = "timed_out"
	Canceled     Termination = "canceled"
	LaunchFailed Termination = "launch_failed"
	// OutOfMemory: Docker killed the container at its memory limit. The
	// command's exit is not its answer.
	OutOfMemory Termination = "out_of_memory"
)

type Outcome struct {
	Output      []byte
	RunErr      error
	TimedOut    bool // compatibility for existing consumers; Termination is authoritative
	Termination Termination
	ExitCode    int // -1 when no normal exit code is available
	CleanupErr  error
	// Report is the requested report's bytes, when it was read; ReportErr
	// says why it was not (missing, not a regular file, too large, or outside
	// the workspace). A caller whose tool declares a report treats a missing
	// one as an error, never as a clean answer.
	Report    []byte
	ReportErr error
}

// Answered reports a run whose exit code is its answer: a normal exit, with
// a real code, and nothing left behind. A timeout, a cancellation, a launch
// or transport failure, or a cleanup that failed is no answer at all.
func (o Outcome) Answered() bool {
	return o.Termination == Exited && o.ExitCode >= 0 && o.CleanupErr == nil
}

// Exited0 reports an answered run that exited 0.
func (o Outcome) Exited0() bool { return o.Answered() && o.ExitCode == 0 }

// A zero Runner uses the host's docker executable. A binary can be supplied by
// the composition layer or a test without changing package-global state.
type Runner struct {
	DockerBinary string
	// Scratch is where a request's generated files are written when it
	// names no folder of its own: a folder the Docker daemon can mount from.
	// "" is the system's temporary folder.
	Scratch string
	// Refused, when set, refuses every run with it: the project-process
	// runner of an AppTown that runs no project process on its own host.
	Refused error
	// ServiceLimits are each of a request's services' limits (services.go);
	// zero is DefaultServiceLimits.
	ServiceLimits Limits
	// Limits bound every Docker run this runner makes (LimitFlags); a zero
	// field is not bounded. AppTown sets them to its task limits.
	Limits Limits
}

func (r Runner) Run(ctx context.Context, backend string, req Request) (out Outcome) {
	if r.Refused != nil {
		return launchError(r.Refused)
	}
	if backend != Local && backend != Docker {
		return launchError(fmt.Errorf("unknown execution backend %q", backend))
	}
	if len(req.Argv) == 0 || req.Argv[0] == "" {
		return launchError(errors.New("execution requires a command"))
	}
	if req.Timeout <= 0 {
		return launchError(errors.New("execution requires a positive timeout"))
	}
	if backend == Docker && (req.Image == "" || req.Name == "") {
		return launchError(errors.New("Docker execution requires an image and an owned container name"))
	}
	if backend == Docker && (!ValidImage(req.Image) || req.ExportImage != "" && !ValidImage(req.ExportImage)) {
		return launchError(fmt.Errorf("%q is not an image reference", req.Image+" "+req.ExportImage))
	}
	ctx, cancel := context.WithTimeout(ctx, req.Timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return classified(nil, err, ctx, false)
	}
	if len(req.Files) > 0 {
		if backend != Docker {
			return launchError(errors.New("generated files are presented only to a Docker run"))
		}
		dir := req.FilesDir
		if dir == "" {
			if r.Scratch != "" {
				if err := os.MkdirAll(r.Scratch, 0o755); err != nil {
					return launchError(fmt.Errorf("a folder for the generated files: %w", err))
				}
			}
			tmp, err := os.MkdirTemp(r.Scratch, "forge-files-*")
			if err != nil {
				return launchError(fmt.Errorf("a folder for the generated files: %w", err))
			}
			defer os.RemoveAll(tmp)
			dir = filepath.Join(tmp, "files")
		}
		mounts, err := StageFiles(dir, req.Files)
		if err != nil {
			return launchError(fmt.Errorf("writing the generated files: %w", err))
		}
		req.Mounts = append(append([]string{}, req.Mounts...), mounts...)
	}
	if len(req.Services) > 0 {
		if backend != Docker {
			return launchError(errors.New("services run only beside a Docker run"))
		}
		network, remove, err := r.startServices(ctx, req)
		// Removed after the run, whatever became of it, with a lifetime of
		// its own: what is left behind makes the run no answer.
		defer func() {
			if cerr := remove(); cerr != nil {
				out.CleanupErr = errors.Join(out.CleanupErr, cerr)
			}
		}()
		if err != nil {
			return launchError(fmt.Errorf("starting the run's services: %w", err))
		}
		req.Network = network
	}
	command, args := req.Argv[0], req.Argv[1:]
	if backend == Docker {
		command, args = r.dockerBinary(), runArgs(req, r.limitsFor(req))
	}
	if req.ExportImage != "" {
		if backend != Docker || req.Workspace == "" {
			return launchError(errors.New("an image is exported only into a Docker run's workspace"))
		}
		if err := r.exportImage(ctx, req.Workspace, req.ExportImage); err != nil {
			return launchError(fmt.Errorf("saving the image %s for the check: %w", req.ExportImage, err))
		}
	}
	cmd := exec.CommandContext(ctx, command, args...)
	configureCancellation(cmd)
	if backend == Local {
		cmd.Dir = req.Workspace
		// Without the GIT_* variables AppTown inherited (gitexec.Environ):
		// git run here, by AppTown or by a command it runs, names its own
		// repository.
		cmd.Env = gitexec.Environ(req.Env...)
	}
	output, err, started := stream(cmd, req.OnLine)
	out = classified(output, err, ctx, started)
	if req.Report != "" && out.Termination == Exited {
		out.Report, out.ReportErr = ReadReport(req.Workspace, req.Report)
	}
	if backend == Docker && started {
		// The container outlives its client (no --rm): an exit is read for
		// how it happened, then the container is removed. Cleanup gets its own
		// bounded lifetime after the caller's context expires; canceling the
		// client does not establish that the owned workload stopped.
		cleanupCtx, cancelCleanup := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		if out.Termination == Exited {
			inspect := exec.CommandContext(cleanupCtx, r.dockerBinary(), "inspect", "-f", "{{.State.OOMKilled}}", req.Name)
			configureCancellation(inspect)
			answer, err := inspect.CombinedOutput()
			switch strings.TrimSpace(string(answer)) {
			case "true":
				out.Termination, out.ExitCode = OutOfMemory, -1
				out.RunErr = fmt.Errorf("the container ran out of memory at its limit of %d MiB", r.limitsFor(req).MemoryBytes>>20)
			case "false":
			default:
				out.CleanupErr = fmt.Errorf("reading how container %s ended: %v: %s", req.Name, err, strings.TrimSpace(string(answer)))
			}
		}
		cleanup := exec.CommandContext(cleanupCtx, r.dockerBinary(), "rm", "-f", req.Name)
		configureCancellation(cleanup)
		log, cleanupErr := cleanup.CombinedOutput()
		cancelCleanup()
		if cleanupErr != nil && !strings.Contains(strings.ToLower(string(log)), "no such container") {
			out.CleanupErr = errors.Join(out.CleanupErr, fmt.Errorf("remove owned container %s: %w: %s", req.Name, cleanupErr, strings.TrimSpace(string(log))))
		}
	}
	return out
}

// limitsFor is the request's own limits, or the runner's.
func (r Runner) limitsFor(req Request) Limits {
	if req.Limits != (Limits{}) {
		return req.Limits
	}
	return r.Limits
}

func (r Runner) dockerBinary() string {
	if r.DockerBinary != "" {
		return r.DockerBinary
	}
	return "docker"
}
func launchError(err error) Outcome {
	return Outcome{RunErr: err, Termination: LaunchFailed, ExitCode: -1}
}

func classified(output []byte, err error, ctx context.Context, started bool) Outcome {
	out := Outcome{Output: output, RunErr: err, ExitCode: -1}
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		out.Termination, out.TimedOut = TimedOut, true
		if err == nil {
			out.RunErr = ctx.Err()
		}
	case ctx.Err() == context.Canceled:
		out.Termination = Canceled
		if err == nil {
			out.RunErr = ctx.Err()
		}
	case !started:
		out.Termination = LaunchFailed
	default:
		out.Termination = Exited
		if err == nil {
			out.ExitCode = 0
		} else {
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				out.ExitCode = ee.ExitCode()
			}
		}
	}
	return out
}

// runArgs is a run's docker arguments, bounded by limits. The container is
// not removed on exit: the runner asks how it ended (OOMKilled) and removes
// it itself.
func runArgs(req Request, limits Limits) []string {
	args := removeFlag(dockerArgs(req), "--rm")
	return append(args[:1:1], append(LimitFlags(limits), args[1:]...)...)
}

func dockerArgs(req Request) []string {
	args := []string{"run", "--rm", "--init", "--name", req.Name}
	if req.Workspace != "" { // a task with no workspace mounts none
		mount := req.Workspace + ":/work"
		if req.WorkspaceRO {
			mount += ":ro"
		}
		args = append(args, "-v", mount, "-w", "/work")
	}
	for _, label := range req.Labels {
		args = append(args, "--label", label)
	}
	for _, mount := range req.Mounts {
		args = append(args, "-v", mount)
	}
	if req.History != nil {
		args = append(args, "-v", req.History.Repository+":/history:ro")
	}
	for _, host := range req.ExtraHosts {
		args = append(args, "--add-host", host)
	}
	if req.Network != "" {
		args = append(args, "--network", req.Network)
	}
	for _, env := range req.Env {
		args = append(args, "-e", env)
	}
	if req.NoEntrypoint {
		args = append(args, "--entrypoint", "")
	}
	return append(append(args, req.Image), req.Argv...)
}

// Output is bounded even for a process that never emits a newline. Oversized
// lines stream in fragments so draining stdout cannot consume unbounded memory.
func stream(cmd *exec.Cmd, onLine func(string)) ([]byte, error, bool) {
	pr, pw := io.Pipe()
	defer pr.Close()
	cmd.Stdout, cmd.Stderr = pw, pw
	var output []byte
	done := make(chan struct{})
	go func() {
		defer close(done)
		eachFragment(pr, func(fragment []byte) {
			n := min(len(fragment), maxCapturedOutput-len(output))
			output = append(output, fragment[:n]...)
			if onLine != nil {
				onLine(strings.TrimRight(string(fragment), "\n"))
			}
		})
	}()
	if err := cmd.Start(); err != nil {
		_ = pw.Close()
		<-done
		return output, err, false
	}
	err := cmd.Wait()
	_ = pw.Close()
	<-done
	return output, err, true
}

// eachFragment calls emit for each line read from r, newline included, until
// r ends. A line longer than maxStreamLine arrives as maxStreamLine-byte
// fragments, and a last line without a newline arrives as it is. The buffer
// starts small and grows only as far as a long line needs: most processes
// print short lines, and a fixed 1 MiB buffer per process was most of what
// the test suites allocated.
func eachFragment(r io.Reader, emit func([]byte)) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(nil, maxStreamLine)
	scanner.Split(func(data []byte, atEOF bool) (int, []byte, error) {
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			return i + 1, data[:i+1], nil
		}
		if len(data) >= maxStreamLine {
			return maxStreamLine, data[:maxStreamLine], nil
		}
		if atEOF && len(data) > 0 {
			return len(data), data, nil
		}
		return 0, nil, nil
	})
	for scanner.Scan() {
		emit(scanner.Bytes())
	}
}

// ReadReport reads the report at path under workspace: a regular file inside
// it, at most MaxReport bytes. A symbolic link, or a path that leaves the
// workspace, is refused rather than followed.
func ReadReport(workspace, path string) ([]byte, error) {
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return nil, fmt.Errorf("the report's workspace: %w", err)
	}
	defer root.Close()
	fi, err := root.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("the report %s: %w", path, err)
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("the report %s is not a regular file", path)
	}
	if fi.Size() > MaxReport {
		return nil, fmt.Errorf("the report %s is %d bytes, over the %d limit", path, fi.Size(), MaxReport)
	}
	f, err := root.Open(path)
	if err != nil {
		return nil, fmt.Errorf("the report %s: %w", path, err)
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, MaxReport+1))
	if err != nil {
		return nil, fmt.Errorf("the report %s: %w", path, err)
	}
	if len(b) > MaxReport {
		return nil, fmt.Errorf("the report %s grew over the %d limit", path, MaxReport)
	}
	return b, nil
}

// exportImage writes the image to .apptown/image.tar in the workspace, through
// a file created there exclusively and inside it: a link in the tree (the
// project's own) can never choose where the archive is written.
func (r Runner) exportImage(ctx context.Context, workspace, image string) error {
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := root.Mkdir(".apptown", 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	if fi, err := root.Lstat(".apptown"); err != nil || !fi.IsDir() {
		return fmt.Errorf("the tree's .apptown is not a folder")
	}
	f, err := root.OpenFile(filepath.Join(".apptown", "image.tar"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	var stderr strings.Builder
	save := exec.CommandContext(ctx, r.dockerBinary(), "save", image)
	save.Stdout, save.Stderr = f, &stderr
	err = save.Run()
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}
