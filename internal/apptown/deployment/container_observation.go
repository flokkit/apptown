package deployment

import (
	"context"
	"errors"
	"fmt"
	"github.com/flokkit/apptown/internal/dockerlabel"
	"strconv"
	"strings"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/strs"
)

// ContainerObservation is one observation of a deployment's container, with
// the reason when it is unavailable or unknown.
type ContainerObservation struct {
	State  ContainerPresence
	Reason string
}

// ObserveContainer is whether the deployment's container runs, as the
// background observer last saw it: a read never asks a host, so a host that
// does not answer cannot hold up a page. Running or stopped only while the
// observation is fresh; otherwise unknown with why, never stopped or gone. An
// AppTown with no Docker of its own reads its built-in host as unavailable.
// An act that must know now asks with ObserveContainerNow.
func (s *Service) ObserveContainer(ctx context.Context, d *Deployment) ContainerObservation {
	records, err := s.listRecords(ctx, []ids.ID{d.ProjectID})
	if err != nil {
		return ContainerObservation{State: ContainerUnknown, Reason: err.Error()}
	}
	for _, r := range records {
		if r.deployment.ID != d.ID || r.target == nil {
			continue
		}
		if r.target.Kind == KindDockerHost && d.Container == "" {
			// A deployment on a Docker host without a container has not
			// booted one.
			break
		}
		o := s.observations.read(r, s.now())
		switch {
		case errors.Is(o.Err, ErrNoLocalDocker):
			return ContainerObservation{State: ContainerUnavailable, Reason: o.Err.Error()}
		case !o.Stale && o.State == RuntimeRunning:
			return ContainerObservation{State: ContainerRunning}
		case !o.Stale && o.State == RuntimeStopped:
			return ContainerObservation{State: ContainerStopped}
		case o.Err != nil:
			return ContainerObservation{State: ContainerUnknown, Reason: "its host's or platform's status could not be read"}
		case o.ObservedAt.IsZero():
			return ContainerObservation{State: ContainerUnknown, Reason: "it has not been observed yet"}
		case o.Stale:
			return ContainerObservation{State: ContainerUnknown, Reason: "its last observation is out of date"}
		}
		// Fresh, but the provider did not say running or stopped (a
		// rollout in progress).
		return ContainerObservation{State: ContainerUnknown, Reason: "its host or platform does not say whether it runs"}
	}
	return ContainerObservation{State: ContainerUnknown, Reason: "the deployment records no container"}
}

// ObserveContainerNow asks the deployment's own target's daemon whether its
// container runs now, for an act (a runtime check) that needs the answer at
// that moment; reads use ObserveContainer. Only ContainerRunning is evidence
// that it serves; a failure to resolve the target or to get the daemon's
// answer is unknown, never stopped or gone. A deployment without a container
// (App Platform) is its latest observation.
func (s *Service) ObserveContainerNow(ctx context.Context, d *Deployment) ContainerObservation {
	if d.Container == "" {
		return s.ObserveContainer(ctx, d)
	}
	dk, err := s.hosts.DockerScopeFor(ctx, d.TargetID)
	if errors.Is(err, ErrNoLocalDocker) {
		return ContainerObservation{State: ContainerUnavailable, Reason: err.Error()}
	} else if err != nil {
		return ContainerObservation{State: ContainerUnknown, Reason: err.Error()}
	}
	runtime, err := docker.Inspect(ctx, dk, d.Container)
	switch {
	case errors.Is(err, docker.ErrContainerAbsent):
		return ContainerObservation{State: ContainerGone}
	case err != nil:
		return ContainerObservation{State: ContainerUnknown, Reason: err.Error()}
	case runtime.Running:
		return ContainerObservation{State: ContainerRunning}
	}
	return ContainerObservation{State: ContainerStopped}
}

// ServedRevision is the commit a deployment's container serves now, as this
// AppTown witnesses it, or why it cannot be named.
type ServedRevision struct {
	Commit      string // "" when it cannot be named; Reason says why
	ContainerID string // the container that was asked
	Reason      string
}

// ServedRevision names the commit d serves now. The commit is the one the
// deployment's settled generation recorded, and it is named only when the
// container answering on d's own target runs and carries that deployment's
// and generation's identity labels (including a host label retained through
// identity cutover): the container that was booted for the
// recorded generation, not a later or hand-started one under the same name.
// Anything the daemon cannot answer, or a record without a commit, is a
// reason, never a guess.
func (s *Service) ServedRevision(ctx context.Context, d *Deployment) ServedRevision {
	if d.ReleaseSHA == "" {
		return ServedRevision{Reason: "the deployment records no commit"}
	}
	if d.Container == "" {
		return ServedRevision{Reason: "the deployment records no container"}
	}
	dk, err := s.hosts.DockerScopeFor(ctx, d.TargetID)
	if err != nil {
		return ServedRevision{Reason: "its host cannot be asked: " + err.Error()}
	}
	rt, err := docker.Inspect(ctx, dk, d.Container)
	switch {
	case errors.Is(err, docker.ErrContainerAbsent):
		return ServedRevision{Reason: "its container is gone"}
	case err != nil:
		return ServedRevision{Reason: "its container cannot be asked: " + err.Error()}
	case !rt.Running:
		return ServedRevision{ContainerID: rt.ID, Reason: "its container is not running"}
	}
	want := strconv.FormatInt(d.ObservedGeneration, 10)
	if carried := rt.Labels; carried[dockerlabel.Deployment] != string(d.Label()) || carried[dockerlabel.Generation] != want {
		return ServedRevision{ContainerID: rt.ID, Reason: fmt.Sprintf("the running container is not the generation %s the deployment recorded", want)}
	}
	return ServedRevision{Commit: d.ReleaseSHA, ContainerID: rt.ID}
}

// AttachedDockerHostsHealth is Docker deploys' health on an AppTown with no
// Docker of its own: each attached Docker host asked to answer now. They are
// working when one answers, naming it; none attached says to attach one.
func (s *Service) AttachedDockerHostsHealth(ctx context.Context) ProviderHealth {
	targets, err := s.Targets.All(ctx)
	if err != nil {
		return ProviderHealth{OK: false, Detail: "the deployment targets could not be read: " + strs.Truncate(err.Error(), 120)}
	}
	var answered, silent []string
	for _, t := range targets {
		if t.Kind != KindDockerHost || t.Origin != OriginAttached || t.Status == TargetRetired {
			continue
		}
		addr, keyFrom, err := HostTargetAddress(t)
		if err != nil {
			silent = append(silent, t.Name+" ("+err.Error()+")")
			continue
		}
		if ok, words := s.hosts.ProbeHostTarget(ctx, t.Name, addr, keyFrom); ok {
			answered = append(answered, t.Name)
		} else {
			silent = append(silent, t.Name+" ("+strs.Truncate(words, 120)+")")
		}
	}
	const own = "this AppTown has no Docker of its own; it deploys to attached Docker hosts"
	switch {
	case len(answered) == 0 && len(silent) == 0:
		return ProviderHealth{OK: false, Detail: own + ", and none is attached: attach one under Infrastructure"}
	case len(answered) == 0:
		return ProviderHealth{OK: false, Detail: own + ": none answered: " + strings.Join(silent, "; ")}
	case len(silent) > 0:
		return ProviderHealth{OK: true, Detail: own + ": " + strings.Join(answered, ", ") + " answered; " + strings.Join(silent, "; ") + " did not"}
	}
	return ProviderHealth{OK: true, Detail: own + ": " + strings.Join(answered, ", ") + " answered"}
}
