package deployment

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/apptown/operations"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/ids"
)

const OperationTakeDown = "deployment.take-down"

// DeploymentStatus is Deployment's single display answer. Runtime evidence and
// desired intent remain separate; this answer never authorizes an operation.
type DeploymentStatus struct {
	AppliedAt            time.Time     `json:"appliedAt,omitzero"`
	ServingReleaseNumber int64         `json:"servingReleaseNumber,omitempty"`
	ServingRelease       string        `json:"servingRelease,omitempty"`
	Word                 ProgressLabel `json:"word"`
	Reason               string        `json:"reason"`
	CheckedAt            time.Time     `json:"checkedAt,omitzero"`
	Stale                bool          `json:"stale"`
	RetryAt              time.Time     `json:"retryAt,omitzero"`
}

func (s *Service) displayObservation(ctx context.Context, r listingRecord) (RuntimeObservation, error) {
	rows, err := s.displayObservations(ctx, []listingRecord{r})
	if err != nil {
		return RuntimeObservation{}, err
	}
	return rows[0], nil
}

// displayObservations reads owner facts once for the listed deployments.
func (s *Service) displayObservations(ctx context.Context, rows []listingRecord) ([]RuntimeObservation, error) {
	idsList, subjects := []ids.ID{}, []string{}
	for _, r := range rows {
		idsList = append(idsList, r.deployment.ID)
		subjects = append(subjects, SubjectOf(r.deployment))
	}
	q := deploymentsql.New(s.db.SQL())
	releases, err := q.LatestDeploymentReleases(ctx, idsList)
	if err != nil {
		return nil, err
	}
	latest := map[ids.ID]deploymentsql.LatestDeploymentReleasesRow{}
	for _, r := range releases {
		latest[r.DeploymentID] = r
	}
	servingRows, err := q.ServingReleases(ctx, idsList)
	if err != nil {
		return nil, err
	}
	serving := map[ids.ID]*Release{}
	for _, r := range servingRows {
		v, err := s.Releases.record(r, nil)
		if err != nil {
			return nil, err
		}
		serving[r.ID] = v
	}
	profiles, err := q.AppliedProfiles(ctx, idsList)
	if err != nil {
		return nil, err
	}
	applied := map[ids.ID]AppliedProfile{}
	for _, r := range profiles {
		v, err := appliedFrom(r.Applied)
		if err != nil {
			return nil, err
		}
		applied[r.ID] = v
	}
	live, ended := map[string]*operations.Operation{}, map[string]*operations.Operation{}
	if s.operations != nil {
		live, ended, err = s.operations.SubjectProgress(ctx, subjects, OperationTakeDown)
		if err != nil {
			return nil, err
		}
	}
	readings := map[ids.ID]hostReading{}
	for _, r := range rows {
		if r.target == nil || r.target.runsNoContainers() || r.target.reportsParts() || r.deployment.Container == "" {
			continue
		}
		if _, ok := readings[r.target.ID]; ok {
			continue
		}
		reading, err := NewHostSnapshots(s.db, s.now).reading(ctx, r.target)
		if err != nil {
			return nil, err
		}
		readings[r.target.ID] = reading
	}
	out := make([]RuntimeObservation, len(rows))
	for i, r := range rows {
		dep := r.deployment
		o := s.observations.read(r, s.now())
		subject := SubjectOf(dep)
		if op := live[subject]; op != nil && op.Kind == OperationTakeDown {
			o.Status = DeploymentStatus{Word: progressLabelTakingDown, Reason: fmt.Sprintf("taking it down (operation %d)", op.ID)}
			out[i] = o
			continue
		}
		if op := ended[subject]; op != nil && op.State != operations.Succeeded && op.FinishedAt.Time.After(dep.lastActAt()) {
			o.Status = DeploymentStatus{Word: progressLabelFailed, Reason: "take-down did not finish: " + op.Detail}
			out[i] = o
			continue
		}
		release := latest[dep.ID]
		var releasing *releaseProgress
		// A failed latest release is the answer while the reading predates it,
		// when nothing was ever applied, and when a reading after it finds
		// nothing running: a failure that leaves the app down is headed
		// failed, not stopped. A person's recorded stop is the answer over it
		// (observations.read reports that stop as stopped, never attempted).
		if release.State == "releasing" || release.State == "failed" && dep.RuntimeState != "stopped" && (o.AttemptedAt.Before(release.StageFinishedAt.Time) || dep.ObservedGeneration == 0 || o.State == RuntimeStopped) {
			releasing = &releaseProgress{Number: release.Number, Stage: release.Stage}
			if release.State == "failed" {
				releasing.Failure = release.Failure
			}
		}
		d := Diagnostics{Runtime: diagnosedObservation(o)}
		if releasing == nil && dep.Container != "" && dep.ObservedGeneration > 0 {
			if r.target.reportsParts() {
				why := ""
				switch {
				case o.Err != nil:
					why = "its latest observation failed: " + safeReason(o.Err)
				case o.ObservedAt.IsZero():
					why = "not observed yet"
				case o.Stale:
					why = "its last observation is out of date"
				}
				diagnoseReportedPartsRecord(dep, applied[dep.ID], o, why, &d)
			} else {
				var reading hostReading
				if r.target != nil {
					reading = readings[r.target.ID]
				}
				if err := s.diagnosePartsRecord(dep, r.target, r.project, applied[dep.ID], reading, &d); err != nil {
					return nil, err
				}
			}
		}
		o.Status = deploymentAnswer(dep, o, d, releasing, serving[dep.ServingReleaseID])
		out[i] = o
	}
	return out, nil
}

