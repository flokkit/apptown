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
	Word      ProgressLabel `json:"word"`
	Reason    string        `json:"reason"`
	CheckedAt time.Time     `json:"checkedAt,omitzero"`
	Stale     bool          `json:"stale"`
	RetryAt   time.Time     `json:"retryAt,omitzero"`
}

func (s *Service) displayObservation(ctx context.Context, r listingRecord) (RuntimeObservation, error) {
	o := s.observations.read(r, s.now())
	release, err := deploymentsql.New(s.db.SQL()).ReleasingDeployment(ctx, r.deployment.ID)
	var releasing *releaseProgress
	if found, err := database.Lookup(err); err != nil {
		return o, err
	} else if found {
		releasing = &releaseProgress{Number: release.Number, At: release.CreatedAt}
	}
	var d Diagnostics
	if r.deployment.Container != "" && r.deployment.ObservedGeneration > 0 {
		d, err = s.collectDiagnostics(ctx, r.deployment.ProjectID, r.deployment.Name)
		if err != nil {
			return o, err
		}
	}
	o.Status = deploymentAnswer(r.deployment, o, d, releasing)
	return o, nil
}

type releaseProgress struct {
	Number int64
	At     time.Time
}

func deploymentAnswer(dep *Deployment, o RuntimeObservation, d Diagnostics, releasing *releaseProgress) DeploymentStatus {
	a := DeploymentStatus{Word: progressLabelUnknown, CheckedAt: o.AttemptedAt, Stale: o.Stale}
	if a.CheckedAt.IsZero() {
		a.CheckedAt = o.ObservedAt
	}
	switch dep.RuntimeState {
	case "stopped":
		return DeploymentStatus{Word: progressLabelStopped, Reason: "stopped by " + dep.RuntimeActor, CheckedAt: dep.RuntimeAt.Time}
	case "starting", "stopping", "restarting":
		return DeploymentStatus{Word: progressLabelDeploying, Reason: strings.TrimSpace(dep.RuntimeState + " " + dep.RuntimeError), CheckedAt: dep.RuntimeAt.Time}
	}
	if releasing != nil || dep.Readiness == reconciling {
		a.Word, a.Reason, a.Stale = progressLabelDeploying, "releasing", false
		if releasing != nil {
			a.Reason = fmt.Sprintf("Release %d is releasing", releasing.Number)
			a.CheckedAt = releasing.At
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
	} else if o.Stale && a.Reason == "" {
		a.Reason = "the runtime reading is out of date"
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
	return DeploymentStatus{Word: progressLabelUnknown, Reason: "not checked yet", Stale: true}
}
