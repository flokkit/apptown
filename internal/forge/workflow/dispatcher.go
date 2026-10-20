package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/database"
)

type dispatchRecords interface {
	State(context.Context, ids.ID, ids.ID) (DispatchState, error)
	Skip(context.Context, ids.ID, ids.ID, string) error
	Resume(context.Context, ids.ID, ids.ID) (bool, error)
	RestartCandidates(context.Context) ([]ids.ID, error)
	RunOf(context.Context, ids.ID) (ids.ID, error)
	Plan(context.Context, ids.ID) (DispatchPlan, error)
}

// dispatcher owns stage execution and recovery tasks. Runners are
// bound operations; records are private to Work. Each stage
// entry is identified by its first automatic step, allowing a completed stage
// to dispatch the next one before its own goroutine returns.
type dispatcher struct {
	life    context.Context
	records dispatchRecords
	runners map[string]func(ids.ID)
	recover map[string]func(ids.ID)
	prepare func(ids.ID) error
	advance func(ids.ID)
	note    func(ids.ID, string, dispatchNotice) error
	refused func(ids.ID) bool // the run's latest event is a system refusal (repeat_notices.go)
	report  func(error)
	mu      sync.Mutex
	active  map[ids.ID]bool
	again   map[ids.ID]DispatchPlan
	adopt   map[ids.ID]bool // running steps to recover in place (RestartPending)
	stopped bool
	// shuttingDown: AppTown itself is stopping (BeginShutdown), not only
	// this dispatcher's admission (a test's Stop).
	shuttingDown bool
	stopDone     chan struct{}
	tasks        sync.WaitGroup
}

func (d *dispatcher) Dispatch(plan DispatchPlan, recovering bool) bool {
	return d.admit(plan, recovering, false)
}

// A running stage can request rework of its own first step. Coalesce that
// request and admit it after the current task returns instead of dropping it.
// The pending step in the database survives a stop or process restart.
func (d *dispatcher) DispatchAgain(plan DispatchPlan) bool {
	return d.admit(plan, false, true)
}

func (d *dispatcher) admit(plan DispatchPlan, recovering, again bool) bool {
	if len(plan.Steps) == 0 && !plan.NeedsPreparation && !plan.AdvanceEmpty {
		return false
	}
	// Run and step ids never collide: a run waiting on no step is keyed by itself.
	key := plan.RunID
	if len(plan.Steps) > 0 {
		key = plan.Steps[0].ID
	}
	d.mu.Lock()
	if d.stopped {
		d.mu.Unlock()
		return false
	}
	if d.active[key] {
		if again {
			if d.again == nil {
				d.again = make(map[ids.ID]DispatchPlan)
			}
			plan.Steps = append([]ReadyStep(nil), plan.Steps...)
			d.again[key] = plan
		}
		d.mu.Unlock()
		return false
	}
	if d.active == nil {
		d.active = make(map[ids.ID]bool)
	}
	d.active[key] = true
	d.tasks.Add(1)
	d.mu.Unlock()
	plan.Steps = append([]ReadyStep(nil), plan.Steps...)
	go func() {
		defer d.tasks.Done()
		defer func() {
			d.mu.Lock()
			delete(d.active, key)
			next, ok := d.again[key]
			delete(d.again, key)
			d.mu.Unlock()
			if ok {
				d.DispatchAgain(next)
			}
		}()
		defer d.reportPanic("workflow dispatch")
		if d.prepare != nil {
			if err := d.prepare(plan.RunID); err != nil {
				d.report(fmt.Errorf("run %d: preparing workspace: %w", plan.RunID, err))
				return
			}
		}
		if recovering {
			if plan.Pending > 0 {
				if err := d.note(plan.RunID, "steps.redispatched", dispatchNotice{Count: plan.Pending, Reason: "pending after restart"}); err != nil {
					d.report(err)
					return
				}
			} else if d.refused == nil || !d.refused(plan.RunID) {
				if err := d.note(plan.RunID, "stage.recovered", dispatchNotice{Reason: "checks finished before advancement"}); err != nil {
					d.report(err)
					return
				}
			}
		}
		if d.execute(plan) && (len(plan.Steps) > 0 || plan.AdvanceEmpty) && !d.isStopped() {
			d.advance(plan.RunID)
		}
	}()
	return true
}

