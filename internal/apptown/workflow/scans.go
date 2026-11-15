package workflow

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/strs"
)

// MainScans distinguishes current subjects from retained historical software.
// Historical contains at most the requested number of newest non-current rows.
// Known means every subject could be judged, never that it had no findings.
type MainScans struct {
	Main       string                        `json:"main"`
	Source     []conformance.ScanObservation `json:"source"`
	Running    []runningScan                 `json:"running"`
	Historical []conformance.ScanObservation `json:"historical"`
	Reason     string                        `json:"reason,omitempty"`
	// Unscheduled: the scans are not known and no schedule the project's
	// packs carry scans its source, so a new scan will not come by itself.
	Unscheduled bool `json:"unscheduled"`
	// NoRepository: the project has no repository yet, so there is nothing
	// to scan or update; it is outside the janitor's reach, not unknown.
	NoRepository bool `json:"noRepository"`
	Known        bool `json:"known"`
	Stale        bool `json:"stale"`
	// Failed: a current scan ran and its scanner could not judge the software.
	// Unlike code or images newer than the last scan, which the next scheduled
	// scan answers, a failure needs a person.
	Failed   bool      `json:"-"`
	LastScan time.Time `json:"lastScan"`
}

type runningScan struct {
	conformance.ScanObservation
	ProjectFindings      []conformance.Finding `json:"projectFindings"`
	ImageOnlyFindings    []conformance.Finding `json:"imageOnlyFindings"`
	UnclassifiedFindings []conformance.Finding `json:"unclassifiedFindings"`
}

type RunningImages struct {
	DeploymentID, BundleID ids.ID
	// Deployment is the name a reason uses for this deployment.
	Deployment string
	Reason     string
	Images     map[string]string
}

type scanSchedules interface {
	ScansSource(context.Context, ids.ID) (bool, error)
	ScansSourceFrom(context.Context, ids.ID, ProcedureFacts) (bool, error)
}

type runningImages interface {
	RunningImages(context.Context, ids.ID) ([]RunningImages, error)
}

// LatestMainScans reads software observations without starting work. Only exact
// current subjects support current claims; a scan that could not run classifies
// no running findings. Missing or older-than-three-days evidence is not known.
func (s *Service) LatestMainScans(ctx context.Context, projectID ids.ID, earlier int) (MainScans, error) {
	if _, err := s.require(ctx, operationPermission{Action: actionReadProject, ProjectID: projectID}); err != nil {
		return MainScans{}, err
	}
	return s.latestMainScans(ctx, projectID, earlier, nil)
}

func (s *Service) latestMainScans(ctx context.Context, projectID ids.ID, earlier int, scheduled func() (bool, error)) (MainScans, error) {
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
	heads, err := conformance.ScanObservationHeadsIn(ctx, s.db, projectID)
	if err != nil {
		return MainScans{}, err
	}
	mainTree, err := gitexec.HubReadRef(ctx, repo, main+"^{tree}")
	if err != nil {
		return MainScans{}, err
	}
	selection := selectScanHeads(mainTree, running, heads)
	observations, err := conformance.ScanObservationsByIDIn(ctx, s.db, projectID, selection.candidates)
	if err != nil {
		return MainScans{}, err
	}
	out := latestMainScans(main, running, observations, selection, s.now())
	if earlier > 0 {
		current := map[ids.ID]bool{}
		for _, o := range out.Source {
			current[o.ID] = true
		}
		for _, o := range out.Running {
			current[o.ID] = true
		}
		selected := []ids.ID{}
		for _, h := range heads {
			if (h.Subject.On == "source" || h.Subject.On == "running" || h.Subject.On == "bundle") && !current[h.ID] {
				selected = append(selected, h.ID)
				if len(selected) == earlier {
					break
				}
			}
		}
		loaded := map[ids.ID]conformance.ScanObservation{}
		for _, o := range observations {
			loaded[o.ID] = o
		}
		missing := []ids.ID{}
		for _, id := range selected {
			if _, ok := loaded[id]; !ok {
				missing = append(missing, id)
			}
		}
		older, err := conformance.ScanObservationsByIDIn(ctx, s.db, projectID, missing)
		if err != nil {
			return MainScans{}, err
		}
		for _, o := range older {
			loaded[o.ID] = o
		}
		out.Historical = []conformance.ScanObservation{}
		for _, id := range selected {
			if o, ok := loaded[id]; ok {
				out.Historical = append(out.Historical, o)
			}
		}

	} else {
		out.Historical = []conformance.ScanObservation{}
	}
	if !out.Known {
		if scheduled == nil && s.scanSchedules == nil {
			return MainScans{}, fmt.Errorf("scan schedules are unavailable")
		}
		if scheduled == nil {
			scheduled = func() (bool, error) { return s.scanSchedules.ScansSource(ctx, projectID) }
		}
		hasSchedule, err := scheduled()
		if err != nil {
			return MainScans{}, err
		}
		if !hasSchedule {
			out.Unscheduled, out.Reason = true, "its packs schedule no scan"
		}
	}
	return out, nil
}

