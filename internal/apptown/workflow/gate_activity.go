package workflow

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/database"
)

// judgedByGate asks the stage's own gate before activity says "ready to
// advance" or "waiting on you". Every step can have passed while the gate
// still refuses: a check it requires was never recorded, or a check it adds is
// failing; and an approval asked for then could never move the run. Step
// results alone would show such a run as ready while every attempt to advance
// it is refused, the reason only in its event log.
func judgedByGate(a Activity, stage *schema.Stage, checks []Check, steps []*Step) Activity {
	if (a.State != ActivityReady && a.State != ActivityWaiting) || stage == nil {
		return a
	}
	gate, err := evaluateGate(stage, checks, steps)
	if err != nil {
		return Activity{State: ActivityBlocked, Detail: "AppTown cannot judge this stage: " + err.Error()}
	}
	if len(gate.Missing) > 0 {
		missing := make([]string, 0, len(gate.Missing))
		for _, m := range gate.Missing {
			// "scan-source (why)" reads as "scan-source: why" in a sentence.
			if name, why, ok := strings.Cut(m, " ("); ok {
				m = name + ": " + strings.TrimSuffix(why, ")")
			}
			missing = append(missing, m)
		}
		return Activity{State: ActivityBlocked, Detail: "The “" + stage.Name + "” stage is missing evidence. " + strings.Join(missing, "; ") + ". Run its checks again.", MissingEvidence: true}
	}
	// A person's approval still to come is what "waiting on you" already says.
	if a.State == ActivityWaiting || len(gate.Blocking) == 0 {
		return a
	}
	names := make([]string, 0, len(gate.Blocking))
	for _, s := range gate.Blocking {
		names = append(names, s.Name+" ("+s.Status.String()+")")
	}
	return Activity{State: ActivityBlocked, Detail: strings.Join(names, ", ")}
}

// judgedByGateInGraph judges a run from the activity read's own columns: the
// pinned declaration of its current stage and that stage's checks receipt.
func (g workflowActivityGraph) judgedByGate(id ids.ID, a Activity) Activity {
	run := g[id]
	if run == nil || run.stage == nil {
		return a
	}
	steps := make([]*Step, 0, len(run.steps))
	for _, s := range run.steps {
		steps = append(steps, &Step{ID: s.id, Name: s.name, Type: s.kind, Status: s.state, OverrideBy: s.override, Evidence: s.evidence})
	}
	return judgedByGate(a, run.stage, run.checks, steps)
}

// judgedByGateInSnapshot judges a detail view's run from the steps it already
// read; only a run waiting at its gate costs one more read, of the receipt.
func judgedByGateInSnapshot(ctx context.Context, q database.Queryer, run *Run, steps []*Step, a Activity) (Activity, error) {
	if a.State != ActivityReady && a.State != ActivityWaiting {
		return a, nil
	}
	_, stage, err := run.currentStage()
	if err != nil {
		return Activity{State: ActivityBlocked, Detail: "AppTown cannot judge this stage: " + err.Error()}, nil
	}
	checks, err := readStageChecks(ctx, q, run.ID, run.CurrentStage)
	if err != nil {
		return Activity{}, err
	}
	var current []*Step
	for _, s := range steps {
		if s.Stage == run.CurrentStage {
			current = append(current, s)
		}
	}
	return judgedByGate(a, stage, checks, current), nil
}

// stageFacts decodes the activity row's stage columns; null means none.
func stageFacts(declaration, checks json.RawMessage) (*schema.Stage, []Check, error) {
	if len(declaration) == 0 || string(declaration) == "null" {
		return nil, nil, nil
	}
	var stage schema.Stage
	if err := json.Unmarshal(declaration, &stage); err != nil {
		return nil, nil, err
	}
	var recorded []Check
	if len(checks) > 0 && string(checks) != "null" {
		if err := json.Unmarshal(checks, &recorded); err != nil {
			return nil, nil, err
		}
	}
	return &stage, recorded, nil
}
