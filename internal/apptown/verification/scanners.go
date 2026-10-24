package verification

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/dependency"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/strs"
)

// Scanners resolves attached tools and evaluates their evidence. Image
// preparation is shared; gate and fleet policy stay with their callers.
type Scanners struct {
	db           *database.DB
	docker       *docker.Scope
	exec         Executor
	conformance  *conformance.Service
	evaluator    *Evaluator
	integrations *integration.Service
	deployments  *deployment.Deployments
	dependencies *dependency.Catalog
	clock        func() time.Time
}

// now reads AppTown's clock.
func (s *Scanners) now() time.Time { return clock.Or(s.clock)() }

type ScannerDependencies struct {
	DB           *database.DB
	Docker       *docker.Scope
	Executor     Executor
	Conformance  *conformance.Service
	Evaluator    *Evaluator
	Integrations *integration.Service
	Deployments  *deployment.Deployments
	Dependencies *dependency.Catalog
	Now          func() time.Time // AppTown's clock; nil is the real one
}

// NewScanners binds scanner execution and image preparation to their owners.
func NewScanners(d ScannerDependencies) *Scanners {
	return &Scanners{db: d.DB, docker: d.Docker, exec: d.Executor, conformance: d.Conformance, evaluator: d.Evaluator, integrations: d.Integrations, deployments: d.Deployments, dependencies: d.Dependencies, clock: d.Now}
}

const ScannerCapability = "scanner"

// attachedScanners: every scanner binding, parsed, in binding order. An
// unreadable binding list is an error, never "no scanners": empty means the
// read succeeded and found none.
func (s *Scanners) attachedScanners(ctx context.Context) ([]struct {
	Name string
	M    check.Scanner
}, error) {
	var out []struct {
		Name string
		M    check.Scanner
	}
	bindings, err := integration.ForCapability(ctx, s.db, ScannerCapability)
	if err != nil {
		return nil, err
	}
	for _, b := range bindings {
		var m check.Scanner
		if json.Unmarshal([]byte(b.ConfigJSON), &m) == nil {
			out = append(out, struct {
				Name string
				M    check.Scanner
			}{b.Name, m})
		}
	}
	return out, nil
}

// binding resolves a scanner by name, or the default (first) binding
// when name is empty. Resolution order per integrations.md §3.
func (s *Scanners) binding(ctx context.Context, name string) (bindingName string, cfg check.Scanner, version int, rawCfg string, err error) {
	b, readErr := integration.Selected(ctx, s.db, ScannerCapability, name)
	if readErr != nil {
		if name != "" {
			return "", cfg, 0, "", fmt.Errorf("no scanner binding named %q", name)
		}
		return "", cfg, 0, "", fmt.Errorf("no binding for capability %q", ScannerCapability)
	}
	err = json.Unmarshal([]byte(b.ConfigJSON), &cfg)
	return b.Name, cfg, b.Version, b.ConfigJSON, err
}

// goToolchainVersion: the Go that builds this tree, which is the Go its
// mise.toml declares (the agent image has none, and a project's steps get
// Go only from that declaration). Stdlib advisories must be judged against
// THIS, not go.mod's `go` directive: the directive is a floor, and osv can
// score a dependency-free module with findings that the real toolchain has
// already fixed. With no pin there is nothing better to say, and osv judges
// against the directive.
func goToolchainVersion(dir string) string {
	if b, err := os.ReadFile(filepath.Join(dir, "mise.toml")); err == nil {
		if m := regexp.MustCompile(`(?m)^\s*go\s*=\s*"([0-9][0-9.]*)"`).FindSubmatch(b); m != nil {
			return string(m[1])
		}
	}
	return ""
}

// RunToolChecks evaluates every `tool` check in checks. Returns results (one per
// check, runShChecks-shaped), the fail findings, and an error count.
func (s *Scanners) RunToolChecks(ctx context.Context, checks []conformance.DeclaredCheck, tc ToolContext, namePrefix string) (results Results, findings []conformance.Finding, toolErrors int) {
	for _, sc := range checks {
		if check.Evaluator(sc.Check) != "tool" {
			continue
		}
		start := s.now()
		ev, verdict, exceptedBy := s.evalToolCheck(ctx, sc, tc, namePrefix)
		res := resultOf(ev)
		res.Tool, res.Verdict, res.ExceptedBy = sc.Check.Tool, verdict, exceptedBy
		res.ExitCode = nil // a tool check's receipt has never carried one
		res.DurationMs = new(s.now().Sub(start).Milliseconds())
		// The detail rides in "output" so review detail lookups and
		// the CLI recover it uniformly across verdicts.
		if ev.Detail != "" {
			res.Output = strs.Truncate(ev.Detail, 300)
		}
		switch verdict {
		case workflow.VerdictSkipped:
			res.Note = ev.Detail
		case workflow.VerdictFail:
			findings = append(findings, conformance.Finding{Finding: check.Finding{
				Fingerprint: conformance.Fingerprint(sc, ""),
				Kind:        "conformance",
				Severity:    sc.Check.Severity,
				Title:       fmt.Sprintf("guidance %s v%d · %s (tool: %s)", sc.Block, sc.Version, sc.Check.Name, sc.Check.Tool),
				Detail:      strs.FirstNonBlank(ev.Detail, sc.Check.Expects),
				Source:      "guidance",
			}})
		case workflow.VerdictError:
			toolErrors++
		}
		results = append(results, res)
	}
	return results, findings, toolErrors
}