func (d *dispatcher) RestartPending(adopt []ids.ID) {
	runs, err := d.records.RestartCandidates(d.life)
	if err != nil {
		d.report(fmt.Errorf("recovering workflow dispatch: %w", err))
		return
	}
	d.mu.Lock()
	if d.adopt == nil {
		d.adopt = map[ids.ID]bool{}
	}
	for _, id := range adopt {
		d.adopt[id] = true
	}
	d.mu.Unlock()
	for _, stepID := range adopt {
		runID, err := d.records.RunOf(d.life, stepID)
		if err != nil {
			d.report(fmt.Errorf("step %d: recovering its recorded work: %w", stepID, err))
			continue
		}
		if !slices.Contains(runs, runID) {
			runs = append(runs, runID)
		}
	}
	for _, id := range runs {
		if d.isStopped() {
			return
		}
		plan, err := d.records.Plan(d.life, id)
		if err != nil {
			d.report(fmt.Errorf("run %d: recovering stage: %w", id, err))
			continue
		}
		d.Dispatch(plan, true)
	}
}

// wait drains accepted tasks, including any next stages they dispatch. stop
// also closes admission and leaves unstarted steps pending for recovery. A
// runner already in progress finishes under its existing timeout/cancellation
// policy; this owner does not claim to cancel every kind of external work.
func (d *dispatcher) Wait() { d.tasks.Wait() }
func (d *dispatcher) Stop() {
	<-d.BeginStop()
}

// Close admission synchronously; joining already-running stages is separate so
// the daemon can share one deadline with HTTP and its other workers.
func (d *dispatcher) BeginStop() <-chan struct{} {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopDone != nil {
		return d.stopDone
	}
	d.stopped = true
	d.stopDone = make(chan struct{})
	go func() {
		d.Wait()
		close(d.stopDone)
	}()
	return d.stopDone
}

// beginShutdown is BeginStop because AppTown is stopping: from now on a
// step's verdict other than a pass is not recorded (Execution.Settle).
func (d *dispatcher) beginShutdown() <-chan struct{} {
	d.mu.Lock()
	d.shuttingDown = true
	d.mu.Unlock()
	return d.BeginStop()
}

func (d *dispatcher) isShuttingDown() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.shuttingDown
}

func (d *dispatcher) isStopped() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.stopped
}

func (d *dispatcher) execute(plan DispatchPlan) bool {
	halted := ""
	for i := 0; i < len(plan.Steps); i++ {
		if d.isStopped() {
			return false
		}
		step := plan.Steps[i]
		if group := scanPointGroup(plan.Steps, i); len(group) > 1 {
			var wg sync.WaitGroup
			ok := make([]bool, len(group))
			for j, step := range group {
				wg.Add(1)
				go func() {
					defer wg.Done()
					defer d.reportPanic("workflow scan dispatch")
					_, ok[j] = d.executeStep(plan.RunID, step, "")
				}()
			}
			wg.Wait()
			for _, good := range ok {
				if !good {
					return false
				}
			}
			i += len(group) - 1
			continue
		}
		failed, ok := d.executeStep(plan.RunID, step, halted)
		if !ok {
			return false
		}
		if step.OnEnter && failed && halted == "" {
			halted = step.Name
		}
	}
	return true
}

// Completed steps are observed, never replayed. That observation is crucial
// after restart: an earlier failure still halts entry steps, while independent
// gate checks continue. Read/write errors stop dispatch rather than guessing.
func (d *dispatcher) executeStep(runID ids.ID, step ReadyStep, halted string) (failed, ok bool) {
	if d.isStopped() {
		return false, false
	}
	state, err := d.records.State(d.life, runID, step.ID)
	if err != nil {
		d.report(fmt.Errorf("run %d step %d: reading dispatch state: %w", runID, step.ID, err))
		return false, false
	}
	if !state.Active {
		return false, false
	}
	if step.OnEnter && halted != "" {
		if state.Runnable() {
			if err := d.records.Skip(d.life, runID, step.ID, halted); err != nil {
				d.report(fmt.Errorf("run %d step %d: recording skip: %w", runID, step.ID, err))
				return false, false
			}
		}
		return false, true
	}
	if state.Status == StatusRunning {
		// A running step is another process's or this one's work in
		// progress. Only one a restart left with recorded work is continued
		// here, in its place; otherwise the stage waits for it rather than
		// running the steps after it.
		recover, adopted := d.recover[step.Type], d.takeAdopted(step.ID)
		if !adopted || recover == nil {
			return false, false
		}
		recover(step.ID)
		state, err = d.records.State(d.life, runID, step.ID)
		if err != nil {
			d.report(fmt.Errorf("run %d step %d: reading outcome: %w", runID, step.ID, err))
			return false, false
		}
		if !state.Active || state.Status == StatusRunning {
			return false, false
		}
		return state.Status == StatusFail || state.Status == StatusError, true
	}
	if state.Runnable() {
		if state.Orphaned {
			resumed, err := d.records.Resume(d.life, runID, step.ID)
			if err != nil {
				d.report(fmt.Errorf("run %d step %d: preparing resume: %w", runID, step.ID, err))
				return false, false
			}
			if !resumed {
				// Another writer changed this step.
				return false, false
			}
		}
		runner, exists := d.runners[step.Type]
		if !exists {
			d.report(fmt.Errorf("run %d step %d: no runner for %s", runID, step.ID, step.Type))
			return false, false
		}
		runner(step.ID)
		state, err = d.records.State(d.life, runID, step.ID)
		if err != nil {
			d.report(fmt.Errorf("run %d step %d: reading outcome: %w", runID, step.ID, err))
			return false, false
		}
		if !state.Active || state.Status == StatusRunning {
			// A runner that returns with its step still running left work
			// without a recorded outcome (a task whose result could not be
			// recorded yet): nothing after it runs on that basis.
			return false, false
		}
	}
	return state.Status == StatusFail || state.Status == StatusError, true
}

