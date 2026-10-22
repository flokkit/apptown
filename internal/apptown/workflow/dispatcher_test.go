package workflow

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// These tests exercise execution/recovery without App, SQL, or real runners.
type dispatchTestRecords struct {
	mu       sync.Mutex
	p        DispatchPlan
	states   map[ids.ID]dispatchState
	calls    []ids.ID
	resumes  []ids.ID
	advances int
	errors   []error
	readErr  error
}

func (s *dispatchTestRecords) State(_ context.Context, _, id ids.ID) (dispatchState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.states[id], s.readErr
}
func (s *dispatchTestRecords) Skip(_ context.Context, _ ids.ID, id ids.ID, _ string) error {
	s.set(id, StatusSkipped)
	return nil
}
func (s *dispatchTestRecords) Resume(_ context.Context, _ ids.ID, id ids.ID) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resumes = append(s.resumes, id)
	s.states[id] = dispatchState{Status: StatusPending, Active: true}
	return true, nil
}
func (s *dispatchTestRecords) RestartCandidates(context.Context) ([]ids.ID, error) {
	return []ids.ID{s.p.RunID}, nil
}
func (s *dispatchTestRecords) RunOf(context.Context, ids.ID) (ids.ID, error)      { return s.p.RunID, nil }
func (s *dispatchTestRecords) Plan(context.Context, ids.ID) (DispatchPlan, error) { return s.p, nil }
func (s *dispatchTestRecords) set(id ids.ID, status StepState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.states[id] = dispatchState{Status: status, Active: true}
}
func (s *dispatchTestRecords) ran(id ids.ID, status StepState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, id)
	s.states[id] = dispatchState{Status: status, Active: true}
}

func testDispatcher(steps []ReadyStep) (*dispatcher, *dispatchTestRecords) {
	s := &dispatchTestRecords{p: DispatchPlan{RunID: idstest.N(7), Steps: steps}, states: map[ids.ID]dispatchState{}}
	d := &dispatcher{
		records: s, runners: map[string]func(ids.ID){},
		advance: func(ids.ID) { s.mu.Lock(); s.advances++; s.mu.Unlock() },
		note:    func(ids.ID, string, dispatchNotice) error { return nil },
		report:  func(err error) { s.mu.Lock(); s.errors = append(s.errors, err); s.mu.Unlock() },
	}
	for _, step := range steps {
		s.set(step.ID, StatusPending)
		d.runners[step.Type] = func(id ids.ID) { s.ran(id, StatusPass) }
	}
	return d, s
}

func TestWorkflowDispatchRecoveryPreservesEntryFailures(t *testing.T) {
	t.Parallel()
	for _, recovery := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh", true: "recovered"}[recovery], func(t *testing.T) {
			steps := []ReadyStep{{idstest.N(1), "script", true, "prepare"}, {idstest.N(2), "scan", true, "image:scanner"}, {idstest.N(3), "deploy", true, "deploy"}, {idstest.N(4), "tests", false, "independent"}}
			d, s := testDispatcher(steps)
			d.runners["scan"] = func(id ids.ID) { s.ran(id, StatusFail) }
			wantCalls := []ids.ID{idstest.N(1), idstest.N(2), idstest.N(4)}
			if recovery {
				s.set(idstest.N(1), StatusPass)
				s.set(idstest.N(2), StatusFail)
				wantCalls = []ids.ID{idstest.N(4)}
				d.RestartPending(nil)
			} else {
				d.Dispatch(s.p, false)
			}
			d.Wait()
			if !reflect.DeepEqual(s.calls, wantCalls) {
				t.Fatalf("runners = %v, want %v", s.calls, wantCalls)
			}
			if s.states[idstest.N(3)].Status != StatusSkipped || s.advances != 1 || len(s.errors) != 0 {
				t.Fatalf("deploy=%+v, advances=%d, errors=%v", s.states[idstest.N(3)], s.advances, s.errors)
			}
		})
	}
}

