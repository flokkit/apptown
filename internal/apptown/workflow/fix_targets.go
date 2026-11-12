package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/fix"
)

// fixableTargets observes settled named failures in this execution's stage.
// Scanner errors cannot supply targets. Retained attempt targets survive purge.
func (e *Execution) fixableTargets(ctx context.Context, names []string, ds []fix.Declaration) ([]check.Finding, string, error) {
	steps, err := readStageSteps(ctx, e.service.db, e.run.ID, e.step.Stage)
	if err != nil {
		return nil, "", err
	}
	observations, err := conformance.ScanObservationsIn(ctx, e.service.db, e.run.ProjectID)
	if err != nil {
		return nil, "", err
	}
	history, err := workflowsql.New(e.service.db.SQL()).PreviousHeldAttempt(ctx, workflowsql.PreviousHeldAttemptParams{ProjectID: e.run.ProjectID, ChangeID: e.run.ChangeID})
	if err != nil {
		return nil, "", err
	}
	previous := map[string]bool{}
	if len(history) > 0 {
		var ts []check.Finding
		if err := json.Unmarshal(history[0].Targets, &ts); err != nil {
			return nil, "", err
		}
		for _, t := range ts {
			previous[t.Fingerprint] = true
		}
	}
	stopped := false
	current, err := workflowsql.New(e.service.db.SQL()).QueueAttempt(ctx, e.run.ChangeID)
	if err != nil {
		return nil, "", err
	}
	if len(current) > 0 {
		state, err := queueStateIn(ctx, e.service.db, e.run.ProjectID, current[0].Queue)
		if err != nil {
			return nil, "", err
		}
		stopped = state.Stopped()
	}
	seen := map[string]bool{}
	var targets []check.Finding
	for _, name := range names {
		found := false
		for _, s := range steps {
			if s.Name != name && !strings.HasPrefix(s.Name, name+":") {
				continue
			}
			found = true
			if s.Status == StatusPending || s.Status == StatusRunning {
				return nil, "", fmt.Errorf("step %s has not settled", s.Name)
			}
			if s.Status != StatusFail {
				continue
			}
			var cfg struct {
				FailOn string `json:"failOn"`
			}
			if err := json.Unmarshal(s.Config, &cfg); err != nil {
				return nil, "", err
			}
			for _, o := range observations {
				if o.RunID != e.run.ID || o.StepID != s.ID || o.Attempt != s.Attempt || o.Subject.On != "source" {
					continue
				}
				for _, f := range o.Findings {
					if f.Package == nil || previous[f.Fingerprint] || seen[f.Fingerprint] || stopped && !check.FailsAt(cfg.FailOn, f.Severity) {
						continue
					}
					if slices.ContainsFunc(ds, func(d fix.Declaration) bool { return fix.SupportsTargets(d) && d.Ecosystem == f.Package.Ecosystem }) {
						targets = append(targets, f.Finding)
						seen[f.Fingerprint] = true
					}
				}
			}
		}
		if !found {
			return nil, "", fmt.Errorf("whenFailed names unknown step %s", name)
		}
	}
	if len(targets) == 0 {
		if len(history) > 0 {
			return nil, "nothing new since " + history[0].OpenedAt.Format("2 Jan"), nil
		}
		return nil, "no failed step has a fixable finding", nil
	}
	return targets, "", nil
}