// toolJudge is the library judge a tool check runs through: the App's
// executor, the binding it resolved (by the name the check asks for), and a
// way to run a command inside an app image.
func (s *Scanners) toolJudge(ctx context.Context, tool, bindingName string, m check.Scanner) *check.Judge {
	m.Name = bindingName
	j := s.evaluator.judge(ctx)
	j.Scanners = check.ScannersFunc(func(name string) (check.Scanner, bool) { return m, name == tool })
	j.RunImage = func(image string, command, env []string) (int, string) {
		return runInImage(ctx, s.exec.Run, image, command, env)
	}
	return j
}

// evalToolCheck evaluates one tool check and says what AppTown calls its
// outcome: pass, fail, error, skipped (nothing to judge with) or excepted (a
// person has already excused everything the tool found). The evaluation it
// returns is the receipt's shape; the word is AppTown's.
//
// What AppTown adds around the library's judgement is here: the binding a
// name resolves to, the app image an exec-image check runs inside, the
// environment it runs with, and the dispositions.
//
// "excepted" is a fail whose every finding is covered by an active disposition:
// the detail keeps the findings underneath, and exceptedBy names the exception
// (design/subsystems/conformance-surface.md §9) — never folded into pass.
func (s *Scanners) evalToolCheck(ctx context.Context, sc conformance.DeclaredCheck, tc ToolContext, namePrefix string) (ev check.Evaluation, verdict workflow.CheckVerdict, exceptedBy string) {
	// An evaluation AppTown itself made, for what it could settle before
	// the judge was asked.
	apptownSays := func(outcome check.Outcome, detail string) check.Evaluation {
		return check.Evaluation{Declared: sc.Declared(), Mode: check.Gate, Outcome: outcome, Detail: detail}
	}
	bindingName, m, _, _, err := s.binding(ctx, sc.Check.Tool)
	if err != nil {
		// A named tool with no binding is an errored check (recorded, never
		// silently passed) — the way of working references a tool the
		// instance has not attached.
		return apptownSays(check.Error, fmt.Sprintf("no scanner binding %q attached — attach it under Integrations › Scanners", sc.Check.Tool)), workflow.VerdictError, ""
	}
	src := check.Source{Tree: tc.BuildDir}
	if m.Looks() == "exec-image" {
		// The app image is content-addressed by the commit and SHARED: apptown
		// check builds it once, the land gate and repeat invocations reuse it
		// (design/subsystems/conformance.md §13.6). The read-only sweep reuses a cached
		// image but never builds — a cache miss there is "deferred", skipped.
		ref, note, status, err := s.imageForCommit(ctx, tc.ProjectID, tc.BuildDir, tc.SHA, tc.AllowBuild)
		switch status {
		case "error":
			return apptownSays(check.Error, "preparing the app image: "+strs.Truncate(err.Error(), 200)), workflow.VerdictError, ""
		case "none", "deferred":
			return apptownSays(check.CannotAssess, note), workflow.VerdictSkipped, ""
		}
		src.Image, src.Env = ref, s.checkEnvironment(ctx, tc.BuildDir)
	} else {
		toolImageReady(ctx, s.exec.Image, m.Image)
	}
	ev = s.toolJudge(ctx, sc.Check.Tool, bindingName, m).EvaluateTool(sc.Declared(), src, check.Gate, namePrefix)

	// The tool wrote a report the library read: its findings are the answer,
	// and a finding covered by an active disposition is not actionable — the
	// same "actionable = findings − dispositions" the scan gates use
	// (design/subsystems/findings.md §3). Conformance must honor it too: a VERIFIED
	// false positive (or an accepted exception) should clear a tool check,
	// not only a land gate. Otherwise a spurious finding pins an app
	// does-not-conform forever, with no honest way to record the truth.
	// Dispositions stay a human act, carrying a justification (findings.go).
	if ev.Evidence.Report != "" {
		dispositions := tc.Exceptions
		if dispositions == nil {
			if tc.Context == nil {
				return apptownSays(check.Error, conformance.ErrCallerRequired.Error()), workflow.VerdictError, ""
			}
			var err error
			if dispositions, err = s.conformance.Dispositions(tc.Context, tc.ProjectID, true); err != nil {
				return apptownSays(check.Error, fmt.Sprintf("reading exceptions: %v", err)), workflow.VerdictError, ""
			}
		}
		fs := conformance.WrapFindings(ev.Findings)
		actionable := conformance.AnnotateFindings(fs, dispositions)
		if len(fs) == 0 {
			return ev, workflow.VerdictPass, ""
		}
		if len(actionable) == 0 {
			// Every finding is covered: excepted, with the findings kept
			// underneath and the covering exception(s) named.
			var refs []conformance.DispositionRef
			seen := map[ids.ID]bool{}
			for _, f := range fs {
				if f.Disposition != nil && !seen[f.Disposition.ID] {
					seen[f.Disposition.ID] = true
					refs = append(refs, f.Disposition.Reference())
				}
			}
			named, err := strs.JSON(refs[0])
			if err == nil {
				return ev, workflow.VerdictExcepted, named
			}
			// An exception that cannot be named is not applied.
			ev.Detail = check.FindingsSummary(bindingName, conformance.RawFindings(fs)) + "; its exception could not be recorded: " + err.Error()
			return ev, workflow.VerdictFail, ""
		}
		ev.Detail = check.FindingsSummary(bindingName, conformance.RawFindings(actionable))
		return ev, workflow.VerdictFail, ""
	}

	switch ev.Outcome {
	case check.Conforms:
		return ev, workflow.VerdictPass, ""
	case check.DoesNotConform:
		return ev, workflow.VerdictFail, ""
	}
	// Error, and a tool the judge could not reach at all: recorded, never
	// silently passed.
	return ev, workflow.VerdictError, ""
}

