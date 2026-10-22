package workflow

import (
	"errors"
	"fmt"

	"github.com/flokkit/apptown/internal/ids"
)

var ErrDispatchStopped = errors.New("workflow dispatcher is stopping; run was not handed off")

// DispatchStarted hands a freshly started run's first stage to the dispatcher.
func (s *Service) DispatchStarted(runID ids.ID) error {
	plan, err := s.dispatchPlans.Plan(s.life, runID)
	if err != nil {
		return err
	}
	// False also means an empty plan or a coalesced stage. Only shutdown is
	// a failed handoff; do not misreport it as successful run admission.
	if !s.dispatcher.Dispatch(plan, false) && s.dispatcher.isStopped() {
		return ErrDispatchStopped
	}
	return nil
}

// Dispatch hands a stage's ready steps to the runners; false when the
// dispatcher is stopping. Rework dispatch is private to its committing command.
func (s *Service) Dispatch(plan DispatchPlan) bool { return s.dispatcher.Dispatch(plan, false) }

// ResumePending restarts the automatic steps a previous process left
// pending; the daemon calls it once after recovery. adopt names running
// steps whose recorded work a previous process left (a task still on its
// backend): each is continued by its kind's recoverer in its place in the
// stage, before the steps after it, and its run continues when it settles.
func (s *Service) ResumePending(adopt ...ids.ID) { s.dispatcher.RestartPending(adopt) }

// Wait blocks until every dispatched step has finished. Stop refuses new
// dispatch and waits; BeginStop refuses new dispatch and returns the
// channel that closes when the running ones finish.
func (s *Service) Wait() { s.dispatcher.Wait() }

func (s *Service) Stop() { s.dispatcher.Stop() }

func (s *Service) BeginStop() <-chan struct{} { return s.dispatcher.BeginStop() }

// BeginShutdown is BeginStop when AppTown itself is stopping. From then on
// a step whose runner settles anything but a pass is left running for
// startup repair (Execution.Settle): the processes a stopping AppTown's steps
// run are killed with it, so a failure seen now is not evidence about the
// work.
func (s *Service) BeginShutdown() <-chan struct{} { return s.dispatcher.beginShutdown() }

// autoAdvance is the engine's own mechanics, not a door: when a stage's
// runners finish, the run attempts its gate as the system and dispatches
// what the next stage readies. One attempt, then stop: a new stage's
// runners call it again when they finish.
func (s *Service) autoAdvance(runID ids.ID) {
	if s.dispatcher.isStopped() {
		return
	}
	run, err := readRun(s.life, s.db, runID)
	if err != nil {
		s.dispatcher.report(fmt.Errorf("run %d: automatic advancement: %w", runID, err))
		return
	}
	s.advanceCommitted(RunRef{ID: run.ID, ProjectID: run.ProjectID})
}

// The decision already committed under its own authority. Automatic progression
// is engine work, not another permission demanded from the deciding person.
func (s *Service) advanceCommitted(ref RunRef) {
	if s.dispatcher.isStopped() {
		return
	}
	ctx := s.life
	run, err := readScopedRun(ctx, s.db, ref.ID, ref.ProjectID)
	if err == nil && run.Status != RunRunning {
		return
	}
	if err == nil {
		_, err = s.advancePrepared(ctx, run, "system")
	}
	if err != nil {
		s.dispatcher.report(fmt.Errorf("run %d: committed work needs advancement: %w", ref.ID, err))
	}
}

func (s *Service) dispatchAdvanced(runID ids.ID, out *AdvanceResult, err error) {
	if err == nil && out != nil && out.Advanced && out.RunStatus != RunDone && s.dispatcher != nil {
		s.dispatcher.Dispatch(DispatchPlan{RunID: runID, Steps: out.Ready}, false)
	}
}

// dispatchStages is the dispatcher's view of the rest of Work: prepare a
// run's workspace before its first runner, advance the run after one.
type dispatchStages struct{ s *Service }

func (d dispatchStages) Prepare(id ids.ID) error { return d.s.starter.Prepare(d.s.life, id) }

func (d dispatchStages) Advance(id ids.ID) { d.s.autoAdvance(id) }
