package verification

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/progress"
	"github.com/flokkit/apptown/internal/strs"
)

const ScanKind = "scan"
const maxReportBody = 4 * 1024 * 1024

// ScanSteps orders scanner preparation, evidence recording and workflow settlement.
type ScanSteps struct {
	db           *database.DB
	scanners     *Scanners
	conformance  *conformance.Service
	integrations *integration.Service
	progress     *progress.Feeds
	exec         Executor
	dataDir      string
	panics       interface{ Panic(string, any, []byte) }
	clock        func() time.Time
}
type ScanDependencies struct {
	DB           *database.DB
	Scanners     *Scanners
	Conformance  *conformance.Service
	Integrations *integration.Service
	Progress     *progress.Feeds
	Executor     Executor
	DataDir      string
	Panics       interface{ Panic(string, any, []byte) }
	Now          func() time.Time // AppTown's clock; nil is the real one
}

// now is AppTown's clock.
func (a *ScanSteps) now() time.Time { return clock.Or(a.clock)() }

func NewScanSteps(d ScanDependencies) *ScanSteps {
	return &ScanSteps{db: d.DB, scanners: d.Scanners, conformance: d.Conformance, integrations: d.Integrations, progress: d.Progress, exec: d.Executor, dataDir: d.DataDir, panics: d.Panics, clock: d.Now}
}

