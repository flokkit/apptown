package workflow

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/ids"
)

// MainScans distinguishes current subjects from retained historical software.
// Known means every subject could be judged, never that it had no findings.
type MainScans struct {
	Main       string                        `json:"main"`
	Source     []conformance.ScanObservation `json:"source"`
	Running    []runningScan                 `json:"running"`
	Historical []conformance.ScanObservation `json:"historical"`
	Reason     string                        `json:"reason,omitempty"`
	// NoRepository: the project has no repository yet, so there is nothing
	// to scan or update; it is outside the janitor's reach, not unknown.
	NoRepository bool      `json:"noRepository"`
	Known        bool      `json:"known"`
	Stale        bool      `json:"stale"`
	LastScan     time.Time `json:"lastScan"`
}

type runningScan struct {
	conformance.ScanObservation
	ProjectFindings      []conformance.Finding `json:"projectFindings"`
	ImageOnlyFindings    []conformance.Finding `json:"imageOnlyFindings"`
	UnclassifiedFindings []conformance.Finding `json:"unclassifiedFindings"`
}

type RunningImages struct {
	DeploymentID, BundleID ids.ID
	Images                 map[string]string
}

type runningImages interface {
	RunningImages(context.Context, ids.ID) ([]RunningImages, error)
}

// LatestMainScans reads software observations without starting work. Only exact
// current subjects support current claims; a scan that could not run classifies
// no running findings. Missing or older-than-three-days evidence is not known.
func (s *Service) LatestMainScans(ctx context.Context, projectID ids.ID) (MainScans, error) {
	if _, err := s.require(ctx, operationPermission{Action: actionReadProject, ProjectID: projectID}); err != nil {
		return MainScans{}, err
	}
	p, err := workflowsql.New(s.db.SQL()).ProjectByID(ctx, projectID)
	if err != nil {
		return MainScans{}, err
	}
	// A project with no repository has no main to read: Git is never asked
	// for an empty branch name in a folder that does not exist.
	if p.DefaultBranch == "" {
		return MainScans{Source: []conformance.ScanObservation{}, Running: []runningScan{}, Historical: []conformance.ScanObservation{}, NoRepository: true, Reason: "no repository yet, so there is nothing to scan"}, nil
	}
	repo, err := (project.RepositoryRef{ID: p.ID, Slug: p.Slug}).Path(s.dataDir)
	if err != nil {
		return MainScans{}, err
	}
	main, err := gitexec.HubReadRef(ctx, repo, "refs/heads/"+p.DefaultBranch)
	if err != nil {
		return MainScans{}, err
	}
	if s.runningImages == nil {
		return MainScans{}, fmt.Errorf("running software observations are unavailable")
	}
	running, err := s.runningImages.RunningImages(ctx, projectID)
	if err != nil {
		return MainScans{}, err
	}
	observations, err := conformance.ScanObservationsIn(ctx, s.db, projectID)
	if err != nil {
		return MainScans{}, err
	}
	source := []conformance.ScanObservation{}
	for _, o := range observations {
		if o.Subject.On == "source" && o.Subject.History == "" && currentSourceSubject(o.Subject.Commit, main) {
			if len(source) == 0 || source[0].RunID == o.RunID {
				source = append(source, o)
			}
		}
	}
	return latestMainScans(main, running, observations, s.now(), sourceScansComplete(source)), nil
}

