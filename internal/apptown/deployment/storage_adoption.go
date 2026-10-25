package deployment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/apptown/deployment/internal/releasesql"
	"github.com/flokkit/apptown/internal/apptown/operations"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

// StorageObservation is fresh evidence for one settled deployment. Complete
// means every persistent engine need was witnessed, not that it was adopted.
type StorageObservation struct {
	Deployment string                   `json:"deployment"`
	Generation int64                    `json:"generation"`
	Complete   bool                     `json:"complete"`
	Adopted    bool                     `json:"adopted"`
	Needs      []StorageNeedObservation `json:"needs"`
}

type StorageNeedObservation struct {
	Need     string                 `json:"need"`
	Volume   string                 `json:"volume"`
	Current  *deploy.StorageBinding `json:"current,omitempty"`
	Observed *deploy.StorageBinding `json:"observed,omitempty"`
	Why      string                 `json:"why,omitempty"`
}

var ErrStorageUnsettled = fault.New(fault.Conflict, "storage evidence requires a settled Docker deployment with no open attempt or restore hold")

// PreviewStorage reads existing engine/mount/incarnation evidence without writes.
func (s *Service) PreviewStorage(ctx context.Context, projectID ids.ID, name string) (StorageObservation, error) {
	return s.observeStorage(ctx, projectID, name, false)
}

// AdoptStorage re-observes every engine and atomically establishes only absent
// matching evidence. It never changes provider resources or deletion ownership.
func (s *Service) AdoptStorage(ctx context.Context, projectID ids.ID, name string) (StorageObservation, error) {
	return s.observeStorage(ctx, projectID, name, true)
}

