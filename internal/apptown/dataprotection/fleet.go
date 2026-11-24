package dataprotection

import (
	"context"
	"time"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/ids"
)

// FleetDeployment is one deployment on the Backups page: its project,
// its protection exactly as its own protection read answers it, and the
// newest set that holds data, for the time and size the row shows.
type FleetDeployment struct {
	Project         string     `json:"project"`
	ProjectName     string     `json:"projectName"`
	Deployment      string     `json:"deployment"`
	DeploymentLabel string     `json:"deploymentLabel"`
	TargetKind      string     `json:"targetKind"`
	Protection      Protection `json:"protection"`
	Newest          *CopyRef   `json:"newest,omitempty"`
}

// LastNight is what the records say of the nightly copies over the
// deployments listed: Of is how many are listed, Copied how many have a
// complete set the nightly run took in the last day, and At the earliest of
// those. It is not a receipt of one sweep: a copy that failed before it
// recorded a set, a partial set, or a deployment not copied nightly is in
// Of and not in Copied, so the count can understate, never overstate.
type LastNight struct {
	Copied int       `json:"copied"`
	Of     int       `json:"of"`
	At     time.Time `json:"at,omitzero"`
}

// Fleet is every deployment that holds data, or may, with the nightly count.
type Fleet struct {
	Deployments []FleetDeployment `json:"deployments"`
	LastNight   LastNight         `json:"lastNight"`
}

// nightlyWindow is how far back a nightly set counts as last night's.
const nightlyWindow = 24 * time.Hour

// FleetProtection, in the projects' own order and each project's
// deployments as Deployment lists them, is each standing deployment that has been deployed, on a
// target AppTown keeps protection for, with the same answer its own
// protection read gives. One known to hold no data is left out; one whose
// holdings are unknown, or with an operation running, is kept: it may hold
// data. It reads recorded readings and the local ledger, never a provider.
func (s *Service) FleetProtection(ctx context.Context) (*Fleet, error) {
	projects, err := s.projects.All(ctx)
	if err != nil {
		return nil, err
	}
	projectIDs := make([]ids.ID, 0, len(projects))
	for _, p := range projects {
		projectIDs = append(projectIDs, p.ID)
	}
	deps, err := s.deployments.ForProjects(ctx, projectIDs)
	if err != nil {
		return nil, err
	}
	targets, err := s.targets.All(ctx)
	if err != nil {
		return nil, err
	}
	targetKind := map[ids.ID]string{}
	for _, t := range targets {
		targetKind[t.ID] = t.Kind
	}
	now := s.now()
	out := &Fleet{Deployments: []FleetDeployment{}}
	for _, p := range projects {
		for _, dep := range deps[p.ID] {
			kind, known := targetKind[dep.TargetID]
			if dep.Kind != deployment.Standing || dep.ObservedGeneration == 0 && dep.LastApplied == nil || !known || !Applies(dep.Kind, kind) {
				continue
			}
			prot, err := s.DeploymentProtection(ctx, p.ID, dep.Name)
			if err != nil {
				return nil, err
			}
			if !holdsData(prot) && prot.Unknown == "" && !prot.Reading.Unknown && !prot.Reading.Waiting && prot.Running == nil {
				continue
			}
			sets, err := s.backups.ForDeployment(ctx, p.ID, dep.Name, 50)
			if err != nil {
				return nil, err
			}
			row := FleetDeployment{Project: p.Slug, ProjectName: p.Name, Deployment: dep.Name, DeploymentLabel: dep.DisplayLabel, TargetKind: kind, Protection: *prot}
			if row.ProjectName == "" {
				row.ProjectName = p.Slug
			}
			if row.DeploymentLabel == "" {
				row.DeploymentLabel = dep.Name
			}
			for _, b := range sets {
				if b.Status.holds() {
					row.Newest = &CopyRef{Backup: b.ID, TakenAt: b.TakenAt, Bytes: b.Bytes}
					break
				}
			}
			out.LastNight.add(sets, now)
			out.Deployments = append(out.Deployments, row)
		}
	}
	return out, nil
}

// add counts one listed deployment: copied when its newest set the nightly
// run took (newest first, as ForDeployment lists them) is complete and was
// taken within the last day.
func (n *LastNight) add(sets []Backup, now time.Time) {
	n.Of++
	for _, b := range sets {
		if b.Trigger != "scheduled" {
			continue
		}
		taken := mustTime(b.TakenAt)
		if b.Status == BackupOK && now.Sub(taken) <= nightlyWindow {
			n.Copied++
			if n.At.IsZero() || taken.Before(n.At) {
				n.At = taken
			}
		}
		return
	}
}

// holdsData says an item keeps data AppTown copies: one declared
// disposable starts empty and is not backed up.
func holdsData(p *Protection) bool {
	for _, it := range p.Items {
		if it.Copy != CopyDisposable {
			return true
		}
	}
	return false
}