type releaseProgress struct {
	Number  int64
	Stage   string
	Failure string
}

// stageWords says each deploy stage as the deployment page's stage list does,
// so a status reason never shows a stage's stored name.
var stageWords = map[deploy.DeployStage]string{
	deploy.Preparing: "getting the code ready", deploy.BackingUp: "backing up the data", deploy.Building: "building the app",
	deploy.Starting: "starting the app", deploy.Checking: "checking the app answers", deploy.Live: "live",
}

// releasingReason names the release on its way and, once one is recorded,
// its stage in words.
func releasingReason(r *releaseProgress) string {
	stage, err := deploy.ParseDeployStage(r.Stage)
	if err != nil {
		return fmt.Sprintf("release %d", r.Number)
	}
	return fmt.Sprintf("release %d: %s", r.Number, stageWords[stage])
}

func deploymentAnswer(dep *Deployment, o RuntimeObservation, d Diagnostics, releasing *releaseProgress, applied *Release) (a DeploymentStatus) {
	// An open release owns the word and reason. Runtime evidence cannot
	// change its stage, and the release being deployed is not yet served:
	// what is served is still the applied release, said as for any reading.
	if releasing != nil {
		a = DeploymentStatus{Word: progressLabelDeploying, Reason: releasingReason(releasing)}
		if releasing.Failure != "" {
			a.Word, a.Reason = progressLabelFailed, releasing.Failure
		}
		a.ServingRelease, a.ServingReleaseNumber = servingRelease(o, applied, a.AppliedAt)
		return a
	}
	defer func() {
		a.ServingRelease, a.ServingReleaseNumber = servingRelease(o, applied, a.AppliedAt)
		if a.Stale && !o.ObservedAt.IsZero() {
			if a.Reason == "" {
				a.Reason = "old reading"
			} else {
				a.Reason += " · old reading"
			}
		}
	}()
	a = DeploymentStatus{Word: progressLabelUnknown, CheckedAt: o.AttemptedAt, Stale: o.Stale && !o.ObservedAt.IsZero()}
	if a.CheckedAt.IsZero() {
		a.CheckedAt = o.ObservedAt
	}
	switch dep.RuntimeState {
	case "stopped":
		return DeploymentStatus{Word: progressLabelStopped, Reason: "stopped by " + dep.RuntimeActor, CheckedAt: dep.RuntimeAt.Time}
	case "starting", "stopping", "restarting":
		return DeploymentStatus{Word: progressLabelDeploying, Reason: strings.TrimSpace(dep.RuntimeState + " " + dep.RuntimeError), CheckedAt: dep.RuntimeAt.Time}
	}
	// Nothing was ever applied and no release is on its way (an open or
	// failed first release answered above): no container is expected, so
	// none found is not a stopped app.
	if dep.LastApplied == nil && dep.ObservedGeneration == 0 && dep.Readiness != reconciling {
		return DeploymentStatus{Word: progressLabelNotDeployed, Reason: "nothing deployed yet"}
	}
	if dep.Readiness == reconciling {
		a.Word, a.Reason, a.Stale = progressLabelDeploying, "releasing", false
		return a
	}
	// The deployment's last act waits for evidence, without a clock-based
	// grace period: only a reading begun after the act can say what runs now.
	// A reading begun during a restart may finish after it, still reporting
	// the container stopped.
	if acted := dep.lastActAt(); dep.LastApplied != nil && !o.AttemptedAt.After(acted) {
		a = DeploymentStatus{Word: progressLabelWaiting, Reason: "live — first check pending", AppliedAt: dep.LastApplied.At}
		if applied != nil {
			a.Reason = fmt.Sprintf("release %d live — first check pending", applied.Number)
		}
		return a
	}

	switch o.State {
	case RuntimeRunning:
		a.Word = progressLabelWorking
	case RuntimeStopped:
		a.Word, a.Reason = progressLabelStopped, "the application is stopped"
	case runtimeUnreachable:
		a.Word = progressLabelUnreachable
		host := o.TargetName
		if host == "" {
			host = "the deployment host"
		}
		a.Reason = "cannot reach " + host + "; the app may still be running"
	}
	if o.Err != nil && o.State != runtimeUnreachable {
		a.Reason = safeReason(o.Err)
	}
	if o.ObservedAt.IsZero() && a.Reason == "" {
		a.Reason = "not checked yet"
	}
	if o.Stale && o.State != runtimeUnreachable {
		a.Word = progressLabelUnknown
	}
	if dep.Container == "" || dep.ObservedGeneration == 0 || o.State != RuntimeRunning {
		return a
	}
	// A current public part cannot establish that the entire deployment works.
	a.CheckedAt = d.Reading.AttemptedAt
	a.RetryAt = d.Reading.RetryAt
	for _, p := range d.Parts {
		name := p.Part
		if name == "" {
			name = "application"
		}
		if p.Presence == presenceUnknown {
			a.Word, a.Reason, a.Stale = progressLabelUnknown, name+": "+p.Reason, true
			return a
		}
	}
	if d.Reading.Waiting || d.Reading.Unknown || d.Reading.Stale || len(d.Parts) == 0 || d.Reading.Failure != "" {
		a.Word, a.Stale, a.Reason = progressLabelUnknown, true, d.Reading.Failure
		if a.Reason == "" {
			a.Reason = "waiting for a current parts check"
		}
		return a
	}
	for _, p := range d.Parts {
		name := p.Part
		if name == "" {
			name = "application"
		}
		switch {
		case p.Presence == presenceAbsent || p.Presence == presenceStopped:
			a.Word, a.Reason = progressLabelFailed, name+" is "+p.Presence
			return a
		case p.Facts != nil && p.Facts.Health == "unhealthy":
			a.Word, a.Reason = progressLabelFailed, name+" is unhealthy"
			return a
		}
	}
	return a
}

