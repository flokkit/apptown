package verification

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

const RebuildKind = "rebuild"

type rebuildConfig struct {
	WhenFailed []string `json:"whenFailed"`
}

// Rebuilds compares running observations with build-time evidence. Deployment
// alone decides whether a request is eligible and owns every release write.
type Rebuilds struct {
	DB          *database.DB
	Deployment  *deployment.Service
	Conformance *conformance.Service
}

func (a Rebuilds) Run(task *workflow.Execution) {
	if _, err := task.Claim(); err != nil {
		return
	}
	results, reason, err := a.request(task)
	status := workflow.StatusPass
	if err != nil {
		status, reason = workflow.StatusError, err.Error()
	} else if len(results) == 0 {
		status = workflow.StatusNotApplicable
	}
	task.Settle(workflow.EncodeOutcome(status, map[string]any{"reason": reason, "releases": results}, nil))
}

type rebuiltDeployment struct {
	Deployment string          `json:"deployment"`
	Release    int64           `json:"release"`
	Findings   []check.Finding `json:"findings"`
}

func (a Rebuilds) request(task *workflow.Execution) ([]rebuiltDeployment, string, error) {
	ctx, run := task.Context(), task.Run()
	var cfg rebuildConfig
	if err := workflow.DecodeStepConfig(task.Step().Config, &cfg); err != nil {
		return nil, "", err
	}
	steps, err := workflow.ReadScopedStepsIn(ctx, a.DB, workflow.RunRef{ID: run.ID, ProjectID: run.ProjectID})
	if err != nil {
		return nil, "", err
	}
	selected := map[ids.ID]*workflow.Step{}
	for _, name := range cfg.WhenFailed {
		found := false
		for _, step := range steps {
			if step.Stage != task.Step().Stage || step.Type != "scan" || (step.Name != name && !strings.HasPrefix(step.Name, name+":")) {
				continue
			}
			found = true
			if step.Status == workflow.StatusPending || step.Status == workflow.StatusRunning {
				return nil, "", fmt.Errorf("step %s has not settled", step.Name)
			}
			selected[step.ID] = step
		}
		if !found {
			return nil, "", fmt.Errorf("whenFailed names unknown scan step %s", name)
		}
	}
	heads, err := conformance.ScanObservationHeadsIn(ctx, a.DB, run.ProjectID)
	if err != nil {
		return nil, "", err
	}
	receipts := make([]ids.ID, 0, len(heads))
	for _, head := range heads {
		if head.Subject.On == "bundle" || head.RunID == run.ID && head.Subject.On == "running" {
			receipts = append(receipts, head.ID)
		}
	}
	observations, err := conformance.ScanObservationsByIDIn(ctx, a.DB, run.ProjectID, receipts)
	if err != nil {
		return nil, "", err
	}
	ds, err := a.Conformance.Dispositions(conformance.WithRun(ctx, task.RunScope()), run.ProjectID, true)
	if err != nil {
		return nil, "", err
	}
	rows, err := a.Deployment.RunningImages(ctx, run.ProjectID)
	if err != nil {
		return nil, "", err
	}
	results := []rebuiltDeployment{}
	reason := "every finding was in this image when it was built: a rebuild would not clear it"
	follows := false
	for _, row := range rows {
		dep, err := a.Deployment.Deployments.ByID(ctx, row.DeploymentID)
		if err != nil {
			return results, "", err
		}
		if dep.Feed != "main" {
			continue
		}
		follows = true
		if len(row.Images) == 0 {
			reason = row.Reason
			continue
		}
		due := []check.Finding{}
		seen := map[string]bool{}
		for part, digest := range row.Images {
			// Receipts are newest first. One current observation per scanner and part.
			scanners := map[string]bool{}
			for _, o := range observations {
				step := selected[o.StepID]
				if step == nil || o.RunID != run.ID || o.Attempt != step.Attempt || o.Subject.On != "running" || o.Subject.DeploymentID != row.DeploymentID || o.Subject.Part != part || o.Subject.Digest != digest {
					continue
				}
				if scanners[o.Scanner] {
					continue
				}
				scanners[o.Scanner] = true
				if step.Status != workflow.StatusFail {
					continue
				}
				var scan struct {
					FailOn string `json:"failOn"`
				}
				if err := json.Unmarshal(step.Config, &scan); err != nil {
					return results, "", err
				}
				for _, f := range rebuildFindings(o, observations, ds, scan.FailOn) {
					if !seen[f.Fingerprint] {
						due = append(due, f)
						seen[f.Fingerprint] = true
					}
				}
			}
		}
		if len(due) == 0 {
			continue
		}
		slices.SortFunc(due, func(a, b check.Finding) int { return cmp.Compare(a.Fingerprint, b.Fingerprint) })
		fingerprints := make([]string, 0, len(due))
		for _, f := range due {
			fingerprints = append(fingerprints, f.Fingerprint)
		}
		why := "the running scan found findings absent from the image's usable build-time scan: " + strings.Join(fingerprints, ", ")
		release, err := a.Deployment.RequestRebuild(ctx, run.ProjectID, dep.Name, why)
		if errors.Is(err, deployment.ErrRebuildNotApplicable) {
			reason = err.Error()
			continue
		}
		if err != nil {
			return results, "", err
		}
		results = append(results, rebuiltDeployment{dep.Name, release.Number, due})
	}
	if len(results) > 0 {
		reason = "requested fresh images of the running releases"
	} else if !follows {
		reason = "no deployment follows main with a running image"
	}
	return results, reason, nil
}

// rebuildFindings uses the same gate as scans, then compares fingerprints with
// the newest usable bundle receipt of this digest and scanner. Errors cannot
// establish a clean baseline; unknown permits a fresh build.
func rebuildFindings(running conformance.ScanObservation, observations []conformance.ScanObservation, ds []conformance.Disposition, failOn string) []check.Finding {
	if running.Verdict != workflow.StatusFail.String() {
		return nil
	}
	var baseline *conformance.ScanObservation
	for i := range observations {
		o := &observations[i]
		if o.Subject.On == "bundle" && o.Subject.Digest == running.Subject.Digest && o.Scanner == running.Scanner && (o.Verdict == workflow.StatusPass.String() || o.Verdict == workflow.StatusFail.String()) {
			baseline = o
			break
		}
	}
	known := map[string]bool{}
	if baseline != nil {
		for _, f := range baseline.Findings {
			known[f.Fingerprint] = true
		}
	}
	_, gating, _ := scanGate(running.Findings, ds, scanBase{}, false)
	var due []check.Finding
	for _, f := range gating {
		if check.FailsAt(failOn, f.Severity) && !known[f.Fingerprint] {
			due = append(due, f.Finding)
		}
	}
	return due
}
