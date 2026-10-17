package workflow

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/schema"
)

type runStartRecords interface {
	existing(context.Context, StartRequest) (*Run, error)
	create(context.Context, StartRequest, PreparedRun) (*Run, error)
	pendingPreparation(context.Context, ids.ID) (*Run, error)
	prepared(context.Context, ids.ID) error
	preparationFailed(context.Context, ids.ID, string) error
}

// starter owns creation and the ordering of post-commit work. It has no
// App or SQL access. Failed preparation leaves a durable, unready start;
// dispatch/recovery and explicit advancement use the same prepare operation.
type startRuntime struct {
	Source    func(context.Context, StartRequest) (StartSource, error)    // what the request starts from: its change, adoption, superseded runs
	Supersede func(context.Context, RunRef, string) error                 // a failed landing the new run replaces
	Missing   func(context.Context, ids.ID, *schema.Spec) (string, error) // an artifact the workflow needs and the project lacks
	Resolve   func(context.Context, StartRequest, StartSource) (PreparedRun, error)
	Assent    func(context.Context, StartRequest, string) error // landing authority for an effect no approval precedes (assent.go)
	LandingBy func(context.Context, StartRequest) string        // who admits the run holding change:land, "" when nobody
	// AcceptedEvalAuthority: the eval's authority a landing of accepted work
	// in an eval's copy carries, "" for any other start (eval_authority.go).
	AcceptedEvalAuthority func(context.Context, StartRequest) (string, error)
	Prepare               func(context.Context, *Run) error
	Dispatch              func(context.Context, *Run) error
}
type starter struct {
	records   runStartRecords
	runtime   startRuntime
	mu        sync.Mutex
	preparing map[ids.ID]chan struct{}
}

func newStarter(db *database.DB, stages Stages, chats Presence, runtime startRuntime, at clocked) *starter {
	return &starter{records: runStartStore{db, stages, chats, at}, runtime: runtime}
}

func (s *starter) Start(ctx context.Context, req StartRequest) (*Run, error) {
	if req.InputsJSON == "" {
		req.InputsJSON = "{}"
	}
	if err := validateStart(req); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	run, err := s.records.existing(ctx, req)
	if err != nil {
		return nil, err
	}
	if run == nil {
		if s.runtime.Resolve == nil {
			return nil, fmt.Errorf("Run definition resolver is unavailable")
		}
		var source StartSource
		if s.runtime.Source != nil {
			var err error
			if source, err = s.runtime.Source(ctx, req); err != nil {
				return nil, err
			}
		}
		for _, failed := range source.Superseded {
			if s.runtime.Supersede == nil {
				return nil, fmt.Errorf("run %d is a failed landing this start would replace, and nothing can supersede it", failed.ID)
			}
			if err := s.runtime.Supersede(ctx, failed, req.WorkflowName); err != nil {
				return nil, err
			}
		}
		def, err := s.definition(ctx, req, source)
		if err != nil {
			return nil, err
		}
		if req.evalAuthority == "" && !req.AcceptedRunID.IsZero() {
			// A landing of an eval copy's accepted work carries the eval's
			// authority its work run was admitted with.
			if s.runtime.AcceptedEvalAuthority == nil {
				return nil, fmt.Errorf("Run eval authority is unavailable")
			}
			if req.evalAuthority, err = s.runtime.AcceptedEvalAuthority(ctx, req); err != nil {
				return nil, err
			}
		}
		switch {
		case req.evalAuthority != "":
			req.landingBy = req.evalAuthority
		case s.runtime.LandingBy != nil:
			req.landingBy = s.runtime.LandingBy(ctx, req)
		}
		run, err = s.records.create(ctx, req, def)
		if err != nil {
			return nil, err
		}
	}
	if run.Status != RunRunning {
		return run, nil
	}
	if err := s.Prepare(ctx, run.ID); err != nil {
		return run, fmt.Errorf("run %d was created but its workspace is not ready: %w", run.ID, err)
	}
	return run, s.dispatchPrepared(ctx, run)
}

