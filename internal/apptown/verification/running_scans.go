package verification

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/ids"
)

// Running scans only pull and observe. No build or release operation is used.
func (a *ScanSteps) runRunningScan(task *workflow.Execution, cfg scanConfig) {
	ctx := task.Context()
	dispositions, err := a.conformance.Dispositions(conformance.WithRun(ctx, task.RunScope()), task.Run().ProjectID, true)
	var observations []conformance.ScanObservation
	status, reason := workflow.StatusError, ""
	if err != nil {
		reason = "reading exceptions: " + err.Error()
		run, step := task.Run(), task.Step()
		observations = []conformance.ScanObservation{{ProjectID: run.ProjectID, ChangeID: run.ChangeID, RunID: run.ID, StepID: step.ID, Subject: conformance.ScanSubject{On: "running"}, StepConfig: step.Config, ObservedAt: a.now(), Verdict: status.String(), Reason: reason}}
	} else {
		observations, status, reason = a.observeRunningImages(ctx, task.Run(), task.Step(), cfg, dispositions)
	}
	if err := task.InAttempt(ScanKind, func(tx *database.Tx, attempt int64) error {
		for _, o := range observations {
			o.Attempt = attempt
			if err := conformance.RecordScanObservationIn(context.WithoutCancel(ctx), tx, o); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		status = workflow.StatusError
		reason = "recording the scan observations: " + err.Error()
	}
	task.Settle(workflow.EncodeOutcome(status, map[string]any{"reason": reason, "observations": observations}, nil))
}

func (a *ScanSteps) observeRunningImages(ctx context.Context, run *workflow.Run, step *workflow.Step, cfg scanConfig, dispositions []conformance.Disposition) ([]conformance.ScanObservation, workflow.StepState, string) {
	status := workflow.StatusPass
	observations := []conformance.ScanObservation{}
	add := func(subject conformance.ScanSubject, scanner string, version int, raw string, verdict workflow.StepState, findings []conformance.Finding, reason string) {
		observations = append(observations, conformance.ScanObservation{ID: ids.New(a.now()), ProjectID: run.ProjectID, ChangeID: run.ChangeID, RunID: run.ID, StepID: step.ID, Subject: subject, Scanner: scanner, BindingVersion: version, Configuration: raw, ConfigHash: deploy.ConfigHash(raw), StepConfig: step.Config, ObservedAt: a.now(), Verdict: verdict.String(), Findings: findings, Reason: reason})
		if verdict == workflow.StatusError || (verdict == workflow.StatusFail && status != workflow.StatusError) {
			status = verdict
		}
	}
	deps, err := a.deployments.RunningImages(ctx, run.ProjectID)
	if err != nil {
		add(conformance.ScanSubject{On: "running"}, "", 0, "{}", workflow.StatusError, nil, err.Error())
		return observations, status, err.Error()
	}
	if len(deps) == 0 {
		add(conformance.ScanSubject{On: "running"}, "", 0, "{}", workflow.StatusNotApplicable, nil, "no deployments")
		status = workflow.StatusNotApplicable
		return observations, status, "no deployments"
	}
	attached, err := a.scanners.attachedScanners(ctx)
	if err != nil {
		add(conformance.ScanSubject{On: "running"}, "", 0, "{}", workflow.StatusError, nil, err.Error())
		return observations, status, err.Error()
	}
	scanners := []string{}
	for _, sc := range attached {
		if !sc.M.Manual && sc.M.Looks() == "image" {
			scanners = append(scanners, sc.Name)
		}
	}
	// One pull and one run of each scanner per distinct digest in this scan.
	type answer struct {
		scanner, raw, reason string
		version              int
		verdict              workflow.StepState
		findings             []conformance.Finding
	}
	cache := map[string][]answer{}
	for _, dep := range deps {
		subject := conformance.ScanSubject{On: "running", DeploymentID: dep.DeploymentID, Deployment: dep.Deployment, BundleID: dep.BundleID}
		if dep.Reason != "" {
			add(subject, "", 0, "{}", workflow.StatusError, nil, dep.Reason)
			continue
		}
		if len(scanners) == 0 {
			add(subject, "", 0, "{}", workflow.StatusError, nil, "no image scanner attached — attach one under Integrations › Scanners")
			continue
		}
		parts := make([]string, 0, len(dep.Images))
		for part := range dep.Images {
			parts = append(parts, part)
		}
		sort.Strings(parts)
		for _, part := range parts {
			digest := dep.Images[part]
			subject.Part, subject.Digest = part, digest
			if _, ok := cache[digest]; !ok {
				answers := []answer{}
				dir, err := os.MkdirTemp(a.dataDir, "running-scan-")
				if err != nil {
					add(subject, "", 0, "{}", workflow.StatusError, nil, err.Error())
					continue
				}
				// Preparation uses this project's existing per-request login, never an
				// ambient credential. The executor handles the same request on a worker.
				ri, rerr := a.integrations.Registry(ctx, run.ProjectID)
				var login *execution.RegistryLogin
				if rerr == nil && ri != nil {
					login = &execution.RegistryLogin{Host: ri.Host, Username: ri.Username, Token: ri.Token}
				}
				image := execution.ImageAnswer{}
				switch {
				case rerr != nil:
					image.Err = rerr.Error()
				case login == nil:
					image.Err = "no registry credential for this project"
				default:
					image = a.exec.Image(ctx, execution.ImageRequest{Pull: digest, Registry: login, Scratch: dir})
				}
				for _, name := range scanners {
					binding, m, version, raw, err := a.scanners.binding(ctx, name)
					ans := answer{scanner: binding, raw: raw, version: version, verdict: workflow.StatusError}
					switch {
					case err != nil:
						ans.reason = err.Error()
					case image.Err != "":
						ans.reason = image.Err
					case image.ID == "":
						ans.reason = "the running image could not be pulled"
					default:
						argv := append([]string{}, m.Command...)
						for i := range argv {
							argv[i] = strings.ReplaceAll(argv[i], "${IMAGE_TAR}", "/work/.apptown/image.tar")
						}
						if err := clearForScan(dir, m); err != nil {
							ans.reason = err.Error()
							break
						}
						req := m.Request(dir, argv)
						req.ExportImage, req.Report, req.Name = digest, m.Report, execution.UniqueName(execution.StepContainer(step.ID, "running-scan"))
						req.Timeout = check.ScanTimeout
						if cfg.TimeoutMinutes > 0 {
							req.Timeout = time.Duration(cfg.TimeoutMinutes) * time.Minute
						}
						scanned := readScan(a.exec.Run(ctx, req), m, name)
						ans.findings = scanned.findings
						switch {
						case scanned.failure != "":
							ans.reason = scanned.failure
						case scanned.empty:
							ans.reason = "the scanner could not judge the running image"
						default:
							ans.verdict = workflow.StatusPass
							if m.Format == "" || m.Report == "" {
								if scanned.exit != 0 {
									ans.verdict = workflow.StatusFail
									ans.reason = name + " reported findings"
								}
							} else {
								_, gating, _ := scanGate(ans.findings, dispositions, scanBase{}, false)
								for _, finding := range gating {
									if check.FailsAt(cfg.FailOn, finding.Severity) {
										ans.verdict = workflow.StatusFail
										ans.reason = "the running image has findings"
										break
									}
								}
							}
						}
					}
					answers = append(answers, ans)
				}
				if err := os.RemoveAll(dir); err != nil {
					for i := range answers {
						answers[i].verdict = workflow.StatusError
						answers[i].reason = fmt.Sprintf("removing the scan workspace: %v", err)
					}
				}
				cache[digest] = answers
			}
			for _, ans := range cache[digest] {
				add(subject, ans.scanner, ans.version, ans.raw, ans.verdict, ans.findings, ans.reason)
			}
		}
	}
	return observations, status, "running images observed"
}