// currentSource is the newest run's scan of the whole current code, so its
// errors and missing answers stay current. A landing scans only the commits it
// adds with a scanner that has a history command (stock gitleaks); for a
// scanner that run answered only that way on this tree, the answer is the
// scanner's newest whole-tree observation of the same tree and scanner binding
// from an earlier run. Without one, it stays unanswered. No other scanner is filled.
func currentSource(main string, observations []conformance.ScanHead) []bool {
	current := make([]bool, len(observations))
	run := ids.Nil
	for _, o := range observations {
		if o.Subject.On == "source" && o.Subject.History == "" && currentSourceSubject(o.Subject, main) {
			run = o.RunID
			break
		}
	}
	if run.IsZero() {
		return current
	}
	answered, historyOnly := map[string]bool{}, map[string]conformance.ScanHead{}
	for i, o := range observations {
		if o.Subject.On != "source" || o.RunID != run || !currentSourceSubject(o.Subject, main) {
			continue
		}
		if o.Subject.History == "" {
			current[i], answered[o.Scanner] = true, true
		} else {
			if _, seen := historyOnly[o.Scanner]; !seen {
				historyOnly[o.Scanner] = o
			}
		}
	}
	for i, o := range observations {
		history, ok := historyOnly[o.Scanner]
		if ok && o.Subject.On == "source" && o.Subject.History == "" && o.RunID != run && history.Scanner == o.Scanner && history.BindingVersion == o.BindingVersion && history.ConfigHash == o.ConfigHash && !answered[o.Scanner] && currentSourceSubject(o.Subject, main) {
			current[i], answered[o.Scanner] = true, true
		}
	}
	return current
}

type scanSelection struct {
	source     map[ids.ID]bool
	digestRuns map[string]ids.ID
	candidates []ids.ID
}

func selectScanHeads(main string, running []RunningImages, heads []conformance.ScanHead) scanSelection {
	source := currentSource(main, heads)
	out := scanSelection{source: map[ids.ID]bool{}, digestRuns: map[string]ids.ID{}}
	digests := map[string]bool{}
	for _, dep := range running {
		for _, digest := range dep.Images {
			if digest != "" {
				digests[digest] = true
			}
		}
	}
	for _, h := range heads {
		if digests[h.Subject.Digest] && (h.Subject.On == "running" || h.Subject.On == "bundle") && h.Subject.Digest != "" {
			if _, ok := out.digestRuns[h.Subject.Digest]; !ok {
				out.digestRuns[h.Subject.Digest] = h.RunID
			}
		}
	}
	for i, h := range heads {
		if source[i] {
			out.source[h.ID] = true
		}
		if source[i] || (h.Subject.On == "running" || h.Subject.On == "bundle") && h.Subject.Digest != "" && h.RunID == out.digestRuns[h.Subject.Digest] {
			out.candidates = append(out.candidates, h.ID)
		}
	}
	return out
}