// servingRelease says which release a deployment serves, and its number when
// the reading supports naming it.
func servingRelease(o RuntimeObservation, applied *Release, appliedAt time.Time) (string, int64) {
	var number int64
	if applied != nil && (!appliedAt.IsZero() || !o.Stale && (o.State == RuntimeRunning || o.State == RuntimeStopped)) {
		number = applied.Number
	}
	return servingReleaseWords(o, applied, appliedAt), number
}

func servingReleaseWords(o RuntimeObservation, release *Release, appliedAt time.Time) string {
	if !appliedAt.IsZero() && release != nil {
		return fmt.Sprintf("Serving release %d", release.Number)
	}
	if o.Stale || (o.State != RuntimeRunning && o.State != RuntimeStopped) {
		return "Serving release unknown"
	}
	if release == nil {
		return "No serving release recorded"
	}
	if o.State == RuntimeRunning {
		return fmt.Sprintf("Running release %d", release.Number)
	}
	return fmt.Sprintf("Recorded release %d", release.Number)
}

// Status reads the display answer for a known deployment. Its caller owns the
// visibility fence, like Deployments.OnTarget; no provider is contacted.
func (s *Service) Status(ctx context.Context, id ids.ID) (DeploymentStatus, error) {
	dep, err := s.Deployments.ByID(ctx, id)
	if err != nil {
		return DeploymentStatus{}, err
	}
	rows, err := s.listRecords(ctx, []ids.ID{dep.ProjectID})
	if err != nil {
		return DeploymentStatus{}, err
	}
	for _, r := range rows {
		if r.deployment.ID == id {
			o, err := s.displayObservation(ctx, r)
			return o.Status, err
		}
	}
	return DeploymentStatus{}, fmt.Errorf("deployment disappeared while reading its status")
}

// UnobservedStatus is the answer when no deployment observation exists.
func UnobservedStatus() DeploymentStatus {
	return DeploymentStatus{Word: progressLabelUnknown, Reason: "not checked yet"}
}
