package preview

import (
	"context"
	"errors"
	"flokkit.io/forge/internal/deploy/appcontract"
	"fmt"
	"log"
	"net/http/httputil"
	"path/filepath"
	"sync"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/strs"
	"flokkit.io/forge/internal/worker"
)

// Previews on a worker (design/operations/workers.md, "Previews on the
// worker").
// With a worker, a change's and a run's previews boot there and nowhere
// else: the forge prepares the tree and every value the boot reads, the
// worker boots them, and the forge's proxy forwards visitors to them.
// A release's preview boots there too when the release was published to a
// registry, from its images by digest (release_boot.go); a release
// recorded before then boots on this machine, with the image it recorded.

// Worker is the worker's side of previews (worker.Previews).
type Worker interface {
	Boot(ctx context.Context, spec worker.PreviewSpec, prepared docker.Prepared, tree string) (worker.PreviewState, error)
	State(ctx context.Context, name string) (worker.PreviewState, error)
	Remove(ctx context.Context, name string) error
	// RemoveImages removes the build images a change's preview kept once
	// it was stopped (unless a container uses them).
	RemoveImages(ctx context.Context, name string) error
	List(ctx context.Context) ([]string, error)
	// Admit asks whether the worker's daemon can give each part and need
	// the resources it declares, doing nothing there.
	Admit(ctx context.Context, c *appcontract.Contract) error
	// Forward proxies requests to the preview; publicScheme "" is the naked
	// door, with no forwarding headers.
	Forward(name, publicScheme string) *httputil.ReverseProxy
}

// The forge keeps only the previews on the worker it has a record of
// (servedPreviews): deciding to discard one forgets its record first, and
// tryHoldPreview holds a preview's name unless a boot holds it now.
func (s *Service) tryHoldPreview(name string) (unlock func(), busy bool) {
	v, _ := s.booting.LoadOrStore(name, &sync.Mutex{})
	m := v.(*sync.Mutex)
	if !m.TryLock() {
		return nil, true
	}
	return m.Unlock, false
}

// ReconcileWorkerPreviews removes every preview on the worker the forge has
// no record of and is not booting. A removal that never reached the worker
// is therefore made on a later pass, however long the worker was away,
// and a boot that finished after its change closed is removed the same way.

// holdPreview holds one preview's name from the start of its boot on the
// worker until its record is kept, so a reconciliation never takes a
// preview for one the forge has no record of while it boots.
func (s *Service) holdPreview(name string) func() {
	v, _ := s.booting.LoadOrStore(name, &sync.Mutex{})
	m := v.(*sync.Mutex)
	m.Lock()
	return m.Unlock
}

// ReconcileWorkerPreviews removes each preview on the worker this forge has
// no record of, and is not booting. Until the start-up removal is
// confirmed it does nothing: that removal covers every preview.
func (s *Service) ReconcileWorkerPreviews(ctx context.Context) error {
	if s.worker == nil || s.startClearPending() {
		return nil
	}
	names, err := s.worker.List(ctx)
	if err != nil {
		return &UnavailableError{Cause: err}
	}
	var failed []error
	for _, name := range names {
		v, _ := s.booting.LoadOrStore(name, &sync.Mutex{})
		m := v.(*sync.Mutex)
		if !m.TryLock() {
			continue // booting now; its record follows
		}
		if _, recorded := s.servedRecordOf(name); !recorded {
			if err := s.worker.Remove(ctx, name); err != nil {
				failed = append(failed, fmt.Errorf("%s: %w", name, err))
				if errors.Is(err, worker.ErrClaimRequired) {
					m.Unlock()
					s.claimAgainIfLost(ctx, err)
					return errors.Join(failed...)
				}
			}
		}
		m.Unlock()
	}
	return errors.Join(failed...)
}

// RunWorkerPreviewReconciliation, every interval (a minute) until ctx ends,
// asks the worker again to remove the previews from before this start
// while that removal is unconfirmed, then reconciles the worker's previews
// with the forge's records. An unconfirmed removal is therefore tried
// again within a minute; until it is confirmed, reconciliation does nothing.
func (s *Service) RunWorkerPreviewReconciliation(ctx context.Context) {
	t := time.NewTicker(s.startClear.every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s.retryStartClear(ctx)
		if err := s.ReconcileWorkerPreviews(ctx); err != nil {
			log.Printf("reconciling the previews on the worker: %v", err)
		}
	}
}

