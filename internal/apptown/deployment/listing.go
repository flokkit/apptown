package deployment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/flokkit/apptown/internal/deploy/digitalocean"
	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/access"

	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/deploy"
)

// RuntimeState is a transient observation, independent of persisted readiness.
// An unsuccessful rollout or an unreadable provider does not prove a stopped app.
type RuntimeState uint8

const (
	RuntimeUnknown RuntimeState = iota
	RuntimeRunning
	RuntimeStopped
)

func (s RuntimeState) String() string {
	switch s {
	case RuntimeRunning:
		return "running"
	case RuntimeStopped:
		return "stopped"
	default:
		return "unknown"
	}
}
func (s RuntimeState) Running() *bool {
	if s != RuntimeRunning && s != RuntimeStopped {
		return nil
	}
	running := s == RuntimeRunning
	return &running
}

type RuntimeObservation struct {
	State       RuntimeState
	Err         error
	ObservedAt  time.Time
	AttemptedAt time.Time
	Stale       bool
	Rollout     string
	Health      string
	URL         string
}

type TargetSummary struct{ Name, Kind, Origin, Region string }
type NeedSummary struct {
	Need, Name, ID, Region, Size, Version string
	Status                                NeedState
}
type Summary struct {
	DeploymentPolicy DeploymentPolicy
	ID               ids.ID
	Name             string
	Kind             Kind
	URL              string
	HostOnly         bool // URL answers on the AppTown host only (Reach)
	HostPort         int
	ReleaseRun       ids.ID
	UpdatedAt        string
	Attributes
	Protected  bool
	Generation int64
	Readiness  Readiness
	ChangeID   ids.ID
	Target     *TargetSummary
	Needs      []NeedSummary
	// AIModelsKeys names the AI models keys the deployment owns; what
	// OpenRouter says of them is read on its own (Service.AIModels).
	AIModelsKeys []string
	Observation  RuntimeObservation
	// RestoreCheck holds it after AppTown was restored ("" when nothing does).
	RestoreCheck RestoreCheck
}

type listingRecord struct {
	summary    Summary
	deployment *Deployment
	target     *Target
	project    string
}

// List reads authorized stored facts and the matching background observation.
// It never contacts a runtime provider, including on a cold or stale read.
func (s *Service) List(ctx context.Context, projectID ids.ID) ([]Summary, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.authority == nil {
		return nil, access.ErrAuthorityUnavailable
	}
	if projectID.IsZero() {
		return nil, errors.New("deployment listing requires a project")
	}
	if _, err := s.authority.Require(ctx, access.Permission{Capability: access.CapDeploymentRead, Resource: access.Resource{Type: "project", ID: projectID.String(), ProjectID: projectID}, Context: access.Context{Route: "deployment.list"}}); err != nil {
		return nil, err
	}
	records, err := s.listRecords(ctx, []ids.ID{projectID})
	if err != nil {
		return nil, err
	}
	out := make([]Summary, len(records))
	for i, r := range records {
		r.summary.Observation = s.observations.read(r, s.now())
		reach := s.reachedAt(ctx, r.project, r.deployment, r.target, r.summary.Observation.URL)
		r.summary.URL, r.summary.HostOnly = reach.URL, reach.HostOnly
		out[i] = r.summary
	}
	return out, ctx.Err()
}

func runtimeState(o deploy.Observation) RuntimeState {
	switch o.Rollout {
	case "active":
		return RuntimeRunning
	case "absent":
		return RuntimeStopped
	default:
		return RuntimeUnknown
	}
}

func (s *Service) listRecords(ctx context.Context, projectIDs []ids.ID) ([]listingRecord, error) {
	tx, err := s.db.ReadSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	q := deploymentsql.New(tx.SQL())
	rows, err := q.RuntimeDeployments(ctx, projectIDs)
	if err != nil {
		return nil, err
	}
	out := make([]listingRecord, len(rows))
	if len(rows) == 0 {
		return out, nil
	}
	deploymentIDs, targetIDs := make([]ids.ID, len(rows)), make([]ids.ID, len(rows))
	for i, row := range rows {
		deploymentIDs[i], targetIDs[i] = row.Deployment.ID, row.Deployment.TargetID
	}
	targets, err := q.Targets(ctx, targetIDs)
	if err != nil {
		return nil, err
	}
	byTarget := make(map[ids.ID]*Target, len(targets))
	summaries := make(map[ids.ID]*TargetSummary, len(targets))
	for _, row := range targets {
		target, err := targetRecord(row, nil)
		if err != nil {
			return nil, err
		}
		byTarget[row.ID] = target
		summaries[row.ID] = &TargetSummary{Name: target.Name, Kind: target.Kind, Origin: target.Origin, Region: TargetPlace(target)}
	}
	needs, err := q.NeedInstances(ctx, deploymentIDs)
	if err != nil {
		return nil, err
	}
	byDeployment := make(map[ids.ID][]NeedSummary, len(rows))
	for _, row := range needs {
		need, err := summarizeNeed(row)
		if err != nil {
			return nil, err
		}
		byDeployment[row.DeploymentID] = append(byDeployment[row.DeploymentID], need)
	}
	for i, row := range rows {
		e, err := deploymentRecord(row.Deployment, nil)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(row.Deployment.HandleJson, &e.Handle); err != nil {
			return nil, fmt.Errorf("reading deployment identity: %w", err)
		}
		if !e.TargetID.IsZero() && byTarget[e.TargetID] == nil {
			return nil, fmt.Errorf("deployment %s refers to a missing target", e.Name)
		}
		owned, err := ownedIn(ctx, q, e.ID)
		if err != nil {
			return nil, err
		}
		var keys []string
		needs := byDeployment[e.ID]
		for _, o := range owned {
			switch {
			case o.Kind == KindOpenRouterKey:
				keys = append(keys, o.Name)
			case o.Kind == digitalocean.KindBucket && o.Need != "":
				// An object store's bucket backs its need as a cluster does,
				// though it is on the owned list, not a need instance.
				needs = append(needs, NeedSummary{Need: o.Need, Name: o.Name, ID: o.ID, Status: NeedReady})
			}
		}
		out[i] = listingRecord{project: row.Slug, deployment: e, target: byTarget[e.TargetID], summary: Summary{
			DeploymentPolicy: e.UpdatePolicy(), ID: e.ID, Name: e.Name, RestoreCheck: e.RestoreCheck, Kind: e.Kind, URL: e.URL, HostPort: e.HostPort, ReleaseRun: e.ReleaseRun, UpdatedAt: e.UpdatedAt,
			Attributes: e.releaseAttributes(), Protected: e.Protected(), Generation: e.ObservedGeneration, Readiness: e.Readiness,
			ChangeID: e.ChangeID, Target: summaries[e.TargetID], Needs: needs, AIModelsKeys: keys,
		}}
	}
	return out, nil
}

func summarizeNeed(row deploymentsql.NeedInstance) (NeedSummary, error) {
	var handle struct {
		Name, ID, Region, Size, Version string
		Requested                       struct{ Name string }
	}
	if err := json.Unmarshal(row.HandleJson, &handle); err != nil {
		return NeedSummary{}, fmt.Errorf("reading %s identity: %w", row.Need, err)
	}
	if handle.Name == "" {
		handle.Name = handle.Requested.Name
	}
	n := NeedSummary{Need: row.Need, Name: handle.Name, ID: handle.ID, Region: handle.Region, Size: handle.Size, Version: handle.Version}
	if err := n.Status.Scan(row.Status); err != nil {
		return NeedSummary{}, err
	}
	return n, nil
}