// definition shares validation between existing-change starts and atomic creation.
// It resolves outside the transaction; admission rechecks the adoption pin.
func (s *starter) definition(ctx context.Context, req StartRequest, source StartSource) (PreparedRun, error) {
	if s.runtime.Resolve == nil {
		return PreparedRun{}, fmt.Errorf("Run definition resolver is unavailable")
	}
	def, err := s.runtime.Resolve(ctx, req, source)
	if err != nil {
		return PreparedRun{}, err
	}
	if len(def.Spec.Stages) == 0 || def.Spec.Stages[0].Name == "" {
		return PreparedRun{}, fmt.Errorf("workflow %q has no initial stage", req.WorkflowName)
	}
	if req.ParentRunID.IsZero() && s.runtime.Missing != nil {
		missing, err := s.runtime.Missing(ctx, req.ProjectID, &def.Spec)
		if err != nil {
			return PreparedRun{}, err
		}
		if missing != "" {
			return PreparedRun{}, fmt.Errorf("workflow %q needs a %q artifact and this project has none yet — accept one first (product-spec produces spec-final), then invoke again", req.WorkflowName, missing)
		}
	}
	if !req.AcceptedRunID.IsZero() && (!req.ParentRunID.IsZero() || def.Spec.Role != "landing") {
		return PreparedRun{}, fmt.Errorf("accepted work must start a root landing")
	}
	// Accepted landings carry a person's recorded acceptance; a child run
	// was judged as part of its root.
	// An eval's run carries the eval's authority, judged at each effect
	// against its copy (eval_authority.go).
	if req.ParentRunID.IsZero() && req.AcceptedRunID.IsZero() && req.evalAuthority == "" {
		effect, err := unassentedEffect(&def.Spec, false, func(name string) (*schema.Spec, error) {
			called := req
			called.WorkflowName = name
			def, err := s.runtime.Resolve(ctx, called, source)
			return &def.Spec, err
		}, 0)
		if err != nil {
			return PreparedRun{}, err
		}
		if effect != "" {
			if s.runtime.Assent == nil {
				return PreparedRun{}, fmt.Errorf("workflow %q would %s with no approval first, and landing authority cannot be checked", req.WorkflowName, effect)
			}
			if err := s.runtime.Assent(ctx, req, effect); err != nil {
				return PreparedRun{}, err
			}
		}
	}
	return def, nil
}

// Readiness commits the handoff obligation. Caller cancellation no longer
// owns this daemon-local act; dispatch can still fail if the daemon stops.
func (s *starter) dispatchPrepared(ctx context.Context, run *Run) error {
	if s.runtime.Dispatch == nil {
		return fmt.Errorf("Run dispatch is unavailable")
	}
	if err := s.runtime.Dispatch(ctx, run); err != nil {
		return fmt.Errorf("run %d is ready but dispatch needs retry: %w", run.ID, err)
	}
	return nil
}

// Coalesce preparation of the same run without serializing unrelated runs.
// Entries live only for the duration of preparation, not the run's lifetime.
func (s *starter) Prepare(ctx context.Context, id ids.ID) error {
	return s.prepareExpected(ctx, id, nil)
}

// PrepareScoped binds a caller-authorized Run before any workspace effect.
func (s *starter) PrepareScoped(ctx context.Context, expected *Run) error {
	if expected == nil || expected.ID.IsZero() || expected.ProjectID.IsZero() {
		return ErrRunMoved
	}
	snapshot := *expected
	return s.prepareExpected(ctx, snapshot.ID, &snapshot)
}
func (s *starter) prepareExpected(ctx context.Context, id ids.ID, expected *Run) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	for s.preparing[id] != nil {
		done := s.preparing[id]
		s.mu.Unlock()
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
		s.mu.Lock()
	}
	if s.preparing == nil {
		s.preparing = map[ids.ID]chan struct{}{}
	}
	done := make(chan struct{})
	s.preparing[id] = done
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.preparing, id); close(done); s.mu.Unlock() }()
	run, err := s.records.pendingPreparation(ctx, id)
	if err != nil || run == nil {
		return err
	}
	if expected != nil && (run.ID != expected.ID || run.ProjectID != expected.ProjectID || run.ChangeID != expected.ChangeID || run.ParentRunID != expected.ParentRunID || run.DefinitionJSON != expected.DefinitionJSON) {
		return ErrRunMoved
	}
	if run.ParentRunID.IsZero() {
		if s.runtime.Prepare == nil {
			return fmt.Errorf("Run workspace preparation is unavailable")
		}
		if err := s.runtime.Prepare(ctx, run); err != nil {
			receiptErr := s.records.preparationFailed(ctx, id, err.Error())
			if receiptErr != nil {
				return errors.Join(err, fmt.Errorf("record workspace failure: %w", receiptErr))
			}
			return err
		}
	}
	return s.records.prepared(ctx, id)
}