func latestMainScans(main string, running []RunningImages, observations []conformance.ScanObservation, selection scanSelection, now time.Time) MainScans {
	var source []conformance.ScanObservation
	for _, o := range observations {
		if selection.source[o.ID] {
			source = append(source, o)
		}
	}
	unanswered, sourceComplete := unansweredScanners(source)
	out := MainScans{Main: main, Source: []conformance.ScanObservation{}, Running: []runningScan{}, Historical: []conformance.ScanObservation{}, Known: sourceComplete}
	seen := map[string]bool{}
	currentDeployments := map[ids.ID]bool{}
	projected := []conformance.ScanObservation{}
	for _, o := range observations {
		if o.Subject.On == "source" && selection.source[o.ID] && !seen["source/"+o.Scanner] {
			seen["source/"+o.Scanner] = true
			projected = append(projected, o)
		}
	}
	for _, dep := range running {
		complete := len(dep.Images) > 0 && dep.Reason == ""
		if dep.Reason != "" {
			out.Reason = dep.Reason
		}
		for _, part := range slices.Sorted(maps.Keys(dep.Images)) {
			digest := dep.Images[part]
			answered := false
			for _, o := range observations {
				if (o.Subject.On != "running" && o.Subject.On != "bundle") || digest == "" || o.Subject.Digest != digest || o.RunID != selection.digestRuns[digest] {
					continue
				}
				key := fmt.Sprintf("%s/%s/%s", dep.DeploymentID, part, o.Scanner)
				if seen[key] {
					continue
				}
				seen[key], answered = true, true
				o.Subject.DeploymentID, o.Subject.Deployment, o.Subject.Part = dep.DeploymentID, dep.Deployment, part
				projected = append(projected, o)
			}
			complete = complete && answered
		}
		if complete {
			currentDeployments[dep.DeploymentID] = true
		}
	}
	for _, o := range projected {
		if out.LastScan.IsZero() || o.ObservedAt.Before(out.LastScan) {
			out.LastScan = o.ObservedAt
		}
		if o.ObservedAt.Before(now.Add(-3 * 24 * time.Hour)) {
			out.Stale = true
			out.Known = false
		}
		if o.Verdict == StatusError.String() || o.Subject.On == "source" && o.Scanner == "" {
			out.Known, out.Failed = false, true
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
	if len(out.Source) == 0 || len(currentDeployments) != len(running) {
		out.Known = false
	}
	// Each reason names the case and its subject, so a reader knows what a
	// new scan has to answer.
	if !out.Known && out.Reason == "" {
		code := "the current code (" + strs.ShortSHA(main) + ")"
		switch {
		case len(out.Source) == 0:
			out.Reason = code + " has not been scanned"
		case !sourceComplete && len(unanswered) > 0:
			out.Reason = strings.Join(unanswered, " and ") + " gave no result for " + code
		case !sourceComplete:
			out.Reason = "the scan of " + code + " did not say which scanners it ran"
		case len(currentDeployments) != len(running):
			var unscanned []string
			for _, dep := range running {
				if !currentDeployments[dep.DeploymentID] {
					unscanned = append(unscanned, strs.FirstNonBlank(dep.Deployment, "a deployment"))
				}
			}
			if len(unscanned) == 1 {
				out.Reason = "the image running on " + unscanned[0] + " has not been scanned since it was deployed"
			} else {
				out.Reason = "the images running on " + strings.Join(unscanned, " and ") + " have not been scanned since they were deployed"
			}
		case out.Stale:
			out.Reason = "the last scan was on " + out.LastScan.UTC().Format("2 Jan") + ", more than three days ago"
		default:
			out.Reason = "the scanner could not judge the software"
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

func currentSourceSubject(subject conformance.ScanSubject, main string) bool {
	return main != "" && (subject.Tree == main || subject.Commit == "" && subject.Tree == "")
}

// A receipt from one scanner cannot answer for its unfinished peers. Only
// retained observations establish completeness; execution rows are disposable.
// unansweredScanners names the expected scanners without a usable answer;
// a run that named none is incomplete with nothing to name.
func unansweredScanners(observations []conformance.ScanObservation) ([]string, bool) {
	if len(observations) == 0 {
		return nil, false
	}
	var missing []string
	complete := true
	for _, o := range observations {
		if len(o.ExpectedScanners) == 0 {
			complete = false
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
				complete = false
				if scanner != "" && !slices.Contains(missing, scanner) {
					missing = append(missing, scanner)
				}
			}
		}
	}
	return missing, complete
}

// janitorScans carries only the scan facts shown in the fleet view.
type janitorScans struct {
	LastScan time.Time `json:"lastScan"`
}

func (s MainScans) janitorSummary() janitorScans {
	out := janitorScans{LastScan: s.LastScan}
	return out
}