func TestWorkflowDispatchRecoveryContinuesOrphansOnly(t *testing.T) {
	t.Parallel()
	d, s := testDispatcher([]ReadyStep{{idstest.N(1), "script", true, "done"}, {idstest.N(2), "agent/pi", true, "agent"}, {idstest.N(3), "script", false, "check"}})
	s.set(idstest.N(1), StatusPass)
	s.states[idstest.N(2)] = dispatchState{Status: StatusError, Orphaned: true, Active: true}
	d.RestartPending(nil)
	d.Wait()
	if !reflect.DeepEqual(s.calls, []ids.ID{idstest.N(2), idstest.N(3)}) || !reflect.DeepEqual(s.resumes, []ids.ID{idstest.N(2)}) {
		t.Fatalf("runners=%v resumes=%v", s.calls, s.resumes)
	}
}

func TestWorkflowDispatchGroupsRecoveredGateScans(t *testing.T) {
	t.Parallel()
	d, s := testDispatcher([]ReadyStep{{idstest.N(1), "scan", false, "source:a"}, {idstest.N(2), "scan", false, "source:b"}})
	started := make(chan ids.ID, 2)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer func() { unblock(); d.Wait() }()
	d.runners["scan"] = func(id ids.ID) { started <- id; <-release; s.ran(id, StatusPass) }
	d.RestartPending(nil)
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("scans were serialized")
		}
	}
	unblock()
	d.Wait()
	if len(s.calls) != 2 || s.advances != 1 {
		t.Fatalf("runners=%v advances=%d", s.calls, s.advances)
	}
}

func TestWorkflowDispatchTracksRecoveryAndDeduplicatesActiveStage(t *testing.T) {
	t.Parallel()
	d, s := testDispatcher([]ReadyStep{{idstest.N(1), "script", true, "work"}})
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer func() { unblock(); d.Wait() }()
	d.runners["script"] = func(id ids.ID) { close(started); <-release; s.ran(id, StatusPass) }
	d.RestartPending(nil)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("recovery did not start")
	}
	if d.Dispatch(s.p, false) {
		t.Fatal("same active stage accepted twice")
	}
	d.RestartPending(nil)
	waited := make(chan struct{})
	go func() { d.Wait(); close(waited) }()
	select {
	case <-waited:
		t.Fatal("wait ignored recovered work")
	default:
	}
	unblock()
	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		t.Fatal("wait did not finish")
	}
	if !reflect.DeepEqual(s.calls, []ids.ID{idstest.N(1)}) {
		t.Fatalf("runners=%v", s.calls)
	}
}

func TestWorkflowDispatchStopDrainsRunningAndLeavesQueuedSteps(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		d, s := testDispatcher([]ReadyStep{{idstest.N(1), "script", true, "first"}, {idstest.N(2), "script", true, "next"}})
		started, release := make(chan struct{}), make(chan struct{})
		var once sync.Once
		unblock := func() { once.Do(func() { close(release) }) }
		defer func() { unblock(); d.Wait() }()
		d.runners["script"] = func(id ids.ID) { close(started); <-release; s.ran(id, StatusPass) }
		d.Dispatch(s.p, false)
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("runner did not start")
		}
		stopped := make(chan struct{})
		go func() { d.Stop(); close(stopped) }()
		// Once every goroutine in the bubble is blocked, Stop has closed
		// admission and waits on the first runner, still blocked.
		synctest.Wait()
		if !d.isStopped() {
			t.Fatal("stop did not close admission")
		}
		select {
		case <-stopped:
			t.Fatal("stop returned before runner finished")
		default:
		}
		unblock()
		select {
		case <-stopped:
		case <-time.After(5 * time.Second):
			t.Fatal("stop did not drain")
		}
		if s.states[idstest.N(2)].Status != StatusPending || s.advances != 0 || d.Dispatch(s.p, false) {
			t.Fatal("stopped dispatcher started more work")
		}
	})
}

