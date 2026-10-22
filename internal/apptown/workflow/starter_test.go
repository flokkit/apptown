package workflow

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// The owner is testable without App, a database, or real execution.
type startTestRecords struct {
	trace  []string
	run    *Run
	replay bool
	ready  bool
	fail   string
}

func (s *startTestRecords) existing(context.Context, StartRequest) (*Run, error) {
	s.trace = append(s.trace, "lookup")
	if s.replay {
		return s.run, nil
	}
	return nil, nil
}
func (s *startTestRecords) create(context.Context, StartRequest, PreparedRun) (*Run, error) {
	s.trace = append(s.trace, "commit")
	if s.fail == "commit" {
		return nil, errors.New("commit failed")
	}
	return s.run, nil
}
func (s *startTestRecords) pendingPreparation(context.Context, ids.ID) (*Run, error) {
	s.trace = append(s.trace, "read readiness")
	if s.ready {
		return nil, nil
	}
	return s.run, nil
}
func (s *startTestRecords) prepared(context.Context, ids.ID) error {
	s.trace = append(s.trace, "ready")
	if s.fail == "ready" {
		return errors.New("ready failed")
	}
	s.ready = true
	return nil
}

func TestRunStarterOrdersCommitPreparationAndDispatch(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"", "commit", "workspace", "ready"} {
		t.Run(failure, func(t *testing.T) {
			records := &startTestRecords{run: &Run{ID: idstest.N(7), Status: RunRunning}, fail: failure}
			starter := &starter{records: records,
				runtime: startRuntime{Source: func(context.Context, StartRequest) (StartSource, error) { return StartSource{}, nil },
					Resolve: func(context.Context, StartRequest, StartSource) (PreparedRun, error) {
						records.trace = append(records.trace, "resolve")
						return PreparedRun{Spec: schema.Spec{Role: "working", Stages: []schema.Stage{{Name: "first"}}}}, nil
					},
					Prepare: func(context.Context, *Run) error {
						records.trace = append(records.trace, "workspace")
						if failure == "workspace" {
							return errors.New("workspace failed")
						}
						return nil
					},
					Dispatch: func(context.Context, *Run) error { records.trace = append(records.trace, "dispatch"); return nil }},
			}
			run, err := starter.Start(context.Background(), StartRequest{ProjectID: idstest.N(1), WorkflowName: "test"})
			if (err != nil) != (failure != "") {
				t.Fatalf("start: %+v %v", run, err)
			}
			want := []string{"lookup", "resolve", "commit"}
			if failure != "commit" {
				want = append(want, "read readiness", "workspace")
			}
			switch failure {
			case "workspace":
				want = append(want, "error receipt")
			case "ready":
				want = append(want, "ready")
			case "":
				want = append(want, "ready", "dispatch")
			}
			if !reflect.DeepEqual(records.trace, want) {
				t.Fatalf("ordering: %v, want %v", records.trace, want)
			}
			if failure != "commit" && run == nil {
				t.Fatal("a committed start must be returned even when preparation needs retry")
			}
		})
	}
}

func TestRunStarterCompletedReplayDoesNotResolveOrDispatch(t *testing.T) {
	t.Parallel()
	records := &startTestRecords{run: &Run{ID: idstest.N(7), Status: RunDone}, replay: true}
	starter := &starter{records: records}
	run, err := starter.Start(context.Background(), StartRequest{ProjectID: idstest.N(1), WorkflowName: "child", ParentRunID: idstest.N(1), ParentStepID: idstest.N(2)})
	if err != nil || run.ID != idstest.N(7) || !reflect.DeepEqual(records.trace, []string{"lookup"}) {
		t.Fatalf("replay: %+v %v %v", run, err, records.trace)
	}
}

func (s *startTestRecords) preparationFailed(context.Context, ids.ID, string) error {
	s.trace = append(s.trace, "error receipt")
	return nil
}