// baseScanFingerprints stages the base commit hermetically and runs the
// binding on it. Errors never fail the step: they make the comparison
// unavailable, which the caller reports.
func (s *Scanners) baseScanFingerprints(ctx context.Context, repo, baseSHA string, m check.Scanner, binding, namePrefix string) scanBase {
	b := scanBase{SHA: baseSHA}
	switch {
	case strings.TrimSpace(baseSHA) == "":
		b.Note = "no base commit recorded for this change — every finding is treated as introduced"
		return b
	case !gitexec.IsRepo(repo):
		b.Note = "workspace is not a git tree — every finding is treated as introduced"
		return b
	case m.Looks() != "source" || m.Format == "" || m.Report == "":
		b.Note = "this scanner has no structured report to compare"
		return b
	}
	src, cleanup, err := scm.StageTreeAt(ctx, repo, baseSHA)
	if err != nil {
		b.Note = "could not stage the base commit: " + strs.Truncate(err.Error(), 160)
		return b
	}
	defer cleanup()
	_ = os.MkdirAll(filepath.Join(src, ".apptown"), 0o755)
	if strings.Contains(m.Image, "osv") {
		if _, err := os.Stat(filepath.Join(src, "go.mod")); err == nil {
			if v := goToolchainVersion(src); v != "" {
				writeOSVGoOverride(src, v)
			}
		}
	}
	if err := clearForScan(src, m); err != nil {
		b.Note = "scanning the base commit failed (" + strs.Truncate(err.Error(), 160) + ") — every finding is treated as introduced"
		return b
	}
	req := m.Request(src, m.Command)
	req.Name, req.Report = execution.UniqueName(fmt.Sprintf("%s-base-%s", namePrefix, binding)), m.Report
	answer := readScan(s.evaluator.judge(ctx).Run(req), m, binding)
	if answer.failure != "" {
		b.Note = "scanning the base commit failed (" + strs.Truncate(answer.failure, 160) + ") — every finding is treated as introduced"
		return b
	}
	b.Findings = conformance.RawFindings(answer.findings)
	b.OK = true
	return b
}

// clearForScan removes what an earlier scan left in dir where m's run
// writes (its report, and for an image scanner the saved image, which a run
// saves only where no file is), so what is read afterwards is this run's.
// A removal that fails is the error (check.RemoveStale).
func clearForScan(dir string, m check.Scanner) error {
	if m.Report != "" {
		if err := check.RemoveStale(filepath.Join(dir, m.Report)); err != nil {
			return err
		}
	}
	if m.Looks() == "image" {
		return check.RemoveStale(filepath.Join(dir, ".apptown", "image.tar"))
	}
	return nil
}