func latestMainScans(main string, running []RunningImages, observations []conformance.ScanObservation, now time.Time, sourceComplete bool) MainScans {
	out := MainScans{Main: main, Source: []conformance.ScanObservation{}, Running: []runningScan{}, Historical: []conformance.ScanObservation{}, Known: sourceComplete}
	sourceRun, runningRun := ids.Nil, ids.Nil
	for _, o := range observations {
		if o.Subject.On == "source" && o.Subject.History == "" && currentSourceSubject(o.Subject.Commit, main) && sourceRun.IsZero() {
			sourceRun = o.RunID
		}
		if o.Subject.On == "running" && runningRun.IsZero() {
			runningRun = o.RunID
		}
	}
	seen := map[string]bool{}
	currentDeployments := map[ids.ID]bool{}
	for _, o := range observations {
		if o.Subject.On != "source" && o.Subject.On != "running" {
			continue
		}
		current := false
		switch o.Subject.On {
		case "source":
			current = o.Subject.History == "" && o.RunID == sourceRun && currentSourceSubject(o.Subject.Commit, main)
		case "running":
			if o.RunID == runningRun {
				if len(running) == 0 && o.Subject.DeploymentID.IsZero() && o.Verdict == StatusNotApplicable.String() {
					current = true
				}
				for _, dep := range running {
					if dep.DeploymentID == o.Subject.DeploymentID && dep.BundleID == o.Subject.BundleID && (dep.Images[o.Subject.Part] == o.Subject.Digest) {
						current = true
						currentDeployments[dep.DeploymentID] = true
						break
					}
				}
			}
		}
		key := fmt.Sprintf("%s/%s/%s/%s", o.Subject.On, o.Subject.DeploymentID, o.Subject.Part, o.Scanner)
		if !current || seen[key] {
			out.Historical = append(out.Historical, o)
			continue
		}
		seen[key] = true
		if out.LastScan.IsZero() || o.ObservedAt.Before(out.LastScan) {
			out.LastScan = o.ObservedAt
		}
		if o.ObservedAt.Before(now.Add(-3 * 24 * time.Hour)) {
			out.Stale = true
			out.Known = false
		}
		if o.Verdict == StatusError.String() || o.Subject.On == "source" && o.Scanner == "" {
			out.Known = false
			out.Reason = o.Reason
			if out.Reason == "" {
				out.Reason = "scanner could not judge the software"
			}
		}
		if o.Subject.On == "source" {
			out.Source = append(out.Source, o)
		} else {
			out.Running = append(out.Running, runningScan{ScanObservation: o, ProjectFindings: []conformance.Finding{}, ImageOnlyFindings: []conformance.Finding{}, UnclassifiedFindings: []conformance.Finding{}})
		}
	}
	if len(out.Source) == 0 || len(out.Running) == 0 || len(currentDeployments) != len(running) {
		out.Known = false
	}
	if !out.Known && out.Reason == "" {
		switch {
		case len(out.Source) == 0:
			out.Reason = "no scan of the current project code"
		case !sourceComplete:
			out.Reason = "scan not finished"
		case len(out.Running) == 0 || len(currentDeployments) != len(running):
			out.Reason = "no running image scan"
		case out.Stale:
			out.Reason = "scan older than three days"
		default:
			out.Reason = "scanner could not judge the software"
		}
	}
	// One unavailable source scanner makes the source answer incomplete.
	sourceKnown := sourceComplete && len(out.Source) > 0
	for _, o := range out.Source {
		if o.Verdict == StatusError.String() || o.Scanner == "" {
			sourceKnown = false
		}
	}
	for i := range out.Running {
		for _, f := range out.Running[i].Findings {
			if !sourceKnown {
				out.Running[i].UnclassifiedFindings = append(out.Running[i].UnclassifiedFindings, f)
				continue
			}
			projectFinding := false
			if sourceKnown {
				for _, o := range out.Source {
					for _, sf := range o.Findings {
						if samePackageFinding(f.Finding, sf.Finding) {
							projectFinding = true
						}
					}
				}
			}
			if projectFinding {
				out.Running[i].ProjectFindings = append(out.Running[i].ProjectFindings, f)
			} else {
				out.Running[i].ImageOnlyFindings = append(out.Running[i].ImageOnlyFindings, f)
			}
		}
	}
	return out
}

func samePackageFinding(a, b check.Finding) bool {
	if a.Package == nil || b.Package == nil || a.Package.Name != b.Package.Name || a.Package.Ecosystem != b.Package.Ecosystem {
		return false
	}
	for _, id := range a.Identifiers {
		if slices.Contains(b.Identifiers, id) {
			return true
		}
	}
	return false
}

func currentSourceSubject(commit, main string) bool {
	return main != "" && (commit == main || commit == "")
}

// A receipt from one scanner cannot answer for its unfinished peers. Only
// retained observations establish completeness; execution rows are disposable.
func sourceScansComplete(observations []conformance.ScanObservation) bool {
	if len(observations) == 0 {
		return false
	}
	for _, o := range observations {
		if len(o.ExpectedScanners) == 0 {
			return false
		}
		for _, scanner := range o.ExpectedScanners {
			found := false
			for _, answer := range observations {
				if scanner != "" && answer.Scanner == scanner && (answer.Verdict == "pass" || answer.Verdict == "fail" || answer.Verdict == "not_applicable") {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
	}
	return true
}
