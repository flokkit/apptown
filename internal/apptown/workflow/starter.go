package workflow

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
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
	source    func(context.Context, StartRequest) (StartSource, error)    // what the request starts from: its change, adoption, superseded runs
	supersede func(context.Context, RunRef, string) error                 // a failed landing the new run replaces
	missing   func(context.Context, ids.ID, *schema.Spec) (string, error) // an artifact the workflow needs and the project lacks
	// noModel says why a model step of the workflow would have no model
	// (Dependencies.ModelRefusal); nil admits every definition.
	noModel   func(ctx context.Context, projectID ids.ID, spec *schema.Spec, inputsJSON string) (string, error)
	resolve   func(context.Context, StartRequest, StartSource) (PreparedRun, error)
	assent    func(context.Context, StartRequest, string) error // landing authority for an effect no approval precedes (assent.go)
	landingBy func(context.Context, StartRequest) string        // who admits the run holding change:land, "" when nobody
	// acceptedEvalAuthority: the eval's authority a landing of accepted work
	// in an eval's copy carries, "" for any other start (eval_authority.go).
	acceptedEvalAuthority func(context.Context, StartRequest) (string, error)
	prepare               func(context.Context, *Run) error
	dispatch              func(context.Context, *Run) error
}
type starter struct {
	records   runStartRecords
	runtime   startRuntime
	mu        sync.Mutex
	preparing map[ids.ID]chan struct{}
}

func newStarter(db *database.DB, stages stages, chats Presence, runtime startRuntime, at clocked) *starter {
	return &starter{records: runStartStore{db, stages, chats, at}, runtime: runtime}
}

func (s *starter) start(ctx context.Context, req StartRequest) (*Run, error) {
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
		if s.runtime.resolve == nil {
			return nil, fmt.Errorf("Run definition resolver is unavailable")
		}
		var source StartSource
		if s.runtime.source != nil {
			var err error
			if source, err = s.runtime.source(ctx, req); err != nil {
				return nil, err
			}
		}
		for _, failed := range source.Superseded {
			if s.runtime.supersede == nil {
				return nil, fmt.Errorf("run %d is a failed landing this start would replace, and nothing can supersede it", failed.ID)
			}
			if err := s.runtime.supersede(ctx, failed, req.WorkflowName); err != nil {
				return nil, err
			}
		}
		def, err := s.definition(ctx, req, source)
		if err != nil {
			return nil, err
		}
		if req.evalAuthority == "" && !req.acceptedRunID.IsZero() {
			// A landing of an eval copy's accepted work carries the eval's
			// authority its work run was admitted with.
			if s.runtime.acceptedEvalAuthority == nil {
				return nil, fmt.Errorf("Run eval authority is unavailable")
			}
			if req.evalAuthority, err = s.runtime.acceptedEvalAuthority(ctx, req); err != nil {
				return nil, err
			}
		}
		switch {
		case req.evalAuthority != "":
			req.landingBy = req.evalAuthority
		case s.runtime.landingBy != nil:
			req.landingBy = s.runtime.landingBy(ctx, req)
		}
		run, err = s.records.create(ctx, req, def)
		if err != nil {
			return nil, err
		}
	}
	if run.Status != RunRunning {
		return run, nil
	}
	if err := s.prepare(ctx, run.ID); err != nil {
		return run, fmt.Errorf("run %d was created but its workspace is not ready: %w", run.ID, err)
	}
	return run, s.dispatchPrepared(ctx, run)
}

// definition shares validation between existing-change starts and atomic creation.
// It resolves outside the transaction; admission rechecks the adoption pin.
func (s *starter) definition(ctx context.Context, req StartRequest, source StartSource) (PreparedRun, error) {
	if s.runtime.resolve == nil {
		return PreparedRun{}, fmt.Errorf("Run definition resolver is unavailable")
	}
	def, err := s.runtime.resolve(ctx, req, source)
	if err != nil {
		return PreparedRun{}, err
	}
	if len(def.Spec.Stages) == 0 || def.Spec.Stages[0].Name == "" {
		return PreparedRun{}, fmt.Errorf("workflow %q has no initial stage", req.WorkflowName)
	}
	if req.parentRunID.IsZero() && s.runtime.missing != nil {
		missing, err := s.runtime.missing(ctx, req.ProjectID, &def.Spec)
		if err != nil {
			return PreparedRun{}, err
		}
		if missing != "" {
			return PreparedRun{}, fmt.Errorf("workflow %q needs a %q artifact and this project has none yet — accept one first (product-spec produces spec-final), then invoke again", req.WorkflowName, missing)
		}
	}
	// A called workflow is asked too: its model steps need a model as much
	// as its caller's do.
	if s.runtime.noModel != nil {
		why, err := s.runtime.noModel(ctx, req.ProjectID, &def.Spec, req.InputsJSON)
		if err != nil {
			return PreparedRun{}, err
		}
		if why != "" {
			return PreparedRun{}, fault.Wrap(fault.Unprocessable, errors.New(why))
		}
	}
	if !req.acceptedRunID.IsZero() && (!req.parentRunID.IsZero() || def.Spec.Role != "landing") {
		return PreparedRun{}, fmt.Errorf("accepted work must start a root landing")
	}
	// Accepted landings carry a person's recorded acceptance; a child run
	// was judged as part of its root.
	// An eval's run carries the eval's authority, judged at each effect
	// against its copy (eval_authority.go).
	if req.parentRunID.IsZero() && req.acceptedRunID.IsZero() && req.evalAuthority == "" {
		effect, err := unassentedEffect(&def.Spec, false, func(name string) (*schema.Spec, error) {
			called := req
			called.WorkflowName = name
			def, err := s.runtime.resolve(ctx, called, source)
			return &def.Spec, err
		}, 0)
		if err != nil {
			return PreparedRun{}, err
		}
		if effect != "" {
			if s.runtime.assent == nil {
				return PreparedRun{}, fmt.Errorf("workflow %q would %s with no approval first, and landing authority cannot be checked", req.WorkflowName, effect)
			}
			if err := s.runtime.assent(ctx, req, effect); err != nil {
				return PreparedRun{}, err
			}
		}
	}
	return def, nil
}

// Readiness commits the handoff obligation. Caller cancellation no longer
// owns this daemon-local act; dispatch can still fail if the daemon stops.
func (s *starter) dispatchPrepared(ctx context.Context, run *Run) error {
	if s.runtime.dispatch == nil {
		return fmt.Errorf("Run dispatch is unavailable")
	}
	if err := s.runtime.dispatch(ctx, run); err != nil {
		return fmt.Errorf("run %d is ready but dispatch needs retry: %w", run.ID, err)
	}
	return nil
}

// Coalesce preparation of the same run without serializing unrelated runs.
// Entries live only for the duration of preparation, not the run's lifetime.
func (s *starter) prepare(ctx context.Context, id ids.ID) error {
	return s.prepareExpected(ctx, id, nil)
}

// prepareScoped binds a caller-authorized Run before any workspace effect.
func (s *starter) prepareScoped(ctx context.Context, expected *Run) error {
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
		if s.runtime.prepare == nil {
			return fmt.Errorf("Run workspace preparation is unavailable")
		}
		if err := s.runtime.prepare(ctx, run); err != nil {
			receiptErr := s.records.preparationFailed(ctx, id, err.Error())
			if receiptErr != nil {
				return errors.Join(err, fmt.Errorf("record workspace failure: %w", receiptErr))
			}
			return err
		}
	}
	return s.records.prepared(ctx, id)
}