// UnavailableError is a preview the worker could not be asked about:
// never the app's failure, and never evidence about it.
type UnavailableError struct{ Cause error }

func (e *UnavailableError) Error() string {
	return "previews run on the worker, which could not be asked: " + e.Cause.Error()
}
func (e *UnavailableError) Unwrap() error         { return e.Cause }
func (e *UnavailableError) FaultKind() fault.Kind { return fault.Unavailable }

// ErrNoDomain refuses previews on a worker without a preview domain:
// they are reached only through the forge's preview proxy, by name.
var ErrNoDomain = fault.Wrap(fault.Unprocessable, errors.New("previews run on the worker, and are reached through AppTown's preview domain, which this AppTown does not have (-tier-domain preview=<domain>)"))

// IsAppBootFailure says whether a preview's failure is the app's to answer
// for: anything but a place previews run that could not be asked, or a forge
// that cannot run previews there.
func IsAppBootFailure(err error) bool {
	var unavailable *UnavailableError
	return err != nil && !errors.As(err, &unavailable) && !errors.Is(err, ErrNoDomain)
}

// PreviewsOnWorker says whether changes' and runs' previews boot on the worker.
func (s *Service) PreviewsOnWorker() bool { return s != nil && s.worker != nil }

// PreviewServedOnWorker says whether the preview by this container name is
// served on the worker: a change's and a run's previews boot there when the
// forge has one; a release's look only when the forge recorded booting it
// there (ServedOnWorker); a standing deployment never.
func (s *Service) PreviewServedOnWorker(name string) bool {
	return s.PreviewsOnWorker() && (docker.IsWorkPreviewName(name) || s.ServedOnWorker(name))
}

// ChangeStatus is what a change's preview's button says: a boot in
// progress first, else what observeChange finds. inProgress is the change's
// state, which the caller reads from Work.
func (s *Service) ChangeStatus(ctx context.Context, projectID, changeID ids.ID, inProgress bool) Status {
	return s.runtime.Inspect(ctx, ChangeKey(changeID), changeObservation{s: s, projectID: projectID, changeID: changeID, inProgress: inProgress})
}

// changeObservation observes one change's preview for the runtime.
type changeObservation struct {
	s                   *Service
	projectID, changeID ids.ID
	inProgress          bool
}

func (o changeObservation) ObservePreview(ctx context.Context) Observation {
	return o.s.observeChange(ctx, o.projectID, o.changeID, o.inProgress)
}

// observeChange answers where a change's preview is served and whether it
// runs: on the worker when the forge has one (unavailable when the worker
// cannot be asked); otherwise on this forge's own Docker (unavailable when
// it has none, never "not running"). With nothing running, a change still
// in progress whose workspace declares no app has nothing to preview.
func (s *Service) observeChange(ctx context.Context, projectID, changeID ids.ID, inProgress bool) Observation {
	name := docker.PreviewChangeName(changeID)
	label := fmt.Sprintf("change-%d", changeID)
	dk := s.hosts.Local()
	if s.PreviewsOnWorker() {
		st, err := s.PreviewOnWorker(ctx, name)
		if err != nil {
			return Observation{Unavailable: err.Error()}
		}
		if st.Running {
			return Observation{URL: s.env.RoutesFor(ctx, projectID).PreviewURL(label, 0)}
		}
	} else if dk.Absent() {
		// No worker and no local Docker: nowhere to ask, never "not running".
		return Observation{Unavailable: docker.ErrNoLocalBackend.Error()}
	} else if dk.Running(ctx, name) {
		if port, err := docker.ContainerHostPort(ctx, dk, name, 0); err == nil && port > 0 {
			return Observation{URL: s.env.RoutesFor(ctx, projectID).PreviewURL(label, port), HostOnly: s.PreviewsHostOnly()}
		}
	}
	missing := false
	if inProgress {
		ws := filepath.Join(s.dataDir, "workspaces", fmt.Sprintf("change-%d", changeID))
		_, err := docker.ReadWorkspaceContract(ws)
		missing = err == docker.ErrNoContract
	}
	return Observation{NoContract: missing}
}