func TestWorkflowDispatchStopsOnReadFailureOrInactiveStage(t *testing.T) {
	t.Parallel()
	for _, readFailure := range []bool{false, true} {
		d, s := testDispatcher([]ReadyStep{{idstest.N(1), "script", true, "first"}, {idstest.N(2), "deploy", true, "deploy"}})
		if readFailure {
			s.readErr = errors.New("database unavailable")
		} else {
			s.states[idstest.N(1)] = dispatchState{Status: StatusPending, Active: false}
		}
		d.Dispatch(s.p, false)
		d.Wait()
		if len(s.calls) != 0 || s.advances != 0 {
			t.Fatalf("runners=%v advances=%d", s.calls, s.advances)
		}
		if readFailure && len(s.errors) != 1 {
			t.Fatalf("missing read error: %v", s.errors)
		}
	}
}

func TestWorkflowDispatchWaitIncludesNextStage(t *testing.T) {
	t.Parallel()
	d, s := testDispatcher([]ReadyStep{{idstest.N(1), "first", false, "first"}})
	s.set(idstest.N(2), StatusPending)
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer func() { unblock(); d.Wait() }()
	d.runners["next"] = func(id ids.ID) { close(started); <-release; s.ran(id, StatusPass) }
	d.advance = func(runID ids.ID) {
		s.mu.Lock()
		s.advances++
		n := s.advances
		s.mu.Unlock()
		if n == 1 && !d.Dispatch(DispatchPlan{RunID: runID, Steps: []ReadyStep{{idstest.N(2), "next", false, "next"}}}, false) {
			t.Error("next stage was rejected while its predecessor finished")
		}
	}
	d.Dispatch(s.p, false)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("next stage did not start")
	}
	waited := make(chan struct{})
	go func() { d.Wait(); close(waited) }()
	select {
	case <-waited:
		t.Fatal("wait returned with the next stage running")
	case <-time.After(20 * time.Millisecond):
	}
	unblock()
	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		t.Fatal("next stage did not drain")
	}
	if !reflect.DeepEqual(s.calls, []ids.ID{idstest.N(1), idstest.N(2)}) || s.advances != 2 {
		t.Fatalf("runners=%v advances=%d", s.calls, s.advances)
	}
}

func TestWorkflowDispatchRetainsReworkRequestedByActiveStage(t *testing.T) {
	t.Parallel()
	d, s := testDispatcher([]ReadyStep{{idstest.N(1), "agent/pi", true, "build"}, {idstest.N(2), "agent/review", false, "review"}})
	d.runners["agent/review"] = func(id ids.ID) {
		s.ran(id, StatusFail)
		s.set(idstest.N(1), StatusPending)
		plan := DispatchPlan{RunID: idstest.N(7), Steps: []ReadyStep{{idstest.N(1), "agent/pi", false, "build"}}}
		d.DispatchAgain(plan)
		d.DispatchAgain(plan)
	}
	d.Dispatch(s.p, false)
	d.Wait()
	if !reflect.DeepEqual(s.calls, []ids.ID{idstest.N(1), idstest.N(2), idstest.N(1)}) {
		t.Fatalf("lost or duplicated rework: %v", s.calls)
	}
}

// A scan point's scanners are grouped to run together; anything else, and
// an onEnter sequence, stays in order.
func TestScanPointGroup(t *testing.T) {
	t.Parallel()
	auto := []ReadyStep{
		{idstest.N(1), "reconcile", false, "catch-up-with-main"},
		{idstest.N(2), "tests", false, "tests"},
		{idstest.N(3), "scan", false, "scan-source:osv"},
		{idstest.N(4), "scan", false, "scan-source:gitleaks"},
		{idstest.N(5), "scan", false, "scan-source:semgrep"},
		{idstest.N(6), "guidance/conformance", false, "guidance-conformance"},
		{idstest.N(7), "scan", true, "scan-image:trivy"},
		{idstest.N(8), "deploy", true, "deploy"},
	}
	if g := scanPointGroup(auto, 0); g != nil {
		t.Fatalf("a reconcile is not a scan point: %v", g)
	}
	g := scanPointGroup(auto, 2)
	if len(g) != 3 || g[0].ID != idstest.N(3) || g[2].ID != idstest.N(5) {
		t.Fatalf("the three source scanners run together: %v", g)
	}
	if g := scanPointGroup(auto, 3); len(g) != 2 {
		t.Fatalf("from the middle, the rest of the point: %v", g)
	}
	if g := scanPointGroup(auto, 6); g != nil {
		t.Fatalf("an onEnter scan keeps its place in the sequence: %v", g)
	}
}

