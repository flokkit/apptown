package workflow

import (
	"context"
	"fmt"

	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/database"
)

// mergeEffects supplies external Git/provider work and observes its finished result.
// Neither method runs with a Work SQL connection held.
type mergeEffects interface {
	Driver() scm.Driver
	Observed(context.Context, scm.Result)
}

type Merges struct {
	db      *database.DB
	dataDir string
	rules   LawRules
	service *scm.Service
	effects mergeEffects
	evals   evalGate
	clocked
}

func NewMerges(db *database.DB, dataDir string, rules LawRules, service *scm.Service, effects mergeEffects) *Merges {
	return &Merges{db: db, dataDir: dataDir, rules: rules, service: service, effects: effects}
}

var _ advanceChecks = (*Merges)(nil)

func (m *Merges) RefreshLaw(ctx context.Context, run *Run) error {
	laws := newLaws(m.db, m.rules)
	laws.records.clocked = m.clocked
	return laws.refresh(ctx, run)
}

// Execute submits an already-admitted advancement/assertion observation. Its input
// is checked by Work and again by SCM before any new external effect.
func (m *Merges) Execute(ctx context.Context, call MergeCall) (MergeObservation, error) {
	out, _, err := m.execute(ctx, call)
	return out, err
}

func (m *Merges) Merge(ctx context.Context, run *Run) (MergeResult, error) {
	if run == nil {
		return MergeResult{}, errMergeCaller
	}
	snapshot := *run
	run = &snapshot
	source, err := readMergeSourceForRun(ctx, m.db, run)
	if err != nil {
		return MergeResult{}, err
	}
	observation, admitted, err := m.execute(ctx, MergeCall{Purpose: MergeForAdvance, Run: *run, Source: source})
	if err != nil {
		return MergeResult{}, err
	}
	if observation.Refusal != "" {
		if observation.Blocking != nil {
			return MergeResult{Blocking: observation.Blocking}, nil
		}
		gate, err := (mergeGateStore{m.db, m.clocked}).prepare(ctx, run)
		if err != nil {
			return MergeResult{}, err
		}
		step, err := gate.block(ctx, MergeEvidence{Reason: observation.Refusal})
		return MergeResult{Blocking: step}, err
	}
	if !observation.Merged {
		step, err := admitted.gate.block(ctx, MergeBlockEvidence(observation.Conflicts, observation.Drift))
		return MergeResult{Blocking: step}, err
	}
	if admitted.completing {
		if err := admitted.gate.clear(ctx); err != nil {
			return MergeResult{}, err
		}
	}
	return MergeResult{Allowed: true}, nil
}

// Work mints a claim from its exact admitted call. SCM checks it again after
// acquiring repository serialization and immediately before a new effect.
func (m *Merges) execute(ctx context.Context, call MergeCall) (MergeObservation, *admittedMerge, error) {
	if m.effects == nil || m.service == nil {
		return MergeObservation{}, nil, fmt.Errorf("merge runtime is unavailable")
	}
	admission := NewMergeAdmission(m.db, m.rules).withEvalGate(m.evals)
	admission.clocked = m.clocked
	permission, err := admission.Prepare(ctx, call)
	if err != nil {
		return MergeObservation{}, nil, err
	}
	admitted := permission.Admitted
	if admitted == nil {
		return MergeObservation{Refusal: permission.Reason, Blocking: permission.Blocking}, nil, nil
	}
	ctx, err = admitted.Context(ctx, m.dataDir)
	if err != nil {
		return MergeObservation{}, nil, err
	}
	result, err := m.service.Merge(ctx, call.Run.ID, m.effects.Driver())
	if err != nil {
		return MergeObservation{}, admitted, err
	}
	observed := MergeObservation{Merged: result.Merged, Conflicts: result.Conflicts, Drift: result.Drift, Refusal: result.Refusal.Reason}
	if !result.Refusal.CheckID.IsZero() {
		observed.Blocking, err = readStep(ctx, m.db, result.Refusal.CheckID)
		if err != nil {
			return MergeObservation{}, admitted, err
		}
	}
	m.effects.Observed(ctx, result)
	return observed, admitted, nil
}

// ObserveMerge keeps assertion-to-merge admission inside Work. The assertion
// already detached its source and step bytes before releasing the transaction.
func (m *Merges) ObserveMerge(ctx context.Context, in mergeAssertion) (MergeObservation, error) {
	return m.Execute(ctx, MergeCall{Purpose: mergeForAssertion, Run: in.Run, Step: in.Step, Source: in.Source})
}

// ExecuteMerge runs one admitted merge call through the SCM driver and
// reports what was observed.
func (s *Service) ExecuteMerge(ctx context.Context, call MergeCall) (MergeObservation, error) {
	if s.merges == nil {
		return MergeObservation{}, fmt.Errorf("merging is unavailable")
	}
	return s.merges.Execute(ctx, call)
}

// InspectPendingMerges settles merges an earlier process admitted but never
// recorded, by asking the driver what happened; it never replays one.
func (s *Service) InspectPendingMerges(ctx context.Context, driver scm.Driver) (scm.Summary, error) {
	if s.scmService == nil {
		return scm.Summary{}, fmt.Errorf("merging is unavailable")
	}
	return s.scmService.InspectPending(ctx, driver)
}