// PreviewsHaveDomain reports whether previews are reached at their own
// preview-domain address. That address is the one a chat's container can
// reach too: a preview on this forge's own Docker publishes its port on the
// host's loopback only, which no container and no other machine reaches.
func (s *Service) PreviewsHaveDomain() bool {
	return s != nil && s.env.Routes().PreviewDomain != ""
}

// PreviewsHostOnly says whether a preview's address answers on the forge
// host only: with no preview domain a preview is at its published port on
// this forge's own Docker (Routing.PortAddressesHostOnly).
func (s *Service) PreviewsHostOnly() bool {
	return s != nil && !s.PreviewsHaveDomain() && s.env.Routes().PortAddressesHostOnly()
}

// bootOnWorker boots one preview incarnation on the worker. A boot's own
// failure comes back as the boot error it is, with its phase; a worker that
// cannot be asked is UnavailableError.
func (s *Service) bootOnWorker(ctx context.Context, spec worker.PreviewSpec, prepared docker.Prepared, tree string) (worker.PreviewState, error) {
	if s.env.Routes().PreviewDomain == "" {
		return worker.PreviewState{}, ErrNoDomain
	}
	if s.startClearPending() {
		return worker.PreviewState{}, &UnavailableError{Cause: errors.New("the previews on the worker from before AppTown started are not yet removed")}
	}
	st, err := s.worker.Boot(ctx, spec, prepared, tree)
	if err != nil {
		s.claimAgainIfLost(ctx, err)
		return worker.PreviewState{}, &UnavailableError{Cause: err}
	}
	if st.Error != "" || !st.Running {
		return worker.PreviewState{Page: st.Page}, &docker.BootError{Phase: st.Phase, Err: errors.New(st.Error), LogTail: st.LogTail}
	}
	return st, nil
}

// removeOnWorker removes one preview's whole stack on the worker.
func (s *Service) removeOnWorker(ctx context.Context, name string) error {
	if err := s.worker.Remove(ctx, name); err != nil {
		s.claimAgainIfLost(ctx, err)
		return &UnavailableError{Cause: err}
	}
	return nil
}

// claimAgainIfLost claims the worker again, as the forge does when it
// starts, when the worker lost the order of its previews' requests: every
// preview there goes, and each boots again when asked.
func (s *Service) claimAgainIfLost(ctx context.Context, err error) {
	if errors.Is(err, worker.ErrClaimRequired) {
		log.Printf("the worker lost the order of its previews' requests; claiming it again")
		s.RemoveWorkerPreviews(ctx)
	}
}

// PreviewOnWorker is a preview's state on the worker.
func (s *Service) PreviewOnWorker(ctx context.Context, name string) (worker.PreviewState, error) {
	st, err := s.worker.State(ctx, name)
	if err != nil {
		return worker.PreviewState{}, &UnavailableError{Cause: err}
	}
	return st, nil
}

// LiveChange is a change's preview running now. StartedAt is when its
// container started, "" where previews run that does not say (the worker).
type LiveChange struct {
	ChangeID  ids.ID
	StartedAt string
}

// LiveChangePreviews lists the changes' previews running now, where they run:
// on the worker when one is configured, else on this forge's own Docker.
// When that place cannot be asked it is an error, never an empty list: a
// worker that does not answer is UnavailableError, a forge with no
// local Docker docker.ErrNoLocalBackend. The worker's list is its cleanup
// inventory (stopped containers and kept folders too), so each listed
// preview is asked for its state, and only a running one is live; a state the worker cannot give makes the whole answer
// unavailable rather than a guess.
func (s *Service) LiveChangePreviews(ctx context.Context) ([]LiveChange, error) {
	var names []string
	var err error
	if s.PreviewsOnWorker() {
		if names, err = s.worker.List(ctx); err != nil {
			return nil, &UnavailableError{Cause: err}
		}
	} else if s.hosts.Local().Absent() {
		return nil, docker.ErrNoLocalBackend
	} else if names, err = s.hosts.Local().List(ctx, "ps", "--format", "{{.Names}}"); err != nil {
		return nil, err
	}
	var out []LiveChange
	for _, name := range names {
		id, ok := docker.ParsePreviewChangeID(name)
		if !ok {
			continue
		}
		p := LiveChange{ChangeID: id}
		if s.PreviewsOnWorker() {
			st, err := s.PreviewOnWorker(ctx, name)
			if err != nil {
				return nil, err
			}
			if !st.Running {
				continue
			}
		} else {
			p.StartedAt = s.hosts.Local().Text(ctx, "inspect", "-f", "{{.State.StartedAt}}", name)
		}
		out = append(out, p)
	}
	return out, nil
}