func (a *ScanSteps) logPanic(where string) {
	if r := recover(); r != nil {
		stack := debug.Stack()
		log.Printf("PANIC in %s: %v\n%s", where, r, stack)
		if a.panics != nil {
			a.panics.Panic(where, r, stack)
		}
	}
}
func (a *ScanSteps) RunScan(task *workflow.Execution) {
	defer a.logPanic("RunScan")
	step := task.Step()
	stepID := step.ID
	run := task.Run()
	var cfg scanConfig
	cfgErr := workflow.DecodeStepConfig(step.Config, &cfg)
	// No failOn means every actionable finding fails, whatever the bound
	// scanner looks at: a policy ceiling reads the step, not the binding,
	// so the step's words must be the whole threshold. The stock image
	// scans write failOn: high.
	finish := func(status workflow.StepState, evidence map[string]any) {
		task.Settle(workflow.EncodeOutcome(status, evidence, nil))
	}
	if _, err := task.Claim(); err != nil {
		return
	}
	if cfgErr != nil {
		finish(workflow.StatusError, map[string]any{"reason": cfgErr.Error()})
		return
	}
	// A landing scans its pinned candidate, and every outcome names it, even
	// a scan point with nothing attached: the merge requires every passed
	// source gate to name the candidate.
	rev, pinned, err := checkedRevision(task.Context(), a.db, run.ID)
	if err != nil {
		finish(workflow.StatusError, map[string]any{"reason": err.Error()})
		return
	}
	if pinned {
		report := finish
		finish = func(status workflow.StepState, evidence map[string]any) {
			evidence["candidate"] = rev
			report(status, evidence)
		}
	}
	if cfg.None {
		finish(workflow.StatusPass, map[string]any{"note": fmt.Sprintf("no scanner attached for %s — attach one under Integrations › Scanners", cfg.On)})
		return
	}
	stopHB := a.progress.Heartbeat(stepID, "scanning")
	defer stopHB()

	binding, m, version, rawCfg, err := a.scanners.binding(task.Context(), cfg.Scanner)
	if err != nil {
		finish(workflow.StatusError, map[string]any{"reason": err.Error()})
		return
	}
	evidence := map[string]any{
		"binding": binding, "provider": "tool", "bindingVersion": version,
		"configHash": deploy.ConfigHash(rawCfg), "image": m.Image,
	}
	workflow.RecordNoticeOrLog(context.WithoutCancel(task.Context()), a.db, step.RunID, "integration.resolved", map[string]any{
		"capability": ScannerCapability, "binding": binding, "provider": "tool",
		"version": version, "configHash": deploy.ConfigHash(rawCfg), "step": step.Name,
	}, a.now())

	source, err := workflow.ReadSourceIn(task.Context(), a.db, workflow.RunRef{ID: run.ID, ProjectID: run.ProjectID})
	if err != nil {
		finish(workflow.StatusError, map[string]any{"reason": err.Error()})
		return
	}
	ws := source.Workspace(a.dataDir)
	// Scan one COMMITTED tree, resolved once and staged outside every
	// writer's folder, never the working directory: the working tree
	// carries AppTown runtime state (.apptown/sessions with agent transcripts,
	// pi-home, ambient config) that is gitignored and never ships, and that
	// legitimately contains material a secrets scanner must flag if ever
	// committed. Posture is about what ships. A landing scans its pinned
	// candidate; any other run what its gates read (SelectCommit): its
	// folder's HEAD, or its branch when the folder is behind work published
	// there. A folder with no repository has no commit to scan, and staging
	// that fails is an error, never a fall-back to the live folder
	// (design/operations/workers.md, "Checks on the worker").
	sel, err := SelectCommit(task.Context(), a.db, a.dataDir, run, source)
	if err != nil {
		evidence["reason"] = "reading the commit to scan: " + strs.Truncate(err.Error(), 200)
		finish(workflow.StatusError, evidence)
		return
	}
	repo := sel.Repo
	rev = sel.Commit
	if !pinned {
		evidence["commit"] = sel.Commit
	}
	scanSrc, cleanup, err := StageCommittedTree(task.Context(), a.dataDir, repo, rev)
	if err != nil {
		evidence["reason"] = err.Error()
		finish(workflow.StatusError, evidence)
		return
	}
	defer cleanup()
	_ = os.MkdirAll(filepath.Join(scanSrc, ".apptown"), 0o755)
	if strings.Contains(m.Image, "osv") {
		if _, err := os.Stat(filepath.Join(scanSrc, "go.mod")); err == nil {
			if v := goToolchainVersion(scanSrc); v != "" && writeOSVGoOverride(scanSrc, v) {
				evidence["goToolchain"] = v
			}
		}
	}
	// Image target: the artifact that will actually run, not the tree. Build
	// it from the contract (or pull the contract's image), save it as a
	// tarball the scanner can read without a daemon, substitute ${IMAGE_TAR}.
	argv := append([]string{}, m.Command...)
	var imageBase scanBase
	exportImage := ""
	if m.Looks() == "image" {
		// The image is built from the staged commit, not the live folder
		// (whose build context would include uncommitted edits).
		ref, note, err := a.imageForScan(task.Context(), run, scanSrc, rev)
		if err != nil {
			evidence["reason"] = err.Error()
			finish(workflow.StatusError, evidence)
			return
		}
		if ref == "" {
			evidence["note"] = note
			// Skipping the public part is not "nothing ships": name the rest.
			if c, err := docker.ReadWorkspaceContract(scanSrc); err == nil {
				if others := otherImages(c); len(others) > 0 {
					evidence["notScanned"] = others
				}
			}
			finish(workflow.StatusPass, evidence)
			return
		}
		// Provenance for an image scan (design/subsystems/conformance.md §7.3): the base
		// is the image of the commit this change forked from — its OS CVEs
		// are carried debt, not this change's. Scanned first, with the same
		// binding, in the same workspace (the report path is shared).
		baseSHA, err := WorkflowProvenanceBase(task.Context(), a.dataDir, source, rev)
		if err != nil {
			evidence["reason"] = "reading the commit this change is compared with: " + strs.Truncate(err.Error(), 300)
			finish(workflow.StatusError, evidence)
			return
		}
		imageBase = a.scanners.baseImageFingerprints(task.Context(), run.ProjectID, ws, baseSHA, m, binding, scanSrc, argv, fmt.Sprintf("apptown-scan-%d", stepID))
		exportImage = ref // saved into the tree where the scan runs
		evidence["image"] = ref
		// What this receipt covers, and what it does not: the public part's
		// image, never the release's digest, and not the other parts.
		if c, err := docker.ReadWorkspaceContract(scanSrc); err == nil {
			scanned, notScanned := imageScanScope(c, note)
			evidence["scanned"] = scanned
			if len(notScanned) > 0 {
				evidence["notScanned"] = notScanned
			}
		}
		for i := range argv {
			argv[i] = strings.ReplaceAll(argv[i], "${IMAGE_TAR}", "/work/.apptown/image.tar")
		}
	}

	timeout := check.ScanTimeout
	if cfg.TimeoutMinutes > 0 {
		timeout = time.Duration(cfg.TimeoutMinutes) * time.Minute
	}
	// A landing's history scan reads every commit the landing adds, from
	// the project's repository, mounted read-only.
	var history *execution.History
	if pinned && len(m.HistoryCommand) > 0 && m.Looks() == "source" {
		commits, err := landingRange(task.Context(), a.db, run.ID)
		if err != nil {
			evidence["reason"] = err.Error()
			finish(workflow.StatusError, evidence)
			return
		}
		repository := source.Repository(a.dataDir)
		// The scanner reads the range with its own git, and gitleaks reports
		// a range git cannot read as "no leaks found". So the range is read
		// here first, in the repository the scanner is given: one that does
		// not resolve is an error, never a clean scan.
		count, err := gitexec.Run(task.Context(), repository, "rev-list", "--count", commits, "--")
		if err != nil {
			evidence["reason"] = fmt.Sprintf("reading the commits this landing adds (%s) in the project's repository: %s", commits, strs.Truncate(err.Error(), 200))
			finish(workflow.StatusError, evidence)
			return
		}
		argv = nil
		for _, arg := range m.HistoryCommand {
			argv = append(argv, strings.ReplaceAll(arg, "${HISTORY_RANGE}", commits))
		}
		history = &execution.History{Repository: repository, Commits: strings.Split(commits, "..")}
		evidence["history"] = commits
		evidence["historyCommits"] = strings.TrimSpace(count)
	}
	// After the base scan, which wrote to the same paths in this tree.
	if err := clearForScan(scanSrc, m); err != nil {
		evidence["reason"] = err.Error()
		finish(workflow.StatusError, evidence)
		return
	}
	start := a.now()
	req := m.Request(scanSrc, argv)
	req.History, req.Report, req.ExportImage, req.Timeout = history, m.Report, exportImage, timeout
	req.Name = execution.UniqueName(fmt.Sprintf("apptown-scan-%d", stepID))
	outcome := a.exec.Run(task.Context(), req)
	evidence["durationMs"] = a.now().Sub(start).Milliseconds()

	answer := readScan(outcome, m, binding)
	exitCode := answer.exit
	evidence["exitCode"] = exitCode

	// Report → artifact (full body) + structured findings when the manifest
	// declares a format.
	reportBody := answer.report
	if len(reportBody) > 0 {
		body := string(reportBody)
		if len(body) > maxReportBody {
			body = body[:maxReportBody] + "\n... (truncated)"
		}
		akind := m.ArtifactKind
		if akind == "" {
			akind = "scan-report"
		}
		if id, err := workflow.AddArtifactIn(context.WithoutCancel(task.Context()), a.db, step.RunID, akind, binding, "", body, a.now()); err == nil {
			evidence["reportArtifactId"] = id
			workflow.RecordNoticeOrLog(context.WithoutCancel(task.Context()), a.db, step.RunID, "artifact.added", map[string]any{"kind": akind, "name": binding, "id": id}, a.now())
		}
	}

	switch {
	case answer.nothingToScan:
		evidence["note"] = "no package sources found — nothing to scan"
		finish(workflow.StatusPass, evidence)
		return
	case answer.failure != "":
		tailBytes := outcome.Output
		if len(tailBytes) > 2048 {
			tailBytes = tailBytes[len(tailBytes)-2048:]
		}
		evidence["reason"] = answer.failure
		evidence["logTail"] = string(tailBytes)
		finish(workflow.StatusError, evidence)
		return
	}

	// Structured verdict (findings.md §4): actionable = findings − active
	// dispositions; fail when any actionable finding meets the threshold.
	// Falls back to the exit-code contract when no format is declared.
	if m.Format != "" && m.Report != "" {
		findings := answer.findings
		dispositions, err := a.conformance.Dispositions(conformance.WithRun(task.Context(), task.RunScope()), run.ProjectID, true)
		if err != nil {
			evidence["reason"] = fmt.Sprintf("reading exceptions: %v", err)
			finish(workflow.StatusError, evidence)
			return
		}
		// Provenance (design/subsystems/conformance.md §7.3): only findings this change
		// INTRODUCED gate it; findings already on its base commit are carried
		// debt — shown, counted, never a reason to block a change that did not
		// create them (and never a bypass: they stay on the project's record).
		var base scanBase
		compared := true
		switch {
		case history != nil:
			// A history scan reads only the commits main does not have, so
			// each finding is in what this landing publishes. Comparing it
			// with main's tree would class a new token that replaces an old
			// one in the same file as carried debt.
			base.Note = "found in the commits this landing adds to main"
			compared = false
		case m.Looks() == "source":
			baseSHA, err := WorkflowProvenanceBase(task.Context(), a.dataDir, source, rev)
			if err != nil {
				evidence["reason"] = "reading the commit this change is compared with: " + strs.Truncate(err.Error(), 300)
				finish(workflow.StatusError, evidence)
				return
			}
			base = a.scanners.baseScanFingerprints(task.Context(), ws, baseSHA, m, binding, fmt.Sprintf("apptown-scan-%d", stepID))
		case m.Looks() == "image":
			base = imageBase
		default:
			compared = false
		}
		actionable, gating, carried := scanGate(findings, dispositions, base, compared)
		failing := 0
		var failingFindings []conformance.Finding
		bySev := map[string]int{}
		for _, f := range gating {
			bySev[f.Severity]++
			if check.FailsAt(cfg.FailOn, f.Severity) {
				failing++
				failingFindings = append(failingFindings, f)
			}
		}
		fsum := map[string]any{
			"total": len(findings), "dispositioned": len(findings) - len(actionable),
			"actionable": len(actionable), "introduced": len(gating), "carried": len(carried),
			"failing": failing, "bySeverity": bySev, "failOn": cfg.FailOn,
		}
		if base.SHA != "" {
			fsum["baseSha"] = base.SHA
		}
		if base.Note != "" {
			fsum["baseNote"] = base.Note
		}
		if base.Detail != "" {
			fsum["baseDetail"] = base.Detail
		}
		evidence["findings"] = fsum
		if len(carried) > 0 {
			evidence["carried"] = fmt.Sprintf("%d finding(s) already on main before this change (owed debt, not gating): %s", len(carried), carriedSummary(carried))
			fps := make([]string, 0, len(carried))
			for _, f := range carried {
				fps = append(fps, f.Fingerprint)
			}
			evidence["carriedFingerprints"] = fps
		}
		if failing == 0 {
			finish(workflow.StatusPass, evidence)
		} else {
			evidence["reason"] = scanFailReason(failingFindings, base)
			finish(workflow.StatusFail, evidence)
		}
		return
	}

	if exitCode == 0 {
		finish(workflow.StatusPass, evidence)
		return
	}
	if m.Report != "" && len(reportBody) == 0 {
		// Exit 1 with no report is not "findings": the scanner fell over
		// before it could write one (trivy could not download its database
		// on a full disk), and a landing must not be blocked as if the
		// image had findings. That is a scanner error, with the log.
		tailBytes := outcome.Output
		if len(tailBytes) > 2048 {
			tailBytes = tailBytes[len(tailBytes)-2048:]
		}
		evidence["reason"] = fmt.Sprintf("%s exited 1 without writing its report (%s) — a scanner failure, not findings; see the log", binding, m.Report)
		evidence["logTail"] = string(tailBytes)
		finish(workflow.StatusError, evidence)
		return
	}
	evidence["reason"] = fmt.Sprintf("%s reported findings (exit 1) — see the scan-report artifact", binding)
	finish(workflow.StatusFail, evidence)
}
func (a *ScanSteps) imageForScan(ctx context.Context, run *workflow.Run, tree, commit string) (ref, note string, err error) {
	// The image of the commit being scanned: the same image, by ID, that
	// the commit's exec-image checks run in, shared by its commit.
	ref, note, status, err := a.scanners.imageForCommit(ctx, run.ProjectID, tree, commit, true)
	switch {
	case err != nil:
		return "", "", fmt.Errorf("the image to scan: %w", err)
	case status == "none":
		return "", strings.Replace(note, "nothing to run in an image", "nothing to scan as an image", 1), nil
	}
	return ref, note, nil
}

// scanConfig is a scan step's options, and None, which AppTown records
// when a scan point expanded to no scanner.
type scanConfig struct {
	Scanner        string `json:"scanner"`
	FailOn         string `json:"failOn"` // severity threshold over ACTIONABLE findings; "" = any
	TimeoutMinutes int    `json:"timeoutMinutes"`
	On             string `json:"on"`   // a scan point that expanded to nothing
	None           bool   `json:"none"` // … says so and passes
}