// RunSourceScans runs every attached, non-manual SOURCE scanner (osv,
// gitleaks, semgrep — the same set the land `scan-source` point fans out to)
// against an already-staged committed tree. It lets `apptown check` REFLECT the
// land gate's security scans, not just the guidance checks, so an agent sees a
// dependency CVE or a SAST finding in its own loop instead of discovering it
// after handoff when the land sticks (design/subsystems/conformance.md §13.6, the
// office-poll retest). Reuses the check executor so it is testable without Docker.
//
// The land gate fails a source scan on ANY actionable finding (its failOn is
// ""), so every finding returned here is one that would block the land — the
// caller counts them all as failing, unlike guidance findings which gate at
// their severity.
//
// repo + baseSHA name the change's base commit: findings already there are
// CARRIED debt (reported in the result, not returned as gating findings), so
// apptown check previews exactly what the land gate will hold against the
// change (scan_provenance.go). Pass "" to compare against nothing.
func (s *Scanners) RunSourceScans(ctx context.Context, scanSrc string, namePrefix string, repo, baseSHA string) (results Results, findings []conformance.Finding, scanErrors int) {
	_ = os.MkdirAll(filepath.Join(scanSrc, ".apptown"), 0o755)
	attached, err := s.attachedScanners(ctx)
	if err != nil {
		// The scans were not assessed: an errored result, not a pass.
		return Results{{Check: "source scanners", Evaluator: "scanner", Verdict: workflow.VerdictError, AppTownFault: true,
			Output: "reading the attached scanners: " + err.Error()}}, nil, 1
	}
	for _, sc := range attached {
		m := sc.M
		if m.Manual || m.Looks() != "source" {
			continue
		}
		start := s.now()
		res := workflow.CheckReceipt{Check: sc.Name, Scanner: sc.Name, Evaluator: "scanner"}

		// osv: judge stdlib advisories against the toolchain that builds this
		// tree, exactly as RunScan does.
		if strings.Contains(m.Image, "osv") {
			if _, err := os.Stat(filepath.Join(scanSrc, "go.mod")); err == nil {
				if v := goToolchainVersion(scanSrc); v != "" {
					writeOSVGoOverride(scanSrc, v)
				}
			}
		}
		if err := clearForScan(scanSrc, m); err != nil {
			res.Verdict, res.AppTownFault, res.Output = workflow.VerdictError, true, err.Error()
			scanErrors++
			results = append(results, res)
			continue
		}
		req := m.Request(scanSrc, m.Command)
		req.Name, req.Report = execution.UniqueName(fmt.Sprintf("%s-scan-%s", namePrefix, sc.Name)), m.Report
		outcome := s.evaluator.judge(ctx).Run(req)
		res.DurationMs = new(s.now().Sub(start).Milliseconds())
		answer := readScan(outcome, m, sc.Name)
		exitCode := answer.exit
		// osv exits 128 for "no package sources found" — a clean pass with a note.
		if answer.nothingToScan {
			res.Verdict = workflow.VerdictPass
			results = append(results, res)
			continue
		}
		if answer.failure != "" {
			res.Verdict = workflow.VerdictError
			res.Output = strs.Truncate(strings.TrimSpace(answer.failure+"\n"+strings.TrimSpace(string(outcome.Output))), 300)
			scanErrors++
			results = append(results, res)
			continue
		}
		fs := answer.findings
		if len(fs) > 0 && baseSHA != "" {
			base := s.baseScanFingerprints(ctx, repo, baseSHA, m, sc.Name, namePrefix)
			introduced, carried := splitByProvenance(fs, base)
			if len(carried) > 0 {
				res.Carried = len(carried)
				res.CarriedNote = fmt.Sprintf("%d already on main (owed debt, not gating): %s", len(carried), carriedSummary(carried))
			}
			if base.Note != "" {
				res.BaseNote = base.Note
			}
			fs = introduced
		}
		switch {
		case len(fs) > 0:
			res.Verdict = workflow.VerdictFail
			res.Output = scanFindingsSummary(sc.Name, fs)
			findings = append(findings, fs...)
		case res.Carried > 0:
			res.Verdict = workflow.VerdictPass
			res.Output = res.CarriedNote
		case exitCode == 1:
			// exit 1 without a parseable report (e.g. a format-less scanner) is
			// still a finding by the exit-code contract.
			res.Verdict = workflow.VerdictFail
			res.Output = strs.FirstNonBlank(strs.Truncate(strings.TrimSpace(string(outcome.Output)), 200), sc.Name+" reported findings")
			findings = append(findings, conformance.Finding{Finding: check.Finding{
				Fingerprint: "scan|" + sc.Name, Kind: "scan", Severity: "high",
				Title: sc.Name + " reported findings", Source: sc.Name,
			}})
		default:
			res.Verdict = workflow.VerdictPass
		}
		results = append(results, res)
	}
	return results, findings, scanErrors
}