// startClear is the forge's start-up removal of every preview on the
// worker. Until the worker confirms it, no preview boots there: a boot
// could otherwise be removed by the retry, or keep one from before the
// start. The worker previews' loop (RunWorkerPreviewReconciliation) tries
// it again every interval until it succeeds.
type startClear struct {
	mu      sync.Mutex
	pending bool
	every   time.Duration // the worker previews' loop's interval
}

// RemoveWorkerPreviews removes every preview on the worker: a forge that
// starts again boots each again when asked. If the worker cannot confirm
// it now, it stays pending for the worker previews' loop to try again, and
// worker previews wait for it.
func (s *Service) RemoveWorkerPreviews(ctx context.Context) {
	if s.worker == nil {
		return
	}
	c := &s.startClear
	c.mu.Lock()
	c.pending = true
	c.mu.Unlock()
	// Startup and a lost claim have no context of their own; a try that
	// fails is the loop's to repeat.
	s.retryStartClear(ctx)
}

// retryStartClear asks the worker again to remove every preview, if the
// forge's start-up removal is still unconfirmed, and says whether none is
// pending.
func (s *Service) retryStartClear(ctx context.Context) bool {
	c := &s.startClear
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.pending {
		return true
	}
	// The claim forgets only the records made before it began: a boot
	// recorded after that may be one the claim left alone. A record is not
	// proof of a running preview; readers ask the worker.
	made := s.recordsMadeSoFar()
	if err := s.worker.Remove(ctx, ""); err != nil {
		log.Printf("removing the previews on the worker from before this start: %v", err)
		return false
	}
	c.pending = false
	s.forgetRecordsMadeBy(made)
	return true
}

func (s *Service) startClearPending() bool {
	s.startClear.mu.Lock()
	defer s.startClear.mu.Unlock()
	return s.startClear.pending
}

// wordChangeBootError words a change preview's boot failure for the person
// who asked, by phase, wherever it booted.
func wordChangeBootError(err error) error {
	be := docker.AsBootError(err)
	switch be.Phase {
	case docker.PhaseNeeds:
		return &docker.BootError{Phase: be.Phase, Err: fmt.Errorf("starting this version's services: %v", be.Err)}
	case docker.PhaseParts:
		return &docker.BootError{Phase: be.Phase, Err: fmt.Errorf("bringing up the app: %v", be.Err)}
	case docker.PhaseApp:
		return &docker.BootError{Phase: be.Phase, Err: fmt.Errorf("starting the preview: %v", be.Err)}
	case docker.PhaseProbe:
		return &docker.BootError{Phase: be.Phase, LogTail: be.LogTail, Err: fmt.Errorf("the app didn't come up: %s", strs.Truncate(be.LogTail, 300))}
	}
	return err
}

// admit asks the daemon a preview will run on whether it can give each
// part and need the resources it declares (docker.AdmitResources): the
// worker's when previews boot there, else this machine's. Asked before a
// preview's keys are registered or the one running is removed, so a
// refusal leaves everything as it was.
func (s *Service) admit(ctx context.Context, c *appcontract.Contract) error {
	if s.PreviewsOnWorker() {
		err := s.worker.Admit(ctx, c)
		var refused *worker.AdmissionRefused
		if err != nil && !errors.As(err, &refused) {
			return &UnavailableError{Cause: err}
		}
		return err
	}
	return docker.AdmitResources(ctx, s.hosts.Local(), c)
}