// A canceled waiter must neither inherit another caller's context nor stop its
// preparation. Different Runs must remain independent of this coalescing map.
func TestRunPreparationWaiterCancellationAndIndependentRuns(t *testing.T) {
	t.Parallel()
	admissions, req, def := runAdmissionFixture(t)
	db := admissions.records.db
	first, err := admissions.Create(context.Background(), req, def)
	if err != nil {
		t.Fatal(err)
	}
	req.ChangeID = admissionTestChange(t, db, req.ProjectID, "second")
	second, err := admissions.Create(context.Background(), req, def)
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	starter := newStarter(db, NewStages(nil, nil), nil, startRuntime{Prepare: func(ctx context.Context, run *Run) error {
		calls.Add(1)
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 10*time.Minute {
			return errors.New("unbounded preparation")
		}
		if run.ID == first.ID {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}}, clocked{})
	leader := make(chan error, 1)
	go func() { leader <- starter.Prepare(context.Background(), first.ID) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("preparation did not start")
	}
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	waiterCtx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if err := starter.Prepare(waiterCtx, first.ID); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiter: %v", err)
	}
	if err := starter.Prepare(context.Background(), second.ID); err != nil {
		t.Fatal(err)
	}
	wantReadyCount(t, db, true, 1)
	wantRunEventCount(t, db, first.ID, 0, ofKind("branch.error"))
	close(release)
	select {
	case err := <-leader:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("leader remained blocked")
	}
	if err := starter.Prepare(context.Background(), first.ID); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("prepared %d times", calls.Load())
	}
	wantEventCount(t, db, 2, ofKind("run.ready"))
	starter.mu.Lock()
	defer starter.mu.Unlock()
	if len(starter.preparing) != 0 {
		t.Fatal("preparation entries outlive their work")
	}
}

func TestRunPreparationCancellationLeavesExplicitRetry(t *testing.T) {
	t.Parallel()
	admissions, req, def := runAdmissionFixture(t)
	db := admissions.records.db
	run, err := admissions.Create(context.Background(), req, def)
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	starter := newStarter(db, NewStages(nil, nil), nil, startRuntime{Prepare: func(ctx context.Context, _ *Run) error {
		close(entered)
		<-ctx.Done()
		return ctx.Err()
	}}, clocked{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	go func() { finished <- starter.Prepare(ctx, run.ID) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("preparation did not start")
	}
	cancel()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation blocked")
	}
	wantReadyCount(t, db, false, 1)
	wantEventCount(t, db, 0, ofKind("run.ready"))
	retry := newStarter(db, NewStages(nil, nil), nil, startRuntime{Prepare: func(context.Context, *Run) error { return nil }}, clocked{})
	if err := retry.Prepare(context.Background(), run.ID); err != nil {
		t.Fatal(err)
	}
	wantEventCount(t, db, 1, ofKind("run.ready"))
}

func TestRunPreparationUsesItsAuthorizedScope(t *testing.T) {
	t.Parallel()
	for _, changed := range []string{"project", "change", "parent", "definition"} {
		t.Run(changed, func(t *testing.T) {
			t.Parallel()
			expected := Run{ID: idstest.N(7), ProjectID: idstest.N(1), ChangeID: idstest.N(2), DefinitionJSON: `{"stages":[]}`}
			current := expected
			switch changed {
			case "project":
				current.ProjectID = idstest.N(999999)
			case "change":
				current.ChangeID = idstest.N(999999)
			case "parent":
				current.ParentRunID = idstest.N(9)
			case "definition":
				current.DefinitionJSON = `{}`
			}
			records := &startTestRecords{run: &current}
			starter := &starter{records: records, runtime: startRuntime{Prepare: func(context.Context, *Run) error { t.Fatal("changed scope reached workspace preparation"); return nil }}}
			if err := starter.PrepareScoped(context.Background(), &expected); !errors.Is(err, ErrRunMoved) {
				t.Fatalf("scope %s: %v", changed, err)
			}
			if records.ready {
				t.Fatal("changed scope became ready")
			}
		})
	}
}
