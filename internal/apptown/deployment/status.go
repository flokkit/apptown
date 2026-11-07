package deployment

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

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
	o := s.observations.read(r, s.now())
	release, err := deploymentsql.New(s.db.SQL()).LatestDeploymentRelease(ctx, r.deployment.ID)
	var releasing *releaseProgress
	if found, err := database.Lookup(err); err != nil {
		return o, err
	} else if found && (release.State == "releasing" || release.State == "failed" && o.AttemptedAt.Before(release.StageFinishedAt.Time)) {
		releasing = &releaseProgress{Number: release.Number, Stage: release.Stage}
		if release.State == "failed" {
			releasing.Failure = release.Failure
		}
	}
	if releasing != nil {
		o.Status = deploymentAnswer(r.deployment, o, Diagnostics{}, releasing, nil)
		return o, nil
	}
	var d Diagnostics
	if r.deployment.Container != "" && r.deployment.ObservedGeneration > 0 {
		d, err = s.collectDiagnostics(ctx, r.deployment.ProjectID, r.deployment.Name)
		if err != nil {
			return o, err
		}
	}
	applied, err := s.releaseForGeneration(ctx, r.deployment)
	if err != nil {
		return o, err
	}
	o.Status = deploymentAnswer(r.deployment, o, d, releasing, applied)
	return o, nil
}

type releaseProgress struct {
	Number  int64
	Stage   string
	Failure string
}

func deploymentAnswer(dep *Deployment, o RuntimeObservation, d Diagnostics, releasing *releaseProgress, applied *Release) (a DeploymentStatus) {
	// An open release owns the answer. Runtime evidence cannot change its stage.
	if releasing != nil {
		a = DeploymentStatus{Word: progressLabelDeploying, Reason: fmt.Sprintf("release %d %s", releasing.Number, releasing.Stage), ServingRelease: fmt.Sprintf("Serving release %d", releasing.Number), ServingReleaseNumber: releasing.Number}
		if releasing.Failure != "" {
			a.Word, a.Reason = progressLabelFailed, releasing.Failure
		}
		return a
	}
	defer func() {
		a.ServingRelease = servingReleaseWords(o, applied, a.AppliedAt)
		if applied != nil && (!a.AppliedAt.IsZero() || !o.Stale && (o.State == RuntimeRunning || o.State == RuntimeStopped)) {
			a.ServingReleaseNumber = applied.Number
		}
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
	if dep.Readiness == reconciling {
		a.Word, a.Reason, a.Stale = progressLabelDeploying, "releasing", false
		return a
	}
	// A completed apply waits for evidence, without a clock-based grace period.
	if dep.LastApplied != nil && o.AttemptedAt.Before(dep.LastApplied.At) && o.ObservedAt.Before(dep.LastApplied.At) {
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