func (s *Service) observeStorage(ctx context.Context, projectID ids.ID, name string, adopt bool) (StorageObservation, error) {
	if s.authority == nil {
		return StorageObservation{}, access.ErrAuthorityUnavailable
	}
	capability := access.CapDeploymentRead
	if adopt {
		capability = access.CapDeploymentOperate
	}
	actor, err := s.authority.Require(ctx, access.Permission{Capability: capability, Resource: access.Resource{Type: "project", ID: projectID.String(), ProjectID: projectID}, Context: access.Context{Route: "deployment.storage-bindings", Consequential: adopt}})
	if err != nil {
		return StorageObservation{}, err
	}
	if adopt && actor.Principal.Name == "" {
		return StorageObservation{}, errors.New("storage adoption caller required")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	dep, err := s.Deployments.ByName(ctx, projectID, name)
	if err != nil {
		return StorageObservation{}, err
	}
	if dep.Kind != Standing || dep.ObservedGeneration == 0 || dep.DesiredGeneration != dep.ObservedGeneration || dep.RestoreCheck != "" {
		return StorageObservation{}, ErrStorageUnsettled
	}
	target, err := s.Targets.ByID(ctx, dep.TargetID)
	if err != nil {
		return StorageObservation{}, err
	}
	inputs, err := s.Outcomes.InputsAtGeneration(ctx, dep.ID, dep.ObservedGeneration)
	if err != nil {
		return StorageObservation{}, err
	}
	if inputs.Revision == "" || s.release.Sources == nil {
		return StorageObservation{}, fmt.Errorf("%w: no recorded source revision", ErrStorageUnsettled)
	}
	sourceProject, err := s.release.Sources.Project(ctx, projectID)
	if err != nil {
		return StorageObservation{}, err
	}
	contract, revision, err := s.release.Sources.Contract(ctx, sourceProject, inputs.Revision)
	if err != nil {
		return StorageObservation{}, err
	}
	if contract == nil || revision != inputs.Revision {
		return StorageObservation{}, fmt.Errorf("%w: recorded source differs", ErrStorageUnsettled)
	}
	var source docker.SourceInputs
	if inputs.DockerSource != "" {
		if err := json.Unmarshal([]byte(inputs.DockerSource), &source); err != nil {
			return StorageObservation{}, err
		}
	}
	// Only engine declarations and data paths are needed; do not derive credentials
	// or initialize the vault while previewing existing storage.
	stack, err := s.env.ResolveDockerStack(ctx, contract, source.Services, nil)
	if err != nil {
		return StorageObservation{}, err
	}
	dk, err := s.hosts.DockerScope(ctx, target)
	if err != nil {
		return StorageObservation{}, err
	}
	names, err := dep.HostNames()
	if err != nil {
		return StorageObservation{}, err
	}
	return database.Transact(ctx, s.db, func(tx *database.Tx) (StorageObservation, error) {
		// Use the same project-then-deployment order as release/removal admission.
		if _, err := releasesql.New(tx.SQL()).LockReleaseProject(ctx, projectID); err != nil {
			return StorageObservation{}, err
		}
		if err := project.RefuseUnlessActiveIn(ctx, tx, projectID); err != nil {
			return StorageObservation{}, err
		}
		holder, err := operations.ConflictsIn(ctx, tx, SubjectOf(dep))
		if err != nil {
			return StorageObservation{}, err
		}
		if holder != nil {
			return StorageObservation{}, &operations.HeldError{Holder: holder}
		}
		current, err := LockDeploymentIn(ctx, tx, dep.ID)
		if err != nil {
			return StorageObservation{}, err
		}
		if current.TargetID != dep.TargetID || current.ObservedGeneration != dep.ObservedGeneration || current.DesiredGeneration != dep.ObservedGeneration || current.RestoreCheck != "" || current.Kind != Standing {
			return StorageObservation{}, ErrStorageUnsettled
		}
		q := deploymentsql.New(tx.SQL())
		busy, err := q.HasOpenAttempt(ctx, deploymentsql.HasOpenAttemptParams{DeploymentID: dep.ID, RunningState: AttemptRunning.String(), UncertainState: AttemptUncertain.String()})
		if err != nil {
			return StorageObservation{}, err
		}
		if busy {
			return StorageObservation{}, ErrStorageUnsettled
		}
		busy, err = releasesql.New(tx.SQL()).ReleaseInFlight(ctx, releasesql.ReleaseInFlightParams{ProjectID: projectID, DeploymentID: dep.ID, OpenState: Releasing.String()})
		if err != nil {
			return StorageObservation{}, err
		}
		if busy {
			return StorageObservation{}, ErrStorageUnsettled
		}
		out := StorageObservation{Deployment: name, Generation: dep.ObservedGeneration, Complete: true, Needs: []StorageNeedObservation{}}
		paths := map[string]string{}
		for _, need := range stack.Needs {
			paths[need.Name] = need.Data
		}
		for _, need := range stack.Services {
			path := paths[need.Name]
			volume := names.NeedVolume(need.Name)
			row := StorageNeedObservation{Need: need.Name, Volume: volume}
			old, found, err := readStorage(ctx, q, dep.ID, need.Name, volume)
			if err != nil {
				return StorageObservation{}, err
			}
			if found {
				row.Current = &old
			}
			if !need.KeepsImage || path == "" {
				if found {
					row.Why = "existing storage binding requires its persistent engine declaration and data path"
					out.Complete = false
					out.Needs = append(out.Needs, row)
				}
				continue
			}
			observed, err := docker.ObserveExistingStorage(ctx, dk, names.Stack(), path, need, row.Current)
			if err != nil {
				row.Why = err.Error()
				out.Complete = false
			} else {
				row.Observed = &observed
			}
			out.Needs = append(out.Needs, row)
		}
		if !adopt {
			return out, nil
		}
		if !out.Complete {
			return StorageObservation{}, fmt.Errorf("%w: one or more engines could not be witnessed; preview storage bindings for details", ErrStorageUnsettled)
		}
		for _, row := range out.Needs {
			if _, err := establishStorageIn(ctx, tx, current, *row.Observed, actor.Principal.Name, s.now()); err != nil {
				return StorageObservation{}, err
			}
		}
		out.Adopted = true
		return out, nil
	})
}