// takeAdopted reports whether step was left running with recorded work by a
// previous process, and forgets it: it is continued once.
func (d *dispatcher) takeAdopted(step ids.ID) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	adopted := d.adopt[step]
	delete(d.adopt, step)
	return adopted
}

// Scanners expanded from the same gate point can run together. Entry steps
// always retain their declared order, even when they are scanners.
func scanPointGroup(auto []ReadyStep, i int) []ReadyStep {
	point := func(s ReadyStep) string {
		if s.Type != "scan" || s.OnEnter {
			return ""
		}
		if k := strings.Index(s.Name, ":"); k > 0 {
			return s.Name[:k]
		}
		return ""
	}
	p := point(auto[i])
	if p == "" {
		return nil
	}
	var out []ReadyStep
	for j := i; j < len(auto) && point(auto[j]) == p; j++ {
		out = append(out, auto[j])
	}
	return out
}

// dispatchStageHooks supplies the external workspace and post-runner stage effect.
// Prepare retains the Starter's bounded preparation policy. Advance may dispatch
// a next stage before returning; Wait includes that accepted work.
type dispatchStageHooks interface {
	Prepare(ids.ID) error
	Advance(ids.ID)
}

type dispatcherConfig struct {
	Life       context.Context // the dispatcher's lifetime: its reads stop with it
	Runners    map[string]func(ids.ID)
	Recoverers map[string]func(ids.ID)
	Stages     dispatchStageHooks
	Report     func(error)
	Now        func() time.Time // AppTown's clock; nil is the real one
}

// newDispatcher owns its records and freezes the runner registry. Plans describe
// already-admitted Work; every execution still checks the current stored Step.
func newDispatcher(db *database.DB, runtime dispatcherConfig) *dispatcher {
	runners := maps.Clone(runtime.Runners)
	records := newDispatchPlans(db, slices.Sorted(maps.Keys(runners)))
	records.clock = runtime.Now
	d := &dispatcher{life: runtime.Life, records: records, runners: runners, recover: maps.Clone(runtime.Recoverers), report: runtime.Report}
	if d.report == nil {
		d.report = func(error) {}
	}
	if runtime.Stages != nil {
		d.prepare = runtime.Stages.Prepare
		d.advance = runtime.Stages.Advance
	}
	if d.advance == nil {
		d.advance = func(ids.ID) {}
	}
	d.refused = func(id ids.ID) bool {
		refused, err := lastRefusedBySystem(runtime.Life, db, id)
		return err == nil && refused
	}
	d.note = func(id ids.ID, kind string, payload dispatchNotice) error {
		raw, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		return RecordNotice(context.WithoutCancel(runtime.Life), db, Notice{RunID: id, Kind: kind, Payload: raw, At: clock.Or(runtime.Now)()})
	}
	return d
}

type dispatchNotice struct {
	Count  int    `json:"count,omitempty"`
	Reason string `json:"reason"`
}

func (d *dispatcher) reportPanic(where string) {
	if value := recover(); value != nil {
		d.report(&DispatchPanic{Where: where, Message: fmt.Sprint(value), Stack: debug.Stack()})
	}
}

// DispatchPanic preserves diagnostics without exposing an untyped panic value.
// The assembly adapter may forward it to the host's existing incident reporter.
type DispatchPanic struct {
	Where   string
	Message string
	Stack   []byte
}

func (p *DispatchPanic) Error() string {
	return fmt.Sprintf("%s panic: %s\n%s", p.Where, p.Message, p.Stack)
}