func TestDispatcherFailedRestartNoticeStopsBeforeExecution(t *testing.T) {
	t.Parallel()
	q, run, step := dispatchFixture(t)
	completionExec(t, q.db, `ALTER TABLE run_events ADD CONSTRAINT reject_dispatch_notice CHECK (kind!='steps.redispatched')`)
	called := false
	var reported []error
	d := newDispatcher(q.db, dispatcherConfig{Life: context.Background(), Runners: map[string]func(ids.ID){"script": func(ids.ID) { called = true }}, Report: func(err error) { reported = append(reported, err) }})
	d.RestartPending(nil)
	d.Wait()
	if called || len(reported) != 1 {
		t.Fatalf("called=%v errors=%v", called, reported)
	}
	wantStepState(t, q.db, step, StatusPending)
	wantRunEventCount(t, q.db, run.ID, 0, anyEvent)
}

func TestDispatcherFreezesTheRunnerRegistry(t *testing.T) {
	t.Parallel()
	q, run, step := dispatchFixture(t)
	called := false
	runners := map[string]func(ids.ID){"script": func(id ids.ID) {
		called = true
		snapshot, err := readStep(context.Background(), q.db, id)
		if err != nil {
			t.Error(err)
			return
		}
		attempts := newStepAttempts(q.db, func(kind string) bool { return kind == "script" })
		claim, err := attempts.Start(context.Background(), snapshot, "script")
		if err != nil {
			t.Error(err)
			return
		}
		if err := attempts.Finish(context.Background(), claim, StepOutcome{Status: StatusPass, Evidence: []byte(`{}`)}); err != nil {
			t.Error(err)
		}
	}}
	d := newDispatcher(q.db, dispatcherConfig{Life: context.Background(), Runners: runners, Report: func(err error) { t.Error(err) }})
	runners["script"] = func(ids.ID) { t.Error("caller changed an admitted runner") }
	delete(runners, "script")
	d.Dispatch(DispatchPlan{RunID: run.ID, Steps: []ReadyStep{{ID: step, Type: "script", Name: "accept"}}}, false)
	d.Wait()
	if !called {
		t.Fatal("frozen runner did not execute")
	}
}

func TestDispatcherReportsPanicsAndDrainsWithoutAdvancing(t *testing.T) {
	t.Parallel()
	for _, steps := range [][]ReadyStep{
		{{idstest.N(1), "script", true, "entry"}, {idstest.N(2), "script", true, "next"}},
		{{idstest.N(1), "scan", false, "point:a"}, {idstest.N(2), "scan", false, "point:b"}},
	} {
		d, s := testDispatcher(steps)
		for typ := range d.runners {
			d.runners[typ] = func(ids.ID) { panic("runner broke") }
		}
		d.Dispatch(s.p, false)
		d.Wait()
		d.Stop()
		if s.advances != 0 || len(s.errors) == 0 {
			t.Fatalf("advances=%d errors=%v", s.advances, s.errors)
		}
		for _, err := range s.errors {
			var p *DispatchPanic
			if !errors.As(err, &p) || p.Message != "runner broke" || len(p.Stack) == 0 {
				t.Fatalf("panic diagnostics lost: %v", err)
			}
		}
	}
}
