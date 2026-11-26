package verification

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/ids"
)

// Bundle and running scans only pull and observe. No build or release operation is used.
func (a *ScanSteps) runImageScan(task *workflow.Execution, cfg scanConfig) {
	ctx := task.Context()
	run, step := task.Run(), task.Step()
	subjects, status, reason := a.imageSubjects(ctx, run, cfg.On)
	var observations []conformance.ScanObservation
	if status == workflow.StatusPass {
		dispositions, err := a.conformance.Dispositions(conformance.WithRun(ctx, task.RunScope()), run.ProjectID, true)
		if err != nil {
			status, reason = workflow.StatusError, "reading exceptions: "+err.Error()
		} else {
			observations, status, reason = a.observeImages(ctx, run, step, cfg, subjects, dispositions)
		}
	}
	if observations == nil {
		if len(subjects) == 0 {
			subjects = []imageSubject{{subject: conformance.ScanSubject{On: cfg.On}}}
		}
		for _, input := range subjects {
			observations = append(observations, conformance.ScanObservation{ID: ids.New(a.now()), ProjectID: run.ProjectID, ChangeID: run.ChangeID, RunID: run.ID, StepID: step.ID, Subject: input.subject, StepConfig: step.Config, ObservedAt: a.now(), Verdict: status.String(), Reason: reason})
		}
	}
	if err := task.InAttempt(ScanKind, func(tx *database.Tx, attempt int64) error {
		for _, o := range observations {
			o.Attempt = attempt
			if err := conformance.RecordScanObservationIn(context.WithoutCancel(ctx), tx, o); err != nil {
				return err
			}
		}
		return task.RecordJanitorScansIn(context.WithoutCancel(ctx), tx)
	}); err != nil {
		status = workflow.StatusError
		reason = "recording the scan observations: " + err.Error()
	}
	task.Settle(workflow.EncodeOutcome(status, map[string]any{"reason": reason, "observations": observations}, nil))
}

func (a *ScanSteps) observeImages(ctx context.Context, run *workflow.Run, step *workflow.Step, cfg scanConfig, subjects []imageSubject, dispositions []conformance.Disposition) ([]conformance.ScanObservation, workflow.StepState, string) {
	status := workflow.StatusPass
	observations := []conformance.ScanObservation{}
	add := func(subject conformance.ScanSubject, scanner string, version int, raw string, verdict workflow.StepState, findings []conformance.Finding, reason string) {
		observations = append(observations, conformance.ScanObservation{ID: ids.New(a.now()), ProjectID: run.ProjectID, ChangeID: run.ChangeID, RunID: run.ID, StepID: step.ID, Subject: subject, Scanner: scanner, BindingVersion: version, Configuration: raw, ConfigHash: deploy.ConfigHash(raw), StepConfig: step.Config, ObservedAt: a.now(), Verdict: verdict.String(), Findings: findings, Reason: reason})
		if verdict == workflow.StatusError || (verdict == workflow.StatusFail && status != workflow.StatusError) {
			status = verdict
		}
	}
	attached, err := a.scanners.attachedScanners(ctx)
	if err != nil {
		for _, input := range subjects {
			add(input.subject, "", 0, "{}", workflow.StatusError, nil, err.Error())
		}
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
	for _, input := range subjects {
		subject := input.subject
		if input.reason != "" {
			add(subject, "", 0, "{}", workflow.StatusError, nil, input.reason)
			continue
		}
		if len(scanners) == 0 {
			add(subject, "", 0, "{}", workflow.StatusError, nil, "no image scanner attached — attach one under Integrations › Scanners")
			continue
		}
		digest := subject.Digest
		if _, ok := cache[digest]; !ok {
			answers := []answer{}
			dir, err := os.MkdirTemp(a.dataDir, "running-scan-")
			if err != nil {
				add(subject, "", 0, "{}", workflow.StatusError, nil, err.Error())
				continue
			}
			// Preparation uses this project's existing per-request login, never an
			// ambient credential. The executor handles the same request on a worker.
			var login *execution.RegistryLogin
			var rerr error
			need := strings.HasPrefix(subject.Part, "need/")
			if !need {
				ri, err := a.integrations.Registry(ctx, run.ProjectID)
				rerr = err
				if ri != nil {
					login = &execution.RegistryLogin{Host: ri.Host, Username: ri.Username, Token: ri.Token}
				}
			}
			image := execution.ImageAnswer{}
			switch {
			case rerr != nil:
				image.Err = rerr.Error()
			case login == nil && !need:
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
	return observations, status, imagesReason(observations, status)
}

// imagesReason names the first subject whose verdict decided the step, so a
// failing scan says which part and why rather than only that it ran.
func imagesReason(observations []conformance.ScanObservation, status workflow.StepState) string {
	if status != workflow.StatusFail && status != workflow.StatusError {
		return "images observed"
	}
	for _, o := range observations {
		if o.Verdict != status.String() {
			continue
		}
		switch {
		case o.Subject.Part != "":
			return o.Subject.Part + ": " + o.Reason
		case o.Subject.Deployment != "":
			return o.Subject.Deployment + ": " + o.Reason
		}
		return o.Reason
	}
	return "images observed"
}

// Both scan points observe exact digests through the same executor and receipts.
type imageSubject struct {
	subject conformance.ScanSubject
	reason  string
}

func (a *ScanSteps) imageSubjects(ctx context.Context, run *workflow.Run, on string) ([]imageSubject, workflow.StepState, string) {
	var rows []deployment.RunningImages
	if on == "bundle" {
		bundle, err := a.bundles.OfRun(ctx, run.ProjectID, run.ID)
		if err != nil {
			return nil, workflow.StatusError, err.Error()
		}
		if bundle == nil {
			return nil, workflow.StatusNotApplicable, "this run has no release bundle"
		}
		if !bundle.ByDigest() {
			return nil, workflow.StatusNotApplicable, "the bundle uses local image IDs"
		}
		images := map[string]string{}
		for part, digest := range bundle.Parts {
			images[part] = digest
		}
		for part, digest := range bundle.Init {
			images["init/"+part] = digest
		}
		rows = []deployment.RunningImages{{BundleID: bundle.ID, Images: images}}
	} else {
		var err error
		rows, err = a.deployments.RunningImages(ctx, run.ProjectID)
		if err != nil {
			return nil, workflow.StatusError, err.Error()
		}
		if len(rows) == 0 {
			return nil, workflow.StatusNotApplicable, "no deployments"
		}
	}
	out := []imageSubject{}
	for _, row := range rows {
		subject := conformance.ScanSubject{On: on, DeploymentID: row.DeploymentID, Deployment: row.Deployment, BundleID: row.BundleID}
		if row.Reason != "" {
			out = append(out, imageSubject{subject, row.Reason})
		}
		parts := make([]string, 0, len(row.Images))
		for part := range row.Images {
			parts = append(parts, part)
		}
		sort.Strings(parts)
		for _, part := range parts {
			subject.Part, subject.Digest = part, row.Images[part]
			out = append(out, imageSubject{subject: subject})
		}
	}
	return out, workflow.StatusPass, ""
}
